package agent

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
)

// Observation is what one poll found, compared with the state.
type Observation struct {
	// NewSolves are challenges seen solved for the first time.
	NewSolves []string
	// Deficit are challenges the state knows as solved that Juice Shop does
	// not: the sign of a Juice Shop that restarted (§6.2).
	Deficit []string
}

// bootID identifies one run of Juice Shop. Juice Shop recreates its database
// on every start, so every challenge's createdAt is the moment it started
// (see juiceshop/testdata/README.md). The lowest id is used so the choice is
// stable.
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

// baseline returns the keys that do not count in this boot.
func baseline(st *State, boot string) map[string]bool {
	if st.BaselineBoot == "" || st.BaselineBoot != boot {
		return nil
	}
	m := make(map[string]bool, len(st.Baseline))
	for _, k := range st.Baseline {
		m[k] = true
	}
	return m
}

// Observe records first-seen solve times in st and reports what changed
// (§6.1). It does nothing without a registration: nothing solved before a
// student signed up belongs to anybody. Disabled challenges are skipped, and so
// is the baseline. A challenge that is no longer solved keeps its time; it
// shows up in Deficit instead.
func Observe(st *State, chs []juiceshop.Challenge, boot string, now time.Time) Observation {
	var o Observation
	if st.Registration == nil {
		return o
	}
	base := baseline(st, boot)
	for _, c := range chs {
		if c.Disabled() || base[c.Key] {
			continue
		}
		_, known := st.Solved[c.Key]
		switch {
		case c.Solved && !known:
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
	if st.Registration == nil {
		return nil
	}
	cp := *st
	cp.Solved = make(map[string]time.Time, len(st.Solved))
	for k, v := range st.Solved {
		cp.Solved[k] = v
	}
	cp.Registration = st.Registration
	o := Observe(&cp, chs, boot, time.Time{})
	return o.Deficit
}

// signature changes whenever Juice Shop's progress changes, including
// coding-challenge progress, which does not change `solved`. A changed
// signature is when the continue codes are fetched again.
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

// Category is progress in one category.
type Category struct {
	Name   string `json:"name"`
	Solved int    `json:"solved"`
	Total  int    `json:"total"`
}

// Summary counts solved and total challenges, overall and per category.
// Disabled challenges count in neither (§6.1).
func Summary(st *State, chs []juiceshop.Challenge) (solved, total int, cats []Category) {
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
		if _, ok := st.Solved[c.Key]; ok && st.Registration != nil {
			cats[i].Solved++
			solved++
		}
	}
	sort.Slice(cats, func(a, b int) bool { return cats[a].Name < cats[b].Name })
	return solved, total, cats
}
