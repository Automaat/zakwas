package config

import (
	"errors"
	"regexp"

	"go.yaml.in/yaml/v3"
)

var yamlEchoes = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?s)^(line \d+: )?field .* not found in type (\S+)$`), "${1}unknown field in type ${2}"},
	{regexp.MustCompile(`(?s)^(line \d+: )?mapping key .* already defined at line (\d+)$`), "${1}duplicate mapping key; the first one is at line ${2}"},
	{regexp.MustCompile(`(?s)^(yaml: )unknown anchor '.*' referenced$`), "${1}unknown anchor referenced"},
	{regexp.MustCompile(`(?s)^(yaml: )anchor '.*' value contains itself$`), "${1}an anchor's value contains itself"},
	{regexp.MustCompile(`(?s)^(yaml: )invalid map key: .*$`), "${1}invalid map key"},
}

// YAMLError returns err without the key and anchor names the YAML library
// repeats in its messages: a secret pasted as a key must not reach the
// terminal, a log or an agent transcript. Line numbers stay.
func YAMLError(err error) error {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		out := &yaml.TypeError{Errors: make([]string, len(te.Errors))}
		for i, m := range te.Errors {
			out.Errors[i] = redactYAML(m)
		}
		return out
	}
	if m := redactYAML(err.Error()); m != err.Error() {
		return errors.New(m)
	}
	return err
}

func redactYAML(msg string) string {
	for _, e := range yamlEchoes {
		if e.re.MatchString(msg) {
			return e.re.ReplaceAllString(msg, e.repl)
		}
	}
	return msg
}
