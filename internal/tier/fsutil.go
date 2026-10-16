package tier

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/libingtong/ballast/internal/sys"
)

// ownerFunc is the signature of Options.Owner.
type ownerFunc = func(fi fs.FileInfo) (uint32, bool)

func ownerOrDefault(o ownerFunc) ownerFunc {
	if o == nil {
		return defaultOwner
	}
	return o
}

// trustedPerm reports whether neither group nor others can write.
func trustedPerm(fi fs.FileInfo) bool { return fi.Mode().Perm()&0o022 == 0 }

// rootOwned reports uid 0 ownership; an unknown owner is "not root".
func rootOwned(fi fs.FileInfo, owner ownerFunc) bool {
	uid, ok := owner(fi)
	return ok && uid == 0
}

// trustedFile Lstats p and requires a regular file that is root-owned and not
// group/other-writable. The error is non-nil only when Lstat failed for a
// reason other than absence (callers decide whether that is "unreadable").
func trustedFile(fsys sys.FS, owner ownerFunc, p string) (fi fs.FileInfo, ok bool, err error) {
	fi, err = fsys.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !fi.Mode().IsRegular() || !rootOwned(fi, owner) || !trustedPerm(fi) {
		return fi, false, nil
	}
	return fi, true, nil
}

// trustedDir Lstats d and requires a real directory (not a symlink) that is
// root-owned and not group/other-writable.
func trustedDir(fsys sys.FS, owner ownerFunc, d string) (ok bool, err error) {
	fi, err := fsys.Lstat(d)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !fi.IsDir() || !rootOwned(fi, owner) || !trustedPerm(fi) {
		return false, nil
	}
	return true, nil
}

// trustedChain checks d and every ancestor up to "/" with trustedDir. The
// paths handled here are slash-separated (T1 exists only on darwin and
// linux) and must already be free of symlinks.
func trustedChain(fsys sys.FS, owner ownerFunc, d string) (bool, error) {
	ok, _, err := chainCheck(fsys, owner, d)
	return ok, err
}

// chainCheck is trustedChain that also reports whether it met a symbolic link
// (sawLink; the chain is then not ok and callers may retry on the
// EvalSymlinks form). One Lstat per path component.
func chainCheck(fsys sys.FS, owner ownerFunc, d string) (ok, sawLink bool, err error) {
	if !strings.HasPrefix(d, "/") {
		return false, false, nil
	}
	for {
		fi, err := fsys.Lstat(d)
		if err != nil {
			if isNotExist(err) {
				return false, false, nil
			}
			return false, false, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return false, true, nil
		}
		if !fi.IsDir() || !rootOwned(fi, owner) || !trustedPerm(fi) {
			return false, false, nil
		}
		parent := path.Dir(d)
		if parent == d {
			return true, false, nil
		}
		d = parent
	}
}

// fileTrusted: regular file, root-owned, not group/other-writable.
func fileTrusted(fi fs.FileInfo, owner ownerFunc) bool {
	return fi.Mode().IsRegular() && rootOwned(fi, owner) && trustedPerm(fi)
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// maxSettingsBytes bounds every settings file Detect and the repo-layer
// check read. Real settings files are a few hundred bytes; the bound only
// stops a hostile or accidental /dev/zero, FIFO or giant file from hanging
// or exhausting a hook that must answer in milliseconds.
const maxSettingsBytes = 1 << 20

var (
	errNotRegularFile = errors.New("not a regular file")
	errFileTooLarge   = errors.New("file too large")
)

// readSettings reads a settings file after a Stat (which follows symlinks,
// as Claude does) proved it is a regular file of at most maxSettingsBytes.
// Anything else is an error, which every caller maps to its "unreadable"
// (conservative) outcome. The Stat-to-read window is a registered residual
// (docs/p2-design.md §11): closing it needs a non-blocking open on the sys.FS
// seam.
func readSettings(fsys sys.FS, p string) ([]byte, error) {
	fi, err := fsys.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("tier: %s: %w", path.Base(p), errNotRegularFile)
	}
	if fi.Size() > maxSettingsBytes {
		return nil, fmt.Errorf("tier: %s: %w", path.Base(p), errFileTooLarge)
	}
	b, err := fsys.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(b) > maxSettingsBytes {
		return nil, fmt.Errorf("tier: %s: %w", path.Base(p), errFileTooLarge)
	}
	return b, nil
}
