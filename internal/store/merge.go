package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// MaxFuture is how far past the report's sent_at a solve time may lie before
// it is clamped to the time the report was received (§8.2).
const MaxFuture = 5 * time.Minute

// MergeReport folds one report into the database, in one transaction (§8.2).
// It returns the participant the report is about, or 0 when the instance has
// no registration.
//
// The rules, each of which a test in merge_test.go holds in place:
//   - a solve belongs to a PARTICIPANT (instance + registration id), so an
//     instance given to a second student does not hand them the first one's
//     points;
//   - the first recorded solve time wins, and a report never removes a solve:
//     a Juice Shop that lost its progress does not cost the student their
//     score;
//   - a new registration id on an instance ends the previous participant.
func (s *Store) MergeReport(instance string, r *protocol.Report, received time.Time) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE instances SET last_report_at = ?, agent_version = ?, juiceshop_version = ?,
		reachable = ?, restore_failed = ? WHERE id = ?`,
		ts(received), r.AgentVersion, r.JuiceShop.Version, r.JuiceShop.Reachable, r.JuiceShop.RestoreFailed, instance)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}

	for _, id := range r.CommandsApplied {
		if _, err := tx.Exec(`UPDATE commands SET confirmed_at = ? WHERE id = ? AND instance_id = ? AND confirmed_at IS NULL`,
			ts(received), id, instance); err != nil {
			return 0, err
		}
	}

	for _, c := range r.Challenges {
		if _, err := tx.Exec(`INSERT INTO challenges (key, name, category, difficulty, disabled) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET name = excluded.name, category = excluded.category,
			difficulty = excluded.difficulty, disabled = excluded.disabled`,
			c.Key, c.Name, c.Category, c.Difficulty, c.Disabled); err != nil {
			return 0, err
		}
	}

	reg := r.Registration
	if reg == nil {
		return 0, tx.Commit()
	}

	var pid int64
	err = tx.QueryRow(`SELECT id FROM participants WHERE instance_id = ? AND registration_id = ?`, instance, reg.ID).Scan(&pid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`UPDATE participants SET ended_at = ? WHERE instance_id = ? AND ended_at IS NULL`,
			ts(received), instance); err != nil {
			return 0, err
		}
		res, err := tx.Exec(`INSERT INTO participants (instance_id, registration_id, name, registered_at) VALUES (?, ?, ?, ?)`,
			instance, reg.ID, reg.Name, ts(reg.RegisteredAt))
		if err != nil {
			return 0, err
		}
		pid, _ = res.LastInsertId()
	case err != nil:
		return 0, err
	}

	limit := r.SentAt.Add(MaxFuture)
	for _, c := range r.Challenges {
		if c.SolvedAt == nil {
			continue
		}
		at := *c.SolvedAt
		if at.After(limit) {
			at = received
		}
		var cheat any
		if c.Cheat != nil {
			cheat = *c.Cheat
		}
		if _, err := tx.Exec(`INSERT INTO solves (participant_id, challenge_key, solved_at, cheat) VALUES (?, ?, ?, ?)
			ON CONFLICT (participant_id, challenge_key) DO UPDATE SET cheat = COALESCE(solves.cheat, excluded.cheat)`,
			pid, c.Key, ts(at), cheat); err != nil {
			return 0, err
		}
	}
	return pid, tx.Commit()
}

// PendingCommands returns the commands an instance has not confirmed yet,
// and marks them delivered. They are sent again on every report until the
// agent confirms them; the agent applies each id once.
func (s *Store) PendingCommands(instance string, now time.Time) ([]protocol.Command, error) {
	rows, err := s.db.Query(`SELECT id, type FROM commands WHERE instance_id = ? AND confirmed_at IS NULL ORDER BY created_at, id`, instance)
	if err != nil {
		return nil, err
	}
	var out []protocol.Command
	for rows.Next() {
		var c protocol.Command
		if err := rows.Scan(&c.ID, &c.Type); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if len(out) > 0 {
		if _, err := s.db.Exec(`UPDATE commands SET delivered_at = ? WHERE instance_id = ? AND confirmed_at IS NULL AND delivered_at IS NULL`,
			ts(now), instance); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ResetParticipant ends a participant and queues reset_registration for
// their instance (§8.4). A reset already waiting for that instance is not
// queued a second time.
func (s *Store) ResetParticipant(id int64, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var instance string
	if err := tx.QueryRow(`SELECT instance_id FROM participants WHERE id = ?`, id).Scan(&instance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(`UPDATE participants SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, ts(now), id); err != nil {
		return err
	}
	var pending int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM commands WHERE instance_id = ? AND type = ? AND confirmed_at IS NULL`,
		instance, protocol.CmdResetRegistration).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		if _, err := tx.Exec(`INSERT INTO commands (id, instance_id, type, created_at) VALUES (?, ?, ?, ?)`,
			"c-"+httpx.RandomHex(6), instance, protocol.CmdResetRegistration, ts(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetHidden hides or shows a participant on the public pages.
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
