# zakwas

Declarative macOS setup. Describe the machine in `zakwas.yaml`, keep it in a git repo, and `zakwas apply` converges any Mac to it: a fresh one or the one you already use.

- **files / templates**: dotfiles installed as read-only, immutable copies (like the Nix store); edits go through the repo
- **links**: plain symlinks for configs apps must write themselves
- **brew**: GUI apps and formulae from a `Brewfile` (`brew bundle`, optional zap cleanup)
- **mise**: CLI tools pinned in a global mise config
- **defaults**: macOS preferences (`defaults write`, restarts Dock/Finder when needed)
- **system**: Touch ID for sudo, directories, SSH key
- **commands**: guarded one-off steps (`run` only while `check` fails)

*Zakwas* is Polish for sourdough starter: keep it, feed it, and every new loaf comes out the same.

## New Mac

```bash
curl -fsSL https://raw.githubusercontent.com/Automaat/zakwas/main/install.sh |
  bash -s -- --repo https://github.com/you/dotfiles.git
```

Installs the Xcode Command Line Tools and zakwas (checksum-verified, to `~/.local/bin/zakwas`), clones your config to `~/dotfiles` (`--dir` to change), then runs `zakwas apply`. `--no-apply` stops before applying, for configs that need manual steps first (e.g. registering an SSH key). `--version X.Y.Z` pins the release; re-run with `--no-apply` to update zakwas.

zakwas needs nothing else: when the config has a `brew` section and Homebrew is missing, the plan installs it first (official installer, asks for your password); when it has a `mise` section and mise is missing, it downloads a pinned mise release to `~/.local/bin/mise`, verifies its SHA256, then installs your tools with it. If the Brewfile lists `mise`, the Homebrew one is used instead.

## Usage

```bash
zakwas plan            # what would change (--diff shows file contents)
zakwas apply           # show plan, confirm, apply (-y skips the prompt)
zakwas upgrade         # brew update, then apply: upgrades casks and formulae
zakwas check           # exit 2 when the machine drifted (cron/launchd friendly)
zakwas plan --only brew,defaults
```

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

See [`examples/`](examples) for a complete config repo.

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

Modules run in this order: system → files → links → templates → brew → mise → commands → defaults.

Find a macOS preference key: `defaults read > a`, toggle it in System Settings, `defaults read > b`, `diff a b`.

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

- `plan` and `check` never change the system. Every brew call sets `HOMEBREW_NO_AUTO_UPDATE=1`; only `zakwas upgrade` refreshes Homebrew.
- `brew.cleanup: zap` removes anything not in the Brewfile: read the `-` lines of `plan` before `apply`.
- Every third-party `tap` in the Brewfile needs `trusted: true`: `brew bundle cleanup --force` resets Homebrew's trust store to the Brewfile.
- Destinations must be absolute or `~/…` and must not overlap. zakwas refuses to write through a symlinked parent dir under `$HOME` or one resolving into the repo.

## Development

```bash
mise run test               # unit + e2e with fake brew/mise/defaults
mise run test:integration   # adds real `defaults` and mise round-trips
mise run lint
```

Releases: push a `vX.Y.Z` tag; GoReleaser publishes darwin arm64/amd64 binaries. See [CLAUDE.md](CLAUDE.md) for layout and conventions.

## License

MIT
