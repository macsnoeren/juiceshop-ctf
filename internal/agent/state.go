package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// StateVersion is the version of the state file this agent writes.
const StateVersion = 1

// State is everything the agent must remember across restarts (DESIGN.md §6.3).
// It lives in ONE file on the ctf-agent-state volume, and the agent is its
// only writer: ctf-status changes it only through the agent's API.
type State struct {
	Version      int                    `json:"version"`
	Registration *protocol.Registration `json:"registration"`
	// Solved is the first time the agent saw each challenge solved. A time is
	// never moved and a key is never removed, except by a reset.
	Solved map[string]time.Time `json:"solved"`
	// Cheat is Juice Shop's own cheat estimate per challenge, from the webhook.
	Cheat         map[string]float64 `json:"cheat"`
	ContinueCodes juiceshop.Codes    `json:"continue_codes"`
	// Baseline is what Juice Shop already had solved when the student
	// registered: left over from a previous student, or from before the door
	// existed. It never counts. It only applies while Juice Shop runs the boot
	// it was taken in; a restarted Juice Shop has nothing solved.
	Baseline        []string           `json:"baseline,omitempty"`
	BaselineBoot    string             `json:"baseline_boot,omitempty"`
	RestoreFailed   bool               `json:"restore_failed"`
	CommandsApplied []string           `json:"commands_applied"`
	Standing        *protocol.Standing `json:"standing,omitempty"`
	LastReportAt    *time.Time         `json:"last_report_at,omitempty"`
}

func newState() *State {
	return &State{
		Version: StateVersion,
		Solved:  map[string]time.Time{},
		Cheat:   map[string]float64{},
	}
}

func (s *State) fill() {
	if s.Solved == nil {
		s.Solved = map[string]time.Time{}
	}
	if s.Cheat == nil {
		s.Cheat = map[string]float64{}
	}
}

// clearRegistration forgets the student and everything that belongs to them.
func (s *State) clearRegistration() {
	s.Registration = nil
	s.Solved = map[string]time.Time{}
	s.Cheat = map[string]float64{}
	s.ContinueCodes = juiceshop.Codes{}
	s.Baseline, s.BaselineBoot = nil, ""
	s.RestoreFailed = false
	s.Standing = nil
}

// ErrCorrupt means the state file could not be read. Load has moved it aside
// and returned an empty state; the caller must log this loudly, because the
// instance has just forgotten its student.
var ErrCorrupt = errors.New("state file corrupt")

// Store reads and writes the state file.
type Store struct {
	Path string
	now  func() time.Time
}

// NewStore returns a store for path.
func NewStore(path string) Store { return Store{Path: path, now: time.Now} }

// Load reads the state. A missing file is an empty state. A file that cannot
// be parsed is renamed to state.json.corrupt-<time> (never overwritten, so it
// can be inspected) and an empty state is returned together with ErrCorrupt.
// A file written by a NEWER agent is refused, because silently downgrading it
// would lose whatever the newer version stored.
func (s Store) Load() (*State, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil || st.Version == 0 {
		aside := s.Path + ".corrupt-" + s.now().UTC().Format("20060102T150405Z")
		if rerr := os.Rename(s.Path, aside); rerr != nil {
			return nil, fmt.Errorf("%w, and moving it aside failed: %v", ErrCorrupt, rerr)
		}
		return newState(), fmt.Errorf("%w: moved to %s", ErrCorrupt, aside)
	}
	if st.Version > StateVersion {
		return nil, fmt.Errorf("state file has version %d, this agent understands %d", st.Version, StateVersion)
	}
	st.fill()
	return &st, nil
}

// Save writes the state atomically: a temporary file in the same directory,
// fsync, rename over the old one, fsync the directory. A crash leaves either
// the old file or the new one, never half of one. The file is 0600: it holds
// the continue codes, which are this student's progress.
func (s Store) Save(st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	tmp := filepath.Join(dir, "."+filepath.Base(s.Path)+".tmp-"+httpx.RandomHex(4))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
