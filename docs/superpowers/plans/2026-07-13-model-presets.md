# Model Presets Implementation Plan (Phase 1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `preset` layer to opm: named model-mapping overlays (`~/.config/opm/presets/*.json[c]`) applied surgically onto the active profile's oh-my-openagent config, so users can bulk-swap providers/models across all sub-agents and categories in one command.

**Architecture:** New `internal/preset` package (Manager struct mirroring `store.Store`'s temp-dir-testable construction) + one new `cmd/preset.go` command group. Apply uses hujson (JWCC) parse → RFC 6902 patch → pack, so comments and all non-tuning content are preserved. See `docs/superpowers/specs/2026-07-13-model-presets-design.md` for full semantics.

**Tech Stack:** Go, `github.com/tailscale/hujson`, `github.com/stretchr/testify`, `just`.

---

### Task 1: Preset core — types, parsing, extends

**Files:**
- Create: `internal/preset/preset.go`, `internal/preset/preset_test.go`
- Modify: `internal/paths/paths.go` (add `PresetsDir()`, `PresetBackupsDir()`)

- [x] **Step 1:** `Entry` type (`map[string]json.RawMessage` restricted to tuning keys: model, variant, fallback_models, reasoningEffort, thinking, temperature, top_p, maxTokens) with custom unmarshal accepting string shorthand; unknown keys error; `model` must contain `/`.
- [x] **Step 2:** `Preset` struct (Name, Description, Extends, Agents, Categories) parsed via hujson→Standardize→encoding/json.
- [x] **Step 3:** Extends resolution: single chain, cycle detection, entry-level override.
- [x] **Step 4:** Tests: shorthand, unknown-key error, bad model format, extends merge, cycle error.

### Task 2: Manager — store, live-file resolution, apply, backups

**Files:**
- Create: `internal/preset/manager.go`, `internal/preset/apply.go`, `internal/preset/manager_test.go`, `internal/preset/apply_test.go`

- [x] **Step 1:** `Manager{presetsDir, opencodeDir, backupsDir}` + `New`; `List`, `Load`, `Resolve`, `Save`.
- [x] **Step 2:** `LiveFile()` — winning-candidate resolution (oh-my-opencode.jsonc > .json > oh-my-openagent.jsonc > .json), multi-candidate detection for warnings, symlink resolution to final write target.
- [x] **Step 3:** `Diff(preset)` — field-level changes (add/replace/remove per tuning key).
- [x] **Step 4:** `Apply(preset)` — backup to backupsDir (timestamped), build JSON Patch from Diff, `hujson.Value.Patch`, atomic symlink-aware write; create-with-$schema when no live file exists.
- [x] **Step 5:** `Capture(name, force)` and `Revert()`.
- [x] **Step 6:** Tests for all invariants in the spec (comment preservation, prompt/permission survival, stale-key removal, symlink write-through, capture/status round-trip).

### Task 3: Command group

**Files:**
- Create: `cmd/preset.go`
- Modify: `cmd/help.go` (new "Model presets" help group), `internal/output/output.go` (SubcmdHelp nested path via `cmd.CommandPath()`), `cmd/cmd_test.go`

- [x] **Step 1:** `opm preset` parent + `list/show/use/diff/capture/status/revert` subcommands; manager factory derived from `newStore()` paths so the existing test harness injection works unchanged; preset-name shell completion.
- [x] **Step 2:** Help-group registration; SubcmdHelp fix so nested help prints `opm preset use`.
- [x] **Step 3:** cmd-level tests via `cmdHarness` (use/diff/capture/status/revert happy paths + error surfaces).

### Task 4: Verify

- [x] `just fmt` and `just verify` pass clean.
