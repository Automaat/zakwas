package mise

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"

	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/runner"
)

// Version is the mise release installed when mise is missing.
// renovate: datasource=github-releases depName=jdx/mise
const Version = "2026.9.16"

const releaseURL = "https://github.com/jdx/mise/releases/download"

// Bin is where a bootstrapped mise goes: the location mise's own installer
// uses, so `mise self-update` and docs keep working.
const Bin = "~/.local/bin/mise"

// Asset is the release file name of the mise binary for this machine.
func Asset() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return fmt.Sprintf("mise-v%s-macos-%s", Version, arch)
}

// planBootstrap installs mise, then every tool in the config. Without mise
// there is nothing to list or prune yet; the next plan does that.
func (m *Module) planBootstrap() []engine.Change {
	install := m.cmd("install", "--yes")
	install.Stream = true
	return []engine.Change{
		{Action: engine.Create, Target: "mise@" + Version, Detail: Bin, Apply: m.installMise},
		{
			Action: engine.Run, Target: "mise install", Detail: "every tool in " + m.Mise.Config, Streams: true,
			Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, install) },
		},
	}
}

// installMise skips the download when an earlier module (brew) installed
// mise during this apply.
func (m *Module) installMise(ctx context.Context) error {
	if m.Runner.Installed("mise") {
		return nil
	}
	base := cmp.Or(m.ReleaseURL, releaseURL) + "/v" + Version + "/"
	sums, err := download(ctx, base+"SHASUMS256.txt")
	if err != nil {
		return err
	}
	want, err := checksum(sums, Asset())
	if err != nil {
		return err
	}
	bin, err := download(ctx, base+Asset())
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%s: sha256 %s, want %s", Asset(), got, want)
	}
	return install.WriteAtomic(m.Paths.Dst(Bin), bin, 0o755)
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	err = errors.Join(err, resp.Body.Close())
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return body, err
}

// checksum finds asset in a `sha256sum` listing ("<hash>  ./<asset>").
func checksum(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		hash, name, ok := strings.Cut(sc.Text(), "  ")
		if ok && strings.TrimPrefix(name, "./") == asset {
			return hash, nil
		}
	}
	return "", fmt.Errorf("no checksum for %s", asset)
}
