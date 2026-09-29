# Security

How zakwas limits what it downloads and runs on your Mac.

## Release artifacts

Releases are built by GoReleaser in GitHub Actions (`.github/workflows/release.yml`). From 0.4.0 on, the release job also signs [build provenance attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations) for every `zakwas_*_darwin_*.tar.gz` archive and for `checksums.txt`. An attestation ties the file to the repo, workflow, and commit that built it.

### What `install.sh` checks

1. Downloads the archive and `checksums.txt` from the release, and stops if the archive's SHA256 doesn't match.
2. If `gh` is installed and logged in (`gh auth status` succeeds), runs `gh attestation verify` on the archive against `Automaat/zakwas` and stops if that fails.
3. Without an authenticated `gh`, it skips step 2 and prints the command to verify by hand.
4. For `--version` older than 0.4.0 it skips step 2: those releases carry no attestation.

The checksum alone catches a corrupted or truncated download. It can't catch a tampered release, because `checksums.txt` comes from the same place as the archive. The attestation can: it is signed through GitHub's OIDC identity for the release workflow, which a modified asset can't reproduce.

### Verify by hand

```bash
gh release download v0.4.0 -R Automaat/zakwas -p 'zakwas_0.4.0_darwin_arm64.tar.gz'
gh attestation verify zakwas_0.4.0_darwin_arm64.tar.gz --repo Automaat/zakwas
```

Use `darwin_amd64` on an Intel Mac. `checksums.txt` verifies the same way.

## Tools zakwas bootstraps

When the config uses them and they are missing, zakwas installs Homebrew and mise itself. Both are pinned in the source, so a new upstream version only reaches machines through a reviewed zakwas change (Renovate opens the bump PR).

- **mise**: a fixed release (`Version` in `internal/modules/mise/bootstrap.go`). zakwas downloads the binary from that GitHub release and checks its SHA256 against the release's `SHASUMS256.txt` before installing it to `~/.local/bin/mise`.
- **Homebrew**: the official installer script, fetched from a fixed commit of `Homebrew/install` (`installerCommit` in `internal/modules/brew/brew.go`) instead of the moving `HEAD`.

## sudo

zakwas runs `sudo` in two places only, and only during `apply`:

- `system.sudoTouchID`: writes the Touch ID line to `/etc/pam.d/sudo_local`.
- Homebrew bootstrap: `sudo -v` primes credentials so the Homebrew installer can run non-interactively; the installer itself needs root to create `/opt/homebrew`.

`plan` and `check` never call `sudo`. Scripts in your own `commands` run as written, so any `sudo` there is yours.
