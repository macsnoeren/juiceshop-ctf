package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
	"golang.org/x/text/unicode/norm"
)

// Game is one CTF (VERSION-1-1.md §2): a group of students, a time window, a
// scoring table and a board.
type Game struct {
	ID            int64
	Name          string
	Slug          string
	HasCode       bool
	State         string
	StartsAt      *time.Time
	EndsAt        *time.Time
	ClosedAt      *time.Time
	BoardHidden   bool
	BoardInitials bool
	CreatedAt     time.Time
	Scoring       map[int]int
}

// End is when the game stops counting solves: the end of its window or the
// moment it was closed, whichever came first; nil while neither is set.
func (g *Game) End() *time.Time {
	e := g.EndsAt
	if g.ClosedAt != nil && (e == nil || g.ClosedAt.Before(*e)) {
		e = g.ClosedAt
	}
	return e
}

// Ended reports whether the game is over at now: closed, or past its end.
func (g *Game) Ended(now time.Time) bool {
	if g.State == protocol.StateClosed {
		return true
	}
	return g.EndsAt != nil && !now.Before(*g.EndsAt)
}

// Accepting reports whether a solve received at now counts, and whether a
// student may join: the game is open or running and not past its end.
func (g *Game) Accepting(now time.Time) bool {
	return (g.State == protocol.StateOpen || g.State == protocol.StateRunning) && !g.Ended(now)
}

// EffectiveState is the state as the node and the board show it: a running
// game past its end time is closed, even before the operator closes it.
func (g *Game) EffectiveState(now time.Time) string {
	if g.State != protocol.StateDraft && g.Ended(now) {
		return protocol.StateClosed
	}
	return g.State
}

// Public reports whether the game's board may be shown.
func (g *Game) Public() bool { return g.State != protocol.StateDraft && !g.BoardHidden }

// Protocol is the game as a node sees it.
func (g *Game) Protocol(now time.Time) protocol.Game {
	return protocol.Game{ID: g.ID, Name: g.Name, Slug: g.Slug, State: g.EffectiveState(now), StartsAt: g.StartsAt, EndsAt: g.EndsAt}
}

// transitions are the allowed state changes (VERSION-1-1.md §2). A closed
// game may be reopened: closing by mistake at the end of a lesson must not
// be final. Back to draft is not allowed once students may have joined.
var transitions = map[string][]string{
	protocol.StateDraft:   {protocol.StateOpen, protocol.StateRunning, protocol.StateClosed},
	protocol.StateOpen:    {protocol.StateRunning, protocol.StateClosed},
	protocol.StateRunning: {protocol.StateClosed},
	protocol.StateClosed:  {protocol.StateOpen, protocol.StateRunning},
}

// CanTransition reports whether a game in state from may move to state to.
func CanTransition(from, to string) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify makes the board path of a name: "Class 2A — 19 Nov" → "class-2a-19-nov".
func Slugify(name string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(name)) {
		if unicode.Is(unicode.Mn, r) {
			continue // é → e
		}
		b.WriteRune(r)
	}
	s := strings.Trim(slugRE.ReplaceAllString(b.String(), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "game"
	}
	return s
}

// GameInput is what the operator fills in.
type GameInput struct {
	Name          string
	StartsAt      *time.Time
	EndsAt        *time.Time
	BoardHidden   bool
	BoardInitials bool
	Scoring       map[int]int
}

func (in *GameInput) validate() error {
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	if in.Name == "" || len([]rune(in.Name)) > 80 {
		return fmt.Errorf("the name must be 1 to 80 characters")
	}
	if in.StartsAt != nil && in.EndsAt != nil && !in.EndsAt.After(*in.StartsAt) {
		return fmt.Errorf("the end must be after the start")
	}
	if in.Scoring == nil {
		in.Scoring = DefaultScoring
	}
	for d := 1; d <= 6; d++ {
		p, ok := in.Scoring[d]
		if !ok {
			return fmt.Errorf("points for %d stars missing", d)
		}
		if p < 0 || p > 100000 {
			return fmt.Errorf("points must be 0 to 100000")
		}
	}
	return nil
}

// CreateGame makes a game in state draft and returns it with its join code.
// The code is returned ONCE; only its hash is stored. Two games with the same
// name get different slugs (-2, -3, …).
func (s *Store) CreateGame(in GameInput, now time.Time) (*Game, string, error) {
	if err := in.validate(); err != nil {
		return nil, "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	base := Slugify(in.Name)
	slug := base
	for i := 2; ; i++ {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM games WHERE slug = ?`, slug).Scan(&n); err != nil {
			return nil, "", err
		}
		if n == 0 {
			break
		}
		slug = base + "-" + strconv.Itoa(i)
	}
	code := protocol.NewJoinCode()
	res, err := tx.Exec(`INSERT INTO games (name, slug, code_hash, state, starts_at, ends_at, board_hidden, board_initials, created_at)
		VALUES (?, ?, ?, 'draft', ?, ?, ?, ?, ?)`,
		in.Name, slug, protocol.Hash(code), nullTS(in.StartsAt), nullTS(in.EndsAt), in.BoardHidden, in.BoardInitials, ts(now))
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	if err := writeScoring(tx, id, in.Scoring); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	g, err := s.Game(id)
	return g, code, err
}

func writeScoring(tx *sql.Tx, id int64, sc map[int]int) error {
	for d := 1; d <= 6; d++ {
		if _, err := tx.Exec(`INSERT INTO game_scoring (game_id, difficulty, points) VALUES (?, ?, ?)
			ON CONFLICT (game_id, difficulty) DO UPDATE SET points = excluded.points`, id, d, sc[d]); err != nil {
			return err
		}
	}
	return nil
}

// UpdateGame changes what the operator may edit. The slug stays: it is the
// address of the board, and a link on a beamer must keep working.
func (s *Store) UpdateGame(id int64, in GameInput) error {
	if err := in.validate(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE games SET name = ?, starts_at = ?, ends_at = ?, board_hidden = ?, board_initials = ? WHERE id = ?`,
		in.Name, nullTS(in.StartsAt), nullTS(in.EndsAt), in.BoardHidden, in.BoardInitials, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := writeScoring(tx, id, in.Scoring); err != nil {
		return err
	}
	return tx.Commit()
}

// SetState moves a game to another state, if that transition is allowed.
// Closing records when (the end of counting); reopening clears it.
func (s *Store) SetState(id int64, to string, now time.Time) error {
	g, err := s.Game(id)
	if err != nil {
		return err
	}
	if !CanTransition(g.State, to) {
		return fmt.Errorf("%w: a %s game cannot become %s", ErrConflict, g.State, to)
	}
	var closed any
	if to == protocol.StateClosed {
		closed = ts(now)
	}
	_, err = s.db.Exec(`UPDATE games SET state = ?, closed_at = ? WHERE id = ?`, to, closed, id)
	return err
}

// RegenerateCode gives a game a new join code, returned ONCE. The old code
// stops admitting anyone at once; students who joined keep their tokens.
func (s *Store) RegenerateCode(id int64) (string, error) {
	code := protocol.NewJoinCode()
	res, err := s.db.Exec(`UPDATE games SET code_hash = ? WHERE id = ?`, protocol.Hash(code), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return code, nil
}

const gameCols = `id, name, slug, code_hash IS NOT NULL, state, starts_at, ends_at, closed_at, board_hidden, board_initials, created_at`

func scanGame(row interface{ Scan(...any) error }) (*Game, error) {
	g := &Game{}
	var starts, ends, closed sql.NullString
	var created string
	if err := row.Scan(&g.ID, &g.Name, &g.Slug, &g.HasCode, &g.State, &starts, &ends, &closed, &g.BoardHidden, &g.BoardInitials, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	g.StartsAt, g.EndsAt, g.ClosedAt = parseNullTS(starts), parseNullTS(ends), parseNullTS(closed)
	g.CreatedAt = parseTS(created)
	return g, nil
}

func (s *Store) withScoring(g *Game) (*Game, error) {
	rows, err := s.db.Query(`SELECT difficulty, points FROM game_scoring WHERE game_id = ?`, g.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	g.Scoring = map[int]int{}
	for rows.Next() {
		var d, p int
		if err := rows.Scan(&d, &p); err != nil {
			return nil, err
		}
		g.Scoring[d] = p
	}
	for d, p := range DefaultScoring {
		if _, ok := g.Scoring[d]; !ok {
			g.Scoring[d] = p
		}
	}
	return g, rows.Err()
}

// Game returns one game by id.
func (s *Store) Game(id int64) (*Game, error) {
	g, err := scanGame(s.db.QueryRow(`SELECT `+gameCols+` FROM games WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return s.withScoring(g)
}

// GameBySlug returns one game by its board path.
func (s *Store) GameBySlug(slug string) (*Game, error) {
	g, err := scanGame(s.db.QueryRow(`SELECT `+gameCols+` FROM games WHERE slug = ?`, slug))
	if err != nil {
		return nil, err
	}
	return s.withScoring(g)
}

// gameByCode finds the game a join code belongs to, by its hash.
func gameByCode(tx *sql.Tx, code string) (*Game, error) {
	return scanGame(tx.QueryRow(`SELECT `+gameCols+` FROM games WHERE code_hash = ?`, protocol.Hash(code)))
}

// GameSummary is a game in the list in /admin/ and on the public index.
type GameSummary struct {
	Game
	Participants int // on the board or hidden; not those who left
	Online       int
}

// Games lists every game, newest first.
func (s *Store) Games(now time.Time) ([]GameSummary, error) {
	rows, err := s.db.Query(`SELECT `+gameCols+`,
		(SELECT COUNT(*) FROM participants p WHERE p.game_id = games.id AND p.left_at IS NULL),
		(SELECT COUNT(*) FROM participants p WHERE p.game_id = games.id AND p.left_at IS NULL AND p.last_report_at > ?)
		FROM games ORDER BY created_at DESC, id DESC`, ts(now.Add(-onlineWindow)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GameSummary
	for rows.Next() {
		g := &Game{}
		var starts, ends, closed sql.NullString
		var created string
		var gs GameSummary
		if err := rows.Scan(&g.ID, &g.Name, &g.Slug, &g.HasCode, &g.State, &starts, &ends, &closed, &g.BoardHidden, &g.BoardInitials, &created,
			&gs.Participants, &gs.Online); err != nil {
			return nil, err
		}
		g.StartsAt, g.EndsAt, g.ClosedAt = parseNullTS(starts), parseNullTS(ends), parseNullTS(closed)
		g.CreatedAt = parseTS(created)
		gs.Game = *g
		out = append(out, gs)
	}
	return out, rows.Err()
}

// forgetAll queues a forget for every participant of a game that still has a
// token, so each VM in it is told, even after its row is deleted
// (VERSION-1-1.md §5.3). It returns how many were queued.
func forgetAll(tx *sql.Tx, gameID int64, now time.Time) (int, error) {
	rows, err := tx.Query(`SELECT id, token_hash FROM participants WHERE game_id = ? AND token_hash IS NOT NULL`, gameID)
	if err != nil {
		return 0, err
	}
	type p struct {
		id   int64
		hash string
	}
	var ps []p
	for rows.Next() {
		var x p
		if err := rows.Scan(&x.id, &x.hash); err != nil {
			rows.Close()
			return 0, err
		}
		ps = append(ps, x)
	}
	rows.Close()
	for _, x := range ps {
		if err := queueForget(tx, x.hash, x.id, gameID, now); err != nil {
			return 0, err
		}
	}
	return len(ps), nil
}

// ClearGame deletes every participant and solve of a game and keeps the game
// and its code; every VM in it is told forget. It returns the number of
// participants deleted.
func (s *Store) ClearGame(id int64, now time.Time) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := s.gameTx(tx, id); err != nil {
		return 0, err
	}
	if _, err := forgetAll(tx, id, now); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM participants WHERE game_id = ?`, id)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}

// DeleteGame is ClearGame, and then the game itself.
func (s *Store) DeleteGame(id int64, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.gameTx(tx, id); err != nil {
		return err
	}
	if _, err := forgetAll(tx, id, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM participants WHERE game_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM games WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) gameTx(tx *sql.Tx, id int64) (*Game, error) {
	return scanGame(tx.QueryRow(`SELECT `+gameCols+` FROM games WHERE id = ?`, id))
}
