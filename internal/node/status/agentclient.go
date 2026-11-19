package status

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/node/agent"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
)

// AgentClient is what the status page needs from ctf-agent's internal API.
type AgentClient interface {
	// Registration returns the current registration, or nil when there is none.
	Registration(ctx context.Context) (*protocol.Registration, error)
	Register(ctx context.Context, name string) (*protocol.Registration, error)
	State(ctx context.Context) (*agent.StateView, error)
}

// Errors of Register.
var ErrRegistered = errors.New("already registered")

// NameError is the agent refusing a name; the message is for the student.
type NameError struct{ Msg string }

func (e NameError) Error() string { return e.Msg }

// HTTPAgent calls the agent over HTTP with the internal token.
type HTTPAgent struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// NewHTTPAgent returns a client for the agent at base, e.g. http://ctf-agent:8082.
func NewHTTPAgent(base, token string) *HTTPAgent {
	return &HTTPAgent{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func (c *HTTPAgent) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		return resp.StatusCode, fmt.Errorf("agent %s %s: HTTP %d %s", method, path, resp.StatusCode, e.Error)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (c *HTTPAgent) Registration(ctx context.Context) (*protocol.Registration, error) {
	var reg protocol.Registration
	code, err := c.do(ctx, http.MethodGet, "/registration", nil, &reg)
	if code == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &reg, nil
}

func (c *HTTPAgent) Register(ctx context.Context, name string) (*protocol.Registration, error) {
	var reg protocol.Registration
	code, err := c.do(ctx, http.MethodPost, "/registration", map[string]string{"name": name}, &reg)
	switch {
	case code == http.StatusConflict:
		return nil, ErrRegistered
	case code == http.StatusBadRequest:
		msg := err.Error()
		if i := strings.LastIndex(msg, ": "); i >= 0 {
			msg = msg[i+2:]
		}
		return nil, NameError{Msg: msg}
	case err != nil:
		return nil, err
	}
	return &reg, nil
}

func (c *HTTPAgent) State(ctx context.Context) (*agent.StateView, error) {
	var v agent.StateView
	if _, err := c.do(ctx, http.MethodGet, "/state", nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
