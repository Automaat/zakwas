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
// which records the old copy under home's zakwas state.
func Replace(exe, home string, bin []byte) error {
	if runtime.GOOS == "windows" {
		return ReplaceRunning(exe, StatePath(home), bin)
	}
	return install.WriteAtomic(exe, bin, 0o755)
}

// oldDirPrefix starts the name of the directory an update moves the old
// binary into. The copy keeps the name of the binary, so every rule of the
// guard that matches a path ending in zakwas.exe matches it too.
const oldDirPrefix = ".zakwas-old-"

// StatePath records the old copies updates left, for CleanupOld.
func StatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "selfupdate.json")
}

type updateState struct {
	Old []string `json:"old"`
}

// stateMu keeps updates of one process from losing each other's records;
// across processes the run lock does that.
var stateMu sync.Mutex

// ReplaceRunning swaps exe for bin when exe may be running. It stages bin
// under a unique name next to exe, moves exe into a new directory of its own
// (oldDirPrefix) and the staged file into place, and moves the old binary
// back if that fails. The old copy stays until CleanupOld removes it, since
// Windows refuses to delete a running program. It is recorded in statePath
// before it moves, so a crash between the two renames leaves a trail: exe is
// missing then, and the copy is in the recorded directory.
func ReplaceRunning(exe, statePath string, bin []byte) (err error) {
	dir, base := filepath.Dir(exe), filepath.Base(exe)
	staged, err := stage(dir, base, bin)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(staged))
		}
	}()
	oldDir, err := os.MkdirTemp(dir, oldDirPrefix+"*")
	if err != nil {
		return err
	}
	old := filepath.Join(oldDir, base)
	if err := recordOld(statePath, old); err != nil {
		return errors.Join(err, os.Remove(oldDir))
	}
	forget := func() error { return errors.Join(unrecordOld(statePath, old), os.Remove(oldDir)) }
	if err := rename(exe, old); err != nil {
		return errors.Join(err, forget())
	}
	if err := rename(staged, exe); err != nil {
		if back := rename(old, exe); back != nil {
			return errors.Join(err, fmt.Errorf("the old binary is at %s, move it back by hand: %w", old, back))
		}
		return errors.Join(err, forget())
	}
	return nil
}

// stage writes bin to a unique file in dir whose name ends in base.
func stage(dir, base string, bin []byte) (string, error) {
	f, err := os.CreateTemp(dir, "new-*-"+base)
	if err != nil {
		return "", err
	}
	_, err = f.Write(bin)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err == nil {
		err = os.Chmod(f.Name(), 0o755)
	}
	if err != nil {
		return "", errors.Join(err, os.Remove(f.Name()))
	}
	return f.Name(), nil
}

func loadUpdateState(path string) (updateState, error) {
	var st updateState
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return updateState{}, fmt.Errorf("%s: %w", path, err)
	}
	return st, nil
}

func saveUpdateState(path string, st updateState) error {
	if len(st.Old) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return install.WriteAtomic(path, append(data, '\n'), 0o644)
}

func recordOld(path, old string) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	st, err := loadUpdateState(path)
	if err != nil {
		return err
	}
	st.Old = append(st.Old, old)
	return saveUpdateState(path, st)
}

func unrecordOld(path, old string) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	st, err := loadUpdateState(path)
	if err != nil {
		return err
	}
	st.Old = slices.DeleteFunc(st.Old, func(have string) bool { return have == old })
	return saveUpdateState(path, st)
}

// rename and removeFile are replaced in tests to fail a step.
var (
	rename     = fsutil.Rename
	removeFile = os.Remove
)

// CleanupOld removes the old copies of the running binary that updates
// recorded under home's state, once nothing runs from them; it does nothing
// off Windows, where an update replaces the file in place. It deletes only a
// recorded path that lies in a directory of ours (oldDirPrefix) next to the
// running binary, under the binary's name, never a name it guesses. It is best
// effort: a copy still in use stays recorded for the next start.
func CleanupOld(home string) {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cleanupOld(StatePath(home), exe)
}

func cleanupOld(statePath, exe string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	st, err := loadUpdateState(statePath)
	if err != nil || len(st.Old) == 0 {
		return
	}
	var keep []string
	for _, old := range st.Old {
		if !leftBy(old, exe) {
			keep = append(keep, old)
			continue
		}
		if err := removeFile(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
			keep = append(keep, old)
			continue
		}
		_ = os.Remove(filepath.Dir(old))
	}
	if len(keep) != len(st.Old) {
		_ = saveUpdateState(statePath, updateState{Old: keep})
	}
}

// leftBy reports whether old is where an update of exe puts its old copy:
// <dir of exe>/<oldDirPrefix>*/<name of exe>, in a real directory.
func leftBy(old, exe string) bool {
	oldDir := filepath.Dir(old)
	info, err := os.Lstat(oldDir)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return false
	}
	return sameName(filepath.Dir(oldDir), filepath.Dir(exe)) &&
		strings.HasPrefix(filepath.Base(oldDir), oldDirPrefix) &&
		sameName(filepath.Base(old), filepath.Base(exe))
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
