// Package preset implements model presets: named model-mapping overlays
// applied surgically onto the active profile's oh-my-openagent config.
// A preset owns only the model-tuning keys of the agents/categories it
// names; everything else in the live config (prompts, permissions,
// comments, formatting) is preserved.
package preset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

// tuningKeys are the only keys a preset entry may set and the only keys
// Apply may touch in the live config. Prompt/permission and every other
// key are out of a preset's jurisdiction by design. The set mirrors the
// model-tuning surface of upstream's oh-my-opencode.schema.json (v4.17.x):
// category routes an agent to a model tier instead of a direct model;
// providerOptions and textVerbosity are per-model provider tuning.
var tuningKeys = map[string]bool{
	"model":           true,
	"category":        true,
	"variant":         true,
	"fallback_models": true,
	"reasoningEffort": true,
	"thinking":        true,
	"temperature":     true,
	"top_p":           true,
	"maxTokens":       true,
	"textVerbosity":   true,
	"providerOptions": true,
}

// tuningKeyOrder lists tuning keys in display order (model first).
var tuningKeyOrder = []string{
	"model", "category", "variant", "fallback_models", "reasoningEffort",
	"thinking", "temperature", "top_p", "maxTokens", "textVerbosity",
	"providerOptions",
}

// Entry is the full tuning of a single agent or category. Keys are
// restricted to tuningKeys; values are kept raw so fallback chains and
// thinking blocks round-trip losslessly.
type Entry map[string]json.RawMessage

// UnmarshalJSON accepts either the object form or the string shorthand
// "provider/model" (equivalent to {"model": "provider/model"}).
func (e *Entry) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var model string
		if err := json.Unmarshal(trimmed, &model); err != nil {
			return err
		}
		raw, err := json.Marshal(model)
		if err != nil {
			return err
		}
		*e = Entry{"model": raw}
		return nil
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	for k := range m {
		if !tuningKeys[k] {
			return fmt.Errorf("unknown key %q: preset entries may only set model-tuning keys (%s)",
				k, strings.Join(tuningKeyOrder, ", "))
		}
	}
	*e = Entry(m)
	return nil
}

// Model returns the entry's model reference, or "" if unset or malformed.
func (e Entry) Model() string {
	raw, ok := e["model"]
	if !ok {
		return ""
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil {
		return ""
	}
	return model
}

// Summary renders the entry as "model [key=value ...]" for display, with
// keys in tuningKeyOrder. The routing key (model, or category for
// category-routed agents) renders bare/prefixed rather than as key=value,
// echoing the "c:<category>" input syntax used to set it.
func (e Entry) Summary() string {
	parts := make([]string, 0, len(e))
	for _, key := range tuningKeyOrder {
		raw, ok := e[key]
		if !ok {
			continue
		}
		switch key {
		case "model":
			parts = append(parts, renderValue(raw))
			continue
		case "category":
			parts = append(parts, "c:"+renderValue(raw))
			continue
		}
		parts = append(parts, key+"="+renderValue(raw))
	}
	return strings.Join(parts, " ")
}

// validate checks entry invariants beyond key restriction: every entry
// must route somewhere — a direct model in provider/model form, or (for
// agents) a category tier — because within an entry the preset is
// authoritative and an entry without either would strip the live routing
// on apply.
func (e Entry) validate(section, name string) error {
	modelRaw, hasModel := e["model"]
	categoryRaw, hasCategory := e["category"]

	if hasCategory && section == "categories" {
		return fmt.Errorf("%s.%s: \"category\" is only valid on agent entries", section, name)
	}
	if !hasModel && !hasCategory {
		return fmt.Errorf("%s.%s: entry must set \"model\" (or, for agents, \"category\")", section, name)
	}
	if hasModel {
		var model string
		if err := json.Unmarshal(modelRaw, &model); err != nil {
			return fmt.Errorf("%s.%s: \"model\" must be a string", section, name)
		}
		if !strings.Contains(model, "/") {
			return fmt.Errorf("%s.%s: model %q must use provider/model form", section, name, model)
		}
	}
	if hasCategory {
		var category string
		if err := json.Unmarshal(categoryRaw, &category); err != nil || category == "" {
			return fmt.Errorf("%s.%s: \"category\" must be a non-empty string", section, name)
		}
	}
	return nil
}

// Preset is a named model-mapping overlay.
type Preset struct {
	Name        string           `json:"-"`
	Schema      string           `json:"$schema,omitempty"`
	Description string           `json:"description,omitempty"`
	Extends     string           `json:"extends,omitempty"`
	Agents      map[string]Entry `json:"agents,omitempty"`
	Categories  map[string]Entry `json:"categories,omitempty"`
	// Opencode sets top-level opencode.json fields ("model", "small_model").
	// Unlike entries, this block only sets the keys it names.
	Opencode map[string]json.RawMessage `json:"opencode,omitempty"`
}

// Parse decodes a preset from JSON or JSONC bytes. Unknown top-level
// fields and unknown entry keys are errors so typos surface immediately.
func Parse(name string, data []byte) (*Preset, error) {
	std, err := hujson.Standardize(data)
	if err != nil {
		return nil, fmt.Errorf("preset %q: %w", name, err)
	}

	var p Preset
	dec := json.NewDecoder(bytes.NewReader(std))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("preset %q: %w", name, err)
	}
	p.Name = name

	for _, section := range []struct {
		label   string
		entries map[string]Entry
	}{{"agents", p.Agents}, {"categories", p.Categories}} {
		for entryName, entry := range section.entries {
			if err := entry.validate(section.label, entryName); err != nil {
				return nil, fmt.Errorf("preset %q: %w", name, err)
			}
		}
	}

	for key, raw := range p.Opencode {
		if !opencodeKeys[key] {
			return nil, fmt.Errorf("preset %q: opencode.%s: only \"model\" and \"small_model\" may be set", name, key)
		}
		var model string
		if err := json.Unmarshal(raw, &model); err != nil {
			return nil, fmt.Errorf("preset %q: opencode.%s must be a string", name, key)
		}
		if !strings.Contains(model, "/") {
			return nil, fmt.Errorf("preset %q: opencode.%s: model %q must use provider/model form", name, key, model)
		}
	}
	return &p, nil
}

// EntryCount returns the total number of agent and category entries.
func (p *Preset) EntryCount() int {
	return len(p.Agents) + len(p.Categories)
}

// SortedAgentNames returns agent entry names in sorted order.
func (p *Preset) SortedAgentNames() []string { return sortedKeys(p.Agents) }

// SortedCategoryNames returns category entry names in sorted order.
func (p *Preset) SortedCategoryNames() []string { return sortedKeys(p.Categories) }

// OpencodeValues returns the opencode block as sorted key/rendered-value
// pairs for display.
func (p *Preset) OpencodeValues() [][2]string {
	keys := make([]string, 0, len(p.Opencode))
	for k := range p.Opencode {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, renderValue(p.Opencode[k])})
	}
	return out
}

func sortedKeys(m map[string]Entry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// merge overlays child onto base at entry level: a child's entry for an
// agent/category replaces the base's entry entirely. An entry is an atomic
// tuning unit — it is never key-merged across an extends chain.
func merge(base, child *Preset) *Preset {
	out := &Preset{
		Name:        child.Name,
		Description: child.Description,
		Agents:      map[string]Entry{},
		Categories:  map[string]Entry{},
		Opencode:    map[string]json.RawMessage{},
	}
	for name, e := range base.Agents {
		out.Agents[name] = e
	}
	for name, e := range child.Agents {
		out.Agents[name] = e
	}
	for name, e := range base.Categories {
		out.Categories[name] = e
	}
	for name, e := range child.Categories {
		out.Categories[name] = e
	}
	// The opencode block merges key-wise: each key is independent.
	for key, v := range base.Opencode {
		out.Opencode[key] = v
	}
	for key, v := range child.Opencode {
		out.Opencode[key] = v
	}
	if len(out.Opencode) == 0 {
		out.Opencode = nil
	}
	return out
}
