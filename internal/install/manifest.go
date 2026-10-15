package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// LoadManifest reads install-manifest.json (missing ⇒ empty manifest, V set).
// A manifest that cannot be decoded is model.ErrSettingsParse.
func LoadManifest(fsys sys.FS, path string) (*model.InstallManifest, error) {
	b, err := fsys.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &model.InstallManifest{V: model.ManifestVersion}, nil
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m model.InstallManifest
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: manifest %s: %v", model.ErrSettingsParse, path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: manifest %s: trailing data", model.ErrSettingsParse, path)
	}
	if m.V < 1 || m.V > model.ManifestVersion {
		return nil, fmt.Errorf("%w: manifest %s: unsupported version %d", model.ErrSettingsParse, path, m.V)
	}
	return &m, nil
}

func marshalManifest(m *model.InstallManifest) []byte {
	c := *m
	c.Entries = append([]model.ManagedEntry{}, m.Entries...)
	c.Files = append([]model.ManagedFile(nil), m.Files...)
	c.Flags = append([]model.ManagedFlag(nil), m.Flags...)
	c.Backups = append([]model.BackupRecord(nil), m.Backups...)
	sortManifest(&c)
	b, _ := json.MarshalIndent(&c, "", "  ")
	return append(b, '\n')
}

// SaveManifest writes the manifest atomically with fsync.
func SaveManifest(fsys sys.FS, ids sys.IDGen, path string, m *model.InstallManifest) error {
	if m.V == 0 {
		c := *m
		c.V = model.ManifestVersion
		m = &c
	}
	return sys.WriteFileAtomic(fsys, ids, path, marshalManifest(m), 0o600, true)
}

func manifestEmpty(m *model.InstallManifest) bool {
	return len(m.Entries) == 0 && len(m.Files) == 0 && len(m.Flags) == 0
}

// sameManifest compares two manifests ignoring Updated.
func sameManifest(a, b *model.InstallManifest) bool {
	ac, bc := *a, *b
	ac.Updated = bc.Updated
	return bytes.Equal(marshalManifest(&ac), marshalManifest(&bc))
}
