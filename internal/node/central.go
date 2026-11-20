// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// The three requests of protocol v2, and the loops
// that send them: the joiner empties the outbox, the reporter sends the
// snapshot within a couple of seconds of a change and every ReportInterval.

var errUnauthorized = errors.New("central server refused the token (401)")

func (n *Node) post(ctx context.Context, url, token string, body any) (int, []byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ctf-node/"+n.cfg.Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := n.httpc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, data, nil
}

func serverError(data []byte, status int) string {
	var e protocol.ErrorResponse
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return e.Error
	}
	return fmt.Sprintf("HTTP %d", status)
}

func (n *Node) nodeInfo(uid string) protocol.Node {
	n.mu.Lock()
	defer n.mu.Unlock()
	return protocol.Node{UID: uid, Version: n.cfg.Version, JuiceShop: n.jsVersion}
}

// sendJoin posts a join. A *JoinError is a refusal (the code or the game);
// any other error means the server could not be reached or did not answer
// as a central server, and the join should wait in the outbox.
func (n *Node) sendJoin(ctx context.Context, central, name, code, uid string, joinedAt time.Time) (*protocol.JoinResponse, error) {
	req := protocol.JoinRequest{Protocol: protocol.Version, Name: name, Code: code, JoinedAt: &joinedAt, Node: n.nodeInfo(uid)}
	status, data, err := n.post(ctx, central+"/api/v2/join", "", req)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusCreated:
		var jr protocol.JoinResponse
		if err := json.Unmarshal(data, &jr); err != nil || !protocol.ValidParticipantToken(jr.Token) {
			return nil, fmt.Errorf("the central server's answer to the join is not valid")
		}
		return &jr, nil
	case http.StatusForbidden:
		return nil, &JoinError{status, "Unknown join code. Check it with your teacher and try again."}
	case http.StatusConflict:
		return nil, &JoinError{status, "The central server says: " + serverError(data, status) + "."}
	case http.StatusBadRequest:
		return nil, &JoinError{status, "The central server refused this: " + serverError(data, status) + "."}
	case http.StatusTooManyRequests:
		return nil, &JoinError{status, "Too many attempts. Wait a minute and try again."}
	}
	return nil, fmt.Errorf("central server answered the join with %s", serverError(data, status))
}

func (n *Node) sendLeave(ctx context.Context, central, token, reason string) error {
	status, data, err := n.post(ctx, central+"/api/v2/leave", token, protocol.LeaveRequest{Protocol: protocol.Version, Reason: reason})
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusUnauthorized {
		return fmt.Errorf("leave answered %s", serverError(data, status))
	}
	return nil
}

// backoff is base doubling per failure up to max, with ±20 % jitter, so a
// room full of VMs does not retry in step after the central server returns.
func backoff(failures int, base, max time.Duration) time.Duration {
	d := base
	for i := 1; i < failures && d < max; i++ {
		d *= 2
	}
	d = min(d, max)
	return d + time.Duration((rand.Float64()*0.4-0.2)*float64(d))
}

// TryOutbox sends a queued join if it is due.
func (n *Node) TryOutbox(ctx context.Context) {
	n.mu.Lock()
	o, m := n.st.Outbox, n.st.Membership
	if o == nil || m == nil || n.now().Before(o.NextAt) {
		n.mu.Unlock()
		return
	}
	ob, uid, central := *o, m.NodeUID, m.CentralURL
	n.mu.Unlock()

	jctx, cancel := context.WithTimeout(ctx, n.cfg.JoinTimeout)
	jr, err := n.sendJoin(jctx, central, ob.Name, ob.Code, uid, ob.JoinedAt)
	cancel()
	var refused *JoinError
	n.mu.Lock()
	switch {
	case errors.As(err, &refused) && refused.Status != http.StatusTooManyRequests:
		n.st.Outbox = nil
		n.st.Membership.Status, n.st.Membership.Problem = StatusRefused, refused.Msg
		n.log.Warn("the queued join was refused; the page asks for the code again", "status", refused.Status)
	case err != nil:
		n.st.Outbox.Attempts++
		n.st.Outbox.NextAt = n.now().Add(backoff(n.st.Outbox.Attempts, n.cfg.BackoffBase, n.cfg.BackoffMax))
		n.st.Outbox.LastError = err.Error()
		n.mu.Unlock()
		n.save()
		return
	default:
		hash := n.st.Membership.ReturnCodeHash
		n.st.Membership = joinedMembership(jr, uid, ob.JoinedAt, central)
		n.st.Membership.ReturnCodeHash = hash
		n.st.Outbox = nil
		n.log.Info("the queued join was accepted", "participant", jr.Participant, "after_attempts", ob.Attempts+1)
	}
	n.mu.Unlock()
	n.save()
	n.db.Checkpoint() // the code is gone from the outbox: out of the WAL too
	n.TriggerReport()
}

// RunJoiner sends the outbox when it is due.
func (n *Node) RunJoiner(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.joinNow:
		}
		n.TryOutbox(ctx)
	}
}

// BuildReport is the report snapshot. ok is false when there is nothing to
// report to (not joined).
func (n *Node) BuildReport() (protocol.Report, string, string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.st.Membership
	if m == nil || m.Status != StatusJoined || m.Token == "" {
		return protocol.Report{}, "", "", false
	}
	r := protocol.Report{
		Protocol: protocol.Version, SentAt: n.now().UTC(), Boot: n.boot,
		Node:      protocol.Node{UID: m.NodeUID, Version: n.cfg.Version, JuiceShop: n.jsVersion},
		Reachable: n.reachable, RestoreFailed: n.st.RestoreFailed,
		Solves: []protocol.Solve{}, CommandsApplied: append([]string{}, n.st.CommandsApplied...),
	}
	keys := make([]string, 0, len(n.st.Solved))
	for k := range n.st.Solved {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := protocol.Solve{Key: k, SolvedAt: n.st.Solved[k]}
		if v, ok := n.st.Cheat[k]; ok {
			v := v
			s.Cheat = &v
		}
		r.Solves = append(r.Solves, s)
	}
	return r, m.CentralURL, m.Token, true
}

// Report sends one snapshot and handles the answer. It returns errNotJoined
// when there is nothing to send.
func (n *Node) Report(ctx context.Context) error {
	rep, central, token, ok := n.BuildReport()
	if !ok {
		return errNotJoined
	}
	n.reports.Add(1)
	status, data, err := n.post(ctx, central+"/api/v2/report", token, rep)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		n.mu.Lock()
		if m := n.st.Membership; m != nil && m.Token == token {
			m.Status, m.Token, m.ParticipantID = StatusRejected, "", 0
			m.Problem = "The central server no longer knows this VM's membership. Join again with a join code; your solves are kept."
		}
		n.mu.Unlock()
		n.save()
		return errUnauthorized
	default:
		return fmt.Errorf("central server answered the report with %s", serverError(data, status))
	}
	var out protocol.ReportResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("central server response: %w", err)
	}
	for _, c := range out.Commands {
		if !c.Known() {
			continue
		}
		n.mu.Lock()
		done := slices.Contains(n.st.CommandsApplied, c.ID)
		n.mu.Unlock()
		if done {
			continue
		}
		if c.Type == protocol.CmdForget {
			n.log.Info("the central server says forget (the operator removed this participant or cleared the game)")
			err := n.Forget(ctx, protocol.LeaveCommand)
			if err != nil && !errors.Is(err, ErrForgetNoRestart) {
				return err
			}
			return nil // nothing is left to report about
		}
	}
	n.mu.Lock()
	now := n.now().UTC()
	n.st.LastReportAt = &now
	if m := n.st.Membership; m != nil && m.Token == token {
		m.Game.State, m.Game.EndsAt, m.Game.StartsAt = out.Game.State, out.Game.EndsAt, out.Game.StartsAt
		if out.Standing != nil {
			n.st.Standing = &Standing{Rank: out.Standing.Rank, Of: out.Standing.Of, Score: out.Standing.Score, At: now}
		} else {
			n.st.Standing = nil
		}
	}
	n.mu.Unlock()
	n.save()
	return nil
}

var errNotJoined = errors.New("not joined")

// RunReporter sends a report after every change (debounced) and every
// ReportInterval. While the central server is failing it follows the
// back-off and ignores triggers: the next snapshot carries every change.
func (n *Node) RunReporter(ctx context.Context) {
	timer := time.NewTimer(n.cfg.ReportDebounce)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-n.reportNow:
			if failures > 0 {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(n.cfg.ReportDebounce):
			}
			select {
			case <-n.reportNow:
			default:
			}
		}
		err := n.Report(ctx)
		next := n.cfg.ReportInterval
		switch {
		case err == nil:
			if failures > 0 {
				n.log.Info("central server reachable again", "after_failures", failures)
			}
			failures = 0
		case errors.Is(err, errNotJoined):
			failures = 0
		case errors.Is(err, errUnauthorized):
			failures = 0
			n.log.Error("THE CENTRAL SERVER NO LONGER ACCEPTS THIS VM'S TOKEN: the page asks the student to join again")
		default:
			failures++
			next = backoff(failures, n.cfg.BackoffBase, n.cfg.BackoffMax)
			n.log.Warn("report failed", "err", err, "retry_in", next.Round(time.Second))
		}
		timer.Reset(next)
	}
}
