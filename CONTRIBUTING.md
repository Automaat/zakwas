# Contributing

Issues and PRs are welcome. For anything bigger than a bug fix, open an issue first so we can agree on the approach.

## Setup

```bash
git clone https://github.com/Automaat/zakwas && cd zakwas
mise install                # Go, golangci-lint, shellcheck, actionlint, goreleaser
mise run test               # unit + e2e with fake brew/mise/defaults, no system changes
mise run test:integration   # adds real `defaults` and mise round-trips (macOS)
mise run lint
```

Try your build against the example config with a throwaway `$HOME`, so your real zakwas state isn't read:

```bash
HOME=$(mktemp -d) go run ./cmd/zakwas -c examples/zakwas.yaml plan --only files,templates
```

[CLAUDE.md](CLAUDE.md) describes the layout, module order, and how to add a module.

## Rules

- `plan` and `check` never change the system.
- Every external command goes through `runner.Runner`, so tests use `runnertest.Fake`.
- New modules: converge twice in tests and assert the second plan is empty.
- Nothing user-specific in code or `examples/`: no personal paths, taps or keys.
- Machine-readable output is versioned (`engine.FormatVersion`): adding a field is fine, renaming or removing one bumps the version.
- Comments explain a non-obvious *why*, never *what*.

## Commits and PRs

- Conventional commits with a scope: `feat(brew): …`, `fix(install): …`, `docs(readme): …`; title ≤ 50 characters.
- Sign off (`git commit -s`); commits are DCO-signed.
- PRs are squash-merged; the PR title becomes the changelog entry, so make it read well.
- CI must pass: tests, lint, shellcheck, actionlint, and the fresh-Mac e2e jobs.
