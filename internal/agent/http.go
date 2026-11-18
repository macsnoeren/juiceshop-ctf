package agent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

const maxWebhookBody = 64 << 10

// WebhookHandler serves POST /webhook on the ctf-juice network (§6.1, §4).
//
// Juice Shop POSTs here on every solve. The body is NOT believed: anything
// on ctf-juice can reach this port, so a webhook only means "poll now". The
// one thing taken from it is the cheat score, which is shown to the operator
// and never scores, and only the first value per challenge is kept.
func (a *Agent) WebhookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var hook struct {
			Solution struct {
				Challenge  string   `json:"challenge"`
				CheatScore *float64 `json:"cheatScore"`
			} `json:"solution"`
		}
		if json.Unmarshal(body, &hook) == nil && hook.Solution.CheatScore != nil && hook.Solution.Challenge != "" {
			a.mu.Lock()
			if a.st.Registration != nil && len(a.st.Cheat) < 1000 {
				if _, seen := a.st.Cheat[hook.Solution.Challenge]; !seen {
					a.st.Cheat[hook.Solution.Challenge] = *hook.Solution.CheatScore
				}
			}
			a.mu.Unlock()
		}
		a.TriggerPoll()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// StateView is GET /state: what the status page shows.
type StateView struct {
	Instance           string                 `json:"instance"`
	Registration       *protocol.Registration `json:"registration"`
	Solved             int                    `json:"solved"`
	Total              int                    `json:"total"`
	Categories         []Category             `json:"categories"`
	Recent             []SolveView            `json:"recent"`
	Standing           *protocol.Standing     `json:"standing,omitempty"`
	LastReportAt       *time.Time             `json:"last_report_at,omitempty"`
	JuiceShopReachable bool                   `json:"juiceshop_reachable"`
	RestoreFailed      bool                   `json:"restore_failed"`
}

// SolveView is one solved challenge.
type SolveView struct {
	Name       string    `json:"name"`
	Difficulty int       `json:"difficulty"`
	SolvedAt   time.Time `json:"solved_at"`
}

// View returns the state as the status page sees it.
func (a *Agent) View() StateView {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.st
	v := StateView{
		Instance:           a.cfg.InstanceID,
		Standing:           st.Standing,
		LastReportAt:       st.LastReportAt,
		JuiceShopReachable: a.reachable,
		RestoreFailed:      st.RestoreFailed,
		Recent:             []SolveView{},
	}
	if st.Registration != nil {
		r := *st.Registration
		v.Registration = &r
	}
	v.Solved, v.Total, v.Categories = Summary(st, a.catalogue)
	if st.Registration != nil {
		for _, c := range a.catalogue {
			if t, ok := st.Solved[c.Key]; ok {
				v.Recent = append(v.Recent, SolveView{Name: c.Name, Difficulty: c.Difficulty, SolvedAt: t})
			}
		}
	}
	sort.Slice(v.Recent, func(i, j int) bool { return v.Recent[i].SolvedAt.After(v.Recent[j].SolvedAt) })
	if len(v.Recent) > 10 {
		v.Recent = v.Recent[:10]
	}
	return v
}

// APIHandler is the internal API on ctf-internal (§9), for ctf-status only.
// Everything but /healthz needs CTF_INTERNAL_TOKEN.
//
//	GET  /healthz       204, no token (the container's own healthcheck)
//	GET  /state         StateView
//	GET  /registration  the current registration, or 404
//	POST /registration  {"name": "..."} → 201, 400 invalid name, 409 already registered
func (a *Agent) APIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !httpx.EqualSecret(httpx.BearerToken(r), a.cfg.InternalToken) {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /state", auth(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, a.View())
	}))
	mux.HandleFunc("GET /registration", auth(func(w http.ResponseWriter, r *http.Request) {
		reg := a.Registration()
		if reg == nil {
			httpx.WriteError(w, http.StatusNotFound, "no registration")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, reg)
	}))
	mux.HandleFunc("POST /registration", auth(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "body must be {\"name\": \"...\"}")
			return
		}
		reg, err := a.Register(in.Name)
		switch {
		case errors.Is(err, ErrRegistered):
			httpx.WriteError(w, http.StatusConflict, err.Error())
		case errors.Is(err, ErrBadName):
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
		case err != nil:
			httpx.WriteError(w, http.StatusInternalServerError, "registration failed")
		default:
			httpx.WriteJSON(w, http.StatusCreated, reg)
		}
	}))
	return mux
}
