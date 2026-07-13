package preset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestManager returns a Manager rooted in temp dirs plus the opencode
// config dir it targets.
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	opencodeDir := filepath.Join(t.TempDir(), "opencode")
	require.NoError(t, os.MkdirAll(opencodeDir, 0o755))
	m := New(
		filepath.Join(root, "presets"),
		opencodeDir,
		filepath.Join(root, "backups", "presets"),
	)
	return m, opencodeDir
}

func writePreset(t *testing.T, m *Manager, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(m.PresetsDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(m.PresetsDir(), name+".json"), []byte(content), 0o644))
}

func writeLive(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestManager_ListSortedWithInvalid(t *testing.T) {
	m, _ := newTestManager(t)
	writePreset(t, m, "local", `{ "description": "all local", "agents": { "a": "p/m" } }`)
	writePreset(t, m, "broken", `{ not json`)

	infos, err := m.List()
	require.NoError(t, err)
	require.Len(t, infos, 2)
	assert.Equal(t, "broken", infos[0].Name)
	assert.Error(t, infos[0].Err)
	assert.Equal(t, "local", infos[1].Name)
	assert.Equal(t, "all local", infos[1].Description)
}

func TestManager_ListEmptyWhenNoDir(t *testing.T) {
	m, _ := newTestManager(t)
	infos, err := m.List()
	require.NoError(t, err)
	assert.Empty(t, infos)
}

func TestManager_LoadMissing(t *testing.T) {
	m, _ := newTestManager(t)
	_, err := m.Load("nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `preset "nope" does not exist`)
}

func TestManager_LoadInvalidName(t *testing.T) {
	m, _ := newTestManager(t)
	_, err := m.Load("../evil")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid preset name")
}

func TestManager_ResolveExtends(t *testing.T) {
	m, _ := newTestManager(t)
	writePreset(t, m, "base", `{
		"agents": {
			"sisyphus": { "model": "anthropic/claude-opus-4", "variant": "max" },
			"explore": "anthropic/claude-haiku-4"
		},
		"categories": { "quick": "anthropic/claude-haiku-4" }
	}`)
	writePreset(t, m, "local", `{
		"extends": "base",
		"agents": { "sisyphus": { "model": "omlx/qwen3-coder-30b" } }
	}`)

	p, err := m.Resolve("local")
	require.NoError(t, err)

	// Child entry replaces the base entry entirely — no key merge, so the
	// base's variant must NOT survive onto the overridden entry.
	assert.Equal(t, "omlx/qwen3-coder-30b", p.Agents["sisyphus"].Model())
	_, hasVariant := p.Agents["sisyphus"]["variant"]
	assert.False(t, hasVariant)

	// Untouched base entries are inherited.
	assert.Equal(t, "anthropic/claude-haiku-4", p.Agents["explore"].Model())
	assert.Equal(t, "anthropic/claude-haiku-4", p.Categories["quick"].Model())
}

func TestManager_ResolveCycle(t *testing.T) {
	m, _ := newTestManager(t)
	writePreset(t, m, "a", `{ "extends": "b", "agents": { "x": "p/m" } }`)
	writePreset(t, m, "b", `{ "extends": "a", "agents": { "y": "p/m" } }`)

	_, err := m.Resolve("a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

func TestManager_SaveRefusesOverwrite(t *testing.T) {
	m, _ := newTestManager(t)
	p := &Preset{Name: "x", Agents: map[string]Entry{"a": {"model": []byte(`"p/m"`)}}}

	_, err := m.Save(p, false)
	require.NoError(t, err)

	_, err = m.Save(p, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	_, err = m.Save(p, true)
	require.NoError(t, err)
}

func TestManager_LiveFilePriority(t *testing.T) {
	m, opencodeDir := newTestManager(t)

	// Only the modern name exists.
	writeLive(t, opencodeDir, "oh-my-openagent.json", `{}`)
	lf, err := m.LiveFile()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(opencodeDir, "oh-my-openagent.json"), lf.Path)
	assert.True(t, lf.Exists)
	assert.Empty(t, lf.Others)

	// The legacy name appears — it wins, the modern file is shadowed.
	writeLive(t, opencodeDir, "oh-my-opencode.json", `{}`)
	lf, err = m.LiveFile()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(opencodeDir, "oh-my-opencode.json"), lf.Path)
	require.Len(t, lf.Others, 1)
	assert.Equal(t, filepath.Join(opencodeDir, "oh-my-openagent.json"), lf.Others[0])
}

func TestManager_LiveFileNoneExists(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	lf, err := m.LiveFile()
	require.NoError(t, err)
	assert.False(t, lf.Exists)
	assert.Equal(t, filepath.Join(opencodeDir, "oh-my-openagent.json"), lf.Path)
}

func TestManager_LiveFileSymlinkResolvedForWrites(t *testing.T) {
	m, opencodeDir := newTestManager(t)

	// Dotfiles setup: the live file is a symlink into another repo.
	realDir := t.TempDir()
	realFile := writeLive(t, realDir, "oh-my-openagent.json", `{}`)
	require.NoError(t, os.Symlink(realFile, filepath.Join(opencodeDir, "oh-my-openagent.json")))

	lf, err := m.LiveFile()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(opencodeDir, "oh-my-openagent.json"), lf.Path)
	resolved, err := filepath.EvalSymlinks(realFile)
	require.NoError(t, err)
	assert.Equal(t, resolved, lf.Target)
}
