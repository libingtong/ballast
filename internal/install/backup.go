package install

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// MaxBackups is the number of backup directories kept.
const MaxBackups = 10

const backupTimeLayout = "20060102T150405Z"

// EncodeBackupName turns an absolute path into a backup file name: path
// separators become "%2F". To keep the name reversible and valid on Windows,
// '%' is escaped as "%25" and the drive colon as "%3A".
func EncodeBackupName(p string) string {
	drive := ""
	if len(p) >= 2 && p[1] == ':' {
		drive, p = p[:1]+"%3A", p[2:]
	}
	r := strings.NewReplacer("%", "%25", "/", "%2F", `\`, "%2F")
	return drive + r.Replace(p)
}

func isBackupDirName(n string) bool {
	l := len(backupTimeLayout)
	if len(n) != l+7 || n[l] != '-' {
		return false
	}
	if _, err := time.Parse(backupTimeLayout, n[:l]); err != nil {
		return false
	}
	for _, c := range n[l+1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Backup copies files into a new backup directory and rotates (keep 10).
// Files that do not exist are skipped. The directory is
// <Layout.BackupsDir>/<UTC yyyymmddThhmmssZ>-<rand6> (0700), files 0600.
func Backup(opt Options, files []string) (*model.BackupRecord, error) {
	lay, fsys := opt.Layout, opt.FS
	type item struct {
		path string
		data []byte
	}
	var items []item
	for _, f := range files {
		b, err := fsys.ReadFile(f)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("backup %s: %w", f, err)
		}
		items = append(items, item{f, b})
	}
	if len(items) == 0 {
		return nil, nil
	}
	now := opt.Clock.Now().UTC()
	var dir string
	for attempt := 0; ; attempt++ {
		dir = filepath.Join(lay.BackupsDir, now.Format(backupTimeLayout)+"-"+opt.IDs.NewID(3))
		if _, err := fsys.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			break
		}
		if attempt > 32 {
			return nil, fmt.Errorf("backup: cannot allocate a directory under %s", lay.BackupsDir)
		}
	}
	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	rec := &model.BackupRecord{Dir: dir, At: now}
	for _, it := range items {
		dst := filepath.Join(dir, EncodeBackupName(it.path))
		if err := sys.WriteFileAtomic(fsys, opt.IDs, dst, it.data, 0o600, true); err != nil {
			_ = fsys.RemoveAll(dir)
			return nil, fmt.Errorf("backup %s: %w", it.path, err)
		}
		rec.Files = append(rec.Files, it.path)
	}
	rotateBackups(fsys, lay.BackupsDir)
	return rec, nil
}

// rotateBackups keeps the MaxBackups newest backup directories.
func rotateBackups(fsys sys.FS, root string) {
	ents, err := fsys.ReadDir(root)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() && isBackupDirName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > MaxBackups {
		_ = fsys.RemoveAll(filepath.Join(root, names[0]))
		names = names[1:]
	}
}

// pruneBackupRecords drops records whose directory no longer exists and
// keeps at most MaxBackups, most recent first.
func pruneBackupRecords(fsys sys.FS, recs []model.BackupRecord) []model.BackupRecord {
	var out []model.BackupRecord
	for _, r := range recs {
		if fi, err := fsys.Stat(r.Dir); err == nil && fi.IsDir() {
			out = append(out, r)
		}
		if len(out) == MaxBackups {
			break
		}
	}
	return out
}
