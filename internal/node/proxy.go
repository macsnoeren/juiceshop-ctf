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

// The reverse proxy to Juice Shop (VERSION-1-1.md §3.1), where 1.0 had
// nginx (docker/nginx/ctf-instance.conf, DESIGN.md §6.2):
//
//	/juiceshop/…  → 127.0.0.1:3000/…   the prefix stripped on the way in, and
//	                                   put back on any redirect that lacks it
//	/assets/…     → 127.0.0.1:3000/assets/…   the ONE absolute path Juice
//	                                   Shop's frontend uses (translations)
//
// WebSocket upgrades pass: httputil.ReverseProxy switches protocols itself,
// and socket.io needs it for the "challenge solved" pop-ups.

// continueApply matches Juice Shop's three continue-code apply routes, case
// insensitively, as Express matches routes.
//
// >>> NO CONTINUE CODES FROM STUDENTS. Juice Shop's frontend PUTs the
// progress kept in its continueCode cookies here whenever the server starts:
// after Forget the old progress would come straight back, and a code pasted
// from a classmate's shop is all of their points. ctf-node restores directly
// on 127.0.0.1:3000, so nothing legitimate comes through here.
var continueApply = regexp.MustCompile(`(?i)^/juiceshop/rest/continue-code[^/]*/apply(/|$)`)

// Refused reports whether a request path is a continue-code apply. Both the
// decoded and the raw path are checked, so %-encoding cannot get round it.
func Refused(u *url.URL) bool {
	if continueApply.MatchString(u.Path) {
		return true
	}
	raw, err := url.PathUnescape(u.EscapedPath())
	return err == nil && continueApply.MatchString(raw) || continueApply.MatchString(u.EscapedPath())
}

// NewProxy returns the handler for /juiceshop/ and /assets/.
func NewProxy(target *url.URL, log *slog.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host // Juice Shop builds its links from it
			if p, ok := strings.CutPrefix(r.In.URL.Path, "/juiceshop"); ok {
				r.Out.URL.Path = singleSlash(target.Path + p)
				r.Out.URL.RawPath = ""
				if raw, ok := strings.CutPrefix(r.In.URL.RawPath, "/juiceshop"); ok && raw != "" {
					r.Out.URL.RawPath = singleSlash(target.Path + raw)
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
		if Refused(r.URL) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "applying a continue code is switched off in this CTF: your progress is restored for you"})
			return
		}
		rp.ServeHTTP(w, r)
	})
}

func singleSlash(p string) string {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if p == "" {
		return "/"
	}
	return p
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
// score, which is shown to the operator and never scores, first value only.
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
				if _, seen := n.st.Cheat[hook.Solution.Challenge]; !seen {
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
