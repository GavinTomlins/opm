package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/tbcrawford/opm/internal/output"
	"github.com/tbcrawford/opm/internal/preset"
)

// newPresetManager derives preset paths from the store so the cmd test
// harness's storeFactory injection wires presets to temp dirs unchanged.
var presetManagerFactory = func() *preset.Manager {
	s := newStore()
	return preset.New(
		filepath.Join(s.OpmDir(), "presets"),
		s.OpencodeDir(),
		filepath.Join(s.OpmDir(), "backups", "presets"),
	)
}

func newPresetManager() *preset.Manager { return presetManagerFactory() }

var presetCmd = &cobra.Command{
	Use:   "preset",
	Short: "Manage model presets",
	Long: "Model presets are named model-mapping overlays applied onto the active\n" +
		"profile's oh-my-openagent config. A preset re-points every agent and\n" +
		"category it names at different provider/models in one command, while\n" +
		"prompts, permissions, and comments in the live config are preserved.",
	SilenceUsage: true,
}

var presetListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List presets; ● marks presets matching the live config",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runPresetList,
}

var presetShowCmd = &cobra.Command{
	Use:               "show <name>",
	Short:             "Show a preset's resolved model mapping",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: singleArgPresetCompletion,
	SilenceUsage:      true,
	RunE:              runPresetShow,
}

var presetUseCmd = &cobra.Command{
	Use:               "use <name>",
	Short:             "Apply a preset to the live oh-my-openagent config",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: singleArgPresetCompletion,
	SilenceUsage:      true,
	RunE:              runPresetUse,
}

var presetDiffCmd = &cobra.Command{
	Use:               "diff <name>",
	Short:             "Show what 'preset use' would change, without applying",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: singleArgPresetCompletion,
	SilenceUsage:      true,
	RunE:              runPresetDiff,
}

var presetCaptureCmd = &cobra.Command{
	Use:          "capture <name>",
	Short:        "Snapshot the live model assignments into a new preset",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runPresetCapture,
}

var presetStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Report which preset the live config matches",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runPresetStatus,
}

var presetRevertCmd = &cobra.Command{
	Use:          "revert",
	Short:        "Restore the live config from the most recent preset backup",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runPresetRevert,
}

func init() {
	presetCaptureCmd.Flags().Bool("force", false, "Overwrite an existing preset with the same name")

	presetCmd.AddCommand(presetListCmd, presetShowCmd, presetUseCmd, presetDiffCmd,
		presetCaptureCmd, presetStatusCmd, presetRevertCmd)

	markRootHelpGroup(presetCmd, helpGroupPresets)
	markRootHelpOrder(presetCmd, 10)
	rootCmd.AddCommand(presetCmd)
}

// matchingPresets returns the names of presets whose Diff against the live
// config is empty. Presets that fail to resolve or diff are skipped.
func matchingPresets(m *preset.Manager, infos []preset.Info) map[string]bool {
	matches := map[string]bool{}
	for _, info := range infos {
		if info.Err != nil {
			continue
		}
		p, err := m.Resolve(info.Name)
		if err != nil || p.EntryCount() == 0 {
			continue
		}
		changes, err := m.Diff(p)
		if err == nil && len(changes) == 0 {
			matches[info.Name] = true
		}
	}
	return matches
}

func runPresetList(cmd *cobra.Command, args []string) error {
	m := newPresetManager()
	infos, err := m.List()
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No presets found.")
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Capture the current model assignments with 'opm preset capture <name>'.")
		return nil
	}

	matches := matchingPresets(m, infos)
	output.PresetTable(cmd.OutOrStdout(), infos, matches)
	return nil
}

func runPresetShow(cmd *cobra.Command, args []string) error {
	m := newPresetManager()
	p, err := m.Resolve(args[0])
	if err != nil {
		return err
	}
	output.PresetDetails(cmd.OutOrStdout(), p)
	return nil
}

func runPresetUse(cmd *cobra.Command, args []string) error {
	m := newPresetManager()
	p, err := m.Resolve(args[0])
	if err != nil {
		return err
	}
	if p.EntryCount() == 0 {
		return fmt.Errorf("preset %q resolves to no agent or category entries", p.Name)
	}

	result, err := m.Apply(p)
	if err != nil {
		return err
	}

	warnShadowedCandidates(cmd, result.Others, result.LivePath)

	switch {
	case len(result.Changes) == 0:
		output.Success(cmd.OutOrStdout(), "Live config already matches preset "+output.ProfileName(p.Name))
	case result.Created:
		output.Success(cmd.OutOrStdout(), "Applied preset "+output.ProfileName(p.Name),
			"created "+output.ShortenHome(result.LivePath),
			"restart OpenCode sessions to pick up the change")
	default:
		details := []string{
			fmt.Sprintf("%d change(s) to %s", len(result.Changes), output.ShortenHome(result.LivePath)),
			"backup: " + output.ShortenHome(result.BackupPath),
			"restart OpenCode sessions to pick up the change",
		}
		output.Success(cmd.OutOrStdout(), "Applied preset "+output.ProfileName(p.Name), details...)
	}
	return nil
}

func runPresetDiff(cmd *cobra.Command, args []string) error {
	m := newPresetManager()
	p, err := m.Resolve(args[0])
	if err != nil {
		return err
	}
	changes, err := m.Diff(p)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		output.Success(cmd.OutOrStdout(), "No changes — live config matches preset "+output.ProfileName(p.Name))
		return nil
	}
	output.PresetChanges(cmd.OutOrStdout(), changes)
	return nil
}

func runPresetCapture(cmd *cobra.Command, args []string) error {
	m := newPresetManager()
	force, _ := cmd.Flags().GetBool("force")

	p, path, err := m.Capture(args[0], force)
	if err != nil {
		return err
	}
	output.Success(cmd.OutOrStdout(), "Captured preset "+output.ProfileName(p.Name),
		fmt.Sprintf("%d agent(s), %d category(ies)", len(p.Agents), len(p.Categories)),
		output.ShortenHome(path))
	return nil
}

func runPresetStatus(cmd *cobra.Command, args []string) error {
	m := newPresetManager()
	infos, err := m.List()
	if err != nil {
		return err
	}
	lf, err := m.LiveFile()
	if err != nil {
		return err
	}

	warnShadowedCandidates(cmd, lf.Others, lf.Path)

	if !lf.Exists {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No oh-my-openagent config found at "+output.ShortenHome(lf.Path)+".")
		return nil
	}

	matches := matchingPresets(m, infos)
	if len(matches) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No preset matches "+output.ShortenHome(lf.Path)+".")
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Capture the current state with 'opm preset capture <name>'.")
		return nil
	}
	for _, info := range infos {
		if matches[info.Name] {
			output.Success(cmd.OutOrStdout(), "Live config matches preset "+output.ProfileName(info.Name))
		}
	}
	return nil
}

func runPresetRevert(cmd *cobra.Command, args []string) error {
	m := newPresetManager()
	restoredTo, backupName, err := m.Revert()
	if err != nil {
		return err
	}
	output.Success(cmd.OutOrStdout(), "Restored "+output.ShortenHome(restoredTo),
		"from backup "+backupName,
		"restart OpenCode sessions to pick up the change")
	return nil
}

// warnShadowedCandidates warns when multiple oh-my-openagent config files
// exist: only the winning one is loaded by the framework (and patched by opm).
func warnShadowedCandidates(cmd *cobra.Command, others []string, winner string) {
	if len(others) == 0 {
		return
	}
	details := make([]string, 0, len(others)+1)
	for _, o := range others {
		details = append(details, output.ShortenHome(o)+" is shadowed and never loaded")
	}
	details = append(details, "oh-my-openagent loads (and opm patches) "+output.ShortenHome(winner))
	output.Warning(cmd.ErrOrStderr(), "Multiple oh-my-openagent config files found", details...)
}

// completePresetNames returns preset names for shell completion.
func completePresetNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	infos, err := newPresetManager().List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// singleArgPresetCompletion completes the first argument with preset names.
func singleArgPresetCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completePresetNames(cmd, args, toComplete)
}
