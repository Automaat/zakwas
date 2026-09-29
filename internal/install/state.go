package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// State remembers the hash of every file zakwas wrote, so a later plan can tell
// "the repo changed" (safe to overwrite) from "someone edited the installed
// copy" (back it up first).
type State struct {
	path  string
	Files map[string]string `json:"files"`
}

// StatePath is where the state lives for a given home directory.
func StatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "files.json")
}

// LoadState reads the state file; a missing file is an empty state.
func LoadState(path string) (*State, error) {
	s := &State{path: path, Files: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Files == nil {
		s.Files = map[string]string{}
	}
	return s, nil
}

// Get returns the recorded hash for dst.
func (s *State) Get(dst string) (string, bool) {
	sum, ok := s.Files[dst]
	return sum, ok
}

// Record stores the hash for dst and persists the state.
func (s *State) Record(dst, sum string) error {
	s.Files[dst] = sum
	return s.save()
}

func (s *State) save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(s.path, append(data, '\n'), 0o644)
}

// Forget drops dst from the state and persists it.
func (s *State) Forget(dst string) error {
	delete(s.Files, dst)
	return s.save()
}

// Keys returns every recorded destination, sorted.
func (s *State) Keys() []string {
	keys := make([]string, 0, len(s.Files))
	for k := range s.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Sum hashes file content.
func Sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
