package central

import (
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/httpx"
)

// The operator pages (DESIGN.md §8.4). nginx guards <base>/admin/ with an IP
// allow-list and htpasswd; this code adds only CSRF protection, so that a
// page in the operator's browser cannot make them reset a participant.
//
//	GET  <base>/admin/                                the overview
//	POST <base>/admin/instances                       create (token shown once)
//	POST <base>/admin/instances/{id}/rotate           new token (shown once)
//	POST <base>/admin/participants/{id}/{action}      hide | show | reset
//	POST <base>/admin/scoring                         points per difficulty
//	GET  <base>/admin/export.csv                      every participant

const adminCSRFCookie = "ctf_admin_csrf"

func (s *Server) adminRoutes(mux *http.ServeMux) {
	b := s.cfg.BasePath + "/admin"
	mux.HandleFunc("GET "+b+"/{$}", func(w http.ResponseWriter, r *http.Request) { s.adminPage(w, r, http.StatusOK, adminFlash{}) })
	mux.HandleFunc("GET "+b, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, b+"/", http.StatusFound) })
	mux.HandleFunc("POST "+b+"/instances", s.csrf(s.handleCreateInstance))
	mux.HandleFunc("POST "+b+"/instances/{id}/rotate", s.csrf(s.handleRotate))
	mux.HandleFunc("POST "+b+"/participants/{id}/{action}", s.csrf(s.handleParticipantAction))
	mux.HandleFunc("POST "+b+"/scoring", s.csrf(s.handleScoring))
	mux.HandleFunc("GET "+b+"/export.csv", s.handleExport)
}

func (s *Server) adminCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(adminCSRFCookie); err == nil && len(c.Value) == 32 {
		return c.Value
	}
	v := httpx.RandomHex(16)
	http.SetCookie(w, &http.Cookie{
		Name: adminCSRFCookie, Value: v, Path: s.cfg.BasePath + "/admin/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	return v
}

func (s *Server) csrf(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		c, err := r.Cookie(adminCSRFCookie)
		if err != nil || !httpx.EqualSecret(c.Value, r.PostFormValue("csrf")) {
			http.Error(w, "form expired: reload the admin page and try again", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

type adminFlash struct {
	Message string
	Error   string
	// Token is shown once, right after it was created or rotated.
	Token         string
	TokenInstance string
}

type adminData struct {
	Title        string
	Base         string
	CSRF         string
	Flash        adminFlash
	Instances    []store.Instance
	Participants []store.Row
	Scoring      []scoringRow
	CentralURL   string
}

type scoringRow struct{ Difficulty, Points int }

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request, status int, f adminFlash) {
	ins, err := s.st.Instances()
	if err != nil {
		s.fail(w, err)
		return
	}
	ps, err := s.st.AllParticipants(s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.st.Scoring()
	if err != nil {
		s.fail(w, err)
		return
	}
	var rows []scoringRow
	for d := 1; d <= 6; d++ {
		rows = append(rows, scoringRow{d, sc[d]})
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	s.render(w, status, "admin", adminData{
		Title: s.cfg.Title, Base: s.cfg.BasePath, CSRF: s.adminCSRF(w, r), Flash: f,
		Instances: ins, Participants: ps, Scoring: rows,
		CentralURL: scheme + "://" + r.Host + s.cfg.BasePath,
	})
}

func (s *Server) back(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.cfg.BasePath+"/admin/", http.StatusSeeOther)
}

func (s *Server) handleCreateInstance(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PostFormValue("id"))
	label := strings.TrimSpace(r.PostFormValue("label"))
	if len(label) > 100 {
		label = label[:100]
	}
	tok, err := s.st.CreateInstance(id, label, s.now())
	switch {
	case errors.Is(err, store.ErrExists):
		s.adminPage(w, r, http.StatusConflict, adminFlash{Error: "An instance " + id + " already exists. Use “new token” on it instead."})
	case err != nil:
		s.adminPage(w, r, http.StatusBadRequest, adminFlash{Error: err.Error()})
	default:
		s.log.Info("instance created", "instance", id)
		s.adminPage(w, r, http.StatusOK, adminFlash{Message: "Instance " + id + " created.", Token: tok, TokenInstance: id})
	}
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tok, err := s.st.RotateToken(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.log.Info("instance token rotated", "instance", id)
	s.adminPage(w, r, http.StatusOK, adminFlash{Message: "New token for " + id + ". The old one no longer works.", Token: tok, TokenInstance: id})
}

func (s *Server) handleParticipantAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch r.PathValue("action") {
	case "hide":
		err = s.st.SetHidden(id, true)
	case "show":
		err = s.st.SetHidden(id, false)
	case "reset":
		err = s.st.ResetParticipant(id, s.now())
		if err == nil {
			s.log.Info("participant reset; reset_registration queued", "participant", id)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.back(w, r)
}

func (s *Server) handleScoring(w http.ResponseWriter, r *http.Request) {
	for d := 1; d <= 6; d++ {
		v := r.PostFormValue("points" + strconv.Itoa(d))
		if v == "" {
			continue
		}
		p, err := strconv.Atoi(v)
		if err == nil {
			err = s.st.SetScoring(d, p)
		}
		if err != nil {
			s.adminPage(w, r, http.StatusBadRequest, adminFlash{Error: "Points must be whole numbers from 0 to 100000."})
			return
		}
	}
	s.back(w, r)
}

// csvSafe keeps a spreadsheet from reading a cell as a formula. Names cannot
// contain = + @, but they may start with a dash.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.AllParticipants(s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="juiceshop-ctf.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"participant", "name", "instance", "registered_at", "ended_at", "hidden", "rank", "score", "solved", "total", "last_solve", "avg_cheat"})
	f := func(p *float64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatFloat(*p, 'f', 3, 64)
	}
	ts := func(t interface{ Format(string) string }) string { return t.Format("2006-01-02T15:04:05Z07:00") }
	for _, p := range rows {
		ended, last := "", ""
		if p.EndedAt != nil {
			ended = ts(p.EndedAt.UTC())
		}
		if p.LastSolve != nil {
			last = ts(p.LastSolve.UTC())
		}
		cw.Write([]string{
			strconv.FormatInt(p.ParticipantID, 10), csvSafe(p.Name), p.Instance, ts(p.RegisteredAt.UTC()), ended,
			strconv.FormatBool(p.Hidden), strconv.Itoa(p.Rank), strconv.Itoa(p.Score), strconv.Itoa(p.Solved),
			strconv.Itoa(p.Total), last, f(p.AvgCheat),
		})
	}
	cw.Flush()
}
