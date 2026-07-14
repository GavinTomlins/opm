package preset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tailscale/hujson"
)

// liveFixture is a realistic oh-my-openagent.json with comments, prompt and
// permission overrides, and sections a preset must never touch.
const liveFixture = `{
	// managed by hand — comments must survive preset applies
	"$schema": "https://example.com/oh-my-openagent.schema.json",
	"agents": {
		"sisyphus": {
			"model": "kimi-for-coding-oauth/kimi-for-coding",
			"variant": "max",
			"prompt": "Load Node Sage memory context first." // agent-specific override
		},
		"oracle": {
			"model": "kimi-for-coding-oauth/kimi-for-coding",
			"variant": "high",
			"permission": { "edit": "deny" }
		},
		"librarian": { "model": "kimi-for-coding-oauth/kimi-for-coding" }
	},
	"categories": {
		"quick": { "model": "kimi-for-coding-oauth/kimi-for-coding" }
	},
	"team_mode": { "enabled": true },
	"disabled_hooks": ["startup-toast"]
}`

const localPreset = `{
	"description": "all local",
	"agents": {
		"sisyphus": { "model": "omlx/qwen3-coder-30b" },
		"oracle": { "model": "omlx/qwen3.6-35b", "variant": "high" }
	},
	"categories": {
		"quick": "ollama/llama3.1:8b"
	}
}`

func mustResolve(t *testing.T, m *Manager, name string) *Preset {
	t.Helper()
	p, err := m.Resolve(name)
	require.NoError(t, err)
	return p
}

func parseLive(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, jsonUnmarshalJSONC(data, &out))
	return out
}

func TestApply_SurgicalPatch(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	livePath := writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	writePreset(t, m, "local", localPreset)

	result, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)
	assert.False(t, result.Created)
	assert.NotEmpty(t, result.BackupPath)
	assert.NotEmpty(t, result.Changes)

	raw, err := os.ReadFile(livePath)
	require.NoError(t, err)
	text := string(raw)

	// Comments survive.
	assert.Contains(t, text, "comments must survive preset applies")
	assert.Contains(t, text, "// agent-specific override")

	live := parseLive(t, livePath)
	agents := live["agents"].(map[string]any)
	sisyphus := agents["sisyphus"].(map[string]any)
	oracle := agents["oracle"].(map[string]any)

	// Models re-pointed.
	assert.Equal(t, "omlx/qwen3-coder-30b", sisyphus["model"])
	assert.Equal(t, "omlx/qwen3.6-35b", oracle["model"])
	assert.Equal(t, "ollama/llama3.1:8b",
		live["categories"].(map[string]any)["quick"].(map[string]any)["model"])

	// Non-tuning keys inside patched entries survive.
	assert.Equal(t, "Load Node Sage memory context first.", sisyphus["prompt"])
	assert.Equal(t, map[string]any{"edit": "deny"}, oracle["permission"])

	// Stale tuning keys are removed: the preset's sisyphus entry sets no
	// variant, so the live "max" must be gone; oracle keeps its explicit one.
	_, hasVariant := sisyphus["variant"]
	assert.False(t, hasVariant)
	assert.Equal(t, "high", oracle["variant"])

	// Entries and sections the preset does not name are untouched.
	assert.Equal(t, "kimi-for-coding-oauth/kimi-for-coding", agents["librarian"].(map[string]any)["model"])
	assert.Equal(t, map[string]any{"enabled": true}, live["team_mode"])
	assert.Equal(t, []any{"startup-toast"}, live["disabled_hooks"])
}

func TestApply_Idempotent(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	writePreset(t, m, "local", localPreset)

	_, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)

	// Second apply is a no-op: no changes, no backup, no write.
	result, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)
	assert.Empty(t, result.Changes)
	assert.Empty(t, result.BackupPath)
}

func TestApply_CreatesFileWhenNoneExists(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writePreset(t, m, "local", localPreset)

	result, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)
	assert.True(t, result.Created)

	livePath := filepath.Join(opencodeDir, "oh-my-openagent.json")
	live := parseLive(t, livePath)
	assert.Equal(t, omoSchemaURL, live["$schema"])
	assert.Equal(t, "omlx/qwen3-coder-30b",
		live["agents"].(map[string]any)["sisyphus"].(map[string]any)["model"])
}

func TestApply_AddsMissingEntriesAndSections(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	// Live config with no categories section and a missing agent.
	writeLive(t, opencodeDir, "oh-my-openagent.json", `{
		"agents": { "sisyphus": { "model": "a/b" } }
	}`)
	writePreset(t, m, "local", localPreset)

	_, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)

	live := parseLive(t, filepath.Join(opencodeDir, "oh-my-openagent.json"))
	agents := live["agents"].(map[string]any)
	assert.Equal(t, "omlx/qwen3.6-35b", agents["oracle"].(map[string]any)["model"])
	assert.Equal(t, "high", agents["oracle"].(map[string]any)["variant"])
	assert.Equal(t, "ollama/llama3.1:8b",
		live["categories"].(map[string]any)["quick"].(map[string]any)["model"])
}

func TestApply_WritesThroughSymlink(t *testing.T) {
	m, opencodeDir := newTestManager(t)

	realDir := t.TempDir()
	realFile := writeLive(t, realDir, "oh-my-openagent.json", liveFixture)
	linkPath := filepath.Join(opencodeDir, "oh-my-openagent.json")
	require.NoError(t, os.Symlink(realFile, linkPath))
	writePreset(t, m, "local", localPreset)

	_, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)

	// The symlink is still a symlink; the real file received the change.
	fi, err := os.Lstat(linkPath)
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink)

	live := parseLive(t, realFile)
	assert.Equal(t, "omlx/qwen3-coder-30b",
		live["agents"].(map[string]any)["sisyphus"].(map[string]any)["model"])
}

func TestApply_PatchesWinningCanonicalFile(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	canonicalPath := writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	shadowedPath := writeLive(t, opencodeDir, "oh-my-opencode.json", `{"agents":{}}`)
	writePreset(t, m, "local", localPreset)

	result, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)
	assert.Equal(t, canonicalPath, result.LivePath)
	require.Len(t, result.Others, 1)

	// The winning canonical file got the change; the shadowed legacy
	// file didn't.
	live := parseLive(t, canonicalPath)
	assert.Equal(t, "omlx/qwen3-coder-30b",
		live["agents"].(map[string]any)["sisyphus"].(map[string]any)["model"])
	shadowed, err := os.ReadFile(shadowedPath)
	require.NoError(t, err)
	assert.Equal(t, `{"agents":{}}`, string(shadowed))
}

func TestDiff_EmptyWhenMatching(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	writePreset(t, m, "local", localPreset)

	p := mustResolve(t, m, "local")
	changes, err := m.Diff(p)
	require.NoError(t, err)
	assert.NotEmpty(t, changes)

	_, err = m.Apply(p)
	require.NoError(t, err)

	changes, err = m.Diff(p)
	require.NoError(t, err)
	assert.Empty(t, changes)
}

func TestCapture_RoundTrip(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)

	p, path, err := m.Capture("current", false)
	require.NoError(t, err)
	assert.FileExists(t, path)

	// Captured tuning keys only; prompt/permission are not preset content.
	assert.Equal(t, "kimi-for-coding-oauth/kimi-for-coding", p.Agents["sisyphus"].Model())
	_, hasPrompt := p.Agents["sisyphus"]["prompt"]
	assert.False(t, hasPrompt)

	// The captured preset matches the live config it came from.
	resolved := mustResolve(t, m, "current")
	changes, err := m.Diff(resolved)
	require.NoError(t, err)
	assert.Empty(t, changes)
}

func TestCapture_NoLiveConfig(t *testing.T) {
	m, _ := newTestManager(t)
	_, _, err := m.Capture("current", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no oh-my-openagent config found")
}

func TestRevert_RestoresBackup(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	livePath := writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	writePreset(t, m, "local", localPreset)

	original, err := os.ReadFile(livePath)
	require.NoError(t, err)

	_, err = m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)

	restored, backupName, err := m.Revert()
	require.NoError(t, err)
	// Backups record symlink-resolved targets (macOS: /var → /private/var),
	// so compare resolved paths.
	resolvedLive, err := filepath.EvalSymlinks(livePath)
	require.NoError(t, err)
	assert.Equal(t, []string{resolvedLive}, restored)
	assert.Regexp(t, `^\d{8}-\d{6}`, backupName)

	after, err := os.ReadFile(livePath)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(after))
}

func TestRevert_NoBackups(t *testing.T) {
	m, _ := newTestManager(t)
	_, _, err := m.Revert()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no preset backups found")
}

// jsonUnmarshalJSONC standardizes JSONC then unmarshals — test helper for
// reading fixture output.
func jsonUnmarshalJSONC(data []byte, v any) error {
	std, err := hujson.Standardize(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(std, v)
}

func TestManager_LiveEntries(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)

	entries, err := m.LiveEntries()
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		names = append(names, e.Section+":"+e.Name)
	}
	assert.Contains(t, names, "agents:sisyphus")
	assert.Contains(t, names, "agents:oracle")
	assert.Contains(t, names, "agents:librarian")
	assert.Contains(t, names, "categories:quick")

	for _, e := range entries {
		if e.Name == "sisyphus" {
			assert.Equal(t, "kimi-for-coding-oauth/kimi-for-coding", e.Entry.Model())
			_, hasPrompt := e.Entry["prompt"]
			assert.False(t, hasPrompt, "LiveEntries must only surface tuning keys, not prompt/permission")
		}
	}
}

func TestManager_LiveEntries_NoLiveFile(t *testing.T) {
	m, _ := newTestManager(t)
	entries, err := m.LiveEntries()
	require.NoError(t, err)
	assert.Empty(t, entries)
}
