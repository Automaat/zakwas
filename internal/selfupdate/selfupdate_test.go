package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Automaat/zakwas/internal/selfupdate/selfupdatetest"
	"github.com/Automaat/zakwas/internal/testutil"
)

type release struct {
	latest   string
	archive  []byte
	checksum string
}

func serve(t *testing.T, r release) *httptest.Server {
	t.Helper()
	asset := "zakwas_1.2.3_darwin_arm64.tar.gz"
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, req *http.Request) {
		if r.latest == "" {
			http.NotFound(w, req)
			return
		}
		http.Redirect(w, req, "/releases/tag/"+r.latest, http.StatusFound)
	})
	mux.HandleFunc("/releases/tag/", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("followed the latest redirect")
	})
	mux.HandleFunc("/releases/download/v1.2.3/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		sums := "deadbeef  zakwas_1.2.3_darwin_amd64.tar.gz\n"
		if r.checksum != "" {
			sums += r.checksum + "  " + asset + "\n"
		}
		_, _ = w.Write([]byte(sums))
	})
	mux.HandleFunc("/releases/download/v1.2.3/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(r.archive)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLatest(t *testing.T) {
	tests := []struct {
		latest, want, err string
	}{
		{latest: "v1.2.3", want: "1.2.3"},
		{latest: "v2.0.0-rc.1", want: "2.0.0-rc.1"},
		{latest: "", err: "can't find the latest zakwas release"},
		{latest: "nightly", err: "not a release version"},
	}
	for _, tt := range tests {
		t.Run(tt.latest, func(t *testing.T) {
			srv := serve(t, release{latest: tt.latest})
			u := &Updater{BaseURL: srv.URL + "/releases"}
			got, err := u.Latest(context.Background())
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Errorf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("Latest = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestFetch(t *testing.T) {
	good := selfupdatetest.Archive(t, map[string]string{"LICENSE": "MIT", "zakwas": "new binary"})
	noBin := selfupdatetest.Archive(t, map[string]string{"README.md": "hi"})
	tests := []struct {
		name string
		rel  release
		want string
		err  string
	}{
		{"verified", release{archive: good, checksum: selfupdatetest.SHA256(good)}, "new binary", ""},
		{"checksum mismatch", release{archive: good, checksum: selfupdatetest.SHA256([]byte("other"))}, "", "sha256"},
		{"no entry", release{archive: good}, "", "no entry for zakwas_1.2.3_darwin_arm64.tar.gz"},
		{"no binary", release{archive: noBin, checksum: selfupdatetest.SHA256(noBin)}, "", "no zakwas binary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := serve(t, tt.rel)
			u := &Updater{BaseURL: srv.URL + "/releases", OS: "darwin", Arch: "arm64"}
			got, err := u.Fetch(context.Background(), "1.2.3")
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Errorf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || string(got) != tt.want {
				t.Errorf("Fetch = %q, %v", got, err)
			}
		})
	}
}

func TestAssetName(t *testing.T) {
	tests := []struct {
		goos, arch, want string
	}{
		{"darwin", "arm64", "zakwas_1.2.3_darwin_arm64.tar.gz"},
		{"darwin", "amd64", "zakwas_1.2.3_darwin_amd64.tar.gz"},
		{"linux", "amd64", "zakwas_1.2.3_linux_amd64.tar.gz"},
		{"linux", "arm64", "zakwas_1.2.3_linux_arm64.tar.gz"},
		{"windows", "amd64", "zakwas_1.2.3_windows_amd64.zip"},
		{"windows", "arm64", "zakwas_1.2.3_windows_arm64.zip"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.arch, func(t *testing.T) {
			u := &Updater{OS: tt.goos, Arch: tt.arch}
			if got := u.Asset("1.2.3"); got != tt.want {
				t.Errorf("Asset = %q, want %q", got, tt.want)
			}
			if err := u.checkTarget(); err != nil {
				t.Errorf("checkTarget: %v", err)
			}
		})
	}
}

func TestAssetDefaultsToThisMachine(t *testing.T) {
	want := "zakwas_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	if runtime.GOOS == "windows" {
		want = "zakwas_1.2.3_windows_" + runtime.GOARCH + ".zip"
	}
	if got := (&Updater{}).Asset("1.2.3"); got != want {
		t.Errorf("Asset = %q, want %q", got, want)
	}
}

func TestFetchUnsupportedTarget(t *testing.T) {
	for _, target := range [][2]string{{"windows", "386"}, {"linux", "386"}, {"freebsd", "arm64"}} {
		u := &Updater{BaseURL: "http://127.0.0.1:1/releases", OS: target[0], Arch: target[1]}
		_, err := u.Fetch(context.Background(), "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "no zakwas release for "+target[0]+"/"+target[1]) {
			t.Errorf("%v: err = %v", target, err)
		}
	}
}

func TestFetchLinux(t *testing.T) {
	base := selfupdatetest.ServeFor(t, "1.2.3", "linux", "amd64", "linux binary")
	u := &Updater{BaseURL: base, OS: "linux", Arch: "amd64"}
	got, err := u.Fetch(context.Background(), "1.2.3")
	if err != nil || string(got) != "linux binary" {
		t.Errorf("Fetch = %q, %v", got, err)
	}
	other := &Updater{BaseURL: base, OS: "darwin", Arch: "amd64"}
	if _, err := other.Fetch(context.Background(), "1.2.3"); err == nil || !strings.Contains(err.Error(), "no entry for zakwas_1.2.3_darwin_amd64.tar.gz") {
		t.Errorf("darwin asset served from a linux release: %v", err)
	}
}

func TestFetchWindows(t *testing.T) {
	base := selfupdatetest.ServeFor(t, "1.2.3", "windows", "arm64", "windows binary")
	u := &Updater{BaseURL: base, OS: "windows", Arch: "arm64"}
	got, err := u.Fetch(context.Background(), "1.2.3")
	if err != nil || string(got) != "windows binary" {
		t.Errorf("Fetch = %q, %v", got, err)
	}
	if _, err := (&Updater{BaseURL: base, OS: "windows", Arch: "amd64"}).Fetch(context.Background(), "1.2.3"); err == nil || !strings.Contains(err.Error(), "no entry for zakwas_1.2.3_windows_amd64.zip") {
		t.Errorf("amd64 asset served from an arm64 release: %v", err)
	}
}

func TestFetchWindowsChecksTheZipAgainstChecksums(t *testing.T) {
	zipped := selfupdatetest.ZipArchive(t, map[string]string{"zakwas.exe": "windows binary"})
	srv := serveAsset(t, "zakwas_1.2.3_windows_amd64.zip", zipped, selfupdatetest.SHA256([]byte("other")))
	u := &Updater{BaseURL: srv.URL + "/releases", OS: "windows", Arch: "amd64"}
	if _, err := u.Fetch(context.Background(), "1.2.3"); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("err = %v, want a checksum mismatch", err)
	}
}

func serveAsset(t *testing.T, asset string, archive []byte, checksum string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/download/v1.2.3/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksum + "  " + asset + "\n"))
	})
	mux.HandleFunc("/releases/download/v1.2.3/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestExtract(t *testing.T) {
	tests := []struct {
		name    string
		archive []byte
		want    string
		err     string
	}{
		{"tar", selfupdatetest.Archive(t, map[string]string{"LICENSE": "MIT", "zakwas": "unix binary"}), "unix binary", ""},
		{"zip", selfupdatetest.ZipArchive(t, map[string]string{"LICENSE": "MIT", "zakwas.exe": "windows binary"}), "windows binary", ""},
		{"zip with a dot slash", selfupdatetest.ZipArchive(t, map[string]string{"./zakwas.exe": "windows binary"}), "windows binary", ""},
		{"zip with backslashes", selfupdatetest.ZipArchive(t, map[string]string{`.\zakwas.exe`: "windows binary"}), "windows binary", ""},
		{"zip without the binary", selfupdatetest.ZipArchive(t, map[string]string{"LICENSE": "MIT"}), "", "no zakwas binary"},
		{"zip with the unix name", selfupdatetest.ZipArchive(t, map[string]string{"zakwas": "unix binary"}), "", "no zakwas binary"},
		{"zip with a nested binary", selfupdatetest.ZipArchive(t, map[string]string{"dist/zakwas.exe": "windows binary"}), "", "no zakwas binary"},
		{"tar with the windows name", selfupdatetest.Archive(t, map[string]string{"zakwas.exe": "windows binary"}), "", "no zakwas binary"},
		{"truncated zip", selfupdatetest.ZipArchive(t, map[string]string{"zakwas.exe": "windows binary"})[:20], "", ""},
		{"neither", []byte("not an archive"), "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Extract(tt.archive)
			switch {
			case tt.want != "":
				if err != nil || string(got) != tt.want {
					t.Errorf("Extract = %q, %v; want %q", got, err, tt.want)
				}
			case err == nil:
				t.Errorf("Extract = %q, want an error", got)
			case !strings.Contains(err.Error(), tt.err):
				t.Errorf("err = %v, want %q", err, tt.err)
			}
		})
	}
}

func TestFetchMissingRelease(t *testing.T) {
	srv := serve(t, release{})
	u := &Updater{BaseURL: srv.URL + "/releases", OS: "darwin", Arch: "arm64"}
	if _, err := u.Fetch(context.Background(), "9.9.9"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want a 404", err)
	}
}

func TestReplace(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "zakwas")
	if err := os.WriteFile(exe, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Replace(exe, t.TempDir(), []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(exe)
	if err != nil || !testutil.SamePerm(info.Mode(), 0o755) {
		t.Fatalf("stat = %v, %v", info, err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "new" {
		t.Errorf("content = %q", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	want := 1
	if runtime.GOOS == "windows" {
		want = 2 // the old binary stays in a directory of its own until the next start
	}
	if len(entries) != want {
		t.Errorf("files left: %v, want %d", entries, want)
	}
}

// swapDir is a directory with a binary called zakwas.exe in it, and the path
// of a state file outside it.
func swapDir(t *testing.T) (exe, state string) {
	t.Helper()
	exe = filepath.Join(t.TempDir(), "zakwas.exe")
	write(t, exe, "old")
	return exe, filepath.Join(t.TempDir(), "state", "selfupdate.json")
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func recorded(t *testing.T, state string) []string {
	t.Helper()
	st, err := loadUpdateState(state)
	if err != nil {
		t.Fatal(err)
	}
	return st.Old
}

func TestReplaceRunning(t *testing.T) {
	exe, state := swapDir(t)

	if err := ReplaceRunning(exe, state, []byte("new")); err != nil {
		t.Fatal(err)
	}

	if got := read(t, exe); got != "new" {
		t.Errorf("exe = %q, want new", got)
	}
	old := recorded(t, state)
	if len(old) != 1 {
		t.Fatalf("recorded %q, want the old copy", old)
	}
	if got := read(t, old[0]); got != "old" {
		t.Errorf("the old copy = %q", got)
	}
	if filepath.Base(old[0]) != "zakwas.exe" || !strings.HasPrefix(filepath.Base(filepath.Dir(old[0])), oldDirPrefix) || filepath.Dir(filepath.Dir(old[0])) != filepath.Dir(exe) {
		t.Errorf("the old copy is at %s, want <dir>/%s*/zakwas.exe, a name the guard's patterns match", old[0], oldDirPrefix)
	}
	if got := names(t, filepath.Dir(exe)); len(got) != 2 {
		t.Errorf("files next to exe: %q, want zakwas.exe and the old copy's directory", got)
	}
}

func TestReplaceRunningKeepsEveryOldCopy(t *testing.T) {
	exe, state := swapDir(t)

	for _, v := range []string{"v2", "v3"} {
		if err := ReplaceRunning(exe, state, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}

	old := recorded(t, state)
	if len(old) != 2 || read(t, exe) != "v3" {
		t.Fatalf("exe = %q, recorded %q", read(t, exe), old)
	}
	if read(t, old[0]) != "old" || read(t, old[1]) != "v2" {
		t.Errorf("old copies = %q, %q", read(t, old[0]), read(t, old[1]))
	}
}

func TestReplaceRunningRollsBack(t *testing.T) {
	steps := []struct {
		name string
		fail func(from, to string) bool
		want string
	}{
		{"moving the binary aside", func(_, to string) bool { return strings.Contains(to, oldDirPrefix) }, "move aside"},
		{"moving the new binary in", func(from, _ string) bool { return strings.HasPrefix(filepath.Base(from), "new-") }, "move in"},
	}
	for _, tt := range steps {
		t.Run(tt.name, func(t *testing.T) {
			exe, state := swapDir(t)
			saved := rename
			rename = func(from, to string) error {
				if tt.fail(from, to) {
					return errors.New(tt.want)
				}
				return saved(from, to)
			}
			t.Cleanup(func() { rename = saved })

			err := ReplaceRunning(exe, state, []byte("new"))

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if got := read(t, exe); got != "old" {
				t.Errorf("exe = %q, want the old binary back", got)
			}
			if got := names(t, filepath.Dir(exe)); len(got) != 1 {
				t.Errorf("files left next to exe: %q", got)
			}
			if _, err := os.Lstat(state); !os.IsNotExist(err) {
				t.Errorf("the failed update is still recorded: %q", recorded(t, state))
			}
		})
	}
}

func TestReplaceRunningSaysWhereTheOldBinaryIsWhenRollbackFails(t *testing.T) {
	exe, state := swapDir(t)
	saved := rename
	rename = func(from, to string) error {
		if strings.HasPrefix(filepath.Base(from), "new-") || to == exe {
			return errors.New("blocked")
		}
		return saved(from, to)
	}
	t.Cleanup(func() { rename = saved })

	err := ReplaceRunning(exe, state, []byte("new"))

	old := recorded(t, state)
	if len(old) != 1 || err == nil || !strings.Contains(err.Error(), "the old binary is at "+old[0]+", move it back by hand") {
		t.Fatalf("err = %v, recorded %q", err, old)
	}
	if read(t, old[0]) != "old" {
		t.Errorf("the old binary was lost")
	}
}

func TestReplaceRunningConcurrentlyNeverLeavesNoBinary(t *testing.T) {
	for range 5 {
		exe, state := swapDir(t)
		var wg sync.WaitGroup
		for g := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 5 {
					// A swap that loses the race may fail; what matters is the result.
					_ = ReplaceRunning(exe, state, fmt.Appendf(nil, "g%d-%d", g, i))
				}
			}()
		}
		wg.Wait()

		got, err := os.ReadFile(exe)
		if err != nil {
			t.Fatalf("no zakwas.exe left: %v", err)
		}
		if !strings.HasPrefix(string(got), "g") && string(got) != "old" {
			t.Fatalf("zakwas.exe = %q", got)
		}
		for _, name := range names(t, filepath.Dir(exe)) {
			if strings.HasPrefix(name, "new-") {
				t.Fatalf("a staged file is left: %s", name)
			}
		}
	}
}

func TestCleanupOldRemovesOnlyWhatAnUpdateLeft(t *testing.T) {
	exe, state := swapDir(t)
	backup := exe + ".old"
	write(t, backup, "my own backup")
	if err := ReplaceRunning(exe, state, []byte("new")); err != nil {
		t.Fatal(err)
	}
	old := recorded(t, state)[0]

	cleanupOld(state, exe)

	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Errorf("the old copy is still there: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(old)); !os.IsNotExist(err) {
		t.Errorf("its directory is still there: %v", err)
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Errorf("the state file is still there")
	}
	if read(t, backup) != "my own backup" || read(t, exe) != "new" {
		t.Errorf("cleanup touched %s or %s", backup, exe)
	}
}

func TestCleanupOldIgnoresAnythingItDidNotLeave(t *testing.T) {
	exe, state := swapDir(t)
	dir := filepath.Dir(exe)
	if err := os.MkdirAll(filepath.Join(dir, oldDirPrefix+"x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(elsewhere, oldDirPrefix+"y"), 0o755); err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, p := range []string{
		filepath.Join(dir, oldDirPrefix+"x", "other.exe"),
		filepath.Join(dir, "other", "zakwas.exe"),
		filepath.Join(elsewhere, oldDirPrefix+"y", "zakwas.exe"),
		exe,
		exe + ".old",
	} {
		write(t, p, "keep")
		kept = append(kept, p)
	}
	if err := saveUpdateState(state, updateState{Old: kept}); err != nil {
		t.Fatal(err)
	}

	cleanupOld(state, exe)

	for _, p := range kept {
		if read(t, p) != "keep" {
			t.Errorf("%s changed", p)
		}
	}
	if got := recorded(t, state); len(got) != len(kept) {
		t.Errorf("records = %q, want all %d kept for the installs they belong to", got, len(kept))
	}
}

func TestCleanupOldDoesNotFollowASymlinkedDirectory(t *testing.T) {
	exe, state := swapDir(t)
	target := t.TempDir()
	write(t, filepath.Join(target, "zakwas.exe"), "precious")
	link := filepath.Join(filepath.Dir(exe), oldDirPrefix+"link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if err := saveUpdateState(state, updateState{Old: []string{filepath.Join(link, "zakwas.exe")}}); err != nil {
		t.Fatal(err)
	}

	cleanupOld(state, exe)

	if read(t, filepath.Join(target, "zakwas.exe")) != "precious" {
		t.Errorf("cleanup followed the symlink")
	}
}

func TestCleanupOldKeepsACopyThatIsStillInUse(t *testing.T) {
	exe, state := swapDir(t)
	if err := ReplaceRunning(exe, state, []byte("new")); err != nil {
		t.Fatal(err)
	}
	old := recorded(t, state)[0]
	saved := removeFile
	removeFile = func(p string) error {
		if p == old {
			return errors.New("in use")
		}
		return saved(p)
	}
	t.Cleanup(func() { removeFile = saved })

	cleanupOld(state, exe)

	if read(t, old) != "old" || len(recorded(t, state)) != 1 {
		t.Errorf("the copy in use was forgotten")
	}
}

func TestCleanupOldDoesNothingOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this is the Windows behavior")
	}
	home := t.TempDir()
	state := StatePath(home)
	if err := saveUpdateState(state, updateState{Old: []string{"x"}}); err != nil {
		t.Fatal(err)
	}

	CleanupOld(home)

	if got := recorded(t, state); len(got) != 1 {
		t.Errorf("records = %q", got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestManagedByMiseOnWindows(t *testing.T) {
	local := filepath.Join(t.TempDir(), "AppData", "Local")
	home := t.TempDir()
	env := map[string]string{"LOCALAPPDATA": local}
	getenv := func(k string) string { return env[k] }
	if got := ManagedBy(filepath.Join(local, "mise", "installs", "github-automaat-zakwas", "1.0.0", "zakwas.exe"), home, getenv); !strings.Contains(got, "mise") {
		t.Errorf("ManagedBy(mise install) = %q", got)
	}
	for _, exe := range []string{
		filepath.Join(local, "Programs", "zakwas", "zakwas.exe"),
		filepath.Join(local, "mise", "shims", "zakwas.exe"),
	} {
		if got := ManagedBy(exe, home, getenv); got != "" {
			t.Errorf("ManagedBy(%q) = %q, want none", exe, got)
		}
	}
}

func TestNormalizeVersion(t *testing.T) {
	for in, want := range map[string]string{"1.2.3": "1.2.3", "v1.2.3": "1.2.3", "1.2": "", "latest": "", "1.2.3; rm": ""} {
		got, err := NormalizeVersion(in)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("NormalizeVersion(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestManagedBy(t *testing.T) {
	testutil.SkipOnWindows(t, "the install paths are POSIX (Homebrew and mise layouts)")
	home := "/Users/me"
	tests := []struct {
		name string
		exe  string
		env  map[string]string
		want string
	}{
		{"install.sh", "/Users/me/.local/bin/zakwas", nil, ""},
		{"mise default", "/Users/me/.local/share/mise/installs/github-automaat-zakwas/1.0.0/zakwas", nil, "mise"},
		{"MISE_DATA_DIR", "/data/mise/installs/zakwas/1.0.0/zakwas", map[string]string{"MISE_DATA_DIR": "/data/mise"}, "mise"},
		{"XDG_DATA_HOME", "/xdg/mise/installs/zakwas/1.0.0/zakwas", map[string]string{"XDG_DATA_HOME": "/xdg"}, "mise"},
		{"mise shims dir", "/Users/me/.local/share/mise/shims/zakwas", nil, ""},
		{"cask arm", "/opt/homebrew/Caskroom/zakwas/1.0.0/zakwas", nil, "brew upgrade zakwas"},
		{"cask intel", "/usr/local/Caskroom/zakwas/1.0.0/zakwas", nil, "brew upgrade zakwas"},
		{"cellar", "/opt/homebrew/Cellar/zakwas/1.0.0/bin/zakwas", nil, "brew upgrade zakwas"},
		{"cellar intel", "/usr/local/Cellar/zakwas/1.0.0/bin/zakwas", nil, "brew upgrade zakwas"},
		{"HOMEBREW_PREFIX", "/home/linuxbrew/Caskroom/zakwas/1.0.0/zakwas", map[string]string{"HOMEBREW_PREFIX": "/home/linuxbrew"}, "brew upgrade zakwas"},
		{"plain usr local bin", "/usr/local/bin/zakwas", nil, ""},
		{"prefix lookalike", "/opt/homebrew/Caskroom2/zakwas", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ManagedBy(tt.exe, home, func(k string) string { return tt.env[k] })
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Errorf("ManagedBy(%q) = %q, want mention of %q", tt.exe, got, tt.want)
			}
		})
	}
}

func TestAttested(t *testing.T) {
	for v, want := range map[string]bool{"0.3.9": false, "0.4.0": true, "0.4.0-rc.1": true, "0.10.0": true, "1.0.0": true, "0.3.99": false} {
		if got := Attested(v); got != want {
			t.Errorf("Attested(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestManagedByLinux(t *testing.T) {
	testutil.SkipOnWindows(t, "the install paths are POSIX (Homebrew and mise layouts)")
	home := "/home/me"
	tests := []struct {
		name string
		exe  string
		env  map[string]string
		want string
	}{
		{"install.sh", "/home/me/.local/bin/zakwas", nil, ""},
		{"usr local bin", "/usr/local/bin/zakwas", nil, ""},
		{"mise default", "/home/me/.local/share/mise/installs/github-automaat-zakwas/1.0.0/zakwas", nil, "mise"},
		{"XDG_DATA_HOME", "/home/me/data/mise/installs/zakwas/1.0.0/zakwas", map[string]string{"XDG_DATA_HOME": "/home/me/data"}, "mise"},
		{"linuxbrew cellar", "/home/linuxbrew/.linuxbrew/Cellar/zakwas/1.0.0/bin/zakwas", map[string]string{"HOMEBREW_PREFIX": "/home/linuxbrew/.linuxbrew"}, "brew upgrade zakwas"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ManagedBy(tt.exe, home, func(k string) string { return tt.env[k] })
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Errorf("ManagedBy(%q) = %q, want mention of %q", tt.exe, got, tt.want)
			}
		})
	}
}
