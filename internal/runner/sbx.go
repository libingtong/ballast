package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sandbox"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
)

// sandboxBackend is the seam between the runner and internal/sandbox. The
// production value forwards to the package; tests substitute a recording
// fake (Options.backend is unexported, so the exported surface is unchanged).
type sandboxBackend interface {
	Probe(opt sandbox.Options) (sandbox.Kind, error)
	Wrap(opt sandbox.Options, kind sandbox.Kind, spec sandbox.Spec, argv []string) (sandbox.Wrapped, error)
	PostCheck(fsys sys.FS, spec sandbox.Spec, w sandbox.Wrapped) ([]string, error)
	Quarantine(fsys sys.FS, paths, roots []string, dir string) []sandbox.QuarantineEntry
	ResolveGitDirs(fsys sys.FS, workTree string) (gitDir, commonDir string, err error)
}

type realBackend struct{}

func (realBackend) Probe(opt sandbox.Options) (sandbox.Kind, error) { return sandbox.Probe(opt) }
func (realBackend) Wrap(opt sandbox.Options, kind sandbox.Kind, spec sandbox.Spec, argv []string) (sandbox.Wrapped, error) {
	return sandbox.Wrap(opt, kind, spec, argv)
}
func (realBackend) PostCheck(fsys sys.FS, spec sandbox.Spec, w sandbox.Wrapped) ([]string, error) {
	return sandbox.PostCheck(fsys, spec, w)
}
func (realBackend) Quarantine(fsys sys.FS, paths, roots []string, dir string) []sandbox.QuarantineEntry {
	return sandbox.Quarantine(fsys, paths, roots, dir)
}
func (realBackend) ResolveGitDirs(fsys sys.FS, workTree string) (string, string, error) {
	return sandbox.ResolveGitDirs(fsys, workTree)
}

func (o Options) sbx() sandboxBackend {
	if o.backend != nil {
		return o.backend
	}
	return realBackend{}
}

// gitRunner is the git runner of every git child the runner starts outside
// the sandbox (Materialize, TreeSEnd and the snapshots). Tests substitute a
// recording fake through the unexported Options.gitRun.
func (o Options) gitRunner() snapshot.GitRunner {
	if o.gitRun != nil {
		return o.gitRun
	}
	return snapshot.ExecGit{}
}

// tr records a step of the frozen wrap-up order (tests only).
func (o Options) tr(step string) {
	if o.trace != nil {
		o.trace(step)
	}
}

// hostEnv is a sys.Env whose Environ and Getenv are filtered by
// sandbox.HostEnv: what every process the runner starts outside the sandbox
// (git children of the snapshots and of Materialize) is allowed to inherit
// (docs/p2-design.md §3.4 "沙箱外进程的环境").
type hostEnv struct{ sys.Env }

func (h hostEnv) Environ() []string { return sandbox.HostEnv(h.Env.Environ()) }

func (h hostEnv) Getenv(key string) (string, bool) {
	win := h.Env.GOOS() == "windows"
	for _, kv := range h.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == key || (win && strings.EqualFold(k, key)) {
			return v, true
		}
	}
	return "", false
}

// hostOptions is o with Env replaced by the HostEnv-filtered view: the value
// handed to snapshot.Options.Env, Materialize and TreeSEnd.
func (o Options) hostOptions() Options {
	o.Env = hostEnv{o.Env}
	return o
}

func isLoaderVar(kv string) bool {
	k, _, _ := strings.Cut(kv, "=")
	return strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "DYLD_")
}

// stripLoaderEnv removes every LD_* and DYLD_* variable (v1.4): the
// environment of the detached --run-job child and of the sandbox backend
// executable, which must never load agent-chosen code before the sandbox
// exists.
func stripLoaderEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if !isLoaderVar(kv) {
			out = append(out, kv)
		}
	}
	return out
}

// applyEnvChanges applies sandbox.Wrapped.Env to environ: "K=V" sets K, a
// bare "K" unsets it.
func applyEnvChanges(environ, changes []string) []string {
	out := append([]string(nil), environ...)
	for _, ch := range changes {
		k, _, isSet := strings.Cut(ch, "=")
		kept := out[:0]
		for _, kv := range out {
			if ek, _, _ := strings.Cut(kv, "="); ek != k {
				kept = append(kept, kv)
			}
		}
		out = kept
		if isSet {
			out = append(out, ch)
		}
	}
	return out
}

// loaderEnv returns the LD_* settings of environ (last value of a variable
// wins), the Spec.LoaderEnv of a wrap.
func loaderEnv(environ []string) []string {
	last := map[string]string{}
	var order []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, "LD_") || !strings.Contains(kv, "=") {
			continue
		}
		if _, seen := last[k]; !seen {
			order = append(order, k)
		}
		last[k] = kv
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, last[k])
	}
	return out
}

// tierJudgement is the runner's own tier decision for one job (docs/p2-design.md
// §3.4 "runner 自判 T1").
type tierJudgement struct {
	info model.TierInfo
	// t1 is the runner's own judgement: Probe says T1 AND neither the job
	// repo nor HookStartRequest.ProjectDir weakens the managed sandbox.
	t1 bool
	// weakened are the "<basename>:<key>" entries behind a downgrade.
	weakened []string
}

// judgeTier consults the TierProbe and the project layers. The probe result
// is never taken from the hook; ProjectDir can only make the verdict worse.
func (o Options) judgeTier(repo *model.RepoInfo, side jobSpec) tierJudgement {
	j := tierJudgement{}
	if o.Tier != nil {
		j.info = o.Tier.Probe()
	} else {
		j.info = model.TierInfo{Tier: model.TierT0, SandboxPolicy: model.SandboxOff}
	}
	j.t1 = j.info.Tier == model.TierT1
	if !j.t1 {
		return j
	}
	j.weakened = tier.RepoSandboxWeakened(o.FS, repo.Toplevel)
	if pd := side.ProjectDir; pd != "" && filepath.Clean(pd) != filepath.Clean(repo.Toplevel) {
		j.weakened = append(j.weakened, tier.RepoSandboxWeakened(o.FS, pd)...)
	}
	if len(j.weakened) > 0 {
		sort.Strings(j.weakened)
		j.t1 = false
	}
	return j
}

// policy is the effective runner_sandbox policy for origin=cli runs.
func (j tierJudgement) policy() model.SandboxPolicy {
	if j.info.SandboxPolicy != "" {
		return j.info.SandboxPolicy
	}
	if j.info.Tier == model.TierT1 {
		return model.SandboxRequire
	}
	return model.SandboxOff
}

// auditDiag appends one diag line.
func (o Options) auditDiag(rec *model.VerifyRecord, diag string, fields map[string]any) {
	if o.Audit == nil {
		return
	}
	d := map[string]any{"diag": diag}
	for k, v := range fields {
		d[k] = v
	}
	detail, _ := json.Marshal(d)
	err := o.Audit.Append([]model.AuditRecord{{
		TS: o.Clock.Now().UTC(), V: model.AuditVersion, Type: model.AuditDiag,
		SessionID: rec.SessionID, AgentID: rec.AgentID, RepoInst: rec.RepoInst,
		Tier: rec.Tier, BallastVersion: rec.BallastVersion, Detail: detail,
	}})
	if err != nil && rec.LogPath != "" {
		appendLog(o.FS, rec.LogPath, "# ballast: audit append failed: "+err.Error()+"\n")
	}
}

// protectedSpec fills the Spec's protected set D from tier.ProtectedPaths
// (raw and resolved forms; globs separately).
func (o Options) protectedSpec(spec *sandbox.Spec, info model.TierInfo, repoRoot string) {
	seenP, seenG := map[string]bool{}, map[string]bool{}
	for _, pp := range tier.ProtectedPaths(o.FS, o.Layout, o.Self, repoRoot, info.ProtectedExtra) {
		for _, p := range []string{pp.Path, pp.Resolved} {
			if p == "" {
				continue
			}
			if pp.Glob {
				if !seenG[p] {
					seenG[p] = true
					spec.DenyWriteGlobs = append(spec.DenyWriteGlobs, p)
				}
			} else if !seenP[p] {
				seenP[p] = true
				spec.DenyWrite = append(spec.DenyWrite, p)
			}
		}
	}
}

// homeDir is the real home directory handed to sandbox.ValidateSpec.
func (o Options) homeDir() string {
	if h, err := o.Env.HomeDir(); err == nil && h != "" {
		return h
	}
	return o.Layout.InstallRoot
}

// newJobTmp creates the per-job temp directory (0700, removed afterwards).
// With jobDir non-empty (origin=hook) it is <jobDir>/tmp, the sibling of the
// private tree below ~/.ballast/work (sandbox.TreeTmp). Otherwise (origin=cli,
// the human's own terminal) it is a fresh directory under $TMPDIR / os.TempDir().
func (o Options) newJobTmp(jobDir string) (string, error) {
	if jobDir != "" {
		dir := filepath.Join(jobDir, "tmp")
		if err := o.FS.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return dir, nil
	}
	base := ""
	if v, ok := o.Env.Getenv("TMPDIR"); ok && filepath.IsAbs(v) {
		if fi, err := o.FS.Stat(v); err == nil && fi.IsDir() {
			base = v
		}
	}
	if base == "" {
		base = os.TempDir()
	}
	if real, err := o.FS.EvalSymlinks(base); err == nil {
		base = real
	}
	dir := filepath.Join(base, "ballast-job-"+o.IDs.NewID(6))
	if err := o.FS.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// gitCtx is the context of the runner's git children (no deadline: the
// runner is not on the hot path).
func gitCtx() context.Context { return context.Background() }
