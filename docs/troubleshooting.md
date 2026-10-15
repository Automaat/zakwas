# Troubleshooting and FAQ

What a new user runs into first, and what to do. Every message below is what zakwas prints. `plan` and `check` never change the system, so run them freely while you sort something out.

- [`zakwas.yaml not found`](#zakwasyaml-not-found)
- [`apply` refuses to prompt](#apply-refuses-to-prompt)
- [The plan shows `edited in place` or `not managed yet` and a `.zakwas-bak` file](#the-plan-shows-edited-in-place-or-not-managed-yet-and-a-zakwas-bak-file)
- [`Operation not permitted` on a managed file](#operation-not-permitted-on-a-managed-file)
- [Homebrew or mise is missing](#homebrew-or-mise-is-missing)
- [`agents: plan failed: ... is not installed`](#agents-plan-failed--is-not-installed)
- [`refusing to apply from a ... session`](#refusing-to-apply-from-a--session)
- [Where zakwas keeps its state](#where-zakwas-keeps-its-state)
- [Stop managing a file, or remove zakwas](#stop-managing-a-file-or-remove-zakwas)

## `zakwas.yaml not found`

```
zakwas: zakwas.yaml not found
```

zakwas looks for `zakwas.yaml` in the current directory and then in each parent directory. Only when that finds nothing does it use `$ZAKWAS_CONFIG`. A run that starts somewhere else (a launchd job, an agent hook, a new shell in your home directory) therefore needs one of:

- `zakwas -c ~/dotfiles/zakwas.yaml plan`: `-c` overrides everything. A wrong path fails with `open <path>: no such file or directory`.
- `export ZAKWAS_CONFIG=~/dotfiles/zakwas.yaml` in your shell profile, to run zakwas from any directory.

See [commands.md](commands.md#environment).

## `apply` refuses to prompt

```
zakwas: stdin is not a terminal, so apply can't ask for confirmation; review with `zakwas plan`, then run `zakwas apply -y`
```

Exit code 64. `apply` asks before changing anything, and it won't guess an answer when no terminal is attached (a script, CI, a pipe). Review with `zakwas plan`, then run `zakwas apply -y`, or apply a reviewed plan: `zakwas plan -out plan.json`, then `zakwas apply -plan plan.json`.

## The plan shows `edited in place` or `not managed yet` and a `.zakwas-bak` file

```
files
  ~ ~/.zshrc      edited in place, back up to .zshrc.zakwas-bak
  ~ ~/.tmux.conf  not managed yet, back up to .tmux.conf.zakwas-bak
```

zakwas never overwrites or deletes a regular file it did not write without keeping a copy:

- `edited in place`: zakwas wrote this file earlier (it records a hash of each one in `~/.local/state/zakwas/files.json`), and the content has changed since. `apply` backs the file up and restores the repo's version.
- `not managed yet`: the destination exists and differs from the repo's file. `apply` backs it up, then installs the repo's version. A file that already matches is adopted as it is: `already matches, start tracking`.

The backup is `<file>.zakwas-bak`, or `<file>.zakwas-bak.N` when that name is taken. Nothing removes backups; delete them yourself once you have merged what you need into the repo. To keep an in-place edit, copy it into the repo's source file before you `apply`.

Adding `*.zakwas-bak*` to your config repo's `.gitignore` is what `zakwas init` does for you.

## `Operation not permitted` on a managed file

With `protect.immutable: true`, installed files are read-only and carry the macOS `uchg` flag, so `rm`, `chmod`, `:w!` and `echo >>` all fail with `Operation not permitted`. That is the protection working. The change belongs in the repo, followed by `zakwas apply`.

For a quick experiment on one file:

```bash
chflags nouchg ~/.zshrc && chmod u+w ~/.zshrc
```

`zakwas check` reports the file as drifted until you port the edit to the repo, and the next `apply` backs the edited file up and restores it. To stop setting the flag at all, remove `protect.immutable`: the plan then shows `clear immutable` for each file, and files stay read-only (`0444`) but removable.

## Homebrew or mise is missing

zakwas installs what it needs. With a `brew` section and no `brew` on `PATH`, the plan starts the `brew` module with `+ Homebrew`. With a `mise` section and no `mise`, it shows `+ mise@<version>` and then `mise install`. During `apply`:

- Homebrew is installed with the official installer, pinned to one commit. It needs `sudo`, so run `apply` in a terminal and expect a password prompt.
- mise is a pinned release downloaded to `~/.local/bin/mise`, with its SHA256 checked first. If your Brewfile lists `mise`, the Homebrew one is used instead.
- `/opt/homebrew/bin`, `/usr/local/bin` and `~/.local/bin` are added to `PATH` for the rest of the run, so later modules find what an earlier one installed. A shell that is already open does not see them; open a new terminal after the first `apply`.

Details: [security.md](security.md#tools-zakwas-bootstraps) and [security.md](security.md#sudo).

## `agents: plan failed: ... is not installed`

```
✗ agents: plan failed: gemini: gemini is not installed
```

Each provider plans on its own. When a provider's CLI is missing, its error is reported, the other providers and modules still plan and apply, and the run exits 1. Either install the CLI (the `agents` module runs after `brew` and `mise`, but it plans before anything is installed, so run `apply` a second time to get the plugins) or take the provider out of `agents.providers`.

- A CLI is required only for what calls it: Claude Code, Codex and Copilot CLI for plugins and marketplaces, Gemini CLI always once it is named. A Mac without Claude Code still plans `instructions`, `mcp` and `permissions` for it.
- Codex, Gemini CLI and Copilot CLI are converged only when `zakwas.yaml` names them, so their errors appear only then.
- opencode is best effort unless you name it: without its CLI it is skipped without an error.

The provider table is in [agents.md](agents.md#providers).

## `refusing to apply from a ... session`

```
zakwas: refusing to apply from a Claude Code session: an agent that can apply the config can widen its own permissions, add hooks or install marketplaces.
```

With an `agents` section the guard is on. `apply`, `upgrade` and `approve` exit 1 before they change anything when a coding agent started the shell they run in; `plan`, `check`, `capture` and `propose` still work. Run the command in a terminal window you opened yourself, outside the agent.

zakwas recognizes an agent session by environment variables the agents set (`CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`, `CODEX_SANDBOX`, `OPENCODE`, `GEMINI_CLI`, `COPILOT_AGENT_SESSION_ID` and a few more). A terminal that was opened from inside an agent inherits them: check with `env | grep -E 'CLAUDE|CODEX|OPENCODE|GEMINI|COPILOT'` and open a fresh window. To turn the guard off, set `agents.guard: false`; read [security.md](security.md#coding-agents) first.

## Where zakwas keeps its state

Everything lives under `~/.local/state/zakwas/`:

| Path | What |
|---|---|
| `files.json` | the hash of every file zakwas wrote, to tell an edit from a repo change |
| `history.jsonl` | every apply with its changes and the config repo commit it ran from |
| `lock` | serializes `apply`, `upgrade` and `approve`; a crashed run leaves nothing to clean up |
| `proposals/` | pending [proposals](commands.md#propose-proposals-approve) |
| `launchd.json`, `agent-*.json`, `instructions.json`, `skills.json`, `opencode.json`, ... | what the `launchd` and `agents` modules added, so they remove only that |

zakwas also keeps clones of git skill sources and opencode marketplaces under `~/.local/share/zakwas/`. Config and the files it installs live where you declared them.

The state is a record, not data: deleting `files.json` loses nothing. The next plan lists the files that match as `already matches, start tracking`, and backs up the ones that differ. The records of the `agents` and `launchd` modules are what lets zakwas remove only its own entries; if you delete them, it no longer removes what it added earlier.

## Stop managing a file, or remove zakwas

A file you drop from `files` or `templates` is removed by the next `apply` (backed up first if it was edited), and the plan shows it:

```
files
  - ~/.zshrc  no longer managed

⚠ destructive: ~/.zshrc
```

To keep the content, apply, then copy it back from the repo: `cp -p dotfiles/zshrc ~/.zshrc`. The copy is a plain, writable file that zakwas no longer tracks.

To stop using zakwas altogether:

1. Empty the config (`files: []`, drop the other sections) and `zakwas plan`. Read the `-` lines, then `zakwas apply`. Copy back anything you want to keep first, as above.
2. What a removed entry does depends on the module:
   - `files`, `templates`: the copies are deleted (backed up first if edited). `links`: the symlinks stay where they are; remove them yourself.
   - `launchd`: the job is unloaded and its plist deleted, only for jobs zakwas created.
   - `agents`: the MCP servers, permission rules, hooks, profiles, instruction copies and skill links zakwas recorded are removed. Installed plugins and marketplaces stay, unless `agents.prune: true` is set, which removes every undeclared one.
   - `brew` and `mise`: installed packages and tools stay. Only `brew.cleanup` (`uninstall` or `zap`) and `mise.prune` remove what a Brewfile or mise config no longer lists, and they run only while their section exists. Run `brew uninstall` yourself for the rest.
   - `system`, `defaults`, `commands`: not reverted. Directories, the SSH key, Touch ID for `sudo` and `defaults write` values stay as they are.
3. Delete the binary (`rm ~/.local/bin/zakwas` for the installer, `brew uninstall zakwas` for Homebrew, or drop the pin from your mise config), `~/.local/state/zakwas/` and `~/.local/share/zakwas/`.
4. Remove the leftover `*.zakwas-bak` backups once you do not need them.
