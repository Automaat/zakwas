package mise

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/engine"
)

const fakeBinary = "#!/bin/sh\necho mise\n"

// releaseServer serves a mise release; sum overrides the published checksum.
func releaseServer(t *testing.T, sum string) (*httptest.Server, *int) {
	t.Helper()
	if sum == "" {
		h := sha256.Sum256([]byte(fakeBinary))
		sum = hex.EncodeToString(h[:])
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.URL.Path {
		case "/v" + Version + "/SHASUMS256.txt":
			_, _ = w.Write([]byte("0000  ./other\n" + sum + "  ./" + Asset() + "\n"))
		case "/v" + Version + "/" + Asset():
			_, _ = w.Write([]byte(fakeBinary))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestBootstrapInstallsMiseThenTools(t *testing.T) {
	m, fake := newModule(t)
	fake.Missing("mise")
	fake.OnOK("mise install --yes", "")
	srv, _ := releaseServer(t, "")
	m.ReleaseURL = srv.URL

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"+ mise@" + Version, "! mise install"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("planning without mise ran %v", fake.Lines())
	}

	if err := engine.Apply(context.Background(), func(string) {}, engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	bin := m.Paths.Dst(Bin)
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want 0755", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(bin); string(data) != fakeBinary {
		t.Errorf("content %q", data)
	}
	if !fake.Ran("mise install --yes") {
		t.Error("tools not installed")
	}
}

func TestBootstrapRejectsChecksumMismatch(t *testing.T) {
	m, fake := newModule(t)
	fake.Missing("mise")
	srv, _ := releaseServer(t, strings.Repeat("ab", 32))
	m.ReleaseURL = srv.URL

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = changes[0].Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("got %v, want a checksum error", err)
	}
	if _, err := os.Stat(m.Paths.Dst(Bin)); !os.IsNotExist(err) {
		t.Errorf("binary written despite mismatch: %v", err)
	}
}

// Brew runs first and may install mise from the Brewfile.
func TestBootstrapSkipsDownloadWhenBrewInstalledMise(t *testing.T) {
	m, fake := newModule(t)
	fake.Missing("mise")
	srv, hits := releaseServer(t, "")
	m.ReleaseURL = srv.URL

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fake.Install("mise")
	if err := changes[0].Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *hits != 0 {
		t.Errorf("downloaded %d files, want none", *hits)
	}
}

func TestChecksum(t *testing.T) {
	sums := []byte("aa  ./mise-a\nbb  ./mise-b\ncc  mise-c\n")
	for asset, want := range map[string]string{"mise-b": "bb", "mise-c": "cc"} {
		if got, err := checksum(sums, asset); err != nil || got != want {
			t.Errorf("%s: got %q, %v", asset, got, err)
		}
	}
	if _, err := checksum(sums, "mise"); err == nil {
		t.Error("prefix match accepted")
	}
}
