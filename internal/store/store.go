// Package store is ctf-central's SQLite database (DESIGN.md §8).
//
// One file on the ctf-central-db volume, WAL mode, one connection. The traffic
// is a report per instance per minute and a leaderboard refresh per viewer per
// ten seconds; one connection keeps every write serialised without
// SQLITE_BUSY, and is plenty for that.
//
// Times are stored as fixed-width UTC text (2006-01-02T15:04:05.000Z), so
// that ORDER BY on the text is ORDER BY on the time.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
	_ "modernc.org/sqlite"
)

const tsLayout = "2006-01-02T15:04:05.000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsLayout, s)
	return t
}

func parseNullTS(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// DefaultScoring is points per difficulty (§8.3).
var DefaultScoring = map[int]int{1: 100, 2: 250, 3: 450, 4: 700, 5: 1000, 6: 1350}

// Store is the database.
type Store struct {
	db *sql.DB
}

// Open opens (and creates, and migrates) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database for the health check.
func (s *Store) Ping() error { return s.db.Ping() }

// migrations are applied in order, each once. NEVER edit one that has been
// released: add a new one. A database is migrated on every start, so an
// edited migration would never run on a database that already has it.
var migrations = []string{
	`CREATE TABLE instances (
		id                TEXT PRIMARY KEY,
		label             TEXT NOT NULL DEFAULT '',
		token_hash        TEXT NOT NULL UNIQUE,
		created_at        TEXT NOT NULL,
		last_report_at    TEXT,
		agent_version     TEXT NOT NULL DEFAULT '',
		juiceshop_version TEXT NOT NULL DEFAULT '',
		reachable         INTEGER NOT NULL DEFAULT 0,
		restore_failed    INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE participants (
		id              INTEGER PRIMARY KEY,
		instance_id     TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
		registration_id TEXT NOT NULL,
		name            TEXT NOT NULL,
		registered_at   TEXT NOT NULL,
		ended_at        TEXT,
		hidden          INTEGER NOT NULL DEFAULT 0,
		UNIQUE (instance_id, registration_id)
	);
	CREATE TABLE challenges (
		key        TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		category   TEXT NOT NULL,
		difficulty INTEGER NOT NULL,
		disabled   INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE solves (
		participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,
		challenge_key  TEXT NOT NULL,
		solved_at      TEXT NOT NULL,
		cheat          REAL,
		PRIMARY KEY (participant_id, challenge_key)
	);
	CREATE INDEX solves_by_time ON solves (solved_at);
	CREATE TABLE commands (
		id           TEXT PRIMARY KEY,
		instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
		type         TEXT NOT NULL,
		created_at   TEXT NOT NULL,
		delivered_at TEXT,
		confirmed_at TEXT
	);
	CREATE TABLE scoring (
		difficulty INTEGER PRIMARY KEY,
		points     INTEGER NOT NULL
	);
	INSERT INTO scoring (difficulty, points) VALUES (1,100),(2,250),(3,450),(4,700),(5,1000),(6,1350);`,
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	for v := current; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v+1, ts(time.Now())); err != nil {
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

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// NewToken returns a fresh instance token. 256 random bits: a sha256 of it
// needs no salt and no slow hash.
func NewToken() string { return "ctf_" + httpx.RandomHex(32) }

// Instance is one CTF instance as the admin page shows it.
type Instance struct {
	ID               string
	Label            string
	CreatedAt        time.Time
	LastReportAt     *time.Time
	AgentVersion     string
	JuiceShopVersion string
	Reachable        bool
	RestoreFailed    bool
	Participant      string // the active participant's name, if any
}

// Online reports whether the instance reported within the last 3 minutes.
func (i Instance) Online(now time.Time) bool {
	return i.LastReportAt != nil && now.Sub(*i.LastReportAt) < 3*time.Minute
}

// CreateInstance creates an instance and returns its token. The token is
// returned ONCE; only its hash is stored.
func (s *Store) CreateInstance(id, label string, now time.Time) (string, error) {
	if !protocol.ValidInstanceID(id) {
		return "", fmt.Errorf("instance id must be lower-case letters, digits and dashes")
	}
	tok := NewToken()
	_, err := s.db.Exec(`INSERT INTO instances (id, label, token_hash, created_at) VALUES (?, ?, ?, ?)`,
		id, label, hashToken(tok), ts(now))
	if err != nil {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM instances WHERE id = ?`, id).Scan(&n)
		if n > 0 {
			return "", ErrExists
		}
		return "", err
	}
	return tok, nil
}

// SeedInstance creates or updates an instance with a GIVEN token. It is for
// test setups (docker/), where the agent's env file and the central server
// must agree on a token without a trip through /admin/. Idempotent.
func (s *Store) SeedInstance(id, label, token string, now time.Time) error {
	if !protocol.ValidInstanceID(id) {
		return fmt.Errorf("instance id %q: lower-case letters, digits and dashes", id)
	}
	if len(token) < 16 {
		return fmt.Errorf("seed token for %s must be at least 16 characters", id)
	}
	_, err := s.db.Exec(`INSERT INTO instances (id, label, token_hash, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET token_hash = excluded.token_hash, label = excluded.label`,
		id, label, hashToken(token), ts(now))
	return err
}

// RotateToken gives an instance a new token; the old one stops working at once.
func (s *Store) RotateToken(id string) (string, error) {
	tok := NewToken()
	res, err := s.db.Exec(`UPDATE instances SET token_hash = ? WHERE id = ?`, hashToken(tok), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return tok, nil
}

// InstanceForToken returns the instance a token belongs to.
func (s *Store) InstanceForToken(token string) (string, error) {
	if token == "" {
		return "", ErrNotFound
	}
	var id string
	err := s.db.QueryRow(`SELECT id FROM instances WHERE token_hash = ?`, hashToken(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// Instances lists every instance.
func (s *Store) Instances() ([]Instance, error) {
	rows, err := s.db.Query(`SELECT i.id, i.label, i.created_at, i.last_report_at, i.agent_version,
		i.juiceshop_version, i.reachable, i.restore_failed,
		COALESCE((SELECT name FROM participants p WHERE p.instance_id = i.id AND p.ended_at IS NULL ORDER BY p.id DESC LIMIT 1), '')
		FROM instances i ORDER BY i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		var in Instance
		var created string
		var last sql.NullString
		if err := rows.Scan(&in.ID, &in.Label, &created, &last, &in.AgentVersion, &in.JuiceShopVersion,
			&in.Reachable, &in.RestoreFailed, &in.Participant); err != nil {
			return nil, err
		}
		in.CreatedAt = parseTS(created)
		in.LastReportAt = parseNullTS(last)
		out = append(out, in)
	}
	return out, rows.Err()
}

// Scoring returns points per difficulty.
func (s *Store) Scoring() (map[int]int, error) {
	rows, err := s.db.Query(`SELECT difficulty, points FROM scoring`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int]int{}
	for rows.Next() {
		var d, p int
		if err := rows.Scan(&d, &p); err != nil {
			return nil, err
		}
		m[d] = p
	}
	return m, rows.Err()
}

// SetScoring changes the points for one difficulty.
func (s *Store) SetScoring(difficulty, points int) error {
	if difficulty < 1 || difficulty > 6 || points < 0 || points > 100000 {
		return fmt.Errorf("difficulty 1-6, points 0-100000")
	}
	_, err := s.db.Exec(`INSERT INTO scoring (difficulty, points) VALUES (?, ?)
		ON CONFLICT (difficulty) DO UPDATE SET points = excluded.points`, difficulty, points)
	return err
}
