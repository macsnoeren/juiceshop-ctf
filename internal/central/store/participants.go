package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
)

// onlineWindow is how recent a report must be for "online": three missed
// reports at the node's 60-second interval.
const onlineWindow = 3 * time.Minute

// The reasons a participant left, as shown in admin.
const (
	LeftForget  = "forget"  // the student pressed Forget on their VM
	LeftRemoved = "removed" // the operator removed them
)

// Errors of Join. The messages are what the node shows the student.
var (
	ErrUnknownCode = errors.New("unknown join code")
	ErrGameDraft   = errors.New("this game is not open yet")
	ErrGameClosed  = errors.New("this game is closed")
)

// Participant is one student in one game, as admin sees them.
type Participant struct {
	ID               int64
	GameID           int64
	Name             string
	NodeUID          string
	JoinedAt         time.Time
	NodeJoinedAt     *time.Time
	LeftAt           *time.Time
	LeftReason       string
	Hidden           bool
	HasToken         bool
	LastReportAt     *time.Time
	NodeVersion      string
	JuiceShopVersion string
	Reachable        bool
	RestoreFailed    bool
	Boot             string
	LegacyInstance   string

	// Filled in by GameParticipants.
	Score    int
	Solved   int
	Rank     int
	AvgCheat *float64
	MaxCheat *float64
	Flags    []Flag
}

// Status is active, hidden, left (forget) or removed.
func (p *Participant) Status() string {
	switch {
	case p.LeftAt != nil && p.LeftReason == LeftRemoved:
		return "removed"
	case p.LeftAt != nil:
		return "left"
	case p.Hidden:
		return "hidden"
	}
	return "active"
}

// Online reports whether the participant's VM reported recently.
func (p *Participant) Online(now time.Time) bool {
	return p.LeftAt == nil && p.LastReportAt != nil && now.Sub(*p.LastReportAt) < onlineWindow
}

const participantCols = `id, game_id, name, node_uid, joined_at, node_joined_at, left_at, left_reason, hidden,
	token_hash IS NOT NULL, last_report_at, node_version, juiceshop_version, reachable, restore_failed, boot, legacy_instance`

func scanParticipant(row interface{ Scan(...any) error }) (*Participant, error) {
	p := &Participant{}
	var joined string
	var nodeJoined, left, last sql.NullString
	if err := row.Scan(&p.ID, &p.GameID, &p.Name, &p.NodeUID, &joined, &nodeJoined, &left, &p.LeftReason, &p.Hidden,
		&p.HasToken, &last, &p.NodeVersion, &p.JuiceShopVersion, &p.Reachable, &p.RestoreFailed, &p.Boot, &p.LegacyInstance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.JoinedAt = parseTS(joined)
	p.NodeJoinedAt, p.LeftAt, p.LastReportAt = parseNullTS(nodeJoined), parseNullTS(left), parseNullTS(last)
	return p, nil
}

// Participant returns one participant.
func (s *Store) Participant(id int64) (*Participant, error) {
	return scanParticipant(s.db.QueryRow(`SELECT `+participantCols+` FROM participants WHERE id = ?`, id))
}

// JoinResult is a successful join.
type JoinResult struct {
	Participant *Participant
	Token       string
	Game        *Game
}

// Join admits a student into the game their code belongs to (VERSION-1-1.md
// §2.2). The request must be normalised (protocol.JoinRequest.Normalize).
// The token is returned ONCE; only its hash is stored. On a refusal the
// returned game, when not nil, is the one the code belongs to, for the event.
func (s *Store) Join(req *protocol.JoinRequest, now time.Time) (*JoinResult, *Game, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	g, err := gameByCode(tx, req.Code)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, ErrUnknownCode
	}
	if err != nil {
		return nil, nil, err
	}
	switch {
	case g.State == protocol.StateDraft:
		return nil, g, ErrGameDraft
	case !g.Accepting(now):
		return nil, g, ErrGameClosed
	}
	// The node's own join time, never later than now: an offline join that
	// waited in the outbox is older than its arrival (§4.3).
	var nodeJoined *time.Time
	if req.JoinedAt != nil {
		t := *req.JoinedAt
		if t.After(now) {
			t = now
		}
		nodeJoined = &t
	}
	tok := protocol.NewParticipantToken()
	res, err := tx.Exec(`INSERT INTO participants (game_id, name, token_hash, node_uid, joined_at, node_joined_at, node_version, juiceshop_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, req.Name, protocol.Hash(tok), req.Node.UID, ts(now), nullTS(nodeJoined), req.Node.Version, req.Node.JuiceShop)
	if err != nil {
		return nil, g, err
	}
	id, _ := res.LastInsertId()
	p, err := scanParticipant(tx.QueryRow(`SELECT `+participantCols+` FROM participants WHERE id = ?`, id))
	if err != nil {
		return nil, g, err
	}
	if err := tx.Commit(); err != nil {
		return nil, g, err
	}
	g, err = s.withScoring(g)
	return &JoinResult{Participant: p, Token: tok, Game: g}, g, err
}

// SetHidden hides or shows a participant on the board (1.0's hide).
func (s *Store) SetHidden(id int64, hidden bool) error {
	res, err := s.db.Exec(`UPDATE participants SET hidden = ? WHERE id = ?`, hidden, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Remove takes a participant off the game and the board, stops their token,
// and tells their VM forget (VERSION-1-1.md §5.3). The row and the solves
// stay, for the operator; Delete removes them.
func (s *Store) Remove(id int64, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hash sql.NullString
	var game int64
	if err := tx.QueryRow(`SELECT token_hash, game_id FROM participants WHERE id = ?`, id).Scan(&hash, &game); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if hash.Valid {
		if err := queueForget(tx, hash.String, id, game, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE participants SET token_hash = NULL, left_at = COALESCE(left_at, ?), left_reason = ? WHERE id = ?`,
		ts(now), LeftRemoved, id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteParticipant deletes a participant who has already left or been
// removed, with their solves. An active one must be removed first, so their
// VM is told.
func (s *Store) DeleteParticipant(id int64) error {
	res, err := s.db.Exec(`DELETE FROM participants WHERE id = ? AND left_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: only a participant who left or was removed can be deleted", ErrConflict)
	}
	return nil
}

// queueForget queues a forget for the VM holding the token with this hash,
// unless one is already waiting. Commands are keyed by the token's hash, not
// by the participant's row, so a VM whose participant was deleted (clear
// game) can still be told.
func queueForget(tx *sql.Tx, hash string, pid, gid int64, now time.Time) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM commands WHERE token_hash = ? AND type = ? AND confirmed_at IS NULL`,
		hash, protocol.CmdForget).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	id := "c-" + protocol.NewNodeUID()[:12]
	if _, err := tx.Exec(`INSERT INTO commands (id, token_hash, participant_id, game_id, type, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, hash, pid, gid, protocol.CmdForget, ts(now)); err != nil {
		return err
	}
	return addEvent(tx, Event{At: now, Kind: EvCommand, GameID: gid, ParticipantID: pid, Text: "forget queued (" + id + ")"})
}

// LeaveOutcome is what a leave turned out to be.
type LeaveOutcome int

const (
	LeaveUnknown   LeaveOutcome = iota // no such token: 401
	LeaveLeft                          // an active participant left
	LeaveConfirmed                     // a removed or cleared participant's VM confirmed the forget
)

// Leave is POST /api/v2/leave (VERSION-1-1.md §4.4): the participant is
// marked left and taken off the board, the token stops working, and any
// forget waiting for that token is confirmed. The solves stay.
func (s *Store) Leave(tokenHash, reason string, now time.Time, addr string) (LeaveOutcome, *Participant, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return LeaveUnknown, nil, err
	}
	defer tx.Rollback()
	confirmed, err := confirmCommands(tx, tokenHash, nil, now, true)
	if err != nil {
		return LeaveUnknown, nil, err
	}
	p, err := scanParticipant(tx.QueryRow(`SELECT `+participantCols+` FROM participants WHERE token_hash = ?`, tokenHash))
	switch {
	case errors.Is(err, ErrNotFound):
		if confirmed == 0 {
			return LeaveUnknown, nil, nil
		}
		return LeaveConfirmed, nil, tx.Commit()
	case err != nil:
		return LeaveUnknown, nil, err
	}
	if _, err := tx.Exec(`UPDATE participants SET token_hash = NULL, left_at = ?, left_reason = ? WHERE id = ?`, ts(now), LeftForget, p.ID); err != nil {
		return LeaveUnknown, nil, err
	}
	how := "the student pressed Forget"
	if reason == protocol.LeaveCommand {
		how = "the VM carried out a forget"
	}
	if err := addEvent(tx, Event{At: now, Kind: EvLeave, GameID: p.GameID, ParticipantID: p.ID, Address: addr,
		Text: "left (forget): " + how + "; off the board, the solves are kept"}); err != nil {
		return LeaveUnknown, nil, err
	}
	return LeaveLeft, p, tx.Commit()
}

// confirmCommands marks commands of a token confirmed: those in ids, or all
// that are pending when all is true. It writes an event per confirmation and
// returns how many there were.
func confirmCommands(tx *sql.Tx, hash string, ids []string, now time.Time, all bool) (int, error) {
	q := `SELECT id, participant_id, game_id FROM commands WHERE token_hash = ? AND confirmed_at IS NULL`
	rows, err := tx.Query(q, hash)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	type c struct {
		id       string
		pid, gid sql.NullInt64
	}
	var todo []c
	for rows.Next() {
		var x c
		if err := rows.Scan(&x.id, &x.pid, &x.gid); err != nil {
			rows.Close()
			return 0, err
		}
		if all || want[x.id] {
			todo = append(todo, x)
		}
	}
	rows.Close()
	for _, x := range todo {
		if _, err := tx.Exec(`UPDATE commands SET confirmed_at = ?, delivered_at = COALESCE(delivered_at, ?) WHERE id = ?`, ts(now), ts(now), x.id); err != nil {
			return 0, err
		}
		if err := addEvent(tx, Event{At: now, Kind: EvCommand, GameID: x.gid.Int64, ParticipantID: x.pid.Int64, Text: "forget confirmed by the VM (" + x.id + ")"}); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

// pendingCommands returns a token's unconfirmed commands and marks them
// delivered; the first delivery is an event. They are sent again on every
// report until the VM confirms them, and the VM applies each id once.
func pendingCommands(tx *sql.Tx, hash string, now time.Time) ([]protocol.Command, error) {
	rows, err := tx.Query(`SELECT id, type, delivered_at IS NULL, participant_id, game_id FROM commands
		WHERE token_hash = ? AND confirmed_at IS NULL ORDER BY created_at, id`, hash)
	if err != nil {
		return nil, err
	}
	out := []protocol.Command{}
	type first struct {
		id       string
		pid, gid sql.NullInt64
	}
	var firsts []first
	for rows.Next() {
		var c protocol.Command
		var isFirst bool
		var pid, gid sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Type, &isFirst, &pid, &gid); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
		if isFirst {
			firsts = append(firsts, first{c.ID, pid, gid})
		}
	}
	rows.Close()
	for _, f := range firsts {
		if _, err := tx.Exec(`UPDATE commands SET delivered_at = ? WHERE id = ?`, ts(now), f.id); err != nil {
			return nil, err
		}
		if err := addEvent(tx, Event{At: now, Kind: EvCommand, GameID: f.gid.Int64, ParticipantID: f.pid.Int64, Text: "forget delivered (" + f.id + ")"}); err != nil {
			return nil, err
		}
	}
	return out, nil
}
