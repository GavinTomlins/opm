package preset

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// materializedNames are the profile entries an overlay must copy for real
// (symlink-resolved) instead of symlinking: the files a preset apply may
// patch, and the agent markdown dirs it may sync. Everything else in the
// profile is shared with the original via symlinks.
func isMaterializedName(name string) bool {
	for _, candidate := range liveCandidates {
		if name == candidate {
			return true
		}
	}
	for _, candidate := range opencodeCandidates {
		if name == candidate {
			return true
		}
	}
	return name == "agent" || name == "agents"
}

// OverlayProfile builds an ephemeral copy-on-write view of a profile at dst
// for `opm exec --preset`: every entry is symlinked to the original except
// the config files and agent markdown dirs a preset owns, which are
// materialized as real symlink-resolved copies. Applying a preset to the
// overlay therefore never touches the real profile — or the dotfiles repos
// its symlinks point into.
func OverlayProfile(profileDir, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create overlay dir: %w", err)
	}
	entries, err := os.ReadDir(profileDir)
	if err != nil {
		return fmt.Errorf("read profile dir: %w", err)
	}
	for _, entry := range entries {
		src := filepath.Join(profileDir, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if isMaterializedName(entry.Name()) {
			err := copyResolved(src, dstPath)
			if err == nil {
				continue
			}
			if !os.IsNotExist(err) {
				return fmt.Errorf("materialize %s: %w", entry.Name(), err)
			}
			// Dangling symlink — nothing to materialize; mirror it as-is.
		}
		if err := os.Symlink(src, dstPath); err != nil {
			return fmt.Errorf("link %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// copyResolved copies src to dst following symlinks, so the copy is real
// content even when the source (or its children) are links into another
// repo. Special files are skipped.
func copyResolved(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.MkdirAll(dst, fi.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyResolved(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !fi.Mode().IsRegular() {
		return nil
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcFile.Close() }()
	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(dstFile, srcFile)
	if cerr := dstFile.Close(); err == nil {
		err = cerr
	}
	return err
}
