# zakwas

Declarative macOS setup: a Go CLI that converges a Mac to `zakwas.yaml` (files, links, templates, brew, mise, defaults, system, commands). Users keep the config in their own repo; see `examples/`.

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
| `install.sh` | fresh-Mac installer (CLT, brew, mise, zakwas, clone, apply) |
| `examples/` | example config repo; CI plans and applies it |
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

system → files → links → templates → brew → mise → commands → defaults. Files put the mise config in place; brew installs mise; commands may need tools from either.

## Adding a module

1. `internal/modules/<name>/` implementing `engine.Module` (`Name`, `Plan`); each `Change.Apply` does one idempotent step.
2. Run external commands only through `runner.Runner`; test with `runnertest.Fake`.
3. Config in `internal/config` (validate in `Validate`), wire in `cli.Modules`.
4. Tests: table-driven unit tests + converge twice and assert the second plan is empty.
5. Document it in `README.md` and `examples/zakwas.yaml`.

## Rules

- `plan`/`check` must never change the system; every brew call (apply too) sets `HOMEBREW_NO_AUTO_UPDATE=1`, so only `zakwas upgrade` refreshes Homebrew.
- Replaced files zakwas didn't write (or that were edited) are backed up to `<file>.zakwas-bak` (`.zakwas-bak.N` if taken), never deleted or overwritten.
- `files`/`links`/`templates` destinations must be absolute or `~/…` (no `$VAR`) and must not overlap (case-insensitive). Modes are octal: `0644`, not `644`.
- zakwas refuses to write through a symlinked parent dir under `$HOME` or one resolving into the config repo.
- The mise module runs mise from `/`: from `$HOME`, mise treats `~/.config/mise/config.toml` as a project config that outranks the repo file, hiding bumped pins until after `apply`.
- `commands` stops at the first failing entry.
- Nothing user-specific in code or `examples/`: no personal paths, taps, or keys.
- `install.sh` passes `shellcheck`, workflows pass `actionlint` (both in CI).
