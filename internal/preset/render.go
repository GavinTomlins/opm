package preset

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tailscale/hujson"
)

// RenderedFile is the before/after content of one file an apply would touch.
type RenderedFile struct {
	Name   string // file name used under the before/ and after/ trees
	Path   string // original absolute path
	Before []byte // nil when the file would be created
	After  []byte
}

// RenderFiles is a fully dry-run render of an apply: it returns the
// before/after content of every file `preset use` would touch, computed
// with the same patching code paths, without writing to the live config.
// Intended for external diff tools (difftastic, Hunk, git difftool).
func (m *Manager) RenderFiles(p *Preset) ([]RenderedFile, error) {
	var files []RenderedFile

	lf, err := m.LiveFile()
	if err != nil {
		return nil, err
	}
	state, raw, err := m.readLiveState(lf)
	if err != nil {
		return nil, err
	}
	omoChanges := diffState(p, state)
	if len(omoChanges) > 0 {
		file := RenderedFile{Name: filepath.Base(lf.Path), Path: lf.Path}
		if !lf.Exists {
			after, err := newLiveConfig(p)
			if err != nil {
				return nil, err
			}
			file.After = after
		} else {
			patch, err := buildPatch(state, omoChanges)
			if err != nil {
				return nil, err
			}
			value, err := hujson.Parse(bytes.Clone(raw))
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", lf.Path, err)
			}
			if err := value.Patch(patch); err != nil {
				return nil, fmt.Errorf("patch %s: %w", lf.Path, err)
			}
			file.Before = raw
			file.After = value.Pack()
		}
		files = append(files, file)
	}

	mdChanges, err := m.diffAgentMd(p)
	if err != nil {
		return nil, err
	}
	for _, c := range mdChanges {
		data, err := os.ReadFile(c.mdPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", c.mdPath, err)
		}
		updated, err := setFrontmatterModel(data, c.New)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.mdPath, err)
		}
		files = append(files, RenderedFile{
			Name:   filepath.Base(c.mdPath),
			Path:   c.mdPath,
			Before: data,
			After:  updated,
		})
	}

	ocChanges, err := m.diffOpencode(p)
	if err != nil {
		return nil, err
	}
	if len(ocChanges) > 0 {
		path, _, ocRaw, ok, err := m.opencodeFile()
		if err != nil {
			return nil, err
		}
		if ok {
			after, err := patchOpencode(path, ocRaw, ocChanges)
			if err != nil {
				return nil, err
			}
			files = append(files, RenderedFile{
				Name:   filepath.Base(path),
				Path:   path,
				Before: ocRaw,
				After:  after,
			})
		}
	}

	return files, nil
}

// WriteRenderDir writes rendered files as <dir>/before/<name> and
// <dir>/after/<name>. Files that would be created have no before entry.
// Name collisions get an index prefix. Returns the two tree roots.
func WriteRenderDir(dir string, files []RenderedFile) (beforeDir, afterDir string, err error) {
	beforeDir = filepath.Join(dir, "before")
	afterDir = filepath.Join(dir, "after")
	for _, d := range []string{beforeDir, afterDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", "", fmt.Errorf("create render dir: %w", err)
		}
	}

	used := map[string]bool{}
	for i, f := range files {
		name := f.Name
		if used[name] {
			name = fmt.Sprintf("%d-%s", i, name)
		}
		used[name] = true

		if f.Before != nil {
			if err := os.WriteFile(filepath.Join(beforeDir, name), f.Before, 0o644); err != nil {
				return "", "", fmt.Errorf("write render file: %w", err)
			}
		}
		if err := os.WriteFile(filepath.Join(afterDir, name), f.After, 0o644); err != nil {
			return "", "", fmt.Errorf("write render file: %w", err)
		}
	}
	return beforeDir, afterDir, nil
}
