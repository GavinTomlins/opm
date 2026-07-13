package preset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tailscale/hujson"
)

// backupStampPrefix matches the timestamp prefix of backup file names,
// including the .N collision suffix: "20260713-153000-" or "20260713-153000.2-".
var backupStampPrefix = regexp.MustCompile(`^\d{8}-\d{6}(\.\d+)?-`)

// ChangeOp is the kind of field-level change apply would make.
type ChangeOp int

const (
	OpAdd ChangeOp = iota
	OpReplace
	OpRemove
)

// Change is one field-level difference between a preset and the live config.
type Change struct {
	Section string // "agents" or "categories"
	Name    string // agent/category name
	Key     string // tuning key
	Old     string // rendered current value, "" when absent
	New     string // rendered preset value, "" when removed
	Op      ChangeOp

	newRaw       json.RawMessage // raw preset value for patch building
	entryMissing bool            // live entry absent entirely — patch adds the whole entry
}

// ApplyResult reports what Apply did.
type ApplyResult struct {
	Changes    []Change
	LivePath   string   // the file oh-my-openagent loads
	Target     string   // symlink-resolved path actually written
	BackupPath string   // "" when no write happened or the file was created fresh
	Created    bool     // live config did not exist and was created
	Others     []string // shadowed candidate files worth warning about
}

// liveState is the subset of the live config diffing needs. All other
// top-level content is never inspected, let alone modified.
type liveState struct {
	root       map[string]json.RawMessage
	agents     map[string]map[string]json.RawMessage
	categories map[string]map[string]json.RawMessage
}

func (m *Manager) readLiveState(lf LiveFile) (*liveState, []byte, error) {
	state := &liveState{
		root:       map[string]json.RawMessage{},
		agents:     map[string]map[string]json.RawMessage{},
		categories: map[string]map[string]json.RawMessage{},
	}
	if !lf.Exists {
		return state, nil, nil
	}

	raw, err := os.ReadFile(lf.Target)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", lf.Path, err)
	}
	std, err := hujson.Standardize(bytes.Clone(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", lf.Path, err)
	}
	if err := json.Unmarshal(std, &state.root); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", lf.Path, err)
	}
	for key, dst := range map[string]*map[string]map[string]json.RawMessage{
		"agents":     &state.agents,
		"categories": &state.categories,
	} {
		if section, ok := state.root[key]; ok && !bytes.Equal(bytes.TrimSpace(section), []byte("null")) {
			if err := json.Unmarshal(section, dst); err != nil {
				return nil, nil, fmt.Errorf("parse %s: %q section: %w", lf.Path, key, err)
			}
		}
	}
	return state, raw, nil
}

// Diff computes the field-level changes applying p would make to the live
// config. An empty result means the live config already matches the preset.
func (m *Manager) Diff(p *Preset) ([]Change, error) {
	lf, err := m.LiveFile()
	if err != nil {
		return nil, err
	}
	state, _, err := m.readLiveState(lf)
	if err != nil {
		return nil, err
	}
	return diffState(p, state), nil
}

func diffState(p *Preset, state *liveState) []Change {
	var changes []Change
	changes = append(changes, diffSection("agents", p.Agents, state.agents)...)
	changes = append(changes, diffSection("categories", p.Categories, state.categories)...)
	return changes
}

func diffSection(section string, presetEntries map[string]Entry, live map[string]map[string]json.RawMessage) []Change {
	var changes []Change
	names := make([]string, 0, len(presetEntries))
	for name := range presetEntries {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := presetEntries[name]
		liveEntry, exists := live[name]

		for _, key := range tuningKeyOrder {
			want, inPreset := entry[key]
			var have json.RawMessage
			var inLive bool
			if exists {
				have, inLive = liveEntry[key]
			}

			switch {
			case inPreset && !inLive:
				changes = append(changes, Change{
					Section: section, Name: name, Key: key,
					New: renderValue(want), Op: OpAdd,
					newRaw: want, entryMissing: !exists,
				})
			case inPreset && inLive && !jsonEqual(want, have):
				changes = append(changes, Change{
					Section: section, Name: name, Key: key,
					Old: renderValue(have), New: renderValue(want), Op: OpReplace,
					newRaw: want,
				})
			case !inPreset && inLive:
				// Within an entry the preset is authoritative: stale tuning
				// keys are removed so e.g. a leftover variant can't ride on
				// a model that doesn't support it.
				changes = append(changes, Change{
					Section: section, Name: name, Key: key,
					Old: renderValue(have), Op: OpRemove,
				})
			}
		}
	}
	return changes
}

func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// renderValue renders a raw JSON value for display: strings unquoted,
// everything else as compact JSON.
func renderValue(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// Apply patches the live config to match p. Only tuning keys of entries the
// preset names are touched; comments, formatting, and all other content are
// preserved via hujson's JSON-Patch support. The original file is backed up
// first, and the write is atomic and symlink-aware.
func (m *Manager) Apply(p *Preset) (*ApplyResult, error) {
	lf, err := m.LiveFile()
	if err != nil {
		return nil, err
	}
	state, raw, err := m.readLiveState(lf)
	if err != nil {
		return nil, err
	}

	result := &ApplyResult{LivePath: lf.Path, Target: lf.Target, Others: lf.Others}
	result.Changes = diffState(p, state)
	if len(result.Changes) == 0 {
		return result, nil
	}

	if !lf.Exists {
		data, err := newLiveConfig(p)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(lf.Target), 0o755); err != nil {
			return nil, fmt.Errorf("create config dir: %w", err)
		}
		if err := writeFileAtomic(lf.Target, data, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", lf.Path, err)
		}
		result.Created = true
		return result, nil
	}

	patch, err := buildPatch(state, result.Changes)
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

	backupPath, err := m.backup(lf, raw)
	if err != nil {
		return nil, err
	}
	result.BackupPath = backupPath

	perm := os.FileMode(0o644)
	if fi, err := os.Stat(lf.Target); err == nil {
		perm = fi.Mode().Perm()
	}
	if err := writeFileAtomic(lf.Target, value.Pack(), perm); err != nil {
		return nil, fmt.Errorf("write %s: %w", lf.Path, err)
	}
	return result, nil
}

// newLiveConfig renders a fresh oh-my-openagent.json for the created case.
func newLiveConfig(p *Preset) ([]byte, error) {
	content := struct {
		Schema     string           `json:"$schema"`
		Agents     map[string]Entry `json:"agents,omitempty"`
		Categories map[string]Entry `json:"categories,omitempty"`
	}{omoSchemaURL, p.Agents, p.Categories}
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return append(data, '\n'), nil
}

// patchOp is one RFC 6902 operation.
type patchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// buildPatch turns computed changes into an RFC 6902 patch. Entries missing
// from the live config are added as whole objects; existing entries get
// per-key add/replace/remove ops.
func buildPatch(state *liveState, changes []Change) ([]byte, error) {
	var ops []patchOp
	sectionPresent := map[string]bool{}
	for key := range state.root {
		sectionPresent[key] = true
	}
	// null sections must be replaced wholesale, not descended into.
	for _, key := range []string{"agents", "categories"} {
		if raw, ok := state.root[key]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			ops = append(ops, patchOp{Op: "replace", Path: "/" + key, Value: json.RawMessage("{}")})
		}
	}

	addedSections := map[string]bool{}
	addedEntries := map[string]bool{}

	for _, c := range changes {
		if !sectionPresent[c.Section] && !addedSections[c.Section] {
			ops = append(ops, patchOp{Op: "add", Path: "/" + escapePointer(c.Section), Value: json.RawMessage("{}")})
			addedSections[c.Section] = true
		}

		entryPath := "/" + escapePointer(c.Section) + "/" + escapePointer(c.Name)
		if c.entryMissing {
			if !addedEntries[c.Section+"\x00"+c.Name] {
				ops = append(ops, patchOp{Op: "add", Path: entryPath, Value: json.RawMessage("{}")})
				addedEntries[c.Section+"\x00"+c.Name] = true
			}
		}

		keyPath := entryPath + "/" + escapePointer(c.Key)
		switch c.Op {
		case OpAdd:
			ops = append(ops, patchOp{Op: "add", Path: keyPath, Value: c.newRaw})
		case OpReplace:
			ops = append(ops, patchOp{Op: "replace", Path: keyPath, Value: c.newRaw})
		case OpRemove:
			ops = append(ops, patchOp{Op: "remove", Path: keyPath})
		}
	}
	return json.Marshal(ops)
}

// escapePointer escapes a JSON Pointer reference token per RFC 6901.
func escapePointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

// backup copies the original live file bytes into the backups directory
// under a timestamped name that Revert can map back to the live file.
func (m *Manager) backup(lf LiveFile, raw []byte) (string, error) {
	if err := os.MkdirAll(m.backupsDir, 0o755); err != nil {
		return "", fmt.Errorf("create backups dir: %w", err)
	}
	base := filepath.Base(lf.Path)
	stamp := time.Now().Format("20060102-150405")
	path := filepath.Join(m.backupsDir, stamp+"-"+base)
	for i := 2; ; i++ {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(m.backupsDir, fmt.Sprintf("%s.%d-%s", stamp, i, base))
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}
	return path, nil
}

// writeFileAtomic writes data to target via a temp file + rename in the
// target's own directory. Callers pass a symlink-resolved target so the
// rename lands on the real file and never replaces a symlink.
func writeFileAtomic(target string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".opm-preset-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Capture snapshots the live config's tuning keys into a new stored preset.
// Entries without a model are skipped (they are prompt/permission-only
// overrides, which presets must not own).
func (m *Manager) Capture(name string, force bool) (*Preset, string, error) {
	lf, err := m.LiveFile()
	if err != nil {
		return nil, "", err
	}
	if !lf.Exists {
		return nil, "", fmt.Errorf("no oh-my-openagent config found in %s", m.opencodeDir)
	}
	state, _, err := m.readLiveState(lf)
	if err != nil {
		return nil, "", err
	}

	p := &Preset{
		Name:       name,
		Agents:     captureSection(state.agents),
		Categories: captureSection(state.categories),
	}
	if p.EntryCount() == 0 {
		return nil, "", fmt.Errorf("%s has no agent or category model assignments to capture", lf.Path)
	}
	path, err := m.Save(p, force)
	if err != nil {
		return nil, "", err
	}
	return p, path, nil
}

func captureSection(live map[string]map[string]json.RawMessage) map[string]Entry {
	out := map[string]Entry{}
	for name, liveEntry := range live {
		entry := Entry{}
		for key, val := range liveEntry {
			if tuningKeys[key] {
				entry[key] = val
			}
		}
		if entry.Model() == "" {
			continue
		}
		out[name] = entry
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Revert restores the most recent backup over the live file it came from.
func (m *Manager) Revert() (restoredTo, backupName string, err error) {
	entries, err := os.ReadDir(m.backupsDir)
	if os.IsNotExist(err) || (err == nil && len(entries) == 0) {
		return "", "", fmt.Errorf("no preset backups found")
	}
	if err != nil {
		return "", "", fmt.Errorf("list backups: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", "", fmt.Errorf("no preset backups found")
	}
	sort.Strings(names)
	newest := names[len(names)-1]

	data, err := os.ReadFile(filepath.Join(m.backupsDir, newest))
	if err != nil {
		return "", "", fmt.Errorf("read backup: %w", err)
	}

	// Backup names are <stamp>-<original filename>; recover the filename.
	base := backupStampPrefix.ReplaceAllString(newest, "")
	target := filepath.Join(m.opencodeDir, base)
	if resolved, rerr := filepath.EvalSymlinks(target); rerr == nil {
		target = resolved
	} else if dir, derr := filepath.EvalSymlinks(m.opencodeDir); derr == nil {
		target = filepath.Join(dir, base)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", "", fmt.Errorf("create config dir: %w", err)
	}
	if err := writeFileAtomic(target, data, 0o644); err != nil {
		return "", "", fmt.Errorf("restore backup: %w", err)
	}
	return target, newest, nil
}

// marshalPreset renders a preset as stable indented JSON for storage.
func marshalPreset(p *Preset) ([]byte, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
