// Package central is ctf-central: it receives the agents' reports, keeps the
// score and serves the leaderboard (DESIGN.md §7, §8).
//
// Everything is under BasePath ("" on its own hostname, /leaderboard in the
// docker/ test setup):
//
//	POST <base>/api/v1/report        an agent's report (bearer token)
//	GET  <base>/api/v1/leaderboard   the board as JSON, polled by the pages
//	GET  <base>/                     the board
//	GET  <base>/beamer               the board, large, with the latest solves
//	GET  <base>/p/<id>               one participant
//	     <base>/admin/...            the operator pages (see admin.go)
//	GET  <base>/healthz
//
// /admin/ has NO login of its own: nginx guards it (§8.4). Do not publish
// this server without that in front of it.
package central

import (
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
	"github.com/macsnoeren/juiceshop-ctf/web/central"
	"github.com/macsnoeren/juiceshop-ctf/web/static"
)

// Config is ctf-central's configuration.
type Config struct {
	BasePath string
	Title    string
	Location *time.Location
}

// Server is ctf-central.
type Server struct {
	cfg   Config
	st    *store.Store
	log   *slog.Logger
	now   func() time.Time
	pages *template.Template
}

// New returns a server on st.
func New(cfg Config, st *store.Store, log *slog.Logger) (*Server, error) {
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.Title == "" {
		cfg.Title = "Juice Shop CTF"
	}
	s := &Server{cfg: cfg, st: st, log: log, now: time.Now}
	funcs := template.FuncMap{
		"path": func(p string) string { return cfg.BasePath + p },
		"clock": func(t *time.Time) string {
			if t == nil {
				return "–"
			}
			return t.In(cfg.Location).Format("15:04")
		},
		"datetime": func(t *time.Time) string {
			if t == nil {
				return "–"
			}
			return t.In(cfg.Location).Format("2006-01-02 15:04")
		},
		"hm":    func(t time.Time) string { return t.In(cfg.Location).Format("15:04") },
		"stars": func(n int) string { return strings.Repeat("★", n) },
		"pct": func(f *float64) string {
			if f == nil {
				return "–"
			}
			return strconv.Itoa(int(*f*100+0.5)) + "%"
		},
		"online": func(in store.Instance) bool { return in.Online(s.now()) },
	}
	t, err := template.New("").Funcs(funcs).ParseFS(centralweb.FS, "*.html")
	if err != nil {
		return nil, err
	}
	s.pages = t
	return s, nil
}

// Handler returns all routes.
func (s *Server) Handler() http.Handler {
	b := s.cfg.BasePath
	mux := http.NewServeMux()
	staticFS := static.FS
	mux.Handle("GET "+b+"/static/", http.StripPrefix(b+"/static/", http.FileServerFS(staticFS)))
	mux.HandleFunc("GET "+b+"/healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.st.Ping() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST "+b+"/api/v1/report", s.handleReport)
	mux.HandleFunc("GET "+b+"/api/v1/leaderboard", s.handleBoardJSON)
	mux.HandleFunc("GET "+b+"/{$}", s.handleIndex)
	mux.HandleFunc("GET "+b+"/beamer", s.handleBeamer)
	mux.HandleFunc("GET "+b+"/p/{id}", s.handleParticipant)
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

// handleReport is §7.2 on the receiving end.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	inst, err := s.st.InstanceForToken(httpx.BearerToken(r))
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteJSON(w, http.StatusUnauthorized, protocol.Response{Error: "unknown token"})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	var rep protocol.Report
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxBodyBytes))
	if err := dec.Decode(&rep); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpx.WriteJSON(w, http.StatusRequestEntityTooLarge, protocol.Response{Error: "report too large"})
			return
		}
		httpx.WriteJSON(w, http.StatusBadRequest, protocol.Response{Error: "report is not valid JSON"})
		return
	}
	if err := rep.Validate(); err != nil {
		s.log.Warn("invalid report", "instance", inst, "err", err)
		httpx.WriteJSON(w, http.StatusBadRequest, protocol.Response{Error: err.Error()})
		return
	}
	if rep.Instance != inst {
		s.log.Warn("report for another instance", "token_for", inst, "report_for", rep.Instance)
		httpx.WriteJSON(w, http.StatusBadRequest, protocol.Response{Error: "instance does not match the token"})
		return
	}
	now := s.now()
	pid, err := s.st.MergeReport(inst, &rep, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp := protocol.Response{OK: true}
	if resp.Commands, err = s.st.PendingCommands(inst, now); err != nil {
		s.fail(w, err)
		return
	}
	if pid != 0 {
		if resp.Standing, err = s.st.Standing(pid, now); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.log.Debug("report", "instance", inst, "participant", pid, "commands", len(resp.Commands))
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// BoardJSON is GET /api/v1/leaderboard.
type BoardJSON struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Total       int                 `json:"total"`
	Rows        []store.Row         `json:"rows"`
	Recent      []store.RecentSolve `json:"recent"`
}

func (s *Server) board() (*BoardJSON, error) {
	now := s.now()
	rows, err := s.st.Leaderboard(now)
	if err != nil {
		return nil, err
	}
	total, err := s.st.TotalChallenges()
	if err != nil {
		return nil, err
	}
	recent, err := s.st.Recent(12)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []store.Row{}
	}
	return &BoardJSON{GeneratedAt: now.UTC(), Total: total, Rows: rows, Recent: recent}, nil
}

func (s *Server) handleBoardJSON(w http.ResponseWriter, r *http.Request) {
	b, err := s.board()
	if err != nil {
		s.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, b)
}

type boardPage struct {
	Title string
	Base  string
	Board *BoardJSON
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	b, err := s.board()
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "index", boardPage{Title: s.cfg.Title, Base: s.cfg.BasePath, Board: b})
}

func (s *Server) handleBeamer(w http.ResponseWriter, r *http.Request) {
	b, err := s.board()
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "beamer", boardPage{Title: s.cfg.Title, Base: s.cfg.BasePath, Board: b})
}

func (s *Server) handleParticipant(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.st.ParticipantDetail(id, s.now())
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "participant", struct {
		Title string
		D     *store.Detail
	}{s.cfg.Title, d})
}
