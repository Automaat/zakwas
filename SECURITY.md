# Security policy

zakwas runs with your user's permissions, asks for `sudo` (Touch ID for sudo, the Homebrew installer), downloads binaries (mise, Homebrew) and deletes files it no longer manages. Bugs in any of that are security bugs.

## Reporting a vulnerability

Report privately through [GitHub security advisories](https://github.com/Automaat/zakwas/security/advisories/new). Please don't open a public issue.

Include the zakwas version (`zakwas version`), macOS version, the relevant part of your `zakwas.yaml`, and what an attacker could do. You'll get an answer within a week.

## Supported versions

zakwas is pre-1.0: only the latest release gets fixes.

## What zakwas verifies

See [docs/security.md](docs/security.md): release checksums and build provenance, the pinned mise release and Homebrew installer.
