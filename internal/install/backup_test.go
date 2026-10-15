package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func TestEncodeBackupName(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/.claude/settings.json": "%2Fhome%2Fu%2F.claude%2Fsettings.json",
		`C:\Users\u\x.json`:             "C%3A%2FUsers%2Fu%2Fx.json",
		"/a%2Fb":                        "%2Fa%252Fb",
		"relative.json":                 "relative.json",
	} {
		if got := EncodeBackupName(in); got != want {
			t.Errorf("EncodeBackupName(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(EncodeBackupName(in), `/\:`) {
			t.Errorf("%q: encoded name still contains a separator", in)
		}
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
