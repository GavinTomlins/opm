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
// key are out of a preset's jurisdiction by design.
var tuningKeys = map[string]bool{
	"model":           true,
	"variant":         true,
	"fallback_models": true,
	"reasoningEffort": true,
	"thinking":        true,
	"temperature":     true,
	"top_p":           true,
	"maxTokens":       true,
}

// tuningKeyOrder lists tuning keys in display order (model first).
var tuningKeyOrder = []string{
	"model", "variant", "fallback_models", "reasoningEffort",
	"thinking", "temperature", "top_p", "maxTokens",
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
// keys in tuningKeyOrder.
func (e Entry) Summary() string {
	parts := make([]string, 0, len(e))
	for _, key := range tuningKeyOrder {
		raw, ok := e[key]
		if !ok {
			continue
		}
		if key == "model" {
			parts = append(parts, renderValue(raw))
			continue
		}
		parts = append(parts, key+"="+renderValue(raw))
	}
	return strings.Join(parts, " ")
}

// validate checks entry invariants beyond key restriction: every entry
// must pin a model in provider/model form, because within an entry the
// preset is authoritative and an entry without a model would strip the
// live model on apply.
func (e Entry) validate(section, name string) error {
	raw, ok := e["model"]
	if !ok {
		return fmt.Errorf("%s.%s: entry must set \"model\"", section, name)
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil {
		return fmt.Errorf("%s.%s: \"model\" must be a string", section, name)
	}
	if !strings.Contains(model, "/") {
		return fmt.Errorf("%s.%s: model %q must use provider/model form", section, name, model)
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
	return out
}
