# Recipes

Snippets for common setups, one feature each. Copy the piece you need into your own `zakwas.yaml`. The complete sample config repo, with a `Brewfile`, dotfiles and an `agents` section, is [`examples/`](../examples), whose [`zakwas.yaml`](../examples/zakwas.yaml) CI plans and applies.

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


## Coding-agent plugins

Claude Code, Codex and Copilot CLI marketplaces and plugins, with their skills linked into opencode too; `prune` removes user-scope ones you didn't declare (read the plan first). Codex and Copilot read Claude marketplaces too, so one entry serves all of them; `providers` narrows an entry to one agent. `instructions` renders one or more files from the repo into each agent's global instructions (`~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.config/opencode/AGENTS.md`, `~/.copilot/instructions/zakwas.instructions.md`), branching on `{{ .Provider }}` where agents differ.

```yaml
agents:
  providers: [claude, codex, opencode]
  instructions: dotfiles/AGENTS.md
  prune: true
  marketplaces:
    claude-plugins-official: {source: anthropics/claude-plugins-official, providers: [claude]}
    team: example/agent-plugins
  plugins:
    - commit-commands@claude-plugins-official
    - review@team
    - {id: codex-only@team, providers: [codex]}
```

## Agent permissions

One list of shell command permissions for Claude Code, Codex, opencode and, once named, Gemini CLI, so the agents stop asking about the same safe commands. zakwas writes Claude's `settings.json` rules, Codex's `rules/zakwas.rules` and Gemini's `policies/zakwas.toml`; opencode gets them through its config template. Codex runs allowed commands outside its sandbox, so test runners are allowed only for the other two.

```yaml
templates:
  files:
    - {src: dotfiles/opencode/opencode.json.tmpl, dst: ~/.config/opencode/opencode.json}
agents:
  providers: [claude, codex, opencode]
  permissions:
    allow:
      - git status
      - git diff
      - git log
      - {command: go test, providers: [claude, opencode]}
    review:
      - git push
    deny:
      - {command: git push --force, justification: use git push --force-with-lease}
```

`dotfiles/opencode/opencode.json.tmpl`:

```json
{
  "permission": {
    "bash": {{ agentPermissions "opencode" "ask" }}
  }
}
```

## One MCP server for several agents

Declare each MCP server once, as a stdio `command` or an HTTP `url`. zakwas registers it at user scope in Claude Code, Codex and Copilot CLI (and in Gemini CLI once you name it), each in its own format. Secrets stay environment references: you name the variable, and each agent reads it from its own environment, so a secret declared this way reaches neither the config repo nor the agents' files; `env`, `headers`, `args` and `url` are written as they are, so keep tokens out of them.

```yaml
agents:
  providers: [claude, codex, copilot]
  mcp:
    docs:
      command: npx
      args: [-y, "@example/docs-mcp"]
      envVars: [DOCS_TOKEN]
    search:
      url: https://mcp.example.com/mcp
      bearerTokenEnv: SEARCH_TOKEN
      providers: [claude, codex]
```

`docs` is a stdio server that passes `DOCS_TOKEN` through; `search` is an HTTP server that sends `SEARCH_TOKEN` as a bearer token, and only to Claude Code and Codex. Export both variables where the agents start (your shell profile), since an agent reads them when it launches the server. `zakwas plan --diff` shows `${DOCS_TOKEN}` (Codex: `env_vars`), never a value. Servers you added yourself stay, and removing an entry removes only what zakwas registered.

opencode takes its servers from its config template: add `"mcp": {{ agentMCP "opencode" }}` to the `opencode.json` template, as in [Agent permissions](#agent-permissions). A server with `oauth: true` shows the login command in the plan; zakwas never signs in.

## An MCP server behind an OAuth gateway

A gateway without dynamic client registration gives you a client ID registered for a fixed local callback. Declare it once, and zakwas writes the client into each agent's own format. `oauth: true` would only add the login hint to the plan.

```yaml
agents:
  providers: [claude, codex, opencode]
  mcp:
    slack:
      url: https://mcp.example.com/slack
      oauth: {clientId: 0oa1abcdEXAMPLE, callbackPort: 54661, scope: "read write"}
```

`zakwas plan --only agents` shows a `+ claude mcp slack` and a `+ codex mcp slack` line, each ending in the command that signs in; opencode gets its entry through the `agentMCP` template function, so it has no `agents` line; the rendered `opencode.json` shows up under `templates` (run `zakwas plan --only templates,agents`). Claude Code gets what `claude mcp add --transport http slack https://mcp.example.com/slack --client-id 0oa1abcdEXAMPLE --callback-port 54661` writes, plus `scopes`, so a server you already added that way is adopted when it matches. Codex gets `scopes` and an `[mcp_servers.slack.oauth]` table with `client_id` and `callback_port`; opencode gets `"oauth": {"clientId": …, "callbackPort": …, "scope": …}` from `agentMCP "opencode"`. Gemini CLI and Copilot CLI take the same mapping, as listed in [OAuth clients](config.md#oauth-clients).

Register the redirect URIs of the agents you use with the gateway's client: `http://localhost:54661/callback` for Claude Code and `http://localhost:54661/oauth/callback` for Gemini CLI, for example. There is no `clientSecret`: only public clients work, and the secret of a confidential one would have to stay out of the config repo. Sign in once per agent (`/mcp` in Claude Code, `codex mcp login slack`).

## Instructions from several files

`instructions` is one file or a list, joined in order with a blank line between them. Every file is a template, so one list serves agents that differ: branch on `{{ .Provider }}` (`claude`, `codex`, `opencode`, `gemini`, `copilot`).

```yaml
agents:
  providers: [claude, codex]
  instructions:
    - dotfiles/agents/base.md
    - dotfiles/agents/tools.md
```

`dotfiles/agents/base.md`:

```markdown
# Working agreements

- Read the code before changing it.
- Run the tests before saying a change works.
{{ if eq .Provider "claude" }}
- Use the Skill tool for skills.
{{ else }}
- Read a skill's SKILL.md before using it.
{{ end }}
```

Each agent gets its own protected copy: `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, and so on. A file that renders empty for an agent is left out of that agent's copy. Codex reads at most 32 KiB of `AGENTS.md`; a longer copy fails the plan. See [instructions](config.md#instructions) for `@path` imports and the paths of every agent.

## Skills from a path and from git

`skills` links skills that don't come from a plugin marketplace into every managed agent's skills directory: one from the config repo, one from another repository.

```yaml
agents:
  providers: [claude, codex]
  skills:
    - dotfiles/skills/review
    - {git: example/agent-skills, path: skills/pdf, ref: v1}
```

A skill is a directory with a `SKILL.md`, and the directory name must equal the `name` in that file. `plan` never fetches: a git skill shows as a step that `apply` runs, which clones the repository (`--depth 1`) and links the skill. `zakwas upgrade` pulls the clone, and the link follows. A directory or link at the target that zakwas didn't make, such as one from `npx skills`, fails the plan instead of being replaced.

## Profiles: read-only review, build without network

A profile is a named set of permissions you start a session with. Declare it once; zakwas renders a Claude Code subagent, a Codex profile, an opencode agent, and so on.

```yaml
agents:
  providers: [claude, codex]
  profiles:
    review:
      description: Read-only change review
      prompt: dotfiles/agents/review.md
      edit: deny
      shell: ask
    build:
      description: Build and test without network
      edit: allow
      sandbox: workspace-write
      network: false
```

Start them with `claude --agent review`, `codex --profile review`, `opencode --agent review`, `copilot --agent review`, or `@review` in Gemini CLI. `review` removes the edit tools in Claude Code and runs Codex in its read-only sandbox, and it denies every MCP server declared under `agents.mcp` that it doesn't name. `build` can edit, and turns network access off where the agent can: Codex's workspace sandbox and Claude Code's web tools. Claude Code can't stop a shell command from using the network and has no `sandbox`, so the plan lists `claude profile build can't express: sandbox; network: false for shell commands` instead of promising it. Every field a provider can't express is listed that way, and none of them makes `check` fail. The mapping per field is in [profiles](config.md#profiles).

## Guard: keep agents from applying the config

With an `agents` section the guard is on, with nothing to configure. zakwas denies the managed agents `zakwas apply`, `upgrade`, `self-update`, `approve` and `mcp`, denies Claude Code edits to the config repo and zakwas state, and refuses `apply` started from an agent session. Run `apply` in a terminal outside the agent. Agents keep `plan`, `check`, `capture` and `propose`.

To turn it off:

```yaml
agents:
  guard: false
```

The next `apply` removes the rules zakwas added, and only those. Turn it off only if you want the agents to apply the config themselves. What the guard covers per agent, and what it can't stop, is in [security.md](security.md#coding-agents).

## Adopt an existing setup

A Mac that already has plugins, MCP servers and approved commands: let zakwas list them, review, and approve them into the config.

```
$ zakwas capture
claude
  marketplace  team  example/agent-plugins
  plugin       review@team
  mcp server   docs  stdio npx -y @example/docs-mcp
  allow rule   git status

Save these as one proposal with: zakwas capture --write
$ zakwas capture --write
Proposal d82fd6f6: add marketplace team; add plugin review@team; add MCP server docs; allow "git status" (high risk)
  ...
$ zakwas approve d82fd6f6
  ...
Approve d82fd6f6: apply 2 step(s) and commit zakwas.yaml to main? [y/N]
```

`capture` lists what Claude Code and Codex have that `zakwas.yaml` doesn't declare, and under "Not captured" what it left out because it may hold a credential. `--write` saves the findings as one proposal and changes nothing. `approve` shows the diff and plan, asks, applies and commits to the current branch of the config repo; it needs a terminal. The `zakwas plan` after it shows nothing left to change, and a second `capture` lists nothing. An agent can run `capture` and `propose` too, through [`zakwas mcp`](commands.md#mcp), but only you run `approve`. See [Agents](agents.md#day-to-day).

## Opt in to Gemini CLI and Copilot CLI

Codex, Gemini CLI and Copilot CLI stay untouched until the config names them. Naming one also gives it every entry that has no `providers` of its own, so one declaration covers all of them.

```yaml
agents:
  providers: [claude, gemini, copilot]
  instructions: dotfiles/agents/AGENTS.md
  mcp:
    docs: {command: npx, args: [-y, "@example/docs-mcp"], envVars: [DOCS_TOKEN]}
  skills:
    - dotfiles/skills/review
```

- Gemini CLI needs `gemini` on `PATH`: zakwas renders one extension under `~/.gemini/zakwas` (instructions, MCP servers, hooks, subagents) and links it with `gemini extensions link`, plus a policy file for permissions. It never writes `~/.gemini/settings.json` or `GEMINI.md`. Gemini has no plugin marketplaces, so `plugins` never reach it.
- Copilot CLI needs no CLI for these keys, only for plugins and marketplaces. It gets `instructions/zakwas.instructions.md`, `mcp-config.json` servers, profile files in `~/.copilot`, and skills in `~/.agents/skills` (`$COPILOT_HOME/skills` once that variable is set), and never `config.json`. It has no user-level shell rules, so `permissions` that reach it are reported in the plan as unsupported, and the guard covers it only by the refusal.
- `GEMINI_CLI_HOME` and `COPILOT_HOME` move their directories, and zakwas follows them.

The [provider table](agents.md#providers) lists the rest.

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

A launchd agent that runs `zakwas check` every weekday morning and notifies you when the Mac drifted. launchd starts agents from `/` with a minimal `PATH`, so the job sets `env.PATH` and the script sets the config location.

```yaml
files:
  - {src: dotfiles/bin/zakwas-check-notify, dst: ~/.local/bin/zakwas-check-notify}
launchd:
  - label: dev.example.zakwas-check
    program: [~/.local/bin/zakwas-check-notify]
    schedule: {at: "10:00", weekdays: [mon, tue, wed, thu, fri]}
    env: {PATH: "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"}
    log: ~/Library/Logs/zakwas-check.log
```

`dotfiles/bin/zakwas-check-notify` (adjust `ZAKWAS_CONFIG` to your repo; drop `mise exec --` if zakwas comes from Homebrew or `~/.local/bin`):

```sh
#!/bin/sh
PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
ZAKWAS_CONFIG="$HOME/dotfiles/zakwas.yaml"
export PATH ZAKWAS_CONFIG

status=0
mise exec -- zakwas check > /dev/null 2>&1 || status=$?
case $status in
    0) exit 0 ;;
    2) message="This Mac drifted from zakwas.yaml. Run: zakwas plan" ;;
    *) message="zakwas check failed (exit $status). Run: zakwas check" ;;
esac
osascript -e "display notification \"$message\" with title \"zakwas\""
```

`zakwas apply` writes `~/Library/LaunchAgents/dev.example.zakwas-check.plist` and loads it. Editing the schedule reloads the job on the next apply, and deleting the `launchd` entry unloads it and removes the plist. See [`launchd`](config.md#launchd) for every key.

## CI: review before apply

<!-- recipe: skip -->
```yaml
- run: zakwas plan -out plan.json --diff
- run: zakwas plan --json | jq '.summary'
# after review, on the same machine:
- run: zakwas apply -plan plan.json
```
