// Package selfupdate replaces the running zakwas binary with a GitHub
// release, verified against the release's checksums.txt.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

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
	Arch    string
	Client  *http.Client
}

func (u *Updater) base() string { return strings.TrimSuffix(cmp.Or(u.BaseURL, ReleasesURL), "/") }

func (u *Updater) client() *http.Client { return cmp.Or(u.Client, http.DefaultClient) }

// Asset is the release archive name for version on this machine's arch.
func (u *Updater) Asset(version string) string {
	return fmt.Sprintf("zakwas_%s_darwin_%s.tar.gz", version, cmp.Or(u.Arch, runtime.GOARCH))
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

// Extract returns the zakwas binary from a release archive.
func Extract(archive []byte) ([]byte, error) {
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

// Replace swaps exe for bin atomically: a temp file in the same directory,
// renamed over it, so a failed update leaves the old binary working.
func Replace(exe string, bin []byte) error {
	return install.WriteAtomic(exe, bin, 0o755)
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
		if strings.HasPrefix(path, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
