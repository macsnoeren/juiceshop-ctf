package store

import (
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// Row is one participant on a board.
type Row struct {
	Rank          int        `json:"rank"`
	ParticipantID int64      `json:"participant"`
	Name          string     `json:"name"`
	Instance      string     `json:"-"`
	Score         int        `json:"score"`
	Solved        int        `json:"solved"`
	Total         int        `json:"total"`
	LastSolve     *time.Time `json:"last_solve"`
	ReachedAt     time.Time  `json:"-"`
	Online        bool       `json:"online"`
	// Admin-only fields; not in the public JSON.
	RegisteredAt  time.Time  `json:"-"`
	EndedAt       *time.Time `json:"-"`
	Hidden        bool       `json:"-"`
	AvgCheat      *float64   `json:"-"`
	MaxCheat      *float64   `json:"-"`
	RestoreFailed bool       `json:"-"`
}

// rows computes score, solved count and rank (§8.3). With all=false only the
// active, visible participants are included and ranked; with all=true every
// participant is returned (for /admin/ and the CSV) and Rank is 0 for the
// ones that are not on the public board.
//
// Score comes from the scoring table; disabled challenges count neither in the
// score nor in Total. Ties are broken by WHEN the score was reached (the last
// counted solve, or the registration for a score of 0): whoever got there
// first is ahead. Equal score at the same moment is the same rank.
func (s *Store) rows(all bool, now time.Time) ([]Row, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM challenges WHERE disabled = 0`).Scan(&total); err != nil {
		return nil, err
	}
	q := `SELECT p.id, p.instance_id, p.name, p.registered_at, p.ended_at, p.hidden, i.last_report_at, i.restore_failed
		FROM participants p JOIN instances i ON i.id = p.instance_id`
	if !all {
		q += ` WHERE p.ended_at IS NULL AND p.hidden = 0`
	}
	prow, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*Row{}
	var out []*Row
	for prow.Next() {
		r := &Row{Total: total}
		var reg string
		var ended, last sql.NullString
		if err := prow.Scan(&r.ParticipantID, &r.Instance, &r.Name, &reg, &ended, &r.Hidden, &last, &r.RestoreFailed); err != nil {
			prow.Close()
			return nil, err
		}
		r.RegisteredAt = parseTS(reg)
		r.ReachedAt = r.RegisteredAt
		r.EndedAt = parseNullTS(ended)
		if lr := parseNullTS(last); lr != nil {
			r.Online = now.Sub(*lr) < 3*time.Minute && r.EndedAt == nil
		}
		byID[r.ParticipantID] = r
		out = append(out, r)
	}
	prow.Close()

	srow, err := s.db.Query(`SELECT s.participant_id, s.solved_at, s.cheat, COALESCE(sc.points, 0)
		FROM solves s
		JOIN challenges c ON c.key = s.challenge_key AND c.disabled = 0
		LEFT JOIN scoring sc ON sc.difficulty = c.difficulty`)
	if err != nil {
		return nil, err
	}
	cheatSum := map[int64]float64{}
	cheatN := map[int64]int{}
	for srow.Next() {
		var pid int64
		var at string
		var cheat sql.NullFloat64
		var pts int
		if err := srow.Scan(&pid, &at, &cheat, &pts); err != nil {
			srow.Close()
			return nil, err
		}
		r := byID[pid]
		if r == nil {
			continue
		}
		t := parseTS(at)
		r.Score += pts
		r.Solved++
		if r.LastSolve == nil || t.After(*r.LastSolve) {
			tt := t
			r.LastSolve = &tt
			r.ReachedAt = t
		}
		if cheat.Valid {
			cheatSum[pid] += cheat.Float64
			cheatN[pid]++
			if r.MaxCheat == nil || cheat.Float64 > *r.MaxCheat {
				v := cheat.Float64
				r.MaxCheat = &v
			}
		}
	}
	srow.Close()
	for pid, n := range cheatN {
		avg := cheatSum[pid] / float64(n)
		byID[pid].AvgCheat = &avg
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if !a.ReachedAt.Equal(b.ReachedAt) {
			return a.ReachedAt.Before(b.ReachedAt)
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ParticipantID < b.ParticipantID
	})
	rank := 0
	var prev *Row
	result := make([]Row, 0, len(out))
	for _, r := range out {
		onBoard := r.EndedAt == nil && !r.Hidden
		if onBoard {
			rank++
			if prev != nil && prev.Score == r.Score && prev.ReachedAt.Equal(r.ReachedAt) {
				r.Rank = prev.Rank
			} else {
				r.Rank = rank
			}
			prev = r
		}
		result = append(result, *r)
	}
	return result, nil
}

// Leaderboard returns the public board.
func (s *Store) Leaderboard(now time.Time) ([]Row, error) { return s.rows(false, now) }

// AllParticipants returns every participant, for /admin/ and the CSV.
func (s *Store) AllParticipants(now time.Time) ([]Row, error) { return s.rows(true, now) }

// Standing returns a participant's place on the public board, or nil when
// they are not on it (ended or hidden).
func (s *Store) Standing(pid int64, now time.Time) (*protocol.Standing, error) {
	rows, err := s.Leaderboard(now)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.ParticipantID == pid {
			return &protocol.Standing{Score: r.Score, Rank: r.Rank, Of: len(rows)}, nil
		}
	}
	return nil, nil
}

// RecentSolve is one line of the beamer's ticker.
type RecentSolve struct {
	Name       string    `json:"name"`
	Challenge  string    `json:"challenge"`
	Difficulty int       `json:"difficulty"`
	SolvedAt   time.Time `json:"solved_at"`
}

// Recent returns the latest solves of participants on the public board.
func (s *Store) Recent(limit int) ([]RecentSolve, error) {
	rows, err := s.db.Query(`SELECT p.name, c.name, c.difficulty, s.solved_at
		FROM solves s
		JOIN participants p ON p.id = s.participant_id AND p.ended_at IS NULL AND p.hidden = 0
		JOIN challenges c ON c.key = s.challenge_key AND c.disabled = 0
		ORDER BY s.solved_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecentSolve{}
	for rows.Next() {
		var r RecentSolve
		var at string
		if err := rows.Scan(&r.Name, &r.Challenge, &r.Difficulty, &at); err != nil {
			return nil, err
		}
		r.SolvedAt = parseTS(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CategoryProgress is a participant's progress in one category.
type CategoryProgress struct {
	Name   string
	Solved int
	Total  int
}

// SolveDetail is one solve on a participant's page.
type SolveDetail struct {
	Challenge  string
	Category   string
	Difficulty int
	Points     int
	SolvedAt   time.Time
}

// Detail is a participant's public page.
type Detail struct {
	Row        Row
	Categories []CategoryProgress
	Solves     []SolveDetail
}

// ParticipantDetail returns a participant on the public board, or
// ErrNotFound for one that is not on it.
func (s *Store) ParticipantDetail(pid int64, now time.Time) (*Detail, error) {
	rows, err := s.Leaderboard(now)
	if err != nil {
		return nil, err
	}
	d := &Detail{}
	found := false
	for _, r := range rows {
		if r.ParticipantID == pid {
			d.Row, found = r, true
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	crow, err := s.db.Query(`SELECT c.category, COUNT(*),
		SUM(CASE WHEN s.challenge_key IS NULL THEN 0 ELSE 1 END)
		FROM challenges c LEFT JOIN solves s ON s.challenge_key = c.key AND s.participant_id = ?
		WHERE c.disabled = 0 GROUP BY c.category ORDER BY c.category`, pid)
	if err != nil {
		return nil, err
	}
	for crow.Next() {
		var c CategoryProgress
		if err := crow.Scan(&c.Name, &c.Total, &c.Solved); err != nil {
			crow.Close()
			return nil, err
		}
		d.Categories = append(d.Categories, c)
	}
	crow.Close()
	srow, err := s.db.Query(`SELECT c.name, c.category, c.difficulty, COALESCE(sc.points, 0), s.solved_at
		FROM solves s JOIN challenges c ON c.key = s.challenge_key AND c.disabled = 0
		LEFT JOIN scoring sc ON sc.difficulty = c.difficulty
		WHERE s.participant_id = ? ORDER BY s.solved_at DESC`, pid)
	if err != nil {
		return nil, err
	}
	defer srow.Close()
	for srow.Next() {
		var sd SolveDetail
		var at string
		if err := srow.Scan(&sd.Challenge, &sd.Category, &sd.Difficulty, &sd.Points, &at); err != nil {
			return nil, err
		}
		sd.SolvedAt = parseTS(at)
		d.Solves = append(d.Solves, sd)
	}
	return d, srow.Err()
}

// ParticipantInstance returns the instance id of a participant.
func (s *Store) ParticipantInstance(pid int64) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT instance_id FROM participants WHERE id = ?`, pid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// TotalChallenges is the number of challenges that can be solved.
func (s *Store) TotalChallenges() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM challenges WHERE disabled = 0`).Scan(&n)
	return n, err
}
