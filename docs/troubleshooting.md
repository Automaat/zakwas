# Troubleshooting and FAQ

What a new user runs into first, and what to do. Every message below is what zakwas prints. `plan` and `check` never change the system, so run them freely while you sort something out.

- [`zakwas.yaml not found`](#zakwasyaml-not-found)
- [`apply` refuses to prompt](#apply-refuses-to-prompt)
- [The plan shows `edited in place` or `not managed yet` and a `.zakwas-bak` file](#the-plan-shows-edited-in-place-or-not-managed-yet-and-a-zakwas-bak-file)
- [`Operation not permitted` on a managed file](#operation-not-permitted-on-a-managed-file)
- [Homebrew or mise is missing](#homebrew-or-mise-is-missing)
- [`not supported on Linux`](#not-supported-on-linux)
- [`agents: plan failed: ... is not installed`](#agents-plan-failed--is-not-installed)
- [`refusing to apply from a ... session`](#refusing-to-apply-from-a--session)
- [Where zakwas keeps its state](#where-zakwas-keeps-its-state)
- [Windows](#windows)
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

On macOS, with `protect.immutable: true`, installed files are read-only and carry the macOS `uchg` flag, so `rm`, `chmod`, `:w!` and `echo >>` all fail with `Operation not permitted`. That is the protection working. The change belongs in the repo, followed by `zakwas apply`.

For a quick experiment on one file:

```bash
chflags nouchg ~/.zshrc && chmod u+w ~/.zshrc
```

`zakwas check` reports the file as drifted until you port the edit to the repo, and the next `apply` backs the edited file up and restores it. To stop setting the flag at all, remove `protect.immutable`: the plan then shows `clear immutable` for each file, and files stay read-only (`0444`) but removable.

## Homebrew or mise is missing

zakwas installs what it needs. With a `brew` section and no `brew` on `PATH`, the plan starts the `brew` module with `+ Homebrew`. With a `mise` section and no `mise`, it shows `+ mise@<version>` and then `mise install`. During `apply`:

- Homebrew is installed with the official installer, pinned to one commit. It needs `sudo`, so run `apply` in a terminal and expect a password prompt.
- mise is a pinned release downloaded to `~/.local/bin/mise`, with its SHA256 checked first. If your Brewfile lists `mise`, the Homebrew one is used instead.
- `/opt/homebrew/bin`, `/usr/local/bin` and `~/.local/bin` are added to `PATH` for the rest of the run (on Linux, `/usr/local/bin` and `~/.local/bin`), so later modules find what an earlier one installed. A shell that is already open does not see them; open a new terminal after the first `apply`.

Details: [security.md](security.md#tools-zakwas-bootstraps) and [security.md](security.md#sudo).

## `not supported on Linux`

```
zakwas: zakwas.yaml: brew: not supported on Linux (it bootstraps Homebrew for macOS and manages casks); see docs/config.md#platform-support
```

`brew`, `mise`, `defaults`, `launchd` and `system.sudoTouchID` are macOS only. Every command (`plan`, `check`, `apply`, `--only`, `propose`) rejects a config that uses one on Linux, naming each, before anything changes; exit code 1 for a config, 64 for `--only`. Remove the section for the Linux machine. To share one repo between a Mac and a Linux box, keep a second config for Linux (see `examples/linux/zakwas.yaml`) and pass `-c`; the dotfile sources can be shared.

Other Linux notes:

- `protect.immutable: true` is accepted. There is no `uchg` flag, so installed files stay read-only (`0444`) and hash-tracked; `check` stays clean and the plan prints `protect.immutable is macOS-only; files are read-only copies` once (from the `files` module, so `--only agents` does not show it). A read-only file is still removable by its owner (`rm -f`), and `apply` restores it.
- zakwas does not install your agent CLIs. A missing one is reported per provider, as in the next section; install it first (Claude Code's installer puts `claude` in `~/.local/bin`, which zakwas adds to `PATH`).
- The installer needs `curl`, `tar`, `sha256sum` (or `shasum`) and, to clone your repo, `git`. It prints a `PATH` hint when `~/.local/bin` is not on `PATH`.
- A `check` in `commands` that hangs is cut off after 30 seconds. Without a terminal (a hook, cron, CI) it runs in its own process group: the group gets SIGINT at the timeout and SIGKILL 30 seconds later, so children that ignore SIGINT or left the shell do not outlive that. With a terminal, checks stay in the terminal's foreground group so they can still read `/dev/tty`; a hung one is interrupted at the timeout, and a child that ignores SIGINT can keep running. The group does not cover a child that starts its own session (`setsid sleep 600 &`), and a check that exits normally leaves its background children running, as it always did. When zakwas itself gets SIGINT, SIGTERM or SIGHUP, it interrupts the check first, then exits 130, 143 or 129.
- Signals: SIGINT, SIGTERM and SIGHUP cancel the run and interrupt the commands in flight, and zakwas exits 130, 143 or 129. If SIGTERM or SIGHUP finds it blocked (at a confirmation prompt, say), it dies of the signal after 2 seconds, as an unhandled signal would; Ctrl-C at a prompt waits for Enter, as before. A SIGHUP the process started ignoring, as under `nohup`, stays ignored; SIGTERM is always honored. Known gaps: a `run:` step is not in the check's process group, so a signal during one leaves its child running; and a cancelled `propose` or `capture --write` can still have saved a high-risk proposal, which `approve` recomputes before it applies anything.
- On case-insensitive macOS, spelling the opencode skills directory in a different case than before (`XDG_CONFIG_HOME=$HOME/.CONFIG`, or `opencode.skillsDir`) makes the first plan show `track as` changes, and `check` exits 2, until one `apply` re-keys the state. Nothing is deleted.
- Names compared without case (overlapping destinations, skill names) stay case-insensitive, even though Linux file systems are not: `~/.Foo` and `~/.foo` are rejected as overlapping.

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

## Windows

Windows support is early; the agent features and the portable core run, see [agents.md](agents.md#windows). The messages you meet:

- `brew: not supported on Windows (...); see docs/config.md#platform-support`, and the same for `system`, `links`, `mise`, `commands`, `launchd`, `defaults` and the options `system.sudoTouchID` and `agents.skills`: Windows rejects a config that uses them, like Linux does for the macOS sections. Remove them from the config you use on Windows, or keep a separate `zakwas.yaml` for it. Use `files` where you had `links`. [Platform support](config.md#platform-support) lists what runs where.
- `zakwas: self-update is not supported on Windows yet`: download the newer zip or run `install.ps1` again.
- `claude: command not found` while it works in your terminal: zakwas starts agent CLIs without a shell, so they must be on `PATH` as `.exe` or `.cmd`. npm's `claude.cmd` and `codex.cmd` are found; a `.ps1` alone is not. Open a new terminal after installing an agent so the process sees the new `PATH`.
- `stdin is not a terminal, so approve can't ask for confirmation` in Git Bash: mintty hands programs a pipe, not a console. Run `zakwas approve` from PowerShell or Windows Terminal, or prefix it with `winpty`.
- `Access is denied` replacing a file: another program holds it open, such as an editor or the agent itself. zakwas retries briefly, then fails; close the program and apply again.
- A managed file is read-only because zakwas set the read-only attribute. `protect.immutable` has no effect on Windows and `plan` says so. Clear it with `attrib -r <file>` only for a quick experiment.
- Paths in `zakwas.yaml` keep `~/` and `/`; `~\` and `C:\...` also work. A template that writes `{{ .Home }}` into JSON gets backslashes: use `{{ .Home | printf "%q" }}` or write the path with forward slashes.
- A destination under a OneDrive folder, or any other directory that is a reparse point but not a symlink, can be refused with "is a symlink; remove it": the parent checks treat every reparse point as a link, because Go reports junctions the same way. Unverified for OneDrive placeholders; move the destination out of the synced folder.
- The guard's `Edit(...)` deny rule for a config repo outside your home on another drive is written as `/D:/repo`. It is not confirmed against Claude Code on Windows (it may want `//d/repo`), so that edit denial may never match; a repo under your home uses `~/` and is fine. Phase 2.
- When a command is cancelled, zakwas kills the command and what it started (`taskkill /T`), so an npm `.cmd` shim does not keep the output pipes open.
- Long paths: zakwas does not add the `\\?\` prefix. Turn on long path support in Windows if a state or cache path fails with a file name error.

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
