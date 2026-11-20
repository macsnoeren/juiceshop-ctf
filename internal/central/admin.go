// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package central

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// The operator pages. Every one of them needs a
// logged-in admin account (auth.go; the setup page makes the first), and
// every form carries a CSRF token as well, so a page in the operator's
// browser cannot make them clear a game. Every action is written to the
// event feed, once, with the account that did it. An IP allow-list in the
// reverse proxy in front of <base>/admin/ is a good second lock.
//
//	GET  <base>/admin/                                games, settings
//	POST <base>/admin/games                           create
//	GET  <base>/admin/games/{id}                      a game, its join code and its participants
//	GET  <base>/admin/games/{id}/beamer               the board AND the join code, full screen
//	GET  <base>/admin/games/{id}/board.json           what that beamer refreshes from
//	POST <base>/admin/games/{id}/edit                 name, window, scoring, board options
//	POST <base>/admin/games/{id}/state                open | running | closed (closing: type the name)
//	POST <base>/admin/games/{id}/joining              to=closed | open: new students out or in again
//	POST <base>/admin/games/{id}/code                 a new join code
//	POST <base>/admin/games/{id}/clear                every participant out, VMs told forget (type the name)
//	POST <base>/admin/games/{id}/delete               the same, and the game (type the name)
//	GET  <base>/admin/games/{id}/export.csv           the participants
//	POST <base>/admin/participants/{id}/{action}      hide | show | remove (type the game's name) | delete
//	GET  <base>/admin/events[.csv]                    the feed, ?game= &participant= &kind=
//	POST <base>/admin/settings                        title, time zone, retention

const adminCSRFCookie = "ctf_admin_csrf"

func (s *Server) adminRoutes(mux *http.ServeMux) {
	b := s.cfg.BasePath + "/admin"
	mux.HandleFunc("GET "+b, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, b+"/", http.StatusFound) })
	mux.HandleFunc("GET "+b+"/{$}", s.requireUser(func(w http.ResponseWriter, r *http.Request) { s.adminHome(w, r, http.StatusOK, "") }))
	mux.HandleFunc("POST "+b+"/games", s.action(s.handleCreateGame))
	mux.HandleFunc("GET "+b+"/games/{id}", s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if g, ok := s.adminGame(w, r); ok {
			s.gamePage(w, r, g, http.StatusOK, flash{})
		}
	}))
	mux.HandleFunc("POST "+b+"/games/{id}/{action}", s.action(s.handleGameAction))
	mux.HandleFunc("GET "+b+"/games/{id}/export.csv", s.requireUser(s.handleExport))
	mux.HandleFunc("GET "+b+"/games/{id}/beamer", s.requireUser(s.handleAdminBeamer))
	mux.HandleFunc("GET "+b+"/games/{id}/board.json", s.requireUser(s.handleAdminBoardJSON))
	mux.HandleFunc("POST "+b+"/participants/{id}/{action}", s.action(s.handleParticipantAction))
	mux.HandleFunc("GET "+b+"/events", s.requireUser(s.handleEvents))
	mux.HandleFunc("GET "+b+"/events.csv", s.requireUser(s.handleEventsCSV))
	mux.HandleFunc("POST "+b+"/settings", s.action(s.handleSettings))
	s.authRoutes(mux)
}

// abase is pageBase for an admin page: the account, the CSRF token for the
// log-out button in the bar, and which part of admin is open.
func (s *Server) abase(w http.ResponseWriter, r *http.Request, g *store.Game) pageBase {
	pb := s.base(g)
	pb.CSRF = s.adminCSRF(w, r)
	if u := currentUser(r); u != nil {
		pb.Me = u.Username
	}
	switch p := r.URL.Path; {
	case strings.Contains(p, "/admin/events"):
		pb.Section = "events"
	case strings.Contains(p, "/admin/users"), strings.Contains(p, "/admin/account"):
		pb.Section = "users"
	default:
		pb.Section = "games"
	}
	return pb
}

// csrfLife is how long an admin form stays valid.
const csrfLife = 24 * time.Hour

// The CSRF token of an admin form is an httpx.CSRF token BOUND to the
// session cookie (to its hash, which is what the database knows too): a
// cookie that another site on the same domain planted is worthless, because
// nobody but this server can make a token for it, and nobody but the logged
// in browser has the session it is bound to. Before a login (the login and
// setup pages) there is no session, and the token is bound to a random
// cookie of its own; Sec-Fetch-Site keeps other sites out there too.
func (s *Server) csrfBinding(r *http.Request) (string, bool) {
	if c, err := r.Cookie(sessionCookie); err == nil && len(c.Value) == 64 {
		return "s:" + protocol.Hash(c.Value), true
	}
	if c, err := r.Cookie(adminCSRFCookie); err == nil && len(c.Value) == 32 {
		return "c:" + c.Value, true
	}
	return "", false
}

// adminCSRF is the token for the forms of this page.
func (s *Server) adminCSRF(w http.ResponseWriter, r *http.Request) string {
	b, ok := s.csrfBinding(r)
	if !ok {
		v := httpx.RandomHex(16)
		http.SetCookie(w, &http.Cookie{
			Name: adminCSRFCookie, Value: v, Path: s.cfg.BasePath + "/admin/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure(r),
		})
		b = "c:" + v
	}
	return s.csrf.Token(b, s.now())
}

func (s *Server) csrfCheck(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if httpx.CrossOrigin(r) {
			http.Error(w, "this form was sent from another site", http.StatusForbidden)
			return
		}
		b, ok := s.csrfBinding(r)
		if !ok || !s.csrf.Valid(r.PostFormValue("csrf"), b, s.now()) {
			http.Error(w, "form expired: reload the admin page and try again", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// admin writes an admin action to the feed, with who did it and from where.
func (s *Server) admin(r *http.Request, e store.Event) {
	e.Kind = store.EvAdmin
	if u := currentUser(r); u != nil {
		e.Actor = u.Username
	}
	e.Address = s.clientIP(r)
	s.event(e)
}

type flash struct {
	Message string
	Error   string
}

type adminHomeData struct {
	pageBase
	Error    string
	Games    []store.GameSummary
	Settings store.Settings
	Scoring  []scoringRow
	Now      time.Time
}

type scoringRow struct{ Difficulty, Points int }

func scoringRows(sc map[int]int) []scoringRow {
	var out []scoringRow
	for d := 1; d <= 6; d++ {
		out = append(out, scoringRow{d, sc[d]})
	}
	return out
}

func (s *Server) adminHome(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	gs, err := s.st.Games(s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	set, err := s.st.Settings()
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, status, "admin", adminHomeData{pageBase: s.abase(w, r, nil), Error: errMsg,
		Games: gs, Settings: set, Scoring: scoringRows(store.DefaultScoring), Now: s.now()})
}

// parseTime reads a datetime-local field in the operator's time zone.
func (s *Server) parseTime(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation("2006-01-02T15:04", v, s.location())
	if err != nil {
		return nil, fmt.Errorf("a time is written as 2026-11-19T09:00")
	}
	return &t, nil
}

func (s *Server) gameInput(r *http.Request) (store.GameInput, error) {
	in := store.GameInput{Name: r.PostFormValue("name"), BoardHidden: r.PostFormValue("hidden") == "on",
		BoardInitials: r.PostFormValue("initials") == "on", Scoring: map[int]int{}}
	var err error
	if in.StartsAt, err = s.parseTime(r.PostFormValue("starts_at")); err != nil {
		return in, err
	}
	if in.EndsAt, err = s.parseTime(r.PostFormValue("ends_at")); err != nil {
		return in, err
	}
	for d := 1; d <= 6; d++ {
		v := strings.TrimSpace(r.PostFormValue("points" + strconv.Itoa(d)))
		if v == "" {
			in.Scoring[d] = store.DefaultScoring[d]
			continue
		}
		p, err := strconv.Atoi(v)
		if err != nil {
			return in, fmt.Errorf("points must be whole numbers")
		}
		in.Scoring[d] = p
	}
	return in, nil
}

func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	in, err := s.gameInput(r)
	if err != nil {
		s.adminHome(w, r, http.StatusBadRequest, err.Error())
		return
	}
	g, _, err := s.st.CreateGame(in, s.now())
	if err != nil {
		s.adminHome(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.admin(r, store.Event{GameID: g.ID, Text: "game created: " + g.Name + " (draft)"})
	s.gamePage(w, r, g, http.StatusOK, flash{Message: "Game created, in draft: students cannot join until you open it."})
}

func (s *Server) adminGame(w http.ResponseWriter, r *http.Request) (*store.Game, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	g, err := s.st.Game(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	return g, true
}

type gamePageData struct {
	pageBase
	Flash        flash
	G            *store.Game
	Participants []*store.Participant
	Scoring      []scoringRow
	Counts       map[string]int
	BoardURL     string
	Events       []store.Event
	Now          time.Time
}

func (s *Server) gamePage(w http.ResponseWriter, r *http.Request, g *store.Game, status int, f flash) {
	now := s.now()
	ps, err := s.st.GameParticipants(g, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	counts := map[string]int{}
	for _, p := range ps {
		counts[p.Status()]++
	}
	evs, err := s.st.Events(store.EventFilter{GameID: g.ID, Limit: 15})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, status, "admin-game", gamePageData{pageBase: s.abase(w, r, g), Flash: f, G: g,
		Participants: ps, Scoring: scoringRows(g.Scoring), Counts: counts, BoardURL: s.boardURL(r, g.Slug), Events: evs, Now: now})
}

// confirmed checks the typed confirmation of a destructive action: the
// game's name, exactly (surrounding spaces aside).
func confirmed(r *http.Request, g *store.Game) bool {
	return strings.TrimSpace(r.PostFormValue("confirm")) == g.Name
}

func (s *Server) handleGameAction(w http.ResponseWriter, r *http.Request) {
	g, ok := s.adminGame(w, r)
	if !ok {
		return
	}
	now := s.now()
	back := func(msg string) {
		g2, err := s.st.Game(g.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.gamePage(w, r, g2, http.StatusOK, flash{Message: msg})
	}
	refuse := func(status int, msg string) { s.gamePage(w, r, g, status, flash{Error: msg}) }
	needName := "Type the game's name exactly to confirm; nothing was changed."

	switch r.PathValue("action") {
	case "edit":
		in, err := s.gameInput(r)
		if err == nil {
			err = s.st.UpdateGame(g.ID, in)
		}
		if err != nil {
			refuse(http.StatusBadRequest, err.Error())
			return
		}
		s.admin(r, store.Event{GameID: g.ID, Text: "game edited: " + in.Name})
		back("Saved.")
	case "state":
		to := r.PostFormValue("to")
		if to == protocol.StateClosed && !confirmed(r, g) {
			refuse(http.StatusBadRequest, needName)
			return
		}
		if err := s.st.SetState(g.ID, to, now); err != nil {
			refuse(http.StatusConflict, strings.TrimPrefix(err.Error(), store.ErrConflict.Error()+": "))
			return
		}
		what := map[string]string{protocol.StateOpen: "opened for joining", protocol.StateRunning: "started (running)", protocol.StateClosed: "ended (closed): no joins, no solves counted, board frozen"}[to]
		s.admin(r, store.Event{GameID: g.ID, Text: "game " + what})
		back("The game is now " + to + ".")
	case "joining":
		closed := r.PostFormValue("to") == "closed"
		if !closed && r.PostFormValue("to") != "open" {
			refuse(http.StatusBadRequest, "Joining can be closed or open.")
			return
		}
		if err := s.st.SetJoiningClosed(g.ID, closed); err != nil {
			s.fail(w, err)
			return
		}
		if closed {
			s.admin(r, store.Event{GameID: g.ID, Text: "joining closed: nobody new can join; those in the game play on"})
			back("Joining is closed. Those in the game play on; the beamer no longer shows the code.")
		} else {
			s.admin(r, store.Event{GameID: g.ID, Text: "joining opened again"})
			back("Joining is open again.")
		}
	case "code":
		if _, err := s.st.RegenerateCode(g.ID); err != nil {
			s.fail(w, err)
			return
		}
		s.admin(r, store.Event{GameID: g.ID, Text: "join code regenerated; the old code no longer admits anyone"})
		back("New join code. The old one no longer works; students who joined keep playing.")
	case "clear":
		if !confirmed(r, g) {
			refuse(http.StatusBadRequest, needName)
			return
		}
		n, err := s.st.ClearGame(g.ID, now)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.admin(r, store.Event{GameID: g.ID, Text: fmt.Sprintf("game cleared: %d participants and their solves deleted, every VM told forget", n)})
		back(fmt.Sprintf("Cleared: %d participants deleted. Their VMs forget at their next report.", n))
	case "delete":
		if !confirmed(r, g) {
			refuse(http.StatusBadRequest, needName)
			return
		}
		if err := s.st.DeleteGame(g.ID, now); err != nil {
			s.fail(w, err)
			return
		}
		s.admin(r, store.Event{Text: "game deleted: " + g.Name + " (its VMs told forget)"})
		http.Redirect(w, r, s.cfg.BasePath+"/admin/", http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

// joinInfo is what the beamer in admin shows of joining: the code while a
// student can join with it, nothing otherwise.
type joinInfo struct {
	Show bool   `json:"show"`
	Code string `json:"code,omitempty"`
}

func joinOf(g *store.Game, now time.Time) *joinInfo {
	if g.Code == "" || !g.Joinable(now) {
		return &joinInfo{}
	}
	return &joinInfo{Show: true, Code: protocol.FormatCode(g.Code)}
}

// handleAdminBeamer is the public beamer with the join code on it, for the
// one screen in the classroom. It is in admin, behind the login, because the
// public board is reachable by anybody: the code there would let anyone in.
// It works for a draft or a hidden board too, so it can be up before the
// game opens; the code appears when it does.
func (s *Server) handleAdminBeamer(w http.ResponseWriter, r *http.Request) {
	g, ok := s.adminGame(w, r)
	if !ok {
		return
	}
	now := s.now()
	b, err := s.st.Board(g, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "beamer", beamerPage{boardPage{s.base(g), b},
		s.adminPath(fmt.Sprintf("/games/%d/board.json", g.ID)), joinOf(g, now)})
}

// handleAdminBoardJSON is board.json with "join" added, so the beamer in
// admin follows the operator closing joining or making a new code.
func (s *Server) handleAdminBoardJSON(w http.ResponseWriter, r *http.Request) {
	g, ok := s.adminGame(w, r)
	if !ok {
		return
	}
	now := s.now()
	b, err := s.st.Board(g, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		*store.Board
		Join *joinInfo `json:"join"`
	}{b, joinOf(g, now)})
}

func (s *Server) handleParticipantAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.st.Participant(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	g, err := s.st.Game(p.GameID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var msg string
	switch r.PathValue("action") {
	case "hide", "show":
		hide := r.PathValue("action") == "hide"
		if err := s.st.SetHidden(id, hide); err != nil {
			s.fail(w, err)
			return
		}
		msg = map[bool]string{true: "hidden from the board", false: "shown on the board again"}[hide]
	case "remove":
		if !confirmed(r, g) {
			s.gamePage(w, r, g, http.StatusBadRequest, flash{Error: "Type the game's name exactly to remove " + p.Name + "; nothing was changed."})
			return
		}
		if err := s.st.Remove(id, s.now()); err != nil {
			s.fail(w, err)
			return
		}
		msg = "removed: off the board, the token stops working, the VM is told forget"
	case "delete":
		if err := s.st.DeleteParticipant(id); err != nil {
			s.gamePage(w, r, g, http.StatusConflict, flash{Error: strings.TrimPrefix(err.Error(), store.ErrConflict.Error()+": ")})
			return
		}
		msg = "deleted, with their solves"
	default:
		http.NotFound(w, r)
		return
	}
	s.admin(r, store.Event{GameID: g.ID, ParticipantID: id, Text: "participant " + p.Name + " " + msg})
	g2, _ := s.st.Game(g.ID)
	s.gamePage(w, r, g2, http.StatusOK, flash{Message: p.Name + ": " + msg + "."})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.PostFormValue("retention"))
	in := store.Settings{Title: r.PostFormValue("title"), Timezone: strings.TrimSpace(r.PostFormValue("timezone")), RetentionDays: days}
	if err := s.st.SaveSettings(in); err != nil {
		s.adminHome(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.reloadSettings()
	s.admin(r, store.Event{Text: fmt.Sprintf("settings changed: title %q, time zone %s, events kept %d days", in.Title, in.Timezone, in.RetentionDays)})
	http.Redirect(w, r, s.cfg.BasePath+"/admin/", http.StatusSeeOther)
}

// csvSafe keeps a spreadsheet from reading a cell as a formula (1.0's rule).
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func csvTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	g, ok := s.adminGame(w, r)
	if !ok {
		return
	}
	ps, err := s.st.GameParticipants(g, s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+g.Slug+`.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"participant", "name", "status", "rank", "score", "solved", "coding", "joined_at", "left_at", "last_report_at", "node_version", "avg_cheat", "flags"})
	for _, p := range ps {
		avg := ""
		if p.AvgCheat != nil {
			avg = strconv.FormatFloat(*p.AvgCheat, 'f', 3, 64)
		}
		var fl []string
		for _, f := range p.Flags {
			fl = append(fl, f.Text)
		}
		cw.Write([]string{strconv.FormatInt(p.ID, 10), csvSafe(p.Name), p.Status(), strconv.Itoa(p.Rank), strconv.Itoa(p.Score),
			strconv.Itoa(p.Solved), strconv.Itoa(p.Coding), csvTime(&p.JoinedAt), csvTime(p.LeftAt), csvTime(p.LastReportAt), csvSafe(p.NodeVersion), avg, csvSafe(strings.Join(fl, "; "))})
	}
	cw.Flush()
}

func (s *Server) eventFilter(r *http.Request) store.EventFilter {
	q := r.URL.Query()
	f := store.EventFilter{Kind: q.Get("kind")}
	f.GameID, _ = strconv.ParseInt(q.Get("game"), 10, 64)
	f.ParticipantID, _ = strconv.ParseInt(q.Get("participant"), 10, 64)
	return f
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	f := s.eventFilter(r)
	evs, err := s.st.Events(f)
	if err != nil {
		s.fail(w, err)
		return
	}
	gs, err := s.st.Games(s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "admin-events", struct {
		pageBase
		F      store.EventFilter
		Events []store.Event
		Games  []store.GameSummary
		Kinds  []string
		Query  string
	}{s.abase(w, r, nil), f, evs, gs, store.EventKinds, r.URL.RawQuery})
}

func (s *Server) handleEventsCSV(w http.ResponseWriter, r *http.Request) {
	f := s.eventFilter(r)
	f.Limit = 5000
	evs, err := s.st.Events(f)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="juiceshop-ctf-events.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"at", "kind", "game", "participant", "by", "address", "text"})
	for _, e := range evs {
		cw.Write([]string{e.At.UTC().Format(time.RFC3339), e.Kind, csvSafe(e.GameName), csvSafe(e.ParticipantName), csvSafe(e.Actor), csvSafe(e.Address), csvSafe(e.Text)})
	}
	cw.Flush()
}
