package sandbox

import (
	"path/filepath"
	"strings"
)

// within reports whether p equals root or lies below it (both Clean,
// absolute). Separator aware: "/a/bc" is not inside "/a/b".
func within(p, root string) bool {
	if p == root {
		return true
	}
	if root == string(filepath.Separator) {
		return strings.HasPrefix(p, root)
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

func withinAny(p string, roots []string) bool {
	for _, r := range roots {
		if within(p, r) {
			return true
		}
	}
	return false
}

// CacheEnv returns the "K=V" settings that point every CacheVars entry at
// a subdirectory of tmpDir (the per-job temp dir; on linux the child's
// private /tmp), so a sandboxed test never needs a writable shared cache
// in the home directory. A variable is left alone only when its current
// value (cur(K), the runner's own environment) is an absolute path inside
// one of allowWrite — i.e. the administrator deliberately listed that
// cache in runner_allow_write and accepted the residual risk
// (docs/trust-model.md §2.3). Wrap appends the result to Wrapped.Env.
// GOMODCACHE is not redirected: with the default network deny the module
// cache is only read (ro-bind) 【未核验:go 在只读 GOMODCACHE 下的行为,
// P2-SANDBOX 集成测试确认】.
func CacheEnv(tmpDir string, allowWrite []string, cur func(key string) string) []string {
	sub := map[string]string{
		"GOCACHE":           "go-build",
		"XDG_CACHE_HOME":    "xdg",
		"npm_config_cache":  "npm",
		"YARN_CACHE_FOLDER": "yarn",
		"PIP_CACHE_DIR":     "pip",
	}
	roots := make([]string, 0, len(allowWrite))
	for _, a := range allowWrite {
		if a != "" {
			roots = append(roots, filepath.Clean(a))
		}
	}
	var out []string
	for _, k := range CacheVars {
		if cur != nil {
			if v := cur(k); v != "" && filepath.IsAbs(v) && withinAny(filepath.Clean(v), roots) {
				continue
			}
		}
		d, ok := sub[k]
		if !ok { // a variable appended to CacheVars by a later version
			d = strings.ToLower(strings.ReplaceAll(k, "_", "-"))
		}
		out = append(out, k+"="+filepath.Join(tmpDir, "cache", d))
	}
	return out
}

// hostEnvDrop reports whether the variable named k is removed by HostEnv.
func hostEnvDrop(k string) bool {
	if strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "DYLD_") {
		return true
	}
	switch k {
	case "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "CDPATH":
		return true
	}
	return false
}

func envKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}

// HostEnv returns environ minus every variable that would let the content of
// lower settings layers (a project-layer "env", which reaches the hook and
// the runner through Claude Code's environment) run code in a process the
// runner starts OUTSIDE the sandbox (v1.4, docs/p2-design.md §3.4
// "沙箱外进程的环境"). Frozen removal set: every GIT_* variable
// (GIT_CONFIG_PARAMETERS / GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n injection of
// core.fsmonitor and friends, GIT_CONFIG_GLOBAL / GIT_CONFIG_SYSTEM pointing
// at agent-written files, GIT_EXEC_PATH, GIT_SSH*, GIT_EXTERNAL_DIFF,
// GIT_TRACE* which appends to arbitrary files, …); every LD_* and DYLD_*
// variable (loader injection); BASH_ENV, ENV, SHELLOPTS, BASHOPTS, CDPATH
// (git may run sh). Order is preserved. Pure; table-tested.
//
// The runner uses it for: every git child of its snapshots (it passes
// snapshot/evidence a sys.Env whose Environ and Getenv are filtered by
// HostEnv; snapshot then sets its own GIT_INDEX_FILE etc. as today). Processes
// that are Ballast itself (the detached `ballast verify --run-job` child) and
// the backend executable get the narrower loader-only removal (every LD_* and
// DYLD_*; see Wrapped.Env), so the sandboxed child keeps its GIT_* variables.
// It does NOT protect the hook or runner processes
// themselves: they already run with Claude Code's environment, which is why
// a project-layer "env" counts as weakening (tier.WeakenedSandboxKeys,
// LayerRepo) and why darwin release binaries must be signed with the
// hardened runtime (doctor tier.binary_hardening, docs/p2-design.md §3.5).
func HostEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if hostEnvDrop(envKey(kv)) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
