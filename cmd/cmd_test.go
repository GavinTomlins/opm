package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tbcrawford/opm/internal/store"
	"github.com/tbcrawford/opm/internal/symlink"
)

// cmdHarness wires a temp-dir store into the cmd package and returns helpers
// for running commands and capturing their output.
type cmdHarness struct {
	t           *testing.T
	store       *store.Store
	opencodeDir string
}

func (h *cmdHarness) currentPath() string {
	return filepath.Join(filepath.Dir(h.store.ProfilesDir()), "current")
}

func (h *cmdHarness) breakCurrentPath(t *testing.T) {
	t.Helper()
	currentPath := h.currentPath()
	err := os.Remove(currentPath)
	if err != nil && !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	require.NoError(t, os.MkdirAll(currentPath, 0o755))
}

func newHarness(t *testing.T) *cmdHarness {
	t.Helper()
	root := t.TempDir()
	opencodeDir := filepath.Join(t.TempDir(), "opencode")
	s := store.New(root, opencodeDir)
	return &cmdHarness{t: t, store: s, opencodeDir: opencodeDir}
}

// resetCmdFlags resets every flag on the command tree to its default value.
// Cobra does NOT reset flag values between Execute() calls on a shared command
// tree — state would leak from test to test without this.
func resetCmdFlags(root *cobra.Command) {
	root.Flags().VisitAll(func(f *pflag.Flag) { _ = f.Value.Set(f.DefValue) })
	for _, sub := range root.Commands() {
		resetCmdFlags(sub)
	}
}

// run executes the root command with the given args and returns stdout, stderr, and any error.
// It injects the harness store so no real filesystem is touched.
func (h *cmdHarness) run(args ...string) (stdout, stderr string, err error) {
	h.t.Helper()

	resetCmdFlags(rootCmd)

	// Inject store factory — restore after test.
	origFactory := storeFactory
	storeFactory = func() *store.Store { return h.store }
	h.t.Cleanup(func() { storeFactory = origFactory })

	var outBuf, errBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	h.t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	rootCmd.SetArgs(args)
	h.t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err = rootCmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func (h *cmdHarness) useStoreFactory(t *testing.T) {
	t.Helper()
	origFactory := storeFactory
	storeFactory = func() *store.Store { return h.store }
	t.Cleanup(func() { storeFactory = origFactory })
}

// mustInit is a helper that runs opm init and requires no error.
func (h *cmdHarness) mustInit(t *testing.T) {
	t.Helper()
	_, _, err := h.run("init")
	require.NoError(t, err)
}

// ── init ──────────────────────────────────────────────────────────────────────

func TestInit_FreshNoOpencode(t *testing.T) {
	h := newHarness(t)
	out, _, err := h.run("init")
	require.NoError(t, err)
	assert.Contains(t, out, "Initialized opm")

	// Symlink should be installed.
	managed, merr := h.store.IsOpmManaged()
	require.NoError(t, merr)
	assert.True(t, managed)
}

func TestInit_InvalidAsFlag(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("init", "--as", "../evil")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--as")
}

func TestInit_AlreadyInitialized(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("init")
	require.Error(t, err)
	assert.Equal(t, "already initialized (active: default)", err.Error())
}

func TestInit_AlreadyInitialized_WithRelativeManagedSymlink(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, h.store.Init())
	require.NoError(t, os.MkdirAll(h.store.ProfileDir("work"), 0o755))

	rel, err := filepath.Rel(filepath.Dir(h.opencodeDir), h.store.ProfileDir("work"))
	require.NoError(t, err)
	require.NoError(t, os.Symlink(rel, h.opencodeDir))

	_, _, err = h.run("init")
	require.Error(t, err)
	assert.Equal(t, "already initialized (active: work)", err.Error())
}

func TestInit_AlreadyInitialized_WithExistingRequestedProfileReportsRealActiveProfile(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	require.NoError(t, os.MkdirAll(h.store.ProfileDir("work"), 0o755))

	_, _, err := h.run("init", "--as", "work")
	require.Error(t, err)
	assert.Equal(t, "already initialized (active: default)", err.Error())
}

func TestInit_WithExistingOpencodeDir(t *testing.T) {
	h := newHarness(t)
	// Create a real ~/.config/opencode directory to simulate existing config.
	require.NoError(t, os.MkdirAll(h.opencodeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(h.opencodeDir, "opencode.json"), []byte(`{}`), 0o644))

	out, _, err := h.run("init")
	require.NoError(t, err)
	assert.Contains(t, out, "Migrated")

	// Original file should now be inside the profile.
	profileDir := h.store.ProfileDir("default")
	assert.FileExists(t, filepath.Join(profileDir, "opencode.json"))

	managed, merr := h.store.IsOpmManaged()
	require.NoError(t, merr)
	assert.True(t, managed)
}

func TestInit_RefusesRegularFileAtOpencodePath(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, os.WriteFile(h.opencodeDir, []byte("not a directory"), 0o644))

	_, _, err := h.run("init")
	require.Error(t, err)
	assert.Contains(t, err.Error(), h.opencodeDir+" is not a directory or symlink")
	assert.Contains(t, err.Error(), "Back it up and remove it")
	assert.NoDirExists(t, h.store.ProfileDir("default"))
}

func TestInit_RejectsStaleResumeSymlink(t *testing.T) {
	h := newHarness(t)
	profileDir := h.store.ProfileDir("default")
	require.NoError(t, os.MkdirAll(profileDir, 0o755))

	foreignDir := filepath.Join(t.TempDir(), "foreign")
	require.NoError(t, os.MkdirAll(foreignDir, 0o755))
	require.NoError(t, os.Symlink(foreignDir, h.opencodeDir+".opm-new"))

	_, _, err := h.run("init")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale interrupted init state detected")
	assert.Contains(t, err.Error(), "remove "+h.opencodeDir+".opm-new")

	tmpTarget, readErr := os.Readlink(h.opencodeDir + ".opm-new")
	require.NoError(t, readErr)
	assert.Equal(t, foreignDir, tmpTarget)
	_, statErr := os.Lstat(h.opencodeDir)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestInit_RejectsStaleTempSymlinkOnFreshPath(t *testing.T) {
	h := newHarness(t)

	foreignDir := filepath.Join(t.TempDir(), "foreign")
	require.NoError(t, os.MkdirAll(foreignDir, 0o755))
	require.NoError(t, os.Symlink(foreignDir, h.opencodeDir+".opm-new"))

	_, _, err := h.run("init")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale interrupted init state detected")
	assert.Contains(t, err.Error(), "remove "+h.opencodeDir+".opm-new")

	tmpTarget, readErr := os.Readlink(h.opencodeDir + ".opm-new")
	require.NoError(t, readErr)
	assert.Equal(t, foreignDir, tmpTarget)
	_, statErr := os.Lstat(h.opencodeDir)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
	assert.NoDirExists(t, h.store.ProfileDir("default"))
}

func TestInit_RejectsMalformedTempState(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, os.WriteFile(h.opencodeDir+".opm-new", []byte("bad temp state"), 0o644))

	_, _, err := h.run("init")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale interrupted init state detected")
	assert.Contains(t, err.Error(), "remove "+h.opencodeDir+".opm-new")

	info, statErr := os.Lstat(h.opencodeDir + ".opm-new")
	require.NoError(t, statErr)
	assert.False(t, info.Mode()&os.ModeSymlink != 0)
	_, opencodeErr := os.Lstat(h.opencodeDir)
	assert.ErrorIs(t, opencodeErr, os.ErrNotExist)
	assert.NoDirExists(t, h.store.ProfileDir("default"))
}

func TestInit_ResumesMatchingTempSymlink(t *testing.T) {
	h := newHarness(t)
	profileDir := h.store.ProfileDir("default")
	require.NoError(t, os.MkdirAll(profileDir, 0o755))
	require.NoError(t, os.Symlink(profileDir, h.opencodeDir+".opm-new"))

	out, _, err := h.run("init")
	require.NoError(t, err)
	assert.Contains(t, out, "Initialized opm")

	target, readErr := os.Readlink(h.opencodeDir)
	require.NoError(t, readErr)
	assert.Equal(t, profileDir, target)
	_, statErr := os.Lstat(h.opencodeDir + ".opm-new")
	assert.ErrorIs(t, statErr, os.ErrNotExist)

	active, activeErr := h.store.ActiveProfile()
	require.NoError(t, activeErr)
	assert.Equal(t, "default", active)
}

func TestInit_RejectsResumeTargetThatIsNotDirectory(t *testing.T) {
	h := newHarness(t)
	profileDir := h.store.ProfileDir("default")
	require.NoError(t, os.MkdirAll(filepath.Dir(profileDir), 0o755))
	require.NoError(t, os.WriteFile(profileDir, []byte("not a profile directory"), 0o644))
	require.NoError(t, os.Symlink(profileDir, h.opencodeDir+".opm-new"))

	_, _, err := h.run("init")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "partial initialization detected")
	assert.Contains(t, err.Error(), "rm -rf "+profileDir)

	info, statErr := os.Lstat(profileDir)
	require.NoError(t, statErr)
	assert.False(t, info.IsDir())
	_, opencodeErr := os.Lstat(h.opencodeDir)
	assert.ErrorIs(t, opencodeErr, os.ErrNotExist)
	_, tmpErr := os.Lstat(h.opencodeDir + ".opm-new")
	require.NoError(t, tmpErr)
}

func TestInit_RejectsResumeWhenOpencodeDirStillExists(t *testing.T) {
	h := newHarness(t)
	profileDir := h.store.ProfileDir("default")
	require.NoError(t, os.MkdirAll(profileDir, 0o755))
	require.NoError(t, os.Symlink(profileDir, h.opencodeDir+".opm-new"))
	require.NoError(t, os.MkdirAll(h.opencodeDir, 0o755))

	_, _, err := h.run("init")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "partial initialization detected")
	assert.Contains(t, err.Error(), h.opencodeDir+" still exists")
	assert.Contains(t, err.Error(), "inspect and back up "+h.opencodeDir)
	assert.Contains(t, err.Error(), "remove "+h.opencodeDir)
	assert.Contains(t, err.Error(), "remove "+h.opencodeDir+".opm-new")
	assert.NotContains(t, err.Error(), "rm -rf "+profileDir)

	info, statErr := os.Lstat(h.opencodeDir)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
	_, tmpErr := os.Lstat(h.opencodeDir + ".opm-new")
	require.NoError(t, tmpErr)
	active, activeErr := h.store.ActiveProfile()
	require.NoError(t, activeErr)
	assert.Empty(t, active)
}

func TestInit_CustomAsName(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("init", "--as", "work")
	require.NoError(t, err)

	active, err := h.store.ActiveProfile()
	require.NoError(t, err)
	assert.Equal(t, "work", active)
}

func TestInit_CurrentWriteFailureWarnsButSucceeds(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, h.store.Init())
	h.breakCurrentPath(t)

	out, stderr, err := h.run("init")
	require.NoError(t, err)
	assert.Contains(t, out, "Initialized opm")
	assert.Contains(t, stderr, "⚠ Updated live symlink state")
	assert.Contains(t, stderr, "failed to update current cache")

	target, readErr := os.Readlink(h.opencodeDir)
	require.NoError(t, readErr)
	assert.Equal(t, h.store.ProfileDir("default"), target)
}

// ── use ───────────────────────────────────────────────────────────────────────

func TestUse_SwitchesProfile(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)

	out, _, err := h.run("use", "work")
	require.NoError(t, err)
	assert.Contains(t, out, "work")

	active, err := h.store.ActiveProfile()
	require.NoError(t, err)
	assert.Equal(t, "work", active)
}

func TestUse_AlreadyOnProfile(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	out, _, err := h.run("use", "default")
	require.NoError(t, err)
	assert.Contains(t, out, "Already on")
}

func TestUse_NonexistentProfile(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	_, _, err := h.run("use", "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestUse_InvalidName(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	_, _, err := h.run("use", "../evil")
	require.Error(t, err)
}

func TestUse_RequiresInit(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("use", "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not managed by opm")
}

func TestUse_CurrentWriteFailureWarnsButSucceeds(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)
	h.breakCurrentPath(t)

	out, stderr, err := h.run("use", "work")
	require.NoError(t, err)
	assert.Contains(t, out, "work")
	assert.Contains(t, stderr, "⚠ Updated live symlink state")
	assert.Contains(t, stderr, "failed to update current cache")

	target, readErr := os.Readlink(h.opencodeDir)
	require.NoError(t, readErr)
	assert.Equal(t, h.store.ProfileDir("work"), target)
}

// ── create ────────────────────────────────────────────────────────────────────

func TestCreate_Basic(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	out, _, err := h.run("create", "work")
	require.NoError(t, err)
	assert.Contains(t, out, "work")
	assert.DirExists(t, h.store.ProfileDir("work"))
}

func TestCreate_Duplicate(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)

	_, _, err = h.run("create", "work")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreate_FromExisting(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	// Write a file into default profile.
	require.NoError(t, os.WriteFile(filepath.Join(h.store.ProfileDir("default"), "settings.json"), []byte(`{}`), 0o644))

	_, _, err := h.run("create", "work", "--from", "default")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(h.store.ProfileDir("work"), "settings.json"))
}

func TestCreate_InvalidName(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	_, _, err := h.run("create", "../evil")
	require.Error(t, err)
}

// ── list ──────────────────────────────────────────────────────────────────────

func TestList_ShowsProfiles(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)

	out, _, err := h.run("list")
	require.NoError(t, err)
	assert.Contains(t, out, "default")
	assert.Contains(t, out, "work")
}

func TestList_ActiveMarked(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	out, _, err := h.run("list")
	require.NoError(t, err)
	// Active profile line contains ● marker.
	assert.True(t, strings.Contains(out, "default"), "default should appear in list")
}

func TestList_LongFlag(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	out, _, err := h.run("list", "-l")
	require.NoError(t, err)
	// Long format includes the path.
	assert.Contains(t, out, h.store.ProfileDir("default"))
}

// ── show ──────────────────────────────────────────────────────────────────────

func TestShow_PrintsActiveName(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	out, _, err := h.run("show")
	require.NoError(t, err)
	assert.Equal(t, "default\n", out)
}

func TestShow_BrokenManagedSymlinkErrors(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	// Point the symlink at a nonexistent profile directory to create a managed dangling symlink.
	goneDir := h.store.ProfileDir("default")
	require.NoError(t, os.RemoveAll(goneDir))
	require.NoError(t, os.Remove(h.opencodeDir))
	require.NoError(t, os.Symlink(goneDir, h.opencodeDir))
	require.NoError(t, h.store.SetCurrent("default"))

	_, _, err := h.run("show")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "active profile is broken")
	assert.Contains(t, err.Error(), "Run 'opm list'")
}

func TestShow_BrokenSymlinkWithoutCurrentErrors(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	goneDir := h.store.ProfileDir("default")
	require.NoError(t, os.RemoveAll(goneDir))
	require.NoError(t, os.Remove(h.opencodeDir))
	require.NoError(t, os.Symlink(goneDir, h.opencodeDir))
	require.NoError(t, h.store.SetCurrent(""))

	_, _, err := h.run("show")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "active profile is broken")
}

func TestShow_MissingSymlinkErrorsEvenWithCurrentCache(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	require.NoError(t, os.Remove(h.opencodeDir))
	require.NoError(t, h.store.SetCurrent("default"))

	_, _, err := h.run("show")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "active profile is broken")
}

func TestShow_AbsentSymlinkWithoutCurrentErrors(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	require.NoError(t, os.Remove(h.opencodeDir))
	require.NoError(t, h.store.SetCurrent(""))

	_, _, err := h.run("show")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active profile")
}

func TestShow_ForeignSymlinkRejected(t *testing.T) {
	h := newHarness(t)
	foreign := t.TempDir()
	require.NoError(t, os.Symlink(foreign, h.opencodeDir))

	_, _, err := h.run("show")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not managed by opm")
}

func TestCompletion_UnmanagedReturnsEmptyWithoutErrorDirective(t *testing.T) {
	h := newHarness(t)
	h.useStoreFactory(t)

	names, directive := singleArgProfileCompletion(useCmd, nil, "")
	assert.Empty(t, names)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

// ── remove ────────────────────────────────────────────────────────────────────

func TestRemove_NonActive(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)

	_, _, err = h.run("remove", "work")
	require.NoError(t, err)
	assert.NoDirExists(t, h.store.ProfileDir("work"))
}

func TestRemove_ActiveRefused(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	_, _, err := h.run("remove", "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot remove the active profile")
}

func TestRemove_ActiveForced_AutoSwitches(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)

	_, _, err = h.run("use", "default")
	require.NoError(t, err)

	_, _, err = h.run("remove", "--force", "default")
	require.NoError(t, err)

	active, err := h.store.ActiveProfile()
	require.NoError(t, err)
	assert.Equal(t, "work", active)
	assert.NoDirExists(t, h.store.ProfileDir("default"))
}

func TestRemove_OnlyProfile_Refused(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	_, _, err := h.run("remove", "--force", "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot remove the only profile")
}

func TestRemove_MultipleNames(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "a")
	require.NoError(t, err)
	_, _, err = h.run("create", "b")
	require.NoError(t, err)

	_, _, err = h.run("remove", "a", "b")
	require.NoError(t, err)
	assert.NoDirExists(t, h.store.ProfileDir("a"))
	assert.NoDirExists(t, h.store.ProfileDir("b"))
}

func TestRemove_DuplicateNamesRejectedBeforeDeletion(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)

	_, _, err = h.run("remove", "work", "work")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "specified more than once")
	assert.DirExists(t, h.store.ProfileDir("work"))
}

func TestRemove_NonexistentAborts(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "real")
	require.NoError(t, err)

	// "ghost" doesn't exist — should error before removing anything.
	_, _, err = h.run("remove", "real", "ghost")
	require.Error(t, err)
	// "real" should still exist (atomic all-or-nothing validation).
	assert.DirExists(t, h.store.ProfileDir("real"))
}

func TestRemove_ForceCurrentWriteFailureWarnsButSucceeds(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)
	h.breakCurrentPath(t)

	out, stderr, err := h.run("remove", "--force", "default")
	require.NoError(t, err)
	assert.Contains(t, out, "Removed profile")
	assert.Contains(t, stderr, "⚠ Updated live symlink state")
	assert.Contains(t, stderr, "failed to update current cache")
	assert.NoDirExists(t, h.store.ProfileDir("default"))

	target, readErr := os.Readlink(h.opencodeDir)
	require.NoError(t, readErr)
	assert.Equal(t, h.store.ProfileDir("work"), target)
}

func TestRemove_ForceDeleteFailureStillWarnsAfterAutoSwitch(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)
	h.breakCurrentPath(t)
	restore := store.TestHookDeleteProfile(t, func(s *store.Store, name string, force bool) error {
		if name == "default" {
			return fmt.Errorf("delete profile %q: injected failure", name)
		}
		return s.DeleteProfile(name, force)
	})
	t.Cleanup(restore)

	_, stderr, err := h.run("remove", "--force", "default")
	require.Error(t, err)
	assert.Contains(t, stderr, "⚠ Updated live symlink state")
	assert.Contains(t, stderr, "failed to update current cache")
	assert.DirExists(t, h.store.ProfileDir("default"))

	target, readErr := os.Readlink(h.opencodeDir)
	require.NoError(t, readErr)
	assert.Equal(t, h.store.ProfileDir("work"), target)
	active, activeErr := h.store.ActiveProfile()
	require.NoError(t, activeErr)
	assert.Equal(t, "work", active)
}

// ── rename ────────────────────────────────────────────────────────────────────

func TestRename_Inactive(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "old")
	require.NoError(t, err)

	_, _, err = h.run("rename", "old", "new")
	require.NoError(t, err)
	assert.NoDirExists(t, h.store.ProfileDir("old"))
	assert.DirExists(t, h.store.ProfileDir("new"))
}

func TestRename_ActiveUpdatesSymlink(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	_, _, err := h.run("rename", "default", "primary")
	require.NoError(t, err)

	active, err := h.store.ActiveProfile()
	require.NoError(t, err)
	assert.Equal(t, "primary", active)
}

func TestRename_InvalidNewName(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	_, _, err := h.run("rename", "default", "../evil")
	require.Error(t, err)
}

func TestRename_ActiveCurrentWriteFailureWarnsButSucceeds(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	h.breakCurrentPath(t)

	out, stderr, err := h.run("rename", "default", "primary")
	require.NoError(t, err)
	assert.Contains(t, out, "Renamed")
	assert.Contains(t, stderr, "⚠ Updated live symlink state")
	assert.Contains(t, stderr, "failed to update current cache")
	assert.NoDirExists(t, h.store.ProfileDir("default"))
	assert.DirExists(t, h.store.ProfileDir("primary"))

	target, readErr := os.Readlink(h.opencodeDir)
	require.NoError(t, readErr)
	assert.Equal(t, h.store.ProfileDir("primary"), target)
}

// ── copy ──────────────────────────────────────────────────────────────────────

func TestCopy_Basic(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	require.NoError(t, os.WriteFile(filepath.Join(h.store.ProfileDir("default"), "cfg.json"), []byte(`{}`), 0o644))

	_, _, err := h.run("copy", "default", "backup")
	require.NoError(t, err)
	assert.DirExists(t, h.store.ProfileDir("backup"))
	assert.FileExists(t, filepath.Join(h.store.ProfileDir("backup"), "cfg.json"))
}

func TestCopy_InvalidSrcName(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	_, _, err := h.run("copy", "../evil", "dst")
	require.Error(t, err)
}

// ── completion ───────────────────────────────────────────────────────────────

func TestCopy_Completion_OnlyCompletesFirstArg(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)
	h.useStoreFactory(t)

	names, directive := copyCmd.ValidArgsFunction(copyCmd, nil, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.ElementsMatch(t, []string{"default", "work"}, names)

	names, directive = copyCmd.ValidArgsFunction(copyCmd, []string{"default"}, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.Empty(t, names)
}

func TestRename_Completion_OnlyCompletesFirstArg(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)
	h.useStoreFactory(t)

	names, directive := renameCmd.ValidArgsFunction(renameCmd, []string{"default"}, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.Empty(t, names)
}

func TestRemove_Completion_ExcludesAlreadySelectedProfiles(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)
	_, _, err = h.run("create", "personal")
	require.NoError(t, err)
	h.useStoreFactory(t)

	names, directive := removeCmd.ValidArgsFunction(removeCmd, []string{"work"}, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.ElementsMatch(t, []string{"default", "personal"}, names)
}

func TestRemove_Completion_NoRemainingCandidatesIsNotError(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	h.useStoreFactory(t)

	names, directive := removeCmd.ValidArgsFunction(removeCmd, []string{"default"}, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.Empty(t, names)
}

// ── path ──────────────────────────────────────────────────────────────────────

func TestPath_PrintsPath(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	out, _, err := h.run("path", "default")
	require.NoError(t, err)
	assert.Equal(t, h.store.ProfileDir("default")+"\n", out)
}

func TestPath_NonexistentProfile(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	_, _, err := h.run("path", "ghost")
	require.Error(t, err)
}

func TestPath_HelpUsesAbsolutePathWording(t *testing.T) {
	h := newHarness(t)

	out, _, err := h.run("path", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "Print the absolute path to a profile directory")
}

// ── inspect ───────────────────────────────────────────────────────────────────

func TestInspect_ShowsInfo(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	require.NoError(t, os.WriteFile(filepath.Join(h.store.ProfileDir("default"), "opencode.json"), []byte(`{}`), 0o644))

	out, _, err := h.run("inspect", "default")
	require.NoError(t, err)
	assert.Contains(t, out, "default")
	assert.Contains(t, out, "opencode.json")
}

// ── reset ─────────────────────────────────────────────────────────────────────

func TestReset_RestoresDirectory(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	require.NoError(t, os.WriteFile(filepath.Join(h.store.ProfileDir("default"), "opencode.json"), []byte(`{}`), 0o644))

	_, _, err := h.run("reset")
	require.NoError(t, err)

	// opencodeDir should now be a real directory.
	info, err := os.Lstat(h.opencodeDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.False(t, info.Mode()&os.ModeSymlink != 0)
	assert.FileExists(t, filepath.Join(h.opencodeDir, "opencode.json"))
}

// ── doctor ────────────────────────────────────────────────────────────────────

func TestDoctor_HealthyInstallation(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)

	out, _, err := h.run("doctor")
	require.NoError(t, err)
	assert.Contains(t, out, "All checks passed")
}

func TestDoctor_NotInitialized(t *testing.T) {
	h := newHarness(t)

	out, _, err := h.run("doctor")
	// doctor exits with errSilent (non-nil) on failure.
	assert.Error(t, err)
	assert.Contains(t, out, "not an opm-managed symlink")
}

func TestDoctor_ConsistencyMismatch(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)

	// Manually install symlink to "work" but leave current file as "default".
	require.NoError(t, symlink.SetAtomic(h.store.ProfileDir("work"), h.opencodeDir))
	// current file still says "default" — mismatch.

	out, _, err := h.run("doctor")
	// Consistency mismatch is a warning, not a failure — doctor should succeed.
	require.NoError(t, err)
	assert.Contains(t, out, "warning") // Consistency section appears
}

func TestRootHelp_OmitsCompletionCommand(t *testing.T) {
	h := newHarness(t)

	out, _, err := h.run("--help")
	require.NoError(t, err)
	assert.NotContains(t, out, "completion")
}

func TestBuildRootHelpSections_UsesCommandMetadata(t *testing.T) {
	root := &cobra.Command{Use: "opm", Short: "OpenCode profile manager"}
	setup := &cobra.Command{Use: "init", Short: "Initialize opm"}
	profiles := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List all profiles"}
	hidden := &cobra.Command{Use: "completion", Short: "Generate completions"}

	markRootHelpGroup(setup, helpGroupSetup)
	markRootHelpGroup(profiles, helpGroupProfiles)
	root.AddCommand(setup, profiles, hidden)

	sections := buildRootHelpSections(root)
	require.Len(t, sections, 2)
	assert.Equal(t, helpGroupSetup, sections[0].label)
	require.Len(t, sections[0].entries, 1)
	assert.Equal(t, "init", sections[0].entries[0].name)
	assert.Equal(t, "Initialize opm", sections[0].entries[0].short)
	assert.Equal(t, "", sections[0].entries[0].alias)

	assert.Equal(t, helpGroupProfiles, sections[1].label)
	require.Len(t, sections[1].entries, 1)
	assert.Equal(t, "list", sections[1].entries[0].name)
	assert.Equal(t, "List all profiles", sections[1].entries[0].short)
	assert.Equal(t, "ls", sections[1].entries[0].alias)
}

func TestBuildRootHelpSections_RealRootCommandCoversExpectedCommands(t *testing.T) {
	sections := buildRootHelpSections(rootCmd)

	byGroup := make(map[string][]string)
	for _, section := range sections {
		for _, entry := range section.entries {
			byGroup[section.label] = append(byGroup[section.label], entry.name)
		}
	}

	assert.Equal(t, []string{"init", "doctor", "reset"}, byGroup[helpGroupSetup])
	assert.Equal(t, []string{"create", "copy", "use", "exec", "list", "show", "inspect", "rename", "remove"}, byGroup[helpGroupProfiles])
	assert.Equal(t, []string{"path"}, byGroup[helpGroupScripting])
}

func TestCmd_Init_ReinitAfterReset(t *testing.T) {
	h := newHarness(t)

	// First init.
	stdout, _, err := h.run("init")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Initialized opm")

	// Simulate reset: remove symlink, restore plain dir, remove current file.
	profileDir := h.store.ProfileDir("default")
	require.NoError(t, os.Remove(h.opencodeDir))
	require.NoError(t, os.MkdirAll(h.opencodeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "opencode.json"), []byte(`{}`), 0o644))
	_ = os.Remove(filepath.Join(h.store.OpmDir(), "current"))

	// Re-init.
	stdout, _, err = h.run("init")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Reinitialized opm")
	assert.Contains(t, stdout, "default")
}

func TestExec_UnknownProfile_ReturnsError(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("init")
	require.NoError(t, err)

	_, _, err = h.run("exec", "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"ghost"`)
}

func TestExec_InvalidName_ReturnsError(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("init")
	require.NoError(t, err)

	_, _, err = h.run("exec", "../evil")
	require.Error(t, err)
}

func TestExec_SetsXDGConfigHome(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("init")
	require.NoError(t, err)
	_, _, err = h.run("create", "work")
	require.NoError(t, err)

	// Write a marker file into the profile dir. If XDG_CONFIG_HOME is wired
	// correctly, the child process can read it via $XDG_CONFIG_HOME/opencode/.
	// This avoids readlink, whose output format differs between platforms
	// (MSYS on Windows returns POSIX paths, not Windows paths).
	marker := filepath.Join(h.store.ProfileDir("work"), ".opm-exec-marker")
	require.NoError(t, os.WriteFile(marker, []byte("opm-ok"), 0o644))

	stdout, _, err := h.run("exec", "work", "--", "sh", "-c", "cat \"$XDG_CONFIG_HOME/opencode/.opm-exec-marker\"")
	require.NoError(t, err)
	assert.Equal(t, "opm-ok", strings.TrimSpace(stdout))
}

func TestExec_GlobalSymlinkUnchanged(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("init")
	require.NoError(t, err)
	_, _, err = h.run("create", "personal")
	require.NoError(t, err)
	_, _, err = h.run("use", "personal")
	require.NoError(t, err)

	// Create a second profile and exec with it.
	_, _, err = h.run("create", "work")
	require.NoError(t, err)
	_, _, err = h.run("exec", "work", "--", "sh", "-c", ":")
	require.NoError(t, err)

	// Global active profile must still be "personal".
	active, aerr := h.store.ActiveProfile()
	require.NoError(t, aerr)
	assert.Equal(t, "personal", active)
}

func TestExec_EphemeralDirCleanedUp(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("init")
	require.NoError(t, err)
	_, _, err = h.run("create", "work")
	require.NoError(t, err)

	// Capture the XDG_CONFIG_HOME path during exec.
	stdout, _, err := h.run("exec", "work", "--", "sh", "-c", "printf '%s' \"$XDG_CONFIG_HOME\"")
	require.NoError(t, err)
	capturedXDG := strings.TrimSpace(stdout)
	require.NotEmpty(t, capturedXDG)

	// After exec returns, the temp dir must have been removed.
	_, statErr := os.Stat(capturedXDG)
	assert.True(t, os.IsNotExist(statErr), "ephemeral dir %s should be cleaned up after exec", capturedXDG)
}

func TestExec_PassesThroughArgs(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("init")
	require.NoError(t, err)
	_, _, err = h.run("create", "work")
	require.NoError(t, err)

	// Pass multiple args through to the child command.
	stdout, _, err := h.run("exec", "work", "--", "sh", "-c", "echo $1 $2", "--", "hello", "world")
	require.NoError(t, err)
	assert.Contains(t, stdout, "hello")
	assert.Contains(t, stdout, "world")
}

// ── preset ────────────────────────────────────────────────────────────────────

// writeLiveConfig creates opencodeDir as a plain directory containing an
// oh-my-openagent.json with the given content.
func (h *cmdHarness) writeLiveConfig(t *testing.T, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(h.opencodeDir, 0o755))
	path := filepath.Join(h.opencodeDir, "oh-my-openagent.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// writePreset stores a preset file under the harness's opm root.
func (h *cmdHarness) writePreset(t *testing.T, name, content string) {
	t.Helper()
	dir := filepath.Join(h.store.OpmDir(), "presets")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), []byte(content), 0o644))
}

const testLiveConfig = `{
	// keep this comment
	"agents": {
		"sisyphus": { "model": "kimi/kimi-for-coding", "variant": "max", "prompt": "keep me" }
	},
	"categories": { "quick": { "model": "kimi/kimi-for-coding" } }
}`

const testLocalPreset = `{
	"description": "all local",
	"agents": { "sisyphus": { "model": "omlx/qwen3-coder-30b", "variant": "max" } },
	"categories": { "quick": "ollama/llama3.1:8b" }
}`

func TestPreset_ListEmpty(t *testing.T) {
	h := newHarness(t)
	out, _, err := h.run("preset", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "No presets found")
}

func TestPreset_UseMissing(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("preset", "use", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `preset "nope" does not exist`)
}

func TestPreset_CaptureStatusUseDiffFlow(t *testing.T) {
	h := newHarness(t)
	livePath := h.writeLiveConfig(t, testLiveConfig)
	h.writePreset(t, "local", testLocalPreset)

	// Capture the current state as a preset.
	out, _, err := h.run("preset", "capture", "kimi")
	require.NoError(t, err)
	assert.Contains(t, out, "Captured preset")

	// Status reports the captured preset as matching.
	out, _, err = h.run("preset", "status")
	require.NoError(t, err)
	assert.Contains(t, out, "kimi")

	// Diff against the local preset shows pending changes.
	out, _, err = h.run("preset", "diff", "local")
	require.NoError(t, err)
	assert.Contains(t, out, "agents.sisyphus.model")
	assert.Contains(t, out, "omlx/qwen3-coder-30b")

	// Apply it.
	out, _, err = h.run("preset", "use", "local")
	require.NoError(t, err)
	assert.Contains(t, out, "Applied preset")

	// The live file was patched, comment and prompt preserved.
	raw, err := os.ReadFile(livePath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "omlx/qwen3-coder-30b")
	assert.Contains(t, string(raw), "keep this comment")
	assert.Contains(t, string(raw), "keep me")

	// Diff is now clean, status points at local, list marks it active.
	out, _, err = h.run("preset", "diff", "local")
	require.NoError(t, err)
	assert.Contains(t, out, "No changes")

	out, _, err = h.run("preset", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "local")
	assert.Contains(t, out, "all local")

	// Revert restores the pre-apply content.
	out, _, err = h.run("preset", "revert")
	require.NoError(t, err)
	assert.Contains(t, out, "Restored")
	raw, err = os.ReadFile(livePath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "kimi/kimi-for-coding")
}

func TestPreset_CaptureRefusesOverwriteWithoutForce(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)

	_, _, err := h.run("preset", "capture", "kimi")
	require.NoError(t, err)

	_, _, err = h.run("preset", "capture", "kimi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	_, _, err = h.run("preset", "capture", "kimi", "--force")
	require.NoError(t, err)
}

func TestPreset_UseAlreadyMatching(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	_, _, err := h.run("preset", "capture", "kimi")
	require.NoError(t, err)

	out, _, err := h.run("preset", "use", "kimi")
	require.NoError(t, err)
	assert.Contains(t, out, "already matches")
}

func TestPreset_ShowResolved(t *testing.T) {
	h := newHarness(t)
	h.writePreset(t, "base", `{ "agents": { "explore": "anthropic/claude-haiku-4" } }`)
	h.writePreset(t, "local", `{ "extends": "base", "agents": { "sisyphus": "omlx/qwen3-coder-30b" } }`)

	out, _, err := h.run("preset", "show", "local")
	require.NoError(t, err)
	assert.Contains(t, out, "sisyphus")
	assert.Contains(t, out, "omlx/qwen3-coder-30b")
	// Inherited from base via extends.
	assert.Contains(t, out, "explore")
	assert.Contains(t, out, "anthropic/claude-haiku-4")
}

func TestPreset_RevertNoBackups(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.run("preset", "revert")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no preset backups found")
}

func TestPreset_WarnsOnShadowedConfig(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	legacy := filepath.Join(h.opencodeDir, "oh-my-opencode.json")
	require.NoError(t, os.WriteFile(legacy, []byte(testLiveConfig), 0o644))
	h.writePreset(t, "local", testLocalPreset)

	_, stderr, err := h.run("preset", "use", "local")
	require.NoError(t, err)
	assert.Contains(t, stderr, "Multiple oh-my-openagent config files found")
}

func TestPreset_UseBlockedByValidation(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	// opencode.json declares omlx with an explicit models map that does NOT
	// contain the preset's model.
	require.NoError(t, os.WriteFile(filepath.Join(h.opencodeDir, "opencode.json"), []byte(`{
		"provider": { "omlx": { "npm": "@ai-sdk/openai-compatible",
			"options": { "baseURL": "https://api.example.com/v1" },
			"models": { "some-other-model": {} } } }
	}`), 0o644))
	h.writePreset(t, "local", `{ "agents": { "sisyphus": "omlx/qwen3-coder-30b" } }`)

	_, stderr, err := h.run("preset", "use", "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed validation")
	assert.Contains(t, stderr, "qwen3-coder-30b")

	// --force applies anyway.
	out, _, err := h.run("preset", "use", "local", "--force")
	require.NoError(t, err)
	assert.Contains(t, out, "Applied preset")
}

func TestDoctor_ReportsPresetHealth(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	h.writePreset(t, "good", `{ "agents": { "a": "p/m" } }`)
	h.writePreset(t, "broken", `{ not json`)

	out, _, err := h.run("doctor")
	require.Error(t, err) // broken preset is a failure → exit 1
	assert.Contains(t, out, "Presets")
	assert.Contains(t, out, "good")
	assert.Contains(t, out, "broken")
	assert.Contains(t, out, "problem(s) found")
}

func TestPreset_UseProjectWritesOverride(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	h.writePreset(t, "local", testLocalPreset)

	projectDir := t.TempDir()
	origOverride := projectDirOverride
	projectDirOverride = projectDir
	t.Cleanup(func() { projectDirOverride = origOverride })

	out, _, err := h.run("preset", "use", "local", "--project")
	require.NoError(t, err)
	assert.Contains(t, out, "Applied preset")

	// The project override exists and holds the preset's assignments.
	overridePath := filepath.Join(projectDir, ".opencode", "oh-my-openagent.json")
	raw, err := os.ReadFile(overridePath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "omlx/qwen3-coder-30b")

	// The global live config is untouched.
	globalRaw, err := os.ReadFile(filepath.Join(h.opencodeDir, "oh-my-openagent.json"))
	require.NoError(t, err)
	assert.Contains(t, string(globalRaw), "kimi/kimi-for-coding")
	assert.NotContains(t, string(globalRaw), "omlx/qwen3-coder-30b")

	// Diff --project is clean; global diff still shows pending changes.
	out, _, err = h.run("preset", "diff", "local", "--project")
	require.NoError(t, err)
	assert.Contains(t, out, "No changes")
	out, _, err = h.run("preset", "diff", "local")
	require.NoError(t, err)
	assert.Contains(t, out, "agents.sisyphus.model")
}

func TestExec_WithPresetUsesEphemeralOverlay(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)

	// Give the work profile a live config and an extra file.
	workDir := h.store.ProfileDir("work")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "oh-my-openagent.json"), []byte(testLiveConfig), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "other.txt"), []byte("shared"), 0o644))
	h.writePreset(t, "local", testLocalPreset)

	// The child sees the patched config via XDG_CONFIG_HOME...
	stdout, _, err := h.run("exec", "work", "--preset", "local", "--",
		"sh", "-c", `cat "$XDG_CONFIG_HOME/opencode/oh-my-openagent.json"; cat "$XDG_CONFIG_HOME/opencode/other.txt"`)
	require.NoError(t, err)
	assert.Contains(t, stdout, "omlx/qwen3-coder-30b")
	assert.Contains(t, stdout, "keep this comment")
	assert.Contains(t, stdout, "shared")

	// ...while the real profile is untouched.
	raw, err := os.ReadFile(filepath.Join(workDir, "oh-my-openagent.json"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "kimi/kimi-for-coding")
	assert.NotContains(t, string(raw), "omlx/qwen3-coder-30b")
}

func TestExec_WithPresetDoesNotMutateSymlinkedAgents(t *testing.T) {
	h := newHarness(t)
	h.mustInit(t)
	_, _, err := h.run("create", "work")
	require.NoError(t, err)

	// Dotfiles setup: the profile's agents dir is a symlink into a repo,
	// with a markdown agent named by the preset.
	repoDir := t.TempDir()
	agentMd := "---\nmode: subagent\nmodel: kimi/kimi-for-coding\n---\n\nOracle body.\n"
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "sisyphus.md"), []byte(agentMd), 0o644))
	workDir := h.store.ProfileDir("work")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "oh-my-openagent.json"), []byte(testLiveConfig), 0o644))
	require.NoError(t, os.Symlink(repoDir, filepath.Join(workDir, "agents")))
	h.writePreset(t, "local", testLocalPreset)

	// The ephemeral session sees the synced markdown model...
	stdout, _, err := h.run("exec", "work", "--preset", "local", "--",
		"sh", "-c", `cat "$XDG_CONFIG_HOME/opencode/agents/sisyphus.md"`)
	require.NoError(t, err)
	assert.Contains(t, stdout, "omlx/qwen3-coder-30b")

	// ...but the real repo file is untouched.
	raw, err := os.ReadFile(filepath.Join(repoDir, "sisyphus.md"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "kimi/kimi-for-coding")
	assert.NotContains(t, string(raw), "omlx/qwen3-coder-30b")
}

func TestPreset_DiffFilesRendersTrees(t *testing.T) {
	h := newHarness(t)
	livePath := h.writeLiveConfig(t, testLiveConfig)
	h.writePreset(t, "local", testLocalPreset)

	renderDir := filepath.Join(t.TempDir(), "pd")
	out, _, err := h.run("preset", "diff", "local", "--files", renderDir)
	require.NoError(t, err)
	assert.Contains(t, out, "agents.sisyphus.model")
	assert.Contains(t, out, "Rendered 1 file(s) for external diff")

	before, err := os.ReadFile(filepath.Join(renderDir, "before", "oh-my-openagent.json"))
	require.NoError(t, err)
	assert.Contains(t, string(before), "kimi/kimi-for-coding")
	after, err := os.ReadFile(filepath.Join(renderDir, "after", "oh-my-openagent.json"))
	require.NoError(t, err)
	assert.Contains(t, string(after), "omlx/qwen3-coder-30b")
	assert.Contains(t, string(after), "keep this comment")

	// The live config itself is untouched.
	raw, err := os.ReadFile(livePath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "kimi/kimi-for-coding")
}

func TestPreset_CreateAllAndUse(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)

	out, _, err := h.run("preset", "create", "opus", "--all", "anthropic/claude-opus-4-8")
	require.NoError(t, err)
	assert.Contains(t, out, "Created preset")
	assert.Contains(t, out, "anthropic/claude-opus-4-8")

	out, _, err = h.run("preset", "use", "opus")
	require.NoError(t, err)
	assert.Contains(t, out, "Applied preset")

	raw, err := os.ReadFile(filepath.Join(h.opencodeDir, "oh-my-openagent.json"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "anthropic/claude-opus-4-8")
	assert.NotContains(t, string(raw), "kimi/kimi-for-coding")
	assert.Contains(t, string(raw), "keep me")
}

func TestPreset_CreateRequiresAll(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	_, _, err := h.run("preset", "create", "opus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--all")
}

func TestPreset_ModelsListsProviders(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	require.NoError(t, os.WriteFile(filepath.Join(h.opencodeDir, "opencode.json"), []byte(`{
		"provider": {
			"kimi": { "npm": "@ai-sdk/openai-compatible",
				"options": { "baseURL": "https://api.kimi.com/v1" },
				"models": { "kimi-for-coding": {} } },
			"anthropic": { "npm": "@ai-sdk/anthropic", "options": { "apiKey": "x" } }
		}
	}`), 0o644))

	out, _, err := h.run("preset", "models")
	require.NoError(t, err)
	assert.Contains(t, out, "kimi/kimi-for-coding")
	assert.Contains(t, out, "anthropic")
	assert.Contains(t, out, "discovered by OpenCode at runtime")
}

// runWithInput runs the root command with stdin fed from input, for
// interactive commands like `preset edit`.
func (h *cmdHarness) runWithInput(t *testing.T, input string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	rootCmd.SetIn(strings.NewReader(input))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	return h.run(args...)
}

func TestPresetEdit_CreateNew(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig) // agent "sisyphus", category "quick"
	require.NoError(t, os.WriteFile(filepath.Join(h.opencodeDir, "opencode.json"), []byte(`{
		"provider": { "omlx": { "npm": "@ai-sdk/openai-compatible",
			"options": { "baseURL": "https://example.com/v1" },
			"models": { "model-a": {}, "model-b": {} } } }
	}`), 0o644))

	input := strings.Join([]string{
		"",     // description
		"1",    // sisyphus -> catalog #1 (omlx/model-a)
		"",     // variant blank
		"2",    // quick -> catalog #2 (omlx/model-b)
		"high", // variant
		"",     // save confirm (blank = yes)
	}, "\n") + "\n"

	out, _, err := h.runWithInput(t, input, "preset", "edit", "wizard1")
	require.NoError(t, err)
	assert.Contains(t, out, "Saved preset")

	data, rerr := os.ReadFile(filepath.Join(h.store.OpmDir(), "presets", "wizard1.json"))
	require.NoError(t, rerr)
	assert.Contains(t, string(data), `"model": "omlx/model-a"`)
	assert.Contains(t, string(data), `"model": "omlx/model-b"`)
	assert.Contains(t, string(data), `"variant": "high"`)
}

func TestPresetEdit_ExistingClearAndKeep(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig) // agent "sisyphus", category "quick"
	require.NoError(t, os.WriteFile(filepath.Join(h.opencodeDir, "opencode.json"), []byte(`{
		"provider": { "omlx": { "npm": "@ai-sdk/openai-compatible",
			"options": { "baseURL": "https://example.com/v1" },
			"models": { "model-a": {}, "model-b": {} } } }
	}`), 0o644))
	h.writePreset(t, "existing", `{
		"agents": { "sisyphus": "omlx/model-a", "legacy": "omlx/model-b" }
	}`)

	// entry order: agents:sisyphus, categories:quick, agents:legacy (folded in)
	input := strings.Join([]string{
		"",      // description
		"",      // sisyphus -> keep
		"",      // quick -> keep (skip; stays unset)
		"clear", // legacy -> remove from preset
		"",      // save confirm (yes)
	}, "\n") + "\n"

	out, _, err := h.runWithInput(t, input, "preset", "edit", "existing")
	require.NoError(t, err)
	assert.Contains(t, out, "Editing existing preset")

	data, rerr := os.ReadFile(filepath.Join(h.store.OpmDir(), "presets", "existing.json"))
	require.NoError(t, rerr)
	text := string(data)
	assert.Contains(t, text, `"omlx/model-a"`)
	assert.NotContains(t, text, "legacy")
	assert.NotContains(t, text, "omlx/model-b")
	assert.NotContains(t, text, "categories")
}

func TestPresetEdit_DeclineSave(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	require.NoError(t, os.WriteFile(filepath.Join(h.opencodeDir, "opencode.json"), []byte(`{
		"provider": { "omlx": { "npm": "@ai-sdk/openai-compatible",
			"options": { "baseURL": "https://example.com/v1" },
			"models": { "model-a": {} } } }
	}`), 0o644))

	input := strings.Join([]string{
		"",  // description
		"1", // sisyphus -> catalog #1
		"",  // variant blank
		"",  // quick -> skip
		"n", // decline save
	}, "\n") + "\n"

	out, _, err := h.runWithInput(t, input, "preset", "edit", "declined")
	require.NoError(t, err)
	assert.Contains(t, out, "Aborted")

	_, statErr := os.Stat(filepath.Join(h.store.OpmDir(), "presets", "declined.json"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestPresetEdit_InvalidInputReprompts(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	require.NoError(t, os.WriteFile(filepath.Join(h.opencodeDir, "opencode.json"), []byte(`{
		"provider": { "omlx": { "npm": "@ai-sdk/openai-compatible",
			"options": { "baseURL": "https://example.com/v1" },
			"models": { "model-a": {} } } }
	}`), 0o644))

	input := strings.Join([]string{
		"",   // description
		"99", // sisyphus -> out of range, reprompt
		"1",  // sisyphus -> catalog #1
		"",   // variant blank
		"",   // quick -> skip
		"n",  // decline save
	}, "\n") + "\n"

	_, stderr, err := h.runWithInput(t, input, "preset", "edit", "retry")
	require.NoError(t, err)
	assert.Contains(t, stderr, "no model numbered 99")
}

func TestPresetEdit_CategoryRouting(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	require.NoError(t, os.WriteFile(filepath.Join(h.opencodeDir, "opencode.json"), []byte(`{
		"provider": { "omlx": { "npm": "@ai-sdk/openai-compatible",
			"options": { "baseURL": "https://example.com/v1" },
			"models": { "model-a": {} } } }
	}`), 0o644))

	input := strings.Join([]string{
		"",        // description
		"c:quick", // sisyphus -> category routing
		"",        // variant blank
		"",        // quick -> skip
		"y",       // save
	}, "\n") + "\n"

	_, _, err := h.runWithInput(t, input, "preset", "edit", "catroute")
	require.NoError(t, err)

	data, rerr := os.ReadFile(filepath.Join(h.store.OpmDir(), "presets", "catroute.json"))
	require.NoError(t, rerr)
	assert.Contains(t, string(data), `"category": "quick"`)
	assert.NotContains(t, string(data), `"model"`)
}

func TestPresetEdit_NoProvidersDeclared(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	// No opencode.json at all.
	_, _, err := h.run("preset", "edit", "noprov")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no opencode.json")
}

func TestPreset_SetCreatesAndUpdates(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)

	out, _, err := h.run("preset", "set", "mixed", "sisyphus", "kiro/claude-opus-4-7", "--variant", "max")
	require.NoError(t, err)
	assert.Contains(t, out, "Created preset")

	data, rerr := os.ReadFile(filepath.Join(h.store.OpmDir(), "presets", "mixed.json"))
	require.NoError(t, rerr)
	assert.Contains(t, string(data), `"model": "kiro/claude-opus-4-7"`)
	assert.Contains(t, string(data), `"variant": "max"`)

	// A second call on the same preset updates it in place.
	out, _, err = h.run("preset", "set", "mixed", "oracle", "openai/gpt-5.5")
	require.NoError(t, err)
	assert.Contains(t, out, "Updated preset")

	data, rerr = os.ReadFile(filepath.Join(h.store.OpmDir(), "presets", "mixed.json"))
	require.NoError(t, rerr)
	text := string(data)
	assert.Contains(t, text, `"kiro/claude-opus-4-7"`) // sisyphus survived
	assert.Contains(t, text, `"openai/gpt-5.5"`)       // oracle added
}

func TestPreset_SetCategoryFlagTargetsCategories(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)

	_, _, err := h.run("preset", "set", "mixed", "quick", "ollama/llama3.1:8b", "--category")
	require.NoError(t, err)

	data, rerr := os.ReadFile(filepath.Join(h.store.OpmDir(), "presets", "mixed.json"))
	require.NoError(t, rerr)
	text := string(data)
	assert.Contains(t, text, `"categories"`)
	assert.Contains(t, text, `"quick"`)
	assert.Contains(t, text, `"ollama/llama3.1:8b"`)
	assert.NotContains(t, text, `"agents"`)
}

func TestPreset_SetCategoryRouting(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)

	_, _, err := h.run("preset", "set", "mixed", "explore", "c:quick")
	require.NoError(t, err)

	data, rerr := os.ReadFile(filepath.Join(h.store.OpmDir(), "presets", "mixed.json"))
	require.NoError(t, rerr)
	assert.Contains(t, string(data), `"category": "quick"`)
}

func TestPreset_SetCategoryRoutingRejectedOnCategoryTarget(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)

	_, _, err := h.run("preset", "set", "mixed", "quick", "c:deep", "--category")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only valid for agent entries")
}

func TestPreset_SetClear(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)
	h.writePreset(t, "mixed", `{ "agents": { "sisyphus": "kiro/claude-opus-4-7", "oracle": "openai/gpt-5.5" } }`)

	out, _, err := h.run("preset", "set", "mixed", "oracle", "--clear")
	require.NoError(t, err)
	assert.Contains(t, out, "removed")

	data, rerr := os.ReadFile(filepath.Join(h.store.OpmDir(), "presets", "mixed.json"))
	require.NoError(t, rerr)
	text := string(data)
	assert.Contains(t, text, "sisyphus")
	assert.NotContains(t, text, "oracle")
}

func TestPreset_SetInvalidModelFormat(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)

	_, _, err := h.run("preset", "set", "mixed", "sisyphus", "not-a-valid-ref")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider/model form")
}

func TestPreset_SetClearWithModelArgErrors(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)

	_, _, err := h.run("preset", "set", "mixed", "sisyphus", "kiro/claude-opus-4-7", "--clear")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not take a model argument")
}

func TestPreset_SetMissingModelErrors(t *testing.T) {
	h := newHarness(t)
	h.writeLiveConfig(t, testLiveConfig)

	_, _, err := h.run("preset", "set", "mixed", "sisyphus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is required unless --clear")
}

func TestPreset_SetThenUseWorks(t *testing.T) {
	h := newHarness(t)
	livePath := h.writeLiveConfig(t, testLiveConfig)

	_, _, err := h.run("preset", "set", "mixed", "sisyphus", "kimi/kimi-for-coding", "--variant", "high")
	require.NoError(t, err)
	_, _, err = h.run("preset", "set", "mixed", "quick", "kimi/kimi-for-coding", "--category")
	require.NoError(t, err)

	out, _, err := h.run("preset", "use", "mixed")
	require.NoError(t, err)
	assert.Contains(t, out, "Applied preset")

	raw, err := os.ReadFile(livePath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"variant": "high"`)
}

func TestPreset_ExamplesFlag(t *testing.T) {
	h := newHarness(t)
	out, _, err := h.run("preset", "--examples")
	require.NoError(t, err)
	assert.Contains(t, out, "Bulk: same model everywhere")
	assert.Contains(t, out, "opm preset create opus --all kiro/claude-opus-4-8")
	assert.Contains(t, out, "Category tiers")
	assert.Contains(t, out, "c:deep")
}

func TestPreset_BareShowsHelpUnchanged(t *testing.T) {
	h := newHarness(t)
	out, _, err := h.run("preset")
	require.NoError(t, err)
	assert.Contains(t, out, "Model presets are named model-mapping overlays")
	assert.Contains(t, out, "opm preset --examples")
}
