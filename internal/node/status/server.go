// Package status is ctf-status: the student's door to their Juice Shop
// (DESIGN.md §5).
//
// It owns no state. The registration lives in ctf-agent, which it reaches over
// the ctf-internal network with CTF_INTERNAL_TOKEN; the gate cookie is signed
// with CTF_GATE_SECRET and carries the registration id, so checking it needs
// only the agent's current registration, cached for a few seconds.
//
// Everything is under BasePath (/ctf by default), and the page links are
// generated under it, so nginx never has to rewrite HTML.
//
//	GET  <base>/          sign-up, re-entry, or own progress
//	POST <base>/register  name + access code   → gate cookie, 303 to the shop
//	POST <base>/enter     access code           → gate cookie, 303 to the shop
//	POST <base>/leave     clears the cookie
//	GET  <base>/gate      204 or 401, for nginx auth_request (§5.3)
//	GET  <base>/healthz   204
package status

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/node/agent"
	"github.com/macsnoeren/juiceshop-ctf/internal/node/gate"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/httpx"
	protocol "github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol/v1"
	"github.com/macsnoeren/juiceshop-ctf/web/node"
	"github.com/macsnoeren/juiceshop-ctf/web/static"
)

const csrfCookie = "jmnl_ctf_csrf"

// Config is ctf-status's configuration (§9).
type Config struct {
	BasePath       string // "/ctf"
	AccessCode     string
	GateSecret     []byte
	CookieSecure   bool   // false only for plain HTTP on an IP address (§5.3)
	JuiceShopPath  string // "/juiceshop/"
	LeaderboardURL string
	Location       *time.Location
	// CacheTTL is how long the agent's answer is trusted by /gate; ErrorTTL
	// how long a failure is. Both are short: a reset must close the door
	// within seconds, and a recovered agent must open it again soon.
	CacheTTL time.Duration
	ErrorTTL time.Duration
}

// Server is the status page.
type Server struct {
	cfg     Config
	agent   AgentClient
	log     *slog.Logger
	now     func() time.Time
	limiter *Limiter
	pages   *template.Template

	regMu  sync.Mutex
	regAt  time.Time
	reg    *protocol.Registration
	regErr error
}

// New returns a status server.
func New(cfg Config, ag AgentClient, log *slog.Logger) (*Server, error) {
	if cfg.JuiceShopPath == "" {
		cfg.JuiceShopPath = "/juiceshop/"
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 5 * time.Second
	}
	if cfg.ErrorTTL == 0 {
		cfg.ErrorTTL = 2 * time.Second
	}
	s := &Server{cfg: cfg, agent: ag, log: log, now: time.Now, limiter: NewLimiter(5, time.Minute)}
	funcs := template.FuncMap{
		"path": func(p string) string { return cfg.BasePath + p },
		"clock": func(t *time.Time) string {
			if t == nil {
				return "never"
			}
			return t.In(cfg.Location).Format("15:04")
		},
		"hm":         func(t time.Time) string { return t.In(cfg.Location).Format("15:04") },
		"stars":      func(n int) string { return strings.Repeat("★", n) },
		"cookieName": func() string { return gate.CookieName },
	}
	t, err := template.New("").Funcs(funcs).ParseFS(nodeweb.FS, "*.html")
	if err != nil {
		return nil, err
	}
	s.pages = t
	return s, nil
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	b := s.cfg.BasePath
	mux := http.NewServeMux()
	staticFS := static.FS
	mux.Handle("GET "+b+"/static/", http.StripPrefix(b+"/static/", http.FileServerFS(staticFS)))
	mux.HandleFunc("GET "+b+"/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET "+b+"/gate", s.handleGate)
	mux.HandleFunc("GET "+b+"/{$}", s.handleIndex)
	mux.HandleFunc("POST "+b+"/register", s.handleRegister)
	mux.HandleFunc("POST "+b+"/enter", s.handleEnter)
	mux.HandleFunc("POST "+b+"/leave", s.handleLeave)
	if b != "" {
		mux.HandleFunc("GET "+b, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, b+"/", http.StatusFound)
		})
	}
	return httpx.SecurityHeaders(mux)
}

// currentRegistration asks the agent, at most once per CacheTTL. The lock is
// held during the call, so 80 simultaneous requests are one agent call.
func (s *Server) currentRegistration(ctx context.Context) (*protocol.Registration, error) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	ttl := s.cfg.CacheTTL
	if s.regErr != nil {
		ttl = s.cfg.ErrorTTL
	}
	if !s.regAt.IsZero() && s.now().Sub(s.regAt) < ttl {
		return s.reg, s.regErr
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s.reg, s.regErr = s.agent.Registration(ctx)
	s.regAt = s.now()
	if s.regErr != nil {
		s.log.Warn("agent unreachable, the door is closed", "err", s.regErr)
	}
	return s.reg, s.regErr
}

func (s *Server) forgetRegistration() {
	s.regMu.Lock()
	s.regAt = time.Time{}
	s.regMu.Unlock()
}

// cookieRegistration returns the registration id of a valid gate cookie.
func (s *Server) cookieRegistration(r *http.Request) (string, string) {
	c, err := r.Cookie(gate.CookieName)
	if err != nil {
		return "", ""
	}
	id, err := gate.Verify(c.Value, s.cfg.GateSecret, s.now())
	if err != nil {
		return "", ""
	}
	return id, c.Value
}

// handleGate is nginx's auth_request target. It fails CLOSED (§10): no
// agent, no entry, because a door that opens when its checker is down opens
// for the scanners too.
func (s *Server) handleGate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, _ := s.cookieRegistration(r)
	if id == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	reg, err := s.currentRegistration(r.Context())
	if err != nil || reg == nil || reg.ID != id {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type pageData struct {
	CSRF           string
	Error          string
	Name           string
	Registration   *protocol.Registration
	State          *agent.StateView
	Cookie         string
	JuiceShopPath  string
	LeaderboardURL string
	Host           string
	Secure         bool
}

func (s *Server) render(w http.ResponseWriter, status int, page string, d pageData) {
	d.JuiceShopPath = s.cfg.JuiceShopPath
	d.LeaderboardURL = s.cfg.LeaderboardURL
	d.Secure = s.cfg.CookieSecure
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := s.pages.ExecuteTemplate(w, page, d); err != nil {
		s.log.Error("template", "page", page, "err", err)
	}
}

// csrfToken returns the request's CSRF token, setting the cookie when absent.
// It is a double-submit token: the form carries the cookie's value, and a
// cross-site form cannot read the cookie to copy it.
func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) == 32 {
		return c.Value
	}
	v := httpx.RandomHex(16)
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: v, Path: s.cfg.BasePath + "/",
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	return v
}

func (s *Server) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	return err == nil && httpx.EqualSecret(c.Value, r.PostFormValue("csrf"))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	d := pageData{CSRF: s.csrfToken(w, r), Host: r.Host}
	reg, err := s.currentRegistration(r.Context())
	if err != nil {
		s.render(w, http.StatusServiceUnavailable, "unavailable", d)
		return
	}
	if reg == nil {
		s.render(w, http.StatusOK, "register", d)
		return
	}
	d.Registration = reg
	id, cookie := s.cookieRegistration(r)
	if id != reg.ID {
		s.render(w, http.StatusOK, "enter", d)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	st, err := s.agent.State(ctx)
	if err != nil {
		s.render(w, http.StatusServiceUnavailable, "unavailable", d)
		return
	}
	d.State, d.Cookie = st, cookie
	s.render(w, http.StatusOK, "progress", d)
}

// attempt applies CSRF, the rate limit and the access code to a form POST.
// It renders the error page itself and returns false when the attempt fails.
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, page string, d *pageData) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	d.CSRF = s.csrfToken(w, r)
	if !s.checkCSRF(r) {
		d.Error = "This form has expired. Please try again."
		s.render(w, http.StatusForbidden, page, *d)
		return false
	}
	ip := httpx.ClientIP(r)
	if !s.limiter.Allow(ip) {
		s.log.Warn("access code rate limit", "client", ip)
		d.Error = "Too many attempts. Wait a minute and try again."
		s.render(w, http.StatusTooManyRequests, page, *d)
		return false
	}
	if !httpx.EqualSecret(r.PostFormValue("code"), s.cfg.AccessCode) {
		s.log.Info("wrong access code", "client", ip)
		d.Error = "That access code is not right."
		s.render(w, http.StatusForbidden, page, *d)
		return false
	}
	return true
}

func (s *Server) setGateCookie(w http.ResponseWriter, reg *protocol.Registration) {
	exp := s.now().Add(gate.Lifetime)
	http.SetCookie(w, &http.Cookie{
		Name: gate.CookieName, Value: gate.Sign(reg.ID, exp, s.cfg.GateSecret),
		Path: "/", Expires: exp, MaxAge: int(gate.Lifetime / time.Second),
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	d := pageData{Host: r.Host}
	if !s.attempt(w, r, "register", &d) {
		return
	}
	d.Name = r.PostFormValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	reg, err := s.agent.Register(ctx, d.Name)
	var nameErr NameError
	switch {
	case errors.As(err, &nameErr):
		d.Error = nameErr.Msg
		s.render(w, http.StatusBadRequest, "register", d)
		return
	case errors.Is(err, ErrRegistered):
		s.forgetRegistration()
		cur, _ := s.currentRegistration(r.Context())
		d.Registration = cur
		d.Error = "Somebody has just signed up on this instance. If that was you, enter the access code below."
		s.render(w, http.StatusConflict, "enter", d)
		return
	case err != nil:
		s.log.Error("registration failed", "err", err)
		s.render(w, http.StatusServiceUnavailable, "unavailable", d)
		return
	}
	s.forgetRegistration()
	s.log.Info("student registered", "name", reg.Name, "client", httpx.ClientIP(r))
	s.setGateCookie(w, reg)
	http.Redirect(w, r, s.cfg.JuiceShopPath, http.StatusSeeOther)
}

func (s *Server) handleEnter(w http.ResponseWriter, r *http.Request) {
	d := pageData{Host: r.Host}
	reg, err := s.currentRegistration(r.Context())
	if err != nil {
		s.render(w, http.StatusServiceUnavailable, "unavailable", d)
		return
	}
	if reg == nil {
		http.Redirect(w, r, s.cfg.BasePath+"/", http.StatusSeeOther)
		return
	}
	d.Registration = reg
	if !s.attempt(w, r, "enter", &d) {
		return
	}
	s.setGateCookie(w, reg)
	http.Redirect(w, r, s.cfg.JuiceShopPath, http.StatusSeeOther)
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	r.ParseForm()
	if !s.checkCSRF(r) {
		http.Error(w, "form expired", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: gate.CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, s.cfg.BasePath+"/", http.StatusSeeOther)
}
