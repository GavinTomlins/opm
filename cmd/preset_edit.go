package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tbcrawford/opm/internal/output"
	"github.com/tbcrawford/opm/internal/preset"
)

var presetEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Interactively assign models to agents and categories",
	Long: `Walks through every agent and category in the live oh-my-openagent
config, shows the available models (declared providers plus a live query of
local servers), and lets you pick one per entry. Creates the preset if it
doesn't exist yet, or updates it in place.

At each prompt: a number picks a model from the catalog, a typed
provider/model ref is used directly, "c:<category>" routes an agent through
a category instead of a direct model, "clear" removes the entry from the
preset, "list" reprints the catalog, and a blank line skips the entry.`,
	Args:         requireArgs(1, "opm preset edit <name>"),
	SilenceUsage: true,
	RunE:         runPresetEdit,
}

func init() {
	presetCmd.AddCommand(presetEditCmd)
}

// catalogLine is one numbered, selectable entry in the interactive model
// picker.
type catalogLine struct {
	Number          int
	Provider        string
	Ref             string
	Note            string
	ProviderOffline bool
}

// buildCatalog flattens ListModels output into a numbered catalog: declared
// models first, then served-but-undeclared models (deduplicated), in
// provider order. Pure and side-effect-free so it's directly testable.
func buildCatalog(providers []preset.ProviderModels) []catalogLine {
	var lines []catalogLine
	n := 1
	for _, pm := range providers {
		offline := pm.Online != nil && !*pm.Online
		declared := map[string]bool{}
		for _, model := range pm.Declared {
			declared[model] = true
			lines = append(lines, catalogLine{Number: n, Provider: pm.Provider, Ref: pm.Ref(model), ProviderOffline: offline})
			n++
		}
		for _, model := range pm.Served {
			if declared[model] {
				continue
			}
			lines = append(lines, catalogLine{
				Number: n, Provider: pm.Provider, Ref: pm.Ref(model),
				Note: "served, not declared", ProviderOffline: offline,
			})
			n++
		}
	}
	return lines
}

// printCatalog writes the numbered catalog grouped by provider.
func printCatalog(w io.Writer, lines []catalogLine) {
	lastProvider := ""
	for _, line := range lines {
		if line.Provider != lastProvider {
			_, _ = fmt.Fprintln(w)
			header := output.ProfileName(line.Provider)
			if line.ProviderOffline {
				header += "  " + "(offline — local server not responding)"
			}
			_, _ = fmt.Fprintln(w, header)
			lastProvider = line.Provider
		}
		note := ""
		if line.Note != "" {
			note = "  (" + line.Note + ")"
		}
		_, _ = fmt.Fprintf(w, "  %2d) %s%s\n", line.Number, line.Ref, note)
	}
}

// entryAction is what parseEntryInput decided a prompt response means.
type entryAction int

const (
	actionKeep entryAction = iota
	actionClear
	actionRelist
	actionSetModel
	actionSetCategory
	actionInvalid
)

// entryChoice is the parsed result of one prompt response.
type entryChoice struct {
	Action   entryAction
	Ref      string
	Category string
	ErrMsg   string
}

// parseEntryInput interprets one line of user input against the catalog.
// Pure and side-effect-free so it's directly testable without stdin/stdout.
func parseEntryInput(input string, catalog []catalogLine, allowCategory bool) entryChoice {
	input = strings.TrimSpace(input)
	switch {
	case input == "":
		return entryChoice{Action: actionKeep}
	case strings.EqualFold(input, "clear"):
		return entryChoice{Action: actionClear}
	case strings.EqualFold(input, "list"):
		return entryChoice{Action: actionRelist}
	case strings.HasPrefix(input, "c:"):
		if !allowCategory {
			return entryChoice{Action: actionInvalid, ErrMsg: "category routing is only valid for agents"}
		}
		category := strings.TrimSpace(strings.TrimPrefix(input, "c:"))
		if category == "" {
			return entryChoice{Action: actionInvalid, ErrMsg: `category name required after "c:"`}
		}
		return entryChoice{Action: actionSetCategory, Category: category}
	default:
		if n, err := strconv.Atoi(input); err == nil {
			for _, line := range catalog {
				if line.Number == n {
					return entryChoice{Action: actionSetModel, Ref: line.Ref}
				}
			}
			return entryChoice{Action: actionInvalid, ErrMsg: fmt.Sprintf("no model numbered %d", n)}
		}
		if strings.Contains(input, "/") {
			return entryChoice{Action: actionSetModel, Ref: input}
		}
		return entryChoice{Action: actionInvalid, ErrMsg: `enter a number from the catalog, a provider/model ref, "c:<category>", "clear", "list", or leave blank to skip`}
	}
}

// foldInPresetOnlyEntries appends entries present in p but not in the live
// config (e.g. left over after a profile switch) so the wizard offers them
// for review/clearing instead of silently leaving them untouched forever.
func foldInPresetOnlyEntries(entries []preset.LiveEntry, p *preset.Preset) []preset.LiveEntry {
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Section+"\x00"+e.Name] = true
	}
	for name := range p.Agents {
		if !seen["agents\x00"+name] {
			entries = append(entries, preset.LiveEntry{Name: name, Section: "agents"})
		}
	}
	for name := range p.Categories {
		if !seen["categories\x00"+name] {
			entries = append(entries, preset.LiveEntry{Name: name, Section: "categories"})
		}
	}
	return entries
}

func runPresetEdit(cmd *cobra.Command, args []string) error {
	name := args[0]
	m := newPresetManager()
	out := cmd.OutOrStdout()

	providers, err := m.ListModels()
	if err != nil {
		return err
	}
	catalog := buildCatalog(providers)
	if len(catalog) == 0 {
		return fmt.Errorf("no models available to choose from — declare a provider in opencode.json first")
	}

	p := &preset.Preset{Name: name, Agents: map[string]preset.Entry{}, Categories: map[string]preset.Entry{}}
	if existing, loadErr := m.Load(name); loadErr == nil {
		p.Description, p.Extends = existing.Description, existing.Extends
		for k, v := range existing.Agents {
			p.Agents[k] = v
		}
		for k, v := range existing.Categories {
			p.Categories[k] = v
		}
		output.Success(out, "Editing existing preset "+output.ProfileName(name))
	} else {
		_, _ = fmt.Fprintln(out, "Creating new preset "+output.ProfileName(name))
	}

	entries, err := m.LiveEntries()
	if err != nil {
		return err
	}
	entries = foldInPresetOnlyEntries(entries, p)

	printCatalog(out, catalog)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, `At each prompt: a number picks a model, "c:<category>" routes an agent via`)
	_, _ = fmt.Fprintln(out, `a category, "clear" removes the entry, "list" reprints the catalog, blank skips.`)

	reader := bufio.NewScanner(cmd.InOrStdin())

	_, _ = fmt.Fprintf(out, "\nDescription [%s]: ", p.Description)
	if reader.Scan() {
		if d := strings.TrimSpace(reader.Text()); d != "" {
			p.Description = d
		}
	}

	for _, le := range entries {
		section := p.Agents
		if le.Section == "categories" {
			section = p.Categories
		}

		for {
			current := section[le.Name]
			if current == nil {
				current = le.Entry
			}
			_, _ = fmt.Fprintln(out)
			kind := "agent"
			if le.Section == "categories" {
				kind = "category"
			}
			if summary := current.Summary(); summary != "" {
				_, _ = fmt.Fprintf(out, "%s (%s) — current: %s\n", le.Name, kind, summary)
			} else {
				_, _ = fmt.Fprintf(out, "%s (%s) — not set\n", le.Name, kind)
			}
			_, _ = fmt.Fprint(out, "> ")

			if !reader.Scan() {
				return fmt.Errorf("input ended unexpectedly")
			}
			choice := parseEntryInput(reader.Text(), catalog, le.Section == "agents")

			switch choice.Action {
			case actionKeep:
			case actionClear:
				delete(section, le.Name)
			case actionRelist:
				printCatalog(out, catalog)
				continue
			case actionInvalid:
				output.Warning(cmd.ErrOrStderr(), choice.ErrMsg)
				continue
			case actionSetModel, actionSetCategory:
				entry := preset.Entry{}
				if choice.Action == actionSetModel {
					entry["model"], _ = json.Marshal(choice.Ref)
				} else {
					entry["category"], _ = json.Marshal(choice.Category)
				}
				_, _ = fmt.Fprint(out, "variant (blank for none): ")
				if reader.Scan() {
					if v := strings.TrimSpace(reader.Text()); v != "" {
						entry["variant"], _ = json.Marshal(v)
					}
				}
				section[le.Name] = entry
			}
			break
		}
	}

	_, _ = fmt.Fprintln(out)
	output.PresetDetails(out, p)

	if issues, valErr := m.ValidateRefs(p); valErr == nil {
		for _, issue := range issues {
			output.Warning(cmd.ErrOrStderr(), issue.Message)
		}
	}

	_, _ = fmt.Fprintf(out, "\nSave as %q? [Y/n] ", name)
	save := true
	if reader.Scan() {
		ans := strings.ToLower(strings.TrimSpace(reader.Text()))
		save = ans == "" || ans == "y" || ans == "yes"
	}
	if !save {
		_, _ = fmt.Fprintln(out, "Aborted — nothing saved.")
		return nil
	}

	path, err := m.Save(p, true)
	if err != nil {
		return err
	}
	output.Success(out, "Saved preset "+output.ProfileName(name),
		output.ShortenHome(path),
		"review with 'opm preset diff "+name+"', apply with 'opm preset use "+name+"'")
	return nil
}
