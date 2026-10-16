package tier

import (
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// orgState is what condition 4 learns about org.json.
type orgState struct {
	// fileTrusted: org.json is a regular, root-owned, not group/other-writable
	// file that validates (its content may be trusted).
	fileTrusted bool
	org         OrgConfig
	// chainOK: <managed>/ballast and every ancestor (after EvalSymlinks of
	// the managed dir) are root-owned directories, not group/other-writable.
	chainOK bool
	// resolvedManaged is EvalSymlinks(managed) ("" when it failed).
	resolvedManaged string
	// unreadable: an I/O error other than absence happened.
	unreadable bool
}

// readOrg evaluates the file part and the parent chain of condition 4 for
// the managed directory managed (slash-separated, not yet symlink-resolved).
// Cost when org.json is absent: one Lstat.
func readOrg(fsys sys.FS, owner ownerFunc, managed string) orgState {
	var st orgState
	base := path.Join(managed, "ballast")
	orgPath := path.Join(base, "org.json")
	fi, err := fsys.Lstat(orgPath)
	if err != nil {
		st.unreadable = !isNotExist(err)
		return st
	}
	// Fast path: walk the parent chain with one Lstat per component. A
	// symbolic link anywhere in it (macOS /etc → /private/etc style) sends us
	// to the EvalSymlinks form, which is then walked the same way.
	resolved := managed
	chainOK, sawLink, cerr := chainCheck(fsys, owner, base)
	if sawLink {
		r, err := fsys.EvalSymlinks(managed)
		if err != nil {
			st.unreadable = !isNotExist(err)
			return st
		}
		resolved = r
		base = path.Join(r, "ballast")
		orgPath = path.Join(base, "org.json")
		if fi, err = fsys.Lstat(orgPath); err != nil {
			st.unreadable = !isNotExist(err)
			return st
		}
		chainOK, _, cerr = chainCheck(fsys, owner, base)
	}
	if cerr != nil {
		st.unreadable = true
	}
	st.resolvedManaged = resolved
	st.chainOK = chainOK
	if fileTrusted(fi, owner) {
		b, err := fsys.ReadFile(orgPath)
		if err != nil {
			st.unreadable = !isNotExist(err)
		} else if org, err := parseOrgConfig(b, base); err == nil {
			st.fileTrusted = true
			st.org = org
		}
	}
	return st
}

func lockedFrom(st orgState) bool {
	return st.fileTrusted && st.chainOK && st.org.Tier == model.TierT1
}

func lockState(fsys sys.FS, goos string, owner ownerFunc) (OrgConfig, bool) {
	if goos != "darwin" && goos != "linux" {
		return OrgConfig{}, false
	}
	st := readOrg(fsys, ownerOrDefault(owner), layout.ManagedDirFor(goos))
	if !lockedFrom(st) {
		return OrgConfig{}, false
	}
	return st.org, true
}

// lockedHome implements LockedHome.
func lockedHome(opt HomeOptions) (home, source string, err error) {
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = DefaultLockedHomeTimeout
	}
	lookup := opt.Lookup
	if lookup == nil {
		lookup = sys.LookupHome
	}
	type result struct {
		home string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		h, e := lookup(opt.Euid)
		ch <- result{h, e}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var r result
	select {
	case r = <-ch:
	case <-timer.C:
		return "", "", ErrLockedHomeTimeout
	}
	if r.err == nil {
		// An empty or relative answer is no answer; never trust it.
		if r.home == "" || !path.IsAbs(r.home) {
			return "", "", fmt.Errorf("tier: passwd home %q is not an absolute path", r.home)
		}
		return r.home, HomeFromPasswd, nil
	}
	if !errors.Is(r.err, sys.ErrNoPasswdEntry) {
		return "", "", fmt.Errorf("tier: passwd lookup for uid %d failed: %w", opt.Euid, r.err)
	}
	if len(opt.HomeRoot) == 0 {
		return "", "", fmt.Errorf("tier: no passwd entry for uid %d and org.json has no home_root: %w", opt.Euid, r.err)
	}
	h, err := homeRootRule(opt)
	if err != nil {
		return "", "", err
	}
	return h, HomeFromHomeRoot, nil
}

// homeRootRule applies clauses (a)-(d) of LockedHome rule 3.
func homeRootRule(opt HomeOptions) (string, error) {
	owner := ownerOrDefault(opt.Owner)
	if opt.Env == nil {
		return "", errors.New("tier: home_root rule (a): no environment")
	}
	h, herr := opt.Env.HomeDir()
	if herr != nil || h == "" {
		return "", errors.New("tier: home_root rule (a): $HOME is not set")
	}
	if !path.IsAbs(h) || path.Clean(h) != h {
		return "", fmt.Errorf("tier: home_root rule (a): $HOME %q is not an absolute, clean path", h)
	}
	parent := path.Dir(h)
	matched := false
	for _, r := range opt.HomeRoot {
		if r == parent {
			matched = true
			break
		}
	}
	if !matched {
		return "", fmt.Errorf("tier: home_root rule (b): the parent %q of $HOME is not a home_root entry", parent)
	}
	ok, err := trustedChain(opt.FS, owner, parent)
	if err != nil {
		return "", fmt.Errorf("tier: home_root rule (c): %q: %w", parent, err)
	}
	if !ok {
		return "", fmt.Errorf("tier: home_root rule (c): %q or one of its ancestors is not a root-owned, non-writable directory", parent)
	}
	fi, err := opt.FS.Lstat(h)
	if err != nil {
		return "", fmt.Errorf("tier: home_root rule (d): %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("tier: home_root rule (d): $HOME %q is not a directory (or is a symlink)", h)
	}
	uid, known := owner(fi)
	if !known || int64(uid) != int64(opt.Euid) {
		return "", fmt.Errorf("tier: home_root rule (d): $HOME %q is not owned by the effective uid", h)
	}
	if !trustedPerm(fi) {
		return "", fmt.Errorf("tier: home_root rule (d): $HOME %q is group/other-writable", h)
	}
	return h, nil
}
