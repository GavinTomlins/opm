package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/tbcrawford/opm/internal/output"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect <name>",
	Short: "Show detailed information about a profile",
	Long: `Shows a profile's active status, path, and full directory contents.

  opm inspect work

Run 'opm list' first to see available profile names.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("requires a profile name — run 'opm list' to see available profiles, then 'opm inspect <name>'")
		}
		return nil
	},
	PreRunE:           managedGuard,
	ValidArgsFunction: singleArgProfileCompletion,
	SilenceUsage:      true,
	RunE:              runInspect,
}

func init() {
	markRootHelpGroup(inspectCmd, helpGroupProfiles)
	markRootHelpOrder(inspectCmd, 60)
	rootCmd.AddCommand(inspectCmd)
}

func runInspect(cmd *cobra.Command, args []string) error {
	name := args[0]
	s := newStore()

	profilePath, err := s.GetProfile(name)
	if err != nil {
		return err
	}

	active, _ := s.ActiveProfile()
	isActive := active == name

	entries, err := os.ReadDir(profilePath)
	if err != nil {
		return fmt.Errorf("read profile contents: %w", err)
	}

	output.InspectProfile(cmd.OutOrStdout(), name, profilePath, isActive, entries)
	return nil
}
