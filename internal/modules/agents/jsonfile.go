package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Automaat/zakwas/internal/fsutil"
)

// object is a JSON object that keeps its key order, so editing one field of
// a settings file the user also keeps in a dotfiles repo doesn't reshuffle
// it.
type object struct {
	keys   []string
	values map[string]json.RawMessage
}

func (o *object) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return errors.New("not a JSON object")
	}
	o.keys, o.values = nil, map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if _, dup := o.values[key]; !dup {
			o.keys = append(o.keys, key)
		}
		o.values[key] = raw
	}
	_, err = dec.Token()
	return err
}

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(o.values[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (o *object) get(key string) (json.RawMessage, bool) {
	v, ok := o.values[key]
	return v, ok
}

func (o *object) set(key string, v any) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	if o.values == nil {
		o.values = map[string]json.RawMessage{}
	}
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = raw
	return nil
}

func (o *object) delete(key string) {
	delete(o.values, key)
	for i, name := range o.keys {
		if name == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			return
		}
	}
}

// marshal skips json.Marshal's HTML escaping, which would rewrite "&&" in
// untouched fields such as hook commands.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// readObject reads a JSON object file; a missing file is an empty object.
func readObject(path string) (object, bool, error) {
	data, err := readIfExists(path)
	if err != nil || data == nil {
		return object{}, false, err
	}
	o, err := parseObject(path, data)
	return o, err == nil, err
}

// readIfExists reads path, following symlinks; a missing file is nil.
func readIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func parseObject(path string, data []byte) (object, error) {
	var o object
	if err := json.Unmarshal(data, &o); err != nil {
		return object{}, fmt.Errorf("%s: %w", path, err)
	}
	return o, nil
}

// errChanged reports that another program wrote the file while zakwas was
// editing it.
var errChanged = errors.New("changed while zakwas edited it")

// beforeReplace runs just before zakwas checks a file it edits is
// unchanged and replaces it; tests use it to write in between.
var beforeReplace = func(string) {}

// indentObject renders o as a settings file: two-space indent, final
// newline.
func indentObject(o object) ([]byte, error) {
	compact, err := marshal(o)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// writeObject replaces path atomically, writing through a symlink (a
// dotfiles link) rather than over it and keeping the file's mode.
func writeObject(path string, o object) error {
	return writeObjectIf(path, o, nil)
}

// writeObjectIf is writeObject that returns errChanged instead when the
// file no longer holds read, the bytes o was parsed from, so a change the
// agent made meanwhile isn't lost. A nil read skips the check.
func writeObjectIf(path string, o object, read []byte) error {
	out, err := indentObject(o)
	if err != nil {
		return err
	}
	return writeBytesIf(path, out, read)
}

// writeBytesIf replaces path with out, as writeObjectIf does for an object.
func writeBytesIf(path string, out, read []byte) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o200 == 0 {
		return fmt.Errorf("%s is read-only; if zakwas installs it from your repo, make the change there", path)
	}
	existing := read
	if existing == nil {
		existing, _ = os.ReadFile(path)
	}
	out = withLineEnding(out, existing)
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".zakwas-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if read != nil {
		beforeReplace(path)
		current, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, read) {
			return errChanged
		}
	}
	return fsutil.Rename(tmp.Name(), path)
}

// withLineEnding converts rendered LF text to the line ending of the file it
// replaces, so an edit on Windows doesn't turn a CRLF file into a mixed or
// LF one. A new or LF file keeps LF.
func withLineEnding(out, existing []byte) []byte {
	if lineEnding(existing) != "\r\n" {
		return out
	}
	return bytes.ReplaceAll(bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
}

// createObject writes o to path only if nothing is there yet: the hard
// link fails instead of replacing a file the agent created meanwhile, which
// is reported as errChanged.
func createObject(path string, o object, perm fs.FileMode) error {
	out, err := indentObject(o)
	if err != nil {
		return err
	}
	return createBytes(path, out, perm)
}

// createBytes is createObject for rendered content.
func createBytes(path string, out []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".zakwas-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	beforeReplace(path)
	err = os.Link(tmp.Name(), path)
	if errors.Is(err, fs.ErrExist) {
		if info, lerr := os.Lstat(path); lerr == nil && info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink to a missing file", path)
		}
		return errChanged
	}
	return err
}
