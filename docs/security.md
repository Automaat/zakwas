# Security

How zakwas limits what it downloads and runs on your Mac.

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

An agent that can run `zakwas apply` can write agent config files it can't edit itself: permissions, hooks, marketplaces. With an `agents` section zakwas therefore denies the managed agents `zakwas apply`, `upgrade`, `self-update`, `approve` and `mcp`, denies Claude Code edits to the config repo and zakwas state, and refuses `apply`, `upgrade` and `approve` started from an agent session. The deny rules are the boundary: zakwas can't prove a person is at the terminal, since an agent can unset environment variables and get a terminal through `script` or a PTY. `zakwas propose` stays allowed, since it changes nothing, and so does `zakwas capture`, which only saves a proposal with `--write`; `zakwas approve` is denied and refuses agent sessions the same way, so a proposal an agent saved is applied only by a person at a terminal, after the diff and plan are shown. See [`agents.guard`](config.md#guard) for what is covered and how to turn it off.

The one way an agent applies anything itself is `zakwas_apply` of [`zakwas mcp --enable-apply`](commands.md#mcp), which you turn on by registering the server that way. It applies only a low-risk proposal, rated again when called, and only after you confirm a diff and plan zakwas rendered, in a form the client shows you; a client that can't show it, a high-risk proposal and any other answer than yes apply nothing and give `zakwas approve ID` for a terminal. The tool is marked so Claude Code prompts on every call even in bypass mode, and Codex gets `approval_mode = "prompt"` for it. What stops an agent from answering its own form is that it does not start the server: the guard denies it `zakwas mcp`. Like the other deny rules this matches the command as the agent writes it, so a wrapper (`sh -c`, a script) is outside it, and an agent that starts the server through one is the client that answers the form. A person who runs the server in a shell outside the agent is out of scope.

## sudo

zakwas runs `sudo` in two places only, and only during `apply`:

- `system.sudoTouchID`: writes the Touch ID line to `/etc/pam.d/sudo_local`.
- Homebrew bootstrap: `sudo -v` primes credentials so the Homebrew installer can run non-interactively; the installer itself needs root to create `/opt/homebrew`.

`plan` and `check` never call `sudo`. Scripts in your own `commands` run as written, so any `sudo` there is yours.
