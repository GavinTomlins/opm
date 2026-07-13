package preset

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeOpencodeConfig writes an opencode.json declaring one provider with an
// explicit models map and one without.
func writeOpencodeConfig(t *testing.T, dir, baseURL string) {
	t.Helper()
	writeLive(t, dir, "opencode.json", fmt.Sprintf(`{
		"provider": {
			"omlx": {
				"npm": "@ai-sdk/openai-compatible",
				"options": { "baseURL": %q },
				"models": { "qwen3-coder-30b": {} }
			},
			"anthropic": {
				"npm": "@ai-sdk/anthropic",
				"options": { "apiKey": "{env:ANTHROPIC_API_KEY}" }
			}
		}
	}`, baseURL))
}

func TestValidateRefs(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeOpencodeConfig(t, opencodeDir, "http://localhost:8000/v1")
	writePreset(t, m, "mixed", `{
		"agents": {
			"good":       "omlx/qwen3-coder-30b",
			"badmodel":   "omlx/nonexistent-model",
			"discovered": "anthropic/claude-opus-4",
			"unknown":    { "model": "mystery/x", "fallback_models": ["omlx/also-missing"] }
		}
	}`)

	issues, err := m.ValidateRefs(mustResolve(t, m, "mixed"))
	require.NoError(t, err)

	var fails, warns []string
	for _, issue := range issues {
		if issue.Severity == SeverityFail {
			fails = append(fails, issue.Message)
		} else {
			warns = append(warns, issue.Message)
		}
	}

	// Declared provider with explicit models map: unknown models fail —
	// including refs inside fallback_models.
	require.Len(t, fails, 2)
	assert.Contains(t, fails[0], "also-missing")
	assert.Contains(t, fails[1], "nonexistent-model")

	// Undeclared provider warns; declared provider without a models map
	// (anthropic, discovery) produces nothing.
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], `"mystery"`)
	assert.True(t, HasFailures(issues))
}

func TestValidateRefs_NoOpencodeConfig(t *testing.T) {
	m, _ := newTestManager(t)
	writePreset(t, m, "local", `{ "agents": { "a": "p/m" } }`)

	issues, err := m.ValidateRefs(mustResolve(t, m, "local"))
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, SeverityWarn, issues[0].Severity)
	assert.Contains(t, issues[0].Message, "skipped model reference validation")
	assert.False(t, HasFailures(issues))
}

func TestProbeEndpoints_UpAndDown(t *testing.T) {
	m, opencodeDir := newTestManager(t)

	// A live local server: any HTTP response counts as alive.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	writeOpencodeConfig(t, opencodeDir, srv.URL)
	writePreset(t, m, "local", `{ "agents": { "a": "omlx/qwen3-coder-30b" } }`)

	issues, err := m.ProbeEndpoints(mustResolve(t, m, "local"))
	require.NoError(t, err)
	assert.Empty(t, issues)

	// Kill the server: the probe must fail.
	srv.Close()
	issues, err = m.ProbeEndpoints(mustResolve(t, m, "local"))
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, SeverityFail, issues[0].Severity)
	assert.Contains(t, issues[0].Message, `"omlx"`)
}

func TestProbeEndpoints_SkipsNonLoopback(t *testing.T) {
	m, opencodeDir := newTestManager(t)
	writeOpencodeConfig(t, opencodeDir, "https://api.example.com/v1")
	writePreset(t, m, "remote", `{ "agents": { "a": "omlx/qwen3-coder-30b" } }`)

	issues, err := m.ProbeEndpoints(mustResolve(t, m, "remote"))
	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestEntry_ModelRefsWithFallbacks(t *testing.T) {
	p, err := Parse("x", []byte(`{
		"agents": {
			"a": {
				"model": "p/primary",
				"fallback_models": ["p/fb1", { "model": "q/fb2", "variant": "high" }]
			}
		}
	}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"p/primary", "p/fb1", "q/fb2"}, p.Agents["a"].ModelRefs())
}

func TestIsLoopbackURL(t *testing.T) {
	assert.True(t, isLoopbackURL("http://localhost:11434/v1"))
	assert.True(t, isLoopbackURL("http://127.0.0.1:8000/v1"))
	assert.True(t, isLoopbackURL("http://[::1]:8000/v1"))
	assert.False(t, isLoopbackURL("https://api.kimi.com/coding/v1"))
	assert.False(t, isLoopbackURL("http://192.168.1.10:4000/v1"))
	assert.False(t, isLoopbackURL("not a url"))
}
