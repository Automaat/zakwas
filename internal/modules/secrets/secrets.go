// Package secrets restores age identities from Bitwarden secure notes. Plan
// reads only local files, so plan and check never start bw or ask for a
// password; apply opens the vault once and only adds what is missing.
//
// Nothing here may print or return a secret: errors name the note and the
// public key, never a note's content, a key file's content or the session.
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/fsutil"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/runner"
)

const (
	bw           = "bw"
	identityHead = "AGE-SECRET-KEY-1"
	fileMode     = fs.FileMode(0o600)
	dirMode      = fs.FileMode(0o700)
	lockTimeout  = 30 * time.Second
)

type Module struct {
	Secrets     config.Secrets
	Paths       config.Paths
	Runner      runner.Runner
	Interactive bool
}

func (m *Module) Name() string { return "secrets" }

// keyFile is what one secrets.age entry has on this machine.
type keyFile struct {
	entry   config.AgeFile
	path    string
	exists  bool
	perm    fs.FileMode
	content []byte
	missing []config.AgeKey
}

func (m *Module) inspect(entry config.AgeFile) (keyFile, error) {
	f := keyFile{entry: entry, path: m.Paths.Dst(entry.Dst), missing: entry.Keys}
	if err := install.CheckParents(m.Paths, f.path); err != nil {
		return f, fmt.Errorf("secrets: %w", err)
	}
	info, err := os.Lstat(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("secrets: %s: %w", entry.Dst, err)
	}
	if !info.Mode().IsRegular() {
		return f, fmt.Errorf("secrets: %s is not a regular file", entry.Dst)
	}
	f.exists, f.perm = true, info.Mode().Perm()
	if f.content, err = os.ReadFile(f.path); err != nil {
		return f, fmt.Errorf("secrets: %s: %w", entry.Dst, err)
	}
	have := publicKeys(f.content)
	f.missing = slices.DeleteFunc(slices.Clone(entry.Keys), func(k config.AgeKey) bool {
		return slices.Contains(have, k.PublicKey)
	})
	return f, nil
}

func (m *Module) Plan(context.Context) ([]engine.Change, error) {
	var changes []engine.Change
	restore := false
	for _, entry := range m.Secrets.Age {
		f, err := m.inspect(entry)
		if err != nil {
			return nil, err
		}
		target := m.Paths.Pretty(f.path)
		switch {
		case len(f.missing) > 0:
			restore = true
			action := engine.Create
			if f.exists {
				action = engine.Update
			}
			changes = append(changes, engine.Change{Action: action, Target: target, Detail: "add from Bitwarden: " + noteNames(f.missing)})
		case f.perm&^fileMode != 0:
			changes = append(changes, engine.Change{
				Action: engine.Update, Target: target, Detail: "owner only",
				From: fmt.Sprintf("%03o", f.perm), To: fmt.Sprintf("%03o", fileMode),
				Apply: func(context.Context) error { return os.Chmod(f.path, fileMode) },
			})
		}
	}
	if restore {
		changes = append(changes, engine.Change{Action: engine.Run, Target: "restore age keys from Bitwarden", Streams: true, Apply: m.restore})
	}
	return changes, nil
}

// restore re-inspects first, since the plan may be older than the files.
func (m *Module) restore(ctx context.Context) (err error) {
	var pending []keyFile
	for _, entry := range m.Secrets.Age {
		f, err := m.inspect(entry)
		if err != nil {
			return err
		}
		if len(f.missing) > 0 {
			pending = append(pending, f)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if !m.Interactive {
		return errors.New("secrets: restoring age keys needs your master password; run zakwas apply yourself in a terminal")
	}
	if !m.Runner.Installed(bw) {
		return errors.New("secrets: the Bitwarden CLI (bw) isn't installed; install it and run zakwas apply again")
	}
	session, opened, err := m.open(ctx)
	if err != nil {
		return err
	}
	if opened {
		defer func() { err = errors.Join(err, m.lock(ctx, session)) }()
	}
	m.syncVault(ctx, session)
	for _, f := range pending {
		if err := m.restoreFile(ctx, f, session); err != nil {
			return err
		}
	}
	return nil
}

// syncVault ignores a failure: the local copy of the vault is still there,
// and every key is checked against its public key before it is written.
func (m *Module) syncVault(ctx context.Context, session []string) {
	_, _ = m.Runner.Run(ctx, runner.Cmd{Name: bw, Args: []string{"sync"}, Env: session})
}

// open returns the environment that reaches an open vault. opened is false
// when the vault was already unlocked, which leaves locking it to the user.
func (m *Module) open(ctx context.Context) (session []string, opened bool, err error) {
	out, err := m.Runner.Run(ctx, runner.Cmd{Name: bw, Args: []string{"status"}})
	if err != nil {
		return nil, false, fmt.Errorf("secrets: %w", err)
	}
	if out.ExitCode != 0 {
		return nil, false, fmt.Errorf("secrets: bw status: exit %d", out.ExitCode)
	}
	var state struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out.Stdout), &state); err != nil {
		return nil, false, errors.New("secrets: can't read the output of bw status")
	}
	verb := "unlock"
	switch state.Status {
	case "unlocked":
		return nil, false, nil
	case "locked":
	case "unauthenticated":
		verb = "login"
	default:
		return nil, false, fmt.Errorf("secrets: bw status reports %q", state.Status)
	}
	res, err := m.Runner.Run(ctx, runner.Cmd{Name: bw, Args: []string{verb, "--raw"}, Terminal: true})
	if err != nil {
		return nil, false, fmt.Errorf("secrets: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, false, fmt.Errorf("secrets: bw %s: exit %d", verb, res.ExitCode)
	}
	token := strings.TrimSpace(res.Stdout)
	if token == "" {
		return nil, false, fmt.Errorf("secrets: bw %s returned no session", verb)
	}
	return []string{"BW_SESSION=" + token}, true, nil
}

// lock outlives a cancelled run: stopping apply must not leave the vault open.
func (m *Module) lock(ctx context.Context, session []string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lockTimeout)
	defer cancel()
	res, err := m.Runner.Run(ctx, runner.Cmd{Name: bw, Args: []string{"lock"}, Env: session})
	if err == nil && res.ExitCode == 0 {
		return nil
	}
	return errors.New("secrets: couldn't lock the Bitwarden CLI; run bw lock")
}

// restoreFile reads every missing key before writing, so one wrong note
// leaves the whole file as it was. It builds on a fresh read of the file: the
// password prompt can last long enough for someone to add a key to it.
func (m *Module) restoreFile(ctx context.Context, f keyFile, session []string) error {
	identities := make(map[string]string, len(f.missing))
	for _, key := range f.missing {
		identity, err := m.readIdentity(ctx, key, session)
		if err != nil {
			return fmt.Errorf("%w; %s is unchanged", err, f.entry.Dst)
		}
		identities[key.PublicKey] = identity
	}
	fresh, err := m.inspect(f.entry)
	if err != nil {
		return err
	}
	if len(fresh.missing) == 0 {
		return nil
	}
	candidate := slices.Clone(fresh.content)
	if len(candidate) > 0 && candidate[len(candidate)-1] != '\n' {
		candidate = append(candidate, '\n')
	}
	for _, key := range fresh.missing {
		identity, ok := identities[key.PublicKey]
		if !ok {
			return fmt.Errorf("secrets: %s changed during the restore; it is unchanged, so run zakwas apply again", f.entry.Dst)
		}
		candidate = append(candidate, identity...)
		candidate = append(candidate, '\n')
	}
	have := publicKeys(candidate)
	for _, key := range f.entry.Keys {
		if !slices.Contains(have, key.PublicKey) {
			return fmt.Errorf("secrets: %s would still lack %s; it is unchanged", f.entry.Dst, key.PublicKey)
		}
	}
	return writePrivate(f.path, candidate)
}

// readIdentity keeps only the line that matches the public key, so anything
// else in the note stays in the vault. bw's stdout never reaches an error.
func (m *Module) readIdentity(ctx context.Context, key config.AgeKey, session []string) (string, error) {
	res, err := m.Runner.Run(ctx, runner.Cmd{Name: bw, Args: []string{"get", "notes", key.Bitwarden}, Env: session})
	if err != nil {
		return "", fmt.Errorf("secrets: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("secrets: can't read Bitwarden note %q: %s", key.Bitwarden, strings.TrimSpace(res.Stderr))
	}
	for line := range strings.Lines(res.Stdout) {
		line = strings.TrimSpace(line)
		if publicKey(line) == key.PublicKey {
			return line, nil
		}
	}
	return "", fmt.Errorf("secrets: Bitwarden note %q doesn't hold the key for %s", key.Bitwarden, key.PublicKey)
}

func writePrivate(path string, content []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	_, err = tmp.Write(content)
	if err == nil {
		err = tmp.Chmod(fileMode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("secrets: writing next to %s: %w", path, err)
	}
	if err = fsutil.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("secrets: replacing %s: %w", path, err)
	}
	return fsutil.SyncDir(dir)
}

func publicKeys(content []byte) []string {
	var keys []string
	for line := range strings.Lines(string(content)) {
		if key := publicKey(strings.TrimSpace(line)); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// publicKey is empty for a line that isn't an X25519 identity: comments and
// keys of other types stay in the file, unread. The parser's error is
// dropped because it can quote the line.
func publicKey(line string) string {
	if !strings.HasPrefix(line, identityHead) {
		return ""
	}
	identity, err := age.ParseX25519Identity(line)
	if err != nil {
		return ""
	}
	return identity.Recipient().String()
}

func noteNames(keys []config.AgeKey) string {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.Bitwarden
	}
	return strings.Join(names, ", ")
}
