package cli

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/platform"
)

// ModuleNames lists every module in apply order.
var ModuleNames = platform.Names()

func completionModules() []string { return platform.SupportedModules(platform.Current()) }

var shells = []string{"zsh", "bash", "fish"}

// planCommands share the flags defined by newFlags.
var planCommands = []string{"plan", "apply", "upgrade", "check"}

type valueKind int

const (
	noValue valueKind = iota
	fileValue
	moduleValue
	freeValue
)

type complFlag struct {
	name, usage string
	kind        valueKind
}

// spelling is how completion offers the flag; Go's flag package accepts
// one or two dashes for every flag.
func (f complFlag) spelling() string {
	if len(f.name) == 1 {
		return "-" + f.name
	}
	return "--" + f.name
}

// spellings are both forms, so a value after either one completes.
func (f complFlag) spellings() []string {
	return []string{"-" + f.name, "--" + f.name}
}

var freeFlags = []string{"version", "providers", "justification", "command", "url", "arg", "env-var", "bearer-token-env"}

func flagsOf(fs *flag.FlagSet) []complFlag {
	var out []complFlag
	fs.VisitAll(func(f *flag.Flag) {
		cf := complFlag{name: f.Name, usage: f.Usage, kind: fileValue}
		switch {
		case isBoolFlag(f):
			cf.kind = noValue
		case f.Name == "only":
			cf.kind = moduleValue
		case slices.Contains(freeFlags, f.Name):
			cf.kind = freeValue
		}
		out = append(out, cf)
	})
	return out
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

type completionModel struct {
	global, init, selfUpdate, capture, propose, proposals, approve, mcp []complFlag
}

func newCompletionModel() completionModel {
	discard := &console{w: io.Discard}
	global, _ := newFlags(discard)
	initFS, _ := newInitFlags(discard)
	selfUpdateFS, _ := newSelfUpdateFlags(discard)
	captureFS, _ := newCaptureFlags(discard)
	proposeFS, _ := newProposeFlags(discard)
	proposalsFS, _ := newConfigFlags("proposals", "", true, discard)
	approveFS, _ := newConfigFlags("approve", "", false, discard)
	mcpFS, _ := newMCPFlags(discard)
	return completionModel{
		global: flagsOf(global), init: flagsOf(initFS), selfUpdate: flagsOf(selfUpdateFS),
		capture: flagsOf(captureFS), propose: flagsOf(proposeFS), proposals: flagsOf(proposalsFS), approve: flagsOf(approveFS), mcp: flagsOf(mcpFS),
	}
}

const completionUsage = "Usage: zakwas completion zsh|bash|fish\n"

func runCompletion(args []string, out, errOut *console) int {
	if slices.ContainsFunc(args, func(a string) bool { return a == "-h" || a == "--help" }) {
		out.print(completionUsage)
		return ExitOK
	}
	if len(args) != 1 {
		errOut.print(completionUsage)
		return ExitUsage
	}
	m := newCompletionModel()
	switch args[0] {
	case "zsh":
		out.print(m.zsh())
	case "bash":
		out.print(m.bash())
	case "fish":
		out.print(m.fish())
	default:
		errOut.printf("zakwas: unknown shell %q; use zsh, bash or fish\n", args[0])
		return ExitUsage
	}
	return ExitOK
}

func commandNames() []string {
	names := make([]string, len(commandList))
	for i, c := range commandList {
		names[i] = c.name
	}
	return names
}

func (m completionModel) all() []complFlag {
	return slices.Concat(m.global, m.init, m.selfUpdate, m.capture, m.propose, m.proposals, m.approve, m.mcp)
}

func (m completionModel) bash() string {
	var fileFlags, valueFlags, spelled []string
	for _, f := range m.all() {
		switch f.kind {
		case fileValue:
			fileFlags = append(fileFlags, f.spellings()...)
		case freeValue:
			valueFlags = append(valueFlags, f.spellings()...)
		}
	}
	for _, f := range m.global {
		spelled = append(spelled, f.spelling())
	}
	withValue := append(append([]string{"-only", "--only"}, fileFlags...), valueFlags...)
	var b strings.Builder
	b.WriteString("# bash completion for zakwas; load with: eval \"$(zakwas completion bash)\"\n")
	b.WriteString("_zakwas() {\n")
	b.WriteString("    local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]} cmd='' i words\n")
	b.WriteString("    for ((i = 1; i < COMP_CWORD; i++)); do\n        case ${COMP_WORDS[i]} in\n")
	fmt.Fprintf(&b, "            %s) ((i++)) ;;\n", strings.Join(withValue, "|"))
	b.WriteString("            -*) ;;\n            *) cmd=${COMP_WORDS[i]}; break ;;\n        esac\n    done\n")
	b.WriteString("    case $prev in\n")
	fmt.Fprintf(&b, "        -only|--only)\n            local prefix=''\n            [[ $cur == *,* ]] && prefix=${cur%%,*},\n            COMPREPLY=($(compgen -P \"$prefix\" -W '%s' -- \"${cur##*,}\"))\n            return ;;\n", strings.Join(completionModules(), " "))
	fmt.Fprintf(&b, "        %s) return ;;\n", strings.Join(append(fileFlags, valueFlags...), "|"))
	b.WriteString("    esac\n    case $cmd in\n")
	fmt.Fprintf(&b, "        '') if [[ $cur == -* ]]; then words='%s'; else words='%s'; fi ;;\n", strings.Join(spelled, " "), strings.Join(commandNames(), " "))
	fmt.Fprintf(&b, "        %s) words='%s' ;;\n", strings.Join(planCommands, "|"), strings.Join(spelled, " "))
	fmt.Fprintf(&b, "        init) [[ $cur == -* ]] || return; words='%s' ;;\n", spellingsOf(m.init))
	fmt.Fprintf(&b, "        self-update) words='%s' ;;\n", spellingsOf(m.selfUpdate))
	fmt.Fprintf(&b, "        capture) words='%s' ;;\n", spellingsOf(m.capture))
	fmt.Fprintf(&b, "        propose) words='%s' ;;\n", spellingsOf(m.propose))
	fmt.Fprintf(&b, "        proposals) words='%s' ;;\n", spellingsOf(m.proposals))
	fmt.Fprintf(&b, "        approve) words='%s' ;;\n", spellingsOf(m.approve))
	fmt.Fprintf(&b, "        mcp) words='%s' ;;\n", spellingsOf(m.mcp))
	fmt.Fprintf(&b, "        completion) words='%s' ;;\n", strings.Join(shells, " "))
	b.WriteString("        *) return ;;\n    esac\n")
	b.WriteString("    COMPREPLY=($(compgen -W \"$words\" -- \"$cur\"))\n}\n")
	b.WriteString("complete -o default -F _zakwas zakwas\n")
	return b.String()
}

func spellingsOf(flags []complFlag) string {
	var s []string
	for _, f := range flags {
		s = append(s, f.spelling())
	}
	return strings.Join(s, " ")
}

func (m completionModel) zsh() string {
	var b strings.Builder
	b.WriteString("#compdef zakwas\n# zsh completion for zakwas; load with: source <(zakwas completion zsh)\n\n")
	b.WriteString("_zakwas() {\n  local curcontext=$curcontext state line\n  local -a global_flags commands\n")
	b.WriteString("  global_flags=(\n")
	for _, f := range m.global {
		fmt.Fprintf(&b, "    %s\n", zshSpec(f, ""))
	}
	b.WriteString("  )\n  commands=(\n")
	for _, c := range commandList {
		fmt.Fprintf(&b, "    %s\n", zshQuote(c.name+":"+commandHelp(c.name, platform.Current())))
	}
	b.WriteString("  )\n")
	b.WriteString("  _arguments -C $global_flags '1:command:->command' '*::arg:->args'\n")
	b.WriteString("  case $state in\n    command) _describe -t commands command commands ;;\n    args)\n      case $line[1] in\n")
	fmt.Fprintf(&b, "        %s) _arguments $global_flags ;;\n", strings.Join(planCommands, "|"))
	b.WriteString("        init) _arguments")
	for _, f := range m.init {
		b.WriteString(" " + zshSpec(f, "*"))
	}
	b.WriteString(" '1:directory:_directories' ;;\n")
	b.WriteString("        self-update) _arguments")
	for _, f := range m.selfUpdate {
		b.WriteString(" " + zshSpec(f, ""))
	}
	b.WriteString(" ;;\n")
	for _, sub := range []struct {
		name  string
		flags []complFlag
	}{{"capture", m.capture}, {"propose", m.propose}, {"proposals", m.proposals}, {"approve", m.approve}, {"mcp", m.mcp}} {
		fmt.Fprintf(&b, "        %s) _arguments", sub.name)
		for _, f := range sub.flags {
			b.WriteString(" " + zshSpec(f, ""))
		}
		b.WriteString(" ;;\n")
	}
	fmt.Fprintf(&b, "        completion) _arguments '1:shell:(%s)' ;;\n", strings.Join(shells, " "))
	b.WriteString("      esac ;;\n  esac\n}\n\n")
	b.WriteString("if [[ $funcstack[1] == _zakwas ]]; then\n  _zakwas \"$@\"\nelse\n  compdef _zakwas zakwas\nfi\n")
	return b.String()
}

// zshSpec renders an _arguments spec; repeat is "*" for repeatable flags.
func zshSpec(f complFlag, repeat string) string {
	desc := strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`).Replace(f.usage)
	spec := repeat + f.spelling() + "[" + desc + "]"
	switch f.kind {
	case fileValue:
		spec += ":file:_files"
	case moduleValue:
		spec += ":module:_sequence compadd - " + strings.Join(completionModules(), " ")
	case freeValue:
		spec += ":" + f.name + ": "
	}
	return zshQuote(spec)
}

func zshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (m completionModel) fish() string {
	var b strings.Builder
	b.WriteString("# fish completion for zakwas; load with: zakwas completion fish | source\n")
	b.WriteString("complete -c zakwas -f\n")
	for _, c := range commandList {
		fmt.Fprintf(&b, "complete -c zakwas -n __fish_use_subcommand -a %s -d %s\n", c.name, fishQuote(commandHelp(c.name, platform.Current())))
	}
	planCond := fishQuote("__fish_use_subcommand; or __fish_seen_subcommand_from " + strings.Join(planCommands, " "))
	for _, f := range m.global {
		b.WriteString(fishFlag(planCond, f))
	}
	for _, f := range m.init {
		b.WriteString(fishFlag(fishQuote("__fish_seen_subcommand_from init"), f))
	}
	b.WriteString("complete -c zakwas -n '__fish_seen_subcommand_from init' -a '(__fish_complete_directories)'\n")
	for _, f := range m.selfUpdate {
		b.WriteString(fishFlag(fishQuote("__fish_seen_subcommand_from self-update"), f))
	}
	for _, sub := range []struct {
		name  string
		flags []complFlag
	}{{"capture", m.capture}, {"propose", m.propose}, {"proposals", m.proposals}, {"approve", m.approve}, {"mcp", m.mcp}} {
		for _, f := range sub.flags {
			b.WriteString(fishFlag(fishQuote("__fish_seen_subcommand_from "+sub.name), f))
		}
	}
	fmt.Fprintf(&b, "complete -c zakwas -n '__fish_seen_subcommand_from completion' -a %s\n", fishQuote(strings.Join(shells, " ")))
	return b.String()
}

func fishFlag(cond string, f complFlag) string {
	opt := "-l " + f.name
	if len(f.name) == 1 {
		opt = "-s " + f.name
	}
	switch f.kind {
	case fileValue:
		opt += " -r -F"
	case moduleValue:
		opt += " -x -a " + fishQuote(strings.Join(completionModules(), " "))
	case freeValue:
		opt += " -x"
	}
	return fmt.Sprintf("complete -c zakwas -n %s %s -d %s\n", cond, opt, fishQuote(f.usage))
}

func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(s) + "'"
}
