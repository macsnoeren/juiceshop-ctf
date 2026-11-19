package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"regexp"
	"strings"
)

// JoinCodeAlphabet has no 0/o, 1/i/l: a code is read off a beamer and typed
// by thirty students, and those are the characters people confuse. 31
// characters, 10 of them: about 50 bits, far beyond guessing at the rate the
// join endpoint allows (VERSION-1-1.md §5.6).
const (
	JoinCodeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	JoinCodeLength   = 10
	// ReturnCodeLength is the personal code of the optional gate
	// (CTF_GATE=session, VERSION-1-1.md §4.2): shorter, it guards one VM.
	ReturnCodeLength = 8
	TokenPrefix      = "ctfp_"
)

// NewJoinCode returns a fresh join code.
func NewJoinCode() string { return randomCode(rand.Reader, JoinCodeLength) }

// NewReturnCode returns a fresh return code for the optional gate.
func NewReturnCode() string { return randomCode(rand.Reader, ReturnCodeLength) }

// randomCode draws n characters from the alphabet WITHOUT bias: a byte is
// used only when it is below the largest multiple of 31 that fits in a byte
// (248), so every character has the same 8 of the 248 values. A plain
// `b % 31` would give the first 8 characters 9 values out of 256 and the
// others 8 — 12 % more likely, which the test in codes_test.go detects.
func randomCode(r io.Reader, n int) string {
	const k = len(JoinCodeAlphabet)
	limit := byte(256 / k * k)
	out := make([]byte, 0, n)
	buf := make([]byte, 2*n)
	for len(out) < n {
		if _, err := io.ReadFull(r, buf); err != nil {
			panic(err) // crypto/rand does not fail on a supported platform
		}
		for _, b := range buf {
			if b < limit && len(out) < n {
				out = append(out, JoinCodeAlphabet[int(b)%k])
			}
		}
	}
	return string(out)
}

var codeRE = regexp.MustCompile(`^[` + JoinCodeAlphabet + `]+$`)

func normalizeCode(s string, n int) (string, bool) {
	s = strings.ToLower(strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		return r
	}, s))
	if len(s) != n || !codeRE.MatchString(s) {
		return "", false
	}
	return s, true
}

// NormalizeJoinCode lower-cases a code as typed and removes spaces and
// dashes; ok is false when what is left is not a join code.
func NormalizeJoinCode(s string) (string, bool) { return normalizeCode(s, JoinCodeLength) }

// NormalizeReturnCode is NormalizeJoinCode for return codes.
func NormalizeReturnCode(s string) (string, bool) { return normalizeCode(s, ReturnCodeLength) }

// FormatCode groups a code in fives for display: "4ffpq-7tq9f". The dash is
// removed again by NormalizeJoinCode.
func FormatCode(c string) string {
	var b strings.Builder
	for i, r := range c {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NewParticipantToken returns a fresh participant token: "ctfp_" and 256
// random bits. A sha256 of it needs no salt and no slow hash.
func NewParticipantToken() string { return TokenPrefix + randomHex(32) }

var tokenRE = regexp.MustCompile(`^ctfp_[0-9a-f]{64}$`)

// ValidParticipantToken reports whether t has the shape of a token. Checked
// before hashing, so garbage in an Authorization header costs nothing.
func ValidParticipantToken(t string) bool { return tokenRE.MatchString(t) }

// Hash is the stored form of a token, join code or return code: hex
// SHA-256. Codes and tokens are random, so no salt is needed; a join code's
// hash is also what the central server looks the game up by.
func Hash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// HashMatches compares a secret with a stored hash in constant time.
func HashMatches(secret, stored string) bool {
	if secret == "" || stored == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(Hash(secret)), []byte(stored)) == 1
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
