---
name: zakwas
description: Use when the user wants to add or remove an agent plugin, permission rule, MCP server, Homebrew package or mise tool pin, or when you are about to edit agent config (~/.claude, ~/.codex, ~/.config/opencode, ~/.agents, ~/.gemini, ~/.copilot), zakwas.yaml, a Brewfile or a mise config. zakwas manages these files; change them by proposing, never by editing.
---

# zakwas

zakwas converges this machine (macOS or Linux) to a `zakwas.yaml` in a config repo. It owns agent settings, plugins, permissions, MCP servers, hooks, Brewfile entries and mise pins. A hand edit to a managed file is reverted by the next `zakwas apply`, and agents are denied `apply`. Propose the change instead; the user approves it.

## Never

- Edit agent config: `~/.claude/settings.json`, `~/.claude.json`, `~/.codex/config.toml`, `~/.codex/rules/`, `~/.config/opencode/opencode.json`, `~/.agents/`, `~/.gemini/`, `~/.copilot/`.
- Edit `zakwas.yaml`, the Brewfile or the mise config in the config repo.
- Run `zakwas apply`, `upgrade`, `approve`, `self-update`, `mcp` or `layer`. The guard denies them, and a workaround (`sh -c`, a script, another tool) defeats the point of the approval.
- Run `claude plugin install`, `codex plugin add`, `brew install`, `mise use` or similar to get the same result outside zakwas.

## Propose a change

1. Read the current state: `zakwas_config` (its `layers` lists the registered local layers) and `zakwas_plan`, or `zakwas plan` in a shell. Skip an operation the config already covers.
2. Save related changes as one proposal: call `zakwas_propose` with `{"ops": [operation, ...]}`, or run `zakwas propose -json batch operations.json` (a JSON array; `-` reads stdin). Each target may be edited only once; order a new marketplace before its plugins. The whole batch gets one diff, plan and risk tier, and any high-risk operation requires terminal approval. Single changes retain their existing form. Prefer the MCP tool `zakwas_propose`; without the zakwas MCP server, run `zakwas propose -json OPERATION ...` in a shell. Proposing changes nothing on disk except the saved proposal.
3. Report the proposal's `id`, `tier`, `summary` and `reasons` to the user, then follow the tier below.
4. Remove a proposal the user no longer wants with `zakwas_discard`, or `zakwas proposals` to list them.
5. To record what the user installed or approved in an agent by hand, call `zakwas_capture` (or `zakwas capture` in a shell) and pass the `ops` it returns as `{"ops": [...]}` to `zakwas_propose` for one proposal; the user can also run `zakwas capture --write` for one proposal. Never copy a value the scan skipped as a credential.
6. Say "proposed", not "installed", until the user has applied it. Confirm afterwards with `zakwas_check`.

| Operation | MCP `kind` | Shell |
|---|---|---|
| add a plugin marketplace | `add-marketplace`, `name`, `source` | `zakwas propose add-marketplace NAME SOURCE` |
| add an agent plugin | `add-plugin`, `name: NAME@MARKETPLACE` | `zakwas propose add-plugin NAME@MARKETPLACE` |
| add a permission rule | `add-permission`, `decision`, `command` | `zakwas propose add-permission allow\|review\|deny COMMAND...` |
| remove a permission rule | `remove-permission`, `decision`, `command` | `zakwas propose remove-permission allow\|review\|deny COMMAND...` |
| add a Homebrew package | `add-brew`, `name`, `cask` | `zakwas propose add-brew NAME [--cask]` |
| pin a mise tool | `pin-mise`, `name`, `version` | `zakwas propose pin-mise TOOL VERSION` |
| add an MCP server | `add-mcp`, `name`, `server` | `zakwas propose add-mcp NAME --command CMD [--arg A] \| --url URL [--oauth-client-id ID [--oauth-callback-port PORT] [--oauth-scope SCOPES]]` |

An `add-mcp` server with a pre-registered OAuth client takes `server.oauth` with `client_id` and optionally `callback_port` and `scope`. Never pass a client secret; there is no field for one.

## Layers

A machine may have local layers: extra config repos (a work repo next to a private one) merged into the config. Propose into the right repo:

- Pass `layer` (MCP) or `--layer NAME` (shell, before the operation) to send an addition to that layer's `zakwas.yaml`, to the Brewfile it sets with `brew.file` or to the mise config it sets with `mise.config`. Without it an addition goes to the main config's repo. In `{"ops": [...]}`, a top-level `layer` is the default for the operations that name none.
- Ask which repo the user means when the entry could live in either, such as a work-only plugin or tool: a layer name never comes from your guess.
- A removal or a version bump needs no `layer`: it goes to the file that holds the entry. Naming a different layer than the one that holds it is refused.
- One proposal edits one repo: the main config's or a single layer's. Propose the halves of a mixed change separately.
- The proposal's `layer` field shows where it goes, and `zakwas approve ID` commits there. A layer name that isn't in `zakwas_config` fails with a one-line error: report it, do not register the layer (`zakwas layer` is denied to you).
- A main-config plugin cannot come from a marketplace only a layer declares: add the plugin to that layer.

`providers` limits an entry to some of `claude`, `codex`, `opencode`, `gemini` and `copilot`; leave it out unless the user names one. In the shell form, put every flag of zakwas (`-json`, `-c PATH`, `--providers`) before `--`, and every dash-prefixed word of a command after it: `zakwas propose -json add-permission review -- git log --oneline`.

## Tiers

- `low`: the plan touches nothing sensitive. If the MCP server offers `zakwas_apply`, call it with the id; zakwas shows the user a form with the diff and plan, and only a yes applies it. Without that tool, give the user the command below.
- `high`: only a person at a terminal applies it. Give the user this command and stop; never call `zakwas_apply` for it and never retry with a different operation to get a lower tier:

  ```
  zakwas approve ID
  ```

- When the proposal result has a `next` field, relay its command to the user as written.
- Add a plugin only from a marketplace already in `zakwas.yaml`; `zakwas_config` shows it. `allow` rules, casks, tap formulae, mise backend tools, marketplaces and MCP servers are always `high`.

## When a proposal is refused

- Marketplace not declared, flow-style YAML (`plugins: [a@m]`), a duplicate entry or an invalid result: tell the user the exact reason and the edit they would need to make in `zakwas.yaml`. Do not make it.
- Layer not registered, a layer without `brew.file` or `mise.config` for a package or tool, or an entry held by another layer: tell the user the exact reason; registering a layer or setting its file is their edit.
- No operation covers the request (a hook, a profile): describe the `zakwas.yaml` change and let the user make it.
- A proposal older than 7 days expires; propose again.
