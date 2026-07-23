<div align="center">

<img src="assets/banner.png" alt="opm — OpenCode Profile Manager" width="720"/>

Switch between completely isolated OpenCode environments with one command.<br>
Different MCPs, agents, models, plugins, and `AGENTS.md` files. No manual config surgery.

```sh
brew install tbcrawford/tap/opm
```

[![License: MIT](https://img.shields.io/badge/License-MIT-000000?style=flat-square)](LICENSE)&nbsp;&nbsp;[![Go](https://img.shields.io/badge/Go_1.21+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)&nbsp;&nbsp;[![Release](https://img.shields.io/github/v/release/tbcrawford/opm?style=flat-square&color=000000)](https://github.com/tbcrawford/opm/releases)

</div>

## Quick Start

Each profile is a full OpenCode config directory. Switching changes what `~/.config/opencode` points to, so OpenCode keeps using the same path it already knows.

That is the whole flow: one command to switch, same path, no config surgery.

<p align="center">
  <img src="assets/demo/readme-quick-start.gif" alt="Terminal demo showing opm init, create work, use work, and list" width="720" />
</p>

---

## Why opm

OpenCode setups tend to drift into roles.

- **Work** needs strict MCPs, specific models, and a locked-down `AGENTS.md`.
- **Personal projects** want different tools, different defaults, and less ceremony.
- **Experiments** should be free to break without touching the setup you actually rely on.

Without profiles, switching contexts means editing files by hand, remembering what you changed last time, and hoping you undo all of it correctly.

`opm` turns each context into a first-class profile: a complete, isolated OpenCode config directory under `~/.config/opm/profiles/` that you can switch to with a single command.

## What a profile isolates

Every profile is its own OpenCode environment.

- MCP configuration
- agents and prompts
- model selection
- plugins and local tweaks
- `AGENTS.md` rules and project-specific behavior

Nothing leaks between profiles unless you explicitly copy it.

## Why it feels good to use

- **Fast to switch**: `opm use <name>` updates the active profile in one step.
- **Safe to experiment**: copy a working profile, try whatever you want, and switch back.
- **Transparent**: OpenCode still reads and writes `~/.config/opencode` like it always has.
- **Low overhead**: no wrapper workflow, no special edit path, no new mental model after setup.

## Per-project sessions with `opm exec`

`opm use` sets the global active profile — everything from that point forward uses it. For project-specific setups where you want a different profile for a single session without touching the global state, use `opm exec`:

```sh
# Start opencode with the "work" profile, regardless of what opm use last set.
opm exec work

# Pass flags through to opencode.
opm exec personal -- opencode --no-auto-update

# Run any command with the profile's config in scope.
opm exec ci -- opencode run "fix the tests"

# One-off session on a different model preset — see "Model presets" below.
# Applied to an ephemeral copy; the real profile is never modified.
opm exec work --preset local
```

`opm exec` sets `XDG_CONFIG_HOME` to a temporary directory containing a symlink to the named profile, then spawns the command. The global active profile — what `opm show` returns — is never touched. When the command exits, the temp directory is cleaned up automatically.

With `--preset`, the temporary directory is a copy-on-write overlay instead of a plain symlink: every profile file is still symlinked except the ones a preset can touch (the oh-my-openagent config, `opencode.json`, agent markdown), which are materialized as real copies so the preset can be applied to them without writing back to the profile or any repo its files are symlinked into.

## Model presets

Profiles switch *environments*. Presets switch *models* — inside whatever profile is active.

If you use [oh-my-openagent](https://github.com/code-yeongyu/oh-my-openagent), every sub-agent and category is pinned to a `provider/model` in `oh-my-openagent.json`. A preset is a named mapping of those assignments — Anthropic for work, an all-local Ollama stack for offline, whatever — stored under `~/.config/opm/presets/` and applied in one command:

```sh
# Snapshot today's hand-built assignments as your first preset.
opm preset capture kimi

# What models can I actually use? Live-queries local servers (Ollama, omlx, ...).
opm preset models

# "Set everything to X" without touching an editor:
opm preset create opus --all anthropic/claude-opus-4-8

# Prefer picking models interactively, agent by agent? A numbered catalog,
# no editor needed:
opm preset edit tiered

# Or set entries one at a time, non-interactively — the same primitive an
# agent framework can call on your behalf from a plain-English instruction:
opm preset set tiered sisyphus kiro/claude-opus-4-7 --variant max
opm preset set tiered explore c:quick
opm preset set tiered legacy --clear

# Forget the exact syntax? A worked walkthrough covering every command above:
opm preset --examples

# Author richer presets by hand (JSON or JSONC, string shorthand supported), then:
opm preset diff local     # dry-run: exactly what would change
opm preset use local      # bulk-swap every agent/category it names
opm preset status         # which preset the live config matches
opm preset revert         # restore the pre-apply backup
```

Prefer your own diff viewer? `--files` renders before/after trees of every file the apply would touch, without writing to the live config:

```sh
opm preset diff local --files /tmp/pd && difft /tmp/pd/before /tmp/pd/after
```

Presets patch surgically: only the model-tuning keys (`model`, `variant`, `fallback_models`, `reasoningEffort`, `thinking`, `temperature`, `top_p`, `maxTokens`) of the agents and categories the preset names are touched. Prompts, permissions, comments, and everything else in the live config survive every switch. Presets support `extends` inheritance, and each apply writes a timestamped backup first.

Before applying, `use` validates every model reference against the profile's `opencode.json` provider block and pings loopback-hosted providers (Ollama, LM Studio, omlx, …) so you never switch onto a model that doesn't exist or a local server that isn't running — override with `--force`. `opm doctor` checks the health of every stored preset.

Presets also cover the other two places models hide: agents defined as markdown (`agent/*.md` frontmatter — the `model:` line is synced for any agent the preset names, everything else untouched) and the top-level `model`/`small_model` fields of `opencode.json` via an optional `"opencode"` block. Every apply backs up all touched files as one set, and `opm preset revert` restores the whole set together.

Presets compose with profiles and projects:

```sh
# One session with the work profile on all-local models — global state untouched,
# real profile files never modified (the preset is applied to an ephemeral copy).
opm exec work --preset local

# Pin this project to a preset via oh-my-openagent's closest-wins config walking.
opm preset use local --project
```

```jsonc
// ~/.config/opm/presets/local.jsonc
{
  "description": "All-local: omlx heavy, ollama quick",
  "categories": {
    "quick": "ollama/llama3.1:8b",
    "deep":  { "model": "omlx/qwen3-coder-30b", "variant": "high" }
  },
  "agents": {
    "sisyphus": { "model": "omlx/qwen3-coder-30b", "variant": "max" }
  }
}
```

---

## Install

### Get it installed in under a minute.

**Homebrew**

```sh
brew install tbcrawford/tap/opm
```

**Go**

```sh
go install github.com/tbcrawford/opm@latest
```

**Prebuilt binary**

Download the latest release from [GitHub Releases](https://github.com/tbcrawford/opm/releases), extract it, and place `opm` in your `$PATH`.

---

## Command Reference

Everything `opm` exposes for day-to-day use, without context trees.

| Command | Description |
|---|---|
| `opm init [--as <name>]` | Migrate your existing config into opm management. Non-destructive. The initial profile is named `default` unless overridden with `--as`. |
| `opm create <name> [--from <name>]` | Create a new empty profile. `--from` clones an existing profile as the starting point instead. |
| `opm use <name>` | Switch the active profile via atomic symlink swap. Reload OpenCode to pick up the new profile. |
| `opm exec <name> [--preset <name>] [--force] [-- command [args...]]` | Run `opencode` (or any command) using the named profile **without** changing the global active profile. `--preset` applies a model preset to an ephemeral copy for just this session — the real profile is never touched; `--force` applies that preset even if validation/probes fail. |
| `opm list [-l]` | List all profiles. Active marked `●`. Dangling marked `✗` and shown as missing. Pass `-l` to include paths. |
| `opm show` | Print the name of the currently active profile. |
| `opm copy <src> <dst>` | Clone a profile to a new name. |
| `opm rename <old> <new>` | Rename a profile. Updates the symlink atomically if active. |
| `opm remove <name> [name...] [-f]` | Remove one or more profiles. `-f`/`--force` also removes the active profile, auto-switching first. |
| `opm path <name>` | Print the absolute path to a profile directory. Useful for scripting. |
| `opm inspect <name>` | Show a profile's active status, path, and full directory contents. |
| `opm doctor` | Run installation health checks (symlink, profiles, presets). Exits with code 1 on failure. |
| `opm reset` | Remove opm management and restore `~/.config/opencode` as a plain directory. |
| `opm preset --examples` | Print a worked walkthrough covering every task below, with real commands. |
| `opm preset list` | List model presets. `●` marks presets matching the live config. Not scoped to a profile — see [Model presets](#model-presets). |
| `opm preset show <name>` | Show a preset's resolved model mapping (after `extends`). |
| `opm preset use <name> [--force] [--project]` | Apply a preset to the live oh-my-openagent config (backs up first). `--force` applies past a failed validation/probe; `--project` writes `./.opencode/` instead of the profile config. |
| `opm preset diff <name> [--project] [--files <dir>]` | Dry-run: show exactly what `use` would change. `--files <dir>` renders before/after trees for external diff tools; `--project` diffs against `./.opencode/` instead. |
| `opm preset capture <name> [--force]` | Snapshot the live model assignments into a new preset. `--force` overwrites an existing preset of the same name. |
| `opm preset create <name> --all <ref> [--force]` | Generate a preset assigning one model to every agent and category. `--force` overwrites an existing preset of the same name. |
| `opm preset edit <name>` | Interactively assign models per agent/category from a numbered catalog. Creates or updates. |
| `opm preset set <name> <entry> <model\|c:category> [--category] [--variant <v>] [--clear]` | Set or clear a single entry, no prompts — scriptable by hand or by an agent. Creates or updates. `--category` targets a category instead of an agent. |
| `opm preset models` | List available models per provider; live-queries loopback servers' `/models`. |
| `opm preset status` | Report which preset the live config matches. |
| `opm preset revert` | Restore every file the last apply touched, as one set, from the most recent backup. |

**Shell completion** — profile names are tab-completed for `use`, `copy`, `rename`, `remove`, `path`, and `inspect`:

```sh
opm completion bash > /etc/bash_completion.d/opm   # bash
opm completion zsh  > "${fpath[1]}/_opm"           # zsh
opm completion fish > ~/.config/fish/completions/opm.fish  # fish
```

---

## Examples

A runnable cookbook covering the full lifecycle — profiles, ephemeral sessions, and
model presets together. Every command here is real; swap in your own profile/preset
names and model refs.

**First-time setup**

```sh
opm init                      # migrate ~/.config/opencode into profile "default"
opm list                      # ● default
opm doctor                    # confirm the managed symlink and presets are healthy
```

**Switching environments (profiles)**

```sh
opm create personal --from default   # clone default as a starting point
opm use personal                     # atomic symlink swap; reload OpenCode after
opm show                             # personal
opm inspect personal                 # active status, path, full directory contents
opm list -l                          # every profile with its path
```

**Per-project sessions without touching global state**

```sh
opm exec work                                     # one-off session on "work", opm show unaffected
opm exec work -- opencode run "fix the tests"      # any command, not just opencode
opm exec work --preset local                       # + a model preset, applied to an ephemeral copy only
```

**Building your first preset from what's already running**

```sh
opm preset capture kimi          # zero authoring — snapshot today's live assignments
opm preset status                # ✓ Live config matches preset kimi
```

**Bulk provider swap — every agent, one model**

```sh
opm preset models                                    # confirm real refs first, never guess
opm preset create opus --all anthropic/claude-opus-4-8
opm preset diff opus                                 # preview before touching anything
opm preset use opus                                  # apply (new opencode sessions only)
```

**Mixed preset, built one entry at a time — the scriptable path**

```sh
opm preset set tiered sisyphus kiro/claude-opus-4-8 --variant max   # top-tier agent
opm preset set tiered explore kiro/claude-haiku-4-5                # cheap/quick agent
opm preset set tiered deep kiro/claude-opus-4-8 --category          # a category tier
opm preset set tiered oracle c:deep                                 # route an agent via that tier
opm preset set tiered legacy --clear                                 # remove a stale entry
```

**Same thing, picked interactively instead of typed**

```sh
opm preset edit tiered   # numbered catalog per agent/category; blank skips, "clear" removes
```

**Review exactly what would change, in your own diff tool**

```sh
opm preset diff tiered --files /tmp/pd && difft /tmp/pd/before /tmp/pd/after
```

**Undo**

```sh
opm preset revert        # restore the last apply's backup set
opm preset use kimi       # or just switch back to a known-good preset
```

**Per-project override, independent of the global preset**

```sh
opm preset use local --project   # writes ./.opencode/, closest-wins over the global config
```

For the full preset command surface with more scenarios, run `opm preset --examples`.

---

## How it works

`opm init` moves your existing OpenCode config into a named profile directory and replaces `~/.config/opencode` with a symlink.

After that, switching profiles is just repointing the managed symlink:

```
~/.config/opencode  →  ~/.config/opm/profiles/work/
```

That means OpenCode, your tools, and your own muscle memory all keep using the same path as before.

```
~/.config/opm/
├── current
└── profiles/
    ├── default/
    ├── work/
    └── experiments/
```

<br>

---

<div align="center">

MIT License · Built with Go · [Report an issue](https://github.com/tbcrawford/opm/issues)

</div>
