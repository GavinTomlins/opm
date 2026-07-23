package preset

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModels(t *testing.T) {
	m, opencodeDir := newTestManager(t)

	// A live local server reporting its loaded models.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/models", r.URL.Path)
		_, _ = fmt.Fprint(w, `{"data":[{"id":"qwen3-coder-30b"},{"id":"llama3.3:70b"}]}`)
	}))
	t.Cleanup(srv.Close)

	writeLive(t, opencodeDir, "opencode.json", fmt.Sprintf(`{
		"provider": {
			"omlx": {
				"npm": "@ai-sdk/openai-compatible",
				"options": { "baseURL": "%s/v1" },
				"models": { "qwen3-coder-30b": {}, "stale-model": {} }
			},
			"anthropic": { "npm": "@ai-sdk/anthropic", "options": { "apiKey": "x" } },
			"kimi": {
				"npm": "@ai-sdk/openai-compatible",
				"options": { "baseURL": "https://api.kimi.com/coding/v1" },
				"models": { "kimi-for-coding": {} }
			}
		}
	}`, srv.URL))

	providers, err := m.ListModels()
	require.NoError(t, err)
	require.Len(t, providers, 3)

	byName := map[string]ProviderModels{}
	for _, pm := range providers {
		byName[pm.Provider] = pm
	}

	// Loopback provider: probed, online, serves models beyond the declared set.
	omlx := byName["omlx"]
	assert.True(t, omlx.Loopback)
	require.NotNil(t, omlx.Online)
	assert.True(t, *omlx.Online)
	assert.Equal(t, []string{"qwen3-coder-30b", "stale-model"}, omlx.Declared)
	assert.Equal(t, []string{"llama3.3:70b", "qwen3-coder-30b"}, omlx.Served)
	assert.Equal(t, "omlx/qwen3-coder-30b", omlx.Ref("qwen3-coder-30b"))

	// Remote providers are never probed.
	assert.Nil(t, byName["anthropic"].Online)
	assert.Nil(t, byName["kimi"].Online)
	assert.Equal(t, []string{"kimi-for-coding"}, byName["kimi"].Declared)
	assert.Empty(t, byName["anthropic"].Declared)
}

func TestListModels_OfflineLoopback(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // dead server

	writeLive(t, opencodeDir, "opencode.json", fmt.Sprintf(`{
		"provider": { "ollama": { "npm": "@ai-sdk/openai-compatible",
			"options": { "baseURL": "%s/v1" }, "models": { "llama3.1:8b": {} } } }
	}`, srv.URL))

	providers, err := m.ListModels()
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.NotNil(t, providers[0].Online)
	assert.False(t, *providers[0].Online)
	assert.Empty(t, providers[0].Served)
	assert.Equal(t, []string{"llama3.1:8b"}, providers[0].Declared)
}

func TestListModels_NoOpencodeConfig(t *testing.T) {
	m, _ := newTestManager(t)
	_, err := m.ListModels()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no opencode.json")
}

func TestCreateAll(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)

	p, path, err := m.CreateAll("opus", "anthropic/claude-opus-4-8", false)
	require.NoError(t, err)
	assert.FileExists(t, path)

	// Every agent and category from the live config, all on the one model —
	// including prompt-only entries, since "all agents" means all.
	require.Len(t, p.Agents, 3)
	require.Len(t, p.Categories, 1)
	for name, entry := range p.Agents {
		assert.Equal(t, "anthropic/claude-opus-4-8", entry.Model(), "agent %s", name)
	}
	assert.Equal(t, "anthropic/claude-opus-4-8", p.Categories["quick"].Model())

	// The generated preset applies cleanly and matches afterwards.
	_, err = m.Apply(mustResolve(t, m, "opus"))
	require.NoError(t, err)
	changes, err := m.Diff(mustResolve(t, m, "opus"))
	require.NoError(t, err)
	assert.Empty(t, changes)
}

func TestCreateAll_Errors(t *testing.T) {
	m, opencodeDir := newTestManager(t)

	_, _, err := m.CreateAll("x", "no-slash", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider/model")

	_, _, err = m.CreateAll("x", "p/m", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no oh-my-openagent config")

	writeLive(t, opencodeDir, "oh-my-openagent.json", liveFixture)
	_, _, err = m.CreateAll("x", "p/m", false)
	require.NoError(t, err)
	_, _, err = m.CreateAll("x", "p/m", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}
