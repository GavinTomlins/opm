package output

import (
	"fmt"
	"io"
	"strings"

	"github.com/tbcrawford/opm/internal/preset"
)

// PresetTable writes the preset listing: ● marks presets matching the live
// config, ✗ marks presets that fail to parse.
func PresetTable(w io.Writer, infos []preset.Info, matches map[string]bool) {
	maxLen := 0
	for _, info := range infos {
		if len(info.Name) > maxLen {
			maxLen = len(info.Name)
		}
	}
	for _, info := range infos {
		pad := strings.Repeat(" ", maxLen-len(info.Name))
		switch {
		case info.Err != nil:
			_, _ = fmt.Fprintf(w, "%s %s%s    %s\n", red.Sprint("✗"), red.Sprint(info.Name), pad, dim.Sprint("(invalid: "+info.Err.Error()+")"))
		case matches[info.Name]:
			_, _ = fmt.Fprintf(w, "%s %s%s    %s\n", green.Sprint("●"), blue.Sprint(info.Name), pad, dim.Sprint(info.Description))
		default:
			_, _ = fmt.Fprintf(w, "%s %s%s    %s\n", dim.Sprint("○"), dim.Sprint(info.Name), pad, dim.Sprint(info.Description))
		}
	}
}

// PresetDetails writes the resolved mapping of a preset: header, then one
// row per agent/category entry.
func PresetDetails(w io.Writer, p *preset.Preset) {
	_, _ = fmt.Fprintln(w, blue.Sprint(p.Name))
	if p.Description != "" {
		_, _ = fmt.Fprintln(w, dim.Sprint(p.Description))
	}

	sections := []struct {
		label string
		names []string
		get   func(string) preset.Entry
	}{
		{"Categories", p.SortedCategoryNames(), func(n string) preset.Entry { return p.Categories[n] }},
		{"Agents", p.SortedAgentNames(), func(n string) preset.Entry { return p.Agents[n] }},
	}

	for _, sec := range sections {
		if len(sec.names) == 0 {
			continue
		}
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, dim.Sprint(sec.label))
		maxLen := 0
		for _, name := range sec.names {
			if len(name) > maxLen {
				maxLen = len(name)
			}
		}
		for _, name := range sec.names {
			pad := strings.Repeat(" ", maxLen-len(name))
			_, _ = fmt.Fprintf(w, "  %s%s    %s\n", name, pad, sec.get(name).Summary())
		}
	}

	if oc := p.OpencodeValues(); len(oc) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, dim.Sprint("Opencode"))
		maxLen := 0
		for _, kv := range oc {
			if len(kv[0]) > maxLen {
				maxLen = len(kv[0])
			}
		}
		for _, kv := range oc {
			pad := strings.Repeat(" ", maxLen-len(kv[0]))
			_, _ = fmt.Fprintf(w, "  %s%s    %s\n", kv[0], pad, kv[1])
		}
	}
}

// ProviderModelsTable writes the model inventory grouped by provider.
func ProviderModelsTable(w io.Writer, providers []preset.ProviderModels) {
	for i, pm := range providers {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}

		header := blue.Sprint(pm.Provider)
		if pm.BaseURL != "" {
			header += "  " + dim.Sprint(pm.BaseURL)
		}
		switch {
		case pm.Online != nil && *pm.Online:
			header += "  " + green.Sprint("● online")
		case pm.Online != nil:
			header += "  " + red.Sprint("✗ offline")
		}
		_, _ = fmt.Fprintln(w, header)

		served := map[string]bool{}
		for _, model := range pm.Served {
			served[model] = true
		}
		declared := map[string]bool{}
		for _, model := range pm.Declared {
			declared[model] = true
		}

		printed := false
		for _, model := range pm.Declared {
			note := ""
			if pm.Online != nil && *pm.Online && !served[model] {
				note = "  " + yellow.Sprint("(declared but not served)")
			}
			_, _ = fmt.Fprintf(w, "  %s%s\n", pm.Ref(model), note)
			printed = true
		}
		for _, model := range pm.Served {
			if declared[model] {
				continue
			}
			_, _ = fmt.Fprintf(w, "  %s  %s\n", pm.Ref(model), dim.Sprint("(served, not declared in opencode.json)"))
			printed = true
		}
		if !printed {
			_, _ = fmt.Fprintln(w, dim.Sprint("  (no models declared — discovered by OpenCode at runtime)"))
		}
	}
}

// PresetChanges writes the diff listing: one row per field-level change.
func PresetChanges(w io.Writer, changes []preset.Change) {
	maxLen := 0
	rows := make([][2]string, 0, len(changes))
	for _, c := range changes {
		field := c.Field()
		var delta string
		switch c.Op {
		case preset.OpAdd:
			delta = green.Sprint("+ ") + c.New
		case preset.OpRemove:
			delta = red.Sprint("- ") + c.Old
		default:
			delta = c.Old + dim.Sprint(" → ") + c.New
		}
		if len(field) > maxLen {
			maxLen = len(field)
		}
		rows = append(rows, [2]string{field, delta})
	}
	for _, row := range rows {
		pad := strings.Repeat(" ", maxLen-len(row[0]))
		_, _ = fmt.Fprintf(w, "  %s%s    %s\n", row[0], pad, row[1])
	}
}
