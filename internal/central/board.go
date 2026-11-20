// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package central

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// The public pages of a game. All server-rendered; one
// small script (static/board.js) refreshes the board every ten seconds from
// board.json. A draft game and a game with a hidden board are 404 on every
// one of them, the JSON included.
//
//	GET <base>/                         the public games
//	GET <base>/g/<slug>/                the board: podium, rows, ticker, chart
//	GET <base>/g/<slug>/board.json      what the script refreshes from
//	GET <base>/g/<slug>/chart.svg       score over time, top 10
//	GET <base>/g/<slug>/beamer          full screen, countdown, final result
//	GET <base>/g/<slug>/p/<id>          a participant
//	GET <base>/g/<slug>/c/<key>         a challenge: who solved it, in order
//	GET <base>/g/<slug>/cat/<slug>      a category: the challenge matrix

func (s *Server) boardRoutes(mux *http.ServeMux) {
	b := s.cfg.BasePath
	mux.HandleFunc("GET "+b+"/{$}", s.handleIndex)
	mux.HandleFunc("GET "+b+"/g/{slug}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, b+"/g/"+r.PathValue("slug")+"/", http.StatusFound)
	})
	mux.HandleFunc("GET "+b+"/g/{slug}/{$}", s.handleBoard)
	mux.HandleFunc("GET "+b+"/g/{slug}/board.json", s.handleBoardJSON)
	mux.HandleFunc("GET "+b+"/g/{slug}/chart.svg", s.handleChart)
	mux.HandleFunc("GET "+b+"/g/{slug}/beamer", s.handleBeamer)
	mux.HandleFunc("GET "+b+"/g/{slug}/p/{id}", s.handleParticipant)
	mux.HandleFunc("GET "+b+"/g/{slug}/c/{key}", s.handleChallenge)
	mux.HandleFunc("GET "+b+"/g/{slug}/cat/{cat}", s.handleCategory)
}

func (s *Server) funcs() template.FuncMap {
	loc := func() *time.Location { return s.location() }
	return template.FuncMap{
		"path":  func(p string) string { return s.cfg.BasePath + p },
		"title": func() string { return s.siteTitle() },
		"hm":    func(t time.Time) string { return t.In(loc()).Format("15:04") },
		"clock": func(t *time.Time) string {
			if t == nil {
				return "–"
			}
			return t.In(loc()).Format("15:04")
		},
		"datetime": func(t *time.Time) string {
			if t == nil {
				return "–"
			}
			return t.In(loc()).Format("2006-01-02 15:04")
		},
		"dtlocal": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.In(loc()).Format("2006-01-02T15:04")
		},
		"iso": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.UTC().Format(time.RFC3339)
		},
		"stars": func(n int) string { return strings.Repeat("★", n) },
		"pct": func(f *float64) string {
			if f == nil {
				return "–"
			}
			return strconv.Itoa(int(*f*100+0.5)) + "%"
		},
		"code":   protocol.FormatCode,
		"add":    func(a, b int) int { return a + b },
		"catbar": catbar,
		"bar": func(counts []int, cats []store.BoardCategory, total, solved int) barArgs {
			return barArgs{Counts: counts, Cats: cats, Total: max(total, 1), Solved: solved}
		},
		"since": func(t *time.Time) string {
			if t == nil {
				return "never"
			}
			d := s.now().Sub(*t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return fmt.Sprintf("%d min ago", int(d.Minutes()))
			case d < 48*time.Hour:
				return fmt.Sprintf("%d h ago", int(d.Hours()))
			}
			return t.In(loc()).Format("2006-01-02")
		},
		"remaining": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			d := t.Sub(s.now())
			if d <= 0 {
				return "0:00"
			}
			h, m, sec := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
			if h > 0 {
				return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
			}
			return fmt.Sprintf("%d:%02d", m, sec)
		},
		"online":  func(p *store.Participant) bool { return p.Online(s.now()) },
		"slugify": store.Slugify,
		"ptr":     func(t time.Time) *time.Time { return &t },
	}
}

// barSeg is one category's part of a row's category bar: an SVG rect whose
// width is the number solved in that category, on an axis of every
// challenge — so the filled length is the share solved, and its colours say
// where it came from. SVG attributes, not inline style: the CSP forbids that.
type barSeg struct {
	X, W, Index, Total int
	Name               string
}

type barArgs struct {
	Counts        []int
	Cats          []store.BoardCategory
	Total, Solved int
}

func catbar(counts []int, cats []store.BoardCategory) []barSeg {
	var out []barSeg
	x := 0
	for i, n := range counts {
		if n == 0 || i >= len(cats) {
			continue
		}
		out = append(out, barSeg{X: x, W: n, Index: cats[i].Index % 16, Name: cats[i].Name, Total: cats[i].Total})
		x += n
	}
	return out
}

func (s *Server) publicGame(w http.ResponseWriter, r *http.Request) (*store.Game, bool) {
	g, err := s.st.GameBySlug(r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !g.Public()) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	return g, true
}

type pageBase struct {
	Title string
	Game  *store.Game
	// GBase is the game's path, for links: <base>/g/<slug>/
	GBase string
	Ended bool
	State string

	// Admin pages only (abase): the logged-in account, the CSRF token for
	// the log-out button, and the open part of admin for the bar.
	Me      string
	CSRF    string
	Section string
}

func (s *Server) base(g *store.Game) pageBase {
	now := s.now()
	pb := pageBase{Title: s.siteTitle(), Game: g}
	if g != nil {
		pb.GBase = s.cfg.BasePath + "/g/" + g.Slug + "/"
		pb.Ended = g.Ended(now)
		pb.State = g.EffectiveState(now)
	}
	return pb
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	gs, err := s.st.Games(s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	var live, done []store.GameSummary
	for _, g := range gs {
		if !g.Public() {
			continue
		}
		if g.Ended(s.now()) {
			done = append(done, g)
		} else {
			live = append(live, g)
		}
	}
	s.render(w, http.StatusOK, "index", struct {
		pageBase
		Live, Done []store.GameSummary
	}{s.base(nil), live, done})
}

type boardPage struct {
	pageBase
	B *store.Board
}

// beamerPage is the beamer, public or in admin.
type beamerPage struct {
	boardPage
	JSON string    // what board.js refreshes from
	Join *joinInfo // nil on the public beamer, which NEVER shows the code
}

func (s *Server) board(w http.ResponseWriter, r *http.Request) (*store.Game, *store.Board, bool) {
	g, ok := s.publicGame(w, r)
	if !ok {
		return nil, nil, false
	}
	b, err := s.st.Board(g, s.now())
	if err != nil {
		s.fail(w, err)
		return nil, nil, false
	}
	return g, b, true
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	if g, b, ok := s.board(w, r); ok {
		s.render(w, http.StatusOK, "board", boardPage{s.base(g), b})
	}
}

func (s *Server) handleBeamer(w http.ResponseWriter, r *http.Request) {
	if g, b, ok := s.board(w, r); ok {
		s.render(w, http.StatusOK, "beamer", beamerPage{boardPage{s.base(g), b}, s.base(g).GBase + "board.json", nil})
	}
}

func (s *Server) handleBoardJSON(w http.ResponseWriter, r *http.Request) {
	if _, b, ok := s.board(w, r); ok {
		httpx.WriteJSON(w, http.StatusOK, b)
	}
}

func (s *Server) handleChart(w http.ResponseWriter, r *http.Request) {
	_, b, ok := s.board(w, r)
	if !ok {
		return
	}
	// The chart is its own document, loaded as an <img>: it carries its own
	// <style> (light and dark) and can run nothing.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.pages.ExecuteTemplate(w, "chart.svg", buildChart(b, s.location())); err != nil {
		s.log.Error("template", "page", "chart.svg", "err", err)
	}
}

func (s *Server) handleParticipant(w http.ResponseWriter, r *http.Request) {
	g, ok := s.publicGame(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pp, err := s.st.ParticipantPage(g, id, s.now())
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	spread := make([]struct{ Stars, N int }, 6)
	for i, n := range pp.Difficulty {
		spread[i].Stars, spread[i].N = i+1, n
	}
	s.render(w, http.StatusOK, "participant", struct {
		pageBase
		P      *store.ParticipantPage
		Spread []struct{ Stars, N int }
	}{s.base(g), pp, spread})
}

func (s *Server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	g, ok := s.publicGame(w, r)
	if !ok {
		return
	}
	cp, err := s.st.ChallengePage(g, r.PathValue("key"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "challenge", struct {
		pageBase
		C *store.ChallengePage
	}{s.base(g), cp})
}

func (s *Server) handleCategory(w http.ResponseWriter, r *http.Request) {
	g, ok := s.publicGame(w, r)
	if !ok {
		return
	}
	cp, err := s.st.CategoryPage(g, r.PathValue("cat"), s.now())
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "category", struct {
		pageBase
		C *store.CategoryPage
	}{s.base(g), cp})
}
