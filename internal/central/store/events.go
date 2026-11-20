// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package store

import (
	"database/sql"
	"strings"
	"time"
)

// The kinds of event in the feed.
const (
	EvJoin        = "join"
	EvJoinRefused = "join-refused"
	EvReport      = "report"
	EvSolve       = "solve"
	EvFlag        = "flag"
	EvRejected    = "rejected"
	EvCommand     = "command"
	EvLeave       = "leave"
	EvAdmin       = "admin"
	EvLegacy      = "legacy"
	// EvAuth is a login, a failed login, a logout, or a change to an
	// account: who got into admin, and who tried.
	EvAuth = "auth"
)

// EventKinds is every kind, in the order the filter shows them.
var EventKinds = []string{EvJoin, EvJoinRefused, EvSolve, EvFlag, EvReport, EvRejected, EvCommand, EvLeave, EvAdmin, EvAuth, EvLegacy}

// Event is one line of the feed. The feed is the operator's, so it may name
// participants; it never holds a token, a join code or a cookie.
type Event struct {
	ID            int64
	At            time.Time
	Kind          string
	GameID        int64
	ParticipantID int64
	Address       string
	Text          string
	// Actor is the admin user who did it, for kinds admin and auth; empty
	// for what VMs do.
	Actor string

	// Filled in by Events, for display.
	GameName        string
	ParticipantName string
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func addEvent(x execer, e Event) error {
	if len(e.Text) > 500 {
		e.Text = e.Text[:500]
	}
	_, err := x.Exec(`INSERT INTO events (at, kind, game_id, participant_id, address, text, actor) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts(e.At), e.Kind, nullID(e.GameID), nullID(e.ParticipantID), e.Address, e.Text, e.Actor)
	return err
}

// AddEvent writes one event.
func (s *Store) AddEvent(e Event) error { return addEvent(s.db, e) }

// EventFilter narrows the feed. Zero values do not filter.
type EventFilter struct {
	GameID        int64
	ParticipantID int64
	Kind          string
	Limit         int
}

// Events returns the feed, newest first.
func (s *Store) Events(f EventFilter) ([]Event, error) {
	q := `SELECT e.id, e.at, e.kind, COALESCE(e.game_id, 0), COALESCE(e.participant_id, 0), e.address, e.text, e.actor,
		COALESCE(g.name, ''), COALESCE(p.name, '')
		FROM events e LEFT JOIN games g ON g.id = e.game_id LEFT JOIN participants p ON p.id = e.participant_id`
	var where []string
	var args []any
	if f.GameID != 0 {
		where, args = append(where, "e.game_id = ?"), append(args, f.GameID)
	}
	if f.ParticipantID != 0 {
		where, args = append(where, "e.participant_id = ?"), append(args, f.ParticipantID)
	}
	if f.Kind != "" {
		where, args = append(where, "e.kind = ?"), append(args, f.Kind)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 500
	}
	q += " ORDER BY e.at DESC, e.id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Kind, &e.GameID, &e.ParticipantID, &e.Address, &e.Text, &e.Actor, &e.GameName, &e.ParticipantName); err != nil {
			return nil, err
		}
		e.At = parseTS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes what the retention says is old: events,
// and commands confirmed — or never confirmed — before the cut-off. A forget
// that a VM has not picked up within the retention never will be: that VM
// is gone. It returns the number of events deleted.
func (s *Store) Prune(now time.Time) (int, error) {
	set, err := s.Settings()
	if err != nil {
		return 0, err
	}
	cut := ts(now.Add(-time.Duration(set.RetentionDays) * 24 * time.Hour))
	res, err := s.db.Exec(`DELETE FROM events WHERE at < ?`, cut)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.Exec(`DELETE FROM commands WHERE created_at < ?`, cut); err != nil {
		return 0, err
	}
	// Expired admin sessions: they no longer log anyone in, so keeping
	// them only keeps addresses and browsers around.
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, ts(now)); err != nil {
		return 0, err
	}
	return int(n), nil
}
