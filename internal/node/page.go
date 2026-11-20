// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package node

import (
	"context"
	"errors"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/names"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
	nodeweb "github.com/macsnoeren/juiceshop-ctf/web/node"
	"github.com/macsnoeren/juiceshop-ctf/web/static"
)

// The web servers of the VM: the student's page, and the shop. They are
// TWO ORIGINS, on two ports:
//
//	the page, :8080 (Page)
//	GET  /                 → /ctf/
//	GET  /ctf/             join, progress, rank, the buttons, the central server
//	POST /ctf/join         name + join code
//	POST /ctf/restart      Restart Juice Shop
//	POST /ctf/forget       Forget (type "forget")
//	POST /ctf/central      another central server, the default, or none (not while joined)
//	POST /ctf/enter        the optional gate: name + return code
//	GET  /ctf/healthz      204
//	GET  /juiceshop/…      → the same on the shop's port
//
//	the shop, :80 (Shop)
//	     /juiceshop/, /assets/   Juice Shop (proxy.go)
//	GET  anything else     → the page's port
//
// >>> WHY TWO ORIGINS: Juice Shop has XSS on purpose, and script that runs in
// the shop runs with the shop's origin. On ONE origin it could read the CTF
// page, take its form token and press Forget: off the board, the VM wiped,
// from one link a classmate sends. On another port it cannot read the page.
// It can still SET a cookie the page receives (cookies ignore ports), which
// is why the form token is an HMAC (httpx.CSRF) and not a cookie repeated.
//
// NO GATE by default: in the lab only the student's own Kali reaches this VM
// (the host-only network), so nothing here depends on a cookie, and wiping
// every cookie — which Juice Shop's challenges make students do — changes
// nothing. Every POST carries a form token, and is refused when the browser
// says it came from another origin (Sec-Fetch-Site, Origin).

const (
	sessionCookie = "ctf_node_session"
	sessionLife   = 14 * 24 * time.Hour
	// GateTries wrong return codes per GateWindow, for the one student of
	// this VM.
	GateTries  = 5
	GateWindow = 5 * time.Minute
	// csrfLife is how long a form on the page stays valid.
	csrfLife = 24 * time.Hour
	// csrfBinding: the page has no session to bind a token to; the key,
	// which only this process knows, is what makes a token.
	csrfBinding = "ctf-node page"
)

// PageConfig is what the page needs besides the node.
type PageConfig struct {
	Location *time.Location
	// JuiceShop is the reverse proxy; tests pass a fake.
	JuiceShop http.Handler
	// ShopPort and PagePort are the ports a BROWSER uses for the shop and
	// the page (80 and 8080), for the links between the two. They differ
	// from the ports listened on only behind a port mapping.
	ShopPort, PagePort int
}

// Server is the VM's web server.
type Server struct {
	n     *Node
	cfg   PageConfig
	pages *template.Template
	csrf  *httpx.CSRF
}

// NewServer returns the web server of node n.
func NewServer(n *Node, cfg PageConfig) (*Server, error) {
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.ShopPort == 0 {
		cfg.ShopPort = 80
	}
	if cfg.PagePort == 0 {
		cfg.PagePort = 8080
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
	return &Server{n: n, cfg: cfg, pages: t, csrf: httpx.NewCSRF(csrfLife)}, nil
}

// hostOf is the host the browser used, without its port.
func hostOf(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return strings.Trim(r.Host, "[]")
}

// origin is http://<the host the browser used>:<port>, without :80.
func origin(r *http.Request, port int) string {
	h := hostOf(r)
	if port == 80 {
		if strings.Contains(h, ":") {
			return "http://[" + h + "]"
		}
		return "http://" + h
	}
	return "http://" + net.JoinHostPort(h, strconv.Itoa(port))
}

func (s *Server) shopURL(r *http.Request) string { return origin(r, s.cfg.ShopPort) + "/juiceshop/" }
func (s *Server) pageURL(r *http.Request) string { return origin(r, s.cfg.PagePort) + "/ctf/" }

// Page returns every route of the page's port.
func (s *Server) Page() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ctf/", http.StatusFound) })
	mux.HandleFunc("GET /ctf", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ctf/", http.StatusFound) })
	mux.HandleFunc("GET /juiceshop/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, origin(r, s.cfg.ShopPort)+r.URL.RequestURI(), http.StatusFound)
	})
	mux.Handle("GET /ctf/static/", http.StripPrefix("/ctf/static/", http.FileServerFS(static.FS)))
	mux.HandleFunc("GET /ctf/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ctf/{$}", func(w http.ResponseWriter, r *http.Request) { s.page(w, r, http.StatusOK, flash{}) })
	mux.HandleFunc("POST /ctf/join", s.form(s.handleJoin))
	mux.HandleFunc("POST /ctf/restart", s.form(s.handleRestart))
	mux.HandleFunc("POST /ctf/forget", s.form(s.handleForget))
	mux.HandleFunc("POST /ctf/central", s.form(s.handleCentral))
	mux.HandleFunc("POST /ctf/enter", s.form(s.handleEnter))
	return httpx.SecurityHeaders(mux)
}

// Shop returns every route of the shop's port: Juice Shop, and a way to
// the page for everything else. NOTHING of the page is served here.
func (s *Server) Shop() http.Handler {
	shop := s.gated(s.cfg.JuiceShop)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Juice Shop sets its own headers; the CSP of the CTF pages would
		// break it, so the shop gets none of SecurityHeaders.
		switch p := r.URL.Path; {
		case strings.HasPrefix(p, "/juiceshop/") || strings.HasPrefix(p, "/assets/"):
			shop.ServeHTTP(w, r)
		case r.Method != http.MethodGet && r.Method != http.MethodHead:
			http.NotFound(w, r)
		case p == "/juiceshop":
			http.Redirect(w, r, "/juiceshop/", http.StatusFound)
		default: // /, /ctf/…: the page lives on its own port now
			http.Redirect(w, r, s.pageURL(r), http.StatusFound)
		}
	})
}

// gated applies the optional gate (CTF_GATE=session) to the shop.
func (s *Server) gated(h http.Handler) http.Handler {
	if !s.n.cfg.Gate {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasSession(r) {
			http.Redirect(w, r, s.pageURL(r), http.StatusFound)
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
	ShopURL    string // http://<this VM>/juiceshop/, as the browser reaches it
	NeedsEntry bool
	JoinCode   bool
	// Plain is set when the central server is reached over plain HTTP on
	// a network others share: the page says what that means.
	Plain bool
}

func (s *Server) page(w http.ResponseWriter, r *http.Request, status int, f flash) {
	v := s.n.View()
	d := pageData{View: v, Flash: f, CSRF: s.csrf.Token(csrfBinding, s.n.now()), ShopURL: s.shopURL(r)}
	d.NeedsEntry = v.Gate && v.Membership != nil && !s.hasSession(r) && f.ReturnCode == ""
	central := v.CentralURL
	if v.Membership != nil {
		central = v.Membership.CentralURL
	}
	d.JoinCode = central != ""
	d.Plain = PlainOnSharedNetwork(central)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := s.pages.ExecuteTemplate(w, "page", d); err != nil {
		s.n.log.Error("template", "err", err)
	}
}

// form parses a POST, refuses it when it came from another origin (the
// shop's included), and checks its form token.
func (s *Server) form(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if crossOrigin(r) {
			s.page(w, r, http.StatusForbidden, flash{Error: "This form was sent from another page than this one; nothing was done."})
			return
		}
		if !s.csrf.Valid(r.PostFormValue("csrf"), csrfBinding, s.n.now()) {
			s.page(w, r, http.StatusForbidden, flash{Error: "This form had expired. Please try again."})
			return
		}
		h(w, r)
	}
}

// crossOrigin: Sec-Fetch-Site, and, for a browser without it, Origin. The
// page has no proxy in front, so Origin must name exactly the Host asked for.
func crossOrigin(r *http.Request) bool {
	if httpx.CrossOrigin(r) {
		return true
	}
	o := r.Header.Get("Origin")
	return o != "" && o != "http://"+r.Host && o != "https://"+r.Host
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
