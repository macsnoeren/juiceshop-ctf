// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- X-Real-IP counts only from a trusted proxy, and only as an IP ----

func TestClientIP(t *testing.T) {
	def := MustProxies(DefaultTrustedProxies)
	none := MustProxies("none")
	cases := []struct {
		peer, header string
		p            Proxies
		want         string
	}{
		{"172.18.0.5:5000", "198.51.100.7", def, "198.51.100.7"},        // nginx on a Docker network
		{"127.0.0.1:5000", "2001:db8::1", def, "2001:db8::1"},           // nginx on this host
		{"[::ffff:10.0.0.2]:5000", "198.51.100.7", def, "198.51.100.7"}, // a 4-in-6 peer
		{"203.0.113.9:5000", "198.51.100.7", def, "203.0.113.9"},        // a client from the internet
		{"172.18.0.5:5000", "=cmd|' /C calc'!A0", def, "172.18.0.5"},    // not an IP
		{"172.18.0.5:5000", "198.51.100.7, 10.0.0.1", def, "172.18.0.5"},
		{"172.18.0.5:5000", "fe80::1%=HYPERLINK(\"x\")" + strings.Repeat("a", 500), def, "172.18.0.5"}, // a zone is text

		{"172.18.0.5:5000", "", def, "172.18.0.5"},
		{"172.18.0.5:5000", "198.51.100.7", none, "172.18.0.5"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.peer
		if c.header != "" {
			r.Header.Set("X-Real-IP", c.header)
		}
		if got := c.p.ClientIP(r); got != c.want {
			t.Errorf("peer %s, X-Real-IP %q: %q, want %q", c.peer, c.header, got, c.want)
		}
	}
	if _, err := ParseProxies("10.0.0.0/8, nonsense"); err == nil {
		t.Error("a bad proxy list is accepted")
	}
}

func TestSecure(t *testing.T) {
	p := MustProxies("10.0.0.1")
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.RemoteAddr = "10.0.0.1:443"
	if !p.Secure(r) {
		t.Error("X-Forwarded-Proto from the proxy is not believed")
	}
	r.RemoteAddr = "203.0.113.9:443"
	if p.Secure(r) {
		t.Error("X-Forwarded-Proto from a client is believed")
	}
}

// ---- a form token is made only here, for one binding, for a while ----

func TestCSRF(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	c := NewCSRF(time.Hour)
	tok := c.Token("session A", now)
	if len(tok) != 80 || strings.Trim(tok, "0123456789abcdef") != "" {
		t.Fatalf("token %q is not 80 hex characters", tok)
	}
	if !c.Valid(tok, "session A", now.Add(59*time.Minute)) {
		t.Error("a fresh token is refused")
	}
	if c.Valid(tok, "session B", now) {
		t.Error("a token is good for another binding")
	}
	if c.Valid(tok, "session A", now.Add(time.Hour)) {
		t.Error("an expired token is accepted")
	}
	if NewCSRF(time.Hour).Valid(tok, "session A", now) {
		t.Error("a token is good under another key")
	}
	// The expiry cannot be moved: it is under the MAC.
	if c.Valid("ffffffffffffffff"+tok[16:], "session A", now) {
		t.Error("a token with a later expiry is accepted")
	}
	for _, bad := range []string{"", "xyz", strings.Repeat("0", 80), tok[:79]} {
		if c.Valid(bad, "session A", now) {
			t.Errorf("%q is accepted", bad)
		}
	}
}

func TestCrossOrigin(t *testing.T) {
	for v, cross := range map[string]bool{"": false, "same-origin": false, "none": false, "same-site": true, "cross-site": true} {
		r := httptest.NewRequest("POST", "/", nil)
		if v != "" {
			r.Header.Set("Sec-Fetch-Site", v)
		}
		if CrossOrigin(r) != cross {
			t.Errorf("Sec-Fetch-Site %q: %v", v, !cross)
		}
	}
}
