// Package store is ctf-central's SQLite database (VERSION-1-1.md §5): the
// games, their participants and solves, the commands for the VMs, the event
// feed and the flags. It LEADS: everything the operator configures is in
// here and changed through /admin/, nothing comes from the environment.
//
// One file on the ctf-central-db volume, WAL mode, one connection. The
// traffic is a report per VM per minute and a board refresh per viewer per
// ten seconds; one connection keeps every write serialised without
// SQLITE_BUSY, and is plenty for that.
//
// Times are stored as fixed-width UTC text (2006-01-02T15:04:05.000Z), so
// ORDER BY on the text is ORDER BY on the time. Every method that depends on
// the time takes it as an argument, so the tests can move the clock.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	_ "modernc.org/sqlite"
)

const tsLayout = "2006-01-02T15:04:05.000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func nullTS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsLayout, s)
	return t
}

func parseNullTS(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("not allowed in this state")
)

// DefaultScoring is points per difficulty, 1.0's table (DESIGN.md §8.3).
var DefaultScoring = map[int]int{1: 100, 2: 250, 3: 450, 4: 700, 5: 1000, 6: 1350}

// Store is the database.
type Store struct {
	db *sql.DB
}

// Open opens (and creates, and migrates) the database at path, and loads the
// challenge catalogue into it.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	if err := s.loadCatalogue(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database for the health check.
func (s *Store) Ping() error { return s.db.Ping() }

// migrate applies what is missing of migrations, each in its own
// transaction, on ONE connection with foreign keys off: SQLite ignores the
// pragma inside a transaction, and migration 2 rebuilds tables that others
// point at. foreign_key_check before each commit makes sure no migration
// leaves a dangling reference behind.
func (s *Store) migrate() error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	if current >= len(migrations) {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	for v := current; v < len(migrations); v++ {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		rows, err := tx.Query(`PRAGMA foreign_key_check`)
		if err != nil {
			tx.Rollback()
			return err
		}
		bad := rows.Next()
		rows.Close()
		if bad {
			tx.Rollback()
			return fmt.Errorf("migration %d leaves a dangling foreign key", v+1)
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

// loadCatalogue writes the embedded catalogue into the challenges table, so
// the board's SQL can join on it. Challenges of an older Juice Shop that are
// no longer in the catalogue stay (old solves point at them).
func (s *Store) loadCatalogue() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range juiceshop.Catalogue() {
		if _, err := tx.Exec(`INSERT INTO challenges (key, name, category, difficulty, disabled) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET name = excluded.name, category = excluded.category,
			difficulty = excluded.difficulty, disabled = excluded.disabled`,
			c.Key, c.Name, c.Category, c.Difficulty, c.Disabled()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Settings are the operator's settings that are not per game.
type Settings struct {
	Title         string
	Timezone      string
	RetentionDays int
}

// Settings reads the settings, with defaults for anything missing.
func (s *Store) Settings() (Settings, error) {
	out := Settings{Title: "Juice Shop CTF", Timezone: "Europe/Amsterdam", RetentionDays: 30}
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		switch k {
		case "title":
			out.Title = v
		case "timezone":
			out.Timezone = v
		case "event_retention_days":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				out.RetentionDays = n
			}
		}
	}
	return out, rows.Err()
}

// SaveSettings validates and stores the settings.
func (s *Store) SaveSettings(in Settings) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 80 {
		return fmt.Errorf("the title must be 1 to 80 characters")
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil || in.Timezone == "" {
		return fmt.Errorf("unknown time zone %q (e.g. Europe/Amsterdam)", in.Timezone)
	}
	if in.RetentionDays < 1 || in.RetentionDays > 3650 {
		return fmt.Errorf("the retention must be 1 to 3650 days")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range map[string]string{"title": in.Title, "timezone": in.Timezone, "event_retention_days": strconv.Itoa(in.RetentionDays)} {
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}
