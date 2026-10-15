package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

type codexSetting struct {
	path  []string
	value any
}

func codexSettingLeaves(values map[string]any, prefix []string) []codexSetting {
	var out []codexSetting
	for _, key := range sortedKeys(values) {
		path := append(slices.Clone(prefix), key)
		if nested, ok := values[key].(map[string]any); ok {
			out = append(out, codexSettingLeaves(nested, path)...)
		} else {
			out = append(out, codexSetting{path, values[key]})
		}
	}
	return out
}

func settingValue(values map[string]any, path []string) any {
	for _, key := range path[:len(path)-1] {
		next, ok := values[key].(map[string]any)
		if !ok {
			return nil
		}
		values = next
	}
	return values[path[len(path)-1]]
}

func encodeSetting(value any) (string, any, error) {
	data, err := toml.Marshal(map[string]any{"value": value})
	if err != nil {
		return "", nil, err
	}
	var normalized map[string]any
	if err := toml.Unmarshal(data, &normalized); err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(strings.TrimPrefix(string(data), "value = ")), normalized["value"], nil
}

func (m *Module) planCodexSettings() ([]engine.Change, error) {
	if m.Agents.Codex == nil || len(m.Agents.Codex.Settings) == 0 {
		return nil, nil
	}
	if !m.Agents.Names(config.ProviderCodex) {
		return nil, errors.New("agents.codex.settings requires explicit codex provider opt-in")
	}
	path := codexMCP{m}.path()
	data, err := m.readCodexSettings(path)
	if err != nil {
		return nil, err
	}
	var changes []engine.Change
	for _, setting := range codexSettingLeaves(m.Agents.Codex.Settings, nil) {
		next, from, to, err := editCodexSetting(data, setting)
		if err != nil {
			return nil, fmt.Errorf("codex settings: %w", err)
		}
		if bytes.Equal(data, next) {
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Update, Target: "codex setting " + strings.Join(setting.path, "."), From: from, To: to, Group: config.ProviderCodex,
			Apply: func(context.Context) error {
				return retryEdit(path, func() error {
					current, err := m.readCodexSettings(path)
					if err != nil {
						return err
					}
					next, _, _, err := editCodexSetting(current, setting)
					if err != nil {
						return err
					}
					if bytes.Equal(current, next) {
						return nil
					}
					if current == nil {
						return createBytes(path, next, 0o600)
					}
					return writeBytesIf(path, next, current)
				})
			},
		})
		data = next
	}
	return changes, nil
}

func (m *Module) readCodexSettings(path string) ([]byte, error) {
	if err := install.CheckParents(m.Paths, path); err != nil {
		return nil, err
	}
	if err := checkMCPWritable(path); err != nil {
		return nil, err
	}
	data, err := readIfExists(path)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err := toml.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, nil
}

// editCodexSetting replaces one expression value; verification rejects edits
// through inline tables or arrays of tables that cannot be isolated safely.
func editCodexSetting(data []byte, setting codexSetting) ([]byte, string, string, error) {
	var before map[string]any
	if err := toml.Unmarshal(data, &before); err != nil {
		return nil, "", "", err
	}
	text, want, err := encodeSetting(setting.value)
	if err != nil {
		return nil, "", "", err
	}
	old := settingValue(before, setting.path)
	if reflect.DeepEqual(old, want) {
		return data, "", text, nil
	}
	var parser unstable.Parser
	parser.Reset(data)
	var table []string
	var next []byte
	insert := len(data)
	relative := setting.path
	for parser.NextExpression() {
		node := parser.Expression()
		var key []string
		keyOffset := 0
		it := node.Key()
		for it.Next() {
			if len(key) == 0 {
				keyOffset = int(it.Node().Raw.Offset)
			}
			key = append(key, string(it.Node().Data))
		}
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = key
			headerStart := bytes.LastIndexByte(data[:keyOffset], '\n') + 1
			headerEnd := len(data)
			if end := bytes.IndexByte(data[keyOffset:], '\n'); end >= 0 {
				headerEnd = keyOffset + end + 1
			}
			if insert == len(data) && len(relative) == len(setting.path) {
				insert = headerStart
			}
			if node.Kind == unstable.Table && len(key) < len(setting.path) && slices.Equal(key, setting.path[:len(key)]) {
				relative = setting.path[len(key):]
				insert = headerEnd
			}
		case unstable.KeyValue:
			full := append(slices.Clone(table), key...)
			if slices.Equal(full, setting.path) {
				raw := parser.Raw(node.Raw)
				keys := node.Key()
				var end int
				for keys.Next() {
					k := keys.Node()
					end = int(k.Raw.Offset + k.Raw.Length)
				}
				start := end + bytes.IndexByte(data[end:], '=') + 1
				for start < len(data) && (data[start] == ' ' || data[start] == '\t') {
					start++
				}
				finish := int(node.Raw.Offset) + len(raw)
				next = append(slices.Clone(data[:start]), []byte(text)...)
				next = append(next, data[finish:]...)
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, "", "", err
	}
	if next == nil {
		keys := make([]string, len(relative))
		for i, k := range relative {
			keys[i] = tomlKey(k)
		}
		eol := lineEnding(data)
		addition := strings.Join(keys, ".") + " = " + text + eol
		if insert > 0 && data[insert-1] != '\n' {
			addition = eol + addition
		}
		next = append(slices.Clone(data[:insert]), []byte(addition)...)
		next = append(next, data[insert:]...)
	}
	var after map[string]any
	if err := toml.Unmarshal(next, &after); err != nil {
		return nil, "", "", fmt.Errorf("refusing unsafe setting edit: %w", err)
	}
	if !reflect.DeepEqual(settingValue(after, setting.path), want) {
		return nil, "", "", fmt.Errorf("cannot isolate setting %s; change its inline table manually", strings.Join(setting.path, "."))
	}
	// Removing the changed leaf makes every other value comparable.
	removeSetting(before, setting.path)
	removeSetting(after, setting.path)
	if !reflect.DeepEqual(before, after) {
		return nil, "", "", errors.New("refusing setting edit that changes unrelated values")
	}
	return next, fmt.Sprint(old), text, nil
}

func removeSetting(values map[string]any, path []string) {
	if len(path) == 1 {
		delete(values, path[0])
		return
	}
	nested, ok := values[path[0]].(map[string]any)
	if !ok {
		return
	}
	removeSetting(nested, path[1:])
	if len(nested) == 0 {
		delete(values, path[0])
	}
}
