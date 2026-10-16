//go:build unix

package runner

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sandbox"
	"github.com/libingtong/ballast/internal/sys"
)

// realFx is a hook fixture wired to the REAL sandbox backend (fake T1 tier,
// no fake backend). The tests skip when no backend works; with CI=true and
// BALLAST_REQUIRE_BWRAP=1 (linux) / BALLAST_REQUIRE_SANDBOX_EXEC=1 (darwin)
// a missing backend fails instead (scripts/sandbox-docker.sh sets them).
func realFx(t *testing.T) (*hookFx, sandbox.Kind) {
	t.Helper()
	h := newHookFx(t)
	kind, err := sandbox.Probe(sandbox.Options{FS: sys.OSFS{}, Env: sys.MapEnv{Vars: h.vars}, GOOS: runtime.GOOS})
	if kind == sandbox.KindNone {
		required := os.Getenv("CI") == "true" &&
			((runtime.GOOS == "linux" && os.Getenv("BALLAST_REQUIRE_BWRAP") == "1") ||
				(runtime.GOOS == "darwin" && os.Getenv("BALLAST_REQUIRE_SANDBOX_EXEC") == "1"))
		if required {
			t.Fatalf("no sandbox backend although one is required: %v", err)
		}
		t.Skipf("no sandbox backend: %v", err)
	}
	h.opt.backend = nil // the real one
	return h, kind
}

func writeprobeSpec(paths ...string) specJSON {
	return helperSpec("test", "writeprobe", "BALLAST_PROBE_PATHS="+strings.Join(paths, "\n"))
}

func probeResults(log string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(log, "\n") {
		if v, p, ok := strings.Cut(l, " "); ok && (v == "ok" || v == "fail") {
			out[p] = v
		}
	}
	return out
}

func TestRealSandboxHookJob(t *testing.T) {
	h, kind := realFx(t)
	bashrc := filepath.Join(h.sb.Home, ".bashrc")
	evidence := filepath.Join(h.sb.BallastHome, "evidence-probe")
	rules := filepath.Join(h.sb.BallastHome, "rules.json")
	realFile := filepath.Join(h.root, "src", "main.txt")
	realNew := filepath.Join(h.root, "new-in-real-tree.txt")
	hooks := filepath.Join(h.root, ".git", "hooks", "pre-commit")
	project := filepath.Join(h.root, ".ballast", "project.json")
	h.setProject(writeprobeSpec("inside.txt", "@tmp/t.txt", bashrc, evidence, rules, realFile, realNew, hooks, project))
	before := readFileT(t, realFile)
	projectBefore := readFileT(t, project)

	rec, err := h.runHook("test")
	if err != nil {
		t.Fatal(err)
	}
	log := readFileT(t, rec.LogPath)
	if rec.Outcome != model.OutcomePass {
		t.Fatalf("outcome %s (%s)\n%s", rec.Outcome, rec.Error, log)
	}
	if rec.Tier != model.TierT1 || rec.Sandbox != string(kind) || rec.Origin != model.OriginHook || rec.Workspace != model.WorkspaceCopy || !rec.ValidPass() {
		t.Errorf("record: %+v", rec)
	}
	res := probeResults(log)
	for _, p := range []string{"inside.txt", "@tmp/t.txt"} {
		if res[p] != "ok" {
			t.Errorf("write to %s: %q, want ok\n%s", p, res[p], log)
		}
	}
	for _, p := range []string{bashrc, evidence, rules, realFile, realNew, hooks, project} {
		if res[p] != "fail" {
			t.Errorf("write to %s: %q, want fail\n%s", p, res[p], log)
		}
		if p != realFile && p != project {
			if _, err := os.Lstat(p); err == nil {
				t.Errorf("%s exists on the host", p)
			}
		}
	}
	if readFileT(t, realFile) != before || readFileT(t, project) != projectBefore {
		t.Errorf("the real work tree changed")
	}
	noJobDir(t, h, rec.JobID)
}

func TestRealSandboxProtectedPathCreatedInTheCopy(t *testing.T) {
	h, kind := realFx(t)
	h.setProject(writeprobeSpec(".claude/settings.local.json", "inside.txt"))
	rec, err := h.runHook("test")
	if err != nil {
		t.Fatal(err)
	}
	log := readFileT(t, rec.LogPath)
	res := probeResults(log)
	if res["inside.txt"] != "ok" {
		t.Fatalf("inside.txt: %q\n%s", res["inside.txt"], log)
	}
	switch kind {
	case sandbox.KindSandboxExec:
		// The profile refuses the creation itself.
		if res[".claude/settings.local.json"] != "fail" || rec.Outcome != model.OutcomePass {
			t.Errorf("darwin: %q %s %s\n%s", res[".claude/settings.local.json"], rec.Outcome, rec.Error, log)
		}
	default:
		// bwrap cannot bind a path that does not exist: PostCheck finds it,
		// Quarantine moves it, the job is infra.
		if res[".claude/settings.local.json"] != "ok" || rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "protected_path_created") || rec.ValidPass() {
			t.Errorf("linux: %q %s %s\n%s", res[".claude/settings.local.json"], rec.Outcome, rec.Error, log)
		}
		q := filepath.Join(h.opt.Layout.QuarantineDir, rec.JobID, ".claude", "settings.local.json")
		if _, err := os.Lstat(q); err != nil {
			t.Errorf("not quarantined at %s: %v", q, err)
		}
		if !contains(diagNames(h.audit.all()), model.DiagSandboxQuarantine) {
			t.Errorf("no sandbox_quarantine diag")
		}
	}
	noJobDir(t, h, rec.JobID)
}

func TestRealSandboxCLIJob(t *testing.T) {
	h, kind := realFx(t)
	bashrc := filepath.Join(h.sb.Home, ".bashrc")
	rules := filepath.Join(h.sb.BallastHome, "rules.json")
	project := filepath.Join(h.root, ".ballast", "project.json")
	hooks := filepath.Join(h.root, ".git", "hooks", "pre-commit")
	h.setProject(writeprobeSpec("inside-repo.txt", bashrc, rules, project, hooks, filepath.Join(h.root, ".git", "config")))
	var out bytes.Buffer
	h.opt.Output = &out
	rec, err := Foreground(h.opt, h.root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomePass || rec.Tier != model.TierT1 || rec.Sandbox != string(kind) || rec.Workspace != model.WorkspaceWorktree {
		t.Fatalf("%+v\n%s", rec, out.String())
	}
	res := probeResults(out.String())
	if res["inside-repo.txt"] != "ok" {
		t.Errorf("write inside the work tree: %q", res["inside-repo.txt"])
	}
	for _, p := range []string{bashrc, rules, project, hooks, filepath.Join(h.root, ".git", "config")} {
		if res[p] != "fail" {
			t.Errorf("write to %s: %q, want fail", p, res[p])
		}
	}
	os.Remove(filepath.Join(h.root, "inside-repo.txt"))
}

func TestRealSandboxSessionAndStdin(t *testing.T) {
	h, kind := realFx(t)
	h.setProject(helperSpec("test", "report"))
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	log := readFileT(t, rec.LogPath)
	sid, pid := lineValue(log, "sid="), lineValue(log, "pid=")
	if sid == "" || sid == "-1" || pid == "" {
		t.Skipf("no getsid: %q %q", sid, pid)
	}
	switch kind {
	case sandbox.KindSandboxExec:
		// sandbox-exec execs the command: the process the runner started
		// with Setsid is the command itself.
		if sid != pid {
			t.Errorf("the sandboxed command is not a session leader: sid %s pid %s\n%s", sid, pid, log)
		}
	default:
		// bwrap runs the command as a child of its PID-namespace init, which
		// is the session leader (--new-session): inside the namespace the
		// session id is 1 and the command's pid is not.
		if sid != "1" || pid == "1" {
			t.Errorf("not in bwrap's new session: sid %s pid %s\n%s", sid, pid, log)
		}
	}
	if !strings.Contains(log, "stdin-null=true") {
		t.Errorf("stdin is not /dev/null:\n%s", log)
	}
	if got := lineValue(log, "stdout="); got != "file" {
		t.Errorf("stdout = %q", got)
	}
}

func TestRealSandboxMutationOfTheCopyIsVisible(t *testing.T) {
	h, _ := realFx(t)
	h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE=src/main.txt"))
	rec, err := h.runHook("test")
	if err != nil {
		t.Fatal(err)
	}
	// The copy is writable, the real file is not touched, the pass is not valid.
	if rec.Outcome != model.OutcomePass || rec.ValidPass() || rec.SStart == rec.SEnd {
		t.Errorf("%+v", rec)
	}
	if readFileT(t, filepath.Join(h.root, "src", "main.txt")) != "hello\n" {
		t.Errorf("the real file changed")
	}
}

func TestRealSandboxRunnerAllowWriteAndInvalidSpec(t *testing.T) {
	h, _ := realFx(t)
	allow := filepath.Join(h.sb.Root, "shared-cache")
	if err := os.MkdirAll(allow, 0o755); err != nil {
		t.Fatal(err)
	}
	h.setProject(writeprobeSpec(filepath.Join(allow, "in-allow-write.txt"), filepath.Join(h.sb.Root, "outside-allow.txt")))
	info := t1Info()
	info.RunnerAllowWrite = []string{allow}
	h.opt.Tier = fakeTier{info}
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	res := probeResults(readFileT(t, rec.LogPath))
	if res[filepath.Join(allow, "in-allow-write.txt")] != "ok" || res[filepath.Join(h.sb.Root, "outside-allow.txt")] != "fail" {
		t.Errorf("results: %v", res)
	}

	// An allow-write entry that makes the sandbox meaningless (the home
	// directory) is refused by sandbox.ValidateSpec: infra, not executed.
	marker := filepath.Join(h.sb.Root, "ran")
	h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE="+marker))
	info.RunnerAllowWrite = []string{h.sb.Home}
	h.opt.Tier = fakeTier{info}
	rec, err = h.runHook("test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "sandbox_spec_invalid") || rec.Tier != model.TierT0 {
		t.Errorf("%s %q tier %s", rec.Outcome, rec.Error, rec.Tier)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the command ran under an invalid spec")
	}
	noJobDir(t, h, rec.JobID)
}
