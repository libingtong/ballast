package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func TestEncodeBackupName(t *testing.T) {
	for _, in := range []string{
		"/home/u/.claude/settings.json",
		`C:\Users\u\x.json`,
		"/a%2Fb",
		"/opt/a:b/c",
		"relative.json",
		"/" + strings.Repeat("d", 300) + "/settings.json",
		"/x/" + strings.Repeat("é", 200),
	} {
		got := EncodeBackupName(in)
		if got != EncodeBackupName(in) {
			t.Errorf("%q: not deterministic", in)
		}
		if strings.ContainsAny(got, `/\:`) {
			t.Errorf("%q: encoded name %q contains a separator", in, got)
		}
		if len(got) > 16+1+maxBackupBase {
			t.Errorf("%q: encoded name is %d bytes", in, len(got))
		}
		if !utf8.ValidString(got) {
			t.Errorf("%q: encoded name is not valid UTF-8", in)
		}
	}
	if got := EncodeBackupName("/home/u/.claude/settings.json"); !strings.HasSuffix(got, "-settings.json") || len(got) != 16+1+len("settings.json") {
		t.Errorf("name = %q", got)
	}
	if EncodeBackupName("/a/settings.json") == EncodeBackupName("/b/settings.json") {
		t.Error("same basename in different directories must not collide")
	}
	if got := EncodeBackupName(`C:\Users\u\x.json`); !strings.HasSuffix(got, "-x.json") {
		t.Errorf("windows basename: %q", got)
	}
}

func TestBackupVeryLongPaths(t *testing.T) {
	e := newTenv(t)
	// Directory names are capped at NAME_MAX, so nest several long ones to
	// exceed 300 characters for the whole path.
	long := e.sb.Home
	for i := 0; i < 3; i++ {
		long = filepath.Join(long, strings.Repeat("l", 200))
	}
	f := filepath.Join(long, "settings.json")
	if len(f) < 300 {
		t.Fatalf("path is only %d bytes", len(f))
	}
	e.write(f, "payload")
	rec, err := Backup(e.opts(), []string{f})
	if err != nil {
		t.Fatalf("backup of a %d byte path: %v", len(f), err)
	}
	got, err := os.ReadFile(filepath.Join(rec.Dir, EncodeBackupName(f)))
	if err != nil || string(got) != "payload" {
		t.Fatalf("backup content %q %v", got, err)
	}
	ib, err := os.ReadFile(filepath.Join(rec.Dir, backupIndexName))
	if err != nil {
		t.Fatal(err)
	}
	var idx map[string]string
	if err := json.Unmarshal(ib, &idx); err != nil || idx[EncodeBackupName(f)] != f {
		t.Errorf("index.json = %s (%v)", ib, err)
	}
}

func TestBackupLayoutAndPermissions(t *testing.T) {
	e := newTenv(t)
	a := filepath.Join(e.sb.Home, "a.json")
	b := filepath.Join(e.sb.Home, "sub", "b.toml")
	e.write(a, "A")
	e.write(b, "B")
	opt := e.opts()
	rec, err := Backup(opt, []string{a, filepath.Join(e.sb.Home, "missing"), b})
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || len(rec.Files) != 2 || rec.Files[0] != a || rec.Files[1] != b {
		t.Fatalf("record = %+v (missing files must be skipped)", rec)
	}
	if !rec.At.Equal(e.clock.Now()) {
		t.Errorf("At = %v", rec.At)
	}
	if filepath.Dir(rec.Dir) != e.lay.BackupsDir {
		t.Errorf("dir %s not under %s", rec.Dir, e.lay.BackupsDir)
	}
	base := filepath.Base(rec.Dir)
	if !strings.HasPrefix(base, "20261016T080000Z-") || len(base) != len("20261016T080000Z-")+6 || !isBackupDirName(base) {
		t.Errorf("dir name %q", base)
	}
	got, err := os.ReadFile(filepath.Join(rec.Dir, EncodeBackupName(a)))
	if err != nil || string(got) != "A" {
		t.Errorf("backup of a: %q %v", got, err)
	}
	got, _ = os.ReadFile(filepath.Join(rec.Dir, EncodeBackupName(b)))
	if string(got) != "B" {
		t.Errorf("backup of b: %q", got)
	}
	if runtimeIsUnix() {
		di, _ := os.Stat(rec.Dir)
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
		}
		ents, _ := os.ReadDir(rec.Dir)
		for _, en := range ents {
			fi, _ := en.Info()
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %v, want 0600", en.Name(), fi.Mode().Perm())
			}
		}
	}
	// nothing to back up ⇒ no directory
	rec, err = Backup(opt, []string{filepath.Join(e.sb.Home, "missing")})
	if err != nil || rec != nil {
		t.Errorf("rec=%v err=%v", rec, err)
	}
}

func TestBackupRotationKeepsNewestTen(t *testing.T) {
	e := newTenv(t)
	f := filepath.Join(e.sb.Home, "f.json")
	e.write(f, "x")
	opt := e.opts()
	var recs []*model.BackupRecord
	for i := 0; i < 14; i++ {
		e.clock.Advance(time.Second)
		rec, err := Backup(opt, []string{f})
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	ents, _ := os.ReadDir(e.lay.BackupsDir)
	if len(ents) != MaxBackups {
		t.Fatalf("%d backup dirs kept, want %d", len(ents), MaxBackups)
	}
	for i, r := range recs {
		_, err := os.Stat(r.Dir)
		if kept := err == nil; kept != (i >= 4) {
			t.Errorf("backup %d kept=%v", i, kept)
		}
	}
	// foreign directories next to the backups are never deleted
	foreign := filepath.Join(e.lay.BackupsDir, "keep-me")
	_ = os.MkdirAll(foreign, 0o700)
	e.clock.Advance(time.Second)
	if _, err := Backup(opt, []string{f}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("rotation must only touch ballast backup directories")
	}
	if MaxBackups != 10 {
		t.Error("MaxBackups is frozen at 10")
	}
}

func TestBackupDirCollisionPicksAnotherName(t *testing.T) {
	e := newTenv(t)
	f := filepath.Join(e.sb.Home, "f.json")
	e.write(f, "x")
	opt := e.opts()
	opt.IDs = fixedIDs("aabbcc", "aabbcc", "ddeeff")
	r1, err := Backup(opt, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Backup(opt, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Dir == r2.Dir {
		t.Fatal("two backups share a directory")
	}
}

type fixedIDGen struct {
	ids []string
	i   int
}

func fixedIDs(ids ...string) *fixedIDGen { return &fixedIDGen{ids: ids} }
func (f *fixedIDGen) NewID(n int) string {
	s := f.ids[min(f.i, len(f.ids)-1)]
	f.i++
	return s
}

func TestBackupFailureLeavesNoPartialDirectory(t *testing.T) {
	e := newTenv(t)
	f := filepath.Join(e.sb.Home, "f.json")
	e.write(f, "x")
	opt := e.opts()
	opt.FS = sys.FaultFS{FS: e.fs, Fail: map[string]string{"Rename": e.lay.BackupsDir}, Err: errors.New("disk full")}
	if _, err := Backup(opt, []string{f}); err == nil {
		t.Fatal("expected an error")
	}
	ents, _ := os.ReadDir(e.lay.BackupsDir)
	if len(ents) != 0 {
		t.Errorf("partial backup left behind: %v", ents)
	}
}

func TestPruneBackupRecords(t *testing.T) {
	e := newTenv(t)
	var recs []model.BackupRecord
	for i := 0; i < 12; i++ {
		d := filepath.Join(e.sb.Root, "bk", string(rune('a'+i)))
		_ = os.MkdirAll(d, 0o700)
		recs = append(recs, model.BackupRecord{Dir: d})
	}
	_ = os.RemoveAll(recs[1].Dir)
	got := pruneBackupRecords(e.fs, recs)
	if len(got) != MaxBackups || got[0].Dir != recs[0].Dir || got[1].Dir != recs[2].Dir {
		t.Errorf("pruned = %d, first dirs %v", len(got), got[:2])
	}
}

// descIDs yields ever smaller suffixes so the newest directory sorts first.
type descIDs struct{ n int }

func (d *descIDs) NewID(int) string { d.n++; return fmt.Sprintf("%06x", 0xffffff-d.n) }

func TestBackupRotationNeverDeletesTheNewBackup(t *testing.T) {
	for name, ids := range map[string]sys.IDGen{"random": sys.RandomID{}, "descending": &descIDs{}} {
		e := newTenv(t)
		f := filepath.Join(e.sb.Home, "f.json")
		e.write(f, "x")
		opt := e.opts()
		opt.IDs = ids
		for i := 0; i < 12; i++ { // same second every time
			rec, err := Backup(opt, []string{f})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(rec.Dir, EncodeBackupName(f))); err != nil {
				t.Fatalf("%s: backup %d vanished right after Backup returned: %v", name, i, err)
			}
		}
		if ents, _ := os.ReadDir(e.lay.BackupsDir); len(ents) != MaxBackups {
			t.Errorf("%s: %d backup dirs kept, want %d", name, len(ents), MaxBackups)
		}
	}
	// a clock that jumps back keeps the new backup too
	e := newTenv(t)
	f := filepath.Join(e.sb.Home, "f.json")
	e.write(f, "x")
	opt := e.opts()
	for i := 0; i < 11; i++ {
		e.clock.Advance(time.Hour)
		if _, err := Backup(opt, []string{f}); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.Advance(-48 * time.Hour)
	rec, err := Backup(opt, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rec.Dir); err != nil {
		t.Errorf("backup made after a clock step back was rotated away: %v", err)
	}
	if ents, _ := os.ReadDir(e.lay.BackupsDir); len(ents) != MaxBackups {
		t.Errorf("%d backup dirs kept, want %d", len(ents), MaxBackups)
	}
}
