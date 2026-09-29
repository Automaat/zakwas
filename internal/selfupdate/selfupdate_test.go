package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/selfupdate/selfupdatetest"
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
			u := &Updater{BaseURL: srv.URL + "/releases", Arch: "arm64"}
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

func TestFetchMissingRelease(t *testing.T) {
	srv := serve(t, release{})
	u := &Updater{BaseURL: srv.URL + "/releases", Arch: "arm64"}
	if _, err := u.Fetch(context.Background(), "9.9.9"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want a 404", err)
	}
}

func TestReplace(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "zakwas")
	if err := os.WriteFile(exe, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Replace(exe, []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(exe)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("stat = %v, %v", info, err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "new" {
		t.Errorf("content = %q", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
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
