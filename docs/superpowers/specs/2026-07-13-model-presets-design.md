# Model Presets — Design

**Date:** 2026-07-13
**Status:** Approved for phase 1 implementation

## Problem

opm switches whole OpenCode environments (profile = full config directory), but it has no
answer for the *other* kind of switching users actually do daily: re-pointing every
oh-my-openagent sub-agent and category at a different provider's models — Anthropic today,
a local Ollama/omlx stack tonight, Kimi tomorrow. Doing that with opm profiles would mean
duplicating the entire config directory per model-set and hand-propagating every unrelated
change across the copies. Doing it by hand means editing `oh-my-openagent.json` per agent
and hoping nothing else gets lost.

Previous attempts in this ecosystem (omc-tui, the omc-profile-switcher skill) failed the
same way: they kept model profiles in a separate store and **regenerated** the live config
file wholesale, destroying prompt/permission overrides they didn't model.

## Solution: a second, orthogonal switching layer

A **preset** is a named model-mapping overlay applied *onto* the active profile's
oh-my-openagent config. Profiles stay the coarse environment layer; presets are the fine
model layer. The two compose: any preset can be applied inside any profile.

The cardinal rule that fixes what previous attempts got wrong:

> **Apply patches surgically; never regenerate.** A preset owns only the *model-tuning
> keys* of the agents/categories it names. Everything else in the live file — prompts,
> permissions, team_mode, disabled_*, comments, formatting — is preserved byte-for-byte.

### Tuning keys (the only keys a preset may set, and the only keys apply may touch)

`model`, `category` (agents only — routes to a model tier instead of a direct model),
`variant`, `fallback_models`, `reasoningEffort`, `thinking`, `temperature`, `top_p`,
`maxTokens`, `textVerbosity`, `providerOptions`

The set mirrors the model-tuning surface of upstream's `oh-my-opencode.schema.json`
(v4.17.x). Every entry must route somewhere: a `model` in provider/model form, or (for
agents) a `category`.

Within an entry the preset is **authoritative**: tuning keys the preset entry does not
specify are *removed* from the live entry (no stale `variant: max` left behind on a model
that doesn't support it). Entries (agents/categories) the preset does not name are left
completely untouched.

## Preset file format

Location: `~/.config/opm/presets/<name>.json` (loading also accepts `.jsonc`; both parsed
with hujson, so comments and trailing commas are fine). Preset names use the same
validation as profile names (`store.ValidateName`).

```jsonc
// ~/.config/opm/presets/local.jsonc
{
  "description": "All-local: omlx heavy, ollama quick",
  "extends": "base",                       // optional single inheritance
  "categories": {
    "quick": "ollama/llama3.1:8b",         // string shorthand = { "model": ... }
    "deep":  { "model": "omlx/qwen3-coder-30b", "variant": "high" }
  },
  "agents": {
    "sisyphus": { "model": "omlx/qwen3-coder-30b", "variant": "max" },
    "explore":  "ollama/qwen2.5-coder:14b"
  }
}
```

Rules:
- Entry values: string shorthand (`"provider/model"`) or object restricted to tuning keys.
  Unknown keys in an entry are an **error** — presets must not smuggle prompt/permission
  content.
- `model` values must contain `/` (the `provider/model` namespace format).
- `extends`: single inheritance chain, cycle-detected. Child overrides at **entry level**
  (a child's entry for an agent/category replaces the parent's entry entirely — an entry
  is an atomic tuning unit, never key-merged across the chain).

## Live-config resolution

The target file lives in the opencode config dir (in production, `~/.config/opencode`,
i.e. inside the active profile). oh-my-openagent reads, in priority order (canonical name
wins over legacy, `.jsonc` preferred within a name — verified against the plugin's
`detectPluginConfigFile`/`detectConfigFile` source, v4.7.5):

1. `oh-my-openagent.jsonc`
2. `oh-my-openagent.json`
3. `oh-my-opencode.jsonc`
4. `oh-my-opencode.json`

The plugin itself logs "remove the legacy file to avoid confusion" when both names exist,
and auto-migrates a lone legacy file to the canonical name (renaming the original to
`.bak`) — opm's shadow warning mirrors that guidance.

`opm preset use` patches **the winning file** so the change is what oh-my-openagent
actually loads, and warns when more than one candidate exists. When none exists, apply
creates `oh-my-openagent.json` with the framework's `$schema` header.

**Symlink preservation:** if the winning file is a symlink (dotfiles setups pointing into
a git repo), apply resolves it and writes through to the final target via temp-file +
rename *in the target's directory* — the symlink itself is never replaced.

## Apply mechanics

1. Read winning file; `hujson.Parse`.
2. A standardized clone is unmarshalled to compute current state.
3. Build an RFC 6902 JSON Patch containing only add/replace/remove ops on
   `/agents/<name>/<key>` and `/categories/<name>/<key>` paths (whole-entry add when the
   entry doesn't exist yet; JSON Pointer escaping applied).
4. `Value.Patch(...)` — hujson applies the patch while preserving comments/formatting of
   untouched regions.
5. Timestamped backup of the original to `~/.config/opm/backups/presets/`, then atomic
   write (temp + rename, symlink-aware as above).

`opm preset revert` restores the most recent backup.

## Command surface

New `preset` command group (help group "Model presets"). These commands do **not** require
opm-managed state (`managedGuard`) — they operate on whatever `~/.config/opencode`
resolves to, so they work pre-`opm init` too.

| Command | Behavior |
|---|---|
| `opm preset list` | All presets; ● marks preset(s) matching the live config |
| `opm preset show <name>` | Resolved mapping after `extends` |
| `opm preset use <name>` | Backup + surgical apply to the live config |
| `opm preset diff <name>` | Dry-run: exact field-level changes `use` would make. `--files <dir>` additionally renders before/after trees of every file the apply would touch (same patching code paths, zero writes) for external diff tools like difftastic or Hunk |
| `opm preset capture <name>` | Snapshot live tuning keys into a new preset (`--force` to overwrite) |
| `opm preset create <name> --all <ref>` | Generate a preset assigning one model to every agent/category entry in the live config — the "set everything to X" one-liner |
| `opm preset models` | Model discovery: declared models per provider from `opencode.json`, plus a live `/models` query of loopback-hosted providers so the listing reflects what local servers actually serve right now (marks declared-but-not-served and served-but-not-declared) |
| `opm preset status` | Which preset matches the live config, or "no preset matches" |
| `opm preset revert` | Restore the most recent pre-apply backup |

`capture` is the migration path: run it once and today's hand-built config becomes the
first preset, zero authoring.

## Phase 2: validation & doctor (implemented)

`opm preset use` validates before applying; failures block unless `--force`:

- **Model-reference validation** against the profile's `opencode.json[c]` provider block
  (primary models and `fallback_models`): provider declared with an explicit `models` map
  and the model absent → **fail**; provider not declared at all → **warn** (OpenCode knows
  many providers natively); provider declared without a models map → OK (discovery); no
  `opencode.json` → warn and skip.
- **Endpoint probes** for loopback-hosted providers (`localhost`, `127.x`, `::1`) the
  preset references: `GET <baseURL>/models`, 2s timeout. Any HTTP response counts as
  alive; connection failure → **fail**. Non-loopback providers are never probed.

`opm doctor` gains a **Presets** section: every stored preset must parse, resolve its
extends chain, and pass reference validation; shadowed live-config candidates are warned
about. Doctor does not probe endpoints (no network in doctor); probes run on `use`.

## Phase 3: markdown agents & opencode.json (implemented)

Presets now own two more surfaces, diffed/applied/reverted together with the
oh-my-openagent config:

- **Markdown agent sync** — OpenCode agents defined as markdown with YAML frontmatter
  (`<config>/agent/*.md` or `agents/*.md`, both forms supported) shadow same-named
  plugin agents, so they must stay in sync. For every preset `agents` entry whose name
  has a markdown definition in the profile, apply rewrites only the frontmatter
  `model:` line to the entry's model — but only when the file already pins one. A
  markdown agent without a model line inherits the session default by design; presets
  respect that and never force-pin it (which also keeps capture → status round-trips
  clean). Description, mode, permissions, prompt body — untouched. Markdown files that
  are symlinks are written through, not replaced. Files without frontmatter are skipped.
- **`opencode` block** — a preset may set top-level `opencode.json` fields:
  `{ "opencode": { "model": "p/m", "small_model": "p/m" } }`. Unlike entries, this
  block only sets the keys it names (opm does not own the rest of opencode.json);
  patching is hujson-surgical so provider declarations and comments survive.
  `capture` snapshots these fields when present; refs are included in validation.

**Backups are now revertable sets**: each apply that changes anything creates
`~/.config/opm/backups/presets/<stamp>/` containing every pre-change file plus a
`manifest.json` mapping copies to their absolute targets. `opm preset revert` restores
the newest set as a unit — live config, markdown agents, and opencode.json together.

## Phase 4: scoped application (implemented)

- **`opm exec <profile> --preset <name>`** — ephemeral sessions with a preset applied and
  zero global mutation. Instead of symlinking the profile directly, exec builds a
  copy-on-write overlay: every profile entry is symlinked except the files a preset owns
  (the four oh-my-openagent candidates, `opencode.json[c]`, and the `agent`/`agents`
  dirs), which are materialized as symlink-resolved real copies. The preset is applied to
  the overlay; backups land inside the ephemeral temp dir and vanish with it. The real
  profile — and any dotfiles repos its symlinks point into — is never modified.
  Validation (`--force` to override) runs against the overlay's real config copies.
- **`opm preset use <name> --project`** (and `diff --project`) — writes/patches
  `./.opencode/oh-my-openagent.json` in the current project instead of the profile
  config, exploiting oh-my-openagent's closest-wins config walking: the project override
  beats the global config for that project only. Reference validation still runs against
  the profile's `opencode.json` (provider declarations are global). The preset's
  `opencode` block is skipped in project mode with a warning.

## Roadmap

All four phases are implemented. Possible future work: an `opm preset edit` authoring
helper, probing non-loopback LAN endpoints behind an opt-in flag, and adopting the preset
file format in external tools (OCCM, a standalone TUI).

## Dependencies

`github.com/tailscale/hujson` — pure Go, no transitive deps, provides comment-preserving
parse/patch/pack. Within the spirit of the "no heavy config libraries" constraint (it is a
format codec, not a config framework; the viper ban stands).

## Testing

`t.TempDir()` filesystem isolation as everywhere else. Key invariants under test:
- comments/formatting/unknown sections survive apply byte-regions untouched
- prompt/permission keys inside a patched entry survive
- stale tuning keys are removed; unnamed entries untouched
- symlinked live file: target rewritten, symlink preserved
- extends resolution + cycle error; entry-level override semantics
- capture → status reports match; diff on matching preset is empty
- winning-file priority and multi-candidate warning
