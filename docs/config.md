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
- [launchd](#launchd)
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

**Module order**: system → files → links → templates → brew → mise → agents → commands → launchd → defaults. `--only brew,defaults` runs a subset.

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

`{{ agentMCP "opencode" }}` returns [`agents.mcp`](#mcp) as one line of JSON for opencode's `mcp` key, holding the servers that target opencode, sorted by name. Any other provider fails the template.

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

The `claude` (Claude Code), `codex` (Codex), `opencode`, `gemini` (Gemini CLI) and `copilot` (GitHub Copilot CLI) providers are converged. Each managed provider plans on its own: when one fails (its CLI is missing, a source conflicts), the plan reports the error and the other providers' changes still apply. Codex, Gemini and Copilot are opt-in: zakwas converges them only when `zakwas.yaml` names `codex`, `gemini` or `copilot` in `agents.providers` or in an entry's `providers`. Configs that rely on the default providers leave them alone, as they did before; once one is named, entries without their own `providers` target it too. Gemini has no plugin marketplaces, so `marketplaces` and `plugins` never target it: it gets instructions, MCP servers, skills, command hooks, permissions and profiles, see [Gemini CLI](#gemini-cli).

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `providers` | list of `claude` \| `codex` \| `opencode` \| `gemini` \| `copilot` | no | `claude`, `codex`, `opencode`, with `codex`, `gemini` and `copilot` opt-in | Providers zakwas manages. Marketplaces target these unless they set their own; `prune` only touches these. |
| `marketplaces` | map | no | | Marketplace name → source string, or `{source, providers}`. The name must match the `name` in the marketplace's own manifest. |
| `plugins` | list | no | | `name@marketplace` strings, or `{id, providers}`. The marketplace must be declared under `marketplaces`. |
| `upgrade` | bool | no | `false` | Update installed plugins to the version their marketplace offers on `apply`. |
| `prune` | bool | no | `false` | Remove undeclared user-scope plugins and marketplaces of the managed providers. |
| `instructions` | string or list | no | | Global instructions: one file in the repo, or a list joined in order, relative to the directory holding `zakwas.yaml` (or absolute). Rendered per provider into each managed provider's global instructions. See [instructions](#instructions). |
| `sharedInstructions` | bool | no | `false` | Also write the rendered instructions to `~/.agents/AGENTS.md`. See [instructions](#instructions). |
| `hooks.commands` | list | no | | Shared command hooks, each with `event`, `command`, and optional `providers`. See below. |
| `hooks.klaudiush` | bool | no | `false` | Register klaudiush's enabled providers. See below. |
| `permissions.allow`, `permissions.review`, `permissions.deny` | list | no | | Shell command prefixes the agents may run without asking, must have reviewed, or must never run. Each is a command string, or `{command, providers, justification}`. See [permissions](#permissions). |
| `mcp` | map | no | | MCP server name → `{command, args, env, envVars}` for a stdio server or `{url, headers, headerEnv, bearerTokenEnv, oauth}` for an HTTP one, each with optional `providers`. See [mcp](#mcp). |
| `codex.settings` | map | no | | Selected global Codex config.toml values, edited in place independently of profiles. See [global Codex settings](#global-codex-settings). |
| `profiles` | map | no | | Profile name → what it may do, declared once and rendered for Claude Code, Codex, opencode and Gemini CLI: `description`, `prompt`, `tools`, `edit`, `shell`, `sandbox`, `network`, `reviewer`, `mcp`, `providers` and per-provider `claude`, `codex`, `opencode`, `gemini` blocks. See [profiles](#profiles). |
| `skills` | list | no | | Standalone skills linked into every managed agent: a path, or `{path, git, ref, providers}`. See [skills](#skills). |
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

For Copilot CLI, which reads Claude-format marketplaces, `apply` adds missing marketplaces (`copilot plugin marketplace add`, a `#ref` kept in the source) and installs missing plugins (`copilot plugin install`), and enables an installed one that is disabled (`copilot plugin enable`).

- `upgrade`: Copilot lists no version for a plugin it installed from a Git marketplace, so the plan can't tell an outdated one. With `upgrade: true`, `zakwas upgrade` runs `copilot plugin marketplace update` for the declared marketplaces, then `copilot plugin update` for their declared plugins; plugins of a local marketplace are loaded live from it and need no update.
- `prune`: undeclared plugins are uninstalled (`copilot plugin uninstall`), then undeclared marketplaces removed (`copilot plugin marketplace remove`). Copilot's built-in marketplaces (`copilot-plugins`, `awesome-copilot`) and their plugins, plugins installed straight from a repository, and marketplaces that still have a plugin installed are never touched. Copilot only disables an uninstalled plugin of a local marketplace; the plan counts it as gone.

The Copilot plan fails when `copilot` is not on `PATH`, or when a declared marketplace is already added from a different source or ref (Copilot's list omits the ref, so zakwas reads it from `settings.json`). A declared plugin missing from its marketplace fails its install on apply: the plan never browses a marketplace, since Copilot clones one that isn't cached yet. zakwas runs `copilot` from `/` and honors `COPILOT_HOME` (a relative one resolves from the directory zakwas runs in). zakwas never writes Copilot's `config.json` or `settings.json`; Copilot keeps its own marketplace and plugin state in them.

For opencode, which has no plugin system, zakwas links each skill of every declared plugin (the paths its `plugin.json` or marketplace entry lists under `skills`, or else each `skills/<name>/` holding a `SKILL.md`) into `~/.config/opencode/skills/<name>`. `apply` fetches the git marketplaces of those plugins with `git clone --depth 1` into `~/.local/share/zakwas/agents/<name>` (a `#ref` must be a branch or tag) and uses local ones in place; the plugin directory comes from the first of `.claude-plugin/marketplace.json` and `.agents/plugins/marketplace.json` that lists the plugin, and must be inside the marketplace repo, symlinks resolved; skill paths that resolve outside it are skipped. `plan` never fetches: a marketplace not fetched yet shows as a fetch, followed by one step that links its plugins' skills and applies the other listed changes, checking every skill name before linking any. Under `prune`, links of those plugins' skills that the fetched copy no longer ships are removed in the same step; any other removal that only shows up after the fetch is left for the next plan. `zakwas upgrade` pulls the fetched copies first; the links point into them, so skill updates need no relink. zakwas records what it linked and fetched in `~/.local/state/zakwas/opencode.json`; it only replaces or removes its own links, so a skill directory you put there yourself fails the plan instead, and a link you made to the same skill is left alone and never pruned. Two plugins shipping a skill with the same name, ignoring case, fail the plan, naming both. Like other modules, zakwas won't write through a symlinked parent under `$HOME` (this covers its own cache and state under `~/.local` too), so when `~/.config/opencode` is a link into your repo, set `opencode.skillsDir` (opencode also loads `~/.agents/skills`). `OPENCODE_CONFIG_DIR` is ignored, since tools like Orca point it at a private config; set `opencode.skillsDir` to install elsewhere (also when `XDG_CONFIG_HOME` moves opencode's config). With `prune`, links zakwas made for skills no declared plugin ships anymore are removed (also in an earlier `skillsDir`), then fetched copies of marketplaces no opencode plugin uses anymore. The plan fails when `opencode` is not on `PATH`; when no plugin names opencode, it only prunes then. All of this is strict only for plugins that name opencode, in their own `providers`, their marketplace's or `agents.providers`: a plugin that targets opencode by the all-providers default is linked where it can be and otherwise skipped (missing opencode or git, a failed fetch, which `zakwas upgrade` retries, a failed pull, which keeps the copy it has, a plugin stored elsewhere, a skill path that is taken), so a Claude-only config gets no opencode errors once opencode is installed; a skill name shipped by two plugins fails either way.

`instructions`: see [instructions](#instructions).

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

`hooks.commands` declares each command once. `event` is `beforeTool`, `afterTool`, `sessionStart`, or `stop`; `providers` defaults to `agents.providers`, with Codex, Gemini and Copilot opt-in as for plugins. Zakwas registers the command in Claude Code's `${CLAUDE_CONFIG_DIR:-~/.claude}/settings.json`, Codex's `${CODEX_HOME:-~/.codex}/hooks.json`, the `hooks/hooks.json` of the [Gemini extension](#gemini-cli), a hooks file of its own for Copilot CLI, `${COPILOT_HOME:-~/.copilot}/hooks/zakwas.json` (a protected copy like [`instructions`](#instructions); Copilot reads every `*.json` in that directory, so none of its other files are edited; the events become `preToolUse`, `postToolUse`, `sessionStart` and `agentStop`, each command a `bash` hook; its hash is in `~/.local/state/zakwas/agent-hook-files.json`), and a generated OpenCode plugin at `~/.config/opencode/plugins/zakwas-hooks.js` (OpenCode 1.18.29+ or 2.x). It maps the events to `PreToolUse`, `PostToolUse`, `SessionStart`, and `Stop` for Claude and Codex, and to `BeforeTool`, `AfterTool`, `SessionStart` and `AfterAgent` for Gemini; `AfterAgent` fires once per turn, after the final response, so `stop` runs when a Gemini turn ends. In OpenCode, tool events use `tool.execute.before` and `tool.execute.after` on 1.x or `execute.before` and `execute.after` on 2.x; `sessionStart` observes `session.created`, and `stop` observes `session.idle` on 1.x or `session.execution.succeeded` on 2.x. Zakwas preserves unrelated JSON settings and hook groups, records its registrations in `~/.local/state/zakwas/command-hooks.json`, and removes only those registrations when they leave the config. A generated OpenCode plugin edited by hand is left in place when removed from the config. Codex may ask you to trust newly registered hooks through `/hooks` before running them. Commands receive provider-specific event JSON on stdin; a shared script must handle the payload and output rules of each provider. OpenCode blocks a tool when a before-tool command exits nonzero or returns a JSON `decision: block` / `hookSpecificOutput.permissionDecision: deny`; its after-tool and session hooks do not use command output. Don't also manage these agent settings or the generated plugin with `files`, `links`, or `templates`.

`agents.hooks.klaudiush: true` registers global klaudiush hooks for the providers enabled in `${XDG_CONFIG_HOME:-~/.config}/klaudiush/config.toml`. Install klaudiush and place its config with `files` or `templates` first. Zakwas checks hook targets during `plan` and runs `klaudiush init --install-hooks --global` during `apply` when registration is missing or the binary, config, or hook files changed. It records a registration fingerprint in `~/.local/state/zakwas/klaudiush.json`. Klaudiush keeps unrelated hooks. Codex's `hooks_config_path` and opencode's `plugin_path` come from the klaudiush config and can point outside default agent directories. Removing this option leaves existing registrations in place.

### instructions

`agents.instructions` is one file, or a list of files joined in order. Each managed provider gets its own rendered, protected copy (read-only, like [`templates`](#templates) output) at its global instructions path:

| Provider | Path |
|---|---|
| Claude Code | `${CLAUDE_CONFIG_DIR:-~/.claude}/CLAUDE.md` |
| Codex | `${CODEX_HOME:-~/.codex}/AGENTS.md` |
| opencode | `~/.config/opencode/AGENTS.md` (`OPENCODE_CONFIG_DIR` is ignored, as for skills, and `opencode.skillsDir` doesn't move it) |
| Gemini CLI | `${GEMINI_CLI_HOME:-~}/.gemini/zakwas/GEMINI.md`, the context file of the [zakwas extension](#gemini-cli); `~/.gemini/GEMINI.md`, which Gemini's memory tool writes to, is never touched |
| Copilot CLI | `${COPILOT_HOME:-~/.copilot}/instructions/zakwas.instructions.md`, with `applyTo: "**"` frontmatter added so that Copilot loads it in every session |
| shared | `~/.agents/AGENTS.md`, only with `sharedInstructions: true`; read by Warp, Cline and Factory Droid, not by Claude Code, Codex or opencode |

```yaml
agents:
  providers: [claude, codex]
  instructions: [dotfiles/AGENTS.md, dotfiles/claude-only.md]
  sharedInstructions: true
```

- Files are joined in order, separated by a blank line; a file that renders empty is left out. `instructions: dotfiles/AGENTS.md`, a single file, keeps working and is copied as rendered.
- Every file is a Go template, like [`templates`](#templates), sharing `templates.vars`: `{{ .Provider }}` is `claude`, `codex`, `opencode`, `gemini`, `copilot` or `shared`, `{{ .Home }}` is your home directory, `{{ .Vars.name }}` a template variable. Branch on the provider to keep text for agents that can use it: `{{ if eq .Provider "claude" }}Use the Skill tool.{{ end }}`. A file with a literal `{{` needs `{{ "{{" }}`; an unknown variable or a syntax error fails the plan.
- `@path` imports: Claude Code resolves them itself, up to four files deep, so its copy keeps them (a path relative to the instructions file becomes absolute, since the copy lives elsewhere). Codex, opencode, Gemini and the shared copy get imports inlined, so zakwas inlines the imported file, as it is: it is not rendered as a template. A path counts as an import when it starts a line or follows whitespace, begins with `@` and contains a `/` or ends in `.md` (`@docs/git.md`, `@~/notes/me.md`, `@/abs/rules.md`); `@claude` or a path in a code block or code span is left alone. A missing import, an import cycle or one nested more than four files deep fails the plan.
- Size limits: Codex reads at most 32768 bytes of `AGENTS.md`, so a Codex copy over that, after inlining, fails the plan naming the provider and the size.
- A file already at a path is backed up to `<file>.zakwas-bak` first, one you edited later too; a symlink there, such as the link an earlier zakwas made, is replaced without a backup; a directory fails the plan, as does a missing source or a symlinked parent.
- The instructions follow the plugin rules: Codex, Gemini and Copilot get them only when `zakwas.yaml` names them, and Claude Code and every named provider are strict, failing their own plan when the copy can't be rendered or written (the other providers still apply); opencode by the all-providers default is best effort, written only once `opencode` is on `PATH` and skipped when something blocks the write. The shared copy is strict. None of them needs the agent's CLI otherwise.
- zakwas records the hash of each copy in `~/.local/state/zakwas/instruction-files.json`. When a provider leaves `agents.providers`, its config dir moves, `sharedInstructions` is turned off or `instructions` is removed, the copy zakwas wrote is removed (a destructive change in the plan; one edited since is backed up first). Removing the whole `agents` section removes them too. Links an earlier zakwas recorded in `instructions.json` are replaced by the copy on the next `apply`, or removed when no provider wants them. Don't also list these paths under `files`, `links` or `templates`.

### permissions

`agents.permissions` is one list of shell command permissions for every agent. Each entry is a command prefix: one or more words, matched on whole words, so `git status` covers `git status --short` but not `git statusx`. Write it as a string, or as `{command, providers, justification}`:

- `providers` defaults to `agents.providers`, with Codex, Gemini and Copilot opt-in as for plugins: an entry without its own `providers` reaches them only when `zakwas.yaml` names them.
- Copilot CLI keeps user-level shell permission rules only in root-owned managed settings, which zakwas can't write. An entry that reaches `copilot` is reported in the plan as unsupported, which isn't a change and fails nothing.
- `justification` is a one-line reason Codex shows the agent when its rule prompts for or forbids a command, and Gemini CLI when a `deny` rule blocks one. The other providers ignore it.

Prefixes stay plain words. Shell operators, quotes, globs, variables, backslashes, brackets and control characters (`* ? [ ] { } ( ) | & ; < > $` backtick, quotes, `\`) fail validation. A command may sit in only one list per provider, and only once.

| Decision | Claude Code | Codex | opencode | Gemini CLI |
|---|---|---|---|---|
| `allow` | `Bash(<cmd>:*)` in `permissions.allow` | `prefix_rule(…, decision="allow")`: runs **without approval and outside the sandbox** | `"allow"` | `decision = "allow"` |
| `review` | not written | `decision="prompt"` | `"ask"` | `decision = "ask_user"` |
| `deny` | `Bash(<cmd>:*)` in `permissions.deny` | `decision="forbidden"` | `"deny"` | `decision = "deny"` |

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

**Gemini CLI.** zakwas owns `${GEMINI_CLI_HOME:-~}/.gemini/policies/zakwas.toml`, one `[[rule]]` per entry on `run_shell_command` with a `commandPrefix`, which Gemini matches on whole words like zakwas does. Gemini ignores `allow` rules from extensions and ranks extension rules below user ones, so every rule lives in this user-tier file, where the priorities (`deny` 900, `review` 700, `allow` 500) keep deny over ask over allow. Gemini's own approvals go to `settings.json` or other policy files, never to this one. Edit it by hand and zakwas backs it up to `zakwas.toml.zakwas-bak` before rewriting it; it is removed when nothing targets Gemini anymore.

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

### mcp

`agents.mcp` declares each MCP server once, under a name (letters, digits, `_` and `-`), and zakwas registers it at user scope in Claude Code, Codex and Copilot CLI and in the [Gemini extension](#gemini-cli). A server has either a `command` (stdio) or a `url` (HTTP); `providers` defaults to `agents.providers`, with Codex, Gemini and Copilot opt-in as for plugins.

| Key | For | Description |
|---|---|---|
| `command`, `args` | stdio | The command that starts the server, and its arguments. |
| `env` | stdio | Environment variables with their values. Written to the agents' config as is. |
| `envVars` | stdio | Names of environment variables passed through from the agent's own environment. |
| `url` | HTTP | `http://` or `https://` URL of the server. |
| `headers` | HTTP | Header names with their values. Written to the agents' config as is. |
| `headerEnv` | HTTP | Header name → name of the environment variable holding its value. |
| `bearerTokenEnv` | HTTP | Name of the environment variable holding a bearer token, sent as `Authorization: Bearer <token>`. |
| `oauth` | HTTP | The server signs in with OAuth; `plan` and `apply` show the login command. Not combined with `bearerTokenEnv` or an `Authorization` header. |
| `providers` | both | Providers to register the server with; must be in `agents.providers`. |

Secrets never go into the config zakwas writes: `envVars`, `headerEnv` and `bearerTokenEnv` name the environment variable and each agent reads it in its own way. `env`, `headers`, `args` and the `url` are written as they are, so keep tokens out of them (a `url` can't hold a user name or password, and `plan` diffs hide query values).

```yaml
agents:
  providers: [claude, codex, opencode]
  mcp:
    docs:
      command: npx
      args: [-y, "@example/docs-mcp"]
      envVars: [DOCS_TOKEN]
    search:
      url: https://mcp.example.com/mcp
      bearerTokenEnv: SEARCH_TOKEN
    linear:
      url: https://mcp.linear.app/mcp
      oauth: true
      providers: [claude, codex]
```

| Provider | Where | A secret variable `X` becomes |
|---|---|---|
| Claude Code | `mcpServers` in `.claude.json`: `${CLAUDE_CONFIG_DIR}/.claude.json`, or `~/.claude.json` | `${X}` in `env` or `headers` |
| Codex | `[mcp_servers.<name>]` in `${CODEX_HOME:-~/.codex}/config.toml` | `env_vars`, `bearer_token_env_var`, `env_http_headers` |
| Gemini CLI | `mcpServers` in `gemini-extension.json` of the zakwas extension, `httpUrl` for an HTTP server | `$X` in `env` or `headers` |
| Copilot CLI | `mcpServers` in `${COPILOT_HOME:-~/.copilot}/mcp-config.json` | `${X}` in `env` or `headers`, which Copilot expands from its own environment |
| opencode | the `agentMCP` template function (below) | `{env:X}` in `environment` or `headers` |

**Claude Code.** `claude mcp get` starts the server it reports on, so zakwas reads `.claude.json` instead and edits only the entry of the server it changes. The rest of the file keeps its key order and values. If Claude Code saves the file while zakwas edits it, zakwas redoes its edit on the new content, and gives up after three tries.

**Codex.** `codex mcp add` rewrites every `[mcp_servers.*]` table and drops their comments, and it opens a browser for OAuth servers. zakwas edits the server's own tables in `config.toml` as text instead: a changed or removed server rewrites only the lines from its `[mcp_servers.<name>]` header to its last key, subtables included. Everything else, comments included, stays byte-identical, and a new server is appended at the end of the file. Comments and blank lines after a server's last key stay, as does everything above its header, and the file's line endings and final newline are kept. A server written as an inline table (`[mcp_servers]` with `name = { … }`) can't be edited in place: if it differs from `zakwas.yaml`, or has to change or go, the plan fails and says to do it by hand.

**Copilot CLI.** zakwas edits the user-level `mcpServers` of `mcp-config.json` in place, the way `copilot mcp add` does (a stdio server is `type: local`, an HTTP one `type: http`, each with `tools: ["*"]`), and keeps the file's other servers and keys, so servers added with `copilot mcp add` stay. Servers from a workspace `.mcp.json` or a plugin live elsewhere and are never touched. An OAuth server needs `/mcp auth <name>` in Copilot CLI.

**Gemini CLI.** The servers are part of the extension's manifest, a file zakwas owns, so there is no per-server ownership: removing a server removes it from the manifest. Gemini expands `$X` and `${X}` anywhere in `gemini-extension.json` when it loads the extension, from its own environment, so `envVars`, `headerEnv` and `bearerTokenEnv` work as they do for the other agents. A literal `$` in a server's `command`, `args`, `url`, `env` or `headers` would be replaced the same way, so zakwas leaves such a server out of Gemini and the plan says so; so it does for a server whose name has an underscore, which Gemini's policy rules misread. Scope those servers to the other providers. A server with `oauth: true` is registered as is; sign in with `/mcp auth <name>` in Gemini CLI.

**opencode.** zakwas doesn't edit `opencode.json`, which is usually a [template](#templates). Render the servers into its `mcp` key with `agentMCP`:

```json
{
  "mcp": {{ agentMCP "opencode" }}
}
```

A server targets opencode through its `providers` or, without them, `agents.providers`. A stdio server renders as `{"type": "local", "command": [command, args…], "environment": {…}, "enabled": true}` and an HTTP one as `{"type": "remote", "url": …, "headers": {…}, "enabled": true}`; a server with headers also gets `"oauth": false`, so opencode doesn't start a login for it.

**Prompting before a tool.** A server started as `zakwas mcp --enable-apply` (the command's base name is `zakwas`, its first argument `mcp`; a launcher such as `mise exec --` isn't recognized) has an always-prompt tool, `zakwas_apply` ([`zakwas mcp`](commands.md#mcp)). Claude Code asks before it from the tool's own metadata. For Codex zakwas adds `[mcp_servers.<name>.tools.zakwas_apply]` with `approval_mode = "prompt"` to the server's tables. opencode's `permission` object is part of `opencode.json`, which zakwas only renders pieces of, so the plan lists a warning (not a change) naming the entry to add, `"<name>_zakwas_apply": "ask"`, until a template for `opencode.json` contains it.

**Ownership.** zakwas records the servers it registered, with a hash of each entry, in `~/.local/state/zakwas/agent-mcp.json`:

- A server of the same name that zakwas did not add is adopted, without rewriting its file, when it is identical to the declaration. When it differs, the provider's plan fails and names the differing keys, never their values; remove the server or make `zakwas.yaml` match.
- Changing a declared server changes only that server. Removing it, removing `mcp`, or removing the whole `agents` section removes only servers zakwas registered, shown as destructive in the plan. A server edited since zakwas wrote it is left in place and only forgotten when it goes; when it is still declared and differs from `zakwas.yaml`, the plan fails instead of resetting your edit, naming the differing keys.
- When the config dir moves (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`), the servers zakwas registered in the old file are removed there.

`plan` and `check` only read the config files: they start no MCP server and open no browser. A server with `oauth: true` shows how to sign in, because zakwas never does: `/mcp` in Claude Code, `codex mcp login <name>` for Codex. The plan fails when a config file zakwas has to change is read-only or not a regular file, such as one installed with `files`.

### Global Codex settings

`agents.codex.settings` updates selected values in `${CODEX_HOME:-~/.codex}/config.toml` using native Codex field names. Explicitly name `codex` in `agents.providers` or a marketplace/plugin entry's `providers`. Settings apply globally, independently of `agents.profiles`, without requiring the Codex CLI.

```yaml
agents:
  providers: [codex]
  codex:
    settings:
      agents:
        enabled: true
        max_concurrent_threads_per_session: 8
```

Values accept strings, booleans, finite numbers, scalar arrays and nonempty nested mappings. Quote YAML values that must stay strings. This is configuration data: no templates, commands or secret expansion. Keep credentials outside this mapping. Use the dedicated agents fields for `profiles`, `mcp_servers`, `plugins` and `marketplaces`; these keys are rejected here.

Only declared leaves are updated. Removing a value or the settings block leaves its current value in Codex. The plan shows each changed value. Apply rereads the live file, preserving concurrent unrelated edits, comments, MCP registrations and other settings. New files use mode `0600`; existing file permissions stay unchanged. Invalid TOML, read-only files, unsafe paths and conflicting inline-table representations fail without replacing the file. An unchanged inline value needs no edit. Run `zakwas apply` outside an agent session while the agents guard is enabled.

### profiles

`agents.profiles` declares a permission profile, such as "read-only review" or "build without network", once. zakwas renders it in each provider's own format, so the copies cannot drift. Each profile starts a session in its provider:

| Provider | File | Start with |
|---|---|---|
| Claude Code | `${CLAUDE_CONFIG_DIR:-~/.claude}/agents/zakwas/<name>.md` (a subagent file; Claude Code scans the directory recursively) | `claude --agent <name>` |
| Codex | `${CODEX_HOME:-~/.codex}/<name>.config.toml` (layered over `config.toml`) | `codex --profile <name>` |
| opencode | `~/.config/opencode/agents/<name>.md` | `opencode --agent <name>` |
| Gemini CLI | `${GEMINI_CLI_HOME:-~}/.gemini/zakwas/agents/<name>.md` (a subagent of the [zakwas extension](#gemini-cli)) | `@<name>` at the start of a prompt, or let Gemini delegate |
| Copilot CLI | `${COPILOT_HOME:-~/.copilot}/agents/<name>.agent.md` (a custom agent; only when `copilot` is named) | `copilot --agent <name>` |

```yaml
agents:
  mcp:
    datadog: {url: https://mcp.example.com/mcp}
    devtools: {command: devtools-mcp}
  profiles:
    review:
      description: Read-only change review
      prompt: dotfiles/agents/review.md
      tools: [read, search, shell]
      edit: deny
      shell: ask
      network: false
      mcp: {datadog: allow}
      claude: {model: sonnet}
      codex: {model: gpt-5, overrides: {model_reasoning_effort: high}}
    build:
      description: Implement and validate changes
      edit: allow
      shell: ask
      sandbox: workspace-write
      mcp: {devtools: ask}
```

| Field | Description |
|---|---|
| `description` | Required, one line. Codex profiles carry none. |
| `prompt` | A file relative to the directory holding `zakwas.yaml`, rendered as a template that sees `{{ .Provider }}`, `{{ .Home }}` and `{{ .Vars }}`. It is the body of the Claude Code, opencode, Gemini and Copilot CLI files (Claude Code replaces its default system prompt with it) and Codex's `developer_instructions`. |
| `tools` | Any of `read`, `search`, `edit`, `shell`, `web`. Omit for all. |
| `edit`, `shell` | `allow`, `ask` or `deny`. A profile that sets `edit` or `shell` to `allow` or `ask` can't leave that tool out of `tools`. |
| `sandbox` | `read-only`, `workspace-write` or `danger-full-access`: Codex's sandbox mode. Without it, Codex gets `read-only` for `edit: deny` and `workspace-write` for `edit: allow`. |
| `network` | `true` or `false`. |
| `reviewer` | `user`, or `auto` for an automatic reviewer. |
| `mcp` | Server name → `allow`, `ask` or `deny`. Every server must be declared under [`mcp`](#mcp); one the profile doesn't list is denied. Servers added outside zakwas (`claude mcp add`, a plugin, a hand-edited config) are not affected. |
| `providers` | Providers to render for; default `agents.providers`, with Codex, Gemini and Copilot opt-in as for plugins. |
| `claude`, `codex`, `opencode`, `gemini`, `copilot` | `model` in the provider's own spelling, and `overrides`: extra top-level keys written as is. A key zakwas sets from the profile's own fields (`tools`, `permission`, `sandbox_mode`, …) is rejected; opencode's `mode` (`primary`) can be overridden, and Gemini takes `temperature`, `max_turns` and `timeout_mins` here. Values are re-typed by YAML, so quote ones that must stay strings: `2024-01-01` becomes a timestamp. |

How each field maps:

| Field | Claude Code | Codex | opencode | Gemini CLI | Copilot CLI |
|---|---|---|---|---|---|
| `tools` | `tools` allowlist | not expressible | other tools `deny` in `permission` | `tools` list of Gemini's tool names, the declared servers it keeps as `mcp_<server>_*` | `tools` allowlist of `read`, `search`, `edit`, `execute`, `web` |
| `edit` | `deny` removes the edit tools; `allow` is `permissionMode: acceptEdits`; `ask` is `default` | `deny` is sandbox `read-only`, `allow` is `workspace-write`; `ask` is not expressible | `permission.edit` | `deny` denies `write_file` and `replace`; `allow` and `ask` are policy rules for the subagent | `deny` removes `edit` from `tools`; `ask` is Copilot's default; `allow` is not expressible |
| `shell` | `deny` removes `Bash`; `ask` is `default` (with `edit: allow` it is not honored: `acceptEdits` also approves filesystem commands); `allow` is not expressible | `ask` is `approval_policy = "on-request"`, `allow` is `"never"`; `deny` is not expressible | `permission.bash`: `deny`, or the [`agents.permissions`](#permissions) rules with `allow` or `ask` as the catch-all, the guard's deny rules last | `deny` denies `run_shell_command`; `allow` and `ask` are policy rules for the subagent, below the `agents.permissions` rules, so their `allow` entries still run without asking | `deny` removes `execute` from `tools`; `ask` is Copilot's default; `allow` is not expressible |
| `sandbox` | not expressible | `sandbox_mode` | not expressible | not expressible | not expressible |
| `network` | `false` removes `WebFetch` and `WebSearch`; shell commands stay unrestricted | `sandbox_workspace_write.network_access` | `false` denies `webfetch` and `websearch`; shell commands stay unrestricted | `false` denies `web_fetch` and `google_web_search`; shell commands stay unrestricted | `false` removes `web` from `tools`; shell commands stay unrestricted |
| `reviewer` | `auto` is `permissionMode: auto`, `user` is `default` | `approvals_reviewer`: `user` or `auto_review` | `auto` is not expressible | `auto` is not expressible | `auto` is not expressible |
| `mcp` | `mcp__<server>` in `tools` or `disallowedTools`; `allow` is not expressible, the server's tools still ask | `enabled` and `default_tools_approval_mode` (`approve` for `allow`, `prompt` for `ask`) | `<server>_*` in `permission` | `allow` and `ask` are policy rules on the server for the subagent, `deny` a deny rule (or leaving it out of `tools`) | `<server>/*` in `tools`; `deny` leaves the server out; `allow` is not expressible, its tools still ask |

- The plan lists, per provider and profile, every field the provider cannot express (`claude profile review can't express: …`), the same way as the guard's warnings: it is not a change, so `check` stays clean.
- A profile name an opencode template defines under `agent` (a template that renders to the `opencode.json`) fails the opencode plan: the two would define one agent twice.
- The files are protected copies, like [`instructions`](#instructions): a file already there is backed up first. zakwas records their hashes in `~/.local/state/zakwas/agent-profiles.json`. Removing a profile, a provider, or the whole `agents` section removes only the files zakwas rendered (destructive in the plan; one edited since is backed up instead).
- Claude Code and every named provider are strict, failing their own plan when a profile can't be rendered; Codex gets profiles only when `zakwas.yaml` names `codex`; opencode by the all-providers default is best effort, written only once `opencode` is on `PATH`. Gemini and Copilot, like Codex, get profiles only when `zakwas.yaml` names them.
- Gemini has no profile to start a session with: a profile is a subagent the main agent delegates to, or that you name with `@<name>`. A profile that lists `tools` gets exactly those tools; one that doesn't inherits all of them, and what it takes away is a deny rule scoped to the subagent in `zakwas.toml`.

### skills

`agents.skills` links skills that don't come from a plugin marketplace, such as a skill kept in the config repo or one skill of another repository, into every managed agent's skills directory. Agents disagree on where they look, so zakwas links each skill where the agent reads it:

| Provider | Link |
|---|---|
| Claude Code | `${CLAUDE_CONFIG_DIR:-~/.claude}/skills/<name>` |
| Codex, opencode | `~/.agents/skills/<name>`, one link for both (Copilot CLI reads it too) |
| Gemini CLI | `~/.agents/skills/<name>`, the same link, or `${GEMINI_CLI_HOME}/.agents/skills/<name>` when `GEMINI_CLI_HOME` is set |
| Copilot CLI | `~/.agents/skills/<name>`, the same link. Once `COPILOT_HOME` is set, Copilot reads only `${COPILOT_HOME}/skills`, so it gets its own link there instead |

```yaml
agents:
  skills:
    - dotfiles/skills/review
    - ~/work/skills/deploy
    - {git: owner/skills, path: skills/pdf, ref: v1}
    - {path: dotfiles/skills/codex-only, providers: [codex]}
```

| Field | Description |
|---|---|
| `path` | Required. The skill directory, holding `SKILL.md`: relative to the directory holding `zakwas.yaml`, absolute or `~/…`. With `git`, a path inside the repository. The last element is the skill's name. |
| `git` | A GitHub `owner/repo` or a clone URL: the skill is `path` inside that repository. |
| `ref` | A branch or tag to clone, only with `git`. Default: the repository's default branch. |
| `providers` | Providers to link for; default `agents.providers`, with Codex, Gemini and Copilot opt-in as for plugins. |

A skill written as a plain string is its `path`.

- The directory name must equal the `name` in the frontmatter of its `SKILL.md`, and two skills may not share a name, ignoring case. Either fails the plan. A skill without a name fails too, since Codex skips it.
- zakwas only creates and removes its own links, recorded in `~/.local/state/zakwas/skills.json`. A directory, file or link at that path that zakwas did not make fails the plan instead of being replaced; this includes skills installed by `npx skills`, whose links and `~/.agents/.skill-lock.json` zakwas never touches. A link you made to the same directory is left alone.
- `plan` never fetches. `apply` clones missing git sources with `git clone --depth 1` into `~/.local/share/zakwas/skills/<source>` (shared by every skill of one repository and `ref`), checks every skill of the clone and then links them, in one `<provider> skills` step of the plan. `zakwas upgrade` pulls the clones; the links point into them, so a newer skill needs no relink. A skill that resolves outside its repository through a symlink fails.
- Removing a skill, a provider, or the whole `agents` section removes only the links zakwas made (destructive in the plan; a link changed since is only forgotten), then the clones no skill uses anymore (a clone a tracked link still points into is kept until that link has moved).
- zakwas won't write through a symlinked parent under `$HOME`, so when `~/.claude` or `~/.agents` is a link into your repo the plan fails; point `CLAUDE_CONFIG_DIR` elsewhere or unlink it. opencode's `opencode.skillsDir`, which holds plugin skills, is not used for these.
- Claude Code and every named provider are strict, failing their own plan when a skill can't be linked; Codex, Gemini and Copilot get skills only when `zakwas.yaml` names them; opencode by the all-providers default is best effort, linked only once `opencode` is on `PATH`.

### guard

An agent that can run `zakwas apply`, or edit the config repo or zakwas state, can widen its own permissions, add hooks or install marketplaces, because `apply` writes agent config files the agent may not edit itself. One prompt injection in a README is enough. So with an `agents` section, zakwas keeps the managed agents from applying the config unless you set `guard: false`:

- **Deny rules.** Every managed agent is denied `zakwas apply`, `zakwas upgrade`, `zakwas self-update`, `zakwas approve` and `zakwas mcp`, also with a flag before the command (`zakwas -c zakwas.yaml apply`) and, for Claude Code, Gemini CLI and opencode, when run by path (`~/.local/bin/zakwas apply`). Claude Code gets `Bash(zakwas …)` rules in `permissions.deny`, which hold in every permission mode, `bypassPermissions` included. Codex gets `forbidden` rules in `zakwas.rules`, for the commands as written (`zakwas apply`, not `zakwas -c … apply`). Gemini CLI gets `deny` rules in `zakwas.toml` that also match an unquoted flag before the command and an unquoted path, at a priority above `permissions`; a quoted flag value or binary path is not matched. opencode gets `deny` patterns through the [`agentPermissions`](#permissions) template function. They sit with the rules from `permissions`, so removing them follows the same rules: zakwas removes only what it added.
- **Edits.** Claude Code is also denied edits (`Edit(…)` rules, which cover its file tools) to the config repo's main checkout, and to the checkout zakwas runs from when that is a linked worktree of it, and to `~/.local/state/zakwas`. Worktrees agents create outside that checkout stay editable, so an agent can still propose a change as a branch; one created inside it is covered by the rule. A repo that holds your home directory can't be denied, and the plan says so. These rules don't stop a shell command that writes a file; the command rules above are what stop `apply`. They match the command as the agent writes it, so a wrapper (`sh -c`, a script, a PTY) is outside what they stop.
- **Refusal.** `zakwas apply`, `zakwas upgrade` and `zakwas approve` started from an agent session exit 1 with an explanation, before they change anything. zakwas recognizes a session by the environment variables the agents set (`CLAUDECODE`, `CODEX_SANDBOX`, `OPENCODE`, `GEMINI_CLI`, `COPILOT_AGENT_SESSION_ID`, …). An agent can unset them, so this catches accidents and injections that don't know about it; the deny rules are the boundary. `plan` and `check` work in a session, so an agent can review drift and, with `zakwas propose`, save a change for you to [approve](commands.md#approve) in a terminal.
- **Providers that can't be protected.** The plan lists a managed provider the guard can't cover, under `agents`: opencode when no template renders `agentPermissions "opencode"` into its `opencode.json`, and Copilot CLI, which has no user-level deny rules, so only the refusal covers it. Such a warning doesn't count as a change, so `check` stays clean.

Codex, Gemini and Copilot are guarded only when `zakwas.yaml` names them, like their other settings. Run `apply` in a terminal outside the agent. `guard: false` removes the rules zakwas added on the next `apply` and turns the refusal off; rules you wrote yourself stay.

### Gemini CLI

Gemini rewrites `~/.gemini/settings.json` itself, and its memory tool writes the first `context.fileName` file, so zakwas owns neither. It renders one extension instead and links it with `gemini extensions link --consent`, and writes one policy file:

| What | Where |
|---|---|
| Extension | `${GEMINI_CLI_HOME:-~}/.gemini/zakwas/`: `gemini-extension.json` (name, `contextFileName`, `mcpServers`), `GEMINI.md`, `hooks/hooks.json` and `agents/<name>.md` |
| Policy rules | `${GEMINI_CLI_HOME:-~}/.gemini/policies/zakwas.toml`: [`permissions`](#permissions), the [guard](#guard) and what a [profile](#profiles) allows or asks |
| Skills | `~/.agents/skills/<name>`, see [skills](#skills) |

- Naming `gemini` in `agents.providers` converges all of it, except that skills and permissions need no extension: a config with only those writes none, and removes one it wrote; a config that doesn't name it, or an entry that doesn't list it, leaves Gemini alone. `GEMINI_CLI_HOME` moves `.gemini` and `.agents` under it, and zakwas follows it (a relative one resolves from the directory zakwas runs in). When it changes, zakwas runs `gemini extensions uninstall zakwas` with the old value in `GEMINI_CLI_HOME` and then removes the files it wrote there, so the old home keeps no extension record.
- The plan fails, for Gemini only, when `gemini` is not on `PATH`, when an extension named `zakwas` is installed from another place than the directory above (remove it with `gemini extensions uninstall zakwas`), or when a file can't be rendered. The link is the last step, after the files it needs; zakwas reads gemini's record of the link instead of running it, so `plan` and `check` change nothing.
- The files are protected copies, like [`instructions`](#instructions): a file already there is backed up first, and zakwas records their hashes in `~/.local/state/zakwas/gemini-files.json` (the context file and the subagents in `instruction-files.json` and `agent-profiles.json`). When `gemini` leaves the config, zakwas runs `gemini extensions uninstall zakwas` first, since gemini needs the manifest for it, then removes the files it wrote. Empty directories stay.
- Commands in `hooks.commands` receive Gemini's event JSON on stdin and answer in its format: a hook must print nothing but JSON on stdout. Gemini asks you to approve the extension's hooks when it loads them, as for any extension.
- Gemini keeps no plugin marketplaces here: `marketplaces` and `plugins` can't name it, and `prune` and `upgrade` leave it alone.

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

## launchd

Scheduled jobs as launchd user agents, such as a daily drift check. zakwas writes each job's plist to `~/Library/LaunchAgents/<label>.plist` and loads it with `launchctl bootstrap`. Plan only reads launchd (`launchctl print`); it never loads or unloads anything.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `label` | string | yes | | Job name, e.g. `com.example.drift-check`. Letters, digits, `.`, `_` and `-`. Unique, compared case-insensitively. |
| `program` | list of strings | yes | | Program and arguments. The first entry is an absolute path or starts with `~/`; the rest are written as is. |
| `schedule.every` | duration | one of `every`, `at` | | Interval of at least `1m`, e.g. `30m`, `6h`. |
| `schedule.at` | string | one of `every`, `at` | | Local time of day as `HH:MM`, 24-hour clock. |
| `schedule.weekdays` | list | no | every day | Days for `at`: `mon`, `tue`, `wed`, `thu`, `fri`, `sat`, `sun`. |
| `runAtLoad` | bool | no | `false` | Also run once whenever the job is loaded: at login and after each change to it. |
| `env` | map | no | | Environment variables for the job. Values are not expanded. |
| `log` | string | no | discarded | File for stdout and stderr: absolute or `~/…`. Its directory must exist. |

launchd starts jobs from `/` with a minimal `PATH` and expands neither `$VAR` nor `~`, so point `program` at a script, or at `/bin/sh -c`, and set `env.PATH` when the job needs more. A Mac that sleeps through a scheduled time runs the job once on wake. `program`, `env` and `log` may not contain control characters other than tab and newline. `plan` fails when `launchctl print` fails for any reason other than "not loaded", so a job is never unloaded or deleted on a guess.

- **Changing a job** rewrites its plist and reloads it (`bootout`, then `bootstrap`).
- **Removing a job** from the config unloads it and deletes its plist. A plist edited since zakwas wrote it is moved to `<plist>.zakwas-bak` instead of being deleted.
- **Ownership.** zakwas records the labels it created in `~/.local/state/zakwas/launchd.json` and only ever unloads or deletes those. Other agents in `~/Library/LaunchAgents`, loaded or not, are never touched. A configured label that is already loaded, but was not created by zakwas, fails its plan; unload it or pick another label. A plist zakwas did not write at the destination of an unloaded label is backed up like any other file, then replaced.
- The plist path takes part in the [no-overlap rule](#conventions), so `files`, `links` and `templates` can't target the same file.

```yaml
launchd:
  - label: com.example.drift-check
    program: [~/.local/bin/zakwas-check-notify]
    schedule: {at: "10:00", weekdays: [mon, tue, wed, thu, fri]}
    env: {PATH: "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"}
    log: ~/Library/Logs/zakwas-check.log
```

The [daily drift notification](examples.md#daily-drift-notification) recipe builds on this.

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
