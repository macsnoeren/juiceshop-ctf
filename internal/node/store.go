package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
	_ "modernc.org/sqlite"
)

// Store is node.db (VERSION-1-1.md §4.1): one SQLite file in /var/lib/ctf,
// the directory 0700 and the file 0600, both owned by 10001, so Juice Shop
// (65532) can read neither the token nor the continue codes.
//
// secure_delete is on: Forget and a delivered join overwrite what they
// delete instead of leaving it in free pages, and the WAL is truncated after
// them, so a join code or a token that was deleted cannot be read back out of
// the file.
type Store struct {
	db   *sql.DB
	path string
}

const tsLayout = time.RFC3339Nano

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsLayout, s)
	return t
}

func nullTS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

// studentTables are the tables Forget empties: everything about the
// student. settings (the central server's address) is not about the student.
var studentTables = []string{"membership", "outbox", "solves", "codes", "baseline", "standing", "status", "commands_applied", "sessions", "gate_failures"}

var nodeMigrations = []string{
	`CREATE TABLE membership (
		id               INTEGER PRIMARY KEY CHECK (id = 1),
		status           TEXT NOT NULL,
		name             TEXT NOT NULL,
		node_uid         TEXT NOT NULL,
		joined_at        TEXT NOT NULL,
		central_url      TEXT NOT NULL DEFAULT '',
		participant_id   INTEGER NOT NULL DEFAULT 0,
		token            TEXT NOT NULL DEFAULT '',
		game             TEXT NOT NULL DEFAULT '{}',
		board_url        TEXT NOT NULL DEFAULT '',
		problem          TEXT NOT NULL DEFAULT '',
		return_code_hash TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE outbox (
		id         INTEGER PRIMARY KEY CHECK (id = 1),
		name       TEXT NOT NULL,
		code       TEXT NOT NULL,
		joined_at  TEXT NOT NULL,
		attempts   INTEGER NOT NULL DEFAULT 0,
		next_at    TEXT NOT NULL,
		last_error TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE solves (
		key       TEXT PRIMARY KEY,
		solved_at TEXT NOT NULL,
		cheat     REAL
	);
	CREATE TABLE codes (
		id         INTEGER PRIMARY KEY CHECK (id = 1),
		challenges TEXT NOT NULL DEFAULT '',
		find_it    TEXT NOT NULL DEFAULT '',
		fix_it     TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE baseline (
		key  TEXT PRIMARY KEY,
		boot TEXT NOT NULL
	);
	CREATE TABLE standing (
		id    INTEGER PRIMARY KEY CHECK (id = 1),
		rank  INTEGER NOT NULL,
		of    INTEGER NOT NULL,
		score INTEGER NOT NULL,
		at    TEXT NOT NULL
	);
	CREATE TABLE status (
		id             INTEGER PRIMARY KEY CHECK (id = 1),
		restore_failed INTEGER NOT NULL DEFAULT 0,
		last_report_at TEXT
	);
	CREATE TABLE commands_applied (
		id TEXT PRIMARY KEY,
		at TEXT NOT NULL
	);
	CREATE TABLE sessions (
		hash       TEXT PRIMARY KEY,
		expires_at TEXT NOT NULL
	);
	CREATE TABLE gate_failures (
		at TEXT NOT NULL
	);
	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
}

// OpenStore opens (creating and migrating) node.db at path. The directory is
// made 0700 and the file 0600 BEFORE SQLite touches them; SQLite gives the
// -wal and -shm files the database file's mode.
func OpenStore(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("making %s private: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=secure_delete(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	return s, nil
}

// Close closes node.db.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var cur int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&cur); err != nil {
		return err
	}
	for v := cur; v < len(nodeMigrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(nodeMigrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations VALUES (?, ?)`, v+1, ts(time.Now())); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion is the number of migrations applied.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

// Load reads the state.
func (s *Store) Load() (*State, error) {
	st := newState()
	var m Membership
	var joined, game string
	err := s.db.QueryRow(`SELECT status, name, node_uid, joined_at, central_url, participant_id, token, game, board_url, problem, return_code_hash
		FROM membership WHERE id = 1`).Scan(&m.Status, &m.Name, &m.NodeUID, &joined, &m.CentralURL, &m.ParticipantID, &m.Token, &game, &m.BoardURL, &m.Problem, &m.ReturnCodeHash)
	switch {
	case err == nil:
		m.JoinedAt = parseTS(joined)
		json.Unmarshal([]byte(game), &m.Game)
		st.Membership = &m
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	var o Outbox
	var oj, on string
	err = s.db.QueryRow(`SELECT name, code, joined_at, attempts, next_at, last_error FROM outbox WHERE id = 1`).Scan(&o.Name, &o.Code, &oj, &o.Attempts, &on, &o.LastError)
	switch {
	case err == nil:
		o.JoinedAt, o.NextAt = parseTS(oj), parseTS(on)
		st.Outbox = &o
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	rows, err := s.db.Query(`SELECT key, solved_at, cheat FROM solves`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k, at string
		var cheat sql.NullFloat64
		if err := rows.Scan(&k, &at, &cheat); err != nil {
			rows.Close()
			return nil, err
		}
		st.Solved[k] = parseTS(at)
		if cheat.Valid {
			st.Cheat[k] = cheat.Float64
		}
	}
	rows.Close()
	err = s.db.QueryRow(`SELECT challenges, find_it, fix_it FROM codes WHERE id = 1`).Scan(&st.ContinueCodes.Challenges, &st.ContinueCodes.FindIt, &st.ContinueCodes.FixIt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT key, boot FROM baseline ORDER BY key`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k, &st.BaselineBoot); err != nil {
			rows.Close()
			return nil, err
		}
		st.Baseline = append(st.Baseline, k)
	}
	rows.Close()
	var sd Standing
	var sat string
	err = s.db.QueryRow(`SELECT rank, of, score, at FROM standing WHERE id = 1`).Scan(&sd.Rank, &sd.Of, &sd.Score, &sat)
	switch {
	case err == nil:
		sd.At = parseTS(sat)
		st.Standing = &sd
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	var last sql.NullString
	err = s.db.QueryRow(`SELECT restore_failed, last_report_at FROM status WHERE id = 1`).Scan(&st.RestoreFailed, &last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if last.Valid {
		t := parseTS(last.String)
		st.LastReportAt = &t
	}
	rows, err = s.db.Query(`SELECT id FROM commands_applied ORDER BY at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		st.CommandsApplied = append(st.CommandsApplied, id)
	}
	return st, rows.Err()
}

// Save writes the whole state in one transaction. It is small (a hundred
// solves at most), so rewriting it is simpler than tracking changes.
func (s *Store) Save(st *State) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"membership", "outbox", "solves", "codes", "baseline", "standing", "status", "commands_applied"} {
		if _, err := tx.Exec(`DELETE FROM ` + t); err != nil {
			return err
		}
	}
	if m := st.Membership; m != nil {
		game, _ := json.Marshal(m.Game)
		if _, err := tx.Exec(`INSERT INTO membership (id, status, name, node_uid, joined_at, central_url, participant_id, token, game, board_url, problem, return_code_hash)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.Status, m.Name, m.NodeUID, ts(m.JoinedAt), m.CentralURL, m.ParticipantID, m.Token, string(game), m.BoardURL, m.Problem, m.ReturnCodeHash); err != nil {
			return err
		}
	}
	if o := st.Outbox; o != nil {
		if _, err := tx.Exec(`INSERT INTO outbox (id, name, code, joined_at, attempts, next_at, last_error) VALUES (1, ?, ?, ?, ?, ?, ?)`,
			o.Name, o.Code, ts(o.JoinedAt), o.Attempts, ts(o.NextAt), o.LastError); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(st.Solved))
	for k := range st.Solved {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var cheat any
		if v, ok := st.Cheat[k]; ok {
			cheat = v
		}
		if _, err := tx.Exec(`INSERT INTO solves (key, solved_at, cheat) VALUES (?, ?, ?)`, k, ts(st.Solved[k]), cheat); err != nil {
			return err
		}
	}
	if !st.ContinueCodes.Empty() {
		c := st.ContinueCodes
		if _, err := tx.Exec(`INSERT INTO codes (id, challenges, find_it, fix_it) VALUES (1, ?, ?, ?)`, c.Challenges, c.FindIt, c.FixIt); err != nil {
			return err
		}
	}
	for _, k := range st.Baseline {
		if _, err := tx.Exec(`INSERT INTO baseline (key, boot) VALUES (?, ?)`, k, st.BaselineBoot); err != nil {
			return err
		}
	}
	if sd := st.Standing; sd != nil {
		if _, err := tx.Exec(`INSERT INTO standing (id, rank, of, score, at) VALUES (1, ?, ?, ?, ?)`, sd.Rank, sd.Of, sd.Score, ts(sd.At)); err != nil {
			return err
		}
	}
	if st.RestoreFailed || st.LastReportAt != nil {
		if _, err := tx.Exec(`INSERT INTO status (id, restore_failed, last_report_at) VALUES (1, ?, ?)`, st.RestoreFailed, nullTS(st.LastReportAt)); err != nil {
			return err
		}
	}
	for i, id := range st.CommandsApplied {
		if _, err := tx.Exec(`INSERT INTO commands_applied (id, at) VALUES (?, ?)`, id, fmt.Sprintf("%06d", i)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Wipe empties every student table (Forget, VERSION-1-1.md §4.4) and
// truncates the WAL, so nothing of the student is left in the file.
func (s *Store) Wipe() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range studentTables {
		if _, err := tx.Exec(`DELETE FROM ` + t); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.Checkpoint()
}

// Checkpoint folds the WAL into the database and truncates it.
func (s *Store) Checkpoint() error {
	_, err := s.db.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// RowCounts returns the number of rows of each student table, for tests and
// for the operator's check before exporting a VM.
func (s *Store) RowCounts() (map[string]int, error) {
	out := map[string]int{}
	for _, t := range studentTables {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + t).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}

// Setting reads a setting ("" when unset).
func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a setting; "" deletes it.
func (s *Store) SetSetting(key, value string) error {
	if value == "" {
		_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// The optional gate's sessions (CTF_GATE=session, VERSION-1-1.md §4.2):
// server-side, so deleting a cookie loses nothing the node cannot give back
// for the name and the return code.

// NewSession stores a session and returns its id (the cookie value).
func (s *Store) NewSession(now time.Time, life time.Duration) (string, error) {
	id := protocol.NewNodeUID() + protocol.NewNodeUID()
	_, err := s.db.Exec(`INSERT INTO sessions (hash, expires_at) VALUES (?, ?)`, protocol.Hash(id), ts(now.Add(life)))
	return id, err
}

// ValidSession reports whether a cookie value is a live session.
func (s *Store) ValidSession(id string, now time.Time) bool {
	if len(id) != 64 {
		return false
	}
	var exp string
	if err := s.db.QueryRow(`SELECT expires_at FROM sessions WHERE hash = ?`, protocol.Hash(id)).Scan(&exp); err != nil {
		return false
	}
	return now.Before(parseTS(exp))
}

// GateFailure records a wrong return code and returns how many there were
// in the window.
func (s *Store) GateFailures(now time.Time, window time.Duration, add bool) (int, error) {
	if _, err := s.db.Exec(`DELETE FROM gate_failures WHERE at < ?`, ts(now.Add(-window))); err != nil {
		return 0, err
	}
	if add {
		if _, err := s.db.Exec(`INSERT INTO gate_failures (at) VALUES (?)`, ts(now)); err != nil {
			return 0, err
		}
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM gate_failures`).Scan(&n)
	return n, err
}

// ImportV1 reads a 1.0 agent's state.json, if there is one (VERSION-1-1.md
// §8): its solves and continue codes come along, its registration does NOT —
// it belonged to the shared instance identity that 1.1 removes. The file is
// renamed state.json.imported by the caller once the result is saved, so it
// is imported once. It returns the number of solves imported, or -1 when
// there was no file.
func ImportV1(path string, st *State) (int, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	var v1 struct {
		Solved        map[string]time.Time `json:"solved"`
		Cheat         map[string]float64   `json:"cheat"`
		ContinueCodes juiceshop.Codes      `json:"continue_codes"`
	}
	if err := json.Unmarshal(b, &v1); err != nil {
		return 0, fmt.Errorf("%s is not a 1.0 state file: %w", path, err)
	}
	n := 0
	for k, t := range v1.Solved {
		if _, ok := st.Solved[k]; !ok {
			st.Solved[k] = t
			n++
		}
	}
	for k, v := range v1.Cheat {
		if _, ok := st.Cheat[k]; !ok {
			st.Cheat[k] = v
		}
	}
	if st.ContinueCodes.Empty() {
		st.ContinueCodes = v1.ContinueCodes
	}
	return n, nil
}
