package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

type probeResult struct {
	kind Kind
	err  error
}

var (
	probeMu    sync.Mutex
	probeCache = map[string]probeResult{}
)

// Probe returns the available backend for this host, or KindNone with the
// reason. Cached per process (per GOOS and backend executable, so tests that
// point Options at different executables do not share a result).
//
// Probe runs the backend once with a trivial command and a throw-away
// directory that must be denied, and requires the allowed write to succeed
// and the denied writes (outside W, and a protected path inside W) to fail.
func Probe(opt Options) (Kind, error) {
	goos := opt.goos()
	var kind Kind
	var exe string
	switch goos {
	case "darwin":
		kind, exe = KindSandboxExec, opt.sandboxExec()
	case "linux":
		kind = KindBwrap
		p, err := opt.bwrapPath()
		if err != nil {
			return KindNone, fmt.Errorf("sandbox: %v", err)
		}
		exe = p
	default:
		return KindNone, fmt.Errorf("sandbox: no backend on %s", goos)
	}
	key := goos + "|" + exe
	probeMu.Lock()
	defer probeMu.Unlock()
	if r, ok := probeCache[key]; ok {
		return r.kind, r.err
	}
	k, err := runProbe(opt, kind, exe)
	probeCache[key] = probeResult{k, err}
	return k, err
}

func runProbe(opt Options, kind Kind, exe string) (Kind, error) {
	fsys := opt.fs()
	if _, err := fsys.Stat(exe); err != nil {
		return KindNone, fmt.Errorf("sandbox: %s: %v", exe, err)
	}
	base, err := os.MkdirTemp("", "ballast-probe-")
	if err != nil {
		return KindNone, fmt.Errorf("sandbox: probe dir: %v", err)
	}
	defer os.RemoveAll(base)
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	w := filepath.Join(base, "w")
	denied := filepath.Join(w, "denied")
	outside := filepath.Join(base, "outside")
	tmp := filepath.Join(base, "tmp")
	for _, d := range []string{denied, outside, tmp, filepath.Join(base, "home")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return KindNone, fmt.Errorf("sandbox: probe dir: %v", err)
		}
	}
	spec := Spec{
		AllowWrite: []string{w},
		DenyWrite:  []string{denied},
		TmpDir:     tmp,
		Network:    model.NetworkDeny,
		Home:       filepath.Join(base, "home"),
		Cwd:        w,
	}
	script := `echo ok > "$1/ok"; echo no > "$1/denied/no"; echo no > "$2/no"; exit 0`
	argv := []string{"/bin/sh", "-c", script, "sh", w, outside}
	o := opt
	o.FS = fsys
	if kind == KindBwrap {
		o.Bwrap = exe
	} else {
		o.SandboxExec = exe
	}
	wrapped, err := Wrap(o, kind, spec, argv)
	if err != nil {
		return KindNone, fmt.Errorf("sandbox: probe: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, wrapped.Argv[0], wrapped.Argv[1:]...)
	cmd.Env = applyEnv(os.Environ(), wrapped.Env)
	cmd.Stdin = nil
	setsid(cmd)
	out, runErr := cmd.CombinedOutput()
	if b, err := os.ReadFile(filepath.Join(w, "ok")); err != nil || strings.TrimSpace(string(b)) != "ok" {
		return KindNone, fmt.Errorf("sandbox: %s probe: write inside the writable root failed (%v): %s",
			kind, errors.Join(runErr, err), trimOut(out))
	}
	if _, err := os.Lstat(filepath.Join(denied, "no")); err == nil {
		return KindNone, fmt.Errorf("sandbox: %s probe: write to a protected path succeeded", kind)
	}
	if _, err := os.Lstat(filepath.Join(outside, "no")); err == nil {
		return KindNone, fmt.Errorf("sandbox: %s probe: write outside the writable roots succeeded", kind)
	}
	return kind, nil
}

func trimOut(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// applyEnv forms a process environment the way the runner does for the
// backend: base minus every LD_* / DYLD_* variable, then env applied ("K=V"
// sets, a bare "K" unsets).
func applyEnv(base, env []string) []string {
	out := make([]string, 0, len(base)+len(env))
	idx := map[string]int{}
	set := func(kv string) {
		k := envKey(kv)
		if strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "DYLD_") {
			return
		}
		if i, ok := idx[k]; ok {
			out[i] = kv
			return
		}
		idx[k] = len(out)
		out = append(out, kv)
	}
	for _, kv := range base {
		set(kv)
	}
	for _, kv := range env {
		if !strings.Contains(kv, "=") {
			if i, ok := idx[kv]; ok {
				out[i] = ""
			}
			continue
		}
		set(kv)
	}
	res := out[:0]
	for _, kv := range out {
		if kv != "" {
			res = append(res, kv)
		}
	}
	return res
}
