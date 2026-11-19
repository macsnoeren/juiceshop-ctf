// Package central is ctf-central: it admits students into games, receives
// their VMs' reports, and serves the boards and the operator's admin
// (docs/VERSION-1-1.md §2, §5, §6).
//
// Everything is under BasePath ("" on its own hostname, /juiceshop-ctf on
// neo):
//
//	POST <base>/api/v2/join            a student joins: name + join code → token
//	POST <base>/api/v2/report          a VM's snapshot (Bearer token)
//	POST <base>/api/v2/leave           the student pressed Forget (Bearer token)
//	POST <base>/api/v1/report          a 1.0 agent: 410 Gone, and an event
//	GET  <base>/                       the public games
//	GET  <base>/g/<slug>/              a game's board (board.go)
//	     <base>/admin/...              the operator pages (admin.go)
//	GET  <base>/healthz
//
// /admin/ has NO login of its own: nginx guards it (decision D4). Do not
// publish this server without that in front of it.
package central

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
	centralweb "github.com/macsnoeren/juiceshop-ctf/web/central"
	"github.com/macsnoeren/juiceshop-ctf/web/static"
)

// Config is ctf-central's configuration. Everything else is in the database.
type Config struct {
	BasePath string
}

// Server is ctf-central.
type Server struct {
	cfg   Config
	st    *store.Store
	log   *slog.Logger
	now   func() time.Time
	pages *template.Template
	loc   atomic.Pointer[time.Location]
	title atomic.Pointer[string]

	wrongCodes *window // wrong join codes per client address
	codeJoins  *window // joins per game
}

// The join limits (VERSION-1-1.md §5.6). Never per address alone: a school
// puts a whole room behind one address, and thirty students join in the same
// minute. A correct code never uses up the wrong-code budget.
const (
	WrongCodesPerMinute = 20
	JoinsPerGamePerMin  = 300
)

// New returns a server on st.
func New(cfg Config, st *store.Store, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, st: st, log: log, now: time.Now,
		wrongCodes: newWindow(time.Minute), codeJoins: newWindow(time.Minute)}
	if err := s.reloadSettings(); err != nil {
		return nil, err
	}
	t, err := template.New("").Funcs(s.funcs()).ParseFS(centralweb.FS, "*.html", "*.svg")
	if err != nil {
		return nil, err
	}
	s.pages = t
	return s, nil
}

// SetClock replaces the clock; for tests.
func (s *Server) SetClock(now func() time.Time) { s.now = now }

func (s *Server) reloadSettings() error {
	set, err := s.st.Settings()
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(set.Timezone)
	if err != nil {
		loc = time.UTC
	}
	s.loc.Store(loc)
	s.title.Store(&set.Title)
	return nil
}

func (s *Server) location() *time.Location { return s.loc.Load() }
func (s *Server) siteTitle() string        { return *s.title.Load() }

// Handler returns all routes.
func (s *Server) Handler() http.Handler {
	b := s.cfg.BasePath
	mux := http.NewServeMux()
	mux.Handle("GET "+b+"/static/", http.StripPrefix(b+"/static/", http.FileServerFS(static.FS)))
	mux.HandleFunc("GET "+b+"/healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.st.Ping() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST "+b+"/api/v2/join", s.handleJoin)
	mux.HandleFunc("POST "+b+"/api/v2/report", s.handleReport)
	mux.HandleFunc("POST "+b+"/api/v2/leave", s.handleLeave)
	mux.HandleFunc("POST "+b+"/api/v1/report", s.handleLegacy)
	s.boardRoutes(mux)
	s.adminRoutes(mux)
	if b != "" {
		mux.HandleFunc("GET "+b, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, b+"/", http.StatusFound)
		})
	}
	return httpx.SecurityHeaders(mux)
}

func (s *Server) render(w http.ResponseWriter, status int, page string, d any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := s.pages.ExecuteTemplate(w, page, d); err != nil {
		s.log.Error("template", "page", page, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) event(e store.Event) {
	if e.At.IsZero() {
		e.At = s.now()
	}
	if err := s.st.AddEvent(e); err != nil {
		s.log.Error("writing an event failed", "err", err)
	}
}

func apiError(w http.ResponseWriter, status int, msg string) {
	httpx.WriteJSON(w, status, protocol.ErrorResponse{Error: msg})
}

// decode reads a JSON body of at most protocol.MaxBodyBytes. On failure it
// answers, writes a "rejected" event and returns false.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, what string, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxBodyBytes))
	err := dec.Decode(into)
	if err == nil {
		return true
	}
	addr := httpx.ClientIP(r)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		s.event(store.Event{Kind: store.EvRejected, Address: addr, Text: what + ": body too large"})
		apiError(w, http.StatusRequestEntityTooLarge, "request too large")
		return false
	}
	s.event(store.Event{Kind: store.EvRejected, Address: addr, Text: what + ": body is not valid JSON"})
	apiError(w, http.StatusBadRequest, "body is not valid JSON")
	return false
}

func (s *Server) boardURL(r *http.Request, slug string) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + s.cfg.BasePath + "/g/" + slug + "/"
}

// handleJoin is POST /api/v2/join (VERSION-1-1.md §2.2).
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	addr := httpx.ClientIP(r)
	// Before anything else, so a guesser is slowed down whatever they send.
	if s.wrongCodes.Count(addr, now) >= WrongCodesPerMinute {
		s.event(store.Event{At: now, Kind: store.EvJoinRefused, Address: addr, Text: "too many wrong join codes from this address (429)"})
		apiError(w, http.StatusTooManyRequests, "too many wrong join codes; wait a minute")
		return
	}
	var req protocol.JoinRequest
	if !s.decode(w, r, "join", &req) {
		return
	}
	if err := req.Normalize(); err != nil {
		s.event(store.Event{At: now, Kind: store.EvJoinRefused, Address: addr, Text: "invalid join: " + err.Error()})
		apiError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), protocol.ErrInvalid.Error()+": "))
		return
	}
	codeKey := protocol.Hash(req.Code)
	if s.codeJoins.Count(codeKey, now) >= JoinsPerGamePerMin {
		s.event(store.Event{At: now, Kind: store.EvJoinRefused, Address: addr, Text: "join limit of this game reached (429)"})
		apiError(w, http.StatusTooManyRequests, "too many joins at once; try again in a minute")
		return
	}
	res, g, err := s.st.Join(&req, now)
	var gid int64
	if g != nil {
		gid = g.ID
	}
	switch {
	case errors.Is(err, store.ErrUnknownCode):
		s.wrongCodes.Add(addr, now)
		s.event(store.Event{At: now, Kind: store.EvJoinRefused, Address: addr, Text: "unknown join code (403), name " + req.Name})
		apiError(w, http.StatusForbidden, err.Error())
		return
	case errors.Is(err, store.ErrGameDraft), errors.Is(err, store.ErrGameClosed):
		s.event(store.Event{At: now, Kind: store.EvJoinRefused, GameID: gid, Address: addr, Text: err.Error() + " (409), name " + req.Name})
		apiError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	s.codeJoins.Add(codeKey, now)
	p := res.Participant
	text := fmt.Sprintf("joined as %s; node %s, Juice Shop %s, uid %s", p.Name, req.Node.Version, req.Node.JuiceShop, req.Node.UID[:8])
	if req.JoinedAt != nil && now.Sub(*req.JoinedAt) > time.Minute {
		text += fmt.Sprintf("; pressed join %s earlier (offline)", now.Sub(*req.JoinedAt).Round(time.Second))
	}
	s.event(store.Event{At: now, Kind: store.EvJoin, GameID: res.Game.ID, ParticipantID: p.ID, Address: addr, Text: text})
	s.log.Info("participant joined", "game", res.Game.Slug, "participant", p.ID)
	httpx.WriteJSON(w, http.StatusCreated, protocol.JoinResponse{
		Participant: p.ID, Token: res.Token, Name: p.Name, Game: res.Game.Protocol(now), Board: s.boardURL(r, res.Game.Slug),
	})
}

// token returns the hash of a well-formed bearer token, or answers 401.
func (s *Server) token(w http.ResponseWriter, r *http.Request, what string) (string, bool) {
	t := httpx.BearerToken(r)
	if !protocol.ValidParticipantToken(t) {
		s.event(store.Event{Kind: store.EvRejected, Address: httpx.ClientIP(r), Text: what + ": no or malformed token (401)"})
		apiError(w, http.StatusUnauthorized, "a participant token is required")
		return "", false
	}
	return protocol.Hash(t), true
}

// handleReport is POST /api/v2/report.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	hash, ok := s.token(w, r, "report")
	if !ok {
		return
	}
	var rep protocol.Report
	if !s.decode(w, r, "report", &rep) {
		return
	}
	addr := httpx.ClientIP(r)
	if err := rep.Validate(); err != nil {
		s.event(store.Event{Kind: store.EvRejected, Address: addr, Text: "report: " + err.Error()})
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, res, err := s.st.Report(hash, &rep, s.now(), addr)
	if err != nil {
		s.fail(w, err)
		return
	}
	if res.Outcome == store.ReportUnknown {
		s.event(store.Event{Kind: store.EvRejected, Address: addr, Text: "report: unknown token (401)"})
		apiError(w, http.StatusUnauthorized, "unknown token: join again")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// handleLeave is POST /api/v2/leave.
func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	hash, ok := s.token(w, r, "leave")
	if !ok {
		return
	}
	var req protocol.LeaveRequest
	if !s.decode(w, r, "leave", &req) {
		return
	}
	addr := httpx.ClientIP(r)
	if err := req.Validate(); err != nil {
		s.event(store.Event{Kind: store.EvRejected, Address: addr, Text: "leave: " + err.Error()})
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, _, err := s.st.Leave(hash, req.Reason, s.now(), addr)
	if err != nil {
		s.fail(w, err)
		return
	}
	if out == store.LeaveUnknown {
		s.event(store.Event{Kind: store.EvRejected, Address: addr, Text: "leave: unknown token (401)"})
		apiError(w, http.StatusUnauthorized, "unknown token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLegacy answers a 1.0 agent: its protocol is gone, and the operator
// should see which address still runs one (TASK-1-1 T1.1-19).
func (s *Server) handleLegacy(w http.ResponseWriter, r *http.Request) {
	var v1 struct {
		Instance     string `json:"instance"`
		AgentVersion string `json:"agent_version"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, protocol.MaxBodyBytes))
	json.Unmarshal(body, &v1)
	text := "a 1.0 agent reported (410)"
	if v1.Instance != "" && len(v1.Instance) <= 63 {
		text += fmt.Sprintf(": instance %q, agent %.20s", v1.Instance, v1.AgentVersion)
	}
	s.event(store.Event{Kind: store.EvLegacy, Address: httpx.ClientIP(r), Text: text})
	apiError(w, http.StatusGone, "protocol 1 is no longer accepted: this central server runs Juice Shop CTF 1.1; import the 1.1 VM and join a game")
}
