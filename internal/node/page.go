package node

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/shared/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/names"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
	nodeweb "github.com/macsnoeren/juiceshop-ctf/web/node"
	"github.com/macsnoeren/juiceshop-ctf/web/static"
)

// The web server of the VM (VERSION-1-1.md §4.2, §4.4): the student's page,
// and the shop behind it.
//
//	GET  /                 → /ctf/
//	GET  /ctf/             join, progress, rank, the buttons, the central server
//	POST /ctf/join         name + join code
//	POST /ctf/restart      Restart Juice Shop
//	POST /ctf/forget       Forget (type "forget")
//	POST /ctf/central      another central server, the default, or none (not while joined)
//	POST /ctf/enter        the optional gate: name + return code
//	     /juiceshop/, /assets/   the shop (proxy.go)
//	GET  /ctf/healthz      204
//
// NO GATE by default: in the lab only the student's own Kali reaches this VM
// (the host-only network), so nothing here depends on a cookie, and wiping
// every cookie — which Juice Shop's challenges make students do — changes
// nothing. Every POST carries a CSRF token (a double-submit cookie: when the
// cookie is gone, the next page view sets a new one).

const (
	csrfCookie    = "ctf_node_csrf"
	sessionCookie = "ctf_node_session"
	sessionLife   = 14 * 24 * time.Hour
	// GateTries wrong return codes per GateWindow, for the one student of
	// this VM.
	GateTries  = 5
	GateWindow = 5 * time.Minute
)

// PageConfig is what the page needs besides the node.
type PageConfig struct {
	Location *time.Location
	// JuiceShop is the reverse proxy; tests pass a fake.
	JuiceShop http.Handler
}

// Server is the VM's web server.
type Server struct {
	n     *Node
	cfg   PageConfig
	pages *template.Template
}

// NewServer returns the web server of node n.
func NewServer(n *Node, cfg PageConfig) (*Server, error) {
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	funcs := template.FuncMap{
		"hm": func(t time.Time) string { return t.In(cfg.Location).Format("15:04") },
		"clock": func(t *time.Time) string {
			if t == nil {
				return "never"
			}
			return t.In(cfg.Location).Format("15:04")
		},
		"datetime": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.In(cfg.Location).Format("2006-01-02 15:04")
		},
		"stars": func(k int) string { return strings.Repeat("★", k) },
		"code":  protocol.FormatCode,
	}
	t, err := template.New("").Funcs(funcs).ParseFS(nodeweb.FS, "*.html")
	if err != nil {
		return nil, err
	}
	return &Server{n: n, cfg: cfg, pages: t}, nil
}

// Handler returns every route of port 80.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ctf/", http.StatusFound) })
	mux.HandleFunc("GET /ctf", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ctf/", http.StatusFound) })
	mux.HandleFunc("GET /juiceshop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/juiceshop/", http.StatusFound) })
	mux.Handle("GET /ctf/static/", http.StripPrefix("/ctf/static/", http.FileServerFS(static.FS)))
	mux.HandleFunc("GET /ctf/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ctf/{$}", func(w http.ResponseWriter, r *http.Request) { s.page(w, r, http.StatusOK, flash{}) })
	mux.HandleFunc("POST /ctf/join", s.form(s.handleJoin))
	mux.HandleFunc("POST /ctf/restart", s.form(s.handleRestart))
	mux.HandleFunc("POST /ctf/forget", s.form(s.handleForget))
	mux.HandleFunc("POST /ctf/central", s.form(s.handleCentral))
	mux.HandleFunc("POST /ctf/enter", s.form(s.handleEnter))
	pages := httpx.SecurityHeaders(mux)

	shop := s.gated(s.cfg.JuiceShop)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Juice Shop sets its own headers; the CSP of the CTF pages would
		// break it, so the shop paths bypass SecurityHeaders.
		if strings.HasPrefix(r.URL.Path, "/juiceshop/") || strings.HasPrefix(r.URL.Path, "/assets/") {
			shop.ServeHTTP(w, r)
			return
		}
		pages.ServeHTTP(w, r)
	})
}

// gated applies the optional gate (CTF_GATE=session) to the shop.
func (s *Server) gated(h http.Handler) http.Handler {
	if !s.n.cfg.Gate {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasSession(r) {
			http.Redirect(w, r, "/ctf/", http.StatusFound)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) hasSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.n.db.ValidSession(c.Value, s.n.now())
}

func (s *Server) newSession(w http.ResponseWriter) error {
	id, err := s.n.db.NewSession(s.n.now(), sessionLife)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", MaxAge: int(sessionLife / time.Second),
		HttpOnly: true, SameSite: http.SameSiteLaxMode})
	return nil
}

type flash struct {
	Error   string
	Message string
	// ReturnCode is the gate's personal code, shown once after joining.
	ReturnCode string
	Name       string
}

type pageData struct {
	View
	Flash      flash
	CSRF       string
	Host       string
	NeedsEntry bool
	JoinCode   bool
}

func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) == 32 {
		return c.Value
	}
	v := httpx.RandomHex(16)
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: v, Path: "/ctf/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return v
}

func (s *Server) page(w http.ResponseWriter, r *http.Request, status int, f flash) {
	v := s.n.View()
	d := pageData{View: v, Flash: f, CSRF: s.csrfToken(w, r), Host: r.Host}
	d.NeedsEntry = v.Gate && v.Membership != nil && !s.hasSession(r) && f.ReturnCode == ""
	central := v.CentralURL
	if v.Membership != nil {
		central = v.Membership.CentralURL
	}
	d.JoinCode = central != ""
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := s.pages.ExecuteTemplate(w, "page", d); err != nil {
		s.n.log.Error("template", "err", err)
	}
}

// form parses a POST and checks its CSRF token.
func (s *Server) form(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		c, err := r.Cookie(csrfCookie)
		if err != nil || !httpx.EqualSecret(c.Value, r.PostFormValue("csrf")) {
			s.page(w, r, http.StatusForbidden, flash{Error: "This form had expired. Please try again."})
			return
		}
		h(w, r)
	}
}

func (s *Server) back(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ctf/", http.StatusSeeOther)
}

// needSession refuses an action on a gated VM without a session.
func (s *Server) needSession(w http.ResponseWriter, r *http.Request) bool {
	v := s.n.cfg.Gate && !s.hasSession(r)
	if v {
		s.page(w, r, http.StatusForbidden, flash{Error: "Enter your name and return code first."})
	}
	return v
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	m := s.n.View().Membership
	if m != nil && s.needSession(w, r) {
		return
	}
	name := r.PostFormValue("name")
	res, err := s.n.Join(ctx, name, r.PostFormValue("code"))
	var je *JoinError
	switch {
	case errors.As(err, &je):
		s.page(w, r, je.Status, flash{Error: je.Msg, Name: name})
		return
	case errors.Is(err, ErrJoinedAlready):
		s.page(w, r, http.StatusConflict, flash{Error: err.Error()})
		return
	case err != nil:
		s.n.log.Error("join failed", "err", err)
		s.page(w, r, http.StatusInternalServerError, flash{Error: "Joining failed. Try again in a moment."})
		return
	}
	f := flash{}
	switch res.Outcome {
	case JoinedOnline:
		f.Message = "You are in. Every challenge you solve in your Juice Shop now counts."
	case JoinQueued:
		f.Message = "The central server cannot be reached right now. Play on: your solves are kept on this VM, and your join is sent as soon as the server answers."
	case JoinedLocal:
		f.Message = "You are playing on this VM only: there is no central server."
	}
	if res.ReturnCode != "" {
		if err := s.newSession(w); err != nil {
			s.n.log.Error("session", "err", err)
		}
		f.ReturnCode = res.ReturnCode
		s.page(w, r, http.StatusOK, f)
		return
	}
	s.page(w, r, http.StatusOK, f)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.needSession(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.n.Restart(ctx); err != nil {
		s.page(w, r, http.StatusServiceUnavailable, flash{Error: "Restarting failed: " + err.Error()})
		return
	}
	s.page(w, r, http.StatusOK, flash{Message: "Juice Shop is restarting. Your progress is put back in it within a minute; your score is not affected."})
}

func (s *Server) handleForget(w http.ResponseWriter, r *http.Request) {
	if s.needSession(w, r) {
		return
	}
	if strings.TrimSpace(strings.ToLower(r.PostFormValue("confirm"))) != "forget" {
		s.page(w, r, http.StatusBadRequest, flash{Error: "Type the word forget to confirm. Nothing was deleted."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	err := s.n.Forget(ctx, protocol.LeaveForget)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	switch {
	case errors.Is(err, ErrForgetNoRestart):
		s.page(w, r, http.StatusOK, flash{Error: "Everything about you is deleted from this VM, but Juice Shop could not be restarted. Restart the VM to empty the shop too."})
	case err != nil:
		s.n.log.Error("forget failed", "err", err)
		s.page(w, r, http.StatusInternalServerError, flash{Error: "Forget failed: " + err.Error()})
	default:
		s.page(w, r, http.StatusOK, flash{Message: "Forgotten. This VM knows nothing about you any more, and Juice Shop starts empty."})
	}
}

func (s *Server) handleCentral(w http.ResponseWriter, r *http.Request) {
	if s.needSession(w, r) {
		return
	}
	v := ""
	switch r.PostFormValue("action") {
	case "default":
	case "none":
		v = centralNone
	default:
		v = r.PostFormValue("url")
		if strings.TrimSpace(v) == "" {
			s.page(w, r, http.StatusBadRequest, flash{Error: "Give the address of the central server."})
			return
		}
	}
	if err := s.n.SetCentral(v); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrJoinedAlready) {
			status = http.StatusConflict
		}
		s.page(w, r, status, flash{Error: err.Error()})
		return
	}
	s.page(w, r, http.StatusOK, flash{Message: "Central server set. The next join goes there."})
}

// handleEnter is the optional gate's re-entry: name + return code, five
// wrong codes per five minutes (there is one student per VM).
func (s *Server) handleEnter(w http.ResponseWriter, r *http.Request) {
	if !s.n.cfg.Gate {
		http.NotFound(w, r)
		return
	}
	now := s.n.now()
	if n, _ := s.n.db.GateFailures(now, GateWindow, false); n >= GateTries {
		s.page(w, r, http.StatusTooManyRequests, flash{Error: "Too many wrong codes. Wait five minutes and try again."})
		return
	}
	s.n.mu.Lock()
	m := s.n.st.Membership
	var want, hash string
	if m != nil {
		want, hash = m.Name, m.ReturnCodeHash
	}
	s.n.mu.Unlock()
	name, _ := names.Normalize(r.PostFormValue("name"))
	code, _ := protocol.NormalizeReturnCode(r.PostFormValue("code"))
	if m == nil || !strings.EqualFold(name, want) || !protocol.HashMatches(code, hash) {
		s.n.db.GateFailures(now, GateWindow, true)
		s.page(w, r, http.StatusForbidden, flash{Error: "That name and return code do not match."})
		return
	}
	if err := s.newSession(w); err != nil {
		s.n.log.Error("session", "err", err)
	}
	s.back(w, r)
}
