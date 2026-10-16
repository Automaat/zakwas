# Contributing

Issues and PRs are welcome. For anything bigger than a bug fix (a new module, a new agent provider, a config key), open an issue first so we can agree on the approach before you write code.

By taking part you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md). Security problems go through [SECURITY.md](SECURITY.md), not a public issue. Questions about using zakwas: see [.github/SUPPORT.md](.github/SUPPORT.md).

## Scope

zakwas converges a Mac, or a Linux machine for the modules that support it, to a `zakwas.yaml`. macOS comes first; Linux is limited to what [Platform support](docs/config.md#platform-support) lists. It is pre-1.0, so config keys can still change between minor versions. A change fits when it makes a plan more accurate, adds a typed way to manage something a user would otherwise script in `commands`, or adds a coding agent the `agents` module can render for. Nothing user-specific belongs in code or `examples/`: no personal paths, taps or keys.

## Setup

```bash
git clone https://github.com/Automaat/zakwas && cd zakwas
mise install                # Go, golangci-lint, shellcheck, actionlint, goreleaser (versions in mise.toml)
mise run test               # go test -race ./...: unit + e2e with fake brew/mise/defaults, no system changes
mise run test:integration   # adds the `integration` build tag: real `defaults` and mise round-trips (macOS)
mise run lint               # golangci-lint run
mise run schema             # regenerate schema/zakwas.schema.json after changing internal/config
```

Try your build against the example config with a throwaway `$HOME`, so your real zakwas state isn't read. Build first: with a new `$HOME`, `go run` downloads the Go modules again.

```bash
go build -o /tmp/zakwas ./cmd/zakwas
HOME=$(mktemp -d) /tmp/zakwas -c examples/zakwas.yaml plan --only system,files,templates,defaults
```

`plan` and `check` never change the system. While developing, run `apply` only with a temp `$HOME` and a config that names just the modules under test (`--only`).

[CLAUDE.md](CLAUDE.md) (also read as `AGENTS.md`) describes the layout, the module order and the rules each module keeps. It is the reference for what follows, so read it before a larger change.

### Testing on Linux

On a Linux machine the setup above works as is: `mise install && mise run test`. The `ubuntu` CI job runs `go build`, `go vet`, `golangci-lint`, `go test -race ./...` (unit tests and the e2e scripts), plans and applies [`examples/linux/zakwas.yaml`](examples/linux/zakwas.yaml) and smoke-tests `install.sh`. From a Mac, `GOOS=linux go build ./... && GOOS=linux go vet ./...` catches build errors, and a container runs the tests: cross-compile a package's test binary and run it where its sources are (the tests need `git` and `ssh-keygen`; use a throwaway `$HOME`):

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o /tmp/cli.test ./internal/cli
docker run --rm -v "$PWD:/src" -v /tmp/cli.test:/cli.test -w /src/internal/cli your-image-with-git /cli.test
```

## Making a change

### Add a module

Follow [Adding a module](CLAUDE.md#adding-a-module) and keep the [module order](CLAUDE.md#module-order). Register it in `internal/platform` with the systems it supports, and add it to the table in `docs/config.md`. In short: a package under `internal/modules/<name>/` that implements `engine.Module`, config in `internal/config` (validated in `Validate`), wiring in `cli.Modules`, and docs in `README.md`, `docs/config.md` and `examples/zakwas.yaml`.

### Add an agent provider

The `agents` entry under [Rules](CLAUDE.md#rules) lists what every provider must keep. Open an issue first: a provider's own file formats and CLI decide how much zakwas can manage, and the [agents guide](docs/agents.md#providers) shows what each existing provider supports. The pieces are:

- the provider name in `internal/config/agents.go` (`Providers`, and `OptIn` for a provider that must not change what existing configs mean);
- a `backend` in `internal/modules/agents` (`plan` and `refresh`), registered in `backends()`. Gemini CLI is the one provider without a `backend`: it is rendered as one extension (`gemini.go`), which suits a tool that rewrites its own settings files;
- support for each feature in the other `agents` files: instructions, MCP servers, skills, hooks, permissions, profiles, guard, capture;
- a unit test file next to it that runs every CLI call through `runnertest.Fake`, and an end-to-end script like the existing `cmd/zakwas/testdata/script/agents-*.txtar` ones, with a fake binary on `PATH`;
- the support matrix in `docs/agents.md`, the keys in `docs/config.md` and a recipe in `docs/recipes.md`.

A test must never touch a provider's real state: set its home variable (`CODEX_HOME`, `COPILOT_HOME` and `HOME`, `GEMINI_CLI_HOME`, `CLAUDE_CONFIG_DIR`) to a temp dir.

### Change config keys

Config fields carry `jsonschema_description` tags (plus required and enum). After changing the types in `internal/config`:

1. Run `mise run schema`. A test fails on a stale schema or an undocumented property.
2. Document the key in `docs/config.md`. Modes in docs use `0o` octal (`0o700`), because editors read YAML 1.2, where `0700` is decimal.

### Add a command

Add it to `commandList` in `internal/cli/cli.go`, which feeds usage and shell completion, and document it in `docs/commands.md`.

### Docs

- `docs/recipes.md` holds snippets, one feature each. The sample config repo is `examples/`; don't mix the two.
- `internal/docs` checks every fenced `yaml` block in `README.md` and `docs/*.md`: it must load through `config.Parse` and `Validate`, and a block that declares only `agents` (with optional `templates`) must also plan, with the files it references created in a temp config repo as stubs, so the bodies of instruction, prompt, skill and template files are not checked. The failure names the file and line.
- Write a block as a complete `zakwas.yaml` or as a fragment that starts with a top-level key. A `yaml` block that is not a config (a CI workflow, an editor modeline) gets `<!-- recipe: skip -->` on the line before its fence. A marker on a block that uses a `zakwas.yaml` top-level key fails the test. Open the fence as ```` ```yaml ```` at the start of the line; other spellings (`yml`, `~~~`, indented) fail the test. Use another fence language for templates, JSON, shell and Brewfiles, which needs no marker.
- Modes in docs use `0o` octal (see Change config keys); anchors come from heading text, so renaming a heading means updating its links.

### Tests

- Unit tests are table-driven. External commands go through `runner.Runner`, and tests use `runnertest.Fake`.
- A module test converges twice and asserts the second plan is empty.
- End-to-end tests are [testscript](https://pkg.go.dev/github.com/rogpeppe/go-internal/testscript) files in `cmd/zakwas/testdata/script/*.txtar`, run with fake `brew`, `mise` and agent CLIs. They also hold the plan goldens: when plan output changes on purpose, update them.
- Tests that need the real macOS `defaults` or mise carry the `integration` build tag.
- Tests that need macOS itself (the `uchg` flag, `defaults`) call `testutil.RequireDarwin(t, "reason")` from `internal/testutil`, or start an e2e script with `[!darwin] skip 'reason'`; the skip says what is not covered. Do not weaken an assertion to make it pass on both systems.
- Behavior that differs by operating system takes the system as a parameter (`config.LoadFor`, `cli.Env.GOOS`, `platform.Supported(module, goos)`), not a build tag, so both sides are unit tested on any host. Build tags are only for system calls (`internal/install/flags_*.go`, `internal/modules/agents/swap_*.go`).
- An e2e script that needs a macOS-only module against fake binaries starts with `env ZAKWAS_TEST_GOOS=darwin`; one that checks Linux behavior starts with `env ZAKWAS_TEST_GOOS=linux`. Both run on any host. The real Linux run is the `ubuntu` CI job.
- Docs: the platform tables in `docs/config.md` are checked against `internal/platform`; change them together.
- Machine-readable output (`--json`, apply history) is versioned by `engine.FormatVersion`: adding a field is fine, renaming or removing one bumps the version.
- Comments explain a non-obvious why, never what.

## Commits and PRs

- Conventional commits with a scope: `feat(brew): …`, `fix(install): …`, `docs(docs): …`. Keep the title at 50 characters or fewer and body lines at 72 or fewer.
- Sign off every commit (`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org).
- PRs are squash-merged and the PR title becomes the commit subject, so write it as a conventional commit that reads well.
- Release notes are generated from those subjects (`changelog` in `.goreleaser.yaml`): `feat`, `fix`, `perf` and `docs` get a section; `chore`, `ci`, `test`, `style` and `build` are left out; any other type lands under Other. Pick the type by what a user sees.
- Fill in [the PR template](.github/pull_request_template.md): Motivation (starting with the `> Changelog: type(scope): description` line, which repeats the PR title), Implementation information, Supporting documentation (how you tested it).
- CI must pass. Four checks gate a merge: `test` (lint, shellcheck, actionlint, the GoReleaser snapshot build, unit, e2e and integration tests), `e2e on a fresh Mac`, `install.sh on a fresh Mac` and `installs mise on a Mac without it`. They run on macOS runners; see `.github/workflows/ci.yml`.

If you work with a coding agent, it reads CLAUDE.md. You are still the author: read the diff, run the checks above, and say in the PR what you ran.

## Dependencies

[Renovate](https://docs.renovatebot.com) keeps dependencies current (`renovate.json`) with `chore(deps)` PRs. Minor and patch updates merge on their own once the required checks pass; majors are reviewed one at a time. The tools zakwas downloads on users' machines, the pinned mise release (`Version` in `internal/modules/mise/bootstrap.go`) and the Homebrew installer commit (`installerCommit` in `internal/modules/brew/brew.go`), are labeled `bootstrap-pin` and never auto-merge. Don't bump them by hand in an unrelated PR, and keep the `// renovate:` comment above each, which is how Renovate finds it. See [docs/security.md](docs/security.md#tools-zakwas-bootstraps).

## Releases

Only the maintainer cuts releases. For reference:

1. In a PR, set `version` in `plugins/zakwas/.claude-plugin/plugin.json` to `X.Y.Z`. It is the only place the plugin version lives, and it must equal the release tag.
2. Merge the PR, then push the tag `vX.Y.Z` on that commit.
3. `.github/workflows/release.yml` checks the tag against `plugin.json` first and fails on a mismatch, before anything is published. Fix it in a merged PR, delete the tag and tag the new commit.
4. GoReleaser publishes darwin arm64 and amd64 archives, `checksums.txt` and the release notes, and updates the Homebrew cask in `Automaat/homebrew-tap` when the `HOMEBREW_TAP_TOKEN` secret is set.
5. A separate `attest` job signs build provenance for the archives. If it fails, re-run only that job.

Change the `zakwas` plugin's skill (`plugins/zakwas/skills/`) together with the `zakwas mcp` tools and `propose` operations it names.
