package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tbcrawford/opm/internal/preset"
)

func TestBuildCatalog(t *testing.T) {
	offline := false
	online := true
	providers := []preset.ProviderModels{
		{Provider: "omlx", Declared: []string{"model-a", "model-b"}, Served: []string{"model-b", "model-c"}, Online: &online},
		{Provider: "openai", Declared: []string{"gpt-5.5"}},
		{Provider: "ollama", Online: &offline},
	}
	lines := buildCatalog(providers)
	require.Len(t, lines, 4) // model-a, model-b (declared, dedup'd against served), model-c (served-only), gpt-5.5

	var refs []string
	for _, l := range lines {
		refs = append(refs, l.Ref)
	}
	assert.Equal(t, []string{"omlx/model-a", "omlx/model-b", "omlx/model-c", "openai/gpt-5.5"}, refs)

	// Numbers are sequential starting at 1, no gaps or repeats.
	for i, l := range lines {
		assert.Equal(t, i+1, l.Number)
	}

	// Declared entries carry no note; served-but-undeclared does.
	assert.Empty(t, lines[0].Note)
	assert.NotEmpty(t, lines[2].Note)

	// Online status is per-provider, not per-model.
	assert.False(t, lines[0].ProviderOffline)
	assert.False(t, lines[3].ProviderOffline) // openai has no Online set (nil) => not offline

	offlineLines := buildCatalog([]preset.ProviderModels{{Provider: "ollama", Declared: []string{"x"}, Online: &offline}})
	require.Len(t, offlineLines, 1)
	assert.True(t, offlineLines[0].ProviderOffline)
}

func TestParseEntryInput(t *testing.T) {
	catalog := []catalogLine{{Number: 1, Ref: "omlx/model-a"}, {Number: 2, Ref: "openai/gpt-5.5"}}

	cases := []struct {
		name         string
		input        string
		allowCat     bool
		wantAction   entryAction
		wantRef      string
		wantCategory string
	}{
		{"blank keeps", "", true, actionKeep, "", ""},
		{"clear", "clear", true, actionClear, "", ""},
		{"clear case-insensitive", "CLEAR", true, actionClear, "", ""},
		{"list", "list", true, actionRelist, "", ""},
		{"number selects catalog entry", "2", true, actionSetModel, "openai/gpt-5.5", ""},
		{"out of range number", "99", true, actionInvalid, "", ""},
		{"literal ref", "kiro/claude-opus-4-7", true, actionSetModel, "kiro/claude-opus-4-7", ""},
		{"category routing", "c:quick", true, actionSetCategory, "", "quick"},
		{"category routing rejected on categories", "c:quick", false, actionInvalid, "", ""},
		{"empty category name", "c:", true, actionInvalid, "", ""},
		{"garbage", "not-a-ref-or-number", true, actionInvalid, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseEntryInput(tc.input, catalog, tc.allowCat)
			assert.Equal(t, tc.wantAction, got.Action)
			if tc.wantAction == actionSetModel {
				assert.Equal(t, tc.wantRef, got.Ref)
			}
			if tc.wantAction == actionSetCategory {
				assert.Equal(t, tc.wantCategory, got.Category)
			}
			if tc.wantAction == actionInvalid {
				assert.NotEmpty(t, got.ErrMsg)
			}
		})
	}
}
