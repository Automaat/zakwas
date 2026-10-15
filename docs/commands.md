# Command reference

```
zakwas [flags] <command>
```

Flags go before or after the command: `zakwas -y apply` and `zakwas apply -y` are the same. Go's flag parser accepts one or two dashes for every flag (`-only` = `--only`).

| Command | What it does |
|---|---|
| [`plan`](#plan) | show pending changes |
| [`apply`](#apply) | show pending changes, confirm, apply them |
| [`upgrade`](#upgrade) | `brew update` and refresh agent marketplaces, then apply |
| [`check`](#check) | exit 2 when anything drifted |
| [`capture`](#capture) | list what agents have that `zakwas.yaml` doesn't declare; `--write` saves it as a proposal |
| [`propose`](#propose) | save a config change as a proposal, without editing anything |
| [`proposals`](#proposals) | list pending proposals |
| [`approve`](#approve) | show a proposal's diff and plan, confirm, apply it and commit it |
| [`mcp`](#mcp) | serve config reads and proposals to coding agents over MCP |
| [`init`](#init) | create a starter config repo from this Mac |
| [`self-update`](#self-update) | replace the zakwas binary with a release |
| [`completion`](#completion) | print a shell completion script |
| [`schema`](#schema) | print the JSON Schema of `zakwas.yaml` |
| [`version`](#version) | print the zakwas version |

## Config commands: plan, apply, upgrade, check

These four read `zakwas.yaml` and share one set of flags.

| Flag | Commands | Meaning |
|---|---|---|
| `-c PATH` | all four | config file; default: search upward from the current directory, then `$ZAKWAS_CONFIG` |
| `--only LIST` | all four | comma-separated modules to run: `system`, `files`, `links`, `templates`, `brew`, `mise`, `agents`, `commands`, `defaults` |
| `--diff` | all four | show file content diffs and command scripts |
| `--json` | all four | machine-readable output: one plan document (`plan`, `check`) or JSON lines events (`apply`, `upgrade`) |
| `--no-color` | all four | disable colors (also `NO_COLOR`, or stdout not a terminal) |
| `-y` | `apply`, `upgrade` | apply without asking for confirmation |
| `--no-wait` | `apply`, `upgrade` | exit 1 at once when another zakwas run holds the lock, instead of waiting for it |
| `-out FILE` | `plan` | also save the plan to FILE, for `apply -plan` |
| `-plan FILE` | `apply` | apply a saved plan if the machine still matches it, without prompting; can't be combined with `--only` |

`apply --json` and `upgrade --json` need `-y` (or `-plan`): JSON consumers can't answer a prompt. Without a terminal on stdin, `apply` refuses to prompt; review with `plan`, then `apply -y`.

### plan

Shows what `apply` would change. Never changes the system.

```bash
zakwas plan
zakwas plan --diff --only files,templates
zakwas plan --json -out plan.json
```

### apply

Shows the plan, asks for confirmation, applies it, and appends the result to `~/.local/state/zakwas/history.jsonl`. Warns when the config repo is dirty, not on `main`, or behind upstream.

```bash
zakwas apply
zakwas apply -y
zakwas apply -plan plan.json
```

Only one `apply` or `upgrade` runs at a time, so two of them never overwrite each other's state. A second one waits, printing the command and pid it waits for, then plans against what the first left behind; with `--no-wait` it exits 1 instead. The lock is held from before the plan until the run ends, including while the confirmation prompt is open. It is an `flock` on `~/.local/state/zakwas/lock`, so a crashed or killed run releases it and the next run needs no cleanup. With an `agents` section, `apply` and `upgrade` refuse to run from a coding-agent session, exiting 1 before they change anything; see [`agents.guard`](config.md#guard). The commands an `apply` or `upgrade` runs get `ZAKWAS_LOCK_HELD=<pid>`; a `zakwas apply` or `upgrade` started from one fails at once instead of waiting on its parent forever. `plan` and `check` take no lock and can run at any time: zakwas replaces its state files atomically, so they never read a half-written one.

### upgrade

Refreshes package and plugin sources, then runs `apply`, so new versions get picked up: `brew update` when the `brew` module runs, and, when the `agents` module runs, `claude plugin marketplace update` for every declared Claude marketplace and `codex plugin marketplace upgrade` for every declared Git marketplace in Codex that is already configured, and `git fetch` for every opencode Git marketplace and every [skill](config.md#skills) Git source zakwas already fetched. Codex moves installed plugins to the upgraded marketplace's versions itself. Needs a `brew` or `agents` section. Every other command runs brew with `HOMEBREW_NO_AUTO_UPDATE=1` and never refreshes marketplaces; `apply` only clones an opencode Git marketplace or a skill Git source that isn't fetched yet.

### check

Like `plan`, but exits 2 when anything would change. For CI, cron or launchd.

```bash
zakwas check || notify "machine drifted"
zakwas check --json
```

## propose, proposals, approve

Changing the setup without finding the right YAML section by hand, and without letting an agent edit `zakwas.yaml` freely: `propose` takes a typed operation and saves the edit, `approve` applies it from a terminal.

```
zakwas propose OPERATION [flags]
zakwas proposals [-c PATH] [-json]
zakwas approve ID [-c PATH]
```

### propose

Saves the edit with its diff, plan and risk tier under `~/.local/state/zakwas/proposals/`, and prints them. It changes neither the config repo nor the system; the plan only reads, like `zakwas plan`.

| Operation | Edits | Flags |
|---|---|---|
| `add-marketplace NAME SOURCE` | `agents.marketplaces` | `--providers LIST` |
| `add-plugin NAME@MARKETPLACE` | `agents.plugins` | `--providers LIST` |
| `add-permission allow\|review\|deny COMMAND...` | `agents.permissions.<decision>` | `--providers LIST`, `--justification TEXT` |
| `remove-permission allow\|review\|deny COMMAND...` | `agents.permissions.<decision>` | |
| `add-brew NAME` | the Brewfile (`brew.file`) | `--cask` |
| `pin-mise TOOL VERSION` | the `[tools]` table of the mise config (`mise.config`) | |
| `add-mcp NAME` | `agents.mcp` | `--command CMD` with `--arg A` and `--env-var NAME`, or `--url URL` with `--bearer-token-env NAME`; `--providers LIST` |

`--providers`, `--arg` and `--env-var` take a comma list or repeat. A command that starts with a dash goes after `--`: `zakwas propose add-permission allow -- ls -la`. Also `-c PATH` and `-json`, which prints the proposal as JSON.

Edits splice the original bytes: every line outside the added or removed entries stays as it was, comments, quoting and folded `>-` scalars included. A flow-style collection (`plugins: [a@m]`) can't be spliced and is rejected with a message; so are an edit that makes `zakwas.yaml` invalid, a duplicate entry, a plugin whose marketplace isn't declared (one declared by an earlier operation of the same proposal counts), a `zakwas.yaml` with more than one YAML document, a Brewfile with Ruby blocks or entries spanning lines, and a brew or mise edit when the config file is a symlink or outside the config repo.

**Risk tier.** `low` or `high`, from the operations and the plan of the modules they affect (a permission or MCP edit also plans `templates`, a mise pin `files`):

| Tier | Operations |
|---|---|
| low | add a plugin from a declared marketplace; add a `deny` or `review` rule; remove an `allow` rule; add a core Homebrew formula (not a cask, not from a tap); pin a mise tool (without a backend prefix such as `npm:` or `github:`) |
| high | everything else: an `allow` rule, removing a `deny` or `review` rule, a cask or tap formula, a tool from a mise backend, an MCP server, a marketplace |

Whatever the operations, a proposal is high when the plan it will run touches agent config (`~/.claude`, `~/.claude.json`, `~/.codex`, `~/.config/opencode`, `~/.agents`, `$CLAUDE_CONFIG_DIR`, `$CODEX_HOME`), shell rc files (`~/.zshrc`, `~/.zprofile`, `~/.bashrc`, …), `~/.ssh` or `/etc`, or when a module of that plan fails to plan. The plan is the whole plan of those modules, including drift that was already there, since that is what `approve` runs.

### proposals

Lists the pending proposals of this config: id, tier, time left and what they do. A proposal expires after 7 days.

### approve

Loads the proposal, computes the diff, plan and tier again from its operations against the current files, shows them and asks. On `y` it writes the files, applies only the affected modules (same lock, history and progress as `apply`), deletes the proposal and commits the edited files, and only those, to the current branch of the config repo. It never pushes. Needs a terminal on stdin and has no `-y`.

It refuses, changing nothing, when:

- the config file, Brewfile or mise config the proposal was made against has changed (propose again); a file saved while the prompt waits is detected before anything is written, and never overwritten;
- the proposal is older than 7 days, or was made for another config;
- an edited file has uncommitted changes, the config repo isn't a git repository, or `HEAD` is detached;
- a module can't be planned;
- it runs from an agent session while [`agents.guard`](config.md#guard) is on. The guard also denies the managed agents `zakwas approve`.

If the apply fails, the files are written back as they were and the proposal stays pending; the steps that ran stay done. If only the commit fails, the change stays applied and uncommitted, and the message says so.

## capture

```
zakwas capture [-c PATH] [-json] [--write]
```

Users and agents install plugins, add MCP servers and approve commands directly in each agent. `capture` finds what those changes left behind, so they reach the config repo instead of missing on the next Mac or being pruned. It lists, per provider, what the managed agents have that `zakwas.yaml` doesn't declare:

| Provider | Listed |
|---|---|
| Claude Code | user-scope plugins; marketplaces declared in the user settings; MCP servers in `~/.claude.json`; `allow` rules of the user `settings.json` written as `Bash(command:*)` or `Bash(command *)` |
| Codex (only when `zakwas.yaml` names `codex`) | plugins and marketplaces of `config.toml`; `[mcp_servers.*]` tables; the prefix rules of `rules/default.rules`, where approving a command in Codex saves it |

opencode has no plugin commands, and gets servers and rules through its templates, so it isn't read.

Never listed: plugins that come from no marketplace (built-in, synced from claude.ai, `--plugin-dir` and `.claude/skills` ones), project and local installs, servers and rules zakwas added itself, and the rules file zakwas writes. A server, plugin or rule that `zakwas.yaml` already declares for the provider isn't listed either.

Nothing is changed; like `plan` it takes no lock and runs no command but the providers' list commands. Without `--write` it prints the findings and what can't be declared:

```
claude
  marketplace  sai  o/sai
  plugin       humanize@sai
  mcp server   docs  stdio npx -y docs-mcp
  allow rule   git status
codex
  approval     allow cargo build

Not captured
  claude plugin off@sai: installed but disabled, and declaring it would enable it
```

**Not captured.** A finding is only declared when the declaration reproduces what is there, so an apply right after adopts it without changing it. These are listed with the reason and left out:

- a disabled plugin (declaring it would enable it) and a plugin whose marketplace `zakwas.yaml` doesn't declare for the provider and capture can't either;
- an MCP server with settings `agents.mcp` can't express here (literal `env` values or headers, which may be secrets; a URL with a query, a fragment or credentials; options such as timeouts), and one whose name another provider uses for a different server;
- a Claude rule that isn't a command prefix (`Read(...)`, `WebFetch(...)`, an exact `Bash(cmd)` rule, `Bash(*)`), and a Codex rule whose pattern has alternatives or whose decision isn't `allow`, `prompt` or `forbidden`;
- an operation that wouldn't apply to `zakwas.yaml` (a marketplace declared for other providers only, a flow-style list, an invalid rule), with the error.

A server's command and arguments are copied as they are: read them in the proposal's diff before approving, since a key passed as an argument would reach the config repo.

With `--write` the findings become **one proposal** that follows the normal [approval rules](#propose-proposals-approve): it is saved with its diff, plan and tier (high, since it adds marketplaces or servers), nothing is edited, and a person applies it with `zakwas approve ID` in a terminal. Marketplaces are added as `add-marketplace`, plugins as `add-plugin`, servers as `add-mcp`, rules as `add-permission`, each with `providers` set to where it was found, so nothing reaches a provider that didn't have it. After the proposal is approved, a second `capture` lists nothing; what it can't declare stays under "Not captured". Without anything to declare, `--write` saves no proposal. `-json` prints `findings`, `skipped`, `ops` (each a valid [`zakwas_propose`](#mcp) argument) and, with `--write`, the `proposal`. Exit 1 when a provider can't be read, for example its CLI is missing; the others' findings are still printed.

## mcp

```
zakwas mcp [-c PATH] [--enable-apply]
```

Serves zakwas to a coding agent over [MCP](https://modelcontextprotocol.io) on stdin and stdout, so an agent gets structured JSON instead of parsing `zakwas plan`, and a low-risk change can be confirmed in the agent instead of a terminal. The agent starts it; register it with [`agents.mcp`](config.md#mcp):

```yaml
agents:
  mcp:
    zakwas: {command: zakwas, args: [mcp, --enable-apply, -c, ~/dotfiles/zakwas.yaml]}
```

Pass `-c` (or set `ZAKWAS_CONFIG` in the agent's environment): an agent starts the server in whatever directory it is in, where `zakwas.yaml` isn't found. The config is read on every call, so editing it needs no restart; a missing or invalid one fails that call, not the server.

| Tool | What it does |
|---|---|
| `zakwas_config` | the config path, repo root, modules, agent providers, MCP servers, whether the [guard](config.md#guard) is on, and the text of `zakwas.yaml` |
| `zakwas_plan` | the plan as JSON, like `zakwas plan --json`; arguments `only` (modules) and `diff` |
| `zakwas_check` | the same, with `drift: true` when anything would change |
| `zakwas_capture` | what the agents have that `zakwas.yaml` doesn't declare, like [`capture`](#capture): `findings`, `skipped` with the reason, and in `ops` the operations that declare the rest, each valid for `zakwas_propose`. Changes nothing |
| `zakwas_propose` | saves a proposal like [`propose`](#propose); arguments are the operation's fields (`kind`, `name`, `decision`, `command`, `version`, `cask`, `providers`, `justification`, `server`). Returns the proposal and what to do next |
| `zakwas_proposals` | pending proposals of this config |
| `zakwas_discard` | deletes a pending proposal of this config |
| `zakwas_apply` | only with `--enable-apply`: applies a low-risk proposal after you confirm it in the agent |

Everything except `zakwas_apply` leaves the config repo and the system alone, like `plan`, `check` and `propose`. A tool failure comes back as an error result with the message, so the agent can react; the server never writes anything but JSON-RPC to stdout (tool output goes to stderr).

**Applying.** `zakwas_apply` takes a proposal id and works only when all of these hold; otherwise nothing changes and the result says why and gives `zakwas approve ID` to run in a terminal:

- the server started with `--enable-apply`; without it the tool does not exist;
- the proposal is low risk when rated again now (the tier in the proposal file is not trusted), so a high-risk proposal is never applied over MCP;
- the client declared elicitation support (MCP 2025-06-18 or later, form mode), because the confirmation is an elicitation form. A client without it, such as Gemini CLI, gets the command instead;
- you answer yes. The form shows what zakwas rendered, never text written by the agent: the proposal, its tier and reasons, the diff, the plan and "apply N step(s) and commit FILES to BRANCH?". Decline, cancel, an error, no answer within 10 minutes, or a review over 32 KiB all apply nothing.

After a yes it does what [`approve`](#approve) does: the same checks, files written only while they still hold what you reviewed, the plan made again and compared with the one you saw, the affected modules applied, the proposal deleted and the edited files committed to the current branch without pushing, with the same history entry. It reports progress (`notifications/progress`, one per step start and end) when the client sent a progress token. It takes the [run lock](#apply) before it plans and holds it until it ends, including while the form is open; a busy lock fails the call at once instead of waiting. A proposal discarded or changed while the form is open is not applied. If the client closes its end, a running apply finishes (stopping halfway is what leaves files edited) and a form nobody can answer counts as no; SIGINT or SIGTERM stops the apply before its next step and puts the edited files back.

**Always asked.** `zakwas_apply` carries `_meta["anthropic/requiresUserInteraction"]`, so Claude Code prompts on every call, even in bypass mode. For a server registered through `agents.mcp` as `zakwas mcp --enable-apply`, zakwas also writes `approval_mode = "prompt"` for the tool in Codex's `config.toml`. opencode has no place zakwas can edit for this: the plan names the `permission` entry to add to your `opencode.json` template. The form comes on top of those prompts.

Only people should start this server with `--enable-apply`: the [guard](config.md#guard) denies the managed agents `zakwas mcp`, since an agent that ran it in a shell would be the client that answers the form.

## init

```
zakwas init DIR [--add PATH]...
```

Creates a starter config repo in DIR from the current Mac. DIR must not exist or be empty (exit 64 otherwise). Nothing outside DIR is changed.

| Flag | Meaning |
|---|---|
| `--add PATH` | file or directory under `$HOME` to copy into the repo and manage; repeatable |

What it writes:

| Path in DIR | When | Content |
|---|---|---|
| `zakwas.yaml` | always | schema comment, `protect.immutable: true`, a `files` entry per `--add`, plus `brew`/`mise` sections |
| `Brewfile` | Homebrew installed | `brew bundle dump --tap --formula --cask --mas`: no go/npm/cargo/uv/vscode entries, which mise or the tools themselves manage; third-party taps without a `trusted:` option get `trusted: true` (an existing `trusted:` option is left alone) |
| `dotfiles/mise/config.toml` | mise installed | a byte-for-byte copy of your global mise config, with tool versions pinned by mise itself (see below). Installed to `~/.config/mise/config.toml` |
| `dotfiles/<path>` | per `--add` | copy of the file or directory |
| `.gitignore` | always | `*.zakwas-bak*` |
| `.git` | always | `git init -b main` |

Sections for tools that aren't installed are left out. `brew.cleanup` starts as `none`; switch to `zap` once the Brewfile lists everything you want to keep.

**mise pins.** init copies the file `mise ls --global` reports your tools from (else `$MISE_GLOBAL_CONFIG_FILE` or `~/.config/mise/config.toml`) unchanged, then, for each active global tool whose requested version isn't already exact (`"22"`, `"latest"`), runs `mise use --global --pin <tool>@<version in use>` against the copy (from `/`, with a throwaway `MISE_STATE_DIR`/`MISE_CACHE_DIR` and `MISE_AUTO_INSTALL=0`). mise's own editor does the rewrite, so:

- comments, `[settings]`, `[env]`, `[[watch_files]]` and every other section stay as written;
- tool options stay: `"pipx:black" = { version = "latest", uvx = false }` only gets its `version` changed, same for `[tools.<name>]` tables and multi-line arrays;
- aliases resolve: `nodejs = "22"` becomes `node = "22.21.1"` (no duplicate key), moved to the end of `[tools]`; order there decides which tool wins when two ship the same binary, so check it;
- tools already pinned exactly, and tools only other global files list, are left alone;
- a tool with several versions keeps the file's order (the first is mise's default).

A tool init can't pin stays as you wrote it and is listed in the summary: versions not installed (pinning would install them), several versions under a key init can't match (e.g. an alias), or a `mise use` failure. The result must parse as TOML, or init fails and removes what it created.

**`--add` layout.** Each path keeps its location relative to `$HOME` under `dotfiles/`, with the leading dot dropped from every path component, so nothing in the repo is hidden:

| `--add` | Repo path | `dst` |
|---|---|---|
| `~/.zshrc` | `dotfiles/zshrc` | `~/.zshrc` |
| `~/.config/git` | `dotfiles/config/git` | `~/.config/git` |
| `~/.ssh/config` | `dotfiles/ssh/config` | `~/.ssh/config` |
| `~/Library/Application Support/Code/User/settings.json` | `dotfiles/Library/Application Support/Code/User/settings.json` | same path under `~/` |

Files inside an added directory keep their names, dots included. Directories are copied file by file; `.git` directories, symlinks and other non-regular files inside them are skipped and listed. File permissions are kept (the repo copy is made owner-writable).

`init` rejects, before writing anything: paths outside `$HOME` or missing; two paths that land on the same repo path (`~/.foo` and `~/foo`) or inside one another; `~/.config/mise/config.toml` (or a directory holding it) when mise is installed, since init generates that file; a symlink to a directory; an empty directory; a path containing DIR.

Then:

```bash
cd DIR && zakwas plan    # added files show as "already matches, start tracking"; others are backed up on apply
git add -A && git commit -m "Initial config"
git remote add origin git@github.com:you/dotfiles.git && git push -u origin main
# on a new Mac
curl -fsSL https://raw.githubusercontent.com/Automaat/zakwas/main/install.sh | bash -s -- --repo https://github.com/you/dotfiles.git
```

If a step fails after writing started, init removes what it created, including parent directories it made for DIR.

If zakwas already manages files on this Mac from another config, init warns: `plan` in the new repo lists those files as no longer managed, and `apply` deletes them.

## self-update

```
zakwas self-update [--version X.Y.Z]
```

| Flag | Meaning |
|---|---|
| `--version X.Y.Z` | release to install (a leading `v` is fine); default: latest |

Resolves the latest release from the `https://github.com/Automaat/zakwas/releases/latest` redirect (not the rate-limited API), downloads `zakwas_<version>_darwin_<arm64|amd64>.tar.gz` and `checksums.txt`, verifies the SHA256, and replaces the running binary atomically (temp file in the same directory, `chmod 0755`, rename). Prints a message and does nothing when that version is already running.

Build provenance is checked the way `install.sh` does it (see [security.md](security.md)): when `gh` is installed, logged in (`gh auth status`) and supports `--source-ref`, self-update runs `gh attestation verify <archive> --repo Automaat/zakwas --signer-workflow Automaat/zakwas/.github/workflows/release.yml --source-ref refs/tags/v<version>` and stops (exit 1) if it fails. Without such a `gh` it prints the command to verify by hand and continues. Versions before 0.4.0 carry no attestation and skip the check.

It refuses (exit 64) when a package manager owns the binary (the path after resolving symlinks):

| Binary under | Update with |
|---|---|
| `$MISE_DATA_DIR/installs`, `$XDG_DATA_HOME/mise/installs`, `~/.local/share/mise/installs` | bump `"github:Automaat/zakwas"` in your mise config |
| `Caskroom` or `Cellar` of `$HOMEBREW_PREFIX`, `/opt/homebrew`, `/usr/local` | `brew upgrade zakwas` |

`install.sh --no-apply` also updates a `~/.local/bin/zakwas` install.

## completion

```
zakwas completion zsh|bash|fish
```

Prints a completion script for commands, flags, `--only` module names (comma lists in zsh and bash), shells, and file/directory arguments. Flags are read from the CLI's own flag definitions, so the scripts stay in sync with the binary that printed them.

**zsh**, in `~/.zshrc` (after `compinit`):

```zsh
source <(zakwas completion zsh)
```

or install it on `fpath`:

```zsh
zakwas completion zsh > "${fpath[1]}/_zakwas"
```

**bash** (macOS bash 3.2 can't `source <(…)`), in `~/.bashrc`:

```bash
eval "$(zakwas completion bash)"
```

**fish**:

```fish
zakwas completion fish > ~/.config/fish/completions/zakwas.fish
```

To manage it with zakwas, generate the file into your config repo and add it to `files`.

## schema

```
zakwas schema
```

Prints the JSON Schema of `zakwas.yaml` (see [config.md](config.md)). Editors with the YAML language server use it through the first line `zakwas init` writes:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json
```

## version

```
zakwas version
zakwas --version
```

Prints `zakwas <version>` (`dev` for builds outside a release).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success; `check` found no drift; `-h`/`--help` (usage goes to stdout) |
| 1 | error; also when a module failed to plan (the others still apply) |
| 2 | `check` found drift |
| 64 | usage error (usage goes to stderr): no or unknown command, unknown flag, conflicting flags, invalid `init`/`self-update` arguments, package-managed binary |
| 130 | interrupted (Ctrl-C) |

## Environment

| Variable | Used by | Effect |
|---|---|---|
| `ZAKWAS_CONFIG` | config commands | config path when no `zakwas.yaml` is found upward from the current directory; `-c` overrides both |
| `NO_COLOR` | config commands | set to anything to disable colors ([no-color.org](https://no-color.org)) |
| `MISE_DATA_DIR`, `XDG_DATA_HOME` | `self-update` | where mise installs live, to detect a mise-managed zakwas |
| `HOMEBREW_PREFIX` | `self-update` | extra Homebrew prefix to detect a Homebrew-managed zakwas |
