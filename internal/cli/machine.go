package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/Automaat/zakwas/internal/engine"
)

// planDoc is the JSON form of a plan: printed by `plan --json`, saved by
// `plan -out`, and the first event of `apply --json`.
type planDoc struct {
	FormatVersion string              `json:"format_version"`
	ZakwasVersion string              `json:"zakwas_version"`
	Host          string              `json:"host"`
	Config        string              `json:"config"`
	Commit        string              `json:"commit,omitempty"`
	Only          []string            `json:"only,omitempty"`
	Modules       []engine.JSONModule `json:"modules"`
	Summary       engine.JSONSummary  `json:"summary"`
}

func newPlanDoc(env Env, config, commit string, only []string, p engine.Plan, withDiff bool) planDoc {
	return planDoc{
		FormatVersion: engine.FormatVersion,
		ZakwasVersion: env.Version,
		Host:          env.Host,
		Config:        config,
		Commit:        commit,
		Only:          only,
		Modules:       p.JSONModules(withDiff),
		Summary:       p.JSONSummary(),
	}
}

func writeJSON(c *console, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		c.err = errors.Join(c.err, err)
		return
	}
	c.print(string(data) + "\n")
}

func savePlan(path string, doc planDoc) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func loadPlan(path string) (planDoc, error) {
	var doc planDoc
	data, err := os.ReadFile(path)
	if err != nil {
		return doc, err
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return doc, fmt.Errorf("%s: %w", path, err)
	}
	if doc.FormatVersion != engine.FormatVersion {
		return doc, fmt.Errorf("%s: format_version %q, this zakwas reads %q", path, doc.FormatVersion, engine.FormatVersion)
	}
	return doc, nil
}

// errStalePlan means the machine or config changed after the plan was saved,
// so applying now would do something nobody reviewed.
var errStalePlan = errors.New("saved plan is stale: the machine or config changed since it was made; run `zakwas plan -out` again")

func checkSaved(saved, now planDoc) error {
	if saved.Host != now.Host {
		return fmt.Errorf("saved plan was made on %q, this is %q", saved.Host, now.Host)
	}
	if saved.Config != now.Config || !reflect.DeepEqual(saved.Modules, now.Modules) {
		return errStalePlan
	}
	return nil
}

// event is one line of `apply --json`.
type event struct {
	Type          string             `json:"type"`
	FormatVersion string             `json:"format_version,omitempty"`
	Plan          *planDoc           `json:"plan,omitempty"`
	Step          int                `json:"step,omitempty"`
	Total         int                `json:"total,omitempty"`
	Change        *engine.JSONChange `json:"change,omitempty"`
	DurationMS    int64              `json:"duration_ms,omitempty"`
	Error         string             `json:"error,omitempty"`
	Result        *resultJSON        `json:"result,omitempty"`
}

type resultJSON struct {
	Applied    int   `json:"applied"`
	Failed     int   `json:"failed"`
	Skipped    int   `json:"skipped"`
	DurationMS int64 `json:"duration_ms"`
}

func newResultJSON(r engine.Result) *resultJSON {
	return &resultJSON{Applied: r.Applied, Failed: r.Failed, Skipped: r.Skipped, DurationMS: r.Duration.Milliseconds()}
}

type jsonEvents struct{ out *console }

func stepChange(s engine.Step) *engine.JSONChange {
	c := s.Change.JSON(false)
	c.Module = s.Module
	return &c
}

func (j jsonEvents) Start(s engine.Step) {
	writeJSON(j.out, event{Type: "step_start", Step: s.N, Total: s.Total, Change: stepChange(s)})
}

func (j jsonEvents) Done(s engine.Step, d time.Duration, err error) {
	e := event{Type: "step_done", Step: s.N, Total: s.Total, Change: stepChange(s), DurationMS: d.Milliseconds()}
	if err != nil {
		e.Error = err.Error()
	}
	writeJSON(j.out, e)
}
