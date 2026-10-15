package install

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func TestLoadManifestMissingIsEmpty(t *testing.T) {
	e := newTenv(t)
	m, err := LoadManifest(e.fs, e.lay.Manifest)
	if err != nil || m == nil || m.V != model.ManifestVersion || len(m.Entries) != 0 {
		t.Fatalf("%v %+v", err, m)
	}
}

func TestLoadManifestRejectsCorruptFiles(t *testing.T) {
	e := newTenv(t)
	for name, content := range map[string]string{
		"empty":       "",
		"garbage":     "not json",
		"truncated":   `{"v":1,"entries":[`,
		"no version":  `{"entries":[]}`,
		"future":      `{"v":2,"entries":[]}`,
		"trailing":    `{"v":1} {"v":1}`,
		"wrong shape": `{"v":1,"entries":{"a":1}}`,
		"array":       `[]`,
		"bad nested":  `{"v":1,"entries":[{"client":5}]}`,
	} {
		e.write(e.lay.Manifest, content)
		if _, err := LoadManifest(e.fs, e.lay.Manifest); !errors.Is(err, model.ErrSettingsParse) {
			t.Errorf("%s: err = %v, want ErrSettingsParse", name, err)
		}
	}
}

func TestLoadManifestUnreadableIsIOError(t *testing.T) {
	e := newTenv(t)
	if err := os.MkdirAll(e.lay.Manifest, 0o755); err != nil { // a directory where the file should be
		t.Fatal(err)
	}
	_, err := LoadManifest(e.fs, e.lay.Manifest)
	if err == nil || errors.Is(err, model.ErrSettingsParse) {
		t.Fatalf("err = %v, want a plain I/O error", err)
	}
}

func TestSaveManifestRoundTripAndPerms(t *testing.T) {
	e := newTenv(t)
	m := &model.InstallManifest{
		V: 1, Binary: "/b", Updated: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Entries: []model.ManagedEntry{
			{Client: model.ClientCursor, File: "/c", Native: "stop", Key: "hooks.stop[0]", Hash: "h2", Command: "c2"},
			{Client: model.ClientClaude, File: "/a", Native: "Stop", Key: "k", Hash: "h1", Command: "/b", Args: []string{"hook"}},
			{Client: model.ClientClaude, File: "/a", Native: "SessionStart", Key: "k0", Hash: "h0", Command: "/b"},
		},
		Files:   []model.ManagedFile{{Client: model.ClientCursor, Path: "/m", Hash: "x", Created: true, Whole: true}},
		Flags:   []model.ManagedFlag{{Client: model.ClientCodex, File: "/t", Key: "features.hooks", Value: "true", SetByBallast: true}},
		Backups: []model.BackupRecord{{Dir: "/bk", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Files: []string{"/a"}}},
	}
	if err := SaveManifest(e.fs, e.ids, e.lay.Manifest, m); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(e.lay.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeIsUnix() && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	got, err := LoadManifest(e.fs, e.lay.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	// stored in canonical order: client order, then file, then native table order
	if got.Entries[0].Native != "SessionStart" || got.Entries[1].Native != "Stop" || got.Entries[2].Client != model.ClientCursor {
		t.Errorf("entries not sorted: %+v", got.Entries)
	}
	if got.Binary != "/b" || !got.Updated.Equal(m.Updated) || len(got.Files) != 1 || len(got.Flags) != 1 || len(got.Backups) != 1 || !got.Flags[0].SetByBallast {
		t.Errorf("manifest changed in the round trip: %+v", got)
	}
	// saving twice yields identical bytes
	before, _ := os.ReadFile(e.lay.Manifest)
	_ = SaveManifest(e.fs, e.ids, e.lay.Manifest, got)
	after, _ := os.ReadFile(e.lay.Manifest)
	if string(before) != string(after) {
		t.Error("save is not deterministic")
	}
	// a manifest without V is saved with the current version
	if err := SaveManifest(e.fs, e.ids, e.lay.Manifest, &model.InstallManifest{}); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadManifest(e.fs, e.lay.Manifest); err != nil || got.V != model.ManifestVersion {
		t.Errorf("V = %d, %v", got.V, err)
	}
	// no temp files are left behind
	ents, _ := os.ReadDir(filepath.Dir(e.lay.Manifest))
	for _, en := range ents {
		if en.Name() != filepath.Base(e.lay.Manifest) {
			t.Errorf("stray file %s", en.Name())
		}
	}
}

func TestSaveManifestFailureLeavesOldFile(t *testing.T) {
	e := newTenv(t)
	_ = SaveManifest(e.fs, e.ids, e.lay.Manifest, &model.InstallManifest{V: 1, Binary: "/old"})
	faulty := sys.FaultFS{FS: e.fs, Fail: map[string]string{"Rename": "install-manifest"}, Err: errors.New("boom")}
	if err := SaveManifest(faulty, e.ids, e.lay.Manifest, &model.InstallManifest{V: 1, Binary: "/new"}); err == nil {
		t.Fatal("expected an error")
	}
	got, _ := LoadManifest(e.fs, e.lay.Manifest)
	if got.Binary != "/old" {
		t.Error("failed save must not damage the existing manifest")
	}
	ents, _ := os.ReadDir(filepath.Dir(e.lay.Manifest))
	if len(ents) != 1 {
		t.Errorf("temp file left behind: %d entries", len(ents))
	}
}

func TestMergeRecordsReplacesOnlyPlannedFiles(t *testing.T) {
	old := &model.InstallManifest{V: 1,
		Entries: []model.ManagedEntry{
			{Client: model.ClientClaude, File: "/a", Native: "Stop", Hash: "old"},
			{Client: model.ClientCursor, File: "/c", Native: "stop", Hash: "keep"},
		},
		Files: []model.ManagedFile{{Client: model.ClientClaude, Path: "/a"}, {Client: model.ClientCursor, Path: "/c"}},
		Flags: []model.ManagedFlag{{Client: model.ClientCodex, File: "/t", Key: "k"}},
	}
	plan := &PlanResult{Path: "/a", Entries: []model.ManagedEntry{{Client: model.ClientClaude, File: "/a", Native: "Stop", Hash: "new"}},
		Files: []model.ManagedFile{{Client: model.ClientClaude, Path: "/a", Created: true}}}
	out := mergeRecords(old, []plannedFile{{model.ClientClaude, plan}})
	if len(out.Entries) != 2 || out.Entries[0].Hash != "new" || out.Entries[1].Hash != "keep" {
		t.Errorf("entries = %+v", out.Entries)
	}
	if len(out.Flags) != 1 || len(out.Files) != 2 || !out.Files[0].Created {
		t.Errorf("files/flags = %+v %+v", out.Files, out.Flags)
	}
	if len(old.Entries) != 2 || old.Entries[0].Hash != "old" {
		t.Error("mergeRecords must not mutate its input")
	}
}
