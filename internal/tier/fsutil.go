package tier

import (
	"errors"
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
