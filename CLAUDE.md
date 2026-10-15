# zakwas

Declarative macOS setup: a Go CLI that converges a Mac to `zakwas.yaml` (files, links, templates, brew, mise, agents, defaults, system, commands, launchd). Users keep the config in their own repo; see `examples/`.

## Layout

| Path | What |
|---|---|
| `cmd/zakwas/` | entrypoint + testscript e2e tests (`testdata/script/*.txtar`) |
| `internal/cli/` | flag parsing, module wiring, exit codes, apply history |
| `internal/config/` | `zakwas.yaml` schema, `Validate`, config discovery |
| `internal/engine/` | `Module` interface, plan/apply |
| `internal/modules/<name>/` | one package per module |
| `internal/proposal/` | typed config edits (byte-splicing), proposal store, risk tiers |
| `.claude-plugin/`, `plugins/zakwas/` | the repo's agent marketplace and its `zakwas` plugin (skill teaching agents the proposal flow); `plugin.json` holds the plugin version |
| `internal/install/` | protected-copy writer + hash state (`~/.local/state/zakwas/files.json`) |
| `internal/lock/` | flock that serializes `apply`/`upgrade` (`~/.local/state/zakwas/lock`) |
| `internal/runner/` | exec abstraction; `runnertest.Fake` for tests |
| `install.sh` | fresh-Mac installer: CLT, zakwas binary from the release, clone, apply |
| `examples/` | example config repo; CI plans and applies it |
| `schema/` | generated `zakwas.schema.json` (embedded for `zakwas schema`); generator in `internal/schemagen` |
| `internal/selfupdate/` | release download + verification for `self-update` |
| `docs/` | user docs: config reference, commands, examples, security |
| `.goreleaser.yaml` | darwin release binaries, published on `v*` tags |

## Commands

```bash
mise run test                 # unit + e2e (fake binaries, no system changes)
mise run test:integration     # + real `defaults` round-trip (macOS only)
mise run lint
go run ./cmd/zakwas -c examples/zakwas.yaml plan
```

Exit codes: 0 ok, 1 error, 2 drift (`check`), 64 usage, 130 interrupted.

## Module order

system → files → links → templates → brew → mise → agents → commands → launchd → defaults. Files put the mise config in place; brew installs mise; agents need the agent CLIs brew or mise install; commands may need tools from any; launchd jobs may run what commands set up.

## Adding a module

1. `internal/modules/<name>/` implementing `engine.Module` (`Name`, `Plan`); each `Change.Apply` does one idempotent step.
2. Run external commands only through `runner.Runner`; test with `runnertest.Fake`.
3. Config in `internal/config` (validate in `Validate`), wire in `cli.Modules`.
4. Tests: table-driven unit tests + converge twice and assert the second plan is empty.
5. Document it in `README.md` and `examples/zakwas.yaml`.

## Rules

- `plan`/`check` must never change the system; every brew call (apply too) sets `HOMEBREW_NO_AUTO_UPDATE=1`, so only `zakwas upgrade` refreshes Homebrew. Likewise only `upgrade` refreshes agent marketplaces.
- `engine.Build` plans modules concurrently (and brew runs its read-only queries together), so `Plan` and `Warnings` must not write state shared with other modules; modules that share a struct (e.g. the `files`/`templates` installer) guard it. Output stays in module order.
- agents: each provider is a `backend` in `internal/modules/agents` (claude, codex, opencode). A provider that fails to plan returns its error alongside the other providers' changes, which still apply; each change's `Group` is its provider, so an apply failure stops only that provider. Codex is opt-in: converged only when `zakwas.yaml` names `codex`. Never pass `-y`/`--accept-command` to `claude plugin`; never prune project/local-scope installs. Codex prune only touches marketplaces in `$CODEX_HOME/config.toml` and their plugins. Tests that run a real `codex` must set `CODEX_HOME` to a temp dir. opencode has no plugin system: zakwas links skills into its skills dir, tracks its links in `~/.local/state/zakwas/opencode.json` and fetches git marketplaces only in apply (pulls only in `upgrade`); plugins that reach opencode only by the default providers are best effort. `agents.instructions` links one file per provider via `install.PlanLink` (shared with `links`), tracked in `~/.local/state/zakwas/instructions.json`; same strict/opt-in/best-effort rules. `agents.permissions` adds rules to Claude's `settings.json` (only the ones zakwas added are tracked, in `~/.local/state/zakwas/agent-permissions.json`, and ever removed) and owns `$CODEX_HOME/rules/zakwas.rules`; opencode gets them only through the `agentPermissions` template function, since its `opencode.json` is usually a template. `agents.skills` links standalone skills (Claude `~/.claude/skills`, Codex and opencode `~/.agents/skills`) and tracks its links and git clones in `~/.local/state/zakwas/skills.json`; it only ever replaces or removes its own links, never `npx skills` installs. `agents.mcp` registers servers at user scope: Claude's `.claude.json` (`$CLAUDE_CONFIG_DIR` aware) is edited directly because `claude mcp get` starts the server, and Codex's `[mcp_servers.<name>]` tables are edited as text in place (`tomledit.go`), never with `codex mcp add`; only servers recorded in `~/.local/state/zakwas/agent-mcp.json` are changed or removed, a same-named unowned server is adopted when identical and fails that provider's plan when not; opencode gets them through the `agentMCP` template function. Gemini is opt-in like Codex (`config.OptIn`, `Agents.Wants`) and has no `backend`: `gemini.go` renders one extension (`~/.gemini/zakwas`, linked with `gemini extensions link --consent`, which `plan` detects by reading gemini's install record, never by running it) holding the manifest with MCP servers, hooks, and the context file and subagents the instructions and profiles planners write there, plus the whole user policy file `policies/zakwas.toml` (extension rules rank below user ones and can't allow, so all permission, guard and profile rules live there: deny 900/990 > ask 700 > allow 500 > profile 400); files are installer copies tracked in `gemini-files.json`, unlink goes before the files go; never write `~/.gemini/settings.json` or `GEMINI.md`; `GEMINI_CLI_HOME` moves `.gemini` and `.agents`; servers whose command, args, url, env or headers hold a literal `$` (Gemini expands it) or whose name has `_` are left out with a warning; the manifest of an unmanaged extension stays tracked until `gemini` can uninstall it.
- `apply`/`upgrade` hold the run lock (`internal/lock`) from before the plan to exit; `plan`/`check` never lock, so every state file must be written with a rename (`install.WriteAtomic`), never in place.
- agents guard (`agents.guard`, default on): `agents.Module.Guard` (set by `cli.Modules` from the config's agents section; the synthesized empty section never guards) adds deny rules for `zakwas apply|upgrade|self-update` per guarded provider (`config.Agents.GuardsProvider`: managed, Codex only when named) and Claude `Edit` rules for the config repo checkouts and zakwas state, through the same ownership tracking as `agents.permissions`; opencode gets them in `agentPermissions`. `cli` refuses `apply`/`upgrade` when `Env.AgentSession` (env markers, `DetectAgentSession`) is set. Providers the guard can't cover are `engine.Warner` warnings: never changes, so `check` stays clean. Tests that don't exercise the guard set `guard: false` (testscripts) or leave `Module.Guard` unset.
- `zakwas mcp` (`internal/mcp` protocol, `internal/cli/mcp.go` tools) writes only JSON-RPC to stdout: the runner's output goes to stderr. Its `zakwas_apply` exists only with `--enable-apply`, reuses `approval` from `approve.go` with `maxTier: low` and an elicitation `ask`, rates the proposal again (never trusts the stored tier), takes the run lock with `takeLock`, and fails closed on anything but an explicit yes. `mcp` is a guarded command: an agent that ran it in a shell would answer its own form. A `zakwas mcp --enable-apply` entry in `agents.mcp` gets Codex `approval_mode = "prompt"` for the tool (`MCPServer.PromptTools`).
- Replaced files zakwas didn't write (or that were edited) are backed up to `<file>.zakwas-bak` (`.zakwas-bak.N` if taken), never deleted or overwritten.
- `files`/`links`/`templates` destinations must be absolute or `~/…` (no `$VAR`) and must not overlap (case-insensitive). Modes are octal: `0644`, not `644`.
- zakwas refuses to write through a symlinked parent dir under `$HOME` or one resolving into the config repo.
- The mise module runs mise from `/`: from `$HOME`, mise treats `~/.config/mise/config.toml` as a project config that outranks the repo file, hiding bumped pins until after `apply`.
- `commands` stops at the first failing entry.
- launchd: `plan` only runs `launchctl print`; apply writes `~/Library/LaunchAgents/<label>.plist`, then `bootstrap`s it (`bootout` first on a change). Only labels in `~/.local/state/zakwas/launchd.json` are ever unloaded or deleted; a configured label loaded from a plist zakwas did not write is a plan error.
- zakwas bootstraps its own prerequisites: brew/mise modules plan an install of Homebrew / mise (pinned `mise.Version`, SHA256 from the release's `SHASUMS256.txt`) when `Runner.Installed` says the binary is missing. `main` appends `/opt/homebrew/bin`, `/usr/local/bin`, `~/.local/bin` to `PATH` so later modules find what earlier ones installed.
- Changes carry structure, not just text: `From`/`To` for versions and values, `Destructive` for deletions/cleanup/prune, `Streams` when Apply passes tool output through, `Diff` for file diffs and command scripts. Changes with nil `Apply` only list what a later step of the module does; prompts and progress count steps (`Plan.Steps`).
- Machine output: `engine.JSONChange`/`JSONModule` (`engine/json.go`), plan document and apply events in `cli/machine.go`, history in `cli/history.go`; all versioned by `engine.FormatVersion`. Adding a field is fine; renaming or removing one bumps it.
- Human output lives in `engine.Render` and `cli/progress.go`; plan goldens in `cmd/zakwas/testdata/script`.
- Config fields carry `jsonschema_description` (and required/enum) tags. After changing `internal/config` types run `mise run schema`; a test fails on a stale schema or an undocumented property. Update `docs/config.md` too. Documented modes use `0o` octal: editors read YAML 1.2, where `0700` is decimal.
- New commands go in `commandList` (`internal/cli/cli.go`), which feeds usage and completion; document them in `docs/commands.md`.
- Proposals (`propose`/`proposals`/`approve`): edits splice the original bytes of `zakwas.yaml`, the Brewfile and the mise config, never re-encode them. `propose` never touches the repo or system and saves under `~/.local/state/zakwas/proposals/`; `approve` recomputes diff, plan and tier from the saved operations (the stored ones are informational), refuses a changed base, an age over 7 days, a dirty edited file, a non-TTY stdin and an agent session (`GuardedCommands` includes `approve`), restores the files when apply fails, and commits only the edited files without pushing. Low tier is a closed list (`proposal.Classify`); a plan touching agent config, shell rc files, `~/.ssh`, `/etc` or failing to plan makes it high.
- Release attestation runs in a separate `attest` job on the uploaded `dist` artifact; if it fails, re-run only that job. Don't make GoReleaser releases drafts while the cask is published: the cask would point at undownloadable assets.
- The Homebrew installer is pinned to a commit (`installerCommit` in `internal/modules/brew/brew.go`); never fetch `HEAD`.
- The plugin version (`plugins/zakwas/.claude-plugin/plugin.json`, nowhere else) must equal the release tag: bump it in a PR before tagging; `release.yml` fails a tag that differs. Change the skill together with the `zakwas mcp` tools and `propose` operations it names.
- Nothing user-specific in code or `examples/`: no personal paths, taps, or keys.
- `install.sh` passes `shellcheck`, workflows pass `actionlint` (both in CI).
