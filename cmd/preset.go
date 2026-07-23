package cmd

import (
	"fmt"
	"io"
	"os"
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

// projectDirOverride lets tests pin the --project working directory.
var projectDirOverride = ""

// scopedPresetManager returns the manager for a command's target scope:
// the profile config dir normally, or ./.opencode when --project is set.
// oh-my-openagent walks project dirs upward and the closest config wins,
// so a project-level file overrides the global one for that project only.
func scopedPresetManager(cmd *cobra.Command) (*preset.Manager, bool, error) {
	project, _ := cmd.Flags().GetBool("project")
	if !project {
		return newPresetManager(), false, nil
	}
	dir := projectDirOverride
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, false, fmt.Errorf("determine working directory: %w", err)
		}
		dir = wd
	}
	s := newStore()
	return preset.New(
		filepath.Join(s.OpmDir(), "presets"),
		filepath.Join(dir, ".opencode"),
		filepath.Join(s.OpmDir(), "backups", "presets"),
	), true, nil
}

var presetCmd = &cobra.Command{
	Use:   "preset",
	Short: "Create, capture, and switch model presets",
	Long: `Model presets are named model-mapping overlays applied onto the active
profile's oh-my-openagent config. A preset re-points every agent and
category it names at different provider/models in one command, while
prompts, permissions, and comments in the live config are preserved.

Presets themselves are not scoped to a profile — the same stored presets
(~/.config/opm/presets/) apply no matter which profile is active; a preset
just patches whatever config the currently active profile resolves to.

  opm preset set tiered sisyphus kiro/claude-opus-4-8
  opm preset use tiered

That sets the "sisyphus" agent to the "kiro/claude-opus-4-8" model inside
the "tiered" preset, then applies it. Run 'opm preset <command> --help'
for details on any individual command below, or 'opm preset --examples'
for a longer worked walkthrough covering every common task.`,
	SilenceUsage: true,
	RunE:         runPresetRoot,
}

// presetExampleSections is the content behind 'opm preset --examples' — one
// real, runnable command per line, grounded in actual agent/model/category
// names rather than <placeholder> syntax.
var presetExampleSections = []struct {
	label string
	lines []string
}{
	{"Bulk: same model everywhere", []string{
		`opm preset create opus --all kiro/claude-opus-4-8`,
	}},
	{"One agent at a time", []string{
		`opm preset set tiered sisyphus kiro/claude-opus-4-8 --variant max`,
		`opm preset set tiered oracle openai/gpt-5.5`,
	}},
	{"Category tiers — change many agents at once", []string{
		`opm preset set kiro deep kiro/claude-opus-4-8 --category`,
		`opm preset set kiro sisyphus c:deep`,
		`opm preset set kiro prometheus c:deep`,
	}},
	{"Remove an entry", []string{
		`opm preset set tiered legacy --clear`,
	}},
	{"Snapshot what's live right now", []string{
		`opm preset capture kimi`,
	}},
	{"Preview, then apply", []string{
		`opm preset diff tiered`,
		`opm preset use tiered`,
	}},
	{"Discover real model refs — never guess an ID", []string{
		`opm preset models`,
	}},
	{"Interactive picker instead of typing refs by hand", []string{
		`opm preset edit tiered`,
	}},
}

func printPresetExamples(w io.Writer) {
	for i, sec := range presetExampleSections {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		output.HelpSection(w, sec.label+":")
		for _, line := range sec.lines {
			_, _ = fmt.Fprintln(w, "  "+line)
		}
	}
}

func runPresetRoot(cmd *cobra.Command, args []string) error {
	examples, _ := cmd.Flags().GetBool("examples")
	if !examples {
		return cmd.Help()
	}
	printPresetExamples(cmd.OutOrStdout())
	return nil
}

var presetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List presets; ● marks presets matching the live config",
	Long: `Lists every stored preset (~/.config/opm/presets/), with ● marking
whichever one currently matches the live oh-my-openagent config.

Presets are not scoped to a profile — this same list shows regardless of
which profile is active, since a preset just patches whatever config the
active profile currently resolves to. If you switch profiles with
'opm use <name>', this list is unchanged; run 'opm preset status' to see
whether any preset still matches the newly active profile's config.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runPresetList,
}

var presetShowCmd = &cobra.Command{
	Use:               "show <name>",
	Short:             "Show a preset's resolved model mapping",
	Args:              requireArgs(1, "opm preset show <name>"),
	ValidArgsFunction: singleArgPresetCompletion,
	SilenceUsage:      true,
	RunE:              runPresetShow,
}

var presetUseCmd = &cobra.Command{
	Use:               "use <name>",
	Short:             "Apply a preset to the live oh-my-openagent config",
	Args:              requireArgs(1, "opm preset use <name>"),
	ValidArgsFunction: singleArgPresetCompletion,
	SilenceUsage:      true,
	RunE:              runPresetUse,
}

var presetDiffCmd = &cobra.Command{
	Use:   "diff <name>",
	Short: "Show what 'preset use' would change, without applying",
	Long: `Shows the field-level changes applying the preset would make, without
touching any file.

With --files <dir>, additionally renders every file the apply would touch
into <dir>/before/ and <dir>/after/ trees for external diff tools:

  opm preset diff local --files /tmp/pd && difft /tmp/pd/before /tmp/pd/after`,
	Args:              requireArgs(1, "opm preset diff <name>"),
	ValidArgsFunction: singleArgPresetCompletion,
	SilenceUsage:      true,
	RunE:              runPresetDiff,
}

var presetCaptureCmd = &cobra.Command{
	Use:          "capture <name>",
	Short:        "Snapshot the live model assignments into a new preset",
	Args:         requireArgs(1, "opm preset capture <name>"),
	SilenceUsage: true,
	RunE:         runPresetCapture,
}

var presetModelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List available models per provider (live-queries local servers)",
	Long: `Lists every model reference you can use in a preset, grouped by the
providers declared in the profile's opencode.json.

Loopback-hosted providers (Ollama, LM Studio, omlx, ...) are queried live
via their /models endpoint, so the listing shows what the server actually
has loaded right now. Remote providers list their declared models only.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runPresetModels,
}

var presetCreateCmd = &cobra.Command{
	Use:   "create <name> --all <provider/model>",
	Short: "Generate a preset assigning one model to every agent and category",
	Long: `Generates a preset that points every agent and category entry in the
live oh-my-openagent config at a single model — the "set everything to X"
one-liner:

  opm preset create opus --all anthropic/claude-opus-4-8
  opm preset use opus

Discover valid model references with 'opm preset models'. To snapshot the
current mixed assignments instead, use 'opm preset capture'.`,
	Args:         requireArgs(1, "opm preset create <name> --all <provider/model>"),
	SilenceUsage: true,
	RunE:         runPresetCreate,
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
	presetCmd.Flags().Bool("examples", false, "Show worked examples for common preset tasks")

	presetCaptureCmd.Flags().Bool("force", false, "Overwrite an existing preset with the same name")
	presetUseCmd.Flags().Bool("force", false, "Apply even when model validation or endpoint probes fail")
	presetUseCmd.Flags().Bool("project", false, "Apply to ./.opencode/ as a project-level override instead of the profile config")
	presetDiffCmd.Flags().Bool("project", false, "Diff against ./.opencode/ instead of the profile config")
	presetDiffCmd.Flags().String("files", "", "Render before/ and after/ file trees into this directory for external diff tools")

	presetCreateCmd.Flags().String("all", "", "provider/model to assign to every agent and category, e.g. anthropic/claude-opus-4-8")
	presetCreateCmd.Flags().Bool("force", false, "Overwrite an existing preset with the same name")

	presetCmd.AddCommand(presetListCmd, presetShowCmd, presetUseCmd, presetDiffCmd,
		presetCaptureCmd, presetCreateCmd, presetModelsCmd, presetStatusCmd, presetRevertCmd)

	// Deliberate workflow order for the root help's flattened "preset <verb>"
	// rows: discover what's available, inspect what exists, author a
	// preset, preview the change, apply it, undo if needed. Left to Go's
	// init()-order-across-files default, 'edit'/'set' (registered in
	// separate files) would land awkwardly at the very end.
	markRootHelpOrder(presetModelsCmd, 10)
	markRootHelpOrder(presetListCmd, 20)
	markRootHelpOrder(presetShowCmd, 30)
	markRootHelpOrder(presetStatusCmd, 40)
	markRootHelpOrder(presetCaptureCmd, 50)
	markRootHelpOrder(presetCreateCmd, 60)
	markRootHelpOrder(presetSetCmd, 70)
	markRootHelpOrder(presetEditCmd, 80)
	markRootHelpOrder(presetDiffCmd, 90)
	markRootHelpOrder(presetUseCmd, 100)
	markRootHelpOrder(presetRevertCmd, 110)

	markRootHelpGroup(presetCmd, helpGroupPresets)
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
	m, project, err := scopedPresetManager(cmd)
	if err != nil {
		return err
	}
	p, err := m.Resolve(args[0])
	if err != nil {
		return err
	}
	if p.EntryCount() == 0 && len(p.Opencode) == 0 {
		return fmt.Errorf("preset %q resolves to no agent, category, or opencode entries", p.Name)
	}
	if project && len(p.Opencode) > 0 {
		output.Warning(cmd.ErrOrStderr(), "opencode block skipped in --project mode",
			"top-level opencode.json fields are only applied to the profile config")
	}

	force, _ := cmd.Flags().GetBool("force")
	// Validate against the profile's opencode.json even in project mode —
	// provider declarations are global, not per-project.
	if err := checkPreset(cmd, newPresetManager(), p, force); err != nil {
		return err
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
	m, _, err := scopedPresetManager(cmd)
	if err != nil {
		return err
	}
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

	filesDir, _ := cmd.Flags().GetString("files")
	if filesDir != "" {
		rendered, err := m.RenderFiles(p)
		if err != nil {
			return err
		}
		beforeDir, afterDir, err := preset.WriteRenderDir(filesDir, rendered)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout())
		output.Success(cmd.OutOrStdout(),
			fmt.Sprintf("Rendered %d file(s) for external diff", len(rendered)),
			output.ShortenHome(beforeDir),
			output.ShortenHome(afterDir),
			"e.g. difft "+output.ShortenHome(beforeDir)+" "+output.ShortenHome(afterDir))
	}
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

func runPresetModels(cmd *cobra.Command, args []string) error {
	m := newPresetManager()
	providers, err := m.ListModels()
	if err != nil {
		return err
	}
	if len(providers) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No providers declared in opencode.json.")
		return nil
	}
	output.ProviderModelsTable(cmd.OutOrStdout(), providers)
	return nil
}

func runPresetCreate(cmd *cobra.Command, args []string) error {
	m := newPresetManager()
	ref, _ := cmd.Flags().GetString("all")
	if ref == "" {
		return fmt.Errorf("--all <provider/model> is required — or snapshot the current assignments with 'opm preset capture'")
	}
	force, _ := cmd.Flags().GetBool("force")

	p, path, err := m.CreateAll(args[0], ref, force)
	if err != nil {
		return err
	}

	// Non-blocking heads-up if the ref looks wrong; use still validates.
	if issues, err := m.ValidateRefs(p); err == nil {
		for _, issue := range issues {
			output.Warning(cmd.ErrOrStderr(), issue.Message)
		}
	}

	output.Success(cmd.OutOrStdout(), "Created preset "+output.ProfileName(p.Name),
		fmt.Sprintf("%d agent(s), %d category(ies) → %s", len(p.Agents), len(p.Categories), ref),
		output.ShortenHome(path),
		"apply with 'opm preset use "+p.Name+"'")
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
	restored, backupName, err := m.Revert()
	if err != nil {
		return err
	}
	details := make([]string, 0, len(restored)+2)
	for _, path := range restored {
		details = append(details, output.ShortenHome(path))
	}
	details = append(details, "restart OpenCode sessions to pick up the change")
	output.Success(cmd.OutOrStdout(),
		fmt.Sprintf("Restored %d file(s) from backup %s", len(restored), backupName), details...)
	return nil
}

// checkPreset validates model refs against the profile's opencode.json and
// probes loopback provider endpoints. Warnings are printed; failures block
// the apply unless force is set.
func checkPreset(cmd *cobra.Command, m *preset.Manager, p *preset.Preset, force bool) error {
	issues, err := m.ValidateRefs(p)
	if err != nil {
		return err
	}
	probeIssues, err := m.ProbeEndpoints(p)
	if err != nil {
		return err
	}
	issues = append(issues, probeIssues...)

	for _, issue := range issues {
		switch issue.Severity {
		case preset.SeverityFail:
			output.Failure(cmd.ErrOrStderr(), issue.Message)
		default:
			output.Warning(cmd.ErrOrStderr(), issue.Message)
		}
	}
	if preset.HasFailures(issues) && !force {
		return fmt.Errorf("preset %q failed validation — fix the issues above or apply anyway with --force", p.Name)
	}
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
