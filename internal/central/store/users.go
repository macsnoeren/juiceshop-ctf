package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Admin accounts and their sessions (migration 4). This file only stores
// them: the password hashing and the cookie live in internal/central
// (password.go, auth.go). A password reaches this package already hashed and
// a session only as the SHA-256 of its cookie, so nothing here, and nothing
// in a copy of the database, can log anybody in.
//
// All users are equal: every one may do everything in admin, including
// adding and deleting other users. A lab has one or two teachers; roles
// would be a second thing to get wrong for no gain.

// ErrExists is a username that is already taken (case does not matter).
var ErrExists = errors.New("that username is already taken")

// User is one admin account.
type User struct {
	ID                int64
	Username          string
	PasswordHash      string
	CreatedAt         time.Time
	CreatedBy         string // "" for the account made on the setup page
	PasswordChangedAt time.Time
	LastLoginAt       *time.Time

	// Sessions is the number of sessions that have not expired; filled in
	// by Users only.
	Sessions int
}

// Session is a login: one browser, until it logs out or expires.
type Session struct {
	TokenHash  string
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	// Remember is "keep me logged in": a cookie that outlives the browser,
	// and a longer expiry on the server.
	Remember  bool
	Address   string
	UserAgent string
}

// ValidUsername trims u and checks it: 2–40 characters, letters, digits and
// . _ - @, starting with a letter or digit. The rule is narrow on purpose:
// a username appears in the event feed and on every admin page, and an
// address-like or space-padded name is a way to impersonate another.
func ValidUsername(u string) (string, error) {
	u = strings.TrimSpace(u)
	if len(u) < 2 || len(u) > 40 {
		return "", fmt.Errorf("a username is 2 to 40 characters")
	}
	for i, r := range u {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !alnum {
			return "", fmt.Errorf("a username starts with a letter or a digit")
		}
		if !alnum && !strings.ContainsRune("._-@", r) {
			return "", fmt.Errorf("a username has only letters, digits and . _ - @")
		}
	}
	return u, nil
}

const userCols = `id, username, password_hash, created_at, created_by, password_changed_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created, changed string
	var last sql.NullString
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &created, &u.CreatedBy, &changed, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt, u.PasswordChangedAt, u.LastLoginAt = parseTS(created), parseTS(changed), parseNullTS(last)
	return &u, nil
}

// UserCount is the number of accounts. Zero means the setup page is open.
func (s *Store) UserCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateFirstUser makes the account of the setup page, and ONLY while there
// is none: the check and the insert are one statement, so two people on the
// setup page at once cannot both become the first admin. ErrConflict when
// an account already exists.
func (s *Store) CreateFirstUser(username, hash string, now time.Time) (*User, error) {
	username, err := ValidUsername(username)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(`INSERT INTO users (username, password_hash, created_at, created_by, password_changed_at)
		SELECT ?, ?, ?, '', ? WHERE NOT EXISTS (SELECT 1 FROM users)`, username, hash, ts(now), ts(now))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrConflict
	}
	return s.UserByName(username)
}

// CreateUser adds an account; by is the user who added it.
func (s *Store) CreateUser(username, hash, by string, now time.Time) (*User, error) {
	username, err := ValidUsername(username)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`INSERT INTO users (username, password_hash, created_at, created_by, password_changed_at) VALUES (?, ?, ?, ?, ?)`,
		username, hash, ts(now), by, ts(now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrExists
		}
		return nil, err
	}
	return s.UserByName(username)
}

// UserByName finds an account, whatever the case of name.
func (s *Store) UserByName(name string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, strings.TrimSpace(name)))
}

// User finds an account by id.
func (s *Store) User(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// Users lists every account, by name, with its live sessions counted.
func (s *Store) Users(now time.Time) ([]User, error) {
	rows, err := s.db.Query(`SELECT `+userCols+`,
		(SELECT COUNT(*) FROM sessions x WHERE x.user_id = users.id AND x.expires_at > ?)
		FROM users ORDER BY username COLLATE NOCASE`, ts(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var created, changed string
		var last sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &created, &u.CreatedBy, &changed, &last, &u.Sessions); err != nil {
			return nil, err
		}
		u.CreatedAt, u.PasswordChangedAt, u.LastLoginAt = parseTS(created), parseTS(changed), parseNullTS(last)
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetPassword stores a new password hash and ends every session of the user
// except keep (the hash of the session that made the change, or ""): a
// changed password must lock out whoever knew the old one, everywhere.
func (s *Store) SetPassword(id int64, hash string, now time.Time, keep string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`, hash, ts(now), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, id, keep); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUser deletes an account and, through the cascade, its sessions.
func (s *Store) DeleteUser(id int64) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateSession stores a login and notes it as the user's last.
func (s *Store) CreateSession(x Session) error {
	if len(x.UserAgent) > 300 {
		x.UserAgent = x.UserAgent[:300]
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO sessions (token_hash, user_id, created_at, last_seen_at, expires_at, remember, address, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, x.TokenHash, x.UserID, ts(x.CreatedAt), ts(x.LastSeenAt), ts(x.ExpiresAt), x.Remember, x.Address, x.UserAgent); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, ts(x.CreatedAt), x.UserID); err != nil {
		return err
	}
	return tx.Commit()
}

// SessionUser returns the session with this hash and its user. ErrNotFound
// when there is none or it has expired: the two are the same to a browser.
func (s *Store) SessionUser(hash string, now time.Time) (*Session, *User, error) {
	var x Session
	var created, seen, expires string
	err := s.db.QueryRow(`SELECT token_hash, user_id, created_at, last_seen_at, expires_at, remember FROM sessions WHERE token_hash = ?`, hash).
		Scan(&x.TokenHash, &x.UserID, &created, &seen, &expires, &x.Remember)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	x.CreatedAt, x.LastSeenAt, x.ExpiresAt = parseTS(created), parseTS(seen), parseTS(expires)
	if !now.Before(x.ExpiresAt) {
		return nil, nil, ErrNotFound
	}
	u, err := s.User(x.UserID)
	if err != nil {
		return nil, nil, err
	}
	return &x, u, nil
}

// TouchSession records that a session was used and moves its expiry.
func (s *Store) TouchSession(hash string, seen, expires time.Time) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE token_hash = ?`, ts(seen), ts(expires), hash)
	return err
}

// DeleteSession ends one session (logging out).
func (s *Store) DeleteSession(hash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hash)
	return err
}

// DeleteSessions ends every session of a user except keep, and returns how
// many it ended ("log out everywhere else").
func (s *Store) DeleteSessions(userID int64, keep string) (int, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, keep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
