package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func isNotDir(err error) bool { return errors.Is(err, syscall.ENOTDIR) }

// isMissing is git's is_missing_file_error: ENOENT or ENOTDIR.
func isMissing(err error) bool { return errors.Is(err, fs.ErrNotExist) || isNotDir(err) }

func (l *Locator) fsys() sys.FS {
	if l.FS == nil {
		return sys.OSFS{}
	}
	return l.FS
}

// locate implements Locator.Locate.
func (l *Locator) locate(cwd string) (*model.RepoInfo, error) {
	fsys := l.fsys()
	if cwd == "" || !filepath.IsAbs(cwd) {
		return nil, fmt.Errorf("snapshot: locate: cwd %q is not absolute", cwd)
	}
	real, err := fsys.EvalSymlinks(cwd)
	if err != nil {
		if isMissing(err) {
			return nil, fmt.Errorf("%w: %v", model.ErrNotRepo, err)
		}
		return nil, err
	}
	for dir := real; ; {
		gitDir, found, err := candidateGitDir(fsys, dir)
		if err != nil {
			return nil, err
		}
		if found {
			if isUnder(real, gitDir) {
				return nil, fmt.Errorf("%w: %s is inside the git directory", model.ErrNotRepo, cwd)
			}
			return buildRepoInfo(fsys, dir, gitDir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("%w: %s", model.ErrNotRepo, cwd)
		}
		dir = parent
	}
}

// candidateGitDir inspects dir/.git. found is true when it names a usable git
// directory (resolved to its realpath).
func candidateGitDir(fsys sys.FS, dir string) (gitDir string, found bool, err error) {
	dotGit := filepath.Join(dir, ".git")
	fi, err := fsys.Lstat(dotGit)
	if err != nil {
		if isMissing(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		if fi, err = fsys.Stat(dotGit); err != nil {
			if isMissing(err) {
				return "", false, nil
			}
			return "", false, err
		}
	}
	if fi.IsDir() {
		if _, err := fsys.Stat(filepath.Join(dotGit, "HEAD")); err != nil {
			if isMissing(err) {
				return "", false, nil // not a git directory: keep walking up
			}
			return "", false, err
		}
		real, err := fsys.EvalSymlinks(dotGit)
		if err != nil {
			return "", false, err
		}
		return real, true, nil
	}
	data, err := fsys.ReadFile(dotGit)
	if err != nil {
		return "", false, err
	}
	line := strings.TrimSpace(string(data))
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", false, fmt.Errorf("%w: %s is not a gitdir file", model.ErrNotRepo, dotGit)
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, filepath.FromSlash(target))
	}
	real, err := fsys.EvalSymlinks(target)
	if err != nil {
		if isMissing(err) {
			return "", false, fmt.Errorf("%w: %s points to a missing git directory", model.ErrNotRepo, dotGit)
		}
		return "", false, err
	}
	return real, true, nil
}

func buildRepoInfo(fsys sys.FS, top, gitDir string) (*model.RepoInfo, error) {
	info := &model.RepoInfo{
		Toplevel:     top,
		GitDir:       gitDir,
		CommonDir:    gitDir,
		IndexPath:    filepath.Join(gitDir, "index"),
		ObjectFormat: "sha1",
	}
	if data, err := fsys.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		p := strings.TrimSpace(string(data))
		if p != "" {
			if !filepath.IsAbs(p) {
				p = filepath.Join(gitDir, filepath.FromSlash(p))
			}
			if real, err := fsys.EvalSymlinks(p); err == nil {
				info.CommonDir = real
			}
		}
	}
	info.Inst = RepoInst(info.Toplevel, info.CommonDir)
	if ref, ok := readHead(fsys, gitDir); ok {
		info.HeadRef = ref
	}
	// One cheap scan of the common config: object format and origin url.
	if data, err := fsys.ReadFile(filepath.Join(info.CommonDir, "config")); err == nil {
		haveURL := false
		scanConfig(data, func(section, sub, key, raw string, hasEq bool) {
			switch {
			case section == "extensions" && sub == "" && key == "objectformat" && hasEq:
				if v := strings.ToLower(cfgString(raw)); v != "" {
					info.ObjectFormat = v
				}
			case !haveURL && section == "remote" && sub == "origin" && key == "url" && hasEq && raw != "":
				sum := sha256.Sum256([]byte(raw))
				info.RemoteHash = hex.EncodeToString(sum[:8])
				haveURL = true
			}
		})
	}
	return info, nil
}

// readHead returns the symbolic ref of HEAD ("refs/heads/main"); ok is false
// when HEAD is unreadable. A detached HEAD yields ("", true).
func readHead(fsys sys.FS, gitDir string) (ref string, ok bool) {
	data, err := fsys.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	if target, isRef := strings.CutPrefix(s, "ref:"); isRef {
		target = strings.TrimSpace(target)
		if target == "" || target == "refs/heads/.invalid" { // reftable stub
			return "", false
		}
		return target, true
	}
	if isHexID(s) {
		return "", true
	}
	return "", false
}

func isHexID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// branchOf implements Branch.
func branchOf(fsys sys.FS, repo *model.RepoInfo) (string, model.Tri) {
	if repo == nil || repo.GitDir == "" {
		return "", model.TriUnknown
	}
	ref, ok := readHead(fsys, repo.GitDir)
	if !ok {
		return "", model.TriUnknown
	}
	if ref == "" {
		return "", model.TriFalse
	}
	name, isHead := strings.CutPrefix(ref, "refs/heads/")
	if !isHead || name == "" {
		return "", model.TriUnknown
	}
	return name, model.TriTrue
}
