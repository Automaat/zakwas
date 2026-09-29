package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanJSON(t *testing.T) {
	env, _ := setup(t, linksOnly)
	env.Version, env.Host = "1.2.3", "mac"
	r := invoke(env, "", "plan", "--json")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	var doc planDoc
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, r.stdout)
	}
	root, err := filepath.EvalSymlinks(env.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	if doc.FormatVersion != "1" || doc.ZakwasVersion != "1.2.3" || doc.Host != "mac" ||
		doc.Config != filepath.Join(root, "zakwas.yaml") {
		t.Errorf("header = %+v", doc)
	}
	var links *struct{ status, action, target string }
	for _, m := range doc.Modules {
		if m.Name == "links" && len(m.Changes) == 1 {
			links = &struct{ status, action, target string }{m.Status, m.Changes[0].Action, m.Changes[0].Target}
		}
	}
	if links == nil || links.status != "changes" || links.action != "create" || links.target != "~/.zshrc" {
		t.Errorf("links module = %+v in %s", links, r.stdout)
	}
	if doc.Summary.Create != 1 || doc.Summary.Steps != 1 {
		t.Errorf("summary = %+v", doc.Summary)
	}
	if strings.Contains(r.stdout, `"diff"`) {
		t.Error("diffs included without --diff")
	}
}

func TestCheckJSONKeepsExitCodes(t *testing.T) {
	env, _ := setup(t, linksOnly)
	if r := invoke(env, "", "check", "--json"); r.code != ExitDrift || !json.Valid([]byte(r.stdout)) {
		t.Errorf("%+v", r)
	}
}

func TestApplyJSONEvents(t *testing.T) {
	env, _ := setup(t, linksOnly)
	r := invoke(env, "", "apply", "--json", "-y")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	var types []string
	sc := bufio.NewScanner(strings.NewReader(r.stdout))
	for sc.Scan() {
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		types = append(types, e.Type)
		if e.Type == "step_done" && (e.Change == nil || e.Change.Module != "links" || e.Step != 1 || e.Total != 1) {
			t.Errorf("step_done = %+v", e)
		}
		if e.Type == "summary" && (e.Result == nil || e.Result.Applied != 1) {
			t.Errorf("summary = %+v", e)
		}
	}
	if got := strings.Join(types, ","); got != "plan,step_start,step_done,summary" {
		t.Errorf("events %s", got)
	}
}

func TestFlagConflicts(t *testing.T) {
	for _, args := range [][]string{
		{"apply", "--json"},
		{"check", "-out", "p.json"},
		{"plan", "-plan", "p.json"},
		{"apply", "-plan", "p.json", "--only", "links"},
	} {
		env, _ := setup(t, linksOnly)
		if r := invoke(env, "", args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, r.code, ExitUsage)
		}
	}
}

func TestSavedPlan(t *testing.T) {
	env, home := setup(t, linksOnly)
	env.Host = "mac"
	saved := filepath.Join(t.TempDir(), "plan.json")
	if r := invoke(env, "", "plan", "-out", saved); r.code != ExitOK || !strings.Contains(r.stdout, "Plan: 1 to add") {
		t.Fatalf("%+v", r)
	}

	other := env
	other.Host = "other"
	if r := invoke(other, "", "apply", "-plan", saved); r.code != ExitErr || !strings.Contains(r.stderr, `made on "mac"`) {
		t.Errorf("other host: %+v", r)
	}

	if r := invoke(env, "", "apply", "-plan", saved); r.code != ExitOK {
		t.Fatalf("apply saved plan without prompting: %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(home, ".zshrc")); err != nil {
		t.Fatal(err)
	}
	if r := invoke(env, "", "apply", "-plan", saved); r.code != ExitErr || !strings.Contains(r.stderr, "stale") {
		t.Errorf("reapplying an outdated plan: %+v", r)
	}
}

func TestSavedPlanKeepsOnly(t *testing.T) {
	env, _ := setup(t, linksOnly)
	saved := filepath.Join(t.TempDir(), "plan.json")
	if r := invoke(env, "", "plan", "--only", "links", "-out", saved); r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	r := invoke(env, "", "apply", "-plan", saved)
	if r.code != ExitOK || strings.Contains(r.stdout, "up to date") {
		t.Errorf("apply must plan only the saved modules: %+v", r)
	}
}
