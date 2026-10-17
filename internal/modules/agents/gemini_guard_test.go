package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

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

func geminiWindowsGuardMatcher(t testing.TB, words ...string) func(string) bool {
	t.Helper()
	re := regexp.MustCompile(`"command":"` + geminiGuardRegexFor(words, true))
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
		windows := `"command":"` + geminiGuardRegexFor([]string{"zakwas", command}, true)
		if len(windows) > 1700 {
			t.Errorf("%s regex for Windows is %d characters, Gemini rejects 2048", command, len(windows))
		}
		if nestedQuantifier.MatchString(windows) {
			t.Errorf("%s regex for Windows has a quantified group, which Gemini rejects as unsafe", command)
		}
		if _, err := regexp.Compile(windows); err != nil {
			t.Errorf("%s regex for Windows: %v", command, err)
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
		{"zakwas \t\tapply", true},
		{"zakwas\t\t apply", true},
		{"zakwas \t \t apply", true},
		{"zakwas  \t\t  \t apply", true},
		{"zakwas\t\t\t\tapply", true},
		{" \t\t zakwas apply", true},
		{"\t \t\t\tzakwas apply", true},
		{"zakwas -c x.yaml \t\tapply", true},
		{"zakwas -c x.yaml \"apply\" \t\t-y", true},
		{"zakwas -c x.yaml \"apply\"\t\t -y", true},
		{"zakwas -y \"apply\" \t\t", true},
		{"zakwas -y \"apply\" \nls", true},
		{"zakwas -y \"apply\" \n", true},
		{"zakwas -y 'apply' \t \t\t# why", true},
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
		{`zakwas -y "apply" `, true},
		{"zakwas -y \"apply\"\t", true},
		{"zakwas -y \"apply\"\t\t", true},
		{`zakwas -y "apply" # why`, true},
		{`zakwas -c x.yaml "upgrade" # why`, false},
		{`zakwas -c "a b" -y "apply" `, true},
		{`zakwas -y 'apply' `, true},
		{`zakwas -y 'apply' # why`, true},
		{`zakwas -m "apply" plan # why`, false},
		{`zakwas "apply" `, true},
		{"\t\tzakwas apply", true},
		{"\t\t zakwas apply", true},
		{`zakwas -c x.yaml "apply" "-y"`, true},
		{`zakwas -c x.yaml "apply" '-y'`, true},
		{`zakwas -c x.yaml "apply" $F`, true},
		{`zakwas -c x.yaml "apply" "$F"`, true},
		{"zakwas -y \"apply\" `echo -y`", true},
		{`zakwas -- "apply" "-y"`, true},
		{`zakwas -c x.yaml "apply" \-y`, true},
		{`zakwas -c it\'s apply`, true},
		{`zakwas -c $'a\'b' apply`, true},
		{`zakwas -c a\"b apply`, true},
		{`"/my dir/"zakwas apply`, true},
		{`"/a/"zakwas apply`, true},
		{"/a\u00a0b/zakwas apply", true},
		{"/a\u2003b/zakwas apply", true},
		{"/a\u2028b/zakwas apply", true},
		{`'/a/'zakwas apply`, true},
		{`"a b"/zakwas apply`, true},
		{`'a b'/zakwas apply`, true},
		{`/a\ b/zakwas apply`, true},
		{`/a\ b\ c/zakwas apply`, true},
		{`x/"a b"/y/zakwas apply`, true},
		{`"a b"x/zakwas -y "apply"`, true},
		{`/a\ b/"zakwas" apply`, true},
		{"zakwas plan", false},
		{`"a b"/zakwas plan`, false},
		{`/a\ b/zakwas plan`, false},
		{`"a b" /zakwas apply`, false},
		{`/a\ b /zakwas apply`, false},
		{`echo "a b"/zakwas apply`, false},
		{`echo /a\ b/zakwas apply`, false},
		{`zakwas -y "apply" x.yaml`, false},
		{`zakwas --json "apply" "y" status`, false},
		{`zakwas --json "apply" 'y' status`, false},
		{"zakwas -m \"apply\"\t\tplan", false},
		{"zakwas -m \"apply\" \t\tplan", false},
		{`zakwas -c "x.yaml" plan`, false},
		{`zakwas -c x.yaml "apply" 'y'`, false},
		{`zakwas -c x.yaml "apply" "plan"`, false},
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

func TestGeminiGuardRegexForOtherSystemsHasNoWindowsPieces(t *testing.T) {
	for _, word := range config.GuardedCommands {
		regex := geminiGuardRegexFor([]string{"zakwas", word}, false)
		if regex != geminiGuardRegex([]string{"zakwas", word}) {
			t.Errorf("%s: geminiGuardRegex is not the regex for other systems", word)
		}
		for _, piece := range []string{guardExe, guardCallOperator} {
			if strings.Contains(regex, piece) {
				t.Errorf("%s: the regex off Windows holds %q", word, piece)
			}
		}
	}
}

func TestGeminiGuardRegexTableOnWindows(t *testing.T) {
	denied := geminiWindowsGuardMatcher(t, "zakwas", "apply")
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"zakwas apply", true},
		{"zakwas.exe apply", true},
		{"ZAKWAS.EXE apply", true},
		{"Zakwas.Exe apply", true},
		{`.\zakwas.exe apply`, true},
		{`.\zakwas apply`, true},
		{`C:\Users\me\bin\zakwas.exe apply`, true},
		{`C:\Users\me\bin\zakwas apply`, true},
		{`c:\users\me\bin\ZAKWAS.EXE apply`, true},
		{`C:/Users/me/bin/zakwas.exe apply`, true},
		{`~/bin/zakwas.exe apply`, true},
		{`C:\Users\me\bin\zakwas.exe -c C:\repo\zakwas.yaml apply`, true},
		{`zakwas.exe -c "C:\my dir\zakwas.yaml" apply`, true},
		{`zakwas.exe -c 'C:\my dir\zakwas.yaml' -y apply`, true},
		{`zakwas.exe "apply"`, true},
		{`zakwas.exe 'apply'`, true},
		{"zakwas.exe\tapply", true},
		{`"zakwas.exe" apply`, true},
		{`'zakwas.exe' apply`, true},
		{`"zakwas" apply`, true},
		{`"C:\Program Files\zakwas\zakwas.exe" apply`, true},
		{`"C:\Program Files\zakwas\zakwas" apply`, true},
		{`'C:\Program Files\zakwas\zakwas.exe' apply`, true},
		{`"C:/Program Files/zakwas/zakwas.exe" apply`, true},
		{`& "C:\Program Files\zakwas\zakwas.exe" apply`, true},
		{`& 'C:\Program Files\zakwas\zakwas.exe' apply`, true},
		{`&"C:\Program Files\zakwas\zakwas.exe" apply`, true},
		{`& C:\bin\zakwas.exe apply`, true},
		{`& .\zakwas.exe -c x.yaml apply`, true},
		{`& zakwas apply`, true},
		{`&  zakwas.exe  apply`, true},
		{`  & zakwas.exe apply`, true},
		{`& "C:\Program Files\zakwas\zakwas.exe" -c "C:\my dir\x.yaml" apply`, true},
		{`C:\bin\zakwas.exe apply -y`, true},
		{`C:\bin\zakwas.exe apply;echo`, true},
		{`C:\bin\zakwas.exe 2>&1 apply`, true},
		{`zakwas.exe plan`, false},
		{`C:\bin\zakwas.exe plan`, false},
		{`& "C:\Program Files\zakwas\zakwas.exe" plan`, false},
		{`& "C:\Program Files\zakwas\zakwas.exe" check -c "apply notes.yaml"`, false},
		{`C:\bin\zakwas.exe -m "apply" plan`, false},
		{`zakwas.exe plan apply`, false},
		{`zakwas.exe applyx`, false},
		{`zakwas.exe apply-x`, false},
		{`zakwas.exex apply`, false},
		{`zakwas.ex apply`, false},
		{`zakwas.exe.bak apply`, false},
		{`notzakwas.exe apply`, false},
		{`C:\bin\notzakwas.exe apply`, false},
		{`C:\bin\zakwasx.exe apply`, false},
		{`echo zakwas.exe apply`, false},
		{`echo C:\bin\zakwas.exe apply`, false},
		{`Write-Host zakwas apply`, false},
		{`& echo zakwas apply`, false},
		{`"zakwas.exe apply"`, false},
		{`"C:\Program Files\zakwas\x\zakwas.exe plan"`, false},
	} {
		if got := denied(tc.command); got != tc.want {
			t.Errorf("%q denied = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestGeminiGuardRegexOffWindowsDoesNotNameExe(t *testing.T) {
	denied := geminiGuardMatcher(t, "zakwas", "apply")
	for _, command := range []string{`zakwas.exe apply`, `C:\bin\zakwas.exe apply`, `& zakwas apply`, `.\zakwas apply`} {
		if denied(command) {
			t.Errorf("%q is denied off Windows", command)
		}
	}
}

func TestGeminiGuardRegexOnWindowsKeepsEveryOtherSystemCase(t *testing.T) {
	linux := geminiGuardMatcher(t, "zakwas", "apply")
	windows := geminiWindowsGuardMatcher(t, "zakwas", "apply")
	rng := rand.New(rand.NewPCG(3, 4))
	for _, c := range guardCases(rng, 20000) {
		if linux(c.command) && !windows(c.command) {
			t.Errorf("%q is denied off Windows but not on it", c.command)
		}
	}
}

func TestGeminiGuardRegexTabLimit(t *testing.T) {
	denied := geminiGuardMatcher(t, "zakwas", "apply")
	for n := 1; n <= geminiGuardTabs+1; n++ {
		tabs := strings.Repeat("\t", n)
		for name, command := range map[string]string{
			"lead":    tabs + "zakwas apply",
			"between": "zakwas" + tabs + "apply",
			"mixed":   "zakwas " + tabs + " apply",
			"spaced":  "zakwas" + strings.Repeat(" \t", n) + " apply",
			"after":   `zakwas -y "apply"` + tabs + "-y",
			"end":     `zakwas -y "apply" ` + tabs,
		} {
			if got, want := denied(command), n <= geminiGuardTabs; got != want {
				t.Errorf("%s with %d tabs %q: denied = %v, want %v", name, n, command, got, want)
			}
		}
	}
}

func TestGeminiGuardRegexPathPartLimit(t *testing.T) {
	denied := geminiGuardMatcher(t, "zakwas", "apply")
	for n := 0; n <= geminiGuardPathParts+1; n++ {
		for name, command := range map[string]string{
			"escaped": "/a" + strings.Repeat(`\ b`, n) + "/zakwas apply",
			"quoted":  strings.Repeat(`"a b"`, n) + "/zakwas apply",
		} {
			if got, want := denied(command), n <= geminiGuardPathParts; got != want {
				t.Errorf("%s with %d parts %q: denied = %v, want %v", name, n, command, got, want)
			}
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

const guardTimingScript = `
const { regexes, inputs } = JSON.parse(require("fs").readFileSync(0, "utf8"));
const worst = [];
for (const source of regexes) {
  const re = new RegExp(source);
  for (const [name, text] of inputs) {
    const start = process.hrtime.bigint();
    re.test('"command":' + JSON.stringify(text) + ',"description":"d"');
    worst.push([Number(process.hrtime.bigint() - start) / 1e6, name]);
  }
}
worst.sort((a, b) => b[0] - a[0]);
console.log(JSON.stringify(worst[0]));
`

func TestGeminiGuardRegexIsLinearInJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	const n = 40000
	inputs := [][2]string{
		{"quotes", strings.Repeat(`"`, n)},
		{"apostrophes", strings.Repeat(`'`, n)},
		{"quote-slash", strings.Repeat(`"/`, n/2) + "zakwas apply"},
		{"apostrophe-slash", strings.Repeat(`'/`, n/2) + "zakwas apply"},
		{"quote-slash-lattice", strings.Repeat(`"a/"b/`, n/6) + "zakwas apply"},
		{"escaped-spaces", "/a" + strings.Repeat(`\ b`, n/4) + "/zakwas apply"},
		{"quoted-parts", strings.Repeat(`"a b"`, n/5) + "/zakwas apply"},
		{"slashes", strings.Repeat("/", n)},
		{"tabs", "zakwas" + strings.Repeat("\t", n) + "apply"},
		{"space-tabs", "zakwas" + strings.Repeat(" \t", n/2) + "apply"},
		{"quoted-word-tabs", `zakwas -y "apply"` + strings.Repeat(" \t", n/2) + "x"},
		{"flags", "zakwas " + strings.Repeat("-y ", n/3) + "apply"},
		{"quoted-values", "zakwas " + strings.Repeat(`-c "a b" `, n/9) + "plan"},
		{"backslashes", strings.Repeat(`\`, n)},
		{"backslash-slash", strings.Repeat(`\/`, n/2) + "zakwas.exe apply"},
		{"windows-path", `C:` + strings.Repeat(`\a`, n/2) + `\zakwas.exe apply`},
		{"windows-quoted-path", `"C:` + strings.Repeat(`\a b`, n/4) + `\zakwas.exe" apply`},
		{"windows-quote-backslash-lattice", strings.Repeat(`"a\b\`, n/6) + "zakwas.exe apply"},
		{"calls", strings.Repeat("& ", n/2) + "zakwas apply"},
		{"ampersands", strings.Repeat("&", n)},
		{"exes", strings.Repeat("zakwas.exe", n/10)},
		{"exe-then-tabs", "zakwas.exe" + strings.Repeat("\t", n) + "apply"},
	}
	var regexes []string
	for _, word := range config.GuardedCommands {
		regexes = append(regexes, `"command":"`+geminiGuardRegex([]string{"zakwas", word}), `"command":"`+geminiGuardRegexFor([]string{"zakwas", word}, true))
	}
	payload, err := json.Marshal(map[string]any{"regexes": regexes, "inputs": inputs})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", guardTimingScript)
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v (a timeout means the regex backtracks superlinearly)", err)
	}
	var worst struct {
		ms   float64
		name string
	}
	var row []any
	if err := json.Unmarshal(out, &row); err != nil || len(row) != 2 {
		t.Fatalf("node output %q: %v", out, err)
	}
	worst.ms, _ = row[0].(float64)
	worst.name, _ = row[1].(string)
	if worst.ms > 250 {
		t.Errorf("%s takes %.0f ms on %d characters, the regex backtracks superlinearly", worst.name, worst.ms, n)
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
		{`"/a b/"zakwas`, true},
		{`'/a b/'zakwas`, true},
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
	seps := []string{" ", " ", " ", "  ", "\t", " \t", "\t\t", " \t\t", "\t \t", "\t\t\t\t", "  \t \t  "}
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
		{`-c it\'s`, false, 1},
		{`-c $'a\'b'`, false, 1},
		{`-c a\"b`, false, 1},
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
		{` "-y"`, false},
		{` '-y'`, false},
		{" $F", false},
		{` "$F"`, false},
		{" `echo -y`", false},
		{` \-y`, false},
		{" ", false},
		{"\t", false},
		{"\t\t", false},
		{" # why", false},
		{` "plan"`, true},
		{` 'y'`, true},
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
		escaped = escaped || strings.Contains(it.text, `\"`) || strings.Contains(it.text, `\'`)
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
		"zakwas $SUBCOMMAND", "zakwas" + strings.Repeat("\t", geminiGuardTabs+1) + "apply", strings.Repeat("\t", geminiGuardTabs+1) + "zakwas apply",
		"zakwas -y \\\napply", "/a" + strings.Repeat(`\ b`, geminiGuardPathParts+1) + "/zakwas apply",
		"zakwas" + strings.Repeat(` -c "a b"`, geminiGuardQuotedValues+1) + " apply",
		"zakwas -c \"a\" -m 'b' -m \"c\" -m 'd' -m \"e\" -m 'f' -m \"g\" apply",
	} {
		if denied(command) {
			t.Errorf("%q is denied, so the limit in docs/config.md is out of date", command)
		}
	}
}

func TestGeminiPolicyGuardFollowsThePlannedSystem(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			m := &Module{Agents: config.Agents{Providers: []string{config.ProviderGemini}}, Guard: true, GOOS: goos}
			rules := m.geminiRules()
			if len(rules) != len(config.GuardedCommands) {
				t.Fatalf("%d rules, want %d", len(rules), len(config.GuardedCommands))
			}
			for i, r := range rules {
				if want := geminiGuardRegexFor([]string{"zakwas", config.GuardedCommands[i]}, goos == "windows"); r.CommandRegex != want {
					t.Errorf("rule %d regex is not the one for %s", i, goos)
				}
				if got := regexp.MustCompile(`"command":"` + r.CommandRegex).MatchString(geminiCall(t, `C:\bin\zakwas.exe `+config.GuardedCommands[i])); got != (goos == "windows") {
					t.Errorf("%s: denies C:\\bin\\zakwas.exe %s = %v", goos, config.GuardedCommands[i], got)
				}
			}
		})
	}
}
