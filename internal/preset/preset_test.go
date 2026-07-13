package preset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_StringShorthand(t *testing.T) {
	p, err := Parse("local", []byte(`{
		"agents": { "explore": "ollama/qwen2.5-coder:14b" },
		"categories": { "quick": "ollama/llama3.1:8b" }
	}`))
	require.NoError(t, err)
	assert.Equal(t, "ollama/qwen2.5-coder:14b", p.Agents["explore"].Model())
	assert.Equal(t, "ollama/llama3.1:8b", p.Categories["quick"].Model())
}

func TestParse_JSONCWithComments(t *testing.T) {
	p, err := Parse("local", []byte(`{
		// heavy lifting stays on omlx
		"agents": {
			"sisyphus": { "model": "omlx/qwen3-coder-30b", "variant": "max" }, // trailing comma ok
		},
	}`))
	require.NoError(t, err)
	assert.Equal(t, "omlx/qwen3-coder-30b", p.Agents["sisyphus"].Model())
}

func TestParse_UnknownEntryKeyRejected(t *testing.T) {
	_, err := Parse("bad", []byte(`{
		"agents": { "sisyphus": { "model": "a/b", "prompt": "sneaky override" } }
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown key "prompt"`)
}

func TestParse_UnknownTopLevelFieldRejected(t *testing.T) {
	_, err := Parse("bad", []byte(`{ "agent": { "sisyphus": "a/b" } }`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent")
}

func TestParse_EntryWithoutModelRejected(t *testing.T) {
	_, err := Parse("bad", []byte(`{ "agents": { "sisyphus": { "variant": "max" } } }`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `must set "model"`)
}

func TestParse_ModelFormatValidated(t *testing.T) {
	_, err := Parse("bad", []byte(`{ "agents": { "sisyphus": "claude-opus" } }`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider/model")
}

func TestParse_SchemaFieldAllowed(t *testing.T) {
	_, err := Parse("ok", []byte(`{ "$schema": "https://example.com/x.json", "agents": { "a": "p/m" } }`))
	require.NoError(t, err)
}

func TestEntry_Summary(t *testing.T) {
	p, err := Parse("x", []byte(`{
		"agents": { "sisyphus": { "model": "omlx/qwen3-coder-30b", "variant": "max", "thinking": {"type":"enabled"} } }
	}`))
	require.NoError(t, err)
	sum := p.Agents["sisyphus"].Summary()
	assert.Equal(t, `omlx/qwen3-coder-30b variant=max thinking={"type":"enabled"}`, sum)
}
