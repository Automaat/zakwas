# zakwas

Declarative macOS setup: a Go CLI that converges a Mac to `zakwas.yaml` (files, links, templates, brew, mise, agents, defaults, system, commands). Users keep the config in their own repo; see `examples/`.

## Layout

| Path | What |
|---|---|
| `cmd/zakwas/` | entrypoint + testscript e2e tests (`testdata/script/*.txtar`) |
| `internal/cli/` | flag parsing, module wiring, exit codes, apply history |
| `internal/config/` | `zakwas.yaml` schema, `Validate`, config discovery |
| `internal/engine/` | `Module` interface, plan/apply |
| `internal/modules/<name>/` | one package per module |
| `internal/install/` | protected-copy writer + hash state (`~/.local/state/zakwas/files.json`) |
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

system → files → links → templates → brew → mise → agents → commands → defaults. Files put the mise config in place; brew installs mise; agents need the agent CLIs brew or mise install; commands may need tools from any.

## Adding a module

1. `internal/modules/<name>/` implementing `engine.Module` (`Name`, `Plan`); each `Change.Apply` does one idempotent step.
2. Run external commands only through `runner.Runner`; test with `runnertest.Fake`.
3. Config in `internal/config` (validate in `Validate`), wire in `cli.Modules`.
4. Tests: table-driven unit tests + converge twice and assert the second plan is empty.
5. Document it in `README.md` and `examples/zakwas.yaml`.

## Rules

- `plan`/`check` must never change the system; every brew call (apply too) sets `HOMEBREW_NO_AUTO_UPDATE=1`, so only `zakwas upgrade` refreshes Homebrew. Likewise only `upgrade` refreshes agent marketplaces.
- agents: each provider is a `backend` in `internal/modules/agents` (claude, codex; opencode is validated and skipped). A provider that fails to plan returns its error alongside the other providers' changes, which still apply. Never pass `-y`/`--accept-command` to `claude plugin`; never prune project/local-scope installs. Codex prune only touches marketplaces in `$CODEX_HOME/config.toml` and their plugins. Tests that run a real `codex` must set `CODEX_HOME` to a temp dir.
- Replaced files zakwas didn't write (or that were edited) are backed up to `<file>.zakwas-bak` (`.zakwas-bak.N` if taken), never deleted or overwritten.
- `files`/`links`/`templates` destinations must be absolute or `~/…` (no `$VAR`) and must not overlap (case-insensitive). Modes are octal: `0644`, not `644`.
- zakwas refuses to write through a symlinked parent dir under `$HOME` or one resolving into the config repo.
- The mise module runs mise from `/`: from `$HOME`, mise treats `~/.config/mise/config.toml` as a project config that outranks the repo file, hiding bumped pins until after `apply`.
- `commands` stops at the first failing entry.
- zakwas bootstraps its own prerequisites: brew/mise modules plan an install of Homebrew / mise (pinned `mise.Version`, SHA256 from the release's `SHASUMS256.txt`) when `Runner.Installed` says the binary is missing. `main` appends `/opt/homebrew/bin`, `/usr/local/bin`, `~/.local/bin` to `PATH` so later modules find what earlier ones installed.
- Changes carry structure, not just text: `From`/`To` for versions and values, `Destructive` for deletions/cleanup/prune, `Streams` when Apply passes tool output through, `Diff` for file diffs and command scripts. Changes with nil `Apply` only list what a later step of the module does; prompts and progress count steps (`Plan.Steps`).
- Machine output: `engine.JSONChange`/`JSONModule` (`engine/json.go`), plan document and apply events in `cli/machine.go`, history in `cli/history.go`; all versioned by `engine.FormatVersion`. Adding a field is fine; renaming or removing one bumps it.
- Human output lives in `engine.Render` and `cli/progress.go`; plan goldens in `cmd/zakwas/testdata/script`.
- Config fields carry `jsonschema_description` (and required/enum) tags. After changing `internal/config` types run `mise run schema`; a test fails on a stale schema or an undocumented property. Update `docs/config.md` too. Documented modes use `0o` octal: editors read YAML 1.2, where `0700` is decimal.
- New commands go in `commandList` (`internal/cli/cli.go`), which feeds usage and completion; document them in `docs/commands.md`.
- Release attestation runs in a separate `attest` job on the uploaded `dist` artifact; if it fails, re-run only that job. Don't make GoReleaser releases drafts while the cask is published: the cask would point at undownloadable assets.
- The Homebrew installer is pinned to a commit (`installerCommit` in `internal/modules/brew/brew.go`); never fetch `HEAD`.
- Nothing user-specific in code or `examples/`: no personal paths, taps, or keys.
- `install.sh` passes `shellcheck`, workflows pass `actionlint` (both in CI).
