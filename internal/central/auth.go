package central

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
)

// The login in front of /admin/. Until this, admin had no login of its own
// and nginx was the only lock (decision D4); now the application has
// accounts, and nginx's allow-list stays in front as a second lock where it
// is configured.
//
//	GET|POST <base>/admin/setup               the FIRST account; only while there is none
//	GET|POST <base>/admin/login               username, password, "keep me logged in"
//	POST     <base>/admin/logout
//	GET      <base>/admin/users               the accounts
//	POST     <base>/admin/users               add one
//	POST     <base>/admin/users/{id}/password set a new password (ends their sessions)
//	POST     <base>/admin/users/{id}/delete   type the username to confirm; not yourself
//	POST     <base>/admin/account/password    your own: the current one, then the new one
//	POST     <base>/admin/account/sessions    log out everywhere else
//
// THE SETUP PAGE is open to whoever reaches it first while the database has
// no account. That is the moment between installing and opening admin, and
// an allow-list in the reverse proxy can keep it to the operator. Do not
// publish a fresh central server without either that, or opening /admin/
// yourself first.
//
// A session is a random cookie (32 bytes) stored as its SHA-256. "Keep me
// logged in" makes a cookie that outlives the browser and a session that
// lasts 30 days from its last use; without it, the cookie ends with the
// browser and the session after 12 hours without use. The cookie is
// HttpOnly, SameSite=Lax, Secure behind HTTPS, and scoped to <base>/admin/,
// so the public boards never see it. Every form still carries the CSRF
// token of admin.go: the cookie alone authorises no POST.

const (
	sessionCookie = "ctf_session"
	// SessionRemember is how long "keep me logged in" lasts after the last use.
	SessionRemember = 30 * 24 * time.Hour
	// SessionPlain is how long a session without it lasts after the last use.
	SessionPlain = 12 * time.Hour
	// touchEvery limits the writes for "last used" to one per session per
	// ten minutes; the board's admin pages reload often.
	touchEvery = 10 * time.Minute

	// The login limits: failures per address and per username in a window
	// of loginWindow. Per address alone would let one school lock out its
	// teacher; per username alone would let a guesser try every name.
	loginWindow          = 15 * time.Minute
	LoginFailsPerAddress = 20
	LoginFailsPerUser    = 10
)

type userKey struct{}

type authInfo struct {
	user    *store.User
	session *store.Session
}

// currentUser is the user of an authorised request; nil outside requireUser.
func currentUser(r *http.Request) *store.User {
	if a, ok := r.Context().Value(userKey{}).(*authInfo); ok {
		return a.user
	}
	return nil
}

func currentSession(r *http.Request) *store.Session {
	if a, ok := r.Context().Value(userKey{}).(*authInfo); ok {
		return a.session
	}
	return nil
}

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) adminPath(p string) string { return s.cfg.BasePath + "/admin" + p }

func (s *Server) authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+s.adminPath("/setup"), s.handleSetupPage)
	mux.HandleFunc("POST "+s.adminPath("/setup"), s.csrf(s.handleSetup))
	mux.HandleFunc("GET "+s.adminPath("/login"), s.handleLoginPage)
	mux.HandleFunc("POST "+s.adminPath("/login"), s.csrf(s.handleLogin))
	mux.HandleFunc("POST "+s.adminPath("/logout"), s.action(s.handleLogout))
	mux.HandleFunc("GET "+s.adminPath("/users"), s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		s.usersPage(w, r, http.StatusOK, flash{})
	}))
	mux.HandleFunc("POST "+s.adminPath("/users"), s.action(s.handleAddUser))
	mux.HandleFunc("POST "+s.adminPath("/users/{id}/{action}"), s.action(s.handleUserAction))
	mux.HandleFunc("POST "+s.adminPath("/account/{action}"), s.action(s.handleAccount))
}

// action is a POST in admin: the CSRF token, then a logged-in user.
func (s *Server) action(h http.HandlerFunc) http.HandlerFunc { return s.csrf(s.requireUser(h)) }

// requireUser lets a request through with a live session, and otherwise
// sends the browser to the setup page (no account yet) or the login page,
// remembering where it was going.
func (s *Server) requireUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := s.st.UserCount()
		if err != nil {
			s.fail(w, err)
			return
		}
		if n == 0 {
			http.Redirect(w, r, s.adminPath("/setup"), http.StatusSeeOther)
			return
		}
		sess, u := s.session(w, r)
		if u == nil {
			to := s.adminPath("/login")
			if r.Method == http.MethodGet {
				to += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey{}, &authInfo{user: u, session: sess})))
	}
}

// session finds the request's session, and keeps a used one alive.
func (s *Server) session(w http.ResponseWriter, r *http.Request) (*store.Session, *store.User) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || len(c.Value) != 64 {
		return nil, nil
	}
	now := s.now()
	sess, u, err := s.st.SessionUser(protocol.Hash(c.Value), now)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("reading a session", "err", err)
		}
		return nil, nil
	}
	if now.Sub(sess.LastSeenAt) >= touchEvery {
		life := SessionPlain
		if sess.Remember {
			life = SessionRemember
		}
		sess.LastSeenAt, sess.ExpiresAt = now, now.Add(life)
		if err := s.st.TouchSession(sess.TokenHash, now, sess.ExpiresAt); err != nil {
			s.log.Error("touching a session", "err", err)
		}
		if sess.Remember {
			s.setSessionCookie(w, r, c.Value, sess.ExpiresAt)
		}
	}
	return sess, u
}

// setSessionCookie sets the cookie; a zero expiry makes it end with the browser.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	c := &http.Cookie{Name: sessionCookie, Value: token, Path: s.adminPath("/"),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure(r)}
	if !expires.IsZero() {
		c.Expires = expires
		c.MaxAge = int(expires.Sub(s.now()).Seconds())
	}
	http.SetCookie(w, c)
}

// login starts a session for u and sends the browser on.
func (s *Server) login(w http.ResponseWriter, r *http.Request, u *store.User, remember bool, next string) {
	now := s.now()
	token := httpx.RandomHex(32)
	life := SessionPlain
	if remember {
		life = SessionRemember
	}
	err := s.st.CreateSession(store.Session{TokenHash: protocol.Hash(token), UserID: u.ID, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(life), Remember: remember, Address: httpx.ClientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		s.fail(w, err)
		return
	}
	var cookieExpiry time.Time
	if remember {
		cookieExpiry = now.Add(life)
	}
	s.setSessionCookie(w, r, token, cookieExpiry)
	http.Redirect(w, r, s.safeNext(next), http.StatusSeeOther)
}

// safeNext keeps the redirect after a login inside admin: anything else
// (another site, a scheme-relative //host, a backslash trick) goes home.
func (s *Server) safeNext(next string) string {
	home := s.adminPath("/")
	if !strings.HasPrefix(next, home) || strings.Contains(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return home
	}
	return next
}

func (s *Server) authEvent(r *http.Request, actor, text string) {
	s.event(store.Event{Kind: store.EvAuth, Actor: actor, Address: httpx.ClientIP(r), Text: text})
}

// ---- setup -------------------------------------------------------------------

type authPageData struct {
	pageBase
	Error    string
	Message  string
	Username string
	Next     string
	Remember bool
}

func (s *Server) noUsers(w http.ResponseWriter) (bool, bool) {
	n, err := s.st.UserCount()
	if err != nil {
		s.fail(w, err)
		return false, false
	}
	return n == 0, true
}

func (s *Server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	none, ok := s.noUsers(w)
	if !ok {
		return
	}
	if !none {
		http.Redirect(w, r, s.adminPath("/login"), http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "setup", authPageData{pageBase: s.abase(w, r, nil), Remember: true})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	name, pw := strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("password")
	again := func(msg string) {
		s.render(w, http.StatusBadRequest, "setup", authPageData{pageBase: s.abase(w, r, nil), Error: msg, Username: name, Remember: true})
	}
	if _, err := store.ValidUsername(name); err != nil {
		again(err.Error())
		return
	}
	if err := checkPassword(pw, name); err != nil {
		again(err.Error())
		return
	}
	if pw != r.PostFormValue("password2") {
		again("The two passwords are not the same.")
		return
	}
	hash, err := hashPassword(pw)
	if err != nil {
		s.fail(w, err)
		return
	}
	u, err := s.st.CreateFirstUser(name, hash, s.now())
	if errors.Is(err, store.ErrConflict) {
		// Someone else finished the setup first: their account stands.
		http.Redirect(w, r, s.adminPath("/login"), http.StatusSeeOther)
		return
	}
	if err != nil {
		again(err.Error())
		return
	}
	s.authEvent(r, u.Username, "first admin account created on the setup page: "+u.Username)
	s.login(w, r, u, true, "")
}

// ---- login and logout ----------------------------------------------------------

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	none, ok := s.noUsers(w)
	if !ok {
		return
	}
	if none {
		http.Redirect(w, r, s.adminPath("/setup"), http.StatusSeeOther)
		return
	}
	if _, u := s.session(w, r); u != nil {
		http.Redirect(w, r, s.safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	d := authPageData{pageBase: s.abase(w, r, nil), Next: r.URL.Query().Get("next"), Remember: true}
	if r.URL.Query().Has("bye") {
		d.Message = "You are logged out."
	}
	s.render(w, http.StatusOK, "login", d)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	addr := httpx.ClientIP(r)
	name, pw := strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("password")
	remember := r.PostFormValue("remember") == "on"
	next := r.PostFormValue("next")
	ka, ku := "a:"+addr, "u:"+strings.ToLower(name)
	again := func(status int, msg string) {
		s.render(w, status, "login", authPageData{pageBase: s.abase(w, r, nil), Error: msg, Username: name, Next: next, Remember: remember})
	}
	// The limit is checked BEFORE the password, so a locked-out guesser
	// learns nothing more and costs no hashing.
	if s.loginFails.Count(ka, now) >= LoginFailsPerAddress || s.loginFails.Count(ku, now) >= LoginFailsPerUser {
		again(http.StatusTooManyRequests, fmt.Sprintf("Too many failed logins. Wait %d minutes and try again.", int(loginWindow.Minutes())))
		return
	}
	u, err := s.st.UserByName(name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		verifyNobody(pw)
	case err != nil:
		s.fail(w, err)
		return
	}
	if u == nil || !verifyPassword(u.PasswordHash, pw) {
		s.loginFails.Add(ka, now)
		s.loginFails.Add(ku, now)
		shown := name
		if len(shown) > 40 {
			shown = shown[:40] + "…"
		}
		s.authEvent(r, "", fmt.Sprintf("failed login as %q", shown))
		again(http.StatusUnauthorized, "Wrong username or password.")
		return
	}
	how := "logged in"
	if remember {
		how += ", kept logged in for 30 days"
	}
	s.authEvent(r, u.Username, how)
	s.login(w, r, u, remember, next)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sess := currentSession(r); sess != nil {
		if err := s.st.DeleteSession(sess.TokenHash); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.authEvent(r, currentUser(r).Username, "logged out")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: s.adminPath("/"), MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure(r)})
	http.Redirect(w, r, s.adminPath("/login?bye=1"), http.StatusSeeOther)
}

// ---- users -------------------------------------------------------------------------

type usersPageData struct {
	pageBase
	Flash flash
	Users []store.User
	MinPw int
}

func (s *Server) usersPage(w http.ResponseWriter, r *http.Request, status int, f flash) {
	us, err := s.st.Users(s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, status, "admin-users", usersPageData{pageBase: s.abase(w, r, nil), Flash: f, Users: us, MinPw: MinPasswordLen})
}

// newPassword reads and checks the "password" and "password2" fields.
func newPassword(r *http.Request, username string) (string, error) {
	pw := r.PostFormValue("password")
	if err := checkPassword(pw, username); err != nil {
		return "", err
	}
	if pw != r.PostFormValue("password2") {
		return "", errors.New("the two passwords are not the same")
	}
	return hashPassword(pw)
}

func sentence(err error) string {
	m := err.Error()
	if m == "" {
		return m
	}
	return strings.ToUpper(m[:1]) + m[1:] + "."
}

func (s *Server) handleAddUser(w http.ResponseWriter, r *http.Request) {
	name, err := store.ValidUsername(r.PostFormValue("username"))
	if err != nil {
		s.usersPage(w, r, http.StatusBadRequest, flash{Error: sentence(err)})
		return
	}
	hash, err := newPassword(r, name)
	if err != nil {
		s.usersPage(w, r, http.StatusBadRequest, flash{Error: sentence(err)})
		return
	}
	u, err := s.st.CreateUser(name, hash, currentUser(r).Username, s.now())
	if err != nil {
		s.usersPage(w, r, http.StatusBadRequest, flash{Error: sentence(err)})
		return
	}
	s.admin(r, store.Event{Text: "admin account added: " + u.Username})
	s.usersPage(w, r, http.StatusOK, flash{Message: u.Username + " can now log in. Give them the password in person, not by e-mail."})
}

func (s *Server) handleUserAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, err := s.st.User(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	me := currentUser(r)
	switch r.PathValue("action") {
	case "password":
		if u.ID == me.ID {
			s.usersPage(w, r, http.StatusBadRequest, flash{Error: "Change your own password under “Your account”, with your current one."})
			return
		}
		hash, err := newPassword(r, u.Username)
		if err != nil {
			s.usersPage(w, r, http.StatusBadRequest, flash{Error: sentence(err)})
			return
		}
		if err := s.st.SetPassword(u.ID, hash, s.now(), ""); err != nil {
			s.fail(w, err)
			return
		}
		s.admin(r, store.Event{Text: "new password set for " + u.Username + "; their sessions ended"})
		s.usersPage(w, r, http.StatusOK, flash{Message: "New password for " + u.Username + ". They are logged out everywhere."})
	case "delete":
		if u.ID == me.ID {
			s.usersPage(w, r, http.StatusBadRequest, flash{Error: "You cannot delete your own account. Ask another admin."})
			return
		}
		if strings.TrimSpace(r.PostFormValue("confirm")) != u.Username {
			s.usersPage(w, r, http.StatusBadRequest, flash{Error: "Type the username exactly to delete " + u.Username + "; nothing was changed."})
			return
		}
		if err := s.st.DeleteUser(u.ID); err != nil {
			s.fail(w, err)
			return
		}
		s.admin(r, store.Event{Text: "admin account deleted: " + u.Username})
		s.usersPage(w, r, http.StatusOK, flash{Message: u.Username + " is deleted and logged out everywhere."})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	me, sess := currentUser(r), currentSession(r)
	switch r.PathValue("action") {
	case "password":
		if !verifyPassword(me.PasswordHash, r.PostFormValue("current")) {
			s.authEvent(r, me.Username, "password change refused: wrong current password")
			s.usersPage(w, r, http.StatusBadRequest, flash{Error: "Your current password is not right; nothing was changed."})
			return
		}
		hash, err := newPassword(r, me.Username)
		if err != nil {
			s.usersPage(w, r, http.StatusBadRequest, flash{Error: sentence(err)})
			return
		}
		if err := s.st.SetPassword(me.ID, hash, s.now(), sess.TokenHash); err != nil {
			s.fail(w, err)
			return
		}
		s.authEvent(r, me.Username, "changed their password; their other sessions ended")
		s.usersPage(w, r, http.StatusOK, flash{Message: "Your password is changed. You stay logged in here; everywhere else you are logged out."})
	case "sessions":
		n, err := s.st.DeleteSessions(me.ID, sess.TokenHash)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.authEvent(r, me.Username, fmt.Sprintf("logged out %d other session(s)", n))
		s.usersPage(w, r, http.StatusOK, flash{Message: fmt.Sprintf("Logged out everywhere else (%d session(s)).", n)})
	default:
		http.NotFound(w, r)
	}
}
