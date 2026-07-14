package preset

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// agentMdDirs are the directories OpenCode reads markdown agent definitions
// from, relative to the config dir. Both singular and plural forms exist in
// the wild (the plural is common in dotfiles setups).
var agentMdDirs = []string{"agent", "agents"}

// agentMdPath returns the markdown definition file for an agent, if one
// exists in the profile.
func (m *Manager) agentMdPath(name string) (string, bool) {
	for _, dir := range agentMdDirs {
		path := filepath.Join(m.opencodeDir, dir, name+".md")
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			return path, true
		}
	}
	return "", false
}

// frontmatterModelLine matches the model key line inside YAML frontmatter.
var frontmatterModelLine = regexp.MustCompile(`(?m)^model:[^\n]*$`)

// splitFrontmatter splits a markdown file into its frontmatter block
// (delimiters included) and the remaining body. ok is false when the file
// has no frontmatter.
func splitFrontmatter(data []byte) (frontmatter, body []byte, ok bool) {
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return nil, nil, false
	}
	end := bytes.Index(data[4:], []byte("\n---"))
	if end < 0 {
		return nil, nil, false
	}
	// Frontmatter runs through the closing delimiter's trailing newline (or EOF).
	closeStart := 4 + end + 1 // index of the closing "---"
	closeEnd := closeStart + 3
	if closeEnd < len(data) && data[closeEnd] == '\n' {
		closeEnd++
	}
	return data[:closeEnd], data[closeEnd:], true
}

// readFrontmatterModel extracts the model value from a markdown agent file.
func readFrontmatterModel(data []byte) (string, bool) {
	fm, _, ok := splitFrontmatter(data)
	if !ok {
		return "", false
	}
	line := frontmatterModelLine.Find(fm)
	if line == nil {
		return "", false
	}
	return string(bytes.TrimSpace(line[len("model:"):])), true
}

// setFrontmatterModel returns the file with its frontmatter model key set to
// model, inserting the key before the closing delimiter when absent. Only
// the model line is touched.
func setFrontmatterModel(data []byte, model string) ([]byte, error) {
	fm, body, ok := splitFrontmatter(data)
	if !ok {
		return nil, fmt.Errorf("file has no YAML frontmatter")
	}
	newLine := []byte("model: " + model)
	if frontmatterModelLine.Match(fm) {
		fm = frontmatterModelLine.ReplaceAll(fm, newLine)
	} else {
		closing := bytes.LastIndex(fm, []byte("---"))
		var buf bytes.Buffer
		buf.Write(fm[:closing])
		buf.Write(newLine)
		buf.WriteByte('\n')
		buf.Write(fm[closing:])
		fm = buf.Bytes()
	}
	return append(fm, body...), nil
}

// diffAgentMd computes the markdown-frontmatter changes applying p would
// make: for every preset agent entry whose name has a markdown definition
// in the profile, the file's frontmatter model is synced to the entry's
// model — but only when the file already pins one. A markdown agent
// without a model line inherits the session default by design; presets
// respect that and never force-pin it (which also keeps capture → status
// round-trips clean). Files without frontmatter are likewise skipped.
func (m *Manager) diffAgentMd(p *Preset) ([]Change, error) {
	names := make([]string, 0, len(p.Agents))
	for name := range p.Agents {
		names = append(names, name)
	}
	sort.Strings(names)

	var changes []Change
	for _, name := range names {
		path, exists := m.agentMdPath(name)
		if !exists {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		current, hasModel := readFrontmatterModel(data)
		if !hasModel {
			continue
		}
		want := p.Agents[name].Model()
		if want == "" {
			// Category-routed entry — no direct model to sync into markdown.
			continue
		}
		if current == want {
			continue
		}
		changes = append(changes, Change{
			Section: "agent-md", Name: name, Key: "model",
			Old: current, New: want, Op: OpReplace,
			mdPath: path,
		})
	}
	return changes, nil
}

// applyAgentMd rewrites the markdown files named in changes, backing each up
// first. Writes are atomic and symlink-aware.
func (m *Manager) applyAgentMd(changes []Change, backup *backupSet) error {
	for _, c := range changes {
		if c.Section != "agent-md" {
			continue
		}
		target := c.mdPath
		if resolved, err := filepath.EvalSymlinks(target); err == nil {
			target = resolved
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("read %s: %w", target, err)
		}
		updated, err := setFrontmatterModel(data, c.New)
		if err != nil {
			return fmt.Errorf("%s: %w", target, err)
		}
		if err := backup.add(target, data); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if fi, err := os.Stat(target); err == nil {
			perm = fi.Mode().Perm()
		}
		if err := writeFileAtomic(target, updated, perm); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
	}
	return nil
}
