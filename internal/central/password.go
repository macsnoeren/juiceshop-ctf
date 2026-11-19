package central

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Admin passwords are stored as PBKDF2-HMAC-SHA256 with a random salt,
// written as
//
//	pbkdf2-sha256$<iterations>$<salt, base64>$<key, base64>
//
// WHY PBKDF2 and not bcrypt or argon2: it is in Go's standard library
// (crypto/pbkdf2), so it adds no dependency to a binary that has two, and
// at OWASP's 600 000 iterations for SHA-256 it is an accepted choice. The
// iteration count is in every hash, so raising it later only affects new
// and changed passwords; old ones keep verifying.
//
// A login therefore costs a fraction of a second of CPU on purpose. The
// login limits in auth.go keep that from becoming a way to load the server.

// passwordIterations is the cost of a new hash. A variable only so the tests
// can lower it; never lower it in production code.
var passwordIterations = 600_000

const (
	passwordPrefix = "pbkdf2-sha256"
	// MinPasswordLen is the shortest password accepted. Length is the one
	// rule that matters (NIST SP 800-63B); no composition rules.
	MinPasswordLen = 10
	maxPasswordLen = 200
)

// checkPassword says what is wrong with a new password, or nil.
func checkPassword(pw, username string) error {
	switch n := utf8.RuneCountInString(pw); {
	case n < MinPasswordLen:
		return fmt.Errorf("a password is at least %d characters", MinPasswordLen)
	case len(pw) > maxPasswordLen:
		return fmt.Errorf("a password is at most %d characters", maxPasswordLen)
	}
	if strings.EqualFold(strings.TrimSpace(pw), strings.TrimSpace(username)) {
		return fmt.Errorf("the password cannot be the username")
	}
	return nil
}

// hashPassword returns the stored form of pw.
func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, pw, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return passwordPrefix + "$" + strconv.Itoa(passwordIterations) + "$" + enc.EncodeToString(salt) + "$" + enc.EncodeToString(key), nil
}

// verifyPassword checks pw against a stored hash, in constant time.
func verifyPassword(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != passwordPrefix {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > 10_000_000 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is verified against when a username does not exist, so a
// failed login takes as long for an unknown user as for a wrong password,
// and the time does not tell which usernames exist.
var dummyHash = sync.OnceValue(func() string {
	h, _ := hashPassword("not a password of anybody")
	return h
})

func verifyNobody(pw string) {
	verifyPassword(dummyHash(), pw)
}
