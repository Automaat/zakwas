// Package system handles one-off machine setup: directories, Touch ID for
// sudo, and the SSH key.
package system

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

// DefaultPAMFile survives macOS updates, unlike /etc/pam.d/sudo.
const DefaultPAMFile = "/etc/pam.d/sudo_local"

const pamTouchID = "auth       sufficient     pam_tid.so"

const defaultDirMode fs.FileMode = 0o755

type Module struct {
	System  config.System
	Paths   config.Paths
	Runner  runner.Runner
	PAMFile string
}

func (m *Module) Name() string { return "system" }

func (m *Module) Plan(_ context.Context) ([]engine.Change, error) {
	var planners []func() (*engine.Change, error)
	for _, d := range m.System.Dirs {
		planners = append(planners, func() (*engine.Change, error) { return m.planDir(d) })
	}
	if m.System.SudoTouchID {
		planners = append(planners, m.planTouchID)
	}
	if k := m.System.SSHKey; k != nil {
		planners = append(planners, func() (*engine.Change, error) { return m.planSSHKey(*k) })
	}

	var changes []engine.Change
	for _, plan := range planners {
		c, err := plan()
		if err != nil {
			return nil, err
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	return changes, nil
}

// planDir creates missing dirs. The mode of an existing dir is enforced only
// when set explicitly, so dirs like ~/Documents keep whatever macOS set.
func (m *Module) planDir(d config.Dir) (*engine.Change, error) {
	path := m.Paths.Dst(d.Path)
	mode := d.Mode
	if mode == 0 {
		mode = defaultDirMode
	}
	target := m.Paths.Pretty(path)
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &engine.Change{
			Action: engine.Create, Target: target, Detail: fmt.Sprintf("dir %o", mode),
			Apply: func(context.Context) error {
				if err := os.MkdirAll(path, mode); err != nil {
					return err
				}
				return os.Chmod(path, mode)
			},
		}, nil
	case err != nil:
		return nil, err
	case !info.IsDir():
		return nil, fmt.Errorf("%s exists and is not a directory", path)
	case d.Mode != 0 && info.Mode().Perm() != d.Mode:
		return &engine.Change{
			Action: engine.Update, Target: target, Detail: "mode", From: fmt.Sprintf("%o", info.Mode().Perm()), To: fmt.Sprintf("%o", d.Mode),
			Apply: func(context.Context) error { return os.Chmod(path, d.Mode) },
		}, nil
	}
	return nil, nil
}

func (m *Module) pamFile() string {
	if m.PAMFile != "" {
		return m.PAMFile
	}
	return DefaultPAMFile
}

func (m *Module) planTouchID() (*engine.Change, error) {
	file := m.pamFile()
	if info, err := os.Lstat(file); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return m.replacePAMSymlink(file), nil
	}
	enabled, err := HasTouchID(file)
	if err != nil {
		return nil, err
	}
	if enabled {
		return nil, nil
	}
	cmd := runner.Cmd{Name: "sudo", Args: []string{"tee", "-a", file}, Stdin: pamTouchID + "\n"}
	return &engine.Change{
		Action: engine.Update, Target: file, Detail: "enable Touch ID for sudo",
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) },
	}, nil
}

// replacePAMSymlink swaps a nix-darwin symlink (into /etc/static, i.e. the
// Nix store) for a real file; writing through it breaks once Nix is gone.
func (m *Module) replacePAMSymlink(file string) *engine.Change {
	rm := runner.Cmd{Name: "sudo", Args: []string{"rm", "-f", file}}
	write := runner.Cmd{Name: "sudo", Args: []string{"tee", file}, Stdin: pamTouchID + "\n"}
	return &engine.Change{
		Action: engine.Update, Target: file, Detail: "replace symlink with real file, enable Touch ID for sudo",
		Apply: func(ctx context.Context) error {
			if err := runner.Check(ctx, m.Runner, rm); err != nil {
				return err
			}
			return runner.Check(ctx, m.Runner, write)
		},
	}
}

// HasTouchID reports whether an active (uncommented) pam_tid line exists.
func HasTouchID(file string) (bool, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && strings.Contains(line, "pam_tid.so") {
			return true, nil
		}
	}
	return false, nil
}

func (m *Module) planSSHKey(k config.SSHKey) (*engine.Change, error) {
	path := m.Paths.Dst(k.Path)
	_, err := os.Stat(path)
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	cmd := runner.Cmd{Name: "ssh-keygen", Args: []string{"-q", "-t", "ed25519", "-C", k.Comment, "-f", path, "-N", ""}}
	return &engine.Change{
		Action: engine.Create, Target: m.Paths.Pretty(path), Detail: "ssh-keygen ed25519",
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) },
	}, nil
}
