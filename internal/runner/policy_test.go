package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sandbox"
)

// --- the policy table (docs/p2-design.md §3.4) ----------------------------

func infoT1(p model.SandboxPolicy) *model.TierInfo {
	i := t1Info()
	i.SandboxPolicy = p
	return &i
}

func infoT0Org(p model.SandboxPolicy) *model.TierInfo {
	return &model.TierInfo{Tier: model.TierT0, Declared: model.TierT1, Missing: []string{model.TierMissingManagedFile}, SandboxPolicy: p}
}

func diagNames(recs []model.AuditRecord) []string {
	var out []string
	for _, r := range recs {
		if r.Type != model.AuditDiag {
			continue
		}
		var d struct {
			Diag string `json:"diag"`
		}
		_ = json.Unmarshal(r.Detail, &d)
		out = append(out, d.Diag)
	}
	return out
}

func TestPolicyTable(t *testing.T) {
	none := sandbox.KindNone
	unavail := fmt.Errorf("no backend here")
	type row struct {
		name      string
		origin    model.VerifyOrigin
		tier      *model.TierInfo
		avail     sandbox.Kind
		probeErr  error
		ran       bool
		outcome   model.Outcome
		errText   string
		wantTier  model.Tier
		sandbox   string
		missing   []string
		workspace string
		diag      string
		probed    bool
	}
	rows := []row{
		{name: "cli T1 require avail", origin: model.OriginCLI, tier: infoT1(model.SandboxRequire), avail: sandbox.KindBwrap, ran: true, outcome: model.OutcomePass, wantTier: model.TierT1, sandbox: "bwrap", workspace: "worktree", probed: true},
		{name: "cli T1 require unavailable", origin: model.OriginCLI, tier: infoT1(model.SandboxRequire), avail: none, probeErr: unavail, outcome: model.OutcomeInfra, errText: "sandbox_unavailable", wantTier: model.TierT0, sandbox: "none", workspace: "worktree", diag: model.DiagSandboxUnavailable, probed: true},
		{name: "cli T1 prefer avail", origin: model.OriginCLI, tier: infoT1(model.SandboxPrefer), avail: sandbox.KindSandboxExec, ran: true, outcome: model.OutcomePass, wantTier: model.TierT1, sandbox: "sandbox-exec", workspace: "worktree", probed: true},
		{name: "cli T1 prefer unavailable", origin: model.OriginCLI, tier: infoT1(model.SandboxPrefer), avail: none, probeErr: unavail, ran: true, outcome: model.OutcomePass, wantTier: model.TierT0, sandbox: "none", missing: []string{"runner_sandbox"}, workspace: "worktree", probed: true},
		{name: "cli T1 off", origin: model.OriginCLI, tier: infoT1(model.SandboxOff), avail: sandbox.KindBwrap, ran: true, outcome: model.OutcomePass, wantTier: model.TierT0, sandbox: "none", missing: []string{"runner_sandbox"}, workspace: "worktree"},
		{name: "cli T0 default", origin: model.OriginCLI, avail: sandbox.KindBwrap, ran: true, outcome: model.OutcomePass, wantTier: model.TierT0, sandbox: "none", workspace: "worktree"},
		{name: "cli T0 org prefer avail", origin: model.OriginCLI, tier: infoT0Org(model.SandboxPrefer), avail: sandbox.KindBwrap, ran: true, outcome: model.OutcomePass, wantTier: model.TierT0, sandbox: "bwrap", missing: []string{"managed_settings"}, workspace: "worktree", probed: true},
		{name: "cli T0 org require unavailable", origin: model.OriginCLI, tier: infoT0Org(model.SandboxRequire), avail: none, probeErr: unavail, outcome: model.OutcomeInfra, errText: "sandbox_unavailable", wantTier: model.TierT0, sandbox: "none", missing: []string{"managed_settings"}, workspace: "worktree", diag: model.DiagSandboxUnavailable, probed: true},

		{name: "hook T1 require avail", origin: model.OriginHook, tier: infoT1(model.SandboxRequire), avail: sandbox.KindBwrap, ran: true, outcome: model.OutcomePass, wantTier: model.TierT1, sandbox: "bwrap", workspace: "copy", probed: true},
		{name: "hook T1 prefer avail", origin: model.OriginHook, tier: infoT1(model.SandboxPrefer), avail: sandbox.KindBwrap, ran: true, outcome: model.OutcomePass, wantTier: model.TierT1, sandbox: "bwrap", workspace: "copy", probed: true},
		{name: "hook T1 off avail", origin: model.OriginHook, tier: infoT1(model.SandboxOff), avail: sandbox.KindSandboxExec, ran: true, outcome: model.OutcomePass, wantTier: model.TierT1, sandbox: "sandbox-exec", workspace: "copy", probed: true},
		{name: "hook T1 require unavailable", origin: model.OriginHook, tier: infoT1(model.SandboxRequire), avail: none, probeErr: unavail, outcome: model.OutcomeInfra, errText: "sandbox_unavailable", wantTier: model.TierT0, sandbox: "none", workspace: "copy", diag: model.DiagSandboxUnavailable, probed: true},
		{name: "hook T1 prefer unavailable", origin: model.OriginHook, tier: infoT1(model.SandboxPrefer), avail: none, probeErr: unavail, outcome: model.OutcomeInfra, errText: "sandbox_unavailable", wantTier: model.TierT0, sandbox: "none", workspace: "copy", diag: model.DiagSandboxUnavailable, probed: true},
		{name: "hook T1 off unavailable", origin: model.OriginHook, tier: infoT1(model.SandboxOff), avail: none, probeErr: unavail, outcome: model.OutcomeInfra, errText: "sandbox_unavailable", wantTier: model.TierT0, sandbox: "none", workspace: "copy", diag: model.DiagSandboxUnavailable, probed: true},
		{name: "hook T1 unavailable without error", origin: model.OriginHook, tier: infoT1(model.SandboxOff), avail: none, outcome: model.OutcomeInfra, errText: "sandbox_unavailable", wantTier: model.TierT0, sandbox: "none", workspace: "copy", diag: model.DiagSandboxUnavailable, probed: true},
		{name: "hook T0 avail", origin: model.OriginHook, avail: sandbox.KindBwrap, ran: true, outcome: model.OutcomePass, wantTier: model.TierT0, sandbox: "bwrap", workspace: "copy", probed: true},
		{name: "hook T0 unavailable", origin: model.OriginHook, avail: none, probeErr: unavail, outcome: model.OutcomeInfra, errText: "sandbox_unavailable", wantTier: model.TierT0, sandbox: "none", workspace: "copy", diag: model.DiagSandboxUnavailable, probed: true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			h := newHookFx(t)
			marker := filepath.Join(h.sb.Root, "ran")
			h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE="+marker))
			h.be.kind, h.be.probeErr = r.avail, r.probeErr
			if r.tier == nil {
				h.opt.Tier = nil
			} else {
				h.opt.Tier = fakeTier{*r.tier}
			}
			var rec *model.VerifyRecord
			var err error
			if r.origin == model.OriginHook {
				rec, err = h.runHook("test")
			} else {
				h.opt.Output = &bytes.Buffer{}
				rec, err = Foreground(h.opt, h.root, "test")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, serr := os.Stat(marker); (serr == nil) != r.ran {
				t.Errorf("command ran = %v, want %v", serr == nil, r.ran)
			}
			if !r.ran && h.be.called("wrap") != 0 {
				t.Errorf("a command that must not run was wrapped")
			}
			if rec.Outcome != r.outcome || (r.errText != "" && rec.Error != r.errText) {
				t.Errorf("outcome %s error %q, want %s %q", rec.Outcome, rec.Error, r.outcome, r.errText)
			}
			if rec.Tier != r.wantTier || rec.Sandbox != r.sandbox || rec.Origin != r.origin || rec.Workspace != r.workspace {
				t.Errorf("tier/sandbox/origin/workspace = %s/%s/%s/%s, want %s/%s/%s/%s", rec.Tier, rec.Sandbox, rec.Origin, rec.Workspace, r.wantTier, r.sandbox, r.origin, r.workspace)
			}
			if !reflect.DeepEqual(rec.TierMissing, r.missing) {
				t.Errorf("tier_missing = %v, want %v", rec.TierMissing, r.missing)
			}
			if (h.be.called("probe") > 0) != r.probed {
				t.Errorf("probed = %v, want %v", h.be.called("probe") > 0, r.probed)
			}
			diags := diagNames(h.audit.all())
			if r.diag != "" && !contains(diags, r.diag) {
				t.Errorf("diags %v lack %s", diags, r.diag)
			}
			if r.diag == "" && contains(diags, model.DiagSandboxUnavailable) {
				t.Errorf("unexpected diag in %v", diags)
			}
			if !r.ran && rec.ValidPass() {
				t.Errorf("a pass record for a command that did not run")
			}
			// T1 iff own judgement T1 and wrapped (and the command ran).
			if rec.Tier == model.TierT1 && (rec.Sandbox == "none" || !r.ran) {
				t.Errorf("T1 record without a sandboxed run: %+v", rec)
			}
		})
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// origin=hook never runs a test unwrapped, in any policy/backend combination.
func TestHookNeverRunsUnwrapped(t *testing.T) {
	for _, pol := range []model.SandboxPolicy{model.SandboxRequire, model.SandboxPrefer, model.SandboxOff, ""} {
		for _, tiered := range []bool{true, false} {
			for _, avail := range []sandbox.Kind{sandbox.KindNone, sandbox.KindBwrap} {
				name := fmt.Sprintf("policy=%q t1=%v avail=%s", pol, tiered, avail)
				t.Run(name, func(t *testing.T) {
					h := newHookFx(t)
					marker := filepath.Join(h.sb.Root, "ran")
					h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE="+marker))
					h.be.kind = avail
					if tiered {
						i := t1Info()
						i.SandboxPolicy = pol
						h.opt.Tier = fakeTier{i}
					} else {
						h.opt.Tier = fakeTier{model.TierInfo{Tier: model.TierT0, SandboxPolicy: pol}}
					}
					rec, err := h.runHook("test")
					if err != nil {
						t.Fatal(err)
					}
					_, ran := os.Stat(marker)
					if avail == sandbox.KindNone {
						if ran == nil || rec.Outcome != model.OutcomeInfra || rec.Error != "sandbox_unavailable" || rec.ValidPass() {
							t.Errorf("ran=%v %+v", ran == nil, rec)
						}
						if h.be.called("wrap") != 0 {
							t.Errorf("wrapped without a backend")
						}
					} else if ran != nil || h.be.called("wrap") != 1 {
						t.Errorf("did not run wrapped: %v wraps=%d", ran, h.be.called("wrap"))
					}
				})
			}
		}
	}
}

// --- own tier judgement: project layers ----------------------------------

func writeSettings(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWeakenedRepoLayerDowngradesTheRecord(t *testing.T) {
	for _, origin := range []model.VerifyOrigin{model.OriginHook, model.OriginCLI} {
		t.Run(string(origin), func(t *testing.T) {
			h := newHookFx(t)
			marker := filepath.Join(h.sb.Root, "ran")
			h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE="+marker))
			writeSettings(t, h.root, "settings.json", `{"sandbox":{"excludedCommands":["npm"]}}`)
			var rec *model.VerifyRecord
			var err error
			if origin == model.OriginHook {
				rec, err = h.runHook("test")
			} else {
				h.opt.Output = &bytes.Buffer{}
				rec, err = Foreground(h.opt, h.root, "test")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, serr := os.Stat(marker); serr != nil {
				t.Errorf("the command did not run")
			}
			if h.be.called("wrap") != 1 || rec.Sandbox != "bwrap" {
				t.Errorf("the command was not sandboxed: wraps=%d sandbox=%s", h.be.called("wrap"), rec.Sandbox)
			}
			if rec.Tier != model.TierT0 || !reflect.DeepEqual(rec.TierMissing, []string{"lower_sandbox_weakened"}) {
				t.Errorf("tier %s missing %v", rec.Tier, rec.TierMissing)
			}
			if !contains(diagNames(h.audit.all()), model.DiagTierMismatch) {
				t.Errorf("no tier_mismatch diag: %v", diagNames(h.audit.all()))
			}
			// The audit detail carries entries only.
			for _, a := range h.audit.all() {
				if a.Type == model.AuditDiag && strings.Contains(string(a.Detail), h.root) {
					t.Errorf("a path leaked into the diag: %s", a.Detail)
				}
			}
		})
	}
}

func TestWeakenedProjectDirDowngradesTheRecord(t *testing.T) {
	h := newHookFx(t)
	h.write("sub/keep.txt", "x\n")
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	sub := filepath.Join(h.root, "sub")
	writeSettings(t, sub, "settings.json", `{"env":{"LD_PRELOAD":"/x"}}`)
	req := model.HookStartRequest{RequestID: h.opt.newJobID(), Cwd: h.root, Names: []string{"test"}, Client: model.ClientClaude, ProjectDir: sub}
	started, err := StartRequest(h.opt, req)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := RunJob(h.opt, started[0].JobID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomePass || rec.Tier != model.TierT0 || !reflect.DeepEqual(rec.TierMissing, []string{"lower_sandbox_weakened"}) {
		t.Errorf("%+v", rec)
	}
	if h.be.called("wrap") != 1 {
		t.Errorf("not sandboxed")
	}
	// The same job with a clean ProjectDir and repo is T1.
	h2 := newHookFx(t)
	h2.write("sub/keep.txt", "x\n")
	h2.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	writeSettings(t, filepath.Join(h2.root, "sub"), "settings.json", `{"model":"x","permissions":{"allow":["Bash(ls)"]}}`)
	req.RequestID = h2.opt.newJobID()
	req.Cwd, req.ProjectDir = h2.root, filepath.Join(h2.root, "sub")
	started, err = StartRequest(h2.opt, req)
	if err != nil {
		t.Fatal(err)
	}
	rec, err = RunJob(h2.opt, started[0].JobID)
	if err != nil || rec.Tier != model.TierT1 || len(rec.TierMissing) != 0 {
		t.Errorf("clean project dir: %+v %v", rec, err)
	}
}

// The runner trusts only its own probe: a hint nowhere in HookStartRequest
// can lift the record, and ProjectDir equal to the repo is not checked twice.
func TestProjectDirEqualToRepoIsCheckedOnce(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	writeSettings(t, h.root, "settings.local.json", `{"sandbox":{"network":{"allowUnixSockets":["/x"]}}}`)
	req := model.HookStartRequest{RequestID: h.opt.newJobID(), Cwd: h.root, Names: []string{"test"}, Client: model.ClientClaude, ProjectDir: h.root}
	started, err := StartRequest(h.opt, req)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := RunJob(h.opt, started[0].JobID)
	if err != nil || len(rec.TierMissing) != 1 {
		t.Fatalf("%+v %v", rec, err)
	}
	for _, a := range h.audit.all() {
		if a.Type == model.AuditDiag && strings.Contains(string(a.Detail), "tier_mismatch") {
			var d struct{ Entries []string }
			_ = json.Unmarshal(a.Detail, &d)
			if len(d.Entries) != 1 {
				t.Errorf("entries = %v", d.Entries)
			}
		}
	}
}

// --- environments -------------------------------------------------------

var hostileVars = map[string]string{
	"GIT_CONFIG_PARAMETERS": "'core.fsmonitor=/evil'",
	"GIT_CONFIG_COUNT":      "1",
	"GIT_TRACE":             "/nonexistent/trace",
	"GIT_EXEC_PATH":         "/nonexistent/exec",
	"BASH_ENV":              "/nonexistent/bashenv",
	"LD_PRELOAD":            "/nonexistent/evil.so",
	"LD_LIBRARY_PATH":       "/opt/lib",
	"DYLD_INSERT_LIBRARIES": "/nonexistent/evil.dylib",
}

func assertCleanHostEnv(t *testing.T, g *recGit) {
	t.Helper()
	if len(g.calls) == 0 {
		t.Fatal("no git child ran")
	}
	for _, c := range g.calls {
		for _, kv := range c.env {
			k, _, _ := strings.Cut(kv, "=")
			if _, bad := hostileVars[k]; bad {
				t.Errorf("git %v inherited %s", c.args, k)
			}
		}
		var explicit int
		for _, kv := range c.env {
			if strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_INDEX_FILE=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") {
				explicit++
			}
		}
		if explicit != 3 {
			t.Errorf("git %v lacks explicit GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE: %v", c.args, c.env)
		}
		if !strings.Contains(strings.Join(c.args, " "), "core.fsmonitor=false") {
			t.Errorf("git %v lacks -c core.fsmonitor=false", c.args)
		}
	}
}

func TestGitChildrenGetAFilteredEnvironment(t *testing.T) {
	// origin=hook: Materialize and TreeSEnd.
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	for k, v := range hostileVars {
		h.vars[k] = v
	}
	h.rebuild()
	h.wire()
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	assertCleanHostEnv(t, h.rg)

	// origin=cli: the snapshot fallback (a filter attribute on a dirty file).
	h2 := newHookFx(t)
	h2.write(".gitattributes", "*.txt filter=nothing\n")
	h2.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	h2.write("src/main.txt", "dirty\n")
	for k, v := range hostileVars {
		h2.vars[k] = v
	}
	h2.rebuild()
	h2.wire() // fake T1 + require: the command is wrapped, so the hostile loader variables are stripped
	h2.opt.Output = &bytes.Buffer{}
	rec, err = Foreground(h2.opt, h2.root, "test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	if !h2.rg.has("add") {
		t.Fatalf("the snapshot did not use the git fallback: %v", h2.rg.verbs())
	}
	assertCleanHostEnv(t, h2.rg)
}

func TestLoaderVariablesNeverReachTheBackendOrTheChild(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "report", "LD_AUDIT=/spec/audit.so", "KEEP_ME=1"))
	for k, v := range hostileVars {
		h.vars[k] = v
	}
	h.rebuild()
	h.wire()
	h.opt.Spawner = h.spawn
	started, _, err := h.startHook("test")
	if err != nil {
		t.Fatal(err)
	}
	// The detached child is Ballast: no loader variables.
	if len(h.spawn.calls) != 1 {
		t.Fatalf("spawn calls: %d", len(h.spawn.calls))
	}
	for _, kv := range h.spawn.calls[0].env {
		if isLoaderVar(kv) {
			t.Errorf("the --run-job child inherited %s", kv)
		}
	}
	if !envHas(h.spawn.calls[0].env, "GIT_TRACE=/nonexistent/trace") {
		t.Errorf("the child lost its other variables")
	}
	rec, err := RunJob(h.opt, started[0].JobID)
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	log := readFileT(t, rec.LogPath)
	for _, bad := range []string{"env:LD_", "env:DYLD_"} {
		if strings.Contains(log, bad) {
			t.Errorf("the sandbox backend's environment still had %s*:\n%s", bad, log)
		}
	}
	// The test's loader variables travel in Spec.LoaderEnv.
	spec := h.be.specs[0]
	want := []string{"LD_PRELOAD=/nonexistent/evil.so", "LD_LIBRARY_PATH=/opt/lib", "LD_AUDIT=/spec/audit.so"}
	got := append([]string(nil), spec.LoaderEnv...)
	for _, w := range want {
		if !envHas(got, w) {
			t.Errorf("LoaderEnv %v lacks %s", got, w)
		}
	}
	for _, kv := range got {
		if !strings.HasPrefix(kv, "LD_") {
			t.Errorf("non-LD entry %s in LoaderEnv", kv)
		}
	}
	if !strings.Contains(log, "env:TMPDIR="+spec.TmpDir) {
		t.Errorf("Wrapped.Env was not applied (TMPDIR %s):\n%s", spec.TmpDir, log)
	}
	// Start (origin=cli) strips them from its child too.
	h.opt.Tier = nil
	h.spawn.calls = nil
	if _, err := Start(h.opt, h.root, []string{"test"}); err != nil {
		t.Fatal(err)
	}
	for _, kv := range h.spawn.calls[0].env {
		if isLoaderVar(kv) {
			t.Errorf("Start's child inherited %s", kv)
		}
	}
}

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestApplyEnvChanges(t *testing.T) {
	got := applyEnvChanges([]string{"A=1", "B=2", "SSH_AUTH_SOCK=/s", "TMPDIR=/old"}, []string{"TMPDIR=/new", "SSH_AUTH_SOCK", "C=3"})
	want := []string{"A=1", "B=2", "TMPDIR=/new", "C=3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if got := loaderEnv([]string{"LD_A=1", "X=2", "LD_B=3", "LD_A=4", "LDX=5", "DYLD_Z=1"}); !reflect.DeepEqual(got, []string{"LD_A=4", "LD_B=3"}) {
		t.Errorf("loaderEnv = %v", got)
	}
	if got := stripLoaderEnv([]string{"LD_A=1", "X=2", "DYLD_B=3", "LDX=5"}); !reflect.DeepEqual(got, []string{"X=2", "LDX=5"}) {
		t.Errorf("stripLoaderEnv = %v", got)
	}
}

// --- sandbox spec, wrap failures, quarantine -------------------------------

func TestHookSpecIsTheCopyOnly(t *testing.T) {
	h := newHookFx(t)
	allow := filepath.Join(h.sb.Root, "cache")
	if err := os.MkdirAll(allow, 0o755); err != nil {
		t.Fatal(err)
	}
	info := t1Info()
	info.RunnerAllowWrite = []string{allow}
	info.Network = model.NetworkLoopback
	h.opt.Tier = fakeTier{info}
	h.write("sub/keep.txt", "x\n")
	spec := helperSpec("test", "report")
	spec.Cwd = "sub"
	h.setProject(spec)
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	sp := h.be.specs[0]
	treeRoot := filepath.Join(h.realWorkDir(), rec.JobID, "tree")
	if sp.Tree != treeRoot || sp.WorkRoot != h.realWorkDir() {
		t.Errorf("Tree/WorkRoot = %s / %s", sp.Tree, sp.WorkRoot)
	}
	if !reflect.DeepEqual(sp.RepoRoots, []string{treeRoot}) || len(sp.GitDirs) != 0 {
		t.Errorf("RepoRoots %v GitDirs %v", sp.RepoRoots, sp.GitDirs)
	}
	if !reflect.DeepEqual(sp.AllowWrite, []string{treeRoot, allow}) {
		t.Errorf("AllowWrite = %v", sp.AllowWrite)
	}
	if sp.Cwd != filepath.Join(treeRoot, "sub") || sp.Network != model.NetworkLoopback {
		t.Errorf("Cwd %s Network %s", sp.Cwd, sp.Network)
	}
	if sp.TmpDir == "" || sp.Home != h.sb.Home {
		t.Errorf("TmpDir %q Home %q", sp.TmpDir, sp.Home)
	}
	// ~/.ballast is protected (raw form at least); the real repo is not in W.
	if !contains(sp.DenyWrite, h.sb.BallastHome) {
		t.Errorf("DenyWrite lacks ~/.ballast: %v", sp.DenyWrite)
	}
	for _, p := range sp.AllowWrite {
		if withinDir(p, h.root) {
			t.Errorf("the real work tree is writable: %s", p)
		}
	}
	if _, err := os.Stat(sp.TmpDir); err == nil {
		t.Errorf("the job temp dir %s was not removed", sp.TmpDir)
	}
	log := readFileT(t, rec.LogPath)
	if !strings.Contains(log, "cwd=") || !strings.Contains(log, filepath.Join("tree", "sub")) {
		t.Errorf("the command did not run in the copy's sub directory:\n%s", log)
	}
}

func TestCLISpecIsTheWorkTreeAndItsGitDirs(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "report"))
	h.opt.Output = &bytes.Buffer{}
	rec, err := Foreground(h.opt, h.root, "test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	sp := h.be.specs[0]
	repo := h.repoInfo()
	if sp.Tree != "" || sp.WorkRoot != "" {
		t.Errorf("Tree mode for origin=cli")
	}
	if !reflect.DeepEqual(sp.AllowWrite, []string{repo.Toplevel}) || !reflect.DeepEqual(sp.RepoRoots, []string{repo.Toplevel}) {
		t.Errorf("AllowWrite %v RepoRoots %v", sp.AllowWrite, sp.RepoRoots)
	}
	if !reflect.DeepEqual(sp.GitDirs, []string{repo.GitDir}) {
		t.Errorf("GitDirs = %v", sp.GitDirs)
	}
	if rec.Workspace != model.WorkspaceWorktree || rec.Origin != model.OriginCLI {
		t.Errorf("record: %+v", rec)
	}
	if got := readFileT(t, rec.LogPath); !strings.Contains(got, "cwd="+h.root) {
		// The command ran in the work tree itself.
		t.Errorf("did not run in the work tree:\n%s", got)
	}
	for _, v := range h.rg.verbs() {
		if v == "read-tree" || v == "checkout-index" {
			t.Errorf("origin=cli materialized a tree")
		}
	}
	if _, err := os.Stat(h.opt.Layout.WorkDir); err == nil {
		if ents, _ := os.ReadDir(h.opt.Layout.WorkDir); len(ents) != 0 {
			t.Errorf("work dir is not empty: %v", ents)
		}
	}
}

func TestSpecValidationFailureIsInfraAndNotExecuted(t *testing.T) {
	for _, origin := range []model.VerifyOrigin{model.OriginHook, model.OriginCLI} {
		for _, tc := range []struct {
			name string
			err  error
			want string
		}{
			{"invalid spec", fmt.Errorf("%w: writable root is the home directory", sandbox.ErrInvalidSpec), "sandbox_spec_invalid"},
			{"unavailable at wrap", fmt.Errorf("%w: bwrap vanished", sandbox.ErrUnavailable), "sandbox_unavailable"},
			{"other", errors.New("boom"), "sandbox_wrap_failed"},
		} {
			t.Run(string(origin)+"/"+tc.name, func(t *testing.T) {
				h := newHookFx(t)
				marker := filepath.Join(h.sb.Root, "ran")
				h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE="+marker))
				h.be.wrapErr = tc.err
				var rec *model.VerifyRecord
				var err error
				if origin == model.OriginHook {
					rec, err = h.runHook("test")
				} else {
					h.opt.Output = &bytes.Buffer{}
					rec, err = Foreground(h.opt, h.root, "test")
				}
				if err != nil {
					t.Fatal(err)
				}
				if rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, tc.want) {
					t.Errorf("%s %q, want infra %s", rec.Outcome, rec.Error, tc.want)
				}
				if _, serr := os.Stat(marker); serr == nil {
					t.Errorf("the command ran")
				}
				if rec.Tier != model.TierT0 {
					t.Errorf("tier %s", rec.Tier)
				}
			})
		}
	}
}

func TestCLIResolveGitDirsFailureRunsNoGit(t *testing.T) {
	h := newHookFx(t)
	marker := filepath.Join(h.sb.Root, "ran")
	h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE="+marker))
	h.be.resolveErr = fmt.Errorf("%w: gitfile points elsewhere", sandbox.ErrInvalidSpec)
	h.opt.Output = &bytes.Buffer{}
	rec, err := Foreground(h.opt, h.root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "sandbox_spec_invalid") {
		t.Errorf("%s %q", rec.Outcome, rec.Error)
	}
	if len(h.rg.calls) != 0 {
		t.Errorf("git children ran: %v", h.rg.verbs())
	}
	if _, serr := os.Stat(marker); serr == nil || h.be.called("wrap") != 0 {
		t.Errorf("the command ran or was wrapped")
	}
}

func TestPostCheckFindingsAreQuarantined(t *testing.T) {
	for _, origin := range []model.VerifyOrigin{model.OriginHook, model.OriginCLI} {
		t.Run(string(origin), func(t *testing.T) {
			h := newHookFx(t)
			h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
			var found string
			h.opt.trace = func(s string) {
				if s == "postcheck" {
					root := h.root
					if origin == model.OriginHook {
						root = h.be.specs[0].Tree
					}
					found = filepath.Join(root, ".claude", "settings.local.json")
					h.be.postFound = []string{found}
				}
			}
			var rec *model.VerifyRecord
			var err error
			if origin == model.OriginHook {
				rec, err = h.runHook("test")
			} else {
				h.opt.Output = &bytes.Buffer{}
				rec, err = Foreground(h.opt, h.root, "test")
			}
			if err != nil {
				t.Fatal(err)
			}
			if rec.Outcome != model.OutcomeInfra || rec.Error != "protected_path_created" || rec.ValidPass() || rec.ExitCode != nil {
				t.Errorf("record: %+v", rec)
			}
			if len(h.be.quarantined) != 1 {
				t.Fatalf("quarantine calls: %d", len(h.be.quarantined))
			}
			q := h.be.quarantined[0]
			if q.dir != filepath.Join(h.opt.Layout.QuarantineDir, rec.JobID) || !reflect.DeepEqual(q.paths, []string{found}) {
				t.Errorf("quarantine call: %+v", q)
			}
			if origin == model.OriginHook && !reflect.DeepEqual(q.roots, []string{h.be.specs[0].Tree}) {
				t.Errorf("roots = %v", q.roots)
			}
			if origin == model.OriginCLI && !reflect.DeepEqual(q.roots, []string{h.repoInfo().Toplevel, h.repoInfo().GitDir}) {
				t.Errorf("roots = %v", q.roots)
			}
			var detail struct {
				Diag  string   `json:"diag"`
				Paths []string `json:"paths"`
			}
			var seen bool
			for _, a := range h.audit.all() {
				if a.Type == model.AuditDiag {
					_ = json.Unmarshal(a.Detail, &detail)
					if detail.Diag == model.DiagSandboxQuarantine {
						seen = true
						if !reflect.DeepEqual(detail.Paths, []string{".claude/settings.local.json"}) {
							t.Errorf("detail paths = %v", detail.Paths)
						}
					}
				}
			}
			if !seen {
				t.Errorf("no sandbox_quarantine diag")
			}
		})
	}
}

func TestPostCheckResidueAndScanFailure(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	h.be.postFound = []string{"/x"}
	rec, err := h.runHook("test")
	if err != nil || rec.Error != "protected_path_created" {
		t.Fatalf("%+v %v", rec, err)
	}
	// A scan problem without findings fails closed.
	h2 := newHookFx(t)
	h2.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	h2.be.postErr = errors.New("cannot read dir")
	rec, err = h2.runHook("test")
	if err != nil || rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "sandbox_postcheck_failed") || rec.ValidPass() {
		t.Fatalf("%+v %v", rec, err)
	}
}

// --- terminal (§3.4) -------------------------------------------------------

func TestWrappedCommandStartsANewSessionWithoutATerminal(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "report"))
	var out bytes.Buffer
	h.opt.Output = &out
	rec, err := Foreground(h.opt, h.root, "test") // origin=cli, wrapped by the fake T1 require policy
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	for _, text := range []string{out.String(), readFileT(t, rec.LogPath)} {
		sid, pid := lineValue(text, "sid="), lineValue(text, "pid=")
		if sid == "" || sid == "-1" {
			t.Skip("no getsid on this platform")
		}
		if !strings.HasPrefix(sid, strings.Fields(pid)[0]) {
			t.Errorf("sid %s != pid %s: the command is not a session leader", sid, pid)
		}
		if !strings.Contains(text, "stdin-null=true") {
			t.Errorf("stdin is not /dev/null:\n%s", text)
		}
	}
	// Foreground output goes through a pipe, never the terminal.
	if got := lineValue(out.String(), "stdout="); got != "pipe" {
		t.Errorf("foreground stdout = %q, want pipe", got)
	}
	// Background (no tee): the job log file.
	h.opt.Output = nil
	started, _, err := h.startHook("test")
	if err != nil {
		t.Fatal(err)
	}
	rec, err = RunJob(h.opt, started[0].JobID)
	if err != nil {
		t.Fatal(err)
	}
	log := readFileT(t, rec.LogPath)
	if got := lineValue(log, "stdout="); got != "file" {
		t.Errorf("background stdout = %q, want file", got)
	}
	if sid, pid := lineValue(log, "sid="), lineValue(log, "pid="); sid != pid {
		t.Errorf("hook job: sid %s pid %s", sid, pid)
	}
}

// lineValue returns the text after prefix on the first line that starts with
// it ("sid=123 pid=123" lines come as one line: the helper prints them
// together, so the first field is returned for "sid=" and the rest for "pid=").
func lineValue(text, prefix string) string {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "sid=") {
			f := strings.Fields(l)
			if prefix == "sid=" {
				return strings.TrimPrefix(f[0], "sid=")
			}
			if prefix == "pid=" && len(f) > 1 {
				return strings.TrimPrefix(f[1], "pid=")
			}
		}
		if prefix != "sid=" && prefix != "pid=" && strings.HasPrefix(l, prefix) {
			return strings.TrimPrefix(l, prefix)
		}
	}
	return ""
}

// An unwrapped origin=cli run keeps today's behaviour (own process group, the
// runner's session), so only wrapped commands change.
func TestUnwrappedRunKeepsProcessGroupBehaviour(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "report"))
	var out bytes.Buffer
	f.opt.Output = &out
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	if rec.Sandbox != "none" || rec.Tier != model.TierT0 || rec.Origin != model.OriginCLI || rec.Workspace != model.WorkspaceWorktree {
		t.Errorf("record: %+v", rec)
	}
}

// A protected path found inside an administrator's runner_allow_write root
// is handed to Quarantine with that root among its roots (Quarantine refuses
// paths outside the roots it receives), and reported relative to it.
func TestQuarantineRootsIncludeRunnerAllowWrite(t *testing.T) {
	h := newHookFx(t)
	allow := filepath.Join(h.sb.Root, "cache")
	if err := os.MkdirAll(allow, 0o755); err != nil {
		t.Fatal(err)
	}
	info := t1Info()
	info.RunnerAllowWrite = []string{allow}
	h.opt.Tier = fakeTier{info}
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	found := filepath.Join(allow, ".claude", "settings.local.json")
	h.opt.trace = func(s string) {
		if s == "postcheck" {
			h.be.postFound = []string{found}
		}
	}
	rec, err := h.runHook("test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Error != "protected_path_created" || len(h.be.quarantined) != 1 {
		t.Fatalf("record %+v, quarantine calls %d", rec, len(h.be.quarantined))
	}
	q := h.be.quarantined[0]
	if !reflect.DeepEqual(q.paths, []string{found}) || !reflect.DeepEqual(q.roots, []string{h.be.specs[0].Tree, allow}) {
		t.Errorf("quarantine call: %+v", q)
	}
}
