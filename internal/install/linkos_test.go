package install

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

type fakeLinks struct {
	symlinkErr  error
	junctionErr error
	symlinks    []string
	junctions   []string
}

func (f *fakeLinks) install(t *testing.T, goos string) {
	t.Helper()
	t.Cleanup(SetLinkOS(goos,
		func(target, link string) error {
			f.symlinks = append(f.symlinks, link)
			if f.symlinkErr != nil {
				return &os.LinkError{Op: "symlink", Old: target, New: link, Err: f.symlinkErr}
			}
			return os.Symlink(target, link)
		},
		func(target, link string) error {
			f.junctions = append(f.junctions, link)
			if f.junctionErr != nil {
				return f.junctionErr
			}
			return os.Symlink(target, link)
		}))
}

func TestRequireLinksOnWindows(t *testing.T) {
	denied := syscall.Errno(1314)
	tests := []struct {
		name        string
		goos        string
		symlinkErr  error
		junctionErr error
		dir         bool
		want        error
	}{
		{"unix needs nothing", "linux", denied, nil, true, nil},
		{"symlinks work", "windows", nil, nil, false, nil},
		{"symlinks work for directories", "windows", nil, nil, true, nil},
		{"directory falls back to a junction", "windows", denied, nil, true, nil},
		{"a file has no junction", "windows", denied, nil, false, ErrLinksDenied},
		{"neither works", "windows", denied, errors.New("no"), true, ErrLinksDenied},
		{"access denied counts", "windows", syscall.Errno(5), errors.New("no"), true, ErrLinksDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeLinks{symlinkErr: tt.symlinkErr, junctionErr: tt.junctionErr}
			f.install(t, tt.goos)

			err := RequireLinks(tt.dir)

			if !errors.Is(err, tt.want) {
				t.Errorf("RequireLinks(%v) = %v, want %v", tt.dir, err, tt.want)
			}
		})
	}
}

func TestRequireLinksProbesOnceAndCleansUp(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	f := &fakeLinks{}
	f.install(t, "windows")

	for range 3 {
		if err := RequireLinks(true); err != nil {
			t.Fatal(err)
		}
	}
	if err := RequireLinks(false); err != nil {
		t.Fatal(err)
	}

	if len(f.symlinks) != 2 {
		t.Errorf("probed %d times (%v), want once per kind", len(f.symlinks), f.symlinks)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("the probe left %v behind", left)
	}
	for _, l := range f.symlinks {
		if !strings.HasPrefix(l, tmp) {
			t.Errorf("probe link %s is outside the temp dir", l)
		}
	}
}

func TestProbeKeepsErrorsThatAreNotDenial(t *testing.T) {
	f := &fakeLinks{symlinkErr: syscall.ENOSPC}
	f.install(t, "windows")

	err := RequireLinks(false)

	if err == nil || errors.Is(err, ErrLinksDenied) || !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("err = %v, want the disk error as is", err)
	}
}

func TestDeniedErrorIsOneLineThatSaysHowToEnableLinks(t *testing.T) {
	msg := ErrLinksDenied.Error()
	for _, want := range []string{"Developer Mode", "administrator", "SeCreateSymbolicLinkPrivilege", "file links", "junction"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("message spans lines: %q", msg)
	}
}

func TestCreateLinkFallsBackToAJunctionOnlyForDirectories(t *testing.T) {
	dir, file := t.TempDir(), filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeLinks{symlinkErr: syscall.Errno(1314)}
	f.install(t, "windows")

	if err := CreateLink(dir, filepath.Join(t.TempDir(), "d")); err != nil {
		t.Errorf("directory link: %v", err)
	}
	if len(f.junctions) != 1 {
		t.Errorf("junctions = %v, want one", f.junctions)
	}
	if err := CreateLink(file, filepath.Join(t.TempDir(), "l")); !errors.Is(err, ErrLinksDenied) {
		t.Errorf("file link err = %v, want ErrLinksDenied", err)
	}
	if len(f.junctions) != 1 {
		t.Errorf("a file link tried a junction: %v", f.junctions)
	}
}

func TestPlanLinkOnWindowsNeedsLinksOnlyToCreateOne(t *testing.T) {
	f := &fakeLinks{symlinkErr: syscall.Errno(1314), junctionErr: errors.New("no")}
	f.install(t, "windows")
	root, home := t.TempDir(), t.TempDir()
	paths := config.Paths{Home: home, Root: root}
	src := filepath.Join(root, "rc")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := PlanLink(paths, src, filepath.Join(home, "rc"))
	if c != nil || !errors.Is(err, ErrLinksDenied) {
		t.Fatalf("PlanLink = %v, %v; want ErrLinksDenied", c, err)
	}

	if err := os.Symlink(src, filepath.Join(home, "rc")); err != nil {
		t.Skipf("this machine can't create symlinks: %v", err)
	}
	if c, err := PlanLink(paths, src, filepath.Join(home, "rc")); c != nil || err != nil {
		t.Errorf("a converged link plans %v, %v; it needs no privilege to stay", c, err)
	}
}

func TestPlanLinkOnWindowsIgnoresTheCaseOfTheTarget(t *testing.T) {
	f := &fakeLinks{}
	f.install(t, "windows")
	root, home := t.TempDir(), t.TempDir()
	paths := config.Paths{Home: home, Root: root}
	src := filepath.Join(root, "rc")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(home, "rc")
	if err := os.Symlink(strings.ToUpper(src), dst); err != nil {
		t.Skipf("this machine can't create symlinks: %v", err)
	}

	c, err := PlanLink(paths, src, dst)

	if err != nil || c != nil {
		t.Errorf("a link that differs only in case plans %v, %v", c, err)
	}
}

func TestIsLinkMode(t *testing.T) {
	tests := []struct {
		name string
		goos string
		mode fs.FileMode
		tag  uint32
		want bool
	}{
		{"symlink", "linux", fs.ModeSymlink, 0, true},
		{"windows symlink", "windows", fs.ModeSymlink, 0, true},
		{"regular", "windows", 0, 0, false},
		{"junction", "windows", fs.ModeIrregular, reparseTagMountPoint, true},
		{"irregular symlink tag", "windows", fs.ModeIrregular, reparseTagSymlink, true},
		{"cloud placeholder", "windows", fs.ModeIrregular, 0x9000001A, false},
		{"app execution link", "windows", fs.ModeIrregular, 0x8000001B, false},
		{"irregular off windows", "linux", fs.ModeIrregular, reparseTagMountPoint, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLinkMode(tt.goos, tt.mode, func() uint32 { return tt.tag }); got != tt.want {
				t.Errorf("isLinkMode = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsLinkModeReadsTheTagOnlyForIrregularFiles(t *testing.T) {
	read := func() uint32 { t.Error("the reparse tag was read"); return 0 }
	isLinkMode("windows", 0, read)
	isLinkMode("windows", fs.ModeSymlink, read)
	isLinkMode("linux", fs.ModeIrregular, read)
}

func TestSameTarget(t *testing.T) {
	tests := []struct {
		name          string
		goos          string
		link, cur, to string
		want          bool
	}{
		{"unix equal", "linux", "/h/l", "/a/b", "/a/b", true},
		{"unix is exact", "linux", "/h/l", "/a/B", "/a/b", false},
		{"unix does not clean", "darwin", "/h/l", "/a/./b", "/a/b", false},
		{"windows equal", "windows", `C:\h\l`, `C:\a\b`, `C:\a\b`, true},
		{"windows case", "windows", `C:\h\l`, `c:\A\B`, `C:\a\b`, true},
		{"windows verbatim prefix", "windows", `C:\h\l`, `\\?\C:\a\b`, `C:\a\b`, true},
		{"windows nt prefix", "windows", `C:\h\l`, `\??\C:\a\b`, `C:\a\b`, true},
		{"windows slashes", "windows", `C:\h\l`, `C:/a/b`, `C:\a\b`, true},
		{"windows trailing separator", "windows", `C:\h\l`, `C:\a\b\`, `C:\a\b`, true},
		{"windows dots", "windows", `C:\h\l`, `C:\a\x\..\b`, `C:\a\b`, true},
		{"windows relative to the link", "windows", `C:\h\l`, `..\a\b`, `C:\a\b`, true},
		{"windows relative elsewhere", "windows", `C:\h\l`, `..\a\c`, `C:\a\b`, false},
		{"windows drive relative", "windows", `D:\h\l`, `\a\b`, `D:\a\b`, true},
		{"windows other drive", "windows", `C:\h\l`, `D:\a\b`, `C:\a\b`, false},
		{"windows unc", "windows", `C:\h\l`, `\\?\UNC\srv\share\a`, `\\srv\share\a`, true},
		{"windows unc case", "windows", `C:\h\l`, `\\SRV\Share\A`, `\\srv\share\a`, true},
		{"windows different", "windows", `C:\h\l`, `C:\a\b`, `C:\a\c`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameTarget(tt.goos, tt.link, tt.cur, tt.to); got != tt.want {
				t.Errorf("sameTarget(%q, %q, %q) = %v, want %v", tt.link, tt.cur, tt.to, got, tt.want)
			}
		})
	}
}

func TestLinkTargetReadsSymlinksOnly(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this machine can't create symlinks: %v", err)
	}
	for path, want := range map[string]string{link: target, target: ""} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := LinkTarget(path, info)
		if err != nil || got != want || ok != (want != "") {
			t.Errorf("LinkTarget(%s) = %q, %v, %v; want %q", path, got, ok, err, want)
		}
	}
}
