# zakwas.yaml reference

`zakwas.yaml` describes one Mac. zakwas finds it by searching up from the current directory, then `$ZAKWAS_CONFIG`; `-c path` overrides both. The directory holding it is the **config root**: every `src`, `brew.file` and `mise.config` path is relative to it (absolute paths work too).

Every section is optional. Unknown keys are errors, and zakwas reports every problem in the file at once before touching anything.

- [Editor setup](#editor-setup)
- [Conventions](#conventions)
- [protect](#protect)
- [files](#files)
- [links](#links)
- [templates](#templates)
- [brew](#brew)
- [mise](#mise)
- [system](#system)
- [commands](#commands)
- [defaults](#defaults)
- [Full example](#full-example)

## Editor setup

A JSON Schema for `zakwas.yaml` lives at [`schema/zakwas.schema.json`](../schema/zakwas.schema.json) and is published at:

```
https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json
```

It gives completion, hover docs and validation (unknown keys, missing required fields, wrong enum values, decimal file modes) in any editor using [yaml-language-server](https://github.com/redhat-developer/yaml-language-server). `zakwas schema` prints the schema matching your installed binary.

### Modeline (any editor)

Put this on the first line of `zakwas.yaml`:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json
```

To pin the schema to your zakwas version instead of `main`, save it next to the config and point the modeline at the file:

```bash
zakwas schema > zakwas.schema.json
```

```yaml
# yaml-language-server: $schema=./zakwas.schema.json
```

### VS Code

Install the [YAML extension](https://marketplace.visualstudio.com/items?itemName=redhat.vscode-yaml) (Red Hat). The modeline works as is; to map the schema without one, add to `settings.json`:

```json
{
  "yaml.schemas": {
    "https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json": "zakwas.yaml"
  }
}
```

### Zed

YAML support ships with yaml-language-server, so the modeline works. Without it, in `settings.json`:

```json
{
  "lsp": {
    "yaml-language-server": {
      "settings": {
        "yaml": {
          "schemas": {
            "https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json": "zakwas.yaml"
          }
        }
      }
    }
  }
}
```

### Neovim

With `yamlls` configured through [nvim-lspconfig](https://github.com/neovim/nvim-lspconfig), the modeline works. Without it:

```lua
vim.lsp.config("yamlls", {
  settings = {
    yaml = {
      schemas = {
        ["https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json"] = "zakwas.yaml",
      },
    },
  },
})
vim.lsp.enable("yamlls")
```

Other editors (Helix, JetBrains, Sublime) that run yaml-language-server honor the modeline too.

## Conventions

**Destination paths** (`files[].dst`, `links[].dst`, `templates.files[].dst`, `system.dirs[].path`, `system.sshKey.path`) must be absolute or start with `~/`, which expands to your home directory. `$HOME` and other variables are not expanded, and relative paths are rejected.

**No overlapping destinations.** Two `files`/`links`/`templates` entries may not target the same path, or one a path inside another's directory. The comparison is case-insensitive, because APFS is by default.

**File modes are octal integers with the `0o` prefix**: `0o700`, `0o644`. Editors parse YAML 1.2, where `0700` is decimal 700 and fails validation; zakwas itself still reads `0700` as octal (YAML 1.1), so older configs keep working, but prefer `0o`. `700` with no prefix is decimal everywhere (octal `1274`), which zakwas rejects. Don't quote modes: `"0o644"` is a string, not a mode.

**Quote strings that look like something else.** YAML reads unquoted `true`, `yes`/`no`, `8080`, `1.5` and `2024-01-01` as booleans, numbers and dates. Fields documented as strings (`key`, `check`, `run`, `name`, `comment`, paths) must be quoted when their text looks like that, e.g. `check: "true"`; the schema flags them in your editor. `templates.vars` values are the exception: any scalar is read as its text.

**Empty sections are fine.** A key with nothing after it (`brew:`, `prune:`) is null and means the same as leaving it out.

**Backups.** When zakwas replaces a file it didn't write, or one that was edited in place, it moves it to `<file>.zakwas-bak` (`.zakwas-bak.N` if taken) first. It never deletes or overwrites such a file.

**Module order**: system → files → links → templates → brew → mise → agents → commands → defaults. `--only brew,defaults` runs a subset.

## protect

How `files` and `templates` are locked down after zakwas writes them.

| Key | Type | Default | Description |
|---|---|---|---|
| `immutable` | bool | `false` | Also set the macOS `uchg` flag, so even the owner can't edit, `chmod` or `rm` the file (`:w!` fails too) without `chflags nouchg`. |

Write bits are always stripped, with or without `immutable`. `plan` flags a file whose protection was removed (`mode 644 → 444`, `set immutable`) and `apply` restores it.

```yaml
protect:
  immutable: true
```

## files

List of repo files installed into your home directory as protected, read-only copies. Use it for every dotfile an app only reads.

| Key | Type | Required | Description |
|---|---|---|---|
| `src` | string | yes | Source in the config repo. A directory copies every file inside it, file by file. |
| `dst` | string | yes | Destination, absolute or `~/…`. |

- The copy keeps the source's read and execute bits and drops the write bits.
- zakwas records the hash of every file it writes (`~/.local/state/zakwas/files.json`), so `plan` tells a repo change (`content`, overwritten) from an in-place edit (`edited in place`, backed up, then overwritten).
- A file dropped from `files` (or from a directory source) is deleted (`no longer managed`), backed up first if it was edited.
- To change a file, edit it in the repo and run `zakwas apply`. For a quick experiment: `chflags nouchg F && chmod u+w F`; `zakwas check` reports it until you port the change back.

```yaml
files:
  - {src: dotfiles/zsh/zshrc, dst: ~/.zshrc}
  - {src: dotfiles/git, dst: ~/.config/git}     # every file under dotfiles/git
```

## links

List of plain symlinks from your home directory into the repo, for configs an app must be able to write itself (settings UIs, lock files). Links are not protected: edits through the link change the repo file.

| Key | Type | Required | Description |
|---|---|---|---|
| `src` | string | yes | Link target in the config repo. |
| `dst` | string | yes | Where the symlink is created, absolute or `~/…`. |

An existing file at `dst` is backed up before the link replaces it. zakwas refuses to write through a symlinked parent directory under `$HOME` or one resolving into the config repo (for example after moving a directory from `links` to `files`): remove the old symlink first.

```yaml
links:
  - {src: dotfiles/ssh/config, dst: ~/.ssh/config}
```

## templates

Go [`text/template`](https://pkg.go.dev/text/template) files rendered into your home directory as protected copies, for dotfiles that need your home path, per-machine values, or must be real files rather than links.

| Key | Type | Default | Description |
|---|---|---|---|
| `vars` | map of string → scalar | `{}` | Values available to every template as `{{ .Vars.name }}`. Numbers and booleans are used as their text (`port: 8080` renders `8080`). |
| `files` | list | `[]` | Templates to render (below). |

Each `files` entry:

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `src` | string | yes | | Template in the config repo. |
| `dst` | string | yes | | Destination, absolute or `~/…`. |
| `mode` | octal int | no | `0o444` | Permissions of the rendered file. Must be readable by the owner; write bits are stripped anyway. |

Templates see `{{ .Home }}` (your home directory) and `{{ .Vars.x }}`. Referencing a var that isn't defined is an error, not an empty string. Rendered files are protected and tracked exactly like `files`.

```yaml
templates:
  vars:
    name: Your Name
    email: you@example.com
  files:
    - {src: dotfiles/git/user.tmpl, dst: ~/.config/git/user, mode: 0o600}
```

```
# dotfiles/git/user.tmpl
[user]
  name = {{ .Vars.name }}
  email = {{ .Vars.email }}
[core]
  excludesFile = {{ .Home }}/.config/git/ignore
```

## brew

Homebrew packages from a [Brewfile](https://github.com/Homebrew/homebrew-bundle). Omit the section to leave Homebrew alone. When Homebrew is missing, the plan installs it first with the official installer (asks for your password).

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `file` | string | yes | | Brewfile in the config repo. |
| `cleanup` | `none` \| `uninstall` \| `zap` | no | `none` | What to do with installed packages missing from the Brewfile. |
| `upgrade` | bool | no | `false` | Upgrade outdated Brewfile packages on `apply`. |

`cleanup`:

- `none`: keep them.
- `uninstall`: remove them (`brew bundle cleanup --force`).
- `zap`: remove them and their cask app data (`--zap`). Read the `-` lines of `zakwas plan` before `apply`: anything not in the Brewfile goes.

`upgrade`: when `false`, only missing packages are installed (`brew bundle install --no-upgrade`). Every brew call sets `HOMEBREW_NO_AUTO_UPDATE=1`, so versions only move after `zakwas upgrade`, which runs `brew update` and then applies.

Every third-party `tap` in the Brewfile needs `trusted: true`, otherwise the plan fails: `brew bundle cleanup --force` resets Homebrew's trust store to the Brewfile, so trust granted any other way is lost.

```yaml
brew:
  file: Brewfile
  cleanup: zap
  upgrade: true
```

## mise

CLI tools pinned in a global [mise](https://mise.jdx.dev) config. Omit the section to leave mise alone. When mise is missing, the plan downloads a pinned release to `~/.local/bin/mise` and verifies its SHA256; a mise from the Brewfile is used instead when there is one.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `config` | string | yes | | mise `config.toml` in the config repo. Its tools are installed with `mise install`. |
| `prune` | bool | no | `false` | Remove installed tool versions no mise config on the machine references (`mise prune`). Other projects' configs count, so their tools stay. |

zakwas runs mise with this file as the global config, so bumped pins apply even before the installed copy is updated. Install the same file to `~/.config/mise/config.toml` with a `files` entry so mise uses it outside zakwas too.

```yaml
files:
  - {src: dotfiles/mise/config.toml, dst: ~/.config/mise/config.toml}
mise:
  config: dotfiles/mise/config.toml
  prune: true
```

## agents

Coding-agent plugins and the marketplaces they come from, declared once for every provider they target. Omit the section to leave agent plugins alone.

Only the `claude` provider (Claude Code) is converged so far. `codex` and `opencode` are accepted everywhere a provider is, and skipped: entries that target only them do nothing yet, so configs written for them stay valid.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `providers` | list of `claude` \| `codex` \| `opencode` | no | all three | Providers zakwas manages. Marketplaces target these unless they set their own; `prune` only touches these. |
| `marketplaces` | map | no | | Marketplace name → source string, or `{source, providers}`. The name must match the `name` in the marketplace's own manifest. |
| `plugins` | list | no | | `name@marketplace` strings, or `{id, providers}`. The marketplace must be declared under `marketplaces`. |
| `upgrade` | bool | no | `false` | Update installed plugins to the version their marketplace offers on `apply`. |
| `prune` | bool | no | `false` | Remove undeclared user-scope plugins and marketplaces of the managed providers. |

A marketplace `source` is a GitHub `owner/repo`, a git URL, or a local path starting with `./`, `../`, `~/` or `/` (relative paths resolve from the directory holding `zakwas.yaml`). A marketplace targets `agents.providers` unless it lists its own `providers`; a plugin targets its marketplace's providers unless it lists its own, which must be a subset.

For Claude Code, `apply` adds missing marketplaces (`claude plugin marketplace add`), turns on their `autoUpdate` flag (the same flag the `/plugin` menu sets, in `known_marketplaces.json` and in user settings), installs missing plugins at user scope, and enables disabled ones. zakwas never accepts a marketplace-declared install command (it never passes `-y`): such a plugin fails with the command shown, for you to review and install yourself.

`upgrade`: plugins whose marketplace manifest carries a version different from the installed one are updated (`claude plugin update`), shown in the plan as `from → to`. Plugins versioned only by commit are left to Claude's own auto-update. `plan`, `check` and `apply` never fetch marketplaces; `zakwas upgrade` refreshes the declared ones first (`claude plugin marketplace update`).

`prune`: undeclared user-scope plugins are uninstalled, then undeclared marketplaces removed. Plugins installed for a single project (project or local scope), and the marketplaces they come from, are never touched; neither are plugins from marketplaces Claude doesn't list as configured (built-in ones, `skills-dir`). Removals show in `zakwas plan` as destructive first.

The plan fails when `claude` is not on `PATH` (run `apply` again once brew or mise has installed it), when a declared marketplace is already configured from a different source, or when a declared plugin is not in its marketplace. zakwas runs `claude` from `/`, so a project's `.claude` settings don't leak in, and honors `CLAUDE_CONFIG_DIR`.

```yaml
agents:
  providers: [claude]
  upgrade: true
  prune: true
  marketplaces:
    claude-plugins-official: anthropics/claude-plugins-official
    team: {source: git@github.com:example/agent-plugins.git, providers: [claude]}
  plugins:
    - commit-commands@claude-plugins-official
    - {id: review@team, providers: [claude]}
```

## system

Machine-level setup.

| Key | Type | Default | Description |
|---|---|---|---|
| `dirs` | list | `[]` | Directories to create (below). |
| `sudoTouchID` | bool | `false` | Allow Touch ID for `sudo` by adding `pam_tid.so` to `/etc/pam.d/sudo_local`, which survives macOS updates. |
| `sshKey` | object | none | Generate an SSH key when missing (below). |

`dirs` entries:

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `path` | string | yes | | Directory, absolute or `~/…`. Missing parents are created. |
| `mode` | octal int | no | see below | Permissions. The owner must be able to read and enter it (`0o500` bits set). |

With `mode` set, it is enforced on existing directories too. Without it, a new directory gets `0o755` and an existing one keeps whatever it has, so directories like `~/Documents` keep their macOS permissions.

`sshKey`:

| Key | Type | Required | Description |
|---|---|---|---|
| `path` | string | yes | Private key path, absolute or `~/…`, e.g. `~/.ssh/id_ed25519`. |
| `comment` | string | no | Key comment (`ssh-keygen -C`), usually your email. |

The key is an ed25519 key without a passphrase, created only when `path` doesn't exist. An existing key is never touched.

```yaml
system:
  sudoTouchID: true
  dirs:
    - {path: ~/.ssh, mode: 0o700}
    - {path: ~/code}
  sshKey:
    path: ~/.ssh/id_ed25519
    comment: you@example.com
```

## commands

Guarded one-off setup steps. Entries run in order; the first failing one stops the rest, so put steps that can only succeed later (e.g. one that needs a key you import by hand) last.

| Key | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Label shown in plan and apply output. |
| `check` | string | yes | Shell snippet that exits 0 when the step is already done. |
| `run` | string | yes | Shell snippet executed while `check` fails. |

Both run with `sh -c` in your home directory. `check` runs during `plan` and `check` too, so it must not change anything; it times out after 30 seconds. After `run`, `check` runs again and must pass, or apply fails. `--diff` shows the scripts in the plan.

```yaml
commands:
  - name: rosetta
    check: /usr/bin/pgrep -q oahd
    run: softwareupdate --install-rosetta --agree-to-license
```

## defaults

macOS preferences written with `defaults write`. Each `domain` + `key` (+ `currentHost`) may appear once.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `domain` | string | yes | | Preferences domain, e.g. `com.apple.dock`, `NSGlobalDomain`. |
| `key` | string | yes | | Preference key. |
| `value` | bool \| int \| float \| string | yes | | Value to write; its YAML type picks the defaults type. |
| `restart` | string | no | built-in mapping | Process to `killall` after the value changes. |
| `currentHost` | bool | no | `false` | Write the per-host (ByHost) preferences, like `defaults -currentHost write`. |

`value` types:

| YAML | defaults type |
|---|---|
| `true`, `false` | `-bool` |
| `2` | `-int` |
| `1.5` | `-float` |
| `hello`, `"2"`, `"2024-01-01"` (quoted) | `-string` |

Lists and maps are not supported. An unquoted date like `2024-01-01` is a YAML timestamp and zakwas rejects it: quote it to write the text as a string.

`restart` defaults: `com.apple.dock` → `Dock`, `com.apple.finder` → `Finder`, `com.apple.screencapture` → `SystemUIServer`; other domains restart nothing. Changed `NSGlobalDomain` values are applied to the running session with `activateSettings` when macOS has it.

To find a key: `defaults read > a`, toggle the setting in System Settings, `defaults read > b`, `diff a b`. Add `-currentHost` to both reads for ByHost settings.

```yaml
defaults:
  - {domain: com.apple.dock, key: autohide, value: true}
  - {domain: com.apple.dock, key: tilesize, value: 48}
  - {domain: NSGlobalDomain, key: KeyRepeat, value: 2}
  - {domain: com.apple.screencapture, key: type, value: png}
  - {domain: com.apple.HIToolbox, key: AppleFnUsageType, value: 0, currentHost: true}
```

## Full example

See [`examples/`](../examples) for a complete config repo with its dotfiles and Brewfile:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json
protect:
  immutable: true

files:
  - {src: dotfiles/zsh/zshrc, dst: ~/.zshrc}
  - {src: dotfiles/git/config, dst: ~/.config/git/config}
  - {src: dotfiles/mise/config.toml, dst: ~/.config/mise/config.toml}

templates:
  vars:
    name: Your Name
  files:
    - {src: dotfiles/git/user.tmpl, dst: ~/.config/git/user}

brew:
  file: Brewfile
  cleanup: zap

mise:
  config: dotfiles/mise/config.toml
  prune: true

agents:
  providers: [claude]
  marketplaces:
    claude-plugins-official: anthropics/claude-plugins-official
  plugins:
    - commit-commands@claude-plugins-official

system:
  sudoTouchID: true
  dirs:
    - {path: ~/.ssh, mode: 0o700}
  sshKey:
    path: ~/.ssh/id_ed25519
    comment: you@example.com

commands:
  - name: rosetta
    check: /usr/bin/pgrep -q oahd
    run: softwareupdate --install-rosetta --agree-to-license

defaults:
  - {domain: com.apple.dock, key: autohide, value: true}
  - {domain: NSGlobalDomain, key: KeyRepeat, value: 2}
  - {domain: com.apple.finder, key: AppleShowAllExtensions, value: true}
```
