# zakwas

[![ci](https://github.com/Automaat/zakwas/actions/workflows/ci.yml/badge.svg)](https://github.com/Automaat/zakwas/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/Automaat/zakwas)](https://github.com/Automaat/zakwas/releases/latest)
[![license](https://img.shields.io/github/license/Automaat/zakwas)](LICENSE)

zakwas sets up a Mac from one YAML file. You list what the machine should have (dotfiles, Homebrew apps, CLI tool versions, macOS preferences, launchd jobs and the setup of your coding agents) and keep the file in a git repo. `zakwas plan` shows what would change, `zakwas apply` makes it so, and `zakwas check` tells you when the Mac drifted. It works on a fresh Mac and on the one you already use.

What it manages:

- **files / templates**: dotfiles installed as read-only copies, immutable with `protect.immutable: true` (like the Nix store); edits go through the repo
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

## Who it is for

Good fit:

- You keep dotfiles in git and want Homebrew, pinned CLI tool versions, macOS preferences and agent config declared in the same place, with a plan before anything changes.
- You use more than one coding agent (Claude Code, Codex, opencode, Gemini CLI, Copilot CLI) and want MCP servers, instructions, skills and permissions declared once. See [docs/agents.md](docs/agents.md).
- You set up Macs now and then and want to know when one drifted from the config.

Not a fit:

- Linux or Windows. zakwas is macOS only; the installer refuses to run elsewhere.
- Many machines from a server. zakwas has no server or inventory: it converges the Mac it runs on, as your user.
- Building packages. It hands that to Homebrew and mise (see [Compared to](#compared-to) for Nix).
- Keeping secrets. Keep them out of the config repo; agent MCP secrets are environment references, not values.

## Status

- **Pre-1.0.** The config format can change between minor versions until 1.0; check the [release notes](https://github.com/Automaat/zakwas/releases) before upgrading. Machine-readable output (`--json`, apply history) carries its own `format_version`.
- **macOS only.** Release binaries are darwin arm64 and amd64. CI runs on macOS 26; older macOS versions are not tested.
- **One maintainer.** Issues and PRs get answered when there is time; see [.github/SUPPORT.md](.github/SUPPORT.md).
- **Agents.** Claude Code and opencode are converged by default; Codex, Gemini CLI and Copilot CLI only when you name them. What each supports, and where the guard has gaps, is in [docs/agents.md](docs/agents.md) and [docs/security.md](docs/security.md#coding-agents).
- **Release binaries are not notarized.** `install.sh` and `zakwas self-update` verify checksums (and build provenance when a logged-in `gh` is available); see [Security model](#security-model).

Full documentation is listed [below](#documentation). If something breaks on first use, start with [docs/troubleshooting.md](docs/troubleshooting.md).

## Try it in two minutes

Nothing on your Mac changes until the last command, and that one shows its plan and asks first.

```bash
git clone https://github.com/Automaat/zakwas.git && cd zakwas
./install.sh --dir examples --no-apply     # installs only zakwas, to ~/.local/bin
export PATH="$HOME/.local/bin:$PATH"
zakwas init ~/dotfiles --add ~/.zshrc      # writes a starter repo in ~/dotfiles, nothing else
cd ~/dotfiles && zakwas plan               # read-only
zakwas apply --only files                  # confirm, then ~/.zshrc and a mise config are installed
```

- On a Mac without the Xcode Command Line Tools, run `xcode-select --install` first and wait for it to finish; `git` comes with them.
- `init` writes `zakwas.yaml`, a `Brewfile` from what Homebrew has installed, your global mise tools pinned to their current versions, and a copy of each file you `--add`. It does not apply anything.
- `plan` lists every change, one line each; read it before you apply. `apply --only files` limits the run to the `files` module: your `~/.zshrc`, plus `~/.config/mise/config.toml` when `init` found mise (an existing one is backed up first).
- After `apply`, `~/.zshrc` is read-only and immutable: edit `~/dotfiles/dotfiles/zshrc` and apply again. A warning about uncommitted changes is expected until you commit the new repo. To undo, see [Stop managing a file](docs/troubleshooting.md#stop-managing-a-file-or-remove-zakwas).
- Want to see a full config before writing your own? [`examples/`](examples) is a complete sample config repo (`zakwas.yaml`, a `Brewfile` and dotfiles) with an `agents` section: MCP servers, instructions, a skill and a profile. From the clone, `zakwas -c examples/zakwas.yaml plan --only files,agents` shows what it would do and changes nothing.
- Already on Homebrew or mise, or want Go? See [Install](#install). Push `~/dotfiles` to GitHub and any other Mac is one command away: [New Mac](#new-mac).

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
zakwas init ~/dotfiles --add ~/.zshrc  # starter config repo from this Mac
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

- Secrets stay environment references (`envVars`, `bearerTokenEnv`); a secret declared that way reaches neither the config repo nor the agents' files. `env`, `headers`, `args` and `url` are written as they are, so keep tokens out of them.
- zakwas removes only what it added. An MCP server it didn't add is adopted when identical and fails the plan when it differs; rules you wrote stay untouched.
- With an `agents` section the guard is on: the managed agents are denied `zakwas apply`, `upgrade`, `self-update`, `approve` and `mcp`, and `apply`, `upgrade` and `approve` refuse to run from an agent session, so a prompt injection can't widen an agent's own permissions. Agents keep `plan`, `check`, `capture` and `propose`. Set `agents.guard: false` to turn it off.
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

## Security model

zakwas runs as your user and changes what your config declares. What to know before you run it:

- Your config repo is code that runs as you: `commands`, templates and agent hooks execute what it says. Apply only a config you trust, and read `zakwas plan` first.
- `sudo` is used in two places, both during `apply`: the Touch ID line in `/etc/pam.d/sudo_local` (`system.sudoTouchID`) and, when Homebrew is missing, its installer. `plan` and `check` never call it. Scripts in your own `commands` run as written.
- The binaries zakwas installs are verified: release archives against `checksums.txt` and, with a logged-in `gh`, against GitHub build provenance; a mise it installs is a pinned release checked against its SHA256; the Homebrew installer comes from a pinned commit.
- A regular file zakwas did not write, or one that was edited after zakwas wrote it, is backed up to `<file>.zakwas-bak` before it is replaced or removed.
- Coding agents cannot run `apply` through zakwas: with an `agents` section the guard denies them the command (as deny rules for Claude Code, Codex and Gemini CLI; for opencode once a template renders them; Copilot CLI has only the refusal) and zakwas refuses to apply from an agent session. They can read the plan and `propose` changes, which a person approves in a terminal. The guard is a set of deny rules in each agent, not a sandbox; its limits are in [docs/security.md](docs/security.md#coding-agents).

Report a vulnerability through [SECURITY.md](SECURITY.md), not a public issue.

## Compared to

- **nix-darwin / home-manager**: the same "machine = repo" model and read-only dotfiles, without learning Nix. zakwas delegates packages to Homebrew and mise instead of building them, so there's no store, no evaluation, and upgrades are whatever `brew` and Renovate-bumped mise pins give you.
- **chezmoi / stow / yadm**: built around dotfiles, with scripts (chezmoi's `run_` scripts, yadm's bootstrap) for everything else. zakwas has typed modules for apps, CLI tool versions, macOS preferences and one-off steps, plans all of them before acting, and protects installed files from edits in place.
- **A Brewfile plus a shell script**: fine until the script isn't idempotent or you forget what it changed. zakwas plans before it acts, records every apply, and `check` tells you when the machine drifted.

## Documentation

- [docs/troubleshooting.md](docs/troubleshooting.md): first-use failures, backups, immutable files, state files, how to stop managing a file or remove zakwas
- [docs/config.md](docs/config.md): every key in `zakwas.yaml`
- [docs/commands.md](docs/commands.md): commands, flags, exit codes, environment
- [docs/examples.md](docs/examples.md): snippets for common setups
- [examples/](examples): a complete sample config repo, including an `agents` section ([examples/zakwas.yaml](examples/zakwas.yaml))
- [docs/agents.md](docs/agents.md): coding agents: what each supports and the capture, propose, approve flow
- [docs/security.md](docs/security.md): what zakwas verifies, how coding agents are kept from applying the config, when it uses sudo
- [CONTRIBUTING.md](CONTRIBUTING.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), [SECURITY.md](SECURITY.md), [.github/SUPPORT.md](.github/SUPPORT.md)

## Contributing

Issues and PRs are welcome; [CONTRIBUTING.md](CONTRIBUTING.md) covers setup (`mise run test`, `mise run lint`), how to add a module or an agent provider, and the commit and PR format. [CLAUDE.md](CLAUDE.md) has the layout and conventions. Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

MIT
