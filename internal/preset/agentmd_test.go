package preset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oracleMd = `---
description: Debugging consultant
mode: subagent
model: kimi/kimi-for-coding
permission:
  edit: deny
---

You are the oracle. Investigate carefully.
`

func writeAgentMd(t *testing.T, opencodeDir, dir, name, content string) string {
	t.Helper()
	full := filepath.Join(opencodeDir, dir)
	require.NoError(t, os.MkdirAll(full, 0o755))
	path := filepath.Join(full, name+".md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestSetFrontmatterModel(t *testing.T) {
	updated, err := setFrontmatterModel([]byte(oracleMd), "omlx/qwen3.6-35b")
	require.NoError(t, err)
	text := string(updated)

	model, ok := readFrontmatterModel(updated)
	require.True(t, ok)
	assert.Equal(t, "omlx/qwen3.6-35b", model)

	// Everything else is untouched.
	assert.Contains(t, text, "description: Debugging consultant")
	assert.Contains(t, text, "  edit: deny")
	assert.Contains(t, text, "You are the oracle. Investigate carefully.")
	assert.NotContains(t, text, "kimi/kimi-for-coding")
}

func TestSetFrontmatterModel_InsertsWhenAbsent(t *testing.T) {
	src := "---\nmode: subagent\n---\n\nBody.\n"
	updated, err := setFrontmatterModel([]byte(src), "p/m")
	require.NoError(t, err)
	model, ok := readFrontmatterModel(updated)
	require.True(t, ok)
	assert.Equal(t, "p/m", model)
	assert.Contains(t, string(updated), "mode: subagent")
	assert.Contains(t, string(updated), "Body.")
}

func TestSetFrontmatterModel_NoFrontmatter(t *testing.T) {
	_, err := setFrontmatterModel([]byte("just a body\n"), "p/m")
	require.Error(t, err)
}

func TestApply_SyncsAgentMarkdown(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	mdPath := writeAgentMd(t, opencodeDir, "agents", "oracle", oracleMd)
	writePreset(t, m, "local", localPreset)

	result, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)

	// The diff includes the markdown change alongside the config changes.
	var mdChange *Change
	for i := range result.Changes {
		if result.Changes[i].Section == "agent-md" {
			mdChange = &result.Changes[i]
		}
	}
	require.NotNil(t, mdChange)
	assert.Equal(t, "oracle", mdChange.Name)
	assert.Equal(t, "agent-md.oracle.model", mdChange.Field())

	data, err := os.ReadFile(mdPath)
	require.NoError(t, err)
	model, ok := readFrontmatterModel(data)
	require.True(t, ok)
	assert.Equal(t, "omlx/qwen3.6-35b", model)
	assert.Contains(t, string(data), "You are the oracle.")

	// A second apply is a no-op: the markdown is in sync.
	result, err = m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)
	assert.Empty(t, result.Changes)
}

func TestApply_AgentMarkdownSymlinkPreserved(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)

	// The markdown file is a symlink into another repo (dotfiles setup).
	realDir := t.TempDir()
	realFile := filepath.Join(realDir, "oracle.md")
	require.NoError(t, os.WriteFile(realFile, []byte(oracleMd), 0o644))
	agentsDir := filepath.Join(opencodeDir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	linkPath := filepath.Join(agentsDir, "oracle.md")
	require.NoError(t, os.Symlink(realFile, linkPath))

	writePreset(t, m, "local", localPreset)
	_, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)

	fi, err := os.Lstat(linkPath)
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink)

	data, err := os.ReadFile(realFile)
	require.NoError(t, err)
	model, ok := readFrontmatterModel(data)
	require.True(t, ok)
	assert.Equal(t, "omlx/qwen3.6-35b", model)
}

func TestApply_OpencodeBlock(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	writeLive(t, opencodeDir, "opencode.json", `{
		// provider declarations must survive
		"provider": { "omlx": { "npm": "@ai-sdk/openai-compatible", "options": { "baseURL": "http://localhost:8000/v1" } } },
		"model": "kimi/kimi-for-coding"
	}`)
	writePreset(t, m, "local", `{
		"agents": { "sisyphus": "omlx/qwen3-coder-30b" },
		"opencode": { "model": "omlx/qwen3-coder-30b", "small_model": "omlx/qwen3-coder-30b" }
	}`)

	result, err := m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)

	fields := map[string]bool{}
	for _, c := range result.Changes {
		fields[c.Field()] = true
	}
	assert.True(t, fields["opencode.model"])
	assert.True(t, fields["opencode.small_model"])

	raw, err := os.ReadFile(filepath.Join(opencodeDir, "opencode.json"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "provider declarations must survive")

	var cfg map[string]any
	require.NoError(t, jsonUnmarshalJSONC(raw, &cfg))
	assert.Equal(t, "omlx/qwen3-coder-30b", cfg["model"])
	assert.Equal(t, "omlx/qwen3-coder-30b", cfg["small_model"])
	assert.Contains(t, cfg["provider"].(map[string]any), "omlx")

	// Idempotent.
	result, err = m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)
	assert.Empty(t, result.Changes)
}

func TestParse_OpencodeBlockValidated(t *testing.T) {
	_, err := Parse("bad", []byte(`{ "opencode": { "temperature": 0.5 } }`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opencode.temperature")

	_, err = Parse("bad", []byte(`{ "opencode": { "model": "no-slash" } }`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider/model")
}

func TestRevert_RestoresMultiFileSet(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	livePath := writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	mdPath := writeAgentMd(t, opencodeDir, "agent", "oracle", oracleMd)
	writePreset(t, m, "local", localPreset)

	originalLive, err := os.ReadFile(livePath)
	require.NoError(t, err)
	originalMd, err := os.ReadFile(mdPath)
	require.NoError(t, err)

	_, err = m.Apply(mustResolve(t, m, "local"))
	require.NoError(t, err)

	restored, _, err := m.Revert()
	require.NoError(t, err)
	assert.Len(t, restored, 2)

	afterLive, err := os.ReadFile(livePath)
	require.NoError(t, err)
	assert.Equal(t, string(originalLive), string(afterLive))
	afterMd, err := os.ReadFile(mdPath)
	require.NoError(t, err)
	assert.Equal(t, string(originalMd), string(afterMd))
}

func TestCapture_IncludesOpencodeBlock(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	writeLive(t, opencodeDir, "opencode.json", `{ "model": "kimi/kimi-for-coding" }`)

	p, _, err := m.Capture("current", false)
	require.NoError(t, err)
	require.NotNil(t, p.Opencode)
	assert.Equal(t, `"kimi/kimi-for-coding"`, string(p.Opencode["model"]))
}
