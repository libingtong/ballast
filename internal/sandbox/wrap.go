package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

const (
	defaultSandboxExec = "/usr/bin/sandbox-exec"
	bwrapName          = "bwrap"
)

func (o Options) fs() sys.FS {
	if o.FS != nil {
		return o.FS
	}
	return sys.OSFS{}
}

func (o Options) env() sys.Env {
	if o.Env != nil {
		return o.Env
	}
	return sys.OSEnv{}
}

func (o Options) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

func (o Options) sandboxExec() string {
	if o.SandboxExec != "" {
		return o.SandboxExec
	}
	return defaultSandboxExec
}

// bwrapPath returns the bwrap to use: Options.Bwrap, or the first "bwrap" on
// PATH that is a regular executable owned by root and writable by neither
// group nor others (an agent-planted fake bwrap earlier on PATH is skipped).
func (o Options) bwrapPath() (string, error) {
	if o.Bwrap != "" {
		return o.Bwrap, nil
	}
	fsys := o.fs()
	path, _ := o.env().Getenv("PATH")
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, bwrapName)
		fi, err := fsys.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		if fi.Mode().Perm()&0o022 != 0 {
			continue
		}
		if uid, ok := fileUID(fi); !ok || uid != 0 {
			continue
		}
		return p, nil
	}
	return "", errors.New("no trusted bwrap on PATH (root-owned, not group/other-writable)")
}

// resolveSpec returns spec with the writable roots and Cwd symlink-resolved
// (SBPL and bind mounts act on the resolved path: /var is /private/var on
// macOS), and with the resolved forms of DenyWrite entries added next to the
// raw ones.
func resolveSpec(fsys sys.FS, spec Spec) Spec {
	rs := func(ps []string) []string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, resolvePartial(fsys, p))
		}
		return dedupe(out)
	}
	r := spec
	r.AllowWrite = rs(spec.AllowWrite)
	r.RepoRoots = rs(spec.RepoRoots)
	r.GitDirs = rs(spec.GitDirs)
	r.Cwd = resolvePartial(fsys, spec.Cwd)
	if spec.TmpDir != "" {
		r.TmpDir = resolvePartial(fsys, spec.TmpDir)
	}
	if spec.Tree != "" {
		r.Tree = resolvePartial(fsys, spec.Tree)
	}
	if spec.WorkRoot != "" {
		r.WorkRoot = resolvePartial(fsys, spec.WorkRoot)
	}
	r.DenyWrite = dedupe(append(append([]string(nil), spec.DenyWrite...), rs(spec.DenyWrite)...))
	var globs []string
	for _, g := range spec.DenyWriteGlobs {
		globs = append(globs, g)
		globs = append(globs, filepath.Join(resolvePartial(fsys, filepath.Dir(g)), filepath.Base(g)))
	}
	r.DenyWriteGlobs = dedupe(globs)
	return r
}

// envUnset returns the bare-key entries removing every LD_* / DYLD_* variable
// currently set in env.
func loaderUnsets(env sys.Env) []string {
	var out []string
	for _, kv := range env.Environ() {
		k := envKey(kv)
		if strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "DYLD_") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

// wrapEnv builds Wrapped.Env.
func wrapEnv(opt Options, spec Spec, tmp string) []string {
	env := opt.env()
	out := []string{"TMPDIR=" + tmp}
	out = append(out, "SSH_AUTH_SOCK", "DOCKER_HOST", "DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS")
	out = append(out, loaderUnsets(env)...)
	cur := func(k string) string {
		v, _ := env.Getenv(k)
		return v
	}
	out = append(out, CacheEnv(tmp, spec.AllowWrite, cur)...)
	if spec.Tree != "" {
		out = append(out, "GIT_CEILING_DIRECTORIES="+filepath.Dir(filepath.Clean(spec.Tree)))
	}
	return out
}

// Wrap returns the argv that runs argv inside backend kind (KindNone ⇒
// argv unchanged). It calls ValidateSpec(opt.FS, spec) first and fills
// Wrapped.Baseline with ScanMandatory.
func Wrap(opt Options, kind Kind, spec Spec, argv []string) (Wrapped, error) {
	if len(argv) == 0 {
		return Wrapped{}, errors.New("sandbox: empty argv")
	}
	if kind == KindNone || kind == "" {
		return Wrapped{Kind: KindNone, Argv: append([]string(nil), argv...)}, nil
	}
	fsys := opt.fs()
	if err := ValidateSpec(fsys, spec); err != nil {
		return Wrapped{}, err
	}
	rs := resolveSpec(fsys, spec)
	baseline, err := ScanMandatory(fsys, rs)
	if err != nil {
		return Wrapped{}, err
	}
	w := Wrapped{Kind: kind, Baseline: baseline, NewSession: true, ids: fingerprints(fsys, baseline)}
	switch kind {
	case KindSandboxExec:
		if rs.TmpDir == "" {
			return Wrapped{}, invalid("darwin needs Spec.TmpDir")
		}
		w.Argv = append([]string{opt.sandboxExec(), "-p", DarwinProfile(rs), "--"}, argv...)
		w.Env = wrapEnv(opt, rs, rs.TmpDir)
	case KindBwrap:
		exe, err := opt.bwrapPath()
		if err != nil {
			return Wrapped{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		existing := map[string]bool{}
		mark := func(p string) {
			p = filepath.Clean(p)
			if _, done := existing[p]; done {
				return
			}
			fi, lerr := fsys.Lstat(p)
			existing[p] = lerr == nil
			if lerr == nil && fi.IsDir() {
				existing[p+"/"] = true
			}
		}
		for _, p := range rs.AllowWrite {
			mark(p)
		}
		for _, p := range rs.DenyWrite {
			mark(p)
		}
		for _, g := range rs.DenyWriteGlobs {
			mark(filepath.Dir(g))
		}
		for _, p := range AncestorPins(rs) {
			mark(p)
		}
		for _, p := range baseline {
			mark(p)
		}
		if rs.Tree != "" {
			mark(rs.Tree)
		}
		// Explicit protected paths inside W that do not exist yet cannot be
		// bound without creating a mount point on the host: post-check them.
		var missing []string
		for _, d := range rs.DenyWrite {
			if withinAny(d, rs.AllowWrite) && !existing[d] {
				missing = append(missing, d)
			}
		}
		for _, g := range rs.DenyWriteGlobs {
			if dir := filepath.Dir(g); withinAny(dir, rs.AllowWrite) && !existing[dir] {
				missing = append(missing, dir)
			}
		}
		sort.Strings(missing)
		w.MissingProtected = dedupe(missing)
		args := append([]string{exe}, socketDirArgs(fsys, BwrapArgs(rs, existing), "/run", "/var/run")...)
		args = append(args, "--")
		w.Argv = append(args, argv...)
		w.Env = wrapEnv(opt, rs, "/tmp")
	default:
		return Wrapped{}, fmt.Errorf("%w: unknown backend %q", ErrUnavailable, kind)
	}
	return w, nil
}

// Decide applies the runner policy (docs/p2-design.md §3.4 table):
//   - origin == model.OriginHook: the sandbox is mandatory whatever policy
//     says; no backend ⇒ ErrUnavailable;
//   - origin == model.OriginCLI: SandboxRequire and no backend ⇒
//     ErrUnavailable; SandboxPrefer ⇒ avail (possibly KindNone);
//     SandboxOff ⇒ KindNone.
//
// Fail-closed details beyond the table: an origin other than cli (and not
// empty, which is how pre-P2 records spell cli) is treated like hook, and an
// unknown or empty policy like require.
func Decide(policy model.SandboxPolicy, origin model.VerifyOrigin, avail Kind, availErr error) (Kind, error) {
	usable := availErr == nil && avail != "" && avail != KindNone
	unavailable := func() (Kind, error) {
		if availErr != nil {
			return KindNone, fmt.Errorf("%w: %v", ErrUnavailable, availErr)
		}
		return KindNone, ErrUnavailable
	}
	if origin == model.OriginCLI || origin == "" {
		switch policy {
		case model.SandboxOff:
			return KindNone, nil
		case model.SandboxPrefer:
			if usable {
				return avail, nil
			}
			return KindNone, nil
		}
	}
	// hook (any policy), and cli with require / unknown policy.
	if usable {
		return avail, nil
	}
	return unavailable()
}

func fingerprints(fsys sys.FS, paths []string) map[string]fileKey {
	m := make(map[string]fileKey, len(paths))
	for _, p := range paths {
		if fi, err := fsys.Lstat(p); err == nil {
			if k, ok := keyOf(fi); ok {
				m[p] = k
			}
		}
	}
	return m
}

// socketDirArgs adapts the frozen "--tmpfs /run --tmpfs /var/run" pair to the
// host: on Debian 12 and most other distributions /var/run is a symlink to
// /run, and bubblewrap (0.8.0) cannot mount a tmpfs on such a destination
// ("Can't mount tmpfs on /newroot/var/run: No such file or directory"). The
// directory is masked once through its real path, which is the same thing:
// a symlink is replaced by the resolved path (dropped when that is already
// masked), a missing directory is dropped (there is nothing to mask).
func socketDirArgs(fsys sys.FS, args []string, dirs ...string) []string {
	masked := map[string]bool{}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--tmpfs" && i+1 < len(args) && contains(dirs, args[i+1]) {
			target := args[i+1]
			i++
			fi, err := fsys.Lstat(target)
			if err != nil {
				continue // nothing to mask
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				real, rerr := fsys.EvalSymlinks(target)
				if rerr != nil {
					continue
				}
				target = real
			}
			if masked[target] || withinAny(target, keys(masked)) {
				continue
			}
			masked[target] = true
			out = append(out, "--tmpfs", target)
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
