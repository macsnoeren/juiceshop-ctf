// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package node

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
)

// The reverse proxy to Juice Shop:
//
//	/juiceshop/…  → 127.0.0.1:3000/…   the prefix stripped on the way in, and
//	                                   put back on any redirect that lacks it
//	/assets/…     → 127.0.0.1:3000/assets/…   the ONE absolute path Juice
//	                                   Shop's frontend uses (translations)
//
// WebSocket upgrades pass: httputil.ReverseProxy switches protocols itself,
// and socket.io needs it for the "challenge solved" pop-ups.
//
// >>> THE PATH THAT IS CHECKED MUST BE THE PATH THAT IS SENT. A path with an
// empty segment (//) or a dot segment (. or ..) is REFUSED, not repaired:
// once "/juiceshop//rest/continue-code/apply" was checked as it came in
// (no match: the regexp wants one slash) and then sent with its slashes
// collapsed (a match in Juice Shop). Juice Shop's frontend never sends such a
// path, so refusing it costs nothing; repairing it is how the two got apart.

// continueApply matches Juice Shop's three continue-code apply routes, case
// insensitively, as Express matches routes, on the path JUICE SHOP sees
// (without /juiceshop).
//
// >>> NO CONTINUE CODES FROM STUDENTS. Juice Shop's frontend PUTs the
// progress kept in its continueCode cookies here whenever the server starts:
// after Forget the old progress would come straight back, and a code is all
// the points it encodes, a classmate's or one made by hand (the encoding's
// salt is in Juice Shop's source). ctf-node restores directly on
// 127.0.0.1:3000, so nothing legitimate comes through here.
var continueApply = regexp.MustCompile(`(?i)^/rest/continue-code[^/]*/apply(/|$)`)

// Malformed reports whether a request path has an empty or a dot segment,
// as typed or after %-decoding: the proxy does not pass those on.
func Malformed(u *url.URL) bool {
	paths := []string{u.Path, u.EscapedPath()}
	if raw, err := url.PathUnescape(u.EscapedPath()); err == nil {
		paths = append(paths, raw)
	} else {
		return true
	}
	for _, p := range paths {
		if strings.Contains(p, "//") {
			return true
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == "." || seg == ".." {
				return true
			}
		}
	}
	return false
}

// shopPath is p as Juice Shop receives it: /juiceshop taken off.
func shopPath(p string) string {
	if rest, ok := strings.CutPrefix(p, "/juiceshop"); ok {
		return rest
	}
	return p
}

// Refused reports whether a request path is a continue-code apply. The
// decoded, the raw and the raw-decoded path are all checked, each as Juice
// Shop would receive it, so %-encoding cannot get round it either.
func Refused(u *url.URL) bool {
	paths := []string{u.Path, u.EscapedPath()}
	if raw, err := url.PathUnescape(u.EscapedPath()); err == nil {
		paths = append(paths, raw)
	}
	for _, p := range paths {
		if continueApply.MatchString(shopPath(p)) {
			return true
		}
	}
	return false
}

// NewProxy returns the handler for /juiceshop/ and /assets/.
func NewProxy(target *url.URL, log *slog.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host // Juice Shop builds its links from it
			// The prefix comes off, and nothing else changes: the handler
			// below has refused every path that could change on the way.
			if p, ok := strings.CutPrefix(r.In.URL.Path, "/juiceshop"); ok {
				r.Out.URL.Path = join(target.Path, p)
				r.Out.URL.RawPath = ""
				if raw, ok := strings.CutPrefix(r.In.URL.RawPath, "/juiceshop"); ok && raw != "" {
					r.Out.URL.RawPath = join(target.Path, raw)
				}
			}
			r.SetXForwarded()
		},
		ModifyResponse: func(resp *http.Response) error {
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", fixLocation(loc, target, resp.Request.Host))
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !errors.Is(err, http.ErrAbortHandler) {
				log.Debug("juice shop not answering", "err", err)
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Juice Shop is starting</title>`+
				`<p>The Juice Shop is (re)starting. Try again in a few seconds. <a href="/ctf/">Your CTF page</a></p>`)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Malformed(r.URL) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "this CTF does not pass on a path with // or a . or .. segment"})
			return
		}
		if Refused(r.URL) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "applying a continue code is switched off in this CTF: your progress is restored for you"})
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// join puts the target's own path (none for Juice Shop) before p.
func join(base, p string) string {
	if base == "" || base == "/" {
		if p == "" {
			return "/"
		}
		return p
	}
	return strings.TrimSuffix(base, "/") + p
}

// fixLocation puts /juiceshop back on a redirect Juice Shop sent: a path
// without it (nginx's proxy_redirect ~^/(?!juiceshop/)(.*)$ /juiceshop/$1), or
// an absolute URL pointing at the backend or at this VM.
func fixLocation(loc string, target *url.URL, host string) string {
	// An absolute URL to the backend, or to the address the student used
	// (Juice Shop builds those from the Host header it is sent), becomes a
	// path, so it gets the prefix like any other.
	for _, h := range []string{target.Host, host} {
		for _, scheme := range []string{"http://", "https://"} {
			if h != "" && strings.HasPrefix(loc, scheme+h+"/") {
				loc = strings.TrimPrefix(loc, scheme+h)
			}
		}
	}
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") &&
		loc != "/juiceshop" && !strings.HasPrefix(loc, "/juiceshop/") && !strings.HasPrefix(loc, "/assets/") {
		return "/juiceshop" + loc
	}
	return loc
}

const maxWebhookBody = 64 << 10

// WebhookHandler serves POST /webhook for Juice Shop's SOLUTIONS_WEBHOOK, on
// 127.0.0.1 only: Juice Shop shares ctf-node's network namespace, nothing
// outside the container can reach it.
//
// The body is NOT believed: code in Juice Shop can post anything here. A
// webhook only means "poll now"; the one thing taken from it is the cheat
// score, which is shown to the operator and never scores. The HIGHEST value
// is kept: with the first, code in Juice Shop could post a 0 for every
// challenge beforehand and hide every real score.
func (n *Node) WebhookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var hook struct {
			Solution struct {
				Challenge  string   `json:"challenge"`
				CheatScore *float64 `json:"cheatScore"`
			} `json:"solution"`
		}
		if json.Unmarshal(body, &hook) == nil && hook.Solution.CheatScore != nil && hook.Solution.Challenge != "" {
			c := *hook.Solution.CheatScore
			n.mu.Lock()
			if n.st.Membership != nil && len(n.st.Cheat) < 1000 && c >= 0 && c <= 1 {
				if old, seen := n.st.Cheat[hook.Solution.Challenge]; !seen || c > old {
					n.st.Cheat[hook.Solution.Challenge] = c
				}
			}
			n.mu.Unlock()
		}
		n.TriggerPoll()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
