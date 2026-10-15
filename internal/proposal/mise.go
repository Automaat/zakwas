package proposal

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	toolsHeader = regexp.MustCompile(`^\s*\[tools\]\s*(#.*)?$`)
	anyHeader   = regexp.MustCompile(`^\s*\[`)
	bareKey     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// editMiseTools sets tool to version in the [tools] table by editing its
// line, or adds the line, keeping the rest of the file as it was. Values
// that aren't a plain string (an array, an inline table) are refused.
func editMiseTools(data []byte, tool, version string) ([]byte, error) {
	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	header := -1
	for i, l := range lines {
		if toolsHeader.MatchString(strings.TrimRight(l, "\r\n")) {
			header = i
			break
		}
	}
	if header < 0 {
		return appendTools(data, lines, eol, tool, version), nil
	}
	end := len(lines)
	for i := header + 1; i < len(lines); i++ {
		if anyHeader.MatchString(lines[i]) {
			end = i
			break
		}
	}
	keyRe := regexp.MustCompile(`^(\s*)(?:"` + regexp.QuoteMeta(tool) + `"|'` + regexp.QuoteMeta(tool) + `'|` + regexp.QuoteMeta(tool) + `)\s*=\s*(.*)$`)
	last, indent := header, ""
	for i := header + 1; i < end; i++ {
		body := strings.TrimRight(lines[i], "\r\n")
		if !blank(body) && !comment(body) {
			last = i
			if indent == "" {
				indent = body[:indentOf(body)]
			}
		}
		g := keyRe.FindStringSubmatch(body)
		if g == nil {
			continue
		}
		_, tail, err := splitString(g[2])
		if err != nil {
			return nil, fmt.Errorf("%s in [tools] isn't a plain version string (%w); edit it by hand", tool, err)
		}
		lines[i] = g[1] + tomlKey(tool) + " = " + `"` + version + `"` + tail + lines[i][len(body):]
		return []byte(strings.Join(lines, "")), nil
	}
	entry := indent + tomlKey(tool) + " = " + `"` + version + `"` + eol
	if last == len(lines)-1 && !strings.HasSuffix(lines[last], "\n") {
		lines[last] += eol
		entry = strings.TrimSuffix(entry, eol)
	}
	lines = append(lines[:last+1], append([]string{entry}, lines[last+1:]...)...)
	return []byte(strings.Join(lines, "")), nil
}

func appendTools(data []byte, lines []string, eol, tool, version string) []byte {
	out := string(data)
	if len(lines) > 0 {
		if !strings.HasSuffix(out, "\n") {
			out += eol
		}
		if !blank(lines[len(lines)-1]) {
			out += eol
		}
	}
	return []byte(out + "[tools]" + eol + tomlKey(tool) + " = " + `"` + version + `"` + eol)
}

func tomlKey(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return `"` + k + `"`
}

// splitString reads a TOML basic or literal string at the start of s and
// returns its content and what follows it, which must be blank or a comment.
func splitString(s string) (value, tail string, err error) {
	if s == "" {
		return "", "", errors.New("no value")
	}
	q := s[0]
	if q != '"' && q != '\'' {
		return "", "", errors.New("not a string")
	}
	for i := 1; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			i++
		case s[i] == q:
			rest := s[i+1:]
			if t := strings.TrimSpace(rest); t != "" && !strings.HasPrefix(t, "#") {
				return "", "", errors.New("not a string")
			}
			return s[1:i], rest, nil
		}
	}
	return "", "", errors.New("unterminated string")
}
