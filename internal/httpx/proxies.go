// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package httpx

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Proxies are the peers whose X-Real-IP and X-Forwarded-Proto are believed.
//
// >>> A HEADER IS ONLY AS GOOD AS WHO SENT IT. From any other peer, X-Real-IP
// is whatever the client typed: believed, it lets one client pose as a new
// address with every request (round every per-address limit), write any
// address into the audit log, and put a spreadsheet formula into the events
// CSV. So the header counts only when the peer itself is a trusted proxy, and
// only when it holds one valid IP.
type Proxies []netip.Prefix

// DefaultTrustedProxies is loopback and the private ranges: a reverse proxy
// on the same host, or in another container on a Docker network. A client
// that reaches ctf-central from the internet is never in them.
const DefaultTrustedProxies = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

// ParseProxies reads a comma-separated list of addresses and CIDR prefixes.
// "none" (or "") trusts nobody: every address is the peer's own.
func ParseProxies(s string) (Proxies, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "none") {
		return Proxies{}, nil
	}
	var out Proxies
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", f, err)
			}
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", f, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// MustProxies is ParseProxies for a constant.
func MustProxies(s string) Proxies {
	p, err := ParseProxies(s)
	if err != nil {
		panic(err)
	}
	return p
}

func peer(r *http.Request) (netip.Addr, string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, host
	}
	return a.Unmap(), a.Unmap().String()
}

func (p Proxies) trusts(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	for _, pre := range p {
		if pre.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP is the client's address: X-Real-IP when a trusted proxy sent it
// and it is one valid IP, the peer's address otherwise. The result is
// always an IP in its canonical form (at most 45 characters), never text a
// client chose.
func (p Proxies) ClientIP(r *http.Request) string {
	a, s := peer(r)
	if p.trusts(a) {
		// No zone: netip accepts "fe80::1%<any text>", and the zone would
		// be text the client chose, of any length.
		if v, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil && v.Zone() == "" {
			return v.Unmap().String()
		}
	}
	if len(s) > 45 {
		s = s[:45]
	}
	return s
}

// Secure reports whether the browser used HTTPS: TLS on this connection, or
// X-Forwarded-Proto from a trusted proxy.
func (p Proxies) Secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	a, _ := peer(r)
	return p.trusts(a) && r.Header.Get("X-Forwarded-Proto") == "https"
}
