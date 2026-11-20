// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package juiceshop is ctf-node's client for the few Juice Shop endpoints it
// needs. None of them requires a login: they are what
// Juice Shop's own score board and its "restore progress" button use.
//
// The shapes here were captured from the pinned version; see testdata/README.md
// before changing anything, and capture again when Juice Shop is upgraded.
package juiceshop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxBody caps every response read. /api/Challenges is about 70 KB.
const maxBody = 4 << 20

// ErrInvalidCode is Juice Shop refusing a continue code (404).
var ErrInvalidCode = errors.New("juice shop refused the continue code")

// Challenge is one entry of GET /api/Challenges, reduced to what the agent uses.
type Challenge struct {
	ID                    int       `json:"id"`
	Key                   string    `json:"key"`
	Name                  string    `json:"name"`
	Category              string    `json:"category"`
	Difficulty            int       `json:"difficulty"`
	Solved                bool      `json:"solved"`
	DisabledEnv           *string   `json:"disabledEnv"`
	CodingChallengeStatus int       `json:"codingChallengeStatus"`
	CreatedAt             time.Time `json:"createdAt"`
}

// Disabled reports whether Juice Shop disables this challenge in a container.
// Every CTF instance runs in Docker, so a challenge marked for Docker can never
// be solved and must count neither in the score nor in "x of y".
func (c Challenge) Disabled() bool {
	return c.DisabledEnv != nil && strings.Contains(*c.DisabledEnv, "Docker")
}

// Codes are the three continue codes. An empty string means Juice Shop had
// nothing to encode, and is never applied.
type Codes struct {
	Challenges string `json:"challenges"`
	FindIt     string `json:"find_it"`
	FixIt      string `json:"fix_it"`
}

// Empty reports whether there is no code at all.
func (c Codes) Empty() bool { return c.Challenges == "" && c.FindIt == "" && c.FixIt == "" }

// Client talks to one Juice Shop.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for the Juice Shop at base, e.g. http://juiceshop:3000.
func New(base string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp.StatusCode == http.StatusNotFound && method == http.MethodPut {
		return ErrInvalidCode
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s %s: response is not the expected JSON: %w", method, path, err)
	}
	return nil
}

// Challenges returns the full catalogue with the solved flags.
func (c *Client) Challenges(ctx context.Context) ([]Challenge, error) {
	var r struct {
		Status string      `json:"status"`
		Data   []Challenge `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/Challenges", &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("GET /api/Challenges: status %q", r.Status)
	}
	return r.Data, nil
}

// Version returns Juice Shop's version, e.g. "20.3.0".
func (c *Client) Version(ctx context.Context) (string, error) {
	var r struct {
		Version string `json:"version"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/admin/application-version", &r); err != nil {
		return "", err
	}
	return r.Version, nil
}

var codePaths = [3]string{"/rest/continue-code", "/rest/continue-code-findIt", "/rest/continue-code-fixIt"}

// ContinueCodes fetches all three continue codes.
func (c *Client) ContinueCodes(ctx context.Context) (Codes, error) {
	var got [3]string
	for i, p := range codePaths {
		var r struct {
			ContinueCode string `json:"continueCode"`
		}
		if err := c.do(ctx, http.MethodGet, p, &r); err != nil {
			return Codes{}, err
		}
		got[i] = r.ContinueCode
	}
	return Codes{Challenges: got[0], FindIt: got[1], FixIt: got[2]}, nil
}

// ApplyContinueCodes restores progress. Empty codes are skipped. It applies
// every non-empty code even when an earlier one fails, and returns the first
// error, so a refused coding-challenge code does not cost the main progress.
func (c *Client) ApplyContinueCodes(ctx context.Context, codes Codes) error {
	var first error
	for i, code := range [3]string{codes.Challenges, codes.FindIt, codes.FixIt} {
		if code == "" {
			continue
		}
		err := c.do(ctx, http.MethodPut, codePaths[i]+"/apply/"+url.PathEscape(code), nil)
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}
