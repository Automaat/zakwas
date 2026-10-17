package proposal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/testutil"
)

func sample(id string, created time.Time) *Proposal {
	return &Proposal{
		FormatVersion: engine.FormatVersion, ID: id, Created: created, Config: "/repo/zakwas.yaml",
		Summary: "add plugin a@b", Ops: []Op{{Kind: AddPlugin, Name: "a@b"}}, Base: map[string]string{"zakwas.yaml": "abc"},
		Tier: TierLow, Modules: []string{"agents"},
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "proposals")}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range []*Proposal{sample("bbbb2222", now.Add(time.Hour)), sample("aaaa1111", now), sample("aaaa2222", now.Add(2*time.Hour))} {
		if err := s.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(s.Dir)
	if err != nil || !testutil.SamePerm(info.Mode(), 0o700) {
		t.Errorf("dir mode = %v, %v", info.Mode(), err)
	}
	all, err := s.List()
	if err != nil || len(all) != 3 || all[0].ID != "aaaa1111" || all[2].ID != "aaaa2222" {
		t.Fatalf("list = %v, %v", all, err)
	}

	got, err := s.Load("bbbb2222")
	if err != nil || got.Summary != "add plugin a@b" || got.Ops[0].Name != "a@b" {
		t.Fatalf("load = %+v, %v", got, err)
	}
	if got, err := s.Load("bbbb"); err != nil || got.ID != "bbbb2222" {
		t.Errorf("prefix load = %v, %v", got, err)
	}
	if _, err := s.Load("aaaa"); err == nil || !strings.Contains(err.Error(), "matches 2 proposals") {
		t.Errorf("ambiguous prefix: %v", err)
	}
	if _, err := s.Load("ffff"); err == nil || !strings.Contains(err.Error(), "no proposal") {
		t.Errorf("missing: %v", err)
	}
	for _, id := range []string{"../x", "AAAA", "abc", ""} {
		if _, err := s.Load(id); err == nil {
			t.Errorf("Load(%q) accepted", id)
		}
	}

	if err := s.Delete("bbbb2222"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("bbbb2222"); err != nil {
		t.Errorf("deleting twice: %v", err)
	}
	if all, _ := s.List(); len(all) != 2 {
		t.Errorf("after delete: %d", len(all))
	}
}

func TestStoreSkipsUnreadableFiles(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := s.Save(sample("aaaa1111", time.Now())); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"bbbb2222.json": "{",
		"cccc3333.json": `{"format_version": "99", "id": "cccc3333"}`,
		"dddd4444.json": `{"format_version": "1", "id": "eeee5555"}`,
		"notes.txt":     "x",
	} {
		if err := os.WriteFile(filepath.Join(s.Dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if all, err := s.List(); err != nil || len(all) != 1 {
		t.Errorf("list = %d, %v", len(all), err)
	}
}

func TestExpired(t *testing.T) {
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	p := sample("aaaa1111", created)
	if p.Expired(created.Add(MaxAge)) || !p.Expired(created.Add(MaxAge+time.Second)) {
		t.Error("a proposal expires after exactly 7 days")
	}
}
