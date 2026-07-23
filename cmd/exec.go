package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/tbcrawford/opm/internal/preset"
)

var execCmd = &cobra.Command{
	Use:   "exec <profile> [-- command [args...]]",
	Short: "Run a command with a specific profile active",
	Long: `Run opencode (or any command) using the named profile, without changing
the global active profile. The spawned process reads its opencode
configuration from the given profile directory.

With --preset, the session uses an ephemeral copy of the profile with the
named model preset applied — the real profile is never modified.

If no command is provided, opencode is launched.

Examples:
  opm exec work
  opm exec work --preset local
  opm exec personal -- opencode --no-auto-update
  opm exec ci -- opencode run "fix the tests"`,
	Args:              cobra.MinimumNArgs(1),
	ValidArgsFunction: execCompletion,
	SilenceUsage:      true,
	RunE:              runExec,
}

func init() {
	execCmd.Flags().String("preset", "", "Apply a model preset to an ephemeral copy of the profile for this session")
	execCmd.Flags().Bool("force", false, "Apply the preset even when validation or endpoint probes fail")
	_ = execCmd.RegisterFlagCompletionFunc("preset", completePresetNames)
	markRootHelpGroup(execCmd, helpGroupProfiles)
	markRootHelpOrder(execCmd, 35)
	rootCmd.AddCommand(execCmd)
}

// execCompletion completes the first argument (profile name) and offers no
// completions for subsequent arguments (those are passed to the child command).
func execCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		// After the profile name, defer to the shell's own completion.
		return nil, cobra.ShellCompDirectiveDefault
	}
	return completeProfileNames(args, 1, false)
}

func runExec(cmd *cobra.Command, args []string) error {
	profileName := args[0]

	// Remaining args (after optional "--") are the command + arguments to run.
	// If none supplied, default to opencode.
	childArgs := args[1:]
	if len(childArgs) == 0 {
		childArgs = []string{"opencode"}
	}

	s := newStore()
	profileDir, err := s.GetProfile(profileName)
	if err != nil {
		return err
	}

	// Build an ephemeral XDG config root:
	//   <tmpdir>/opencode -> profileDir   (symlink)
	// Setting XDG_CONFIG_HOME=<tmpdir> makes opencode read from profileDir.
	tmpDir, err := os.MkdirTemp("", "opm-ephemeral-*")
	if err != nil {
		return fmt.Errorf("create ephemeral environment: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	opencodeLink := filepath.Join(tmpDir, "opencode")
	presetName, _ := cmd.Flags().GetString("preset")
	if presetName == "" {
		if err := os.Symlink(profileDir, opencodeLink); err != nil {
			return fmt.Errorf("create ephemeral symlink: %w", err)
		}
	} else if err := applyEphemeralPreset(cmd, profileDir, opencodeLink, tmpDir, presetName); err != nil {
		return err
	}

	child := exec.Command(childArgs[0], childArgs[1:]...) //nolint:gosec
	child.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpDir)
	child.Stdin = os.Stdin
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()

	if err := child.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Propagate the child's exit code without printing a redundant error.
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("exec: %w", err)
	}
	return nil
}

// applyEphemeralPreset materializes a copy-on-write overlay of the profile
// at overlayDir and applies the named preset to it. The real profile — and
// any repos its symlinks point into — is never modified; backups land in
// the ephemeral temp dir and vanish with it.
func applyEphemeralPreset(cmd *cobra.Command, profileDir, overlayDir, tmpDir, presetName string) error {
	if err := preset.OverlayProfile(profileDir, overlayDir); err != nil {
		return fmt.Errorf("build ephemeral profile: %w", err)
	}

	s := newStore()
	pm := preset.New(
		filepath.Join(s.OpmDir(), "presets"),
		overlayDir,
		filepath.Join(tmpDir, "preset-backups"),
	)
	p, err := pm.Resolve(presetName)
	if err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool("force")
	if err := checkPreset(cmd, pm, p, force); err != nil {
		return err
	}
	if _, err := pm.Apply(p); err != nil {
		return fmt.Errorf("apply preset %q: %w", presetName, err)
	}
	return nil
}
