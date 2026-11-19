package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
)

// MaxFuture is how far past the moment a report was sent (or received,
// whichever is earlier) a solve time may lie before it is clamped to the time
// of receipt (DESIGN.md §8.2).
const MaxFuture = 5 * time.Minute

// reportEventEvery collapses the "report" events: one per participant per
// minute, unless something changed (VERSION-1-1.md §5.4).
const reportEventEvery = time.Minute

// ReportOutcome says how a report was taken.
type ReportOutcome int

const (
	ReportUnknown   ReportOutcome = iota // no such token: 401
	ReportMerged                         // an active participant's report
	ReportForgotten                      // a removed or cleared participant: only the forget is sent back
)

// ReportResult is what Report returns besides the answer.
type ReportResult struct {
	Outcome     ReportOutcome
	Participant *Participant
	NewSolves   int
}

// Report folds one report into the database, in one transaction, and returns
// the answer for the node (VERSION-1-1.md §2.2, §5.1).
//
// The rules, each held in place by a test in report_test.go:
//   - a report is about ONE participant, found by the token's hash; NOTHING
//     in it ends, moves or touches another participant (the lab's F1);
//   - the first recorded solve time wins and a report never removes a solve:
//     a Juice Shop that lost its progress does not cost the student their
//     score;
//   - each new solve is stored with the time of RECEIPT, which decides first
//     blood and ties: a VM's clock is the student's, the server's is not;
//   - a solve received when the game no longer accepts solves is stored, so
//     the operator sees it, but not counted;
//   - keys the catalogue does not know are ignored: a key cannot be worth
//     anything the central server did not decide.
func (s *Store) Report(tokenHash string, r *protocol.Report, now time.Time, addr string) (protocol.ReportResponse, ReportResult, error) {
	var resp protocol.ReportResponse
	tx, err := s.db.Begin()
	if err != nil {
		return resp, ReportResult{}, err
	}
	defer tx.Rollback()

	p, err := scanParticipant(tx.QueryRow(`SELECT `+participantCols+` FROM participants WHERE token_hash = ?`, tokenHash))
	if errors.Is(err, ErrNotFound) {
		// Removed, or the game was cleared: the token is no participant's
		// any more, but a forget may be waiting for the VM that holds it.
		if _, err := confirmCommands(tx, tokenHash, r.CommandsApplied, now, false); err != nil {
			return resp, ReportResult{}, err
		}
		cmds, err := pendingCommands(tx, tokenHash, now)
		if err != nil {
			return resp, ReportResult{}, err
		}
		if len(cmds) == 0 {
			return resp, ReportResult{Outcome: ReportUnknown}, tx.Commit()
		}
		resp = protocol.ReportResponse{Game: protocol.Game{State: protocol.StateLeft}, Commands: cmds}
		return resp, ReportResult{Outcome: ReportForgotten}, tx.Commit()
	}
	if err != nil {
		return resp, ReportResult{}, err
	}
	g, err := scanGame(tx.QueryRow(`SELECT `+gameCols+` FROM games WHERE id = ?`, p.GameID))
	if err != nil {
		return resp, ReportResult{}, err
	}

	changed := p.Reachable != r.Reachable || p.RestoreFailed != r.RestoreFailed ||
		p.NodeVersion != r.Node.Version || p.JuiceShopVersion != r.Node.JuiceShop
	var flags []Flag

	// Boots: a restart gives a new boot that never comes back, so a boot
	// seen BEFORE the current one returning is two Juice Shops reporting
	// under one token — a VM exported after it joined, or a shared token.
	if r.Boot != "" && r.Boot != p.Boot {
		changed = true
		var seen int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM participant_boots WHERE participant_id = ? AND boot = ?`, p.ID, r.Boot).Scan(&seen); err != nil {
			return resp, ReportResult{}, err
		}
		if seen > 0 && p.Boot != "" {
			flags = append(flags, Flag{Kind: FlagTwoBoots, Text: "one token reports two Juice Shop boots in alternation: a copied VM or a shared token"})
		}
	}
	if r.Boot != "" {
		if _, err := tx.Exec(`INSERT INTO participant_boots (participant_id, boot, first_seen, last_seen) VALUES (?, ?, ?, ?)
			ON CONFLICT (participant_id, boot) DO UPDATE SET last_seen = excluded.last_seen`, p.ID, r.Boot, ts(now), ts(now)); err != nil {
			return resp, ReportResult{}, err
		}
	}
	boot := p.Boot
	if r.Boot != "" {
		boot = r.Boot
	}
	if _, err := tx.Exec(`UPDATE participants SET last_report_at = ?, node_version = ?, juiceshop_version = ?,
		reachable = ?, restore_failed = ?, boot = ? WHERE id = ?`,
		ts(now), r.Node.Version, r.Node.JuiceShop, r.Reachable, r.RestoreFailed, boot, p.ID); err != nil {
		return resp, ReportResult{}, err
	}
	if _, err := confirmCommands(tx, tokenHash, r.CommandsApplied, now, false); err != nil {
		return resp, ReportResult{}, err
	}

	// The solves.
	known := map[string]bool{}
	rows, err := tx.Query(`SELECT challenge_key FROM solves WHERE participant_id = ?`, p.ID)
	if err != nil {
		return resp, ReportResult{}, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return resp, ReportResult{}, err
		}
		known[k] = true
	}
	rows.Close()

	limit := r.SentAt
	if now.Before(limit) {
		limit = now
	}
	limit = limit.Add(MaxFuture)
	counted := g.Accepting(now)
	var fresh []string
	unknown := 0
	for _, sv := range r.Solves {
		c, ok := juiceshop.Lookup(sv.Key)
		if !ok {
			unknown++
			continue
		}
		var cheat any
		if sv.Cheat != nil {
			cheat = *sv.Cheat
		}
		if known[sv.Key] {
			if cheat != nil {
				if _, err := tx.Exec(`UPDATE solves SET cheat = COALESCE(cheat, ?) WHERE participant_id = ? AND challenge_key = ?`, cheat, p.ID, sv.Key); err != nil {
					return resp, ReportResult{}, err
				}
			}
			continue
		}
		at := sv.SolvedAt
		if at.After(limit) {
			at = now
		}
		if _, err := tx.Exec(`INSERT INTO solves (participant_id, challenge_key, solved_at, received_at, cheat, counted) VALUES (?, ?, ?, ?, ?, ?)`,
			p.ID, sv.Key, ts(at), ts(now), cheat, counted); err != nil {
			return resp, ReportResult{}, err
		}
		fresh = append(fresh, sv.Key)
		text := fmt.Sprintf("solved %s (%d★, %s)", c.Name, c.Difficulty, c.Category)
		if sv.Cheat != nil {
			text += fmt.Sprintf(", cheat score %.2f", *sv.Cheat)
		}
		if !counted {
			text += "; NOT counted: the game no longer accepts solves"
		}
		if err := addEvent(tx, Event{At: now, Kind: EvSolve, GameID: g.ID, ParticipantID: p.ID, Text: text}); err != nil {
			return resp, ReportResult{}, err
		}
	}
	if len(fresh) > 0 {
		changed = true
		more, err := solveFlags(tx, p, g, fresh)
		if err != nil {
			return resp, ReportResult{}, err
		}
		flags = append(flags, more...)
	}
	if err := raiseFlags(tx, p, flags, now); err != nil {
		return resp, ReportResult{}, err
	}

	var lastEv sql.NullString
	if err := tx.QueryRow(`SELECT last_report_event_at FROM participants WHERE id = ?`, p.ID).Scan(&lastEv); err != nil {
		return resp, ReportResult{}, err
	}
	if le := parseNullTS(lastEv); changed || le == nil || now.Sub(*le) >= reportEventEvery {
		shop := "Juice Shop up"
		if !r.Reachable {
			shop = "Juice Shop NOT answering"
		}
		if r.RestoreFailed {
			shop += ", restore failed"
		}
		text := fmt.Sprintf("report: %d solves, %d new; %s; node %s", len(r.Solves), len(fresh), shop, r.Node.Version)
		if unknown > 0 {
			text += fmt.Sprintf("; %d unknown challenge keys ignored", unknown)
		}
		if err := addEvent(tx, Event{At: now, Kind: EvReport, GameID: g.ID, ParticipantID: p.ID, Address: addr, Text: text}); err != nil {
			return resp, ReportResult{}, err
		}
		if _, err := tx.Exec(`UPDATE participants SET last_report_event_at = ? WHERE id = ?`, ts(now), p.ID); err != nil {
			return resp, ReportResult{}, err
		}
	}

	cmds, err := pendingCommands(tx, tokenHash, now)
	if err != nil {
		return resp, ReportResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return resp, ReportResult{}, err
	}
	g, err = s.withScoring(g)
	if err != nil {
		return resp, ReportResult{}, err
	}
	resp = protocol.ReportResponse{Game: g.Protocol(now), Commands: cmds}
	if resp.Standing, err = s.StandingOf(g, p.ID, now); err != nil {
		return resp, ReportResult{}, err
	}
	return resp, ReportResult{Outcome: ReportMerged, Participant: p, NewSolves: len(fresh)}, nil
}

// The flags (VERSION-1-1.md §5.5): things an honest node never sends. They
// are raised as events and shown next to the participant, NEVER acted on.
const (
	FlagPrerequisite = "prerequisite"
	FlagBurst        = "burst"
	FlagAfterEnd     = "after_end"
	FlagBeforeJoin   = "before_join"
	FlagTwoBoots     = "two_boots"
	FlagCheatScore   = "cheat_score"
)

// The thresholds. A burst is more than BurstSolves first-seen times within
// BurstWindow: one Juice Shop request can solve two or three challenges at
// once (the poison null byte does), and the node sees them in one poll, so
// the threshold sits above that. The cheat flag needs CheatMinSolves solves
// with a score, because Juice Shop scores any quick second solve high — its
// webhook gave 0.85 for a one-star challenge solved a second after another.
const (
	BurstSolves    = 5
	BurstWindow    = 10 * time.Second
	CheatMinSolves = 5
	CheatAverage   = 0.9
	// ClockSlack is how far a VM's clock may be off before a solve "before
	// the join" or "after the end" is a flag.
	ClockSlack = 2 * time.Minute
)

// Flag is one flag on a participant.
type Flag struct {
	Kind   string
	Detail string
	Text   string
	At     time.Time
}

func solveFlags(tx *sql.Tx, p *Participant, g *Game, fresh []string) ([]Flag, error) {
	type sv struct {
		key   string
		at    time.Time
		cheat sql.NullFloat64
	}
	rows, err := tx.Query(`SELECT challenge_key, solved_at, cheat FROM solves WHERE participant_id = ?`, p.ID)
	if err != nil {
		return nil, err
	}
	var all []sv
	have := map[string]bool{}
	for rows.Next() {
		var x sv
		var at string
		if err := rows.Scan(&x.key, &at, &x.cheat); err != nil {
			rows.Close()
			return nil, err
		}
		x.at = parseTS(at)
		all = append(all, x)
		have[x.key] = true
	}
	rows.Close()
	byKey := map[string]sv{}
	for _, x := range all {
		byKey[x.key] = x
	}

	var out []Flag
	joined := p.JoinedAt
	if p.NodeJoinedAt != nil {
		joined = *p.NodeJoinedAt
	}
	end := g.End()
	for _, k := range fresh {
		if pre, ok := juiceshop.Prerequisites[k]; ok && !have[pre] {
			out = append(out, Flag{Kind: FlagPrerequisite, Detail: k, Text: fmt.Sprintf("%s solved without %s, which Juice Shop always solves with it", k, pre)})
		}
		at := byKey[k].at
		if at.Before(joined.Add(-ClockSlack)) {
			out = append(out, Flag{Kind: FlagBeforeJoin, Detail: k, Text: fmt.Sprintf("%s solved at %s, before the join", k, at.Format(time.RFC3339))})
		}
		if end != nil && at.After(end.Add(ClockSlack)) {
			out = append(out, Flag{Kind: FlagAfterEnd, Detail: k, Text: fmt.Sprintf("%s solved at %s, after the game's end", k, at.Format(time.RFC3339))})
		}
	}

	times := make([]time.Time, 0, len(all))
	for _, x := range all {
		times = append(times, x.at)
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	for i := range times {
		j := i
		for j < len(times) && times[j].Sub(times[i]) <= BurstWindow {
			j++
		}
		if j-i > BurstSolves {
			out = append(out, Flag{Kind: FlagBurst, Text: fmt.Sprintf("%d solves within %s", j-i, BurstWindow)})
			break
		}
	}

	var sum float64
	n := 0
	for _, x := range all {
		if x.cheat.Valid {
			sum += x.cheat.Float64
			n++
		}
	}
	if n >= CheatMinSolves && sum/float64(n) > CheatAverage {
		out = append(out, Flag{Kind: FlagCheatScore, Text: fmt.Sprintf("Juice Shop's cheat score averages %.2f over %d solves", sum/float64(n), n)})
	}
	return out, nil
}

// raiseFlags stores new flags; a flag already raised (same kind and detail)
// is not raised again, so one copied VM is one flag, not one per minute.
func raiseFlags(tx *sql.Tx, p *Participant, flags []Flag, now time.Time) error {
	for _, f := range flags {
		res, err := tx.Exec(`INSERT INTO flags (participant_id, kind, detail, at) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			p.ID, f.Kind, f.Detail, ts(now))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		if err := addEvent(tx, Event{At: now, Kind: EvFlag, GameID: p.GameID, ParticipantID: p.ID, Text: f.Kind + ": " + f.Text}); err != nil {
			return err
		}
	}
	return nil
}

// FlagsOf returns the flags of the participants of a game, by participant.
func (s *Store) FlagsOf(gameID int64) (map[int64][]Flag, error) {
	rows, err := s.db.Query(`SELECT f.participant_id, f.kind, f.detail, f.at FROM flags f
		JOIN participants p ON p.id = f.participant_id WHERE p.game_id = ? ORDER BY f.at`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Flag{}
	for rows.Next() {
		var pid int64
		var f Flag
		var at string
		if err := rows.Scan(&pid, &f.Kind, &f.Detail, &at); err != nil {
			return nil, err
		}
		f.At = parseTS(at)
		f.Text = f.Kind
		if f.Detail != "" {
			f.Text += " " + strings.TrimSuffix(f.Detail, "Challenge")
		}
		out[pid] = append(out[pid], f)
	}
	return out, rows.Err()
}
