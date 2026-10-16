# Getting started

The [README quickstart](../README.md#try-it-in-two-minutes) installs one dotfile. This page is the loop you use after that: build a config repo, plan, apply, change things, catch drift, set up a second Mac. In this loop only `apply` changes your Mac. The output below is trimmed from real runs.

## Install

If you ran the quickstart, zakwas is installed. Otherwise:

```bash
brew install automaat/tap/zakwas
```

Other ways to install are in the [README](../README.md#install). `zakwas version` checks that it works.

## Create the config repo

```bash
zakwas init ~/dotfiles --add ~/.zshrc
cd ~/dotfiles
```

`init` reads this Mac and writes a new git repo. Nothing outside `~/dotfiles` changes. The directory must not exist or be empty (exit 64 otherwise). The quickstart already made `~/dotfiles` this way, so if you ran it, skip to the next section or pick another directory. `--add` copies a file or directory under `$HOME` into the repo; repeat it for more.

```
Created ~/dotfiles
  zakwas.yaml
  .gitignore
  Brewfile                      taps, formulae, casks, mas apps from brew bundle dump
  dotfiles/mise/config.toml     global mise config, active tools pinned to exact versions
  dotfiles/zshrc                ← ~/.zshrc
```

## Read zakwas.yaml

`init` writes this file with a schema comment on the first line and block-style entries; it is condensed here.

```yaml
protect:
  immutable: true

files:
  - {src: dotfiles/zshrc, dst: ~/.zshrc}
  - {src: dotfiles/mise/config.toml, dst: ~/.config/mise/config.toml}

brew:
  file: Brewfile
  cleanup: none

mise:
  config: dotfiles/mise/config.toml
```

- [`protect`](config.md#protect): keep it. Installed files are read-only and carry the `uchg` flag. Delete the section if you want read-only files you can still remove.
- [`files`](config.md#files): one entry per `--add`, plus the mise config. Delete what you do not want managed. To manage another file, copy it under `dotfiles/` and add a line.
- [`brew`](config.md#brew): installs what `Brewfile` lists. `init` filled it from `brew bundle dump`, so open it and delete what a new Mac does not need. Leave `cleanup: none`; `zap` removes every installed package the Brewfile does not list.
- [`mise`](config.md#mise): installs the tools in the mise config, which `init` pinned to the versions you run now.

`init` leaves out a section when its tool is missing, and it never writes `defaults` or `commands`. Add them when you want them:

```yaml
defaults:
  - {domain: com.apple.dock, key: autohide-delay, value: 0.0}
  - {domain: NSGlobalDomain, key: KeyRepeat, value: 1}

commands:
  - name: rosetta
    check: /usr/bin/pgrep -q oahd
    run: softwareupdate --install-rosetta --agree-to-license
```

[`defaults`](config.md#defaults) writes macOS preferences; the YAML type picks the `defaults` type. [`commands`](config.md#commands) runs `run` only while `check` fails.

## Plan

```bash
zakwas plan
```

```
files
  ~ ~/.zshrc                    mode 644 → 444, set immutable, already matches, start tracking
  ~ ~/.config/mise/config.toml  mode 644 → 444, set immutable, already matches, start tracking
✓ up to date: system, links, templates, brew, mise, commands, defaults

Plan: 0 to add, 2 to change, 0 to remove, 0 to run.
```

This is the result when the files on the Mac already match the repo. If your mise config differs from the copy, its line reads `not managed yet`; with no mise config it is a `+` line. `plan` never changes anything. Each line starts with a symbol: `+` add, `~` change, `-` remove, `▶` run a step. A removal of an unedited file is repeated under `⚠ destructive`. The text after the target says why:

- `content`: the repo file changed. Apply overwrites the installed copy.
- `already matches, start tracking`: the file is already identical. Apply records it and sets the protection.
- `not managed yet, back up to X.zakwas-bak`: a different file is already there. Apply moves it aside first.
- `edited in place, back up to X.zakwas-bak`: zakwas wrote the file earlier and someone edited the copy. Apply backs it up, then restores the repo version.
- `no longer managed`: you dropped it from the config. Apply deletes it, backed up first if it was edited.

zakwas never deletes a `.zakwas-bak` file (`.zakwas-bak.1` if the name is taken). `init` already put `*.zakwas-bak*` in `.gitignore`. More in [troubleshooting](troubleshooting.md#the-plan-shows-edited-in-place-or-not-managed-yet-and-a-zakwas-bak-file). Add `--diff` to see file contents.

## Apply

```bash
zakwas apply --only files
```

`--only` limits the run to the named modules. `apply` shows the plan, asks `Apply 2 step(s)? [y/N]`, then prints one line per step. A warning about uncommitted changes is expected until you commit the repo.

```
[1/2] files ~ ~/.zshrc ✓ 9ms
[2/2] files ~ ~/.config/mise/config.toml ✓ 9ms

Applied 2 of 2 steps in 18ms.
```

When the plan looks right, run `zakwas apply` for everything. That installs the Brewfile and the mise tools, and installs Homebrew or mise first if they are missing (Homebrew asks for your password). Read the `+` and `-` lines before you confirm.

## Change a dotfile

Installed files are read-only copies. An edit in place would drift from the repo, and the next apply would back it up and revert it. The repo is the source of truth: edit there, then apply.

```bash
$EDITOR dotfiles/zshrc
zakwas plan --diff --only files
zakwas apply --only files
```

The plan shows `~ ~/.zshrc  content` and, with `--diff`, the added lines. For a quick experiment on one file, run `chflags nouchg ~/.zshrc && chmod u+w ~/.zshrc`. `check` flags the file until you move the change into the repo. For configs an app must write itself, use [`links`](config.md#links) instead of `files`.

## Check for drift

```bash
zakwas check
echo $?
```

`check` runs the plan and exits 0 when the Mac matches, 2 when anything would change, 1 on an error. After a repo edit you have not applied, it prints `~ ~/.zshrc  content` and exits 2. Use it in a script, CI or a launchd job.

```bash
zakwas check --offline
```

`--offline` compares only `files`, `links` and `templates` with what zakwas recorded and starts no command, so it takes milliseconds. Its last line names the modules it skipped; a full `check` covers those.

## Commit and push

```bash
git add -A
git commit -m "Initial config"
git remote add origin git@github.com:you/dotfiles.git
git push -u origin main
```

`init` already ran `git init -b main`. `apply` warns when the repo has uncommitted changes, is not on `main`, or is behind upstream, and records the commit it ran from in `~/.local/state/zakwas/history.jsonl`. Keep secrets out of the repo.

## Set up a second Mac

```bash
curl -fsSL https://raw.githubusercontent.com/Automaat/zakwas/main/install.sh |
  bash -s -- --repo https://github.com/you/dotfiles.git
```

The installer starts the Xcode Command Line Tools install if they are missing (finish the dialog, then press a key; Linux has no such step), installs a checksum-verified zakwas to `~/.local/bin`, clones the repo to `~/dotfiles` (`--dir` changes that; an existing directory is not cloned again, and `--repo` is required when it does not exist) and runs `zakwas apply`. Pass `--no-apply` to stop before applying, for example when the config needs a manual step first; then `cd ~/dotfiles && ~/.local/bin/zakwas plan`, since `~/.local/bin` is not on `PATH` yet. `--version X.Y.Z` pins the release. Open a new terminal afterwards so `PATH` picks up what was installed.

## Next

- [Coding agents](agents.md): MCP servers, instructions, skills and permissions for Claude Code, Codex and others.
- [launchd jobs](config.md#launchd): scheduled jobs; the [daily drift notification](recipes.md#daily-drift-notification) recipe is ready to copy.
- [Recipes](recipes.md): tested snippets for common setups.
- [Troubleshooting](troubleshooting.md): first-use errors, backups, immutable files, removing zakwas.
- [Config reference](config.md) and [command reference](commands.md): every key and flag.
