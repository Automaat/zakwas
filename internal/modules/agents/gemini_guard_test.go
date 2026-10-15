package agents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

func geminiCall(t testing.TB, command string) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(command); err != nil {
		t.Fatal(err)
	}
	return "{\x00\"command\":" + strings.TrimSuffix(buf.String(), "\n") + "\x00,\x00\"description\":\"d\"\x00}"
}

func geminiGuardMatcher(t testing.TB, words ...string) func(string) bool {
	t.Helper()
	re := regexp.MustCompile(`"command":"` + geminiGuardRegex(words))
	return func(command string) bool { return re.MatchString(geminiCall(t, command)) }
}

var nestedQuantifier = regexp.MustCompile(`\([^)]*[*+?{].*\)[*+?{]`)

func TestGeminiGuardRegexPassesGeminiSafetyCheck(t *testing.T) {
	for _, command := range config.GuardedCommands {
		regex := `"command":"` + geminiGuardRegex([]string{"zakwas", command})
		if len(regex) > 1500 {
			t.Errorf("%s regex is %d characters, Gemini rejects 2048 and the guard stays well under", command, len(regex))
		}
		if nestedQuantifier.MatchString(regex) {
			t.Errorf("%s regex has a quantified group, which Gemini rejects as unsafe", command)
		}
		if _, err := regexp.Compile(regex); err != nil {
			t.Errorf("%s regex: %v", command, err)
		}
	}
}

func TestGeminiGuardRegexTable(t *testing.T) {
	denied := geminiGuardMatcher(t, "zakwas", "apply")
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"zakwas apply", true},
		{"zakwas apply -y", true},
		{"zakwas -c zakwas.yaml apply", true},
		{"~/.local/bin/zakwas apply", true},
		{"/opt/homebrew/bin/zakwas -y apply", true},
		{`zakwas -c "x.yaml" apply`, true},
		{`zakwas -c "my dir/x.yaml" apply`, true},
		{`"/usr/local/bin/zakwas" apply`, true},
		{`"zakwas" apply`, true},
		{`"zakwas" -c "x y" apply`, true},
		{`/usr/local/bin/"zakwas" apply`, true},
		{`"/Users/me/my dir/zakwas" apply`, true},
		{`'/usr/local/bin/zakwas' apply`, true},
		{`zakwas -c "a b" -w "c d" apply -y`, true},
		{`zakwas "apply"`, true},
		{`zakwas -c "a" -c "b" -c "c" -c "d" apply`, true},
		{`zakwas -c "a" -c "b" -c "c" -c "d" -c "e" -c "f" apply`, true},
		{`'zakwas' apply`, true},
		{`'/opt/my dir/zakwas' apply`, true},
		{`zakwas 'apply'`, true},
		{"zakwas\tapply", true},
		{"zakwas \t apply", true},
		{"zakwas\t\tapply", true},
		{"zakwas -c\tx.yaml\tapply", true},
		{"zakwas  apply", true},
		{" zakwas apply", true},
		{"\tzakwas apply", true},
		{"ZAKWAS apply", true},
		{"Zakwas apply", true},
		{"/opt/ZAKWAS apply", true},
		{`zakwas -c "a \" b" apply`, true},
		{`zakwas -c 'a "b" c' apply`, true},
		{`zakwas -c "it's" apply`, true},
		{`zakwas -c 'x' "apply"`, true},
		{`zakwas -c x.yaml "apply"`, true},
		{`zakwas -y "apply"`, true},
		{`zakwas -y 'apply'`, true},
		{`zakwas -y "apply" -y`, true},
		{`zakwas -y "apply" 2>&1`, true},
		{`zakwas -y "apply";echo`, true},
		{`zakwas -y "apply"`, true},
		{`zakwas "-y" apply`, true},
		{`zakwas '-c' x.yaml apply`, true},
		{"zakwas -y apply;echo", true},
		{"zakwas apply|cat", true},
		{"zakwas apply>out", true},
		{"zakwas apply 2>&1", true},
		{"zakwas -c x 2>&1 apply", true},
		{"zakwas 2>&1 apply", true},
		{"zakwas >out apply", true},
		{"zakwas -- apply", true},
		{"zakwas -c=x.yaml apply", true},
		{"zakwas $FLAGS apply", true},
		{"zakwas $(echo -y) apply", true},
		{"zakwas apply\necho", true},
		{"zakwas plan", false},
		{`zakwas -c "x.yaml" plan`, false},
		{`"/usr/local/bin/zakwas" plan`, false},
		{`zakwas -c "my dir/x.yaml" check`, false},
		{"zakwas applyx", false},
		{"zakwas apply-x", false},
		{"notzakwas apply", false},
		{"zakwasx apply", false},
		{"echo zakwas apply", false},
		{`x"zakwas" apply`, false},
		{`"notzakwas" apply`, false},
		{`"zakwas apply"`, false},
		{`"zakwas" plan`, false},
		{`zakwas -c "apply notes/x.yaml" plan`, false},
		{`zakwas -c 'apply notes/x.yaml' plan`, false},
		{`zakwas -c "notes apply x" plan`, false},
		{`zakwas -c 'notes apply x' plan`, false},
		{`zakwas -m "apply" plan`, false},
		{`zakwas -m 'apply' plan`, false},
		{`zakwas -c "apply" check -y`, false},
		{`zakwas plan "x" apply`, false},
		{`"zakwas" plan apply`, false},
		{`zakwas propose "apply"`, false},
		{`zakwas propose 'apply'`, false},
		{"zakwas plan apply", false},
		{"zakwas plan | grep apply", false},
		{"zakwas check; echo apply", false},
		{`zakwas "plan" apply`, false},
		{"zakwas apply\"x\"", false},
		{`zakwas 'apply'x`, false},
		{"zakwas\nzakwas plan", false},
	} {
		if got := denied(tc.command); got != tc.want {
			t.Errorf("%q denied = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestGeminiGuardRegexAppliesToEveryGuardedWord(t *testing.T) {
	for _, word := range config.GuardedCommands {
		denied := geminiGuardMatcher(t, "zakwas", word)
		for _, command := range []string{
			"zakwas " + word, "zakwas -c x.yaml " + word, `zakwas -c "my dir/x.yaml" ` + word,
			`"zakwas" "` + word + `"`, "zakwas\t" + word, "/usr/local/bin/zakwas " + word + " 1",
		} {
			if !denied(command) {
				t.Errorf("%q is not denied", command)
			}
		}
		for _, command := range []string{"zakwas plan", "zakwas plan " + word, "zakwas " + word + "x", `zakwas -m "` + word + `" plan`} {
			if denied(command) {
				t.Errorf("%q is denied", command)
			}
		}
	}
}

func TestGeminiGuardRegexQuotedValueLimit(t *testing.T) {
	denied := geminiGuardMatcher(t, "zakwas", "apply")
	for n := 0; n <= geminiGuardQuotedValues+1; n++ {
		command := "zakwas" + strings.Repeat(` -c "a b"`, n) + " apply"
		want := n <= geminiGuardQuotedValues
		if got := denied(command); got != want {
			t.Errorf("%d quoted values: denied = %v, want %v", n, got, want)
		}
	}
}

type guardCase struct {
	command string
	danger  bool
	class   string
}

type guardItem struct {
	text        string
	valueIsWord bool
	quoted      int
}

func guardCases(rng *rand.Rand, n int) []guardCase {
	binaries := []struct {
		text string
		zak  bool
	}{
		{"zakwas", true},
		{"/usr/local/bin/zakwas", true},
		{"~/.local/bin/zakwas", true},
		{"./zakwas", true},
		{`"zakwas"`, true},
		{`'zakwas'`, true},
		{`"/usr/local/bin/zakwas"`, true},
		{`'/opt/my dir/zakwas'`, true},
		{`"/Users/me/my dir/zakwas"`, true},
		{`/usr/local/bin/"zakwas"`, true},
		{"ZAKWAS", true},
		{"Zakwas", true},
		{"/opt/ZAKWAS", true},
		{"notzakwas", false},
		{"zakwasx", false},
		{`"notzakwas"`, false},
		{`x"zakwas"`, false},
		{"zakwas.sh", false},
		{"/a/zakwas/b", false},
		{"echo zakwas", false},
	}
	seps := []string{" ", " ", " ", "  ", "\t", " \t", "\t\t"}
	items := []guardItem{
		{"-y", false, 0},
		{"--json", false, 0},
		{"-no-color", false, 0},
		{"--", false, 0},
		{"-c x.yaml", false, 0},
		{"-c=x.yaml", false, 0},
		{"--c x.yaml", false, 0},
		{"-only a,b", false, 0},
		{`-c "x.yaml"`, false, 1},
		{`-c "my dir/x.yaml"`, false, 1},
		{`-c 'a b/x.yaml'`, false, 1},
		{`-c "apply notes/x.yaml"`, false, 1},
		{`-c 'apply notes'`, false, 1},
		{`-c "notes apply x"`, false, 1},
		{`-c 'notes apply x'`, false, 1},
		{`-c "a \"b\" c"`, false, 1},
		{`-c "it's"`, false, 1},
		{`-c='a b'`, false, 1},
		{`-out "o p"`, false, 1},
		{`-m "apply"`, true, 1},
		{`-m 'apply'`, true, 1},
		{"-c apply", true, 0},
		{"2>&1", false, 0},
		{">out.log", false, 0},
	}
	guarded := []string{"apply", `"apply"`, "'apply'"}
	others := []string{"plan", "check", "propose", "proposals", `"plan"`, "applyx", `"apply plan"`, `"apply"x`, "'apply'x", "apply-x"}
	tails := []struct {
		text     string
		positive bool
	}{
		{"", false},
		{" -y", false},
		{" 2>&1", false},
		{"; echo x", false},
		{" | grep x", false},
		{" > out.txt", false},
		{" | grep apply", false},
		{"; echo apply", false},
		{" plan", true},
		{` x "y z"`, true},
		{" 1", true},
	}
	var cases []guardCase
	for range n {
		bin := binaries[rng.IntN(len(binaries))]
		sep := seps[rng.IntN(len(seps))]
		var pre []guardItem
		for range rng.IntN(10) {
			pre = append(pre, items[rng.IntN(len(items))])
		}
		quoted := 0
		parts := []string{bin.text}
		for _, it := range pre {
			parts = append(parts, it.text)
			quoted += it.quoted
		}
		isGuarded := rng.IntN(2) == 0
		var sub string
		if isGuarded {
			sub = guarded[rng.IntN(len(guarded))]
		} else {
			sub = others[rng.IntN(len(others))]
		}
		tail := tails[rng.IntN(len(tails))]
		parts = append(parts, sub)
		command := strings.Join(parts, sep) + tail.text
		danger := bin.zak && isGuarded && (len(pre) == 0 || !tail.positive)
		cases = append(cases, guardCase{command: command, danger: danger, class: classifyGuardCase(bin.zak, danger, pre, isGuarded, tail.text, quoted)})
	}
	return cases
}

func classifyGuardCase(zak, danger bool, pre []guardItem, isGuarded bool, tail string, quoted int) string {
	switch {
	case !zak:
		return "other-binary"
	case len(pre) == 0:
		return "no-flags"
	case isGuarded && !danger:
		return "guarded-word-then-argument"
	case isGuarded && quoted > geminiGuardQuotedValues:
		return "too-many-quoted-values"
	case isGuarded:
		return "flags-then-guarded"
	}
	escaped, inside := false, false
	for _, it := range pre {
		if it.valueIsWord {
			return "flag-value-is-the-guarded-word"
		}
		escaped = escaped || strings.Contains(it.text, `\"`)
		inside = inside || strings.Contains(it.text, "apply")
	}
	if escaped && inside {
		return "escaped-quote-shifts-pairing"
	}
	if strings.Contains(tail, "apply") {
		return "flags-then-command-then-guarded-word"
	}
	return "flags-then-other-command"
}

func TestGeminiGuardRegexDifferential(t *testing.T) {
	denied := geminiGuardMatcher(t, "zakwas", "apply")
	rng := rand.New(rand.NewPCG(1, 2))
	counts := map[string]int{}
	for _, c := range guardCases(rng, 60000) {
		got := denied(c.command)
		var kind string
		switch {
		case c.danger && got:
			kind = "denied"
		case !c.danger && !got:
			kind = "allowed"
		case c.danger:
			kind = "gap"
		default:
			kind = "false-denial"
		}
		counts[kind+"/"+c.class]++
		switch kind {
		case "gap":
			if c.class != "too-many-quoted-values" {
				t.Errorf("gap: %q is not denied (%s)", c.command, c.class)
			}
		case "false-denial":
			switch c.class {
			case "flag-value-is-the-guarded-word", "flags-then-command-then-guarded-word", "guarded-word-then-argument", "escaped-quote-shifts-pairing":
			default:
				t.Errorf("false denial: %q is denied (%s)", c.command, c.class)
			}
		}
	}
	for _, kind := range []string{"denied/flags-then-guarded", "denied/no-flags", "allowed/flags-then-other-command", "allowed/other-binary"} {
		if counts[kind] == 0 {
			t.Errorf("the corpus has no %s case: %v", kind, fmt.Sprint(counts))
		}
	}
}

func TestGeminiGuardRegexDocumentedLimits(t *testing.T) {
	denied := geminiGuardMatcher(t, "zakwas", "apply")
	for _, command := range []string{
		"env zakwas apply", "sudo zakwas apply", "command zakwas apply", "exec zakwas apply", "nohup zakwas apply",
		"time zakwas apply", "xargs zakwas apply", "FOO=1 zakwas apply", `bash -lc "zakwas apply"`, `eval "zakwas apply"`,
		`\zakwas apply`, `z""akwas apply`, `zakwas ap""ply`, ">/dev/null zakwas apply", "${ZAKWAS:-zakwas} apply",
		"zakwas $SUBCOMMAND", "zakwas\t\t\tapply", `zakwas -c a\"b apply`, "zakwas -y \\\napply",
		"zakwas" + strings.Repeat(` -c "a b"`, geminiGuardQuotedValues+1) + " apply",
		"zakwas -c \"a\" -m 'b' -m \"c\" -m 'd' -m \"e\" -m 'f' -m \"g\" apply",
	} {
		if denied(command) {
			t.Errorf("%q is denied, so the limit in docs/config.md is out of date", command)
		}
	}
}
