package agent

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
	"strings"
	"time"

	protocol "github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol/v1"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/version"
)

// keepCommands is how many applied command ids the agent remembers and
// repeats in its reports. The central server re-sends a command until it is
// confirmed, so the id only has to outlive that round trip.
const keepCommands = 20

var errUnauthorized = errors.New("central server refused the token (401)")

// BuildReport is the snapshot of §7.2.
func (a *Agent) BuildReport() protocol.Report {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.st
	r := protocol.Report{
		Protocol:     protocol.Version,
		Instance:     a.cfg.InstanceID,
		SentAt:       a.now().UTC(),
		AgentVersion: version.Version,
		JuiceShop: protocol.JuiceShop{
			Version:       a.jsVersion,
			Reachable:     a.reachable,
			RestoreFailed: st.RestoreFailed,
		},
		Challenges:      []protocol.Challenge{},
		CommandsApplied: append([]string(nil), st.CommandsApplied...),
	}
	if st.Registration != nil {
		reg := *st.Registration
		r.Registration = &reg
	}
	cat := slices.Clone(a.catalogue)
	sort.Slice(cat, func(i, j int) bool { return cat[i].ID < cat[j].ID })
	for _, c := range cat {
		pc := protocol.Challenge{
			Key: c.Key, Name: c.Name, Category: c.Category,
			Difficulty: c.Difficulty, Disabled: c.Disabled(),
		}
		if t, ok := st.Solved[c.Key]; ok && st.Registration != nil {
			t := t
			pc.SolvedAt = &t
			if v, ok := st.Cheat[c.Key]; ok {
				v := v
				pc.Cheat = &v
			}
		}
		r.Challenges = append(r.Challenges, pc)
	}
	return r
}

// Report sends one snapshot and handles the response.
func (a *Agent) Report(ctx context.Context) error {
	a.reports.Add(1)
	rep := a.BuildReport()
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	url := strings.TrimRight(a.cfg.CentralURL, "/") + "/api/v1/report"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.CentralToken)
	resp, err := a.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errUnauthorized
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("central server answered HTTP %d: %.200s", resp.StatusCode, data)
	}
	var out protocol.Response
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("central server response: %w", err)
	}
	a.handleResponse(out)
	return nil
}

func (a *Agent) handleResponse(out protocol.Response) {
	applied := 0
	for _, c := range out.Commands {
		if !c.Known() {
			continue
		}
		a.mu.Lock()
		done := slices.Contains(a.st.CommandsApplied, c.ID)
		a.mu.Unlock()
		if done {
			continue
		}
		switch c.Type {
		case protocol.CmdResetRegistration:
			a.resetRegistration()
		}
		a.mu.Lock()
		a.st.CommandsApplied = append(a.st.CommandsApplied, c.ID)
		if n := len(a.st.CommandsApplied); n > keepCommands {
			a.st.CommandsApplied = a.st.CommandsApplied[n-keepCommands:]
		}
		a.mu.Unlock()
		applied++
	}
	a.mu.Lock()
	now := a.now().UTC()
	a.st.LastReportAt = &now
	if a.st.Registration != nil {
		a.st.Standing = out.Standing
	} else {
		a.st.Standing = nil
	}
	a.mu.Unlock()
	a.save()
	if applied > 0 {
		a.TriggerReport() // confirm it soon rather than in a minute
	}
}

// backoff is base doubling per failure up to max (2 s and 5 min by default),
// with ±20 % jitter, so a room full of agents does not retry in step after
// the central server comes back.
func backoff(failures int, base, max time.Duration) time.Duration {
	d := base
	for i := 1; i < failures && d < max; i++ {
		d *= 2
	}
	d = min(d, max)
	jitter := time.Duration((rand.Float64()*0.4 - 0.2) * float64(d))
	return d + jitter
}

// RunReporter sends a report after every change (debounced) and every
// ReportInterval. While the central server is failing it follows the backoff
// and ignores triggers: the next snapshot carries every change anyway.
func (a *Agent) RunReporter(ctx context.Context) {
	timer := time.NewTimer(a.cfg.ReportDebounce)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-a.reportNow:
			if failures > 0 {
				continue
			}
			select { // gather what else changes in the next moment
			case <-ctx.Done():
				return
			case <-time.After(a.cfg.ReportDebounce):
			}
			select {
			case <-a.reportNow:
			default:
			}
		}
		err := a.Report(ctx)
		next := a.cfg.ReportInterval
		if err != nil {
			failures++
			next = backoff(failures, a.cfg.BackoffBase, a.cfg.BackoffMax)
			a.logReportError(err, next)
		} else {
			if failures > 0 {
				a.log.Info("central server reachable again", "after_failures", failures)
			}
			failures = 0
		}
		timer.Reset(next)
	}
}

func (a *Agent) logReportError(err error, next time.Duration) {
	if errors.Is(err, errUnauthorized) {
		// Logged at most once an hour: it does not fix itself, and a line a
		// minute would bury everything else in the node's log.
		a.mu.Lock()
		due := a.lastAuthLog.IsZero() || a.now().Sub(a.lastAuthLog) >= time.Hour
		if due {
			a.lastAuthLog = a.now()
		}
		a.mu.Unlock()
		if due {
			a.log.Error("CENTRAL SERVER REFUSES THIS INSTANCE'S TOKEN: check CTF_CENTRAL_TOKEN and CTF_INSTANCE_ID against /admin/ (logged once an hour)")
		}
		return
	}
	a.log.Warn("report failed", "err", err, "retry_in", next.Round(time.Second))
}
