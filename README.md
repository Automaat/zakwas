# zakwas

[![ci](https://github.com/Automaat/zakwas/actions/workflows/ci.yml/badge.svg)](https://github.com/Automaat/zakwas/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/Automaat/zakwas)](https://github.com/Automaat/zakwas/releases/latest)
[![license](https://img.shields.io/github/license/Automaat/zakwas)](LICENSE)

Declarative macOS setup. Describe the machine in `zakwas.yaml`, keep it in a git repo, and `zakwas apply` converges any Mac to it: a fresh one or the one you already use.

- **files / templates**: dotfiles installed as read-only, immutable copies (like the Nix store); edits go through the repo
- **links**: plain symlinks for configs apps must write themselves
- **brew**: GUI apps and formulae from a `Brewfile` (`brew bundle`, optional zap cleanup)
- **mise**: CLI tools pinned in a global mise config
- **agents**: one declaration for Claude Code and opencode and, once you name them, Codex, Gemini CLI and Copilot CLI: plugins and marketplaces, MCP servers, global instructions rendered per agent, skills, profiles, permissions, shared command hooks, and a guard that keeps agents from applying the config
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
zakwas capture                         # list what agents have that zakwas.yaml doesn't declare (--write saves it as a proposal)
zakwas propose add-plugin foo@market   # save a config edit without applying it
zakwas proposals                       # pending proposals
zakwas approve ID                      # diff + plan, confirm, apply, commit (terminal only)
zakwas mcp --enable-apply              # serve plan/propose (and confirmed low-risk apply) to agents over MCP
```

`propose` saves a typed edit (a plugin, a permission rule, an MCP server, a brew package, a mise pin) with its plan and risk tier, and `approve` applies it from a terminal and commits it to the config repo. `capture` finds what agents got outside zakwas. `zakwas mcp` gives coding agents the same read and propose tools. See [Agents](#agents) and [docs/commands.md](docs/commands.md#propose-proposals-approve).

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
  providers: [claude, codex, opencode]   # gemini and copilot are opt-in too
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

## Agents

Declare MCP servers, instructions, skills, profiles and permissions once; zakwas renders each agent's native format. Claude Code and opencode are converged by default. Codex, Gemini CLI and Copilot CLI are opt-in: name them in `agents.providers`.

```yaml
agents:
  providers: [claude, codex]
  instructions: dotfiles/AGENTS.md    # one rendered copy per agent
  mcp:
    docs: {command: npx, args: [-y, "@example/docs-mcp"], envVars: [DOCS_TOKEN]}
  skills: [dotfiles/skills/review]
  profiles:
    review: {description: Read-only change review, edit: deny, shell: ask}
```

- Secrets stay environment references (`envVars`, `bearerTokenEnv`); nothing secret reaches the config repo or the agents' files.
- zakwas removes only what it added. A server or rule it didn't add is adopted when identical and fails the plan when it differs.
- With an `agents` section the guard is on: the managed agents are denied `zakwas apply`, `upgrade`, `self-update`, `approve` and `mcp`, and `apply` refuses to run from an agent session, so a prompt injection can't widen an agent's own permissions. Agents keep `plan`, `check`, `capture` and `propose`. Set `agents.guard: false` to turn it off.
- When you change an agent by hand, `zakwas capture` finds it, an agent or you `propose` a change, and you `zakwas approve` it in a terminal.

The [agents guide](docs/agents.md) has the provider support matrix and the day-to-day flow, [docs/examples.md](docs/examples.md) a tested recipe per feature, [docs/config.md](docs/config.md#agents) every key, and [docs/security.md](docs/security.md#coding-agents) the protection model.

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
- [docs/agents.md](docs/agents.md): coding agents: what each supports and the capture, propose, approve flow
- [docs/security.md](docs/security.md): what zakwas verifies, how coding agents are kept from applying the config, when it uses sudo
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
