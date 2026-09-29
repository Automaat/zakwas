// Package defaults converges macOS preferences via the `defaults` CLI.
package defaults

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

// Processes that cache these domains and only pick up changes on restart.
var restartByDomain = map[string]string{
	"com.apple.dock":          "Dock",
	"com.apple.finder":        "Finder",
	"com.apple.screencapture": "SystemUIServer",
}

// ActivateSettings applies changed NSGlobalDomain preferences (keyboard,
// trackpad, ...) to the running session, which otherwise needs a re-login.
// It is skipped when missing on this macOS.
var ActivateSettings = "/System/Library/PrivateFrameworks/SystemAdministration.framework/Resources/activateSettings"

const globalDomain = "NSGlobalDomain"

type Module struct {
	Defaults []config.Default
	Runner   runner.Runner
}

func (m *Module) Name() string { return "defaults" }

func (m *Module) Plan(ctx context.Context) ([]engine.Change, error) {
	var changes []engine.Change
	var restarts []string
	seen := map[string]bool{}
	activate := false
	for _, d := range m.Defaults {
		want, err := Encode(d.Value)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", d.Domain, d.Key, err)
		}
		have, ok, err := m.read(ctx, d)
		if err != nil {
			return nil, err
		}
		if ok && have == want {
			continue
		}
		from := "unset"
		if ok {
			from = have.String()
		}
		target := d.Domain + " " + d.Key
		if d.CurrentHost {
			target = "-currentHost " + target
		}
		changes = append(changes, engine.Change{
			Action: engine.Update,
			Target: target,
			From:   from,
			To:     want.String(),
			Apply:  m.write(d, want),
		})
		if p := restartFor(d); p != "" && !seen[p] {
			seen[p] = true
			restarts = append(restarts, p)
		}
		activate = activate || d.Domain == globalDomain
	}
	for _, p := range restarts {
		changes = append(changes, m.bestEffort("killall", p))
	}
	if _, err := os.Stat(ActivateSettings); activate && err == nil {
		changes = append(changes, m.bestEffort(ActivateSettings, "-u"))
	}
	return changes, nil
}

// args prefixes -currentHost, which defaults only accepts before the verb.
func args(d config.Default, verb string, rest ...string) []string {
	var a []string
	if d.CurrentHost {
		a = append(a, "-currentHost")
	}
	return append(append(a, verb, d.Domain, d.Key), rest...)
}

// bestEffort ignores the exit code: a process killall finds not running
// reads the new value on its next launch anyway, and activateSettings only
// saves a re-login.
func (m *Module) bestEffort(name string, arg string) engine.Change {
	cmd := runner.Cmd{Name: name, Args: []string{arg}}
	return engine.Change{Action: engine.Run, Target: cmd.String(), Apply: func(ctx context.Context) error {
		_, err := m.Runner.Run(ctx, cmd)
		return err
	}}
}

func restartFor(d config.Default) string {
	if d.Restart != "" {
		return d.Restart
	}
	return restartByDomain[d.Domain]
}

// Value is a typed defaults value in the CLI's textual form.
type Value struct {
	Type string
	Text string
}

func (v Value) String() string { return v.Type + ":" + v.Text }

// Encode converts a YAML value into the form `defaults read` prints, so reads
// and desired values compare directly.
func Encode(v any) (Value, error) {
	switch x := v.(type) {
	case bool:
		if x {
			return Value{"bool", "1"}, nil
		}
		return Value{"bool", "0"}, nil
	case int:
		return Value{"int", strconv.Itoa(x)}, nil
	case float64:
		return Value{"float", strconv.FormatFloat(x, 'f', -1, 64)}, nil
	case string:
		return Value{"string", x}, nil
	}
	return Value{}, fmt.Errorf("unsupported value %#v", v)
}

var typeNames = map[string]string{
	"boolean": "bool",
	"integer": "int",
	"float":   "float",
	"string":  "string",
}

// read returns the current value; ok is false when the key is unset.
// Values of types we don't manage (arrays, dicts, data) are reported with
// their raw type so they always differ from the desired value.
func (m *Module) read(ctx context.Context, d config.Default) (Value, bool, error) {
	res, err := m.Runner.Run(ctx, runner.Cmd{Name: "defaults", Args: args(d, "read-type")})
	if err != nil {
		return Value{}, false, err
	}
	if res.ExitCode != 0 {
		return Value{}, false, nil
	}
	rawType := strings.TrimPrefix(strings.TrimSpace(res.Stdout), "Type is ")
	typ, known := typeNames[rawType]
	if !known {
		return Value{Type: rawType}, true, nil
	}
	out, err := runner.Output(ctx, m.Runner, runner.Cmd{Name: "defaults", Args: args(d, "read")})
	if err != nil {
		return Value{}, false, err
	}
	text := strings.TrimSuffix(out, "\n")
	if typ == "float" {
		if f, err := strconv.ParseFloat(text, 64); err == nil {
			text = strconv.FormatFloat(f, 'f', -1, 64)
		}
	}
	return Value{Type: typ, Text: text}, true, nil
}

func (m *Module) write(d config.Default, v Value) func(context.Context) error {
	text := v.Text
	if v.Type == "bool" {
		text = map[string]string{"1": "true", "0": "false"}[text]
	}
	cmd := runner.Cmd{Name: "defaults", Args: args(d, "write", "-"+v.Type, text)}
	return func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) }
}
