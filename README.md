# zakwas

[![ci](https://github.com/Automaat/zakwas/actions/workflows/ci.yml/badge.svg)](https://github.com/Automaat/zakwas/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/Automaat/zakwas)](https://github.com/Automaat/zakwas/releases/latest)
[![license](https://img.shields.io/github/license/Automaat/zakwas)](LICENSE)

Declarative macOS setup. Describe the machine in `zakwas.yaml`, keep it in a git repo, and `zakwas apply` converges any Mac to it: a fresh one or the one you already use.

- **files / templates**: dotfiles installed as read-only, immutable copies (like the Nix store); edits go through the repo
- **links**: plain symlinks for configs apps must write themselves
- **brew**: GUI apps and formulae from a `Brewfile` (`brew bundle`, optional zap cleanup)
- **mise**: CLI tools pinned in a global mise config
- **agents**: coding-agent plugins and marketplaces (Claude Code, Codex and, opt-in, Copilot CLI; skills for opencode), MCP servers, skills, permissions and profiles for Claude Code, Codex, opencode and, opt-in, Gemini CLI and Copilot CLI, global instructions rendered per provider from one or more files, shared command hooks, and selected global Codex settings
- **defaults**: macOS preferences (`defaults write`, restarts Dock/Finder when needed)
- **system**: Touch ID for sudo, directories, SSH key
- **commands**: guarded one-off steps (`run` only while `check` fails)
- **launchd**: scheduled jobs as launchd user agents, loaded on apply and unloaded when removed from the config

![zakwas plan, apply, and a protected dotfile](docs/demo/demo.gif)

*Zakwas* is Polish for sourdough starter: keep it, feed it, and every new loaf comes out the same.

> **Status:** pre-1.0 and macOS only. The config format may change between minor versions until 1.0; changes are called out in the [release notes](https://github.com/Automaat/zakwas/releases).

## Start from the Mac you have

```bash
zakwas init ~/dotfiles --add ~/.zshrc --add ~/.config/git
cd ~/dotfiles && zakwas plan
```

`init` writes a starter repo: `zakwas.yaml`, a `Brewfile` from what Homebrew has installed, your global mise tools pinned to their current versions, and copies of the dotfiles you `--add`. Push it to GitHub and any other Mac is one command away.

## New Mac

```bash
curl -fsSL https://raw.githubusercontent.com/Automaat/zakwas/main/install.sh |
  bash -s -- --repo https://github.com/you/dotfiles.git
```

Installs the Xcode Command Line Tools and zakwas (checksum-verified, to `~/.local/bin/zakwas`), clones your config to `~/dotfiles` (`--dir` to change), then runs `zakwas apply`. `--no-apply` stops before applying, for configs that need manual steps first (e.g. registering an SSH key). `--version X.Y.Z` pins the release; re-run with `--no-apply` to update zakwas.

zakwas needs nothing else: when the config has a `brew` section and Homebrew is missing, the plan installs it first (official installer, asks for your password); when it has a `mise` section and mise is missing, it downloads a pinned mise release to `~/.local/bin/mise`, verifies its SHA256, then installs your tools with it. If the Brewfile lists `mise`, the Homebrew one is used instead.

## Install

| How | Command | Updates |
|---|---|---|
| installer (new Mac) | see above | `zakwas self-update` |
| Homebrew | `brew install automaat/tap/zakwas` | `brew upgrade zakwas` |
| mise | `"github:Automaat/zakwas" = "X.Y.Z"` in your mise config | bump the pin (Renovate does it) |
| Go | `go install github.com/Automaat/zakwas/cmd/zakwas@latest` | same command |

Releases carry SHA256 checksums and GitHub build provenance; `install.sh` and `self-update` always verify the checksum, and the provenance too when a logged-in `gh` is installed. See [docs/security.md](docs/security.md).

Shell completion: `zakwas completion zsh|bash|fish` ([docs/commands.md](docs/commands.md#completion)).

## Usage

```bash
zakwas plan            # what would change (--diff shows file contents)
zakwas apply           # show plan, confirm, apply (-y skips the prompt)
zakwas upgrade         # brew update + refresh agent marketplaces, then apply
zakwas check           # exit 2 when the machine drifted (cron/launchd friendly)
zakwas check --offline # same for the files zakwas owns only, in ~30 ms, no commands (agent session start)
zakwas plan --only brew,defaults
zakwas propose add-plugin foo@market   # save a config edit without applying it
zakwas proposals                       # pending proposals
zakwas approve ID                      # diff + plan, confirm, apply, commit (terminal only)
zakwas mcp --enable-apply              # serve plan/propose (and confirmed low-risk apply) to agents over MCP
```

`propose` takes a typed operation (add a plugin, add or remove a permission, add a brew package, pin a mise tool, add an MCP server), splices it into the original file bytes, and saves it with its plan and risk tier. `approve` applies only the affected modules and commits to the current branch without pushing. See [docs/commands.md](docs/commands.md#propose-proposals-approve).

`zakwas mcp` serves the same to coding agents over MCP: read-only config, plan and check tools, plus propose and discard. With `--enable-apply` it also applies a low-risk proposal after you confirm a diff and plan zakwas renders in the agent; high-risk proposals and clients that can't show the confirmation get the `zakwas approve ID` command. Register it with `agents.mcp: {zakwas: {command: zakwas, args: [mcp, --enable-apply, -c, ~/dotfiles/zakwas.yaml]}}`. See [docs/commands.md](docs/commands.md#mcp).

The repo is also an agent plugin marketplace. Its `zakwas` plugin ships a skill that tells agents to propose changes instead of editing agent config or `zakwas.yaml`, and to hand high-risk proposals to you as `zakwas approve ID`. Install it with zakwas itself: `agents.marketplaces: {zakwas: Automaat/zakwas}` and `agents.plugins: [zakwas@zakwas]` (Claude Code, Codex and opencode). See [docs/commands.md](docs/commands.md#teach-agents-the-flow).

```
$ zakwas plan
files
  ~ ~/.zshenv                         content
  - ~/.local/bin/old-tool             no longer managed
mise
  ~ ruff          0.16.2 → 0.16.9
  ▶ mise install
✓ up to date: system, links, templates, brew, commands, defaults

⚠ destructive: ~/.local/bin/old-tool

Plan: 0 to add, 2 to change, 1 to remove, 1 to run.
```

`+` add, `~` change, `-` remove, `▶` run a step; destructive changes (deletions, brew cleanup, mise prune) are bold red and repeated before the summary. `--diff` adds file diffs and command scripts. `apply` shows `[n/total]` progress per step, frames tool output (folded into groups on GitHub Actions), and ends with a recap. Colors follow [NO_COLOR](https://no-color.org) and turn off when stdout isn't a terminal or with `--no-color`. Without a terminal, `apply` refuses to prompt: review with `plan`, then `apply -y`.

### For scripts and CI

```bash
zakwas plan --json              # one JSON document: modules, changes, summary
zakwas check --json             # same document; exit code still 0 / 2
zakwas apply --json -y          # JSON lines: plan, step_start, step_done, summary
zakwas plan -out plan.json      # save the reviewed plan
zakwas apply -plan plan.json    # apply it, no prompt; refuses if anything changed since or on another host
```

Every document carries `format_version` (currently `1`): new fields may appear, renamed or removed ones bump it. Actions are `create`, `update`, `delete`, `run`; each change has `target`, optional `from`/`to`/`detail`, `destructive`, and `step` (false for entries a later step of the module carries out). `--diff` adds `diff`. With `--json`, tool output streamed by apply goes to stderr, so stdout is always valid JSON. Apply history (`~/.local/state/zakwas/history.jsonl`) uses the same change objects plus the result.

zakwas finds `zakwas.yaml` by searching up from the current directory, then `$ZAKWAS_CONFIG`; `-c` overrides both. Set `ZAKWAS_CONFIG` in your shell env to run it from anywhere.

Exit codes: 0 ok, 1 error, 2 drift (`check`), 64 usage, 130 interrupted. A module that fails to plan is reported and skipped; the others still apply, and the run exits 1.

## Config

Editors with YAML language support (VS Code, Zed, Neovim, JetBrains) autocomplete and validate the config when its first line is:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json
```

Every key is described in [docs/config.md](docs/config.md); ready-made snippets are in [docs/examples.md](docs/examples.md). Overview:

```yaml
protect:
  immutable: true                 # also set the uchg flag on installed files

files:                            # protected copies; a dir src copies every file
  - {src: dotfiles/zsh/zshrc, dst: ~/.zshrc}

links:                            # plain symlinks, for configs apps write
  - {src: dotfiles/ssh/config, dst: ~/.ssh/config}

templates:                        # Go text/template: .Home, .Vars.x
  vars: {email: you@example.com}
  files:
    - {src: dotfiles/git/user.tmpl, dst: ~/.config/git/user, mode: 0o600}

brew:
  file: Brewfile
  cleanup: zap                    # none | uninstall | zap
  upgrade: true

mise:
  config: dotfiles/mise/config.toml
  prune: true                     # remove versions no mise config references

agents:
  providers: [claude, codex, opencode]   # add gemini to converge Gemini CLI too
  instructions: dotfiles/AGENTS.md
  hooks:
    commands:
      - {event: beforeTool, command: /usr/local/bin/check-tool}
    # klaudiush: true                # register enabled klaudiush providers
  permissions:
    allow: [git status, git diff]
    review: [git push]
    deny: [git push --force]
  marketplaces:
    claude-plugins-official: anthropics/claude-plugins-official
  plugins:
    - commit-commands@claude-plugins-official

system:
  sudoTouchID: true
  dirs: [{path: ~/.ssh, mode: 0o700}]
  sshKey: {path: ~/.ssh/id_ed25519, comment: you@example.com}

commands:
  - name: rosetta
    check: /usr/bin/pgrep -q oahd
    run: softwareupdate --install-rosetta --agree-to-license

defaults:                         # YAML type picks -bool/-int/-float/-string
  - {domain: com.apple.dock, key: autohide, value: true}
  - {domain: NSGlobalDomain, key: KeyRepeat, value: 2}
  - {domain: com.apple.HIToolbox, key: AppleFnUsageType, value: 0, currentHost: true}
```

Modules run in this order: system → files → links → templates → brew → mise → agents → commands → launchd → defaults.

Find a macOS preference key: `defaults read > a`, toggle it in System Settings, `defaults read > b`, `diff a b`.

### Agent permissions

`agents.permissions` declares shell command permissions once, and zakwas writes each agent's native form. Entries are plain command prefixes matched on whole words, or `{command, providers, justification}` to target some agents only:

| Decision | Claude Code `settings.json` | Codex `rules/zakwas.rules` | opencode, via the `agentPermissions` template function |
|---|---|---|---|
| `allow` | `permissions.allow`: `Bash(<cmd>:*)` | `decision="allow"`: no approval, outside the sandbox | `"allow"` |
| `review` | not written: auto mode reviews what isn't allowed | `decision="prompt"` | `"ask"` |
| `deny` | `permissions.deny`: `Bash(<cmd>:*)` | `decision="forbidden"` | `"deny"` |

- zakwas removes only the Claude rules it added; rules you added yourself stay.
- zakwas owns the Codex rules file. When rules from several files match, Codex applies the strictest. Allow only read-only commands for Codex.
- Copilot CLI keeps user-level shell rules only in root-owned managed settings, so the plan reports permissions that reach `copilot` as unsupported instead of failing.
- In an opencode template, write `"bash": {{ agentPermissions "opencode" "ask" }}`. It renders the catch-all first and deny entries last, because opencode applies the last matching pattern.

See [docs/config.md](docs/config.md#permissions).

### Agent MCP servers

`agents.mcp` declares each MCP server once, as a stdio `command` or an HTTP `url`, and zakwas registers it at user scope in Claude Code (`~/.claude.json`) and Codex (`config.toml`):

```yaml
agents:
  mcp:
    docs: {command: npx, args: [-y, "@example/docs-mcp"], envVars: [DOCS_TOKEN]}
    search: {url: https://mcp.example.com/mcp, bearerTokenEnv: SEARCH_TOKEN}
    linear: {url: https://mcp.linear.app/mcp, oauth: true, providers: [claude, codex]}
```

- Secrets stay environment references: `${X}` for Claude and Copilot, `env_vars` and `bearer_token_env_var` for Codex, `{env:X}` for opencode.
- Codex tables are edited in place, so the rest of `config.toml`, comments included, stays byte-identical.
- A same-named server zakwas didn't add is adopted when identical and fails the plan when it differs; removing a server, or the whole `agents` section, removes only what zakwas added.
- `plan` and `check` start no server and open no browser; OAuth servers show how to log in.
- In an opencode template, write `"mcp": {{ agentMCP "opencode" }}`.

See [docs/config.md](docs/config.md#mcp).

### Agent profiles

`agents.profiles` declares a profile such as "read-only review" once, and zakwas renders it for each agent: a Claude Code subagent file (`claude --agent review`), a Codex profile file (`codex --profile review`), an opencode agent file (`opencode --agent review`), once `gemini` is named, a Gemini CLI subagent (`@review`), and once `copilot` is named, a Copilot CLI agent file (`copilot --agent review`). Gemini CLI is opt-in: zakwas renders one linked extension and one policy file for it, and never writes its `settings.json` or `~/.gemini/GEMINI.md`. Copilot CLI is opt-in too, and zakwas never writes its `config.json` or `settings.json`.

```yaml
agents:
  mcp:
    datadog: {url: https://mcp.example.com/mcp}
  profiles:
    review:
      description: Read-only change review
      prompt: dotfiles/agents/review.md
      tools: [read, search, shell]
      edit: deny
      shell: ask
      mcp: {datadog: allow}
      claude: {model: sonnet}
```

- A profile denies the MCP servers declared under `agents.mcp` that it doesn't name, and every one it names must be declared there.
- The plan lists, per provider, the profile fields that provider cannot express.
- A profile name an opencode template also defines fails the plan.
- Removing a profile removes only the files zakwas rendered.

See [docs/config.md](docs/config.md#profiles).

### Agent skills

`agents.skills` links skills that don't come from a plugin marketplace into every managed agent: Claude Code reads only `~/.claude/skills`, while Codex, opencode and Copilot CLI read `~/.agents/skills` (Copilot reads only `$COPILOT_HOME/skills` once that is set).

```yaml
agents:
  skills:
    - dotfiles/skills/review
    - {git: owner/skills, path: skills/pdf, ref: v1}
```

- A skill is a directory in the config repo or elsewhere on disk, or a path inside a git repository; its directory name must equal the `name` in its `SKILL.md`.
- A skill directory or link zakwas did not make fails the plan instead of being replaced; `npx skills` installs are never touched.
- `plan` never fetches, `apply` clones missing git sources and `zakwas upgrade` pulls them.
- Removing a skill removes only the links zakwas made.

See [docs/config.md](docs/config.md#skills).

### Agents can't apply the config

With an `agents` section, zakwas denies the managed agents `zakwas apply`, `upgrade`, `self-update` and `approve` (Claude Code's `permissions.deny`, a Codex `forbidden` rule, opencode `deny` patterns; Copilot CLI has no user-level deny rules, so only the refusal covers it), denies Claude Code edits to the config repo's main checkout and zakwas state, and refuses `apply`, `upgrade` and `approve` started from an agent session. Otherwise one prompt injection could make an agent widen its own permissions through `apply`. `plan` and `check` stay allowed, and the plan names a managed provider the guard can't cover. Set `agents.guard: false` to turn it off; zakwas then removes only the rules it added. See [docs/config.md](docs/config.md#guard).

## Protected dotfiles

`files` and `templates` write copies with write bits stripped; `protect.immutable` adds the macOS `uchg` flag (blocks `:w!`, `chmod`, `rm`). To change one, edit it in the repo and `zakwas apply`.

zakwas records the hash of every file it writes (`~/.local/state/zakwas/files.json`), so `plan` tells apart:

- repo changed → `~ … (content)`, overwritten
- installed copy edited anyway → `~ … (edited in place, back up to X.zakwas-bak)`
- protection removed → `~ … (mode 644 → 444)` / `(set immutable)`
- file dropped from the config → `- … (no longer managed)`, deleted; backed up first if it was edited

Files zakwas didn't write are backed up to `<file>.zakwas-bak` (`.zakwas-bak.N` if taken), never deleted or overwritten.

To hand-edit an installed file for a quick experiment: `chflags nouchg F && chmod u+w F`; `zakwas check` flags it until you port the change to the repo.

Every apply is logged to `~/.local/state/zakwas/history.jsonl` with the config repo commit it ran from. Applying from a dirty checkout, a branch other than `main`, or one behind upstream prints a warning.

## Safety

- `plan` and `check` never change the system. Every brew call sets `HOMEBREW_NO_AUTO_UPDATE=1`; only `zakwas upgrade` refreshes Homebrew and agent marketplaces.
- `brew.cleanup: zap` removes anything not in the Brewfile: read the `-` lines of `plan` before `apply`.
- Every third-party `tap` in the Brewfile needs `trusted: true`: `brew bundle cleanup --force` resets Homebrew's trust store to the Brewfile.
- Destinations must be absolute or `~/…` and must not overlap. zakwas refuses to write through a symlinked parent dir under `$HOME` or one resolving into the repo.

## Compared to

- **nix-darwin / home-manager**: the same "machine = repo" model and read-only dotfiles, without learning Nix. zakwas delegates packages to Homebrew and mise instead of building them, so there's no store, no evaluation, and upgrades are whatever `brew` and Renovate-bumped mise pins give you.
- **chezmoi / stow / yadm**: built around dotfiles, with scripts (chezmoi's `run_` scripts, yadm's bootstrap) for everything else. zakwas has typed modules for apps, CLI tool versions, macOS preferences and one-off steps, plans all of them before acting, and protects installed files from edits in place.
- **A Brewfile plus a shell script**: fine until the script isn't idempotent or you forget what it changed. zakwas plans before it acts, records every apply, and `check` tells you when the machine drifted.

## Documentation

- [docs/config.md](docs/config.md): every key in `zakwas.yaml`
- [docs/commands.md](docs/commands.md): commands, flags, exit codes, environment
- [docs/examples.md](docs/examples.md): snippets for common setups
- [docs/security.md](docs/security.md): what zakwas verifies and when it uses sudo
- [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md)

## Development

```bash
mise run test               # unit + e2e with fake brew/mise/defaults
mise run test:integration   # adds real `defaults` and mise round-trips
mise run lint
```

Releases: set `version` in `plugins/zakwas/.claude-plugin/plugin.json` to `X.Y.Z` in a PR, merge it, then push the `vX.Y.Z` tag on that commit; the release workflow refuses a tag that differs from the plugin version, and GoReleaser publishes darwin arm64/amd64 binaries. See [CLAUDE.md](CLAUDE.md) for layout and conventions.

## License

MIT
