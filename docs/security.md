# Security

How zakwas limits what it downloads and runs on your Mac.

zakwas runs as your user and changes what your config declares. Your config repo is code that runs as you: `commands` and agent hooks run what it says, and files and templates write what it says. Apply only a config you trust, and read `zakwas plan` first. `plan` and `check` never change the system.

## Release artifacts

Releases are built by GoReleaser in GitHub Actions (`.github/workflows/release.yml`). From 0.4.0 on, a follow-up `attest` job signs [build provenance attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations) for every `zakwas_*_darwin_*.tar.gz` archive and for `checksums.txt`. An attestation ties the file to the repo, workflow, and tag that built it. The release is public a minute or two before that job finishes, and stays unattested until it's re-run if it fails; in either window `install.sh` refuses it when a logged-in `gh` new enough to verify is present (older or logged-out `gh` skips the check). Re-run only the `attest` job; it re-attests the uploaded `dist` artifact, which GitHub keeps for 90 days.

### What `install.sh` checks

1. Downloads the archive and `checksums.txt` from the release, and stops if the archive's SHA256 doesn't match.
2. If a `gh` recent enough to support `--source-ref` is installed and logged in (`gh auth status` succeeds), runs `gh attestation verify` on the archive and stops if that fails. It requires the attestation to come from `Automaat/zakwas`, signed by `.github/workflows/release.yml`, for the tag `v<version>`.
3. Without a new enough, authenticated `gh`, it skips step 2 and prints the command to verify by hand.
4. For `--version` older than 0.4.0 it skips step 2: those releases carry no attestation.

The checksum alone catches a corrupted or truncated download. It can't catch a tampered release, because `checksums.txt` comes from the same place as the archive. The attestation can: it is signed through GitHub's OIDC identity for the release workflow, which a modified asset can't reproduce.

### What `zakwas self-update` checks

The same steps: SHA256 against `checksums.txt`, then `gh attestation verify` with the same `--repo`, `--signer-workflow` and `--source-ref` when a logged-in `gh` that supports `--source-ref` is installed (skipped, with the manual command printed, otherwise; skipped for versions before 0.4.0). A failed verification leaves the installed binary untouched.

### Verify by hand

```bash
gh release download v0.4.0 -R Automaat/zakwas -p 'zakwas_0.4.0_darwin_arm64.tar.gz'
gh attestation verify zakwas_0.4.0_darwin_arm64.tar.gz --repo Automaat/zakwas \
  --signer-workflow Automaat/zakwas/.github/workflows/release.yml \
  --source-ref refs/tags/v0.4.0
```

Use `darwin_amd64` on an Intel Mac. `checksums.txt` verifies the same way.

## Tools zakwas bootstraps

When the config uses them and they are missing, zakwas installs Homebrew and mise itself. Both are pinned in the source, so a new upstream version only reaches machines through a reviewed zakwas change. Renovate opens the bump PR, labeled `bootstrap-pin`, and never auto-merges it; a mise release must also be 3 days old before Renovate proposes it.

- **mise**: a fixed release (`Version` in `internal/modules/mise/bootstrap.go`). zakwas downloads the binary from that GitHub release and checks its SHA256 against the release's `SHASUMS256.txt` before installing it to `~/.local/bin/mise`.
- **Homebrew**: the official installer script, fetched from a fixed commit of `Homebrew/install` (`installerCommit` in `internal/modules/brew/brew.go`) instead of the moving `HEAD`.

## Coding agents

An agent that can run `zakwas apply` can write the agent config files it may not edit itself: permissions, hooks, MCP servers, marketplaces. One prompt injection in a README or a web page is enough for an agent to ask zakwas to widen its own permissions. With an `agents` section zakwas therefore keeps the managed agents from applying the config, and sends every change an agent wants through a proposal that a person approves. The guard is on by default; [`agents.guard`](config.md#guard) lists the rules and how to turn it off.

### What the guard does

- **Denies commands.** Every managed agent is denied `zakwas apply`, `upgrade`, `self-update`, `approve` and `mcp`. Claude Code, Gemini CLI and opencode also match a flag before the command (`zakwas -c zakwas.yaml apply`) and a path (`~/.local/bin/zakwas apply`); Codex matches the command as written.
- **Denies edits.** Claude Code is denied edits to the config repo's main checkout (and the linked worktree zakwas runs from) and to `~/.local/state/zakwas`. Worktrees an agent creates elsewhere stay editable, so an agent can still propose a change as a branch.
- **Refuses sessions.** `zakwas apply`, `upgrade` and `approve` exit 1 before changing anything when they start inside an agent session.
- **Leaves reading and proposing open.** `plan`, `check`, `capture` and `propose` change nothing outside a saved proposal, so agents keep them.

### Why deny rules are the boundary

zakwas recognizes an agent session by environment variables the agents set (`CLAUDECODE`, `CODEX_SANDBOX`, `OPENCODE`, `GEMINI_CLI`, `COPILOT_AGENT_SESSION_ID`, and a few more). That cannot be a boundary: an agent can unset a variable, and it can get a terminal through `script` or a PTY, so zakwas cannot prove a person is at the keyboard. The refusal catches accidents and injections that don't know about it.

The deny rules are the boundary, because the agent's own runtime enforces them. Claude Code applies its `permissions.deny` in every permission mode, `bypassPermissions` included. They match the command as the agent writes it. A wrapper (`sh -c '...'`, a script, a PTY) is outside what they match, so keep the agent's own approval mode on for shells you don't trust.

### Per-agent coverage

| Agent | Command rules | Caveat |
|---|---|---|
| Claude Code | `permissions.deny` in `settings.json`; also edit rules | Edit rules cover Claude's file tools, not a shell command that writes a file. |
| Codex | `forbidden` rules in `rules/zakwas.rules` | Matches the command as written: `zakwas apply` is denied, `zakwas -c zakwas.yaml apply` is not. Codex applies the strictest rule of all files, so no other file loosens these. No edit rules. |
| opencode | `deny` patterns, written only through the `agentPermissions` template function | Not protected until a template renders it into `opencode.json`; the plan warns. No edit rules. |
| Gemini CLI | `deny` rules in `policies/zakwas.toml` | Match flags before the command, the binary by path or quoted, and the command word quoted, in any case. They miss a wrapper (`env`, `sudo`, `bash -lc`), a variable or other spelling of the binary, and, in YOLO approval mode, any chain, subshell or `bash -c`; the exact list is in [config.md](config.md#gemini-cli). No edit rules. |
| Copilot CLI | none | Copilot keeps user-level deny rules only in root-owned managed settings, so only the refusal covers it, and an agent that unsets its marker variable gets past it. The plan warns. |

Codex, Gemini CLI and Copilot CLI are guarded only when `agents.providers`, a marketplace's or a plugin's `providers` names them. Naming one only in an `mcp`, `skills`, `profiles`, `permissions` or `hooks` entry converges it but leaves it unguarded. A guard that can't cover a managed provider is a warning in `plan`, not a change, so `check` stays clean.

On Windows the Claude and Codex rules deny `zakwas apply` as a Bash command only, and name `zakwas`, not `zakwas.exe`. An agent can run the same command from PowerShell or `cmd`, or as `zakwas.exe`, and no rule stops it; the refusal inside zakwas, which looks for the agent's environment variables, is the only barrier there. Treat the guard as incomplete on Windows until the PowerShell and `cmd` rules land.

### Proposals and who may approve

An agent that wants a change runs `zakwas propose` (or calls `zakwas_propose` over MCP). The proposal is saved with its diff, plan and risk tier; nothing else changes. A person applies it with `zakwas approve ID`, which shows the diff and plan again, computed from the files as they are now, and needs a terminal on stdin with no `-y`. It refuses to run in an agent session and is denied to the agents.

Each proposal has one of two tiers, decided by zakwas from what it does, never from what the agent says:

| Tier | What it covers |
|---|---|
| low | adds something from a source the config already trusts: a plugin from a declared marketplace, a core Homebrew formula, a mise pin from the registry; by operation a `deny` or `review` rule or removing an `allow` rule is low too, but its plan writes agent config files, which makes it high |
| high | everything else: an `allow` rule, removing a `deny` or `review` rule, an MCP server, a marketplace, a cask or tap formula, a mise tool from a backend such as `npm:` or `github:` |

A proposal is also high when the plan it will run touches agent config (`~/.claude`, `~/.codex`, `~/.config/opencode`, `~/.agents`, `~/.gemini`, `~/.copilot`), a shell rc file, `~/.ssh` or `/etc`, or when a module of that plan fails to plan. The tier stored in the proposal file is not trusted: it is rated again when used.

High needs a terminal because those changes decide what agents run unasked, where their code comes from and which services they reach. The only confirmation an agent can't answer for itself is a prompt on a terminal that the agent's own commands don't control, so that is what `approve` demands. A confirmation inside the agent is shown by the agent's client, which is the party the injection may be steering; zakwas allows it only for low-tier changes, where a wrong yes loosens no rule and adds no new source of code.

### `zakwas mcp --enable-apply`

[`zakwas mcp`](commands.md#mcp) serves read-only tools and `zakwas_propose` to an agent. Started as `zakwas mcp --enable-apply` it adds `zakwas_apply`, the one way an agent applies anything itself. It applies only a low-risk proposal, rated again when called, and only after you confirm a diff and plan that zakwas renders in a form the client shows you. A high-risk proposal, a client that can't show the form, and any answer but yes apply nothing and return `zakwas approve ID`. Claude Code prompts on every `zakwas_apply` call, even in bypass mode, and Codex gets `approval_mode = "prompt"` for it.

What stops an agent from answering its own form is that it does not start the server: you register it in `agents.mcp`, and the guard denies the agent `zakwas mcp`. That denial is a command rule, so it has the limit above. An agent that can run a wrapper such as `sh -c 'zakwas mcp --enable-apply -c ...'` or a script can start the server itself, speak MCP to it over pipes and answer the form: it is both client and user. Two things follow:

- Not registering `--enable-apply` does not remove the risk. The flag is an argument of the command the agent would write, so a wrapper can pass it.
- No environment check stands in the way. The server has to run inside agent sessions, so unlike `apply` it has no session refusal.

What remains bounded is the damage: the wrapped server applies only a low-risk proposal, checks it again before writing, commits to the current branch without pushing, and records the apply in the history. High-risk changes still need `zakwas approve` in a terminal. To close the gap, don't let the agent run shell wrappers unreviewed: keep its own approval prompts on for commands the deny rules don't recognize. A person who starts the server in a shell outside the agent is out of scope.

## sudo

zakwas runs `sudo` in two places only, and only during `apply`:

- `system.sudoTouchID`: writes the Touch ID line to `/etc/pam.d/sudo_local`.
- Homebrew bootstrap: `sudo -v` primes credentials so the Homebrew installer can run non-interactively; the installer itself needs root to create `/opt/homebrew`.

`plan` and `check` never call `sudo`. Scripts in your own `commands` run as written, so any `sudo` there is yours.
