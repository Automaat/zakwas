package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

// ErrLinksDenied is what a link that Windows refuses to create becomes. A
// file link needs a symbolic link, a directory link falls back to a
// junction, and there is no copy to fall back to: copies drift.
var ErrLinksDenied = errors.New("cannot create symbolic links on Windows: turn on Developer Mode, run zakwas as administrator, or grant your account SeCreateSymbolicLinkPrivilege (directory links also try a junction, file links have no substitute)")

const (
	reparseTagSymlink    = 0xA000000C
	reparseTagMountPoint = 0xA0000003

	errAccessDenied     syscall.Errno = 5
	errPrivilegeNotHeld syscall.Errno = 1314
	errInvalidParameter syscall.Errno = 87

	probePattern = "zakwas-linkprobe-"
)

// linkOS is the operating system's link primitives, replaceable in tests.
type linkOS struct {
	goos     string
	symlink  func(target, link string) error
	junction func(target, link string) error
}

var host = linkOS{goos: runtime.GOOS, symlink: os.Symlink, junction: makeJunction}

var (
	probeMu sync.Mutex
	probed  = map[bool]error{}
)

// RequireLinks reports whether this run can create links to a directory
// (dir) or to a file, and returns ErrLinksDenied when it can't. It is
// always nil off Windows. The answer comes from creating a probe link in a
// temporary directory once per run, so a plan stays read-only for the
// user's own directories; callers ask only when a plan needs a new link, so
// a converged machine checks clean without the privilege.
func RequireLinks(dir bool) error {
	if host.goos != "windows" {
		return nil
	}
	probeMu.Lock()
	defer probeMu.Unlock()
	if err, ok := probed[dir]; ok {
		return err
	}
	err := host.probe(dir)
	probed[dir] = err
	return err
}

// SetLinkOS replaces the link primitives and forgets what was probed, and
// returns a function that restores them. For tests only.
func SetLinkOS(goos string, symlink, junction func(target, link string) error) (restore func()) {
	probeMu.Lock()
	defer probeMu.Unlock()
	saved, savedProbed := host, probed
	host = linkOS{goos: goos, symlink: symlink, junction: junction}
	probed = map[bool]error{}
	return func() {
		probeMu.Lock()
		defer probeMu.Unlock()
		host, probed = saved, savedProbed
	}
}

func (h linkOS) probe(dir bool) error {
	tmp, err := os.MkdirTemp("", probePattern)
	if err != nil {
		return fmt.Errorf("checking that links can be created: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	target := filepath.Join(tmp, "target")
	if dir {
		err = os.Mkdir(target, 0o700)
	} else {
		err = os.WriteFile(target, nil, 0o600)
	}
	if err != nil {
		return fmt.Errorf("checking that links can be created: %w", err)
	}
	return h.create(target, filepath.Join(tmp, "link"), dir)
}

// create links link to target: a symlink, else on Windows a junction for a
// directory. Denial becomes ErrLinksDenied.
func (h linkOS) create(target, link string, dir bool) error {
	err := h.symlink(target, link)
	if err == nil || h.goos != "windows" {
		return err
	}
	if dir && h.junction != nil && filepath.IsAbs(target) {
		if jerr := h.junction(target, link); jerr == nil {
			return nil
		}
	}
	if isDenied(err) {
		return ErrLinksDenied
	}
	return err
}

func isDenied(err error) bool {
	return errors.Is(err, errPrivilegeNotHeld) || errors.Is(err, errAccessDenied) || errors.Is(err, errInvalidParameter)
}

// CreateLink makes link point at target: a symbolic link, or on Windows a
// junction when the target is a directory and the symlink is denied. It
// does not create the parent directory and does not replace anything.
func CreateLink(target, link string) error {
	dir := false
	if host.goos == "windows" {
		info, err := os.Stat(target)
		dir = err == nil && info.IsDir()
	}
	return host.create(target, link, dir)
}

// isLinkMode reports a symlink, or on Windows a junction: Go reports a
// junction as irregular, as it does any other reparse point that redirects
// by name. Cloud placeholders and similar reparse points are not links, and
// writing to them writes to the file itself. tag reads the reparse tag only
// when the mode makes it matter.
func isLinkMode(goos string, mode fs.FileMode, tag func() uint32) bool {
	if mode&fs.ModeSymlink != 0 {
		return true
	}
	if goos != "windows" || mode&fs.ModeIrregular == 0 {
		return false
	}
	t := tag()
	return t == reparseTagSymlink || t == reparseTagMountPoint
}

func isLink(path string, info fs.FileInfo) bool {
	return isLinkMode(runtime.GOOS, info.Mode(), func() uint32 { return reparseTag(path) })
}

// LinkTarget returns where the link at path points. ok is false when path
// is not a symlink or, on Windows, a junction; info is path's Lstat.
func LinkTarget(path string, info fs.FileInfo) (target string, ok bool, err error) {
	if !isLink(path, info) {
		return "", false, nil
	}
	target, err = os.Readlink(path)
	return target, err == nil, err
}

// SameTarget reports whether current, as read from the link at link, names
// want. It is an exact comparison everywhere but on Windows, where Readlink
// answers with \??\ or \\?\ prefixes, either slash, a relative path when
// the link was made by hand, and any case.
func SameTarget(link, current, want string) bool {
	return sameTarget(host.goos, link, current, want)
}

func sameTarget(goos, link, current, want string) bool {
	if goos != "windows" {
		return current == want
	}
	return winKey(link, current) == winKey(link, want)
}

// winKey normalizes a Windows path as written in a link, resolved against
// the directory of the link when it is relative, for comparison.
func winKey(link, p string) string {
	p = strings.ReplaceAll(p, "/", `\`)
	switch {
	case strings.HasPrefix(p, `\\?\UNC\`):
		p = `\\` + p[len(`\\?\UNC\`):]
	case strings.HasPrefix(p, `\\?\`), strings.HasPrefix(p, `\??\`):
		p = p[len(`\\?\`):]
	}
	if strings.HasPrefix(p, `\`) && !strings.HasPrefix(p, `\\`) && len(link) >= 2 && link[1] == ':' {
		p = link[:2] + p
	}
	if !winAbs(p) {
		dir := strings.ReplaceAll(link, "/", `\`)
		if i := strings.LastIndex(dir, `\`); i >= 0 {
			dir = dir[:i]
		} else {
			dir = ""
		}
		p = dir + `\` + p
	}
	return strings.ToLower(winClean(p))
}

func winAbs(p string) bool {
	return strings.HasPrefix(p, `\\`) || len(p) >= 3 && p[1] == ':' && p[2] == '\\'
}

// winClean resolves "." and ".." and repeated or trailing separators,
// keeping the drive or the \\server\share head.
func winClean(p string) string {
	head, rest := "", p
	switch {
	case strings.HasPrefix(p, `\\`):
		parts := strings.SplitN(p[2:], `\`, 3)
		head = `\\` + strings.Join(parts[:min(2, len(parts))], `\`)
		rest = ""
		if len(parts) == 3 {
			rest = parts[2]
		}
	case len(p) >= 2 && p[1] == ':':
		head, rest = p[:2], p[2:]
	}
	var out []string
	for part := range strings.SplitSeq(rest, `\`) {
		switch part {
		case "", ".":
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, part)
		}
	}
	return head + `\` + strings.Join(out, `\`)
}
