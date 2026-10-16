# Coding agents

Claude Code, Codex, opencode, Gemini CLI and Copilot CLI each keep their setup in their own files and formats. You declare the setup once in `zakwas.yaml` and zakwas renders each agent's native format, so a new Mac, or a second agent, starts with the same MCP servers, instructions, skills and permissions as the first.

What you can declare under `agents`:

| Key | What it declares |
|---|---|
| `marketplaces`, `plugins` | plugins and the marketplaces they come from |
| `mcp` | MCP servers, as a stdio `command` or an HTTP `url`, with secrets as environment references and, for an HTTP server, an optional pre-registered OAuth client |
| `instructions` | global instructions, from one or more files, rendered per agent |
| `skills` | standalone skills from a path or a git repository |
| `profiles` | named permission profiles such as "read-only review" |
| `permissions` | shell commands agents may run, must ask about, or never run |
| `hooks` | command hooks shared by every agent |
| `guard` | keeps agents from applying the config themselves; on by default |

Every key is documented in [config.md](config.md#agents). This page is the tour: what each agent supports, and how a change moves from "I did this by hand in an agent" to the config repo.

## Providers

`claude`, `codex`, `opencode`, `gemini` and `copilot` are the providers. Claude Code and opencode are converged by default. Codex, Gemini CLI and Copilot CLI are opt-in: zakwas leaves them alone until `zakwas.yaml` names them in `agents.providers` or in an entry's `providers`, so a config written before they existed keeps its meaning.

```yaml
agents:
  providers: [claude, codex, gemini, copilot]
```

Each provider plans on its own. When one fails, for example because its CLI is missing, the plan reports the error and the other providers still apply.

| | Claude Code | Codex | opencode | Gemini CLI | Copilot CLI |
|---|---|---|---|---|---|
| Converged | by default | opt-in | by default, best effort | opt-in | opt-in |
| CLI on `PATH` | for plugins | for plugins | by default, for anything; when named, for plugins and hooks | always | for plugins |
| Plugins, marketplaces | yes | yes | no plugin system: the skills of declared plugins are linked | no | yes |
| MCP servers | `.claude.json` | `config.toml` | `agentMCP` in an `opencode.json` template | extension manifest | `mcp-config.json` |
| Instructions | `CLAUDE.md` | `AGENTS.md` (32 KiB limit) | `AGENTS.md` | extension context file | `instructions/zakwas.instructions.md` |
| Skills | `~/.claude/skills` | `~/.agents/skills` | `~/.agents/skills` | `~/.agents/skills` | `~/.agents/skills`, or `$COPILOT_HOME/skills` when that is set |
| Command hooks | `settings.json` | `hooks.json` | generated plugin | extension `hooks.json` | `hooks/zakwas.json` |
| Permissions | `settings.json` rules; `review` is not written | `rules/zakwas.rules` | `agentPermissions` in the template | `policies/zakwas.toml` | unsupported, reported in the plan |
| Profiles | subagent file, `claude --agent` | profile file, `codex --profile` | agent file, `opencode --agent` | subagent, `@name` | agent file, `copilot --agent` |
| Guard | deny rules, edit rules, refusal | `forbidden` rules, refusal | deny patterns through the template, refusal | deny rules, refusal | refusal only |
| `capture` | yes | yes | no | no | no |

Notes on the cells:

- "Best effort" means that opencode is written only once its CLI is on `PATH`, and a blocked write is skipped instead of failing the plan. Name `opencode` in `providers` to make it strict.
- opencode has no user-level file zakwas can edit for MCP servers, permissions and the guard, because `opencode.json` is usually a template. Render them into it with the `agentMCP` and `agentPermissions` template functions; the plan warns when the guard has nothing to write into.
- Gemini CLI and Copilot CLI rewrite their own `settings.json` or `config.json`, so zakwas never writes them. For Gemini it renders one extension, `~/.gemini/zakwas`, links it, and writes one policy file. Copilot only gets files it reads from a directory of its own.
- An HTTP server can name a pre-registered OAuth client (`oauth: {clientId, callbackPort, scope}`); every provider takes all three keys, each in its own spelling. See [OAuth clients](config.md#oauth-clients).
- A profile field a provider cannot express is listed in the plan as a warning, and `check` stays clean. See the [field mapping](config.md#profiles).

## Start small

Instructions, one MCP server and one skill are enough to see the model. The full version of this snippet, with a profile and the rest of a config repo around it, is [examples/zakwas.yaml](../examples/zakwas.yaml). Everything below needs no agent CLI installed:

```yaml
agents:
  providers: [claude, codex]
  instructions: dotfiles/agents/AGENTS.md
  mcp:
    docs: {command: npx, args: [-y, "@example/docs-mcp"], envVars: [DOCS_TOKEN]}
  skills:
    - dotfiles/agents/skills/review
```

`zakwas plan` lists what each agent gets. `zakwas apply` writes it and records what it wrote, so later removing an entry removes only what zakwas added, never what you set yourself. An MCP server that already exists and differs from the declaration fails the plan instead of being replaced, and a file zakwas did not write is backed up to `<file>.zakwas-bak` first. [recipes.md](recipes.md) has a tested recipe for each key.

## Day to day

Agents change their own setup all the time: you approve a command, install a plugin, add an MCP server. With `prune` the next `apply` removes an undeclared plugin, and without it the next Mac still never gets it. The loop that keeps the config repo the source of truth:

1. Capture what changed by hand.
2. Propose a change, from the CLI or from an agent.
3. Approve it in a terminal.
4. Apply and let the drift check watch.

### Capture what you changed by hand

```bash
zakwas capture            # list what the agents have that zakwas.yaml does not declare
zakwas capture --write    # save the findings as one proposal
```

`capture` reads Claude Code and Codex (Codex only when `zakwas.yaml` names it) and lists plugins, marketplaces, MCP servers, Claude `allow` rules and Codex approvals that `zakwas.yaml` does not declare. It changes nothing. Anything that may hold a credential, such as a token in an argument or a URL with a query, is listed under "Not captured" and left out. With `--write` the findings become one proposal. Read its diff before approving: what passes the screening is copied as it is. See [capture](commands.md#capture).

### Propose a change

A proposal is a typed edit to `zakwas.yaml`, the Brewfile or the mise config, saved with its diff, plan and risk tier. Saving one changes neither the repo nor the machine.

From a terminal:

```bash
zakwas propose add-plugin review@team
zakwas propose add-permission review git rebase
zakwas propose add-mcp docs --command npx --arg -y --arg @example/docs-mcp --env-var DOCS_TOKEN
zakwas proposals
```

From an agent, over MCP. Register the server once, and the agent gets read-only tools for the config, the plan, the drift check and capture, plus `zakwas_propose` (and `zakwas_proposals` and `zakwas_discard` to list and delete pending proposals):

```yaml
agents:
  mcp:
    zakwas: {command: zakwas, args: [mcp, -c, /Users/you/dotfiles/zakwas.yaml]}
```

Pass `-c` with an absolute path (zakwas does not expand `~` there, and the agent starts the server in whatever directory it is in). With the `zakwas` plugin installed (`agents.marketplaces: {zakwas: Automaat/zakwas}`, `agents.plugins: [zakwas@zakwas]`), the agent also knows to propose changes instead of editing agent config or `zakwas.yaml`. See [mcp](commands.md#mcp) and [teach agents the flow](commands.md#teach-agents-the-flow).

The agent cannot apply anything through this. The [guard](config.md#guard) denies it `zakwas apply`, `upgrade`, `self-update`, `approve` and `mcp`.

### Approve in a terminal

```bash
zakwas approve 3f9a1c2b
```

`approve` shows the diff and plan again, computed from the current files, asks, applies only the modules the proposal affects, and commits the edited files to the current branch of the config repo. It never pushes. It needs a terminal on stdin and has no `-y`. A proposal expires after 7 days, and `approve` refuses one whose files changed since.

Every proposal has a tier. Permission rules are rated high in practice, since writing them changes agent config files. A low-risk one (a plugin from a declared marketplace, a core Homebrew formula, a mise pin) can optionally be applied from inside the agent: start the server as `zakwas mcp --enable-apply` and the agent gets `zakwas_apply`, which applies only after you answer yes to a diff and plan that zakwas renders in the agent. A high-risk one (an `allow` rule, an MCP server, a marketplace, a cask, or anything whose plan touches agent config, shell rc files, `~/.ssh` or `/etc`) always goes to `zakwas approve`. Why the split, and what `--enable-apply` leaves open, is in [security.md](security.md#proposals-and-who-may-approve).

### Apply, then keep checking

```bash
zakwas plan
zakwas apply
```

Run `apply` in a terminal outside the agent: with the guard on, zakwas refuses to apply from an agent session.

To learn about drift without waiting for the next `apply`, run `zakwas check` from launchd (see the [daily drift check](recipes.md#daily-drift-notification)), and add a session-start hook so each agent session tells the agent when the setup is stale:

```bash
zakwas check --offline -c ~/dotfiles/zakwas.yaml
```

`--offline` compares only the files zakwas owns (`files`, `links`, `templates`) with what it recorded, starts no command, and takes about 30 ms. It does not look at the `agents` module, so drift in agent settings, plugins or skills shows only in a full `check`. The hook script and its registration are in [commands.md](commands.md#report-drift-when-an-agent-session-starts).

## Where to go next

- [recipes.md](recipes.md): tested recipes for MCP servers, instructions, skills, profiles, the guard, adopting an existing setup and opting in to Gemini and Copilot.
- [config.md](config.md#agents): every key, per-provider paths and ownership rules.
- [commands.md](commands.md#propose-proposals-approve): `propose`, `approve`, `capture` and `mcp` in full.
- [security.md](security.md#coding-agents): what the guard stops, and what it does not.
