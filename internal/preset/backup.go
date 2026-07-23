package preset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// A backupSet is one apply's worth of pre-change file copies, stored under
// <backupsDir>/<stamp>/ with a manifest mapping each copy back to the
// absolute path it came from. Multi-file applies (live config + markdown
// agents + opencode.json) revert as a unit.
type backupSet struct {
	backupsDir string
	dir        string // created lazily on first add
	manifest   backupManifest
	nextIndex  int
}

type backupManifest struct {
	Files []backupEntry `json:"files"`
}

type backupEntry struct {
	Name   string `json:"name"`   // file name inside the stamp dir
	Target string `json:"target"` // absolute path the backup restores to
}

const manifestName = "manifest.json"

// backupStamp matches stamp directory names, including collision suffixes.
var backupStamp = regexp.MustCompile(`^\d{8}-\d{6}(\.\d+)?$`)

func newBackupSet(backupsDir string) *backupSet {
	return &backupSet{backupsDir: backupsDir}
}

// add copies raw (the pre-change content of target) into the set.
func (b *backupSet) add(target string, raw []byte) error {
	if b.dir == "" {
		if err := b.createDir(); err != nil {
			return err
		}
	}
	name := fmt.Sprintf("%d-%s", b.nextIndex, filepath.Base(target))
	b.nextIndex++
	if err := os.WriteFile(filepath.Join(b.dir, name), raw, 0o644); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	b.manifest.Files = append(b.manifest.Files, backupEntry{Name: name, Target: target})
	return nil
}

func (b *backupSet) createDir() error {
	if err := os.MkdirAll(b.backupsDir, 0o755); err != nil {
		return fmt.Errorf("create backups dir: %w", err)
	}
	stamp := time.Now().Format("20060102-150405")
	dir := filepath.Join(b.backupsDir, stamp)
	for i := 2; ; i++ {
		if err := os.Mkdir(dir, 0o755); err == nil {
			break
		} else if !os.IsExist(err) {
			return fmt.Errorf("create backup dir: %w", err)
		}
		dir = filepath.Join(b.backupsDir, fmt.Sprintf("%s.%d", stamp, i))
	}
	b.dir = dir
	return nil
}

// finish writes the manifest. A set with no files leaves nothing behind.
func (b *backupSet) finish() (string, error) {
	if b.dir == "" {
		return "", nil
	}
	data, err := json.MarshalIndent(b.manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(b.dir, manifestName), append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("write manifest: %w", err)
	}
	return b.dir, nil
}

// Revert restores every file recorded in the most recent backup set.
func (m *Manager) Revert() (restored []string, backupName string, err error) {
	entries, err := os.ReadDir(m.backupsDir)
	if os.IsNotExist(err) {
		return nil, "", fmt.Errorf("no preset backups found")
	}
	if err != nil {
		return nil, "", fmt.Errorf("list backups: %w", err)
	}

	var stamps []string
	for _, e := range entries {
		if e.IsDir() && backupStamp.MatchString(e.Name()) {
			stamps = append(stamps, e.Name())
		}
	}
	if len(stamps) == 0 {
		return nil, "", fmt.Errorf("no preset backups found")
	}
	sort.Strings(stamps)
	newest := stamps[len(stamps)-1]
	dir := filepath.Join(m.backupsDir, newest)

	manifestData, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, "", fmt.Errorf("read backup manifest: %w", err)
	}
	var manifest backupManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, "", fmt.Errorf("parse backup manifest: %w", err)
	}

	for _, entry := range manifest.Files {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name))
		if err != nil {
			return restored, newest, fmt.Errorf("read backup %s: %w", entry.Name, err)
		}
		if err := os.MkdirAll(filepath.Dir(entry.Target), 0o755); err != nil {
			return restored, newest, fmt.Errorf("create dir for %s: %w", entry.Target, err)
		}
		if err := writeFileAtomic(entry.Target, data, 0o644); err != nil {
			return restored, newest, fmt.Errorf("restore %s: %w", entry.Target, err)
		}
		restored = append(restored, entry.Target)
	}
	return restored, newest, nil
}
