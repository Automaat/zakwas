package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
		key, err := json.Marshal(k)
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
	raw, err := json.Marshal(v)
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

// readObject reads a JSON object file; a missing file is an empty object.
func readObject(path string) (object, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return object{}, false, nil
	}
	if err != nil {
		return object{}, false, err
	}
	var o object
	if err := json.Unmarshal(data, &o); err != nil {
		return object{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return o, true, nil
}

// writeObject replaces path atomically, writing through a symlink (a
// dotfiles link) rather than over it and keeping the file's mode.
func writeObject(path string, o object) error {
	compact, err := json.Marshal(o)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
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
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".zakwas-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(out.Bytes()); err != nil {
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
	return os.Rename(tmp.Name(), path)
}
