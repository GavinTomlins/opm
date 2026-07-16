package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tbcrawford/opm/internal/output"
	"github.com/tbcrawford/opm/internal/preset"
)

var presetSetCmd = &cobra.Command{
	Use:   "set <name> <entry> <model|c:category>",
	Short: "Set or clear a single agent/category entry in a preset, non-interactively",
	Long: `Adds or updates one agent or category entry in a preset with no
interactive prompts — the scriptable building block for constructing a
mixed preset from the command line, or for an agent framework driving opm
on a user's behalf in response to a plain-English instruction. Creates the
preset if it doesn't exist yet.

  opm preset set tiered sisyphus kiro/claude-opus-4-7 --variant max
  opm preset set tiered explore c:quick
  opm preset set tiered quick ollama/llama3.1:8b --category
  opm preset set tiered legacy --clear

By default <entry> names an agent; pass --category to target a category
instead — the two namespaces can have entries with the same name.`,
	Args:              cobra.RangeArgs(2, 3),
	ValidArgsFunction: presetSetCompletion,
	SilenceUsage:      true,
	RunE:              runPresetSet,
}

func init() {
	presetSetCmd.Flags().Bool("category", false, "Target a category entry instead of an agent")
	presetSetCmd.Flags().String("variant", "", "Set the variant tuning key alongside the model")
	presetSetCmd.Flags().Bool("clear", false, "Remove the entry instead of setting it")
	presetCmd.AddCommand(presetSetCmd)
}

// resolveSetRef parses the <model|c:category> argument. Deliberately
// narrower than the wizard's parseEntryInput: a scriptable command's
// positional argument should have exactly two literal forms, not a
// vocabulary of magic words like "clear"/"list"/blank.
func resolveSetRef(ref string, allowCategoryRoute bool) (preset.Entry, error) {
	if strings.HasPrefix(ref, "c:") {
		if !allowCategoryRoute {
			return nil, fmt.Errorf("category routing is only valid for agent entries")
		}
		category := strings.TrimSpace(strings.TrimPrefix(ref, "c:"))
		if category == "" {
			return nil, fmt.Errorf(`category name required after "c:"`)
		}
		raw, _ := json.Marshal(category)
		return preset.Entry{"category": raw}, nil
	}
	if !strings.Contains(ref, "/") {
		return nil, fmt.Errorf("model %q must use provider/model form, or \"c:<category>\" for category routing", ref)
	}
	raw, _ := json.Marshal(ref)
	return preset.Entry{"model": raw}, nil
}

func runPresetSet(cmd *cobra.Command, args []string) error {
	name, entryName := args[0], args[1]
	isCategory, _ := cmd.Flags().GetBool("category")
	clear, _ := cmd.Flags().GetBool("clear")
	variant, _ := cmd.Flags().GetString("variant")

	if clear && len(args) == 3 {
		return fmt.Errorf("--clear does not take a model argument")
	}
	if !clear && len(args) != 3 {
		return fmt.Errorf("a model (provider/model or c:<category>) is required unless --clear is set")
	}

	m := newPresetManager()
	p := &preset.Preset{Name: name, Agents: map[string]preset.Entry{}, Categories: map[string]preset.Entry{}}
	created := true
	if existing, err := m.Load(name); err == nil {
		created = false
		p.Description, p.Extends = existing.Description, existing.Extends
		for k, v := range existing.Agents {
			p.Agents[k] = v
		}
		for k, v := range existing.Categories {
			p.Categories[k] = v
		}
	}

	section, sectionLabel := p.Agents, "agents"
	if isCategory {
		section, sectionLabel = p.Categories, "categories"
	}

	var summary string
	if clear {
		delete(section, entryName)
		summary = fmt.Sprintf("%s.%s removed", sectionLabel, entryName)
	} else {
		entry, err := resolveSetRef(args[2], !isCategory)
		if err != nil {
			return err
		}
		if variant != "" {
			raw, _ := json.Marshal(variant)
			entry["variant"] = raw
		}
		section[entryName] = entry
		summary = fmt.Sprintf("%s.%s = %s", sectionLabel, entryName, entry.Summary())
	}

	if issues, err := m.ValidateRefs(p); err == nil {
		for _, issue := range issues {
			output.Warning(cmd.ErrOrStderr(), issue.Message)
		}
	}

	path, err := m.Save(p, true)
	if err != nil {
		return err
	}
	verb := "Updated"
	if created {
		verb = "Created"
	}
	output.Success(cmd.OutOrStdout(), fmt.Sprintf("%s preset %s: %s", verb, output.ProfileName(name), summary),
		output.ShortenHome(path))
	return nil
}

// presetSetCompletion completes the preset name (position 0) and live
// agent/category names (position 1).
func presetSetCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completePresetNames(cmd, args, toComplete)
	case 1:
		entries, err := newPresetManager().LiveEntries()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}
