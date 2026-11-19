// Package gate signs and checks the gate cookie (DESIGN.md §5.3).
//
// The cookie is   <registration_id>.<expiry unix seconds>.<base64url HMAC-SHA256>
//
// The registration id is IN the cookie, and ctf-status compares it with the
// agent's current registration on every check. That is what makes resetting a
// registration close the door for every cookie issued before it, with no
// session table to clean up: an old cookie still verifies here, and is then
// refused because its id is no longer the current one.
//
// The MAC covers "v1|<id>|<expiry>", so the id and the expiry cannot be
// changed without the secret, and a future format can change the prefix.
package gate

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// CookieName is the gate cookie's name. nginx never looks at it; it passes the
// request to ctf-status /gate, which does.
const CookieName = "jmnl_ctf_gate"

// Lifetime is how long a gate cookie is valid (§5.3).
const Lifetime = 14 * 24 * time.Hour

var (
	ErrMalformed = errors.New("gate cookie malformed")
	ErrSignature = errors.New("gate cookie signature invalid")
	ErrExpired   = errors.New("gate cookie expired")
)

func mac(id string, expiry int64, secret []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("v1|" + id + "|" + strconv.FormatInt(expiry, 10)))
	return h.Sum(nil)
}

// Sign returns the cookie value for a registration. The id must not contain a
// dot; the agent's ids are hex.
func Sign(registrationID string, expiry time.Time, secret []byte) string {
	exp := expiry.Unix()
	return registrationID + "." + strconv.FormatInt(exp, 10) + "." +
		base64.RawURLEncoding.EncodeToString(mac(registrationID, exp, secret))
}

// Verify checks the signature and the expiry and returns the registration id.
// It does NOT check that the id is still the current registration; the caller
// does that against the agent.
func Verify(cookie string, secret []byte, now time.Time) (string, error) {
	if len(cookie) > 256 {
		return "", ErrMalformed
	}
	parts := strings.Split(cookie, ".")
	if len(parts) != 3 || parts[0] == "" {
		return "", ErrMalformed
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", ErrMalformed
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", ErrMalformed
	}
	if !hmac.Equal(got, mac(parts[0], exp, secret)) {
		return "", ErrSignature
	}
	if now.Unix() >= exp {
		return "", ErrExpired
	}
	return parts[0], nil
}
