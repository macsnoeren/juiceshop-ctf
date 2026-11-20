// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package httpx

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"time"
)

// CSRF mints and checks the token every form carries.
//
// WHY NOT A DOUBLE-SUBMIT COOKIE (a random cookie the form repeats): whoever
// can SET a cookie for the host can forge it. Cookies are not separated by
// port, so script in Juice Shop on :80 can set one for the CTF page on
// :8080; another site on the same registrable domain can set one for the
// central server. A token here is an HMAC under a key only this process
// knows, over an expiry and a BINDING (the admin's session, say): nobody can
// make one without the key, and one made for another binding is worthless.
//
// The key lives in memory: after a restart every open form has expired,
// and the page says so and gives a fresh one.
//
// A token is lower-case hex only (16 for the expiry, 64 for the MAC), so the
// scripts that read it from a page keep matching [0-9a-f]+.
type CSRF struct {
	key  [32]byte
	life time.Duration
}

// NewCSRF returns a CSRF with a fresh key; a token lasts life.
func NewCSRF(life time.Duration) *CSRF {
	c := &CSRF{life: life}
	if _, err := rand.Read(c.key[:]); err != nil {
		panic(err) // crypto/rand does not fail on a supported platform
	}
	return c
}

func (c *CSRF) mac(exp []byte, binding string) []byte {
	m := hmac.New(sha256.New, c.key[:])
	m.Write(exp)
	m.Write([]byte{0})
	m.Write([]byte(binding))
	return m.Sum(nil)
}

// Token returns a token for binding, valid until now + life.
func (c *CSRF) Token(binding string, now time.Time) string {
	exp := make([]byte, 8)
	binary.BigEndian.PutUint64(exp, uint64(now.Add(c.life).Unix()))
	return hex.EncodeToString(exp) + hex.EncodeToString(c.mac(exp, binding))
}

// Valid reports whether token was made by this CSRF for binding and has not
// expired.
func (c *CSRF) Valid(token, binding string, now time.Time) bool {
	if len(token) != 16+64 {
		return false
	}
	exp, err1 := hex.DecodeString(token[:16])
	got, err2 := hex.DecodeString(token[16:])
	if err1 != nil || err2 != nil {
		return false
	}
	if now.Unix() >= int64(binary.BigEndian.Uint64(exp)) {
		return false
	}
	return hmac.Equal(got, c.mac(exp, binding))
}

// CrossOrigin reports whether the browser says this request comes from
// another origin (Sec-Fetch-Site: same-site or cross-site). The browser sets
// that header itself and script cannot change it. Without the header (an
// older browser, curl) it reports false: the token still has to be right.
func CrossOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return false
	}
	return true
}
