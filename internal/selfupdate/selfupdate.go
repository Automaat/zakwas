// Package selfupdate replaces the running zakwas binary with a GitHub
// release, verified against the release's checksums.txt.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/Automaat/zakwas/internal/fsutil"
	"github.com/Automaat/zakwas/internal/install"
)

// ReleasesURL is where releases are published.
const ReleasesURL = "https://github.com/Automaat/zakwas/releases"

// maxDownload caps what is read into memory; release archives are a few MB.
const maxDownload = 256 << 20

var versionRE = regexp.MustCompile(`^\d+\.\d+\.\d+([-+][0-9A-Za-z.+-]+)?$`)

// Updater downloads releases from BaseURL (ReleasesURL when empty).
type Updater struct {
	BaseURL string
	// OS and Arch default to this machine's runtime.GOOS and GOARCH.
	OS     string
	Arch   string
	Client *http.Client
}

var releaseTargets = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"}

func (u *Updater) base() string { return strings.TrimSuffix(cmp.Or(u.BaseURL, ReleasesURL), "/") }

func (u *Updater) client() *http.Client { return cmp.Or(u.Client, http.DefaultClient) }

// Asset is the release archive name for version on this machine's OS and
// arch, the GoReleaser name_template zakwas_{version}_{os}_{arch}, with
// .tar.gz, or .zip on Windows.
func (u *Updater) Asset(version string) string {
	return AssetName(version, cmp.Or(u.OS, runtime.GOOS), cmp.Or(u.Arch, runtime.GOARCH))
}

// AssetName is the release archive name for version, goos and goarch.
func AssetName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("zakwas_%s_%s_%s.%s", version, goos, goarch, ext)
}

func (u *Updater) checkTarget() error {
	target := cmp.Or(u.OS, runtime.GOOS) + "/" + cmp.Or(u.Arch, runtime.GOARCH)
	if !slices.Contains(releaseTargets, target) {
		return fmt.Errorf("no zakwas release for %s; releases cover %s", target, strings.Join(releaseTargets, ", "))
	}
	return nil
}

// NormalizeVersion strips a leading "v" and rejects anything that isn't a
// release version.
func NormalizeVersion(v string) (string, error) {
	v = strings.TrimPrefix(v, "v")
	if !versionRE.MatchString(v) {
		return "", fmt.Errorf("%q is not a release version (X.Y.Z)", v)
	}
	return v, nil
}

// Latest reads the newest version from the releases/latest redirect: the
// API would count against GitHub's unauthenticated rate limit.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.base()+"/latest", http.NoBody)
	if err != nil {
		return "", err
	}
	noFollow := *u.client()
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", err
	}
	if err := resp.Body.Close(); err != nil {
		return "", err
	}
	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("can't find the latest zakwas release: HEAD %s: %s", req.URL, resp.Status)
	}
	v, err := NormalizeVersion(path.Base(loc.Path))
	if err != nil {
		return "", fmt.Errorf("can't find the latest zakwas release: %w", err)
	}
	return v, nil
}

// Download fetches version's archive and checks its SHA256 against the
// release's checksums.txt.
func (u *Updater) Download(ctx context.Context, version string) ([]byte, error) {
	if err := u.checkTarget(); err != nil {
		return nil, err
	}
	base := fmt.Sprintf("%s/download/v%s/", u.base(), version)
	asset := u.Asset(version)
	sums, err := u.download(ctx, base+"checksums.txt")
	if err != nil {
		return nil, err
	}
	want, err := checksum(sums, asset)
	if err != nil {
		return nil, err
	}
	archive, err := u.download(ctx, base+asset)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("%s: sha256 %s, want %s", asset, got, want)
	}
	return archive, nil
}

// Fetch downloads and verifies version's archive and returns the zakwas
// binary inside.
func (u *Updater) Fetch(ctx context.Context, version string) ([]byte, error) {
	archive, err := u.Download(ctx, version)
	if err != nil {
		return nil, err
	}
	return Extract(archive)
}

func (u *Updater) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload))
	err = errors.Join(err, resp.Body.Close())
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return body, err
}

// checksum finds asset in a `sha256sum` listing ("<hash>  <asset>").
func checksum(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		hash, name, ok := strings.Cut(sc.Text(), "  ")
		if ok && strings.TrimPrefix(name, "./") == asset {
			return hash, nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", asset)
}

// Extract returns the zakwas binary from a release archive: zakwas from a
// tar.gz, zakwas.exe from the zip of a Windows release.
func Extract(archive []byte) ([]byte, error) {
	if bytes.HasPrefix(archive, []byte("PK\x03\x04")) {
		return extractZip(archive)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("release archive has no zakwas binary")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Clean(h.Name) == "zakwas" {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

func extractZip(archive []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Mode().IsRegular() && path.Clean(strings.ReplaceAll(f.Name, `\`, "/")) == "zakwas.exe" {
			return readZipFile(f)
		}
	}
	return nil, errors.New("release archive has no zakwas binary")
}

func readZipFile(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxDownload {
		return nil, fmt.Errorf("%s in the release archive is %d bytes, over the %d limit", f.Name, f.UncompressedSize64, maxDownload)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(rc, maxDownload))
	return data, errors.Join(err, rc.Close())
}

// Replace swaps exe for bin. A failed update leaves the old binary
// working: on Unix through a temp file renamed over exe, on Windows, which
// can't overwrite a running program but can rename it, through ReplaceRunning,
// which records the files it leaves under home's zakwas state. On Windows the
// caller holds the run lock; warn gets the notes about a state file it had to
// set aside and may be nil.
func Replace(exe, home string, warn func(string), bin []byte) error {
	if runtime.GOOS == "windows" {
		return Swapper{StatePath: StatePath(home), Warn: warn}.ReplaceRunning(exe, bin)
	}
	return install.WriteAtomic(exe, bin, 0o755)
}

// oldDirPrefix starts the name of the directory an update moves the old
// binary into. The copy keeps the name of the binary, so every rule of the
// guard that matches a path ending in zakwas.exe matches it too.
const oldDirPrefix = ".zakwas-old-"

// stagedPrefix starts the name of the file an update writes the new binary
// to before it moves it into place; the name ends in the binary's name.
const stagedPrefix = "new-"

// StatePath records the files updates left, for CleanupOld.
func StatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "selfupdate.json")
}

// updateState lists the old copies of the binary and the staged new
// binaries that updates created and may have left behind.
type updateState struct {
	Old    []string `json:"old,omitempty"`
	Staged []string `json:"staged,omitempty"`
}

func (st updateState) empty() bool { return len(st.Old) == 0 && len(st.Staged) == 0 }

// Swapper replaces a binary that may be running and removes what that
// leaves behind. Both work on the state file at StatePath by load, change,
// save, so the caller holds the run lock around each call: the lock keeps an
// update and a cleanup, in this process or another, from interleaving.
// stateMu only keeps the goroutines of one process apart.
type Swapper struct {
	StatePath string
	// Warn gets a note when the state file was unreadable and has been set
	// aside; nil drops it.
	Warn func(string)
}

// stateMu keeps updates of one process from losing each other's records.
var stateMu sync.Mutex

func (w Swapper) warn(msg string) {
	if w.Warn != nil {
		w.Warn(msg)
	}
}

// ReplaceRunning swaps exe for bin when exe may be running. It names a staged
// file next to exe, records the name, writes bin there, moves exe into a new
// directory of its own (oldDirPrefix) and the staged file into place, and
// moves the old binary back if that fails. The old copy stays until CleanupOld
// removes it, since Windows refuses to delete a running program. Both paths are
// recorded before anything is written or moved, so a crash leaves a trail: a
// staged file CleanupOld removes, or, between the two renames, no exe and the
// old copy in the recorded directory.
func (w Swapper) ReplaceRunning(exe string, bin []byte) (err error) {
	dir, base := filepath.Dir(exe), filepath.Base(exe)
	staged := filepath.Join(dir, fmt.Sprintf("%s%016x-%s", stagedPrefix, rand.Uint64(), base))
	oldDir := filepath.Join(dir, fmt.Sprintf("%s%016x", oldDirPrefix, rand.Uint64()))
	old := filepath.Join(oldDir, base)
	if err := w.record(func(st *updateState) { st.Staged = append(st.Staged, staged) }); err != nil {
		return err
	}
	forgetStaged := func() error {
		return errors.Join(os.Remove(staged), w.record(func(st *updateState) {
			st.Staged = slices.DeleteFunc(st.Staged, func(p string) bool { return p == staged })
		}))
	}
	if err := writeStaged(staged, bin); err != nil {
		return errors.Join(err, forgetStaged())
	}
	if err := os.Mkdir(oldDir, 0o755); err != nil {
		return errors.Join(err, forgetStaged())
	}
	if err := w.record(func(st *updateState) { st.Old = append(st.Old, old) }); err != nil {
		return errors.Join(err, os.Remove(oldDir), forgetStaged())
	}
	forgetOld := func() error {
		return errors.Join(os.Remove(oldDir), w.record(func(st *updateState) { st.Old = slices.DeleteFunc(st.Old, func(p string) bool { return p == old }) }))
	}
	if err := rename(exe, old); err != nil {
		return errors.Join(err, forgetOld(), forgetStaged())
	}
	if err := rename(staged, exe); err != nil {
		if back := rename(old, exe); back != nil {
			return errors.Join(err, fmt.Errorf("the old binary is at %s, move it back by hand: %w", old, back), forgetStaged())
		}
		return errors.Join(err, forgetOld(), forgetStaged())
	}
	// staged is exe now. Dropping its record is housekeeping: a record left
	// behind names a file that no longer exists, which CleanupOld forgets.
	_ = w.record(func(st *updateState) {
		st.Staged = slices.DeleteFunc(st.Staged, func(p string) bool { return p == staged })
	})
	return nil
}

// writeStaged creates path, which must not exist, with bin in it.
func writeStaged(path string, bin []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	_, err = f.Write(bin)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// loadUpdateState reads the state file. One that can't be parsed (corrupt,
// empty, not an object of lists) is moved aside to path+".bad", replacing an
// older one, and read as empty, so a damaged file never blocks an update; the
// move is reported through warn.
func (w Swapper) loadUpdateState() (updateState, error) {
	var st updateState
	data, err := os.ReadFile(w.StatePath)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		bad := w.StatePath + ".bad"
		if mv := rename(w.StatePath, bad); mv != nil {
			return updateState{}, fmt.Errorf("%s can't be read (%w) or set aside: %w", w.StatePath, err, mv)
		}
		w.warn(fmt.Sprintf("%s was not valid (%v); moved it to %s and started a new one", w.StatePath, err, bad))
		return updateState{}, nil
	}
	return st, nil
}

func (w Swapper) saveUpdateState(st updateState) error {
	if st.empty() {
		if err := os.Remove(w.StatePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return install.WriteAtomic(w.StatePath, append(data, '\n'), 0o644)
}

// record changes the state file by load, change, save.
func (w Swapper) record(change func(*updateState)) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	st, err := w.loadUpdateState()
	if err != nil {
		return err
	}
	change(&st)
	return w.saveUpdateState(st)
}

// rename and removeFile are replaced in tests to fail a step.
var (
	rename     = fsutil.Rename
	removeFile = os.Remove
)

// CleanupOld removes what updates of the binary exe left in home's state:
// the old copies of the binary, once nothing runs from them, and the staged
// binaries of updates that never finished. It deletes only a recorded path
// that lies next to exe under the shape an update gives it, never a name it
// guesses, and keeps what is still in use for the next time. It changes files,
// so only commands that hold the run lock call it (apply, upgrade and
// self-update), never plan or check. exe is the path Replace was given,
// symlinks resolved, as Executable returns it.
func CleanupOld(home, exe string, warn func(string)) {
	Swapper{StatePath: StatePath(home), Warn: warn}.CleanupOld(exe)
}

// CleanupOld is the package function CleanupOld for a state file at
// w.StatePath.
func (w Swapper) CleanupOld(exe string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	st, err := w.loadUpdateState()
	if err != nil || st.empty() {
		return
	}
	var keep updateState
	for _, old := range st.Old {
		if !leftBy(old, exe) {
			keep.Old = append(keep.Old, old)
			continue
		}
		if err := removeFile(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
			keep.Old = append(keep.Old, old)
			continue
		}
		_ = os.Remove(filepath.Dir(old))
	}
	for _, staged := range st.Staged {
		if !stagedBy(staged, exe) {
			keep.Staged = append(keep.Staged, staged)
			continue
		}
		if err := removeFile(staged); err != nil && !errors.Is(err, fs.ErrNotExist) {
			keep.Staged = append(keep.Staged, staged)
		}
	}
	if len(keep.Old) != len(st.Old) || len(keep.Staged) != len(st.Staged) {
		_ = w.saveUpdateState(keep)
	}
}

// sameDir reports whether a and b are the same directory, symlinks and
// junctions resolved and, on Windows, case ignored: the recorded path was
// built from Executable, but the running binary may be reached by another.
func sameDir(a, b string) bool {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return filepath.Clean(p)
	}
	return sameName(resolve(a), resolve(b))
}

// plain reports whether path is a plain directory (dir) or a regular file:
// not a symlink, and not a junction, which Go reports as irregular.
func plain(path string, dir bool) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if dir {
		return info.Mode().Type() == fs.ModeDir
	}
	return info.Mode().Type() == 0
}

// leftBy reports whether old is where an update of exe puts its old copy:
// <dir of exe>/<oldDirPrefix>*/<name of exe>, in a plain directory.
func leftBy(old, exe string) bool {
	oldDir := filepath.Dir(old)
	return plain(oldDir, true) &&
		sameDir(filepath.Dir(oldDir), filepath.Dir(exe)) &&
		strings.HasPrefix(filepath.Base(oldDir), oldDirPrefix) &&
		sameName(filepath.Base(old), filepath.Base(exe))
}

// stagedBy reports whether staged is a file an update of exe stages its
// new binary in: <dir of exe>/new-*-<name of exe>, a regular file.
func stagedBy(staged, exe string) bool {
	base, want := filepath.Base(staged), "-"+filepath.Base(exe)
	return strings.HasPrefix(base, stagedPrefix) && len(base) > len(stagedPrefix)+len(want) &&
		strings.HasSuffix(strings.ToLower(base), strings.ToLower(want)) &&
		sameDir(filepath.Dir(staged), filepath.Dir(exe)) && plain(staged, false)
}

// sameName compares paths as the system does: without regard to case on
// Windows.
func sameName(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Executable returns the running binary's path with symlinks resolved, so a
// ~/.local/bin or /opt/homebrew/bin link leads to the file that owns it.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// ManagedBy explains how to update exe when a package manager installed it,
// or returns "" when self-update may replace it.
func ManagedBy(exe, home string, getenv func(string) string) string {
	var miseDirs []string
	if d := getenv("MISE_DATA_DIR"); d != "" {
		miseDirs = append(miseDirs, d)
	}
	if d := getenv("XDG_DATA_HOME"); d != "" {
		miseDirs = append(miseDirs, filepath.Join(d, "mise"))
	}
	if d := getenv("LOCALAPPDATA"); d != "" {
		miseDirs = append(miseDirs, filepath.Join(d, "mise"))
	}
	miseDirs = append(miseDirs, filepath.Join(home, ".local", "share", "mise"))
	for _, d := range miseDirs {
		if within(exe, filepath.Join(d, "installs")) {
			return `zakwas is managed by mise; bump "github:Automaat/zakwas" in your mise config instead`
		}
	}
	prefixes := []string{"/opt/homebrew", "/usr/local"}
	if p := getenv("HOMEBREW_PREFIX"); p != "" {
		prefixes = append([]string{p}, prefixes...)
	}
	for _, p := range prefixes {
		for _, sub := range []string{"Caskroom", "Cellar"} {
			if within(exe, filepath.Join(p, sub)) {
				return "zakwas is managed by Homebrew; run `brew upgrade zakwas` instead"
			}
		}
	}
	return ""
}

// within compares against dir both as given and with symlinks resolved,
// since exe has its symlinks resolved.
func within(path, dir string) bool {
	dirs := []string{filepath.Clean(dir)}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dirs = append(dirs, resolved)
	}
	for _, d := range dirs {
		prefix := d + string(filepath.Separator)
		if runtime.GOOS == "windows" {
			// Windows paths are case-insensitive.
			if len(path) >= len(prefix) && strings.EqualFold(path[:len(prefix)], prefix) {
				return true
			}
			continue
		}
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
