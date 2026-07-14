package preset

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tbcrawford/opm/internal/store"
)

// Manager owns all preset state. Like store.Store it is constructed from
// explicit directories so tests can point it at t.TempDir().
type Manager struct {
	presetsDir  string // ~/.config/opm/presets
	opencodeDir string // ~/.config/opencode (live config dir, possibly a symlink)
	backupsDir  string // ~/.config/opm/backups/presets
}

// New creates a Manager backed by the given directories.
func New(presetsDir, opencodeDir, backupsDir string) *Manager {
	return &Manager{presetsDir: presetsDir, opencodeDir: opencodeDir, backupsDir: backupsDir}
}

// PresetsDir returns the directory presets are stored in.
func (m *Manager) PresetsDir() string { return m.presetsDir }

// presetExtensions in load-priority order.
var presetExtensions = []string{".json", ".jsonc"}

// Info describes a stored preset for listings.
type Info struct {
	Name        string
	Description string
	Path        string
	Err         error // non-nil when the preset file fails to parse
}

// List returns all stored presets sorted by name. Unparseable files are
// included with Err set so listings can surface them instead of hiding them.
func (m *Manager) List() ([]Info, error) {
	entries, err := os.ReadDir(m.presetsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list presets: %w", err)
	}

	var infos []Info
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".json" && ext != ".jsonc" {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ext)
		if seen[name] {
			continue // .json shadows .jsonc for the same name
		}
		seen[name] = true

		info := Info{Name: name}
		p, loadErr := m.Load(name)
		if loadErr != nil {
			info.Err = loadErr
			info.Path = filepath.Join(m.presetsDir, e.Name())
		} else {
			info.Description = p.Description
			info.Path = m.existingPresetPath(name)
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// existingPresetPath returns the path of the stored preset file, or "" if
// none exists. .json wins over .jsonc for the same name.
func (m *Manager) existingPresetPath(name string) string {
	for _, ext := range presetExtensions {
		path := filepath.Join(m.presetsDir, name+ext)
		if fi, err := os.Lstat(path); err == nil && !fi.IsDir() {
			return path
		}
	}
	return ""
}

// validateName reuses the store's name allowlist with a preset-specific
// error message.
func validateName(name string) error {
	if err := store.ValidateName(name); err != nil {
		return fmt.Errorf("invalid preset name %q: must match [a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}", name)
	}
	return nil
}

// Load reads and parses a single preset without resolving its extends chain.
func (m *Manager) Load(name string) (*Preset, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	path := m.existingPresetPath(name)
	if path == "" {
		return nil, fmt.Errorf("preset %q does not exist", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read preset %q: %w", name, err)
	}
	return Parse(name, data)
}

// Resolve loads a preset and flattens its extends chain. Cycles are errors.
func (m *Manager) Resolve(name string) (*Preset, error) {
	visited := map[string]bool{}

	var resolve func(name string) (*Preset, error)
	resolve = func(name string) (*Preset, error) {
		if visited[name] {
			return nil, fmt.Errorf("preset %q: extends cycle detected", name)
		}
		visited[name] = true

		p, err := m.Load(name)
		if err != nil {
			return nil, err
		}
		if p.Extends == "" {
			return p, nil
		}
		base, err := resolve(p.Extends)
		if err != nil {
			return nil, fmt.Errorf("preset %q extends: %w", name, err)
		}
		return merge(base, p), nil
	}

	return resolve(name)
}

// Save writes a preset as indented JSON to <presetsDir>/<name>.json.
// Refuses to overwrite an existing preset unless force is set.
func (m *Manager) Save(p *Preset, force bool) (string, error) {
	if err := validateName(p.Name); err != nil {
		return "", err
	}
	if existing := m.existingPresetPath(p.Name); existing != "" && !force {
		return "", fmt.Errorf("preset %q already exists — use --force to overwrite", p.Name)
	}
	if err := os.MkdirAll(m.presetsDir, 0o755); err != nil {
		return "", fmt.Errorf("create presets dir: %w", err)
	}

	data, err := marshalPreset(p)
	if err != nil {
		return "", fmt.Errorf("encode preset %q: %w", p.Name, err)
	}
	path := filepath.Join(m.presetsDir, p.Name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write preset %q: %w", p.Name, err)
	}
	return path, nil
}

// liveCandidates in oh-my-openagent's load-priority order, verified
// against the plugin's detectPluginConfigFile/detectConfigFile source
// (v4.7.5): the canonical oh-my-openagent name wins over the legacy
// oh-my-opencode name, and .jsonc wins over .json within a name. Apply
// must patch the file the framework actually loads.
var liveCandidates = []string{
	"oh-my-openagent.jsonc",
	"oh-my-openagent.json",
	"oh-my-opencode.jsonc",
	"oh-my-opencode.json",
}

// defaultLiveName is the file created when no live config exists yet.
const defaultLiveName = "oh-my-openagent.json"

// omoSchemaURL is written into freshly created live configs. This is the
// canonical URL from upstream's configuration reference — note the schema
// asset keeps the legacy basename and lives on the dev branch; an
// oh-my-openagent.schema.json does not exist.
const omoSchemaURL = "https://raw.githubusercontent.com/code-yeongyu/oh-my-openagent/dev/assets/oh-my-opencode.schema.json"

// LiveFile describes the oh-my-openagent config file a preset applies to.
type LiveFile struct {
	Path   string   // winning candidate path inside opencodeDir
	Target string   // symlink-resolved final write target (== Path when not a link)
	Others []string // other existing candidates, shadowed by Path — worth a warning
	Exists bool
}

// LiveFile resolves which oh-my-openagent config file wins and where writes
// must land. When the winning file (or any path component, e.g. an
// opm-managed opencodeDir) is a symlink, Target is the fully resolved path
// so apply writes through the link instead of replacing it.
func (m *Manager) LiveFile() (LiveFile, error) {
	var existing []string
	for _, name := range liveCandidates {
		path := filepath.Join(m.opencodeDir, name)
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			existing = append(existing, path)
		}
	}

	if len(existing) == 0 {
		lf := LiveFile{Path: filepath.Join(m.opencodeDir, defaultLiveName)}
		// The file doesn't exist, but its directory might be a symlink
		// (opm-managed ~/.config/opencode) — resolve it for the write target.
		if dir, err := filepath.EvalSymlinks(m.opencodeDir); err == nil {
			lf.Target = filepath.Join(dir, defaultLiveName)
		} else {
			lf.Target = lf.Path
		}
		return lf, nil
	}

	lf := LiveFile{Path: existing[0], Others: existing[1:], Exists: true}
	target, err := filepath.EvalSymlinks(lf.Path)
	if err != nil {
		return LiveFile{}, fmt.Errorf("resolve %s: %w", lf.Path, err)
	}
	lf.Target = target
	return lf, nil
}
