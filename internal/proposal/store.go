package proposal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Automaat/zakwas/internal/engine"
)

// MaxAge is how long a proposal can be approved after it was made.
const MaxAge = 7 * 24 * time.Hour

// Proposal is a saved edit. Only the operations and the base hashes decide
// what approve does: it computes the diff, plan and tier again from them, so
// the rest is what the proposer saw.
type Proposal struct {
	FormatVersion string              `json:"format_version"`
	ID            string              `json:"id"`
	Created       time.Time           `json:"created"`
	Config        string              `json:"config"`
	Summary       string              `json:"summary"`
	Ops           []Op                `json:"ops"`
	Base          map[string]string   `json:"base"`
	Tier          string              `json:"tier"`
	Reasons       []string            `json:"reasons,omitempty"`
	Modules       []string            `json:"modules"`
	Diff          string              `json:"diff"`
	Plan          []engine.JSONModule `json:"plan"`
}

// Expired reports whether the proposal is too old to approve.
func (p *Proposal) Expired(now time.Time) bool { return now.Sub(p.Created) > MaxAge }

// Expires is when the proposal stops being approvable.
func (p *Proposal) Expires() time.Time { return p.Created.Add(MaxAge) }

// Hash is the digest stored in Base for a file's contents.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Dir is where proposals are kept.
func Dir(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "proposals")
}

// Store keeps one JSON file per proposal.
type Store struct{ Dir string }

var idPattern = regexp.MustCompile(`^[0-9a-f]{4,16}$`)

// NewID makes a short random identifier.
func NewID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s Store) path(id string) string { return filepath.Join(s.Dir, id+".json") }

// Save writes the proposal atomically, readable by its owner only.
func (s Store) Save(p *Proposal) error {
	if !idPattern.MatchString(p.ID) {
		return fmt.Errorf("invalid proposal id %q", p.ID)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.path(p.ID)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Load reads a proposal by id or by a prefix of it that names only one.
func (s Store) Load(id string) (*Proposal, error) {
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("%q is not a proposal id; list them with `zakwas proposals`", id)
	}
	if p, err := s.read(s.path(id)); err == nil {
		return p, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var match []*Proposal
	for _, p := range all {
		if strings.HasPrefix(p.ID, id) {
			match = append(match, p)
		}
	}
	switch len(match) {
	case 0:
		return nil, fmt.Errorf("no proposal %s; list them with `zakwas proposals`", id)
	case 1:
		return match[0], nil
	}
	return nil, fmt.Errorf("%s matches %d proposals; use more of the id", id, len(match))
}

func (s Store) read(path string) (*Proposal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Proposal
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if p.FormatVersion != engine.FormatVersion {
		return nil, fmt.Errorf("%s: format_version %q, this zakwas reads %q", path, p.FormatVersion, engine.FormatVersion)
	}
	if p.ID+".json" != filepath.Base(path) {
		return nil, fmt.Errorf("%s: holds proposal %q", path, p.ID)
	}
	return &p, nil
}

// List returns the readable proposals, oldest first. A file it can't read is
// skipped, so one bad proposal doesn't hide the others.
func (s Store) List() ([]*Proposal, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Proposal
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || !idPattern.MatchString(strings.TrimSuffix(e.Name(), ".json")) {
			continue
		}
		if p, err := s.read(filepath.Join(s.Dir, e.Name())); err == nil {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b *Proposal) int {
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// Delete removes a proposal.
func (s Store) Delete(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid proposal id %q", id)
	}
	err := os.Remove(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
