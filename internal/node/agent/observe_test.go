package agent

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	protocol "github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol/v1"
)

func shopWith(solved ...string) []juiceshop.Challenge {
	set := map[string]bool{}
	for _, k := range solved {
		set[k] = true
	}
	out := make([]juiceshop.Challenge, len(catalogue))
	for i, c := range catalogue {
		c.Solved = set[c.Key]
		out[i] = c
	}
	return out
}

func registered() *State {
	st := newState()
	st.Registration = &protocol.Registration{ID: strings.Repeat("a", 32), Name: "Zoë"}
	return st
}

func TestObserve(t *testing.T) {
	t1 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	st := registered()

	o := Observe(st, shopWith("scoreBoardChallenge"), "b1", t1)
	if !reflect.DeepEqual(o.NewSolves, []string{"scoreBoardChallenge"}) || !st.Solved["scoreBoardChallenge"].Equal(t1) {
		t.Fatalf("new solve: %+v %v", o, st.Solved)
	}

	o = Observe(st, shopWith("scoreBoardChallenge", "loginAdminChallenge"), "b1", t2)
	if !reflect.DeepEqual(o.NewSolves, []string{"loginAdminChallenge"}) {
		t.Errorf("second poll: %+v", o)
	}
	if !st.Solved["scoreBoardChallenge"].Equal(t1) {
		t.Error("a solve seen again must keep its first time")
	}

	o = Observe(st, shopWith(), "b2", t2)
	if len(st.Solved) != 2 {
		t.Error("an un-solve must not remove a recorded solve")
	}
	if len(o.Deficit) != 2 {
		t.Errorf("deficit = %v, want both", o.Deficit)
	}
}

func TestObserveSkipsDisabledBaselineAndUnregistered(t *testing.T) {
	st := newState()
	if o := Observe(st, shopWith("scoreBoardChallenge"), "b1", time.Now()); len(o.NewSolves) != 0 || len(st.Solved) != 0 {
		t.Error("nothing may be recorded without a registration")
	}

	st = registered()
	Observe(st, shopWith("xxeFileDisclosureChallenge"), "b1", time.Now())
	if len(st.Solved) != 0 {
		t.Error("a disabled challenge must not be recorded")
	}

	st.Baseline, st.BaselineBoot = []string{"scoreBoardChallenge"}, "b1"
	Observe(st, shopWith("scoreBoardChallenge"), "b1", time.Now())
	if len(st.Solved) != 0 {
		t.Error("a baseline challenge must not count in its own boot")
	}
	Observe(st, shopWith("scoreBoardChallenge"), "b2", time.Now())
	if len(st.Solved) != 1 {
		t.Error("in a new boot the baseline no longer applies")
	}
}

func TestSummaryExcludesDisabled(t *testing.T) {
	st := registered()
	Observe(st, shopWith("scoreBoardChallenge", "xxeFileDisclosureChallenge"), "b1", time.Now())
	solved, total, cats := Summary(st, shopWith())
	if solved != 1 || total != 3 {
		t.Errorf("solved/total = %d/%d, want 1/3", solved, total)
	}
	for _, c := range cats {
		if c.Name == "XXE" {
			t.Error("a category with only disabled challenges must not appear")
		}
	}
}

func TestBootID(t *testing.T) {
	a := shopWith()
	for i := range a {
		a[i].CreatedAt = time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC)
	}
	// reversed order, same lowest id
	b := append([]juiceshop.Challenge(nil), a...)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	if bootID(a) != bootID(b) || bootID(a) == "" || bootID(nil) != "" {
		t.Errorf("bootID unstable: %q %q", bootID(a), bootID(b))
	}
}
