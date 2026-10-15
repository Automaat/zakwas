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

Templates can also call `{{ agentPermissions "opencode" }}` or `{{ agentPermissions "opencode" "<default>" }}`, where the default is `allow`, `ask` or `deny`. It returns [`agents.permissions`](#permissions) as one line of JSON for opencode's `permission.bash`, holding only the entries that target opencode. The catch-all `"*": "<default>"` comes first when given, then `"<cmd>"` and `"<cmd> *"` for each `allow`, `review` (as `ask`) and `deny` entry, in that order. For example, `"bash": {{ agentPermissions "opencode" "ask" }}` renders as `"bash": {"*": "ask", "git status": "allow", "git status *": "allow", "git push --force": "deny", "git push --force *": "deny"}`. Without an `agents.permissions` section the output is only the catch-all, or `{}`. Any other provider fails the template.

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

The `claude` (Claude Code), `codex` (Codex) and `opencode` providers are converged. Each managed provider plans on its own: when one fails (its CLI is missing, a source conflicts), the plan reports the error and the other providers' changes still apply. Codex is opt-in: zakwas converges it only when `zakwas.yaml` names `codex` in `agents.providers` or in an entry's `providers`. Configs that rely on the default providers leave Codex alone, as they did before Codex support; once `codex` is named, entries without their own `providers` target it too.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `providers` | list of `claude` \| `codex` \| `opencode` | no | all three | Providers zakwas manages. Marketplaces target these unless they set their own; `prune` only touches these. |
| `marketplaces` | map | no | | Marketplace name → source string, or `{source, providers}`. The name must match the `name` in the marketplace's own manifest. |
| `plugins` | list | no | | `name@marketplace` strings, or `{id, providers}`. The marketplace must be declared under `marketplaces`. |
| `upgrade` | bool | no | `false` | Update installed plugins to the version their marketplace offers on `apply`. |
| `prune` | bool | no | `false` | Remove undeclared user-scope plugins and marketplaces of the managed providers. |
| `instructions` | string | no | | Global instructions file in the repo, relative to the directory holding `zakwas.yaml` (or absolute), linked to each managed provider's global instructions. See below. |
| `hooks.commands` | list | no | | Shared command hooks, each with `event`, `command`, and optional `providers`. See below. |
| `hooks.klaudiush` | bool | no | `false` | Register klaudiush's enabled providers. See below. |
| `permissions.allow`, `permissions.review`, `permissions.deny` | list | no | | Shell command prefixes the agents may run without asking, must have reviewed, or must never run. Each is a command string, or `{command, providers, justification}`. See [permissions](#permissions). |
| `opencode.skillsDir` | string | no | `~/.config/opencode/skills` | Where opencode skills are linked; absolute or `~/…`. |
| `guard` | bool | no | `true` | Keep the managed agents from applying this config. See [guard](#guard). |

A marketplace `source` is a GitHub `owner/repo` or a git URL (either optionally with `#ref`), or a local path starting with `./`, `../`, `~/` or `/` (relative paths resolve from the directory holding `zakwas.yaml`). A marketplace targets `agents.providers` unless it lists its own `providers`; a plugin targets its marketplace's providers unless it lists its own, which must be a subset.

For Claude Code, `apply` adds missing marketplaces (`claude plugin marketplace add`), turns on their `autoUpdate` flag (the same flag the `/plugin` menu sets, in `known_marketplaces.json` and in user settings), installs missing plugins at user scope, and enables disabled ones. zakwas never accepts a marketplace-declared install command (it never passes `-y`): such a plugin fails with the command shown, for you to review and install yourself.

`upgrade`: plugins whose marketplace manifest carries a newer semver version than the installed one are updated (`claude plugin update`), shown in the plan as `from → to`. Plugins versioned only by commit are left to Claude's own auto-update. `plan`, `check` and `apply` never fetch marketplaces; `zakwas upgrade` refreshes the declared ones first (`claude plugin marketplace update`).

`prune`: undeclared user-scope plugins are uninstalled, then undeclared marketplaces declared in Claude's user settings removed (`--scope user`). Plugins installed for a single project (project or local scope), the marketplaces they come from, and marketplaces declared only by a project or by older Claude versions outside user settings are never touched; neither are plugins from marketplaces Claude doesn't list as configured (built-in ones, `skills-dir`). Removals show in `zakwas plan` as destructive first.

The plan fails when `claude` is not on `PATH` (run `apply` again once brew or mise has installed it), when a declared marketplace is already configured from a different source, or when a declared plugin is not in its marketplace. zakwas runs `claude` from `/`, so a project's `.claude` settings don't leak in, and honors `CLAUDE_CONFIG_DIR` (a relative one resolves from the directory zakwas runs in).

For Codex, `apply` adds missing marketplaces (`codex plugin marketplace add`, a `#ref` becomes `--ref`) and installs missing plugins (`codex plugin add`), which also re-enables disabled ones. Codex reads both `.agents/plugins/marketplace.json` and `.claude-plugin/marketplace.json`, so the same marketplace can target both providers.

- `upgrade`: Codex has no plugin update command. Plugins whose marketplace snapshot offers a newer semver version (`.codex-plugin/plugin.json`, then `.claude-plugin/plugin.json`, then the manifest entry) are installed again with `codex plugin add`. `zakwas upgrade` runs `codex plugin marketplace upgrade` for the declared Git marketplaces, which already moves their installed plugins to the new versions; local marketplaces are read in place, so `upgrade: true` is what picks up their new versions.
- `prune`: undeclared plugins are removed (`codex plugin remove`), then undeclared marketplaces (`codex plugin marketplace remove`). Only marketplaces listed under `[marketplaces]` in Codex's `config.toml` and their plugins count; marketplaces Codex finds on its own (such as a personal `~/.agents/plugins` one) and their plugins are never touched.

The Codex plan fails when `codex` is not on `PATH`, when a declared marketplace is already in `config.toml` from a different source or ref, when a marketplace Codex finds on its own already has the declared name, when a local marketplace in `config.toml` lost its manifest (Codex can't list anything then; remove it with `codex plugin marketplace remove`), or when a declared plugin is not in its marketplace. zakwas runs `codex` from `/`, so a project's marketplace doesn't leak in, and honors `CODEX_HOME` (a relative one resolves from the directory zakwas runs in).

For opencode, which has no plugin system, zakwas links each skill of every declared plugin (the paths its `plugin.json` or marketplace entry lists under `skills`, or else each `skills/<name>/` holding a `SKILL.md`) into `~/.config/opencode/skills/<name>`. `apply` fetches the git marketplaces of those plugins with `git clone --depth 1` into `~/.local/share/zakwas/agents/<name>` (a `#ref` must be a branch or tag) and uses local ones in place; the plugin directory comes from the first of `.claude-plugin/marketplace.json` and `.agents/plugins/marketplace.json` that lists the plugin, and must be inside the marketplace repo, symlinks resolved; skill paths that resolve outside it are skipped. `plan` never fetches: a marketplace not fetched yet shows as a fetch, followed by one step that links its plugins' skills and applies the other listed changes, checking every skill name before linking any. Under `prune`, links of those plugins' skills that the fetched copy no longer ships are removed in the same step; any other removal that only shows up after the fetch is left for the next plan. `zakwas upgrade` pulls the fetched copies first; the links point into them, so skill updates need no relink. zakwas records what it linked and fetched in `~/.local/state/zakwas/opencode.json`; it only replaces or removes its own links, so a skill directory you put there yourself fails the plan instead, and a link you made to the same skill is left alone and never pruned. Two plugins shipping a skill with the same name, ignoring case, fail the plan, naming both. Like other modules, zakwas won't write through a symlinked parent under `$HOME` (this covers its own cache and state under `~/.local` too), so when `~/.config/opencode` is a link into your repo, set `opencode.skillsDir` (opencode also loads `~/.agents/skills`). `OPENCODE_CONFIG_DIR` is ignored, since tools like Orca point it at a private config; set `opencode.skillsDir` to install elsewhere (also when `XDG_CONFIG_HOME` moves opencode's config). With `prune`, links zakwas made for skills no declared plugin ships anymore are removed (also in an earlier `skillsDir`), then fetched copies of marketplaces no opencode plugin uses anymore. The plan fails when `opencode` is not on `PATH`; when no plugin names opencode, it only prunes then. All of this is strict only for plugins that name opencode, in their own `providers`, their marketplace's or `agents.providers`: a plugin that targets opencode by the all-providers default is linked where it can be and otherwise skipped (missing opencode or git, a failed fetch, which `zakwas upgrade` retries, a failed pull, which keeps the copy it has, a plugin stored elsewhere, a skill path that is taken), so a Claude-only config gets no opencode errors once opencode is installed; a skill name shipped by two plugins fails either way.

`instructions`: one file, linked like a [`links`](#links) entry to the global instructions of every provider in `agents.providers`: Claude Code's `${CLAUDE_CONFIG_DIR:-~/.claude}/CLAUDE.md`, Codex's `${CODEX_HOME:-~/.codex}/AGENTS.md` and opencode's `~/.config/opencode/AGENTS.md` (`OPENCODE_CONFIG_DIR` is ignored, as for skills, and `opencode.skillsDir` doesn't move it). A file already at the path is backed up to `<file>.zakwas-bak` first, another symlink there is replaced, and a directory fails the plan, as does a missing source; zakwas won't write through a symlinked parent. The instructions follow the plugin rules: Codex gets them only when `zakwas.yaml` names `codex`, and Claude Code and every named provider are strict, failing their plan when the link can't be made; opencode by the all-providers default is best effort, linked only once `opencode` is on `PATH`, and skipped without an error when a directory or a symlinked parent is in the way. None of them needs the agent's CLI otherwise. zakwas records the links it made in `~/.local/state/zakwas/instructions.json`: when a provider leaves `agents.providers`, its config dir moves or `instructions` is removed, the link zakwas made there is removed (a destructive change in the plan), while a link you made yourself, or one changed since, is left alone. Removing the whole `agents` section removes them too. Don't also list these paths under `files` or `links`.

```yaml
agents:
  providers: [claude, codex]
  instructions: dotfiles/AGENTS.md
  hooks:
    commands:
      - {event: beforeTool, command: /usr/local/bin/check-tool}
      - {event: stop, command: /usr/local/bin/record-stop, providers: [claude, codex]}
    klaudiush: true
  upgrade: true
  prune: true
  marketplaces:
    claude-plugins-official: {source: anthropics/claude-plugins-official, providers: [claude]}
    team: git@github.com:example/agent-plugins.git#v2
  plugins:
    - commit-commands@claude-plugins-official
    - review@team
    - {id: lint@team, providers: [codex]}
```

`hooks.commands` declares each command once. `event` is `beforeTool`, `afterTool`, `sessionStart`, or `stop`; `providers` defaults to `agents.providers`, with Codex opt-in as for plugins. Zakwas registers the command in Claude Code's `${CLAUDE_CONFIG_DIR:-~/.claude}/settings.json`, Codex's `${CODEX_HOME:-~/.codex}/hooks.json`, and a generated OpenCode plugin at `~/.config/opencode/plugins/zakwas-hooks.js` (OpenCode 1.18.29+ or 2.x). It maps the events to `PreToolUse`, `PostToolUse`, `SessionStart`, and `Stop` for Claude and Codex. In OpenCode, tool events use `tool.execute.before` and `tool.execute.after` on 1.x or `execute.before` and `execute.after` on 2.x; `sessionStart` observes `session.created`, and `stop` observes `session.idle` on 1.x or `session.execution.succeeded` on 2.x. Zakwas preserves unrelated JSON settings and hook groups, records its registrations in `~/.local/state/zakwas/command-hooks.json`, and removes only those registrations when they leave the config. A generated OpenCode plugin edited by hand is left in place when removed from the config. Codex may ask you to trust newly registered hooks through `/hooks` before running them. Commands receive provider-specific event JSON on stdin; a shared script must handle the payload and output rules of each provider. OpenCode blocks a tool when a before-tool command exits nonzero or returns a JSON `decision: block` / `hookSpecificOutput.permissionDecision: deny`; its after-tool and session hooks do not use command output. Don't also manage these agent settings or the generated plugin with `files`, `links`, or `templates`.

`agents.hooks.klaudiush: true` registers global klaudiush hooks for the providers enabled in `${XDG_CONFIG_HOME:-~/.config}/klaudiush/config.toml`. Install klaudiush and place its config with `files` or `templates` first. Zakwas checks hook targets during `plan` and runs `klaudiush init --install-hooks --global` during `apply` when registration is missing or the binary, config, or hook files changed. It records a registration fingerprint in `~/.local/state/zakwas/klaudiush.json`. Klaudiush keeps unrelated hooks. Codex's `hooks_config_path` and opencode's `plugin_path` come from the klaudiush config and can point outside default agent directories. Removing this option leaves existing registrations in place.

### permissions

`agents.permissions` is one list of shell command permissions for every agent. Each entry is a command prefix: one or more words, matched on whole words, so `git status` covers `git status --short` but not `git statusx`. Write it as a string, or as `{command, providers, justification}`:

- `providers` defaults to `agents.providers`, with Codex opt-in as for plugins: an entry without its own `providers` reaches Codex only when `zakwas.yaml` names `codex`.
- `justification` is a one-line reason Codex shows the agent when its rule prompts for or forbids a command. The other providers ignore it.

Prefixes stay plain words. Shell operators, quotes, globs, variables, backslashes, brackets and control characters (`* ? [ ] { } ( ) | & ; < > $` backtick, quotes, `\`) fail validation. A command may sit in only one list per provider, and only once.

| Decision | Claude Code | Codex | opencode |
|---|---|---|---|
| `allow` | `Bash(<cmd>:*)` in `permissions.allow` | `prefix_rule(…, decision="allow")`: runs **without approval and outside the sandbox** | `"allow"` |
| `review` | not written | `decision="prompt"` | `"ask"` |
| `deny` | `Bash(<cmd>:*)` in `permissions.deny` | `decision="forbidden"` | `"deny"` |

```yaml
agents:
  providers: [claude, codex, opencode]
  permissions:
    allow:
      - git status
      - git diff
      - sed -n
      - {command: go test, providers: [claude, opencode]}
    review:
      - git push
    deny:
      - {command: git push --force, justification: use git push --force-with-lease}
```

**Claude Code.** zakwas adds the rules to `${CLAUDE_CONFIG_DIR:-~/.claude}/settings.json` and leaves the rest of the file, including key order, as it is. It records the rules it added in `~/.local/state/zakwas/agent-permissions.json` and later removes only those. A rule that was already in the file stays yours: it is never claimed or removed. `review` entries are not written. Claude Code's auto mode already reviews every command that isn't allowed, while an `ask` rule would stop for a person even in auto mode. So on Claude an `allow` entry that is a prefix of a `review` entry wins: allowing `git` also runs `git push` without review. When zakwas needs to change a read-only `settings.json`, such as one installed with `files`, the Claude plan fails. If Claude Code saves `settings.json` while zakwas edits it, zakwas redoes its edit on the new content instead of overwriting it, and gives up after three tries.

**Codex.** zakwas owns `${CODEX_HOME:-~/.codex}/rules/zakwas.rules`, a regular file holding one `prefix_rule` per entry. Codex loads every `*.rules` file in that directory and saves its own approvals to `default.rules`, which zakwas never touches. When several rules match a command, Codex applies the strictest decision from any file (`forbidden` over `prompt` over `allow`). A `review` or `deny` entry therefore overrides a broader `allow` Codex saved in `default.rules`, and a zakwas `allow` cannot loosen a stricter rule elsewhere.

Codex runs an `allow` command without approval and outside its sandbox, with full disk write and network access. For Codex, allow only read-only commands that can't run other programs. Test and build runners such as `go test`, `cargo test`, `npm test` or `mise run` work fine inside the Codex sandbox, so scope them to `providers: [claude, opencode]`.

If you edit `zakwas.rules` by hand, zakwas backs it up to `zakwas.rules.zakwas-bak` before rewriting it. The file is removed when no entry targets Codex anymore; a hand-edited file is left in place and only forgotten. One unparsable rules file makes Codex ignore every rule, `forbidden` ones included, so zakwas refuses to render anything but plain printable strings.

**opencode.** zakwas doesn't edit `opencode.json`, which is usually managed as a [template](#templates). Render the rules into it with the `agentPermissions` template function instead:

```json
{
  "permission": {
    "bash": {{ agentPermissions "opencode" "ask" }}
  },
  "agent": {
    "build": {
      "permission": {
        "bash": {{ agentPermissions "opencode" "allow" }}
      }
    }
  }
}
```

opencode applies the last pattern that matches. The function therefore writes the optional catch-all `"*"` first, then the `allow`, `review` (`ask`) and `deny` entries in that order, so a deny always wins. Each entry becomes two patterns, `"<cmd>"` and `"<cmd> *"`. opencode merges its built-in defaults, the global `permission`, then each agent's own `permission`. An agent block that declares its own `bash`, for example `{"*": "allow"}`, overrides the global rules for that agent, so call the function in every agent block that declares `bash` too.

`zakwas plan` lists the added and removed rules per file (`--diff` shows them), and `zakwas check` reports drift when `settings.json`, `zakwas.rules` or the rendered `opencode.json` no longer match. Removing `permissions`, or the whole `agents` section, removes the Claude rules and the Codex file zakwas wrote. Don't also manage `zakwas.rules` with `files`, `links` or `templates`.

### guard

An agent that can run `zakwas apply`, or edit the config repo or zakwas state, can widen its own permissions, add hooks or install marketplaces, because `apply` writes agent config files the agent may not edit itself. One prompt injection in a README is enough. So with an `agents` section, zakwas keeps the managed agents from applying the config unless you set `guard: false`:

- **Deny rules.** Every managed agent is denied `zakwas apply`, `zakwas upgrade` and `zakwas self-update`, also with a flag before the command (`zakwas -c zakwas.yaml apply`) and, for Claude Code and opencode, when run by path (`~/.local/bin/zakwas apply`). Claude Code gets `Bash(zakwas …)` rules in `permissions.deny`, which hold in every permission mode, `bypassPermissions` included. Codex gets `forbidden` rules in `zakwas.rules`, for the commands as written (`zakwas apply`, not `zakwas -c … apply`). opencode gets `deny` patterns through the [`agentPermissions`](#permissions) template function. They sit with the rules from `permissions`, so removing them follows the same rules: zakwas removes only what it added.
- **Edits.** Claude Code is also denied edits (`Edit(…)` rules, which cover its file tools) to the config repo's main checkout, and to the checkout zakwas runs from when that is a linked worktree of it, and to `~/.local/state/zakwas`. Worktrees agents create outside that checkout stay editable, so an agent can still propose a change as a branch; one created inside it is covered by the rule. A repo that holds your home directory can't be denied, and the plan says so. These rules don't stop a shell command that writes a file; the command rules above are what stop `apply`. They match the command as the agent writes it, so a wrapper (`sh -c`, a script, a PTY) is outside what they stop.
- **Refusal.** `zakwas apply` and `zakwas upgrade` started from an agent session exit 1 with an explanation, before they change anything. zakwas recognizes a session by the environment variables the agents set (`CLAUDECODE`, `CODEX_SANDBOX`, `OPENCODE`, `GEMINI_CLI`, …). An agent can unset them, so this catches accidents and injections that don't know about it; the deny rules are the boundary. `plan` and `check` work in a session, so an agent can review drift and propose changes.
- **Providers that can't be protected.** The plan lists a managed provider the guard can't cover, under `agents`: opencode when no template renders `agentPermissions "opencode"` into its `opencode.json`. Such a warning doesn't count as a change, so `check` stays clean.

Codex is guarded only when `zakwas.yaml` names `codex`, like its other settings. Run `apply` in a terminal outside the agent. `guard: false` removes the rules zakwas added on the next `apply` and turns the refusal off; rules you wrote yourself stay.

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
