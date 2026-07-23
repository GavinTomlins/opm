package preset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderFiles_DryRunAcrossSurfaces(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	livePath := writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	writeAgentMd(t, opencodeDir, "agents", "oracle", oracleMd)
	writeLive(t, opencodeDir, "opencode.json", `{ "model": "kimi/kimi-for-coding" }`)
	writePreset(t, m, "local", `{
		"agents": {
			"sisyphus": { "model": "omlx/qwen3-coder-30b" },
			"oracle": { "model": "omlx/qwen3.6-35b", "variant": "high" }
		},
		"opencode": { "model": "omlx/qwen3-coder-30b" }
	}`)

	originalLive, err := os.ReadFile(livePath)
	require.NoError(t, err)

	files, err := m.RenderFiles(mustResolve(t, m, "local"))
	require.NoError(t, err)
	require.Len(t, files, 3)

	byName := map[string]RenderedFile{}
	for _, f := range files {
		byName[f.Name] = f
	}

	omo := byName["oh-my-openagent.json"]
	assert.Equal(t, string(originalLive), string(omo.Before))
	assert.Contains(t, string(omo.After), "omlx/qwen3-coder-30b")
	assert.Contains(t, string(omo.After), "comments must survive preset applies")

	md := byName["oracle.md"]
	assert.Contains(t, string(md.Before), "kimi/kimi-for-coding")
	assert.Contains(t, string(md.After), "omlx/qwen3.6-35b")
	assert.Contains(t, string(md.After), "You are the oracle.")

	oc := byName["opencode.json"]
	assert.Contains(t, string(oc.After), "omlx/qwen3-coder-30b")

	// Fully dry-run: nothing on disk changed, no backups created.
	after, err := os.ReadFile(livePath)
	require.NoError(t, err)
	assert.Equal(t, string(originalLive), string(after))
	_, err = os.ReadDir(m.backupsDir)
	assert.True(t, os.IsNotExist(err))
}

func TestRenderFiles_CreatedFileHasNoBefore(t *testing.T) {
	m, _ := newTestManager(t)
	writePreset(t, m, "local", `{ "agents": { "a": "p/m" } }`)

	files, err := m.RenderFiles(mustResolve(t, m, "local"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Nil(t, files[0].Before)
	assert.Contains(t, string(files[0].After), "p/m")
}

func TestWriteRenderDir(t *testing.T) {
	dir := t.TempDir()
	files := []RenderedFile{
		{Name: "a.json", Before: []byte("old"), After: []byte("new")},
		{Name: "created.json", After: []byte("fresh")},
	}
	beforeDir, afterDir, err := WriteRenderDir(dir, files)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "before"), beforeDir)
	assert.Equal(t, filepath.Join(dir, "after"), afterDir)

	raw, err := os.ReadFile(filepath.Join(beforeDir, "a.json"))
	require.NoError(t, err)
	assert.Equal(t, "old", string(raw))
	raw, err = os.ReadFile(filepath.Join(afterDir, "a.json"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(raw))

	// Created files exist only in after/.
	_, err = os.Stat(filepath.Join(beforeDir, "created.json"))
	assert.True(t, os.IsNotExist(err))
	raw, err = os.ReadFile(filepath.Join(afterDir, "created.json"))
	require.NoError(t, err)
	assert.Equal(t, "fresh", string(raw))
}
