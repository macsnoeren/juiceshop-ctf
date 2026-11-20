// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package node

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
)

// The watcher decides what counts as a solve:
//
//   - Juice Shop's /api/Challenges is the only source of truth about solves;
//     the webhook only says "poll now" (and carries the cheat score);
//   - a solve is recorded the first time a poll sees it, and never moved;
//   - Juice Shop recreates its database on every start, so a restarted one
//     has lost the student's progress: the node applies the continue codes it
//     keeps, once per boot, and says so when that fails (restore_failed);
//   - whatever was solved before the student joined is the baseline and
//     never counts.

// bootID identifies one run of Juice Shop: every challenge's createdAt is the
// moment it started (juiceshop/testdata/README.md). The lowest id is used so
// the choice is stable.
func bootID(chs []juiceshop.Challenge) string {
	best := -1
	for i, c := range chs {
		if best < 0 || c.ID < chs[best].ID {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return chs[best].CreatedAt.UTC().Format(time.RFC3339Nano)
}

func baselineSet(st *State, boot string) map[string]bool {
	if st.BaselineBoot == "" || st.BaselineBoot != boot {
		return nil
	}
	m := make(map[string]bool, len(st.Baseline))
	for _, k := range st.Baseline {
		m[k] = true
	}
	return m
}

// Observation is what one poll found, compared with the state.
type Observation struct {
	NewSolves []string
	// Deficit are challenges the state knows as solved that Juice Shop
	// does not: the sign of a restarted Juice Shop.
	Deficit []string
}

// Observe records first-seen solve times in st (only while recording) and
// reports what changed. Disabled challenges and the baseline are skipped.
func Observe(st *State, chs []juiceshop.Challenge, boot string, now time.Time) Observation {
	var o Observation
	if st.Membership == nil {
		return o
	}
	rec := st.recording()
	base := baselineSet(st, boot)
	for _, c := range chs {
		if c.Disabled() || base[c.Key] {
			continue
		}
		_, known := st.Solved[c.Key]
		switch {
		case c.Solved && !known && rec:
			st.Solved[c.Key] = now.UTC()
			o.NewSolves = append(o.NewSolves, c.Key)
		case !c.Solved && known:
			o.Deficit = append(o.Deficit, c.Key)
		}
	}
	return o
}

// deficit is Observe's Deficit without changing anything.
func deficit(st *State, chs []juiceshop.Challenge, boot string) []string {
	if st.Membership == nil {
		return nil
	}
	base := baselineSet(st, boot)
	var out []string
	for _, c := range chs {
		if _, known := st.Solved[c.Key]; known && !c.Solved && !c.Disabled() && !base[c.Key] {
			out = append(out, c.Key)
		}
	}
	return out
}

// signature changes whenever Juice Shop's progress changes, coding-challenge
// progress included; it is when the continue codes are fetched again.
func signature(chs []juiceshop.Challenge) string {
	parts := make([]string, 0, len(chs))
	for _, c := range chs {
		if c.Solved || c.CodingChallengeStatus != 0 {
			parts = append(parts, c.Key+":"+strconv.FormatBool(c.Solved)+":"+strconv.Itoa(c.CodingChallengeStatus))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// takeBaseline records what Juice Shop has solved now as not the student's
// (called at the first join), leaving out what the node already knows as
// theirs (imported from 1.0).
func (n *Node) takeBaseline() {
	n.st.Baseline, n.st.BaselineBoot = nil, ""
	for _, c := range n.catalogue {
		if _, mine := n.st.Solved[c.Key]; c.Solved && !c.Disabled() && !mine {
			n.st.Baseline = append(n.st.Baseline, c.Key)
		}
	}
	if len(n.st.Baseline) > 0 {
		sort.Strings(n.st.Baseline)
		n.st.BaselineBoot = n.boot
	}
}

// Poll reads Juice Shop once, restores it if it lost progress, records new
// solves, and refreshes the continue codes when progress changed. It holds
// opMu throughout, so Forget and Restart never interleave with a restore.
func (n *Node) Poll(ctx context.Context) {
	n.opMu.Lock()
	defer n.opMu.Unlock()
	n.polls.Add(1)
	n.mu.Lock()
	n.lastPoll = n.now()
	n.mu.Unlock()

	chs, err := n.shop.Challenges(ctx)
	if err != nil {
		n.mu.Lock()
		was := n.reachable
		n.reachable = false
		n.mu.Unlock()
		if was {
			n.log.Warn("juice shop unreachable", "err", err)
			n.TriggerReport()
		}
		return
	}
	boot := bootID(chs)
	dirty := false

	n.mu.Lock()
	wasReachable := n.reachable
	n.reachable = true
	newBoot := boot != n.boot
	n.boot, n.catalogue = boot, chs
	st := n.st
	if st.BaselineBoot != "" && st.BaselineBoot != boot {
		st.Baseline, st.BaselineBoot = nil, ""
		dirty = true
	}
	missing := deficit(st, chs, boot)
	needRestore := len(missing) > 0 && !st.ContinueCodes.Empty() && n.restoreBoot != boot
	codes := st.ContinueCodes
	if needRestore {
		n.restoreBoot = boot
	}
	needVersion := newBoot || n.jsVersion == ""
	n.mu.Unlock()

	if needVersion {
		if v, err := n.shop.Version(ctx); err == nil {
			n.mu.Lock()
			n.jsVersion = v
			n.mu.Unlock()
		}
		if newBoot && wasReachable {
			n.log.Info("juice shop restarted", "boot", boot)
		}
	}

	if needRestore {
		n.log.Info("juice shop lost progress, applying the continue codes", "missing", len(missing))
		if err := n.shop.ApplyContinueCodes(ctx, codes); err != nil {
			n.log.Warn("applying continue codes failed", "err", err)
		}
		if again, err := n.shop.Challenges(ctx); err == nil {
			chs = again
			n.mu.Lock()
			n.catalogue = chs
			n.mu.Unlock()
		}
		n.mu.Lock()
		still := deficit(st, chs, boot)
		st.RestoreFailed = len(still) > 0
		n.mu.Unlock()
		if len(still) > 0 {
			n.log.Warn("juice shop progress NOT fully restored; the central score is unaffected", "missing", len(still))
		} else {
			n.log.Info("juice shop progress restored", "challenges", len(missing))
		}
		dirty = true
	}

	n.mu.Lock()
	obs := Observe(st, chs, boot, n.now())
	if len(obs.NewSolves) > 0 {
		dirty = true
	}
	if st.RestoreFailed && len(obs.Deficit) == 0 {
		st.RestoreFailed = false
		dirty = true
	}
	sig := signature(chs)
	fetchCodes := st.Membership != nil && sig != n.lastSig && (len(obs.Deficit) == 0 || n.restoreBoot == boot)
	n.mu.Unlock()
	for _, k := range obs.NewSolves {
		n.log.Info("challenge solved", "challenge", k)
	}

	if fetchCodes {
		if got, err := n.shop.ContinueCodes(ctx); err != nil {
			n.log.Warn("fetching continue codes failed", "err", err)
		} else {
			n.mu.Lock()
			if st.Membership != nil && got != st.ContinueCodes {
				st.ContinueCodes = got
				dirty = true
			}
			n.lastSig = sig
			n.mu.Unlock()
		}
	}

	if dirty {
		n.save()
	}
	if dirty || !wasReachable {
		n.TriggerReport()
	}
}

// RunPoller polls every PollInterval and on TriggerPoll, never more often
// than PollDebounce.
func (n *Node) RunPoller(ctx context.Context) {
	t := time.NewTicker(n.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.pollNow:
		}
		n.mu.Lock()
		wait := n.cfg.PollDebounce - n.now().Sub(n.lastPoll)
		n.mu.Unlock()
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		select { // triggers that arrived while waiting are this poll
		case <-n.pollNow:
		default:
		}
		n.Poll(ctx)
	}
}

// Category is progress in one category.
type Category struct {
	Name   string
	Solved int
	Total  int
}

// Summary counts solved and total challenges, overall and per category, and
// the local score by the scoring table — what the page shows, also without a
// central server. Disabled challenges count in neither.
func Summary(st *State, chs []juiceshop.Challenge, scoring map[int]int) (solved, total, score int, cats []Category) {
	idx := map[string]int{}
	for _, c := range chs {
		if c.Disabled() {
			continue
		}
		i, ok := idx[c.Category]
		if !ok {
			i = len(cats)
			idx[c.Category] = i
			cats = append(cats, Category{Name: c.Category})
		}
		cats[i].Total++
		total++
		if _, ok := st.Solved[c.Key]; ok && st.Membership != nil {
			cats[i].Solved++
			solved++
			score += scoring[c.Difficulty]
		}
	}
	sort.Slice(cats, func(a, b int) bool { return cats[a].Name < cats[b].Name })
	return solved, total, score, cats
}
