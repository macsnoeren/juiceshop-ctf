// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package central

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// The tests hash with far fewer iterations than production: the cost is not
// what they test, and 600 000 per login would make every admin test slow.
func init() { passwordIterations = 1000 }

// form posts a form with the CSRF token of page (any admin page this client
// can open, or the login or setup page).
func (e *env) form(page, path string, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	m := csrfRE.FindStringSubmatch(e.get(page).Body.String())
	if m == nil {
		e.t.Fatalf("no CSRF token on %s", page)
	}
	form.Set("csrf", m[1])
	return e.req("POST", path, strings.NewReader(form.Encode()), "Content-Type", "application/x-www-form-urlencoded")
}

func (e *env) login(user, pw string, remember bool, next string) *httptest.ResponseRecorder {
	e.t.Helper()
	f := url.Values{"username": {user}, "password": {pw}, "next": {next}}
	if remember {
		f.Set("remember", "on")
	}
	return e.form(base+"/admin/login", base+"/admin/login", f)
}

func cookieOf(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func location(w *httptest.ResponseRecorder) string { return w.Header().Get("Location") }

func TestPasswordHash(t *testing.T) {
	h, err := hashPassword("a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$1000$") || strings.Contains(h, "a long enough password") {
		t.Errorf("hash %q", h)
	}
	if !verifyPassword(h, "a long enough password") || verifyPassword(h, "a long enough passworD") || verifyPassword(h, "") {
		t.Error("verify does not tell the right password from a wrong one")
	}
	h2, _ := hashPassword("a long enough password")
	if h == h2 {
		t.Error("two hashes of one password are equal: no salt")
	}
	for _, bad := range []string{"", "x", "pbkdf2-sha256$0$AA$AA", "md5$1$AA$AA", "pbkdf2-sha256$1000$!!$AA"} {
		if verifyPassword(bad, "x") {
			t.Errorf("%q verifies", bad)
		}
	}
	for pw, ok := range map[string]bool{"short": false, "teacher-ok": true, "Teacher": false, strings.Repeat("x", 201): false} {
		if err := checkPassword(pw, "teacher"); (err == nil) != ok {
			t.Errorf("checkPassword(%q) = %v", pw, err)
		}
	}
	if err := checkPassword("Teacher123", "teacher123"); err == nil {
		t.Error("the username as password is accepted")
	}
}

func TestSetup(t *testing.T) {
	e := newBareEnv(t)
	// With no account every admin page leads to the setup page.
	for _, p := range []string{"/admin/", "/admin/login", "/admin/games/1", "/admin/users", "/admin/events"} {
		if w := e.get(base + p); w.Code != http.StatusSeeOther || location(w) != base+"/admin/setup" {
			t.Errorf("%s: %d → %q", p, w.Code, location(w))
		}
	}
	// Bad input is refused and makes nothing.
	for _, f := range []url.Values{
		{"username": {"x"}, "password": {adminPassword}, "password2": {adminPassword}},
		{"username": {"teacher"}, "password": {"short"}, "password2": {"short"}},
		{"username": {"teacher"}, "password": {adminPassword}, "password2": {adminPassword + "!"}},
		{"username": {"teacher"}, "password": {"teacher   "}, "password2": {"teacher   "}},
	} {
		if w := e.form(base+"/admin/setup", base+"/admin/setup", f); w.Code != http.StatusBadRequest {
			t.Errorf("setup %v: %d", f, w.Code)
		}
	}
	if n, _ := e.st.UserCount(); n != 0 {
		t.Fatalf("%d users after refused setups", n)
	}

	w := e.setup(adminUser, adminPassword)
	if location(w) != base+"/admin/" {
		t.Errorf("after setup: %q", location(w))
	}
	c := cookieOf(w, sessionCookie)
	if c == nil || !c.HttpOnly || c.Path != base+"/admin/" || c.SameSite != http.SameSiteLaxMode || c.MaxAge < 29*24*3600 {
		t.Fatalf("session cookie %+v", c)
	}
	if e.get(base+"/admin/").Code != 200 {
		t.Error("the new admin is not logged in")
	}

	// Once there is an account, the setup page is closed: a second visitor
	// who had the form open cannot make a second "first" admin.
	other := newBareEnvOn(e)
	if w := other.get(base + "/admin/setup"); w.Code != http.StatusSeeOther || location(w) != base+"/admin/login" {
		t.Errorf("setup page with an account: %d → %q", w.Code, location(w))
	}
	if w := other.form(base+"/admin/login", base+"/admin/setup", url.Values{"username": {"mallory"}, "password": {"mallory's password"}, "password2": {"mallory's password"}}); w.Code != http.StatusSeeOther || location(w) != base+"/admin/login" {
		t.Errorf("late setup: %d → %q", w.Code, location(w))
	}
	if n, _ := e.st.UserCount(); n != 1 {
		t.Errorf("%d users after a late setup", n)
	}
	if other.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("the late setup logged its sender in")
	}
	if evs, _ := e.st.Events(store.EventFilter{Kind: store.EvAuth}); len(evs) != 1 || evs[0].Actor != adminUser {
		t.Errorf("auth events %+v", evs)
	}
}

// newBareEnvOn is another browser on e's server, with no cookies.
func newBareEnvOn(e *env) *env { return e.client() }

func TestAdminNeedsLogin(t *testing.T) {
	e := newEnv(t)
	id, _ := e.game("Class", nil, "running")
	anon := e.client()
	for _, p := range []string{"/admin/", "/admin/games/1", "/admin/games/1/export.csv", "/admin/events", "/admin/events.csv", "/admin/users"} {
		w := anon.get(base + p)
		if w.Code != http.StatusSeeOther || location(w) != base+"/admin/login?next="+url.QueryEscape(base+p) {
			t.Errorf("%s without a login: %d → %q", p, w.Code, location(w))
		}
		if strings.Contains(w.Body.String(), "Class") {
			t.Errorf("%s shows admin data without a login", p)
		}
	}
	// A POST with a valid CSRF token but no session does nothing.
	w := anon.form(base+"/admin/login", base+"/admin/games/1/clear", url.Values{"confirm": {"Class"}})
	if w.Code != http.StatusSeeOther || location(w) != base+"/admin/login" {
		t.Errorf("POST without a login: %d → %q", w.Code, location(w))
	}
	if g, _ := e.st.Game(id); g == nil {
		t.Fatal("the game is gone")
	}
	// A made-up cookie is no session.
	anon.jar[sessionCookie] = strings.Repeat("ab", 32)
	if anon.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("a made-up session cookie is accepted")
	}
	// The boards and the API stay public.
	if anon.get(base+"/").Code != 200 || anon.get(base+"/g/class/").Code != 200 {
		t.Error("the public pages need a login")
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	if w := c.login(adminUser, "wrong password!", true, ""); w.Code != http.StatusUnauthorized || cookieOf(w, sessionCookie) != nil {
		t.Errorf("wrong password: %d", w.Code)
	}
	if w := c.login("nobody", adminPassword, true, ""); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Wrong username or password.") {
		t.Errorf("unknown user: %d", w.Code)
	}
	// Right: on to where the browser was going, inside admin only.
	if w := c.login("TEACHER", adminPassword, false, base+"/admin/events?kind=auth"); w.Code != http.StatusSeeOther || location(w) != base+"/admin/events?kind=auth" {
		t.Errorf("login: %d → %q", w.Code, location(w))
	} else if ck := cookieOf(w, sessionCookie); ck == nil || ck.MaxAge != 0 || !ck.Expires.IsZero() {
		t.Errorf("without 'keep me logged in' the cookie outlives the browser: %+v", ck)
	}
	for _, next := range []string{"//evil.example/admin/", "https://evil.example/", base + "/g/x/", base + "/admin//evil", "/admin/"} {
		if w := e.client().login(adminUser, adminPassword, false, next); location(w) != base+"/admin/" {
			t.Errorf("next %q → %q", next, location(w))
		}
	}
	// A logged-in browser on the login page goes on.
	if w := c.get(base + "/admin/login"); w.Code != http.StatusSeeOther {
		t.Errorf("login page while logged in: %d", w.Code)
	}

	// Sessions are stored hashed: the cookie is not in the database.
	var n int
	e.st.UserCount()
	evs, _ := e.st.Events(store.EventFilter{Kind: store.EvAuth})
	for _, ev := range evs {
		if strings.Contains(ev.Text, c.jar[sessionCookie]) {
			t.Error("an event holds a session cookie")
		}
		if strings.Contains(ev.Text, "failed login") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d failed-login events, want 2", n)
	}
	if _, _, err := e.st.SessionUser(c.jar[sessionCookie], e.now); err == nil {
		t.Error("the raw cookie finds a session: it is stored as is")
	}
	if _, u, err := e.st.SessionUser(protocol.Hash(c.jar[sessionCookie]), e.now); err != nil || u.Username != adminUser {
		t.Errorf("the session is not stored by its hash: %v", err)
	}

	// Secure behind HTTPS.
	w := c.req("GET", base+"/admin/login", nil)
	if ck := cookieOf(w, adminCSRFCookie); ck != nil && ck.Secure {
		t.Error("Secure over plain HTTP")
	}
	f := url.Values{"username": {adminUser}, "password": {adminPassword}, "remember": {"on"}}
	h := e.client()
	m := csrfRE.FindStringSubmatch(h.get(base + "/admin/login").Body.String())
	f.Set("csrf", m[1])
	w = h.req("POST", base+"/admin/login", strings.NewReader(f.Encode()), "Content-Type", "application/x-www-form-urlencoded", "X-Forwarded-Proto", "https")
	if ck := cookieOf(w, sessionCookie); ck == nil || !ck.Secure {
		t.Errorf("no Secure session cookie behind HTTPS: %+v", ck)
	}
}

func TestLoginLimits(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	for i := 0; i < LoginFailsPerUser; i++ {
		c.login(adminUser, "wrong password!", false, "")
	}
	// Now even the right password is refused for this username...
	if w := c.login(adminUser, adminPassword, false, ""); w.Code != http.StatusTooManyRequests {
		t.Errorf("after %d failures: %d", LoginFailsPerUser, w.Code)
	}
	// ...until the window has passed.
	e.now = e.now.Add(loginWindow)
	if w := c.login(adminUser, adminPassword, false, ""); w.Code != http.StatusSeeOther {
		t.Errorf("after the window: %d", w.Code)
	}
	// Per address: many usernames from one place.
	d := e.client()
	for i := 0; i < LoginFailsPerAddress; i++ {
		d.login("guess"+string(rune('a'+i)), "wrong password!", false, "")
	}
	if w := d.login(adminUser, adminPassword, false, ""); w.Code != http.StatusTooManyRequests {
		t.Errorf("after %d failures from one address: %d", LoginFailsPerAddress, w.Code)
	}
}

func TestSessionsExpire(t *testing.T) {
	e := newEnv(t) // logged in by setup: kept for 30 days
	plain := e.client()
	plain.login(adminUser, adminPassword, false, "")

	e.now = e.now.Add(SessionPlain - time.Minute)
	if plain.get(base+"/admin/").Code != 200 {
		t.Fatal("a plain session ended early")
	}
	// Used just now, so it lasts another 12 hours from here.
	e.now = e.now.Add(SessionPlain - time.Minute)
	if plain.get(base+"/admin/").Code != 200 {
		t.Error("a used session did not move its expiry")
	}
	e.now = e.now.Add(SessionPlain + time.Minute)
	if plain.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("a plain session outlived 12 hours without use")
	}
	// "Keep me logged in": a month without use is the limit.
	if e.get(base+"/admin/").Code != 200 {
		t.Error("the remembered session ended within a day")
	}
	e.now = e.now.Add(SessionRemember - time.Hour)
	w := e.get(base + "/admin/")
	if w.Code != 200 {
		t.Fatal("the remembered session ended within 30 days")
	}
	if ck := cookieOf(w, sessionCookie); ck == nil || ck.MaxAge < 29*24*3600 {
		t.Errorf("the remembered cookie was not renewed on use: %+v", ck)
	}
	e.now = e.now.Add(SessionRemember + time.Hour)
	if e.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("a remembered session outlived 30 days without use")
	}
	if _, err := e.st.Prune(e.now); err != nil {
		t.Fatal(err)
	}
	us, _ := e.st.Users(e.now)
	if us[0].Sessions != 0 {
		t.Errorf("%d live sessions after expiry", us[0].Sessions)
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	old := e.jar[sessionCookie]
	w := e.form(base+"/admin/", base+"/admin/logout", url.Values{})
	if w.Code != http.StatusSeeOther || location(w) != base+"/admin/login?bye=1" {
		t.Errorf("logout: %d → %q", w.Code, location(w))
	}
	if e.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("still logged in after logout")
	}
	e.jar[sessionCookie] = old
	if e.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("the old cookie still works after logout")
	}
}

func TestUsers(t *testing.T) {
	e := newEnv(t)
	add := func(f url.Values) *httptest.ResponseRecorder {
		return e.form(base+"/admin/users", base+"/admin/users", f)
	}
	pw := "anna's password"
	if w := add(url.Values{"username": {"anna"}, "password": {pw}, "password2": {pw}}); w.Code != 200 || !strings.Contains(w.Body.String(), "anna can now log in") {
		t.Fatalf("add: %d", w.Code)
	}
	for _, f := range []url.Values{
		{"username": {"ANNA"}, "password": {pw}, "password2": {pw}}, // taken, whatever the case
		{"username": {"bo b"}, "password": {pw}, "password2": {pw}}, // a space
		{"username": {"bob"}, "password": {"short"}, "password2": {"short"}},
		{"username": {"bob"}, "password": {pw}, "password2": {"other " + pw}},
	} {
		if w := add(f); w.Code != http.StatusBadRequest {
			t.Errorf("add %v: %d", f, w.Code)
		}
	}
	anna, _ := e.st.UserByName("anna")
	if anna.CreatedBy != adminUser || strings.Contains(anna.PasswordHash, pw) {
		t.Errorf("anna %+v", anna)
	}
	a := e.client()
	if w := a.login("anna", pw, true, ""); w.Code != http.StatusSeeOther {
		t.Fatalf("anna cannot log in: %d", w.Code)
	}
	// Anna's action is hers in the feed.
	a.game("Anna's game", nil, "")
	evs, _ := e.st.Events(store.EventFilter{Kind: store.EvAdmin})
	if len(evs) == 0 || evs[0].Actor != "anna" || evs[0].Address == "" {
		t.Errorf("the game's event is not Anna's: %+v", evs[0])
	}

	// A new password for Anna logs her out everywhere.
	np := "anna's new password"
	if w := e.form(base+"/admin/users", base+"/admin/users/"+itoa(anna.ID)+"/password", url.Values{"password": {np}, "password2": {np}}); w.Code != 200 {
		t.Fatalf("reset: %d", w.Code)
	}
	if a.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("Anna is still logged in after her password was reset")
	}
	if a.login("anna", pw, false, "").Code != http.StatusUnauthorized || a.login("anna", np, false, "").Code != http.StatusSeeOther {
		t.Error("the reset password does not work, or the old one still does")
	}

	// Your own password goes through "Your account", with the current one.
	me, _ := e.st.UserByName(adminUser)
	if w := e.form(base+"/admin/users", base+"/admin/users/"+itoa(me.ID)+"/password", url.Values{"password": {np}, "password2": {np}}); w.Code != http.StatusBadRequest {
		t.Errorf("reset of one's own password: %d", w.Code)
	}
	other := e.client()
	other.login(adminUser, adminPassword, true, "")
	mine := "teacher's new password"
	if w := e.form(base+"/admin/users", base+"/admin/account/password", url.Values{"current": {"wrong"}, "password": {mine}, "password2": {mine}}); w.Code != http.StatusBadRequest {
		t.Errorf("own change with a wrong current password: %d", w.Code)
	}
	if w := e.form(base+"/admin/users", base+"/admin/account/password", url.Values{"current": {adminPassword}, "password": {mine}, "password2": {mine}}); w.Code != 200 {
		t.Fatalf("own change: %d", w.Code)
	}
	if e.get(base+"/admin/").Code != 200 || other.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("changing my password must keep this session and end the others")
	}

	// Log out everywhere else.
	other.login(adminUser, mine, true, "")
	if w := e.form(base+"/admin/users", base+"/admin/account/sessions", url.Values{}); w.Code != 200 || !strings.Contains(w.Body.String(), "(1 session(s))") {
		t.Errorf("log out everywhere else: %d", w.Code)
	}
	if e.get(base+"/admin/").Code != 200 || other.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("log out everywhere else ended the wrong session")
	}

	// Delete: not yourself, and only with the name typed.
	if w := e.form(base+"/admin/users", base+"/admin/users/"+itoa(me.ID)+"/delete", url.Values{"confirm": {adminUser}}); w.Code != http.StatusBadRequest {
		t.Errorf("deleting oneself: %d", w.Code)
	}
	if a.get(base+"/admin/").Code != 200 {
		t.Fatal("Anna is not logged in before her account is deleted")
	}
	if w := e.form(base+"/admin/users", base+"/admin/users/"+itoa(anna.ID)+"/delete", url.Values{"confirm": {"ann"}}); w.Code != http.StatusBadRequest {
		t.Errorf("delete without the name: %d", w.Code)
	}
	if w := e.form(base+"/admin/users", base+"/admin/users/"+itoa(anna.ID)+"/delete", url.Values{"confirm": {"anna"}}); w.Code != 200 {
		t.Errorf("delete: %d", w.Code)
	}
	if a.get(base+"/admin/").Code != http.StatusSeeOther {
		t.Error("a deleted user is still logged in")
	}
	if us, _ := e.st.Users(e.now); len(us) != 1 {
		t.Errorf("%d users left", len(us))
	}
	// Every form on the users page carries the CSRF token.
	body := e.get(base + "/admin/users").Body.String()
	if forms, tokens := strings.Count(body, `method="post"`), strings.Count(body, `name="csrf"`); forms == 0 || forms != tokens {
		t.Errorf("users page: %d forms, %d CSRF tokens", forms, tokens)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
