// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package store

import (
	"database/sql"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// The board of a game is computed from two queries — the game's participants
// and their solves joined with the catalogue — and then in Go, so the rules
// read as code rather than as SQL:
//
//   - on the board: participants who have not left and are not hidden;
//   - a solve scores when it was counted (received while the game accepted
//     solves) and its challenge is not disabled in Docker;
//   - rank by score; equal scores by WHEN the score was reached — the
//     receipt of the last scoring solve, or the join for 0 — first there is
//     ahead; equal score at the same moment is the same rank;
//   - first blood of a challenge is the scoring solve RECEIVED first among
//     those on the board. A VM cannot backdate it, and a later report never
//     takes it away. It is a mark, worth no points.

type solveRow struct {
	pid        int64
	key        string
	name       string
	category   string
	difficulty int
	solvedAt   time.Time
	receivedAt time.Time
	cheat      *float64
	scores     bool
	points     int
}

type gameData struct {
	game   *Game
	parts  []*Participant
	byID   map[int64]*Participant
	solves []solveRow // by receipt
	cats   []BoardCategory
	total  int
	chals  []Challenge
}

// Challenge is a challenge of the catalogue as the board shows it.
type Challenge struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	CategorySlug string `json:"category_slug"`
	Difficulty   int    `json:"difficulty"`
	Points       int    `json:"points"`
}

func (s *Store) load(g *Game) (*gameData, error) {
	d := &gameData{game: g, byID: map[int64]*Participant{}}
	rows, err := s.db.Query(`SELECT `+participantCols+` FROM participants WHERE game_id = ? ORDER BY id`, g.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		d.parts = append(d.parts, p)
		d.byID[p.ID] = p
	}
	rows.Close()

	crow, err := s.db.Query(`SELECT key, name, category, difficulty FROM challenges WHERE disabled = 0 ORDER BY category, difficulty, name`)
	if err != nil {
		return nil, err
	}
	catIdx := map[string]int{}
	for crow.Next() {
		var c Challenge
		if err := crow.Scan(&c.Key, &c.Name, &c.Category, &c.Difficulty); err != nil {
			crow.Close()
			return nil, err
		}
		c.CategorySlug = Slugify(c.Category)
		c.Points = g.Scoring[c.Difficulty]
		d.chals = append(d.chals, c)
		i, ok := catIdx[c.Category]
		if !ok {
			i = len(d.cats)
			catIdx[c.Category] = i
			d.cats = append(d.cats, BoardCategory{Name: c.Category, Slug: c.CategorySlug, Index: i})
		}
		d.cats[i].Total++
		d.total++
	}
	crow.Close()

	srow, err := s.db.Query(`SELECT s.participant_id, s.challenge_key, c.name, c.category, c.difficulty,
		s.solved_at, s.received_at, s.cheat, s.counted AND c.disabled = 0
		FROM solves s JOIN participants p ON p.id = s.participant_id
		JOIN challenges c ON c.key = s.challenge_key
		WHERE p.game_id = ? ORDER BY s.received_at, s.solved_at, s.participant_id, s.challenge_key`, g.ID)
	if err != nil {
		return nil, err
	}
	defer srow.Close()
	for srow.Next() {
		var r solveRow
		var solved, received string
		var cheat sql.NullFloat64
		if err := srow.Scan(&r.pid, &r.key, &r.name, &r.category, &r.difficulty, &solved, &received, &cheat, &r.scores); err != nil {
			return nil, err
		}
		r.solvedAt, r.receivedAt = parseTS(solved), parseTS(received)
		if cheat.Valid {
			v := cheat.Float64
			r.cheat = &v
		}
		if r.scores {
			r.points = g.Scoring[r.difficulty]
		}
		d.solves = append(d.solves, r)
	}
	return d, srow.Err()
}

func onBoard(p *Participant) bool { return p.LeftAt == nil && !p.Hidden }

type ranked struct {
	p       *Participant
	score   int
	solved  int
	reached time.Time
	last    *time.Time
	rank    int
	byCat   map[string]int
	bloods  int
}

// rank computes the standings of the participants on the board.
func (d *gameData) rank() ([]*ranked, map[string]int64) {
	byID := map[int64]*ranked{}
	var out []*ranked
	for _, p := range d.parts {
		if !onBoard(p) {
			continue
		}
		r := &ranked{p: p, reached: p.JoinedAt, byCat: map[string]int{}}
		byID[p.ID] = r
		out = append(out, r)
	}
	first := map[string]int64{}
	for _, sv := range d.solves { // in order of receipt
		r := byID[sv.pid]
		if r == nil || !sv.scores {
			continue
		}
		r.score += sv.points
		r.solved++
		r.byCat[sv.category]++
		t := sv.receivedAt
		r.reached, r.last = t, &t
		if _, taken := first[sv.key]; !taken {
			first[sv.key] = sv.pid
			r.bloods++
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if !a.reached.Equal(b.reached) {
			return a.reached.Before(b.reached)
		}
		if a.p.Name != b.p.Name {
			return a.p.Name < b.p.Name
		}
		return a.p.ID < b.p.ID
	})
	for i, r := range out {
		r.rank = i + 1
		if i > 0 && out[i-1].score == r.score && out[i-1].reached.Equal(r.reached) {
			r.rank = out[i-1].rank
		}
	}
	return out, first
}

// StandingOf returns a participant's place on their game's board, or nil
// when they are not on it.
func (s *Store) StandingOf(g *Game, pid int64, now time.Time) (*protocol.Standing, error) {
	d, err := s.load(g)
	if err != nil {
		return nil, err
	}
	rs, _ := d.rank()
	for _, r := range rs {
		if r.p.ID == pid {
			return &protocol.Standing{Rank: r.rank, Of: len(rs), Score: r.score}, nil
		}
	}
	return nil, nil
}

// Initials turns "Zoë de Vries" into "Z.D.V.", for a game whose board shows
// no names.
func Initials(name string) string {
	var b strings.Builder
	for _, w := range strings.Fields(name) {
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(unicode.ToUpper(r))
				b.WriteByte('.')
				break
			}
		}
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

func (d *gameData) shown(p *Participant) string {
	if d.game.BoardInitials {
		return Initials(p.Name)
	}
	return p.Name
}

// BoardCategory is one category column of a board.
type BoardCategory struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Total int    `json:"total"`
	// Index picks the category's colour (style.css .cat-0 … .cat-15); it
	// is the position in the alphabetical list, so a colour means the same
	// category on every board.
	Index int `json:"index"`
}

// BoardGame is the game on its board.
type BoardGame struct {
	Name     string     `json:"name"`
	Slug     string     `json:"slug"`
	State    string     `json:"state"`
	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`
}

// BoardRow is one participant on a board.
type BoardRow struct {
	Rank        int        `json:"rank"`
	Participant int64      `json:"participant"`
	Name        string     `json:"name"`
	Score       int        `json:"score"`
	Solved      int        `json:"solved"`
	LastSolve   *time.Time `json:"last_solve"`
	Online      bool       `json:"online"`
	FirstBloods int        `json:"first_bloods"`
	// Categories is the number solved per category, in the order of
	// Board.Categories.
	Categories []int `json:"categories"`
}

// TickerItem is one solve in the live ticker.
type TickerItem struct {
	At            time.Time `json:"at"`
	Participant   int64     `json:"participant"`
	Name          string    `json:"name"`
	Challenge     string    `json:"challenge"`
	ChallengeName string    `json:"challenge_name"`
	Difficulty    int       `json:"difficulty"`
	Points        int       `json:"points"`
	FirstBlood    bool      `json:"first_blood"`
}

// SeriesPoint is one step of a score-over-time line.
type SeriesPoint struct {
	At    time.Time `json:"at"`
	Score int       `json:"score"`
}

// Series is one participant's score over time.
type Series struct {
	Participant int64         `json:"participant"`
	Name        string        `json:"name"`
	Points      []SeriesPoint `json:"points"`
}

// Board is everything a board page shows, and its JSON (GET …/board.json).
type Board struct {
	Game        BoardGame       `json:"game"`
	GeneratedAt time.Time       `json:"generated_at"`
	Total       int             `json:"total"`
	Categories  []BoardCategory `json:"categories"`
	Rows        []BoardRow      `json:"rows"`
	Podium      []BoardRow      `json:"podium"`
	Ticker      []TickerItem    `json:"ticker"`
	Series      []Series        `json:"series"`
	// From and To are the time axis of Series: the first join (or the
	// start) to the end, or now while the game runs.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// TickerLength and SeriesTop are how much the board shows.
const (
	TickerLength = 15
	SeriesTop    = 10
)

// Board computes the board of a game at now.
func (s *Store) Board(g *Game, now time.Time) (*Board, error) {
	d, err := s.load(g)
	if err != nil {
		return nil, err
	}
	rs, first := d.rank()
	b := &Board{
		Game:        BoardGame{Name: g.Name, Slug: g.Slug, State: g.EffectiveState(now), StartsAt: g.StartsAt, EndsAt: g.EndsAt},
		GeneratedAt: now.UTC(),
		Total:       d.total,
		Categories:  d.cats,
		Rows:        []BoardRow{},
		Podium:      []BoardRow{},
		Ticker:      []TickerItem{},
		Series:      []Series{},
	}
	for _, r := range rs {
		row := BoardRow{Rank: r.rank, Participant: r.p.ID, Name: d.shown(r.p), Score: r.score, Solved: r.solved,
			LastSolve: r.last, Online: r.p.Online(now), FirstBloods: r.bloods, Categories: make([]int, len(d.cats))}
		for i, c := range d.cats {
			row.Categories[i] = r.byCat[c.Name]
		}
		b.Rows = append(b.Rows, row)
	}
	for _, row := range b.Rows {
		if row.Rank <= 3 && len(b.Podium) < 3 {
			b.Podium = append(b.Podium, row)
		}
	}

	for i := len(d.solves) - 1; i >= 0 && len(b.Ticker) < TickerLength; i-- {
		sv := d.solves[i]
		p := d.byID[sv.pid]
		if !sv.scores || !onBoard(p) {
			continue
		}
		b.Ticker = append(b.Ticker, TickerItem{At: sv.receivedAt, Participant: p.ID, Name: d.shown(p), Challenge: sv.key,
			ChallengeName: sv.name, Difficulty: sv.difficulty, Points: sv.points, FirstBlood: first[sv.key] == p.ID})
	}

	// Score over time: the axis runs from the start (or the first join) to
	// the end (or now); each line starts at 0 at its join.
	b.To = now.UTC()
	if e := g.End(); e != nil && e.Before(now) {
		b.To = e.UTC()
	}
	b.From = b.To
	if g.StartsAt != nil {
		b.From = g.StartsAt.UTC()
	}
	for _, r := range rs {
		if r.p.JoinedAt.Before(b.From) {
			b.From = r.p.JoinedAt.UTC()
		}
	}
	for i, r := range rs {
		if i >= SeriesTop {
			break
		}
		ser := Series{Participant: r.p.ID, Name: d.shown(r.p), Points: []SeriesPoint{{At: r.p.JoinedAt.UTC(), Score: 0}}}
		score := 0
		for _, sv := range d.solves {
			if sv.pid == r.p.ID && sv.scores {
				score += sv.points
				ser.Points = append(ser.Points, SeriesPoint{At: sv.receivedAt, Score: score})
			}
		}
		b.Series = append(b.Series, ser)
	}
	return b, nil
}

// ParticipantPage is a participant's public page.
type ParticipantPage struct {
	Row        BoardRow
	Categories []CategoryProgress
	Solves     []SolveLine
	Bloods     []SolveLine
	// Difficulty is the number solved per difficulty, index 0 = one star.
	Difficulty [6]int
}

// CategoryProgress is progress in one category.
type CategoryProgress struct {
	BoardCategory
	Solved int
}

// SolveLine is one solve on a participant's or a challenge's page.
type SolveLine struct {
	Order       int
	Participant int64
	Name        string
	Challenge   Challenge
	At          time.Time
	FirstBlood  bool
}

func (d *gameData) challenge(key string) (Challenge, bool) {
	for _, c := range d.chals {
		if c.Key == key {
			return c, true
		}
	}
	return Challenge{}, false
}

// ParticipantPage returns the page of a participant on the board, or
// ErrNotFound for one who is not on it.
func (s *Store) ParticipantPage(g *Game, pid int64, now time.Time) (*ParticipantPage, error) {
	b, err := s.Board(g, now)
	if err != nil {
		return nil, err
	}
	pp := &ParticipantPage{}
	found := false
	for i, r := range b.Rows {
		if r.Participant == pid {
			pp.Row, found = b.Rows[i], true
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	for i, c := range b.Categories {
		pp.Categories = append(pp.Categories, CategoryProgress{BoardCategory: c, Solved: pp.Row.Categories[i]})
	}
	d, err := s.load(g)
	if err != nil {
		return nil, err
	}
	_, first := d.rank()
	for _, sv := range d.solves {
		if sv.pid != pid || !sv.scores {
			continue
		}
		c, ok := d.challenge(sv.key)
		if !ok {
			continue
		}
		l := SolveLine{Order: len(pp.Solves) + 1, Participant: pid, Name: pp.Row.Name, Challenge: c, At: sv.receivedAt, FirstBlood: first[sv.key] == pid}
		pp.Solves = append(pp.Solves, l)
		if l.FirstBlood {
			pp.Bloods = append(pp.Bloods, l)
		}
		if sv.difficulty >= 1 && sv.difficulty <= 6 {
			pp.Difficulty[sv.difficulty-1]++
		}
	}
	return pp, nil
}

// ChallengePage is who solved one challenge, in order.
type ChallengePage struct {
	Challenge Challenge
	Solvers   []SolveLine
}

// ChallengePage returns a challenge of the catalogue with its solvers on the
// board; ErrNotFound for a key that is not a countable challenge.
func (s *Store) ChallengePage(g *Game, key string) (*ChallengePage, error) {
	d, err := s.load(g)
	if err != nil {
		return nil, err
	}
	c, ok := d.challenge(key)
	if !ok {
		return nil, ErrNotFound
	}
	cp := &ChallengePage{Challenge: c}
	for _, sv := range d.solves {
		p := d.byID[sv.pid]
		if sv.key != key || !sv.scores || !onBoard(p) {
			continue
		}
		cp.Solvers = append(cp.Solvers, SolveLine{Order: len(cp.Solvers) + 1, Participant: p.ID, Name: d.shown(p), Challenge: c,
			At: sv.receivedAt, FirstBlood: len(cp.Solvers) == 0})
	}
	return cp, nil
}

// CategoryPage is the challenge matrix of one category.
type CategoryPage struct {
	Category   BoardCategory
	Challenges []CategoryChallenge
	Rows       []MatrixRow
}

// CategoryChallenge is one column of the matrix.
type CategoryChallenge struct {
	Challenge
	Solvers int
}

// MatrixRow is one participant's line of the matrix.
type MatrixRow struct {
	Rank        int
	Participant int64
	Name        string
	Solved      []bool
}

// CategoryPage returns the matrix of the category with this slug.
func (s *Store) CategoryPage(g *Game, slug string, now time.Time) (*CategoryPage, error) {
	d, err := s.load(g)
	if err != nil {
		return nil, err
	}
	cp := &CategoryPage{}
	found := false
	for _, c := range d.cats {
		if c.Slug == slug {
			cp.Category, found = c, true
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	col := map[string]int{}
	for _, c := range d.chals {
		if c.CategorySlug == slug {
			col[c.Key] = len(cp.Challenges)
			cp.Challenges = append(cp.Challenges, CategoryChallenge{Challenge: c})
		}
	}
	sort.SliceStable(cp.Challenges, func(i, j int) bool {
		a, b := cp.Challenges[i], cp.Challenges[j]
		if a.Difficulty != b.Difficulty {
			return a.Difficulty < b.Difficulty
		}
		return a.Name < b.Name
	})
	for i, c := range cp.Challenges {
		col[c.Key] = i
	}
	rs, _ := d.rank()
	rowOf := map[int64]int{}
	for _, r := range rs {
		rowOf[r.p.ID] = len(cp.Rows)
		cp.Rows = append(cp.Rows, MatrixRow{Rank: r.rank, Participant: r.p.ID, Name: d.shown(r.p), Solved: make([]bool, len(cp.Challenges))})
	}
	for _, sv := range d.solves {
		i, inCat := col[sv.key]
		ri, on := rowOf[sv.pid]
		if !inCat || !on || !sv.scores {
			continue
		}
		cp.Rows[ri].Solved[i] = true
		cp.Challenges[i].Solvers++
	}
	return cp, nil
}

// GameParticipants returns every participant of a game for admin — on the
// board, hidden, left and removed — with score, rank (0 when not on the
// board), cheat scores and flags.
func (s *Store) GameParticipants(g *Game, now time.Time) ([]*Participant, error) {
	d, err := s.load(g)
	if err != nil {
		return nil, err
	}
	rs, _ := d.rank()
	for _, r := range rs {
		r.p.Rank = r.rank
	}
	sum := map[int64]float64{}
	n := map[int64]int{}
	for _, sv := range d.solves {
		p := d.byID[sv.pid]
		if sv.scores {
			p.Score += sv.points
			p.Solved++
		}
		if sv.cheat != nil {
			sum[p.ID] += *sv.cheat
			n[p.ID]++
			if p.MaxCheat == nil || *sv.cheat > *p.MaxCheat {
				v := *sv.cheat
				p.MaxCheat = &v
			}
		}
	}
	for id, k := range n {
		avg := sum[id] / float64(k)
		d.byID[id].AvgCheat = &avg
	}
	flags, err := s.FlagsOf(g.ID)
	if err != nil {
		return nil, err
	}
	for _, p := range d.parts {
		p.Flags = flags[p.ID]
	}
	out := append([]*Participant(nil), d.parts...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Rank == 0) != (b.Rank == 0) {
			return a.Rank != 0
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		return a.ID < b.ID
	})
	return out, nil
}
