package agents

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/Automaat/zakwas/internal/config"
)

// tomlHeader is one table header line: the key path of [a.b] or [[a.b]].
// Tables are edited as lines, never re-encoded, so the comments and layout
// of everything zakwas doesn't touch stay byte-identical.
type tomlHeader struct {
	line int
	path []string
}

// tomlHeaders lists the table headers of a file in order. It tracks
// multi-line strings and arrays so a line inside a value that looks like a
// header is not mistaken for one.
func tomlHeaders(lines []string) []tomlHeader {
	var out []tomlHeader
	multi := ""
	depth := 0
	for i, line := range lines {
		rest := line
		if multi != "" {
			end, ok := closeMultiline(rest, multi)
			if !ok {
				continue
			}
			rest = rest[end:]
		} else if depth == 0 {
			trimmed := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(trimmed, "[") {
				if path, ok := parseTOMLHeader(trimmed); ok {
					out = append(out, tomlHeader{line: i, path: path})
				}
				continue
			}
		}
		multi, depth = scanTOMLValue(rest, depth)
	}
	return out
}

// closeMultiline finds the end of a multi-line string opened on an earlier
// line; end is the index just past the closing delimiter, which may be
// preceded by up to two quotes that belong to the string.
func closeMultiline(line, delim string) (end int, ok bool) {
	for i := 0; i < len(line); i++ {
		if delim == `"""` && line[i] == '\\' {
			i++
			continue
		}
		if strings.HasPrefix(line[i:], delim) {
			end = i + 3
			for end < len(line) && line[end] == delim[0] && end < i+5 {
				end++
			}
			return end, true
		}
	}
	return 0, false
}

// scanTOMLValue walks the rest of a line, tracking bracket depth outside
// strings, and reports the delimiter of a multi-line string it opens.
func scanTOMLValue(line string, depth int) (multi string, nextDepth int) {
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '#':
			return "", depth
		case strings.HasPrefix(line[i:], `"""`), strings.HasPrefix(line[i:], `'''`):
			delim := line[i : i+3]
			end, ok := closeMultiline(line[i+3:], delim)
			if !ok {
				return delim, depth
			}
			i += 3 + end - 1
		case c == '"':
			for i++; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' {
					i++
				}
			}
		case c == '\'':
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return "", depth
			}
			i += end + 1
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return "", depth
}

// parseTOMLHeader parses "[a.b]" or "[[a.b]]" followed by an optional
// comment.
func parseTOMLHeader(line string) ([]string, bool) {
	rest := strings.TrimPrefix(strings.TrimPrefix(line, "["), "[")
	var path []string
	for {
		rest = strings.TrimLeft(rest, " \t")
		var key string
		switch {
		case rest == "":
			return nil, false
		case rest[0] == '"':
			end := 1
			for end < len(rest) && rest[end] != '"' {
				if rest[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(rest) {
				return nil, false
			}
			key = unescapeTOML(rest[1:end])
			rest = rest[end+1:]
		case rest[0] == '\'':
			end := strings.IndexByte(rest[1:], '\'')
			if end < 0 {
				return nil, false
			}
			key = rest[1 : 1+end]
			rest = rest[end+2:]
		default:
			end := 0
			for end < len(rest) && isBareKeyByte(rest[end]) {
				end++
			}
			if end == 0 {
				return nil, false
			}
			key, rest = rest[:end], rest[end:]
		}
		path = append(path, key)
		rest = strings.TrimLeft(rest, " \t")
		switch {
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
		case strings.HasPrefix(rest, "]"):
			return path, true
		default:
			return nil, false
		}
	}
}

func isBareKeyByte(c byte) bool {
	return c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func unescapeTOML(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\t`, "\t", `\n`, "\n").Replace(s)
}

// serverRegions returns the line ranges [start, end) of the tables that
// make up mcp_servers.<name>: its own and its subtables (env, ...). A range
// runs from the header to its last line of content: every comment and
// blank line after that stays, since it may belong to what follows.
func serverRegions(lines []string, name string) [][2]int {
	headers := tomlHeaders(lines)
	var out [][2]int
	for i, h := range headers {
		if len(h.path) < 2 || h.path[0] != "mcp_servers" || h.path[1] != name {
			continue
		}
		end := len(lines)
		if i+1 < len(headers) {
			end = headers[i+1].line
		}
		for end-1 > h.line && (isTOMLComment(lines[end-1]) || strings.TrimSpace(lines[end-1]) == "") {
			end--
		}
		out = append(out, [2]int{h.line, end})
	}
	return out
}

func isTOMLComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

// splitLines splits data into lines without their terminating newline; a
// carriage return of a CRLF file stays at the end of its line.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// joinLines is splitLines's inverse; finalNewline says whether the file
// ended with a newline.
func joinLines(lines []string, finalNewline bool) []byte {
	if len(lines) == 0 {
		return nil
	}
	if !finalNewline {
		lines = slices.Clone(lines)
		lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], "\r")
		return []byte(strings.Join(lines, "\n"))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func endsWithNewline(data []byte) bool { return len(data) == 0 || data[len(data)-1] == '\n' }

// lineEnding is the file's dominant line ending, "\n" for an empty one.
func lineEnding(data []byte) string {
	if crlf := bytes.Count(data, []byte("\r\n")); crlf > 0 && crlf*2 > bytes.Count(data, []byte("\n")) {
		return "\r\n"
	}
	return "\n"
}

// replaceServer puts text in place of the tables of mcp_servers.<name>: at
// the first one, dropping the others and the blank lines before them. It
// reports false when the file has no such table. Everything outside the
// replaced lines is kept as is.
func replaceServer(data []byte, name, text string) ([]byte, bool) {
	lines := splitLines(data)
	regions := serverRegions(lines, name)
	if len(regions) == 0 {
		return nil, false
	}
	drop := make([]bool, len(lines))
	for i, r := range regions {
		for j := r[0]; j < r[1]; j++ {
			drop[j] = true
		}
		for j := r[0] - 1; i > 0 && j >= 0 && strings.TrimSpace(lines[j]) == ""; j-- {
			drop[j] = true
		}
	}
	eol := lineEnding(data)
	var out []string
	for i, l := range lines {
		if i == regions[0][0] {
			for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
				out = append(out, line+strings.TrimSuffix(eol, "\n"))
			}
		}
		if !drop[i] {
			out = append(out, l)
		}
	}
	return joinLines(out, endsWithNewline(data)), true
}

// dropRegions marks the lines of regions, with the blank lines after each,
// and, for one that leaves nothing after it, the blank lines before it.
func dropRegions(lines []string, regions [][2]int) []bool {
	drop := make([]bool, len(lines))
	for _, r := range regions {
		for i := r[0]; i < r[1]; i++ {
			drop[i] = true
		}
		for i := r[1]; i < len(lines) && strings.TrimSpace(lines[i]) == ""; i++ {
			drop[i] = true
		}
	}
	for _, r := range regions {
		atEnd := !slices.Contains(drop[r[0]:], false)
		for i := r[0] - 1; atEnd && i >= 0 && strings.TrimSpace(lines[i]) == ""; i-- {
			drop[i] = true
		}
	}
	return drop
}

// removeServer deletes the tables of mcp_servers.<name> and the blank lines
// around them, so removing a server zakwas appended restores the file.
func removeServer(data []byte, name string) ([]byte, bool) {
	lines := splitLines(data)
	regions := serverRegions(lines, name)
	if len(regions) == 0 {
		return nil, false
	}
	drop := dropRegions(lines, regions)
	var out []string
	for i, l := range lines {
		if !drop[i] {
			out = append(out, l)
		}
	}
	return joinLines(out, endsWithNewline(data)), true
}

// appendServer adds text after the existing content, separated by a blank
// line, with the file's line endings.
func appendServer(data []byte, text string) []byte {
	eol := lineEnding(data)
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 {
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteString(eol)
		}
		if !strings.HasSuffix(b.String(), "\n\n") && !strings.HasSuffix(b.String(), "\n\r\n") {
			b.WriteString(eol)
		}
	}
	b.WriteString(strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", eol))
	b.WriteString(eol)
	return []byte(b.String())
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlKey(k string) string {
	if k == "" {
		return tomlString(k)
	}
	for i := 0; i < len(k); i++ {
		if !isBareKeyByte(k[i]) {
			return tomlString(k)
		}
	}
	return k
}

func tomlStrings(list []string) string {
	quoted := make([]string, len(list))
	for i, s := range list {
		quoted[i] = tomlString(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// renderCodexMCP renders a declared server as the tables `codex mcp add`
// writes: the server, then its oauth client, env and header maps as
// subtables, then a table per tool that must always prompt.
func renderCodexMCP(name string, s config.MCPServer) string {
	var b strings.Builder
	header := "mcp_servers." + tomlKey(name)
	b.WriteString("[" + header + "]\n")
	if s.IsHTTP() {
		b.WriteString("url = " + tomlString(s.URL) + "\n")
		if s.BearerTokenEnv != "" {
			b.WriteString("bearer_token_env_var = " + tomlString(s.BearerTokenEnv) + "\n")
		}
		if scopes := s.OAuth.Scopes(); len(scopes) > 0 && s.OAuth.HasClient() {
			b.WriteString("scopes = " + tomlStrings(scopes) + "\n")
		}
	} else {
		b.WriteString("command = " + tomlString(s.Command) + "\n")
		if len(s.Args) > 0 {
			b.WriteString("args = " + tomlStrings(s.Args) + "\n")
		}
		if len(s.EnvVars) > 0 {
			b.WriteString("env_vars = " + tomlStrings(s.EnvVars) + "\n")
		}
	}
	table := func(sub string, values map[string]string) {
		if len(values) == 0 {
			return
		}
		b.WriteString("\n[" + header + "." + sub + "]\n")
		for _, k := range sortedKeys(values) {
			b.WriteString(tomlKey(k) + " = " + tomlString(values[k]) + "\n")
		}
	}
	if s.IsHTTP() && s.OAuth.HasClient() {
		b.WriteString("\n[" + header + ".oauth]\nclient_id = " + tomlString(s.OAuth.ClientID) + "\n")
		if s.OAuth.CallbackPort != 0 {
			b.WriteString("callback_port = " + strconv.Itoa(s.OAuth.CallbackPort) + "\n")
		}
	}
	table("env", s.Env)
	table("http_headers", s.Headers)
	table("env_http_headers", s.HeaderEnv)
	for _, tool := range s.PromptTools() {
		b.WriteString("\n[" + header + ".tools." + tomlKey(tool) + "]\napproval_mode = \"prompt\"\n")
	}
	return b.String()
}
