package preset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

// ChangeOp is the kind of field-level change apply would make.
type ChangeOp int

const (
	OpAdd ChangeOp = iota
	OpReplace
	OpRemove
)

// Change is one field-level difference between a preset and the live state.
type Change struct {
	Section string // "agents", "categories", "agent-md", or "opencode"
	Name    string // agent/category name (or top-level key for "opencode")
	Key     string // tuning key ("" for "opencode" changes)
	Old     string // rendered current value, "" when absent
	New     string // rendered preset value, "" when removed
	Op      ChangeOp

	newRaw       json.RawMessage // raw preset value for patch building
	entryMissing bool            // live entry absent entirely — patch adds the whole entry
	mdPath       string          // markdown file path for "agent-md" changes
}

// Field renders the change's dotted field path for display.
func (c Change) Field() string {
	field := c.Section + "." + c.Name
	if c.Key != "" {
		field += "." + c.Key
	}
	return field
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

// Diff computes the field-level changes applying p would make across every
// surface a preset owns: the oh-my-openagent config, markdown agent
// frontmatter, and top-level opencode.json fields. An empty result means
// the live state already matches the preset.
func (m *Manager) Diff(p *Preset) ([]Change, error) {
	lf, err := m.LiveFile()
	if err != nil {
		return nil, err
	}
	state, _, err := m.readLiveState(lf)
	if err != nil {
		return nil, err
	}
	changes := diffState(p, state)

	mdChanges, err := m.diffAgentMd(p)
	if err != nil {
		return nil, err
	}
	changes = append(changes, mdChanges...)

	ocChanges, err := m.diffOpencode(p)
	if err != nil {
		return nil, err
	}
	return append(changes, ocChanges...), nil
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

// Apply patches every surface the preset owns to match p: the
// oh-my-openagent config (only tuning keys of named entries), markdown
// agent frontmatter (model line only), and named top-level opencode.json
// fields. Comments, formatting, and all other content are preserved via
// hujson's JSON-Patch support. Originals are backed up as one revertable
// set, and every write is atomic and symlink-aware.
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
	omoChanges := diffState(p, state)
	mdChanges, err := m.diffAgentMd(p)
	if err != nil {
		return nil, err
	}
	ocChanges, err := m.diffOpencode(p)
	if err != nil {
		return nil, err
	}
	result.Changes = append(append(omoChanges, mdChanges...), ocChanges...)
	if len(result.Changes) == 0 {
		return result, nil
	}

	backup := newBackupSet(m.backupsDir)

	if len(omoChanges) > 0 {
		if err := m.applyLiveConfig(p, lf, state, raw, omoChanges, backup, result); err != nil {
			return nil, err
		}
	}
	if err := m.applyAgentMd(mdChanges, backup); err != nil {
		return nil, err
	}
	if err := m.applyOpencode(ocChanges, backup); err != nil {
		return nil, err
	}

	backupPath, err := backup.finish()
	if err != nil {
		return nil, err
	}
	result.BackupPath = backupPath
	return result, nil
}

// applyLiveConfig patches (or creates) the oh-my-openagent config file.
func (m *Manager) applyLiveConfig(p *Preset, lf LiveFile, state *liveState, raw []byte, changes []Change, backup *backupSet, result *ApplyResult) error {
	if !lf.Exists {
		data, err := newLiveConfig(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(lf.Target), 0o755); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
		if err := writeFileAtomic(lf.Target, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", lf.Path, err)
		}
		result.Created = true
		return nil
	}

	patch, err := buildPatch(state, changes)
	if err != nil {
		return err
	}
	value, err := hujson.Parse(bytes.Clone(raw))
	if err != nil {
		return fmt.Errorf("parse %s: %w", lf.Path, err)
	}
	if err := value.Patch(patch); err != nil {
		return fmt.Errorf("patch %s: %w", lf.Path, err)
	}
	if err := backup.add(lf.Target, raw); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(lf.Target); err == nil {
		perm = fi.Mode().Perm()
	}
	if err := writeFileAtomic(lf.Target, value.Pack(), perm); err != nil {
		return fmt.Errorf("write %s: %w", lf.Path, err)
	}
	return nil
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
		Opencode:   m.captureOpencode(),
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

// marshalPreset renders a preset as stable indented JSON for storage.
func marshalPreset(p *Preset) ([]byte, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
