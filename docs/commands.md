# Command reference

```
zakwas [flags] <command>
```

Flags go before or after the command: `zakwas -y apply` and `zakwas apply -y` are the same. Go's flag parser accepts one or two dashes for every flag (`-only` = `--only`).

| Command | What it does |
|---|---|
| [`plan`](#plan) | show pending changes |
| [`apply`](#apply) | show pending changes, confirm, apply them |
| [`upgrade`](#upgrade) | `brew update`, then apply |
| [`check`](#check) | exit 2 when anything drifted |
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
| `--only LIST` | all four | comma-separated modules to run: `system`, `files`, `links`, `templates`, `brew`, `mise`, `commands`, `defaults` |
| `--diff` | all four | show file content diffs and command scripts |
| `--json` | all four | machine-readable output: one plan document (`plan`, `check`) or JSON lines events (`apply`, `upgrade`) |
| `--no-color` | all four | disable colors (also `NO_COLOR`, or stdout not a terminal) |
| `-y` | `apply`, `upgrade` | apply without asking for confirmation |
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

### upgrade

Runs `brew update`, then `apply`, so casks and formulae pick up new versions. Needs a `brew` section. Every other command runs brew with `HOMEBREW_NO_AUTO_UPDATE=1`.

### check

Like `plan`, but exits 2 when anything would change. For CI, cron or launchd.

```bash
zakwas check || notify "machine drifted"
zakwas check --json
```

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
| `Brewfile` | Homebrew installed | `brew bundle dump`; third-party taps this Mac already has get `trusted: true` |
| `dotfiles/mise/config.toml` | mise installed | every active global tool (`mise ls --global --current`) pinned to the exact version in use; installed to `~/.config/mise/config.toml` |
| `dotfiles/<path>` | per `--add` | copy of the file or directory |
| `.gitignore` | always | `*.zakwas-bak*` |
| `.git` | always | `git init -b main` |

Sections for tools that aren't installed are left out. `brew.cleanup` starts as `none`; switch to `zap` once the Brewfile lists everything you want to keep.

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

If zakwas already manages files on this Mac from another config, init warns: `plan` in the new repo lists those files as no longer managed, and `apply` deletes them.

## self-update

```
zakwas self-update [--version X.Y.Z]
```

| Flag | Meaning |
|---|---|
| `--version X.Y.Z` | release to install (a leading `v` is fine); default: latest |

Resolves the latest release from the `https://github.com/Automaat/zakwas/releases/latest` redirect (not the rate-limited API), downloads `zakwas_<version>_darwin_<arm64|amd64>.tar.gz` and `checksums.txt`, verifies the SHA256, and replaces the running binary atomically (temp file in the same directory, `chmod 0755`, rename). Prints a message and does nothing when that version is already running.

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

Prints the JSON Schema of `zakwas.yaml`. Editors with the YAML language server use it through the first line `zakwas init` writes:

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
| 0 | success; `check` found no drift |
| 1 | error; also when a module failed to plan (the others still apply) |
| 2 | `check` found drift |
| 64 | usage error: unknown command or flag, conflicting flags, invalid `init`/`self-update` arguments, package-managed binary |
| 130 | interrupted (Ctrl-C) |

## Environment

| Variable | Used by | Effect |
|---|---|---|
| `ZAKWAS_CONFIG` | config commands | config path when no `zakwas.yaml` is found upward from the current directory; `-c` overrides both |
| `NO_COLOR` | config commands | set to anything to disable colors ([no-color.org](https://no-color.org)) |
| `MISE_DATA_DIR`, `XDG_DATA_HOME` | `self-update` | where mise installs live, to detect a mise-managed zakwas |
| `HOMEBREW_PREFIX` | `self-update` | extra Homebrew prefix to detect a Homebrew-managed zakwas |
