package preset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"
)

// opencodeKeys are the top-level opencode.json fields a preset's "opencode"
// block may set.
var opencodeKeys = map[string]bool{"model": true, "small_model": true}

// opencodeFile locates the profile's opencode.json[c] for patching.
// Returns ok=false when none exists (the opencode block is then skipped
// with a warning at the command layer via Diff returning no changes).
func (m *Manager) opencodeFile() (path, target string, raw []byte, ok bool, err error) {
	for _, name := range opencodeCandidates {
		p := filepath.Join(m.opencodeDir, name)
		data, rerr := os.ReadFile(p)
		if os.IsNotExist(rerr) {
			continue
		}
		if rerr != nil {
			return "", "", nil, false, fmt.Errorf("read %s: %w", p, rerr)
		}
		t, terr := filepath.EvalSymlinks(p)
		if terr != nil {
			return "", "", nil, false, fmt.Errorf("resolve %s: %w", p, terr)
		}
		return p, t, data, true, nil
	}
	return "", "", nil, false, nil
}

// diffOpencode computes changes to top-level opencode.json fields named in
// the preset's "opencode" block. Unlike agent/category entries, this block
// only sets the keys it names — absent keys are never removed, because
// opm does not own the rest of opencode.json.
func (m *Manager) diffOpencode(p *Preset) ([]Change, error) {
	if len(p.Opencode) == 0 {
		return nil, nil
	}
	_, _, raw, ok, err := m.opencodeFile()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	var root map[string]json.RawMessage
	if err := jsonUnmarshalLoose(bytes.Clone(raw), &root); err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}

	var changes []Change
	for _, key := range []string{"model", "small_model"} {
		want, inPreset := p.Opencode[key]
		if !inPreset {
			continue
		}
		have, inLive := root[key]
		switch {
		case !inLive:
			changes = append(changes, Change{
				Section: "opencode", Name: key,
				New: renderValue(want), Op: OpAdd, newRaw: want,
			})
		case !jsonEqual(want, have):
			changes = append(changes, Change{
				Section: "opencode", Name: key,
				Old: renderValue(have), New: renderValue(want), Op: OpReplace, newRaw: want,
			})
		}
	}
	return changes, nil
}

// captureOpencode snapshots the profile's top-level model fields for
// Capture. Only provider/model-form string values are captured.
func (m *Manager) captureOpencode() map[string]json.RawMessage {
	_, _, raw, ok, err := m.opencodeFile()
	if err != nil || !ok {
		return nil
	}
	var root map[string]json.RawMessage
	if jsonUnmarshalLoose(bytes.Clone(raw), &root) != nil {
		return nil
	}
	out := map[string]json.RawMessage{}
	for key := range opencodeKeys {
		v, present := root[key]
		if !present {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && strings.Contains(s, "/") {
			out[key] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// patchOpencode returns raw with the named top-level fields patched,
// preserving comments and everything else.
func patchOpencode(path string, raw []byte, changes []Change) ([]byte, error) {
	var ops []patchOp
	for _, c := range changes {
		if c.Section != "opencode" {
			continue
		}
		op := "replace"
		if c.Op == OpAdd {
			op = "add"
		}
		ops = append(ops, patchOp{Op: op, Path: "/" + escapePointer(c.Name), Value: c.newRaw})
	}
	if len(ops) == 0 {
		return nil, nil
	}
	patch, err := json.Marshal(ops)
	if err != nil {
		return nil, fmt.Errorf("encode patch: %w", err)
	}
	value, err := hujson.Parse(bytes.Clone(raw))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := value.Patch(patch); err != nil {
		return nil, fmt.Errorf("patch %s: %w", path, err)
	}
	return value.Pack(), nil
}

// applyOpencode patches the named top-level fields in opencode.json[c].
func (m *Manager) applyOpencode(changes []Change, backup *backupSet) error {
	path, target, raw, ok, err := m.opencodeFile()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	patched, err := patchOpencode(path, raw, changes)
	if err != nil {
		return err
	}
	if patched == nil {
		return nil
	}
	if err := backup.add(target, raw); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(target); err == nil {
		perm = fi.Mode().Perm()
	}
	if err := writeFileAtomic(target, patched, perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
