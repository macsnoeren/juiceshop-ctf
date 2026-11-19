package agent

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	protocol "github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol/v1"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "state.json"))
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	st := newState()
	st.Registration = &protocol.Registration{ID: strings.Repeat("a", 32), Name: "Zoë", RegisteredAt: now}
	st.Solved["scoreBoardChallenge"] = now
	st.Cheat["scoreBoardChallenge"] = 0.5
	st.ContinueCodes = juiceshop.Codes{Challenges: "abc"}
	st.CommandsApplied = []string{"c-1"}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, st) {
		t.Errorf("round trip differs:\n%+v\n%+v", got, st)
	}
	info, _ := os.Stat(s.Path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
}

func TestMissingFileIsEmptyState(t *testing.T) {
	st, err := NewStore(filepath.Join(t.TempDir(), "state.json")).Load()
	if err != nil || st.Registration != nil || st.Solved == nil {
		t.Fatalf("Load() = %+v, %v", st, err)
	}
}

func TestLeftoverTempFileIsIgnored(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "state.json"))
	st := newState()
	st.Solved["a"] = time.Unix(1, 0).UTC()
	s.Save(st)
	// What a crash between write and rename leaves behind.
	os.WriteFile(filepath.Join(dir, ".state.json.tmp-deadbeef"), []byte("{half"), 0o600)
	got, err := s.Load()
	if err != nil || len(got.Solved) != 1 {
		t.Fatalf("Load() = %+v, %v", got, err)
	}
	if err := s.Save(got); err != nil {
		t.Fatalf("saving next to a leftover temp file: %v", err)
	}
}

func TestCorruptFileIsMovedAsideNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "state.json"))
	s.now = func() time.Time { return time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC) }
	os.WriteFile(s.Path, []byte("not json"), 0o600)

	st, err := s.Load()
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if st == nil || st.Registration != nil {
		t.Fatal("a corrupt file must give an empty state")
	}
	aside := filepath.Join(dir, "state.json.corrupt-20261118T090000Z")
	if b, err := os.ReadFile(aside); err != nil || string(b) != "not json" {
		t.Errorf("corrupt file not preserved at %s: %v", aside, err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Error("the corrupt file is still in place")
	}
}

func TestNewerVersionIsRefused(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "state.json"))
	os.WriteFile(s.Path, []byte(`{"version": 99}`), 0o600)
	if _, err := s.Load(); err == nil || errors.Is(err, ErrCorrupt) {
		t.Errorf("err = %v; a newer state file must be refused, not discarded", err)
	}
}
