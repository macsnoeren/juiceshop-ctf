// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package node

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
)

func storeFilter(kind string) store.EventFilter { return store.EventFilter{Kind: kind} }

func regexpFind(re, s string) string {
	m := regexp.MustCompile(re).FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// backend stands in for Juice Shop: it echoes the path, redirects, and
// upgrades /socket.io/ to an echo connection.
func backend(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/redirect":
			http.Redirect(w, r, "/#/score-board", http.StatusFound)
		case r.URL.Path == "/redirect-abs":
			w.Header().Set("Location", "http://"+r.Host+"/profile")
			w.WriteHeader(http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/socket.io/"):
			if r.Header.Get("Upgrade") != "websocket" {
				http.Error(w, "no upgrade", 400)
				return
			}
			conn, rw, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			rw.Flush()
			line, _ := rw.ReadString('\n')
			rw.WriteString("echo:" + line)
			rw.Flush()
		default:
			io.WriteString(w, "path="+r.URL.Path)
		}
	}))
}

func proxyServer(t *testing.T) (*httptest.Server, *httptest.Server) {
	be := backend(t)
	t.Cleanup(be.Close)
	target, _ := url.Parse(be.URL)
	front := httptest.NewServer(NewProxy(target, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(front.Close)
	return front, be
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestProxyStripsAndRestoresThePrefix(t *testing.T) {
	front, _ := proxyServer(t)
	for path, want := range map[string]string{
		"/juiceshop/rest/products/search": "path=/rest/products/search",
		"/juiceshop/":                     "path=/",
		"/assets/i18n/en.json":            "path=/assets/i18n/en.json",
	} {
		resp, err := http.Get(front.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != want {
			t.Errorf("%s reached the backend as %q, want %q", path, b, want)
		}
	}
	for path, want := range map[string]string{"/juiceshop/redirect": "/juiceshop/#/score-board", "/juiceshop/redirect-abs": "/juiceshop/profile"} {
		resp, err := noRedirect().Get(front.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Location"); got != want {
			t.Errorf("%s: Location %q, want %q", path, got, want)
		}
	}
}

func TestProxyPassesWebSockets(t *testing.T) {
	front, _ := proxyServer(t)
	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET /juiceshop/socket.io/?EIO=4&transport=websocket HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	io.WriteString(conn, "hello\n")
	line, _ := br.ReadString('\n')
	if line != "echo:hello\n" {
		t.Errorf("echo %q", line)
	}
}

func TestProxyRefusesContinueCodes(t *testing.T) {
	front, _ := proxyServer(t)
	// the four spellings of 1.0's e2e, and two encodings
	for _, p := range []string{"rest/continue-code/apply", "REST/Continue-Code/apply", "rest/continue-code-findIt/apply", "rest/continue-code-fixIt/apply",
		"rest/continue-code%2Fapply", "rest/continue%2Dcode/apply"} {
		req, _ := http.NewRequest("PUT", front.URL+"/juiceshop/"+p+"/abc", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("PUT /juiceshop/%s/…: %d, want 403", p, resp.StatusCode)
		}
	}
	resp, _ := http.Get(front.URL + "/juiceshop/rest/continue-code")
	if resp.StatusCode != 200 {
		t.Errorf("reading a continue code must still work (the score board shows it): %d", resp.StatusCode)
	}
}
