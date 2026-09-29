# Examples

Complete, working config: [`examples/`](../examples). A real-world one: [Automaat/environment-as-code](https://github.com/Automaat/environment-as-code) (~65 CLI tools, casks, dotfiles, macOS defaults, a daily drift check).

## Minimal: dotfiles only

No Homebrew, no mise; zakwas only installs protected copies.

```yaml
protect:
  immutable: true
files:
  - {src: dotfiles/zshrc, dst: ~/.zshrc}
  - {src: dotfiles/git, dst: ~/.config/git}   # a directory copies every file in it
```

## Apps and tools

```yaml
brew:
  file: Brewfile
  cleanup: zap        # uninstall anything not in the Brewfile (read the plan first)
  upgrade: true       # treat outdated casks/formulae as drift
mise:
  config: dotfiles/mise/config.toml
  prune: true
files:
  - {src: dotfiles/mise/config.toml, dst: ~/.config/mise/config.toml}
```

Pin exact tool versions in `dotfiles/mise/config.toml` and let Renovate bump them; a CI job that runs `zakwas apply -y --only files,mise` on a macOS runner gates every bump.

## Per-machine values

Templates get `.Home` and your `vars`:

```yaml
templates:
  vars:
    email: you@example.com
  files:
    - {src: dotfiles/git/user.tmpl, dst: ~/.config/git/user, mode: 0o600}
```

```ini
# dotfiles/git/user.tmpl
[user]
	email = {{.Vars.email}}
	signingkey = {{.Home}}/.ssh/id_ed25519.pub
```

## Configs an app rewrites

Protected copies are read-only; for files an app must write (IDE settings, `~/.ssh/config` under MDM that resets permissions), use a plain symlink:

```yaml
links:
  - {src: dotfiles/ssh/config, dst: ~/.ssh/config}
```

## macOS preferences

```yaml
defaults:
  - {domain: com.apple.dock, key: autohide, value: true}
  - {domain: com.apple.dock, key: show-recents, value: false}
  - {domain: NSGlobalDomain, key: KeyRepeat, value: 2}
  - {domain: NSGlobalDomain, key: InitialKeyRepeat, value: 15}
  - {domain: com.apple.screencapture, key: location, value: ~/Documents/screenshots}
  - {domain: com.apple.HIToolbox, key: AppleFnUsageType, value: 0, currentHost: true}
```

Dock, Finder and SystemUIServer (screenshots) restart automatically when one of their values changes; for other apps set `restart: <process name>` on the entry. Changed `NSGlobalDomain` values are applied to the running session without a re-login. Find a key: `defaults read > a`, toggle the setting, `defaults read > b`, `diff a b`.

## One-off steps

`run` executes only while `check` fails; both run in `$HOME`.

```yaml
commands:
  - name: rosetta
    check: /usr/bin/pgrep -q oahd
    run: softwareupdate --install-rosetta --agree-to-license
  - name: gcloud auth plugin
    check: test -x /opt/homebrew/share/google-cloud-sdk/bin/gke-gcloud-auth-plugin
    run: gcloud components install gke-gcloud-auth-plugin --quiet
```

Commands stop at the first failure, so put ones that need manual action (importing a GPG key) last.

## Daily drift notification

A launchd agent that runs `zakwas check` and notifies on drift:

```yaml
templates:
  files:
    - {src: dotfiles/launchd/zakwas-check.plist.tmpl, dst: ~/Library/LaunchAgents/dev.zakwas.check.plist}
commands:
  - name: drift check agent
    check: launchctl print "gui/$(id -u)/dev.zakwas.check" > /dev/null 2>&1
    run: launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/dev.zakwas.check.plist"
```

The plist runs a script like:

```sh
#!/bin/sh
zakwas check > /dev/null 2>&1
case $? in
    0) exit 0 ;;
    2) osascript -e 'display notification "Run: zakwas plan" with title "Mac drifted from zakwas.yaml"' ;;
    *) osascript -e 'display notification "Run: zakwas check" with title "zakwas check failed"' ;;
esac
```

## CI: review before apply

```yaml
- run: zakwas plan -out plan.json --diff
- run: zakwas plan --json | jq '.summary'
# after review, on the same machine:
- run: zakwas apply -plan plan.json
```
