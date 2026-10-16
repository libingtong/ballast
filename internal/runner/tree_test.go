package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/sys"
)

// --- fixtures ----------------------------------------------------------

// richRepo adds an executable, a symlink, an exempt file and ignore rules to
// the fixture and commits.
func (h *hookFx) richRepo(specs ...specJSON) {
	h.t.Helper()
	h.write("bin/run.sh", "#!/bin/sh\necho run\n")
	if err := os.Chmod(filepath.Join(h.root, "bin", "run.sh"), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Symlink("src/main.txt", filepath.Join(h.root, "link")); err != nil {
		h.t.Fatal(err)
	}
	h.write("docs/guide.md", "# guide\n")
	h.write(".gitignore", "node_modules/\nbuild/\n")
	h.setProject(specs...)
}

func (f *fx) pinFor(name string) PinnedEntry {
	f.t.Helper()
	data := []byte(readFileT(f.t, filepath.Join(f.root, ".ballast", "project.json")))
	cfg, err := project.Decode(data)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, v := range cfg.Verify {
		if v.Name == name {
			return PinnedEntry{Spec: v, Exempt: cfg.Exempt, ProjectJSONHash: project.Hash(data)}
		}
	}
	f.t.Fatalf("no entry %q", name)
	return PinnedEntry{}
}

func (f *fx) repoInfo() model.RepoInfo {
	f.t.Helper()
	repo, err := f.opt.Repo.Locate(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	return *repo
}

// realS is the snapshot of the real work tree, computed natively.
func (f *fx) realS(exempt []string) model.TreeID {
	f.t.Helper()
	repo := f.repoInfo()
	flt, err := snapshot.CompileExempt(exempt)
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := snapshot.TreeS(snapshot.Options{FS: sys.OSFS{}, Clock: sys.RealClock{}, AllowFallback: true, Env: sys.MapEnv{Vars: f.vars}}, &repo, flt)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (h *hookFx) materialize(name, jobID string) (Tree, error) {
	h.t.Helper()
	return Materialize(h.opt, h.repoInfo(), jobID, h.pinFor(name))
}

type lsEntry struct{ mode, sha, path string }

// lsFiles lists the index of the real repository.
func (f *fx) lsFiles() []lsEntry {
	f.t.Helper()
	out := f.git("ls-files", "-s", "-z")
	var es []lsEntry
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, p, _ := strings.Cut(rec, "\t")
		fl := strings.Fields(meta)
		es = append(es, lsEntry{fl[0], fl[1], p})
	}
	return es
}

func noJobDir(t *testing.T, h *hookFx, jobID string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(h.opt.Layout.WorkDir, jobID)); err == nil {
		t.Errorf("%s still exists", filepath.Join(h.opt.Layout.WorkDir, jobID))
	}
}

// --- Materialize on a quiet repository ----------------------------------

func TestMaterializeQuietRepo(t *testing.T) {
	h := newHookFx(t)
	h.richRepo(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	pin := h.pinFor("test")
	tr, err := h.materialize("test", "job-quiet")
	if err != nil {
		t.Fatal(err)
	}
	if want := h.realS(pin.Exempt); tr.SStart != want {
		t.Errorf("SStart = %s, snapshot.TreeS = %s", tr.SStart, want)
	}
	if tr.Full == tr.SStart {
		t.Errorf("Full == SStart although exempt paths exist")
	}
	if tr.Dir != filepath.Join(tr.Root, "tree") || filepath.Dir(tr.Root) != h.realWorkDir() {
		t.Errorf("layout: %+v", tr)
	}
	if tr.InputsHash != "" || tr.Inputs != 0 {
		t.Errorf("inputs: %q %d", tr.InputsHash, tr.Inputs)
	}
	// Every tracked file (exempt ones included) is in the copy, byte for
	// byte, with the exec bit; symlinks are symlinks; there is no .git.
	sawExempt, sawLink, sawExec := false, false, false
	for _, e := range h.lsFiles() {
		cp := filepath.Join(tr.Dir, filepath.FromSlash(e.path))
		src := filepath.Join(h.root, filepath.FromSlash(e.path))
		li, err := os.Lstat(cp)
		if err != nil {
			t.Errorf("%s missing in the copy: %v", e.path, err)
			continue
		}
		switch e.mode {
		case "120000":
			sawLink = true
			want, _ := os.Readlink(src)
			got, err := os.Readlink(cp)
			if err != nil || got != want {
				t.Errorf("symlink %s = %q, %v; want %q", e.path, got, err, want)
			}
		default:
			if got, want := readFileT(t, cp), readFileT(t, src); got != want {
				t.Errorf("%s differs: %q vs %q", e.path, got, want)
			}
			if isExec := li.Mode()&0o100 != 0; isExec != (e.mode == "100755") {
				t.Errorf("%s exec bit = %v, mode %s", e.path, isExec, e.mode)
			}
			sawExec = sawExec || e.mode == "100755"
		}
		if e.path == "README.md" || e.path == "docs/guide.md" {
			sawExempt = true
		}
	}
	if !sawExempt || !sawLink || !sawExec {
		t.Errorf("fixture lacks exempt/symlink/exec entries: %v %v %v", sawExempt, sawLink, sawExec)
	}
	if _, err := os.Lstat(filepath.Join(tr.Dir, ".git")); err == nil {
		t.Errorf("the copy contains .git")
	}
	if fi, err := os.Stat(tr.Root); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("root perms: %v %v", fi, err)
	}
	// The git calls are the frozen ones, protectHFS/NTFS included.
	var checkout []string
	for _, c := range h.rg.calls {
		if gitVerb(c.args) == "checkout-index" {
			checkout = c.args
		}
	}
	joined := strings.Join(checkout, " ")
	for _, want := range []string{"-c core.protectHFS=true", "-c core.protectNTFS=true", "-c core.fsmonitor=false", "checkout-index -a -f --prefix=" + tr.Dir + string(filepath.Separator)} {
		if !strings.Contains(joined, want) {
			t.Errorf("checkout-index args %q lack %q", joined, want)
		}
	}
	if err := CleanupTree(h.opt, tr); err != nil {
		t.Fatal(err)
	}
	noJobDir(t, h, "job-quiet")
}

func (h *hookFx) realWorkDir() string {
	h.t.Helper()
	if err := os.MkdirAll(h.opt.Layout.WorkDir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	p, err := filepath.EvalSymlinks(h.opt.Layout.WorkDir)
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

func TestMaterializeDirtyTrackedFile(t *testing.T) {
	h := newHookFx(t)
	h.richRepo(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	h.write("src/main.txt", "edited but uncommitted\n")
	pin := h.pinFor("test")
	tr, err := h.materialize("test", "job-dirty")
	if err != nil {
		t.Fatal(err)
	}
	defer CleanupTree(h.opt, tr)
	if got := readFileT(t, filepath.Join(tr.Dir, "src", "main.txt")); got != "edited but uncommitted\n" {
		t.Errorf("copy has %q", got)
	}
	if tr.SStart != h.realS(pin.Exempt) {
		t.Errorf("SStart is not the snapshot of the dirty tree")
	}
}

// --- attacks on the work tree -------------------------------------------

func TestUntrackedAndIgnoredFilesDoNotReachTheCopy(t *testing.T) {
	h := newHookFx(t)
	h.richRepo(helperSpec("test", "stat", "BALLAST_HELPER_FILE=zz_test.go"))
	h.write("zz_test.go", "package x\n// TestMain would exit 0\n")
	h.write("conftest.py", "# hides failures\n")
	h.write("node_modules/pkg/index.js", "module.exports = 1\n")
	if err := os.WriteFile(filepath.Join(h.root, ".git", "info", "exclude"), []byte("conftest.py\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	if log := readFileT(t, rec.LogPath); !strings.Contains(log, "missing") || strings.Contains(log, "exists") {
		t.Errorf("zz_test.go was visible to the test: %q", log)
	}
	// Directly: neither the untracked nor the ignored file is in the copy.
	tr, err := h.materialize("test", "job-untracked")
	if err != nil {
		t.Fatal(err)
	}
	defer CleanupTree(h.opt, tr)
	for _, p := range []string{"zz_test.go", "conftest.py", "node_modules"} {
		if _, err := os.Lstat(filepath.Join(tr.Dir, p)); err == nil {
			t.Errorf("%s was copied", p)
		}
	}
}

// A tiny Go module whose only real test fails; an untracked zz_test.go turns
// the package's TestMain into os.Exit(0). In the work tree the test binary
// "passes"; in the copy the untracked file does not exist.
func TestUntrackedTestMainDoesNotFakeAPass(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	h := newHookFx(t)
	h.write("go.mod", "module example.invalid/fx\n\ngo 1.21\n")
	h.write("a.go", "package fx\n\nfunc One() int { return 1 }\n")
	h.write("a_test.go", "package fx\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 2 {\n\t\tt.Fatal(\"real failure\")\n\t}\n}\n")
	goenv := []string{"GOCACHE=" + filepath.Join(h.sb.Root, "gocache"), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOPATH=" + filepath.Join(h.sb.Root, "gopath"), "GO111MODULE=on", "CGO_ENABLED=0"}
	h.setProject(specJSON{Name: "test", Argv: []string{"go", "test", "./..."}, Required: true, Env: goenv})
	h.write("zz_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) { os.Exit(0) }\n\nvar _ = testing.Short\n")
	// Control: in the work tree the fake pass works.
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = h.root
	cmd.Env = append(h.environ(), goenv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("the control run (go test in the work tree) failed, cannot demonstrate the attack here: %v\n%s", err, out)
	}
	rec, err := h.runHook("test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomeFail {
		t.Fatalf("outcome = %s (%s), want fail from the real test\n%s", rec.Outcome, rec.Error, readFileT(t, rec.LogPath))
	}
}

func TestSwapDuringRunDoesNotAffectTheCopy(t *testing.T) {
	h := newHookFx(t)
	h.write("check.txt", "real version\n")
	h.setProject(helperSpec("test", "cat", "BALLAST_HELPER_FILE=check.txt"))
	h.be.onWrap = func(sandboxSpecT) {
		// A-B-A in the real work tree while the "test" is about to run.
		p := filepath.Join(h.root, "check.txt")
		if err := os.WriteFile(p, []byte("trivial\n"), 0o644); err != nil {
			t.Error(err)
		}
		time.Sleep(10 * time.Millisecond)
		if err := os.WriteFile(p, []byte("real version\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	if log := readFileT(t, rec.LogPath); !strings.Contains(log, "real version") || strings.Contains(log, "trivial") {
		t.Errorf("the test did not read the original: %q", log)
	}
	if !rec.ValidPass() {
		t.Errorf("not a valid pass: %s %s", rec.SStart, rec.SEnd)
	}
	// And the swap before the run, restored later, leaves a stale record.
	h2 := newHookFx(t)
	h2.write("check.txt", "real version\n")
	h2.setProject(helperSpec("test", "cat", "BALLAST_HELPER_FILE=check.txt"))
	started, _, err := h2.startHook("test")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(h2.root, "check.txt")
	if err := os.WriteFile(p, []byte("trivial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trivialS := h2.realS([]string{"**/*.md", "docs/**"})
	restore := false
	h2.rg.before = func(args []string) {
		if !restore && gitVerb(args) == "add" {
			restore = true
		}
	}
	rec, err = RunJob(h2.opt, started[0].JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !restore {
		t.Fatal("no git add observed")
	}
	if rec.SStart != trivialS {
		t.Errorf("S_start = %s, want the trivial tree %s", rec.SStart, trivialS)
	}
	if err := os.WriteFile(p, []byte("real version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if now := h2.realS([]string{"**/*.md", "docs/**"}); now == rec.SStart {
		t.Errorf("after restoring the real file the record is still fresh")
	}
	// `verify --check` agrees: the record is for the trivial tree, so with
	// the real file back it is stale.
	if res, err := h2.check(); err != nil || res.Value != model.TriFalse || res.Cause != "stale" {
		t.Errorf("check after restore: %+v %v", res, err)
	}
	if err := os.WriteFile(p, []byte("trivial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := h2.check(); err != nil || res.Value != model.TriTrue {
		t.Errorf("check while the work tree is still trivial: %+v %v", res, err)
	}
}

// check runs `verify --check --worktree` for the fixture repository.
func (f *fx) check() (model.FreshResult, error) {
	return evidence.Check(evidence.Options{
		Layout: f.opt.Layout, FS: sys.OSFS{}, Clock: sys.RealClock{}, IDs: sys.RandomID{},
		Locator: f.opt.Repo, Project: f.opt.Project, Env: sys.MapEnv{Vars: f.vars},
	}, f.root, evidence.CheckOptions{Mode: evidence.CheckWorktree})
}

func TestTestMutatingTheCopyIsNotAValidPass(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE=src/main.txt"))
	rec, err := h.runHook("test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomePass || rec.SStart == rec.SEnd || rec.ValidPass() {
		t.Errorf("mutating the copy: %+v", rec)
	}
	if got := readFileT(t, filepath.Join(h.root, "src", "main.txt")); got != "hello\n" {
		t.Errorf("the real file changed: %q", got)
	}
}

// --- inputs_untracked ----------------------------------------------------

func inputsRepo(t *testing.T, globs ...string) *hookFx {
	t.Helper()
	h := newHookFx(t)
	h.write(".gitignore", "node_modules/\nbuild/\n")
	spec := helperSpec("test", "exit", "BALLAST_HELPER_CODE=0")
	spec.InputsUntracked = globs
	h.setProject(spec)
	h.write("node_modules/pkg/index.js", "module.exports = 1\n")
	h.write("node_modules/pkg/bin/tool.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(h.root, "node_modules", "pkg", "bin", "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../pkg/index.js", filepath.Join(h.root, "node_modules", "pkg", "alias.js")); err != nil {
		t.Fatal(err)
	}
	h.write("build/out.bin", "not whitelisted\n")
	return h
}

func TestInputsUntrackedCopy(t *testing.T) {
	h := inputsRepo(t, "node_modules/**")
	tr, err := h.materialize("test", "job-inputs")
	if err != nil {
		t.Fatal(err)
	}
	defer CleanupTree(h.opt, tr)
	nm := filepath.Join(tr.Dir, "node_modules", "pkg")
	if got := readFileT(t, filepath.Join(nm, "index.js")); got != "module.exports = 1\n" {
		t.Errorf("index.js = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(nm, "bin", "tool.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Errorf("exec bit lost: %v %v", fi, err)
	}
	if tgt, err := os.Readlink(filepath.Join(nm, "alias.js")); err != nil || tgt != "../pkg/index.js" {
		t.Errorf("symlink = %q, %v", tgt, err)
	}
	if _, err := os.Lstat(filepath.Join(tr.Dir, "build")); err == nil {
		t.Errorf("a non-whitelisted ignored file was copied")
	}
	if tr.Inputs != 3 {
		t.Errorf("Inputs = %d", tr.Inputs)
	}
	// The digest follows the frozen format, computed independently.
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	lines := []string{
		"120000 " + sum("../pkg/index.js") + " node_modules/pkg/alias.js\n",
		"100755 " + sum("#!/bin/sh\n") + " node_modules/pkg/bin/tool.sh\n",
		"100644 " + sum("module.exports = 1\n") + " node_modules/pkg/index.js\n",
	}
	sort.Strings(lines) // all three start with distinct modes; sort by path instead
	byPath := []string{lines[0], lines[1], lines[2]}
	sort.Slice(byPath, func(i, j int) bool {
		return strings.SplitN(byPath[i], " ", 3)[2] < strings.SplitN(byPath[j], " ", 3)[2]
	})
	if want := sum(strings.Join(byPath, "")); tr.InputsHash != want {
		t.Errorf("InputsHash = %s, want %s", tr.InputsHash, want)
	}
	// No hard links: changing the source later does not change the copy.
	if err := os.WriteFile(filepath.Join(h.root, "node_modules", "pkg", "index.js"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readFileT(t, filepath.Join(nm, "index.js")); got != "module.exports = 1\n" {
		t.Errorf("the copy followed the source: %q", got)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(nm, "index.js"))
		if st := fileNlink(fi); st > 1 {
			t.Errorf("copy has %d links", st)
		}
	}
}

func TestInputsHashInRecordAndEmptyWhitelist(t *testing.T) {
	h := inputsRepo(t, "node_modules/**")
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	if len(rec.InputsHash) != 64 || rec.Workspace != model.WorkspaceCopy || rec.Origin != model.OriginHook {
		t.Errorf("record: %+v", rec)
	}
	h2 := inputsRepo(t) // no whitelist
	rec2, err := h2.runHook("test")
	if err != nil || rec2.InputsHash != "" {
		t.Errorf("record without whitelist: %q %v", rec2.InputsHash, err)
	}
}

func TestInputsTooLarge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files int
		bytes int64
	}{
		{"files", 2, 1 << 40},
		{"bytes", 1 << 20, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := inputsRepo(t, "node_modules/**")
			defer func(f int, b int64) { maxInputFiles, maxInputBytes = f, b }(maxInputFiles, maxInputBytes)
			maxInputFiles, maxInputBytes = tc.files, tc.bytes
			_, err := h.materialize("test", "job-big")
			if !errors.Is(err, ErrInputsTooLarge) {
				t.Fatalf("err = %v", err)
			}
			noJobDir(t, h, "job-big")
			// As a job: infra, the command is not started.
			rec, err := h.runHook("test")
			if err != nil || rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "inputs_too_large") {
				t.Errorf("job: %+v %v", rec, err)
			}
			if h.be.called("wrap") != 0 {
				t.Errorf("the command was wrapped")
			}
		})
	}
}

func TestGitlinkIsRefused(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE="+filepath.Join(h.sb.Root, "ran")))
	h.git("update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(h.git("rev-parse", "HEAD"))+",sub")
	h.git("commit", "-q", "-m", "gitlink")
	if err := os.MkdirAll(filepath.Join(h.root, "sub"), 0o755); err != nil { // an uninitialised submodule keeps its gitlink
		t.Fatal(err)
	}
	_, err := h.materialize("test", "job-sub")
	if !errors.Is(err, ErrTreeSubmodule) {
		t.Fatalf("err = %v", err)
	}
	noJobDir(t, h, "job-sub")
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomeInfra || rec.Error != "tree_has_submodule: gitlink \"sub\"" {
		t.Fatalf("%+v %v", rec, err)
	}
	if _, err := os.Stat(filepath.Join(h.sb.Root, "ran")); err == nil {
		t.Errorf("the test command ran")
	}
	if rec.Tier != model.TierT0 || rec.ValidPass() {
		t.Errorf("record: %+v", rec)
	}
}

// --- wrap-up order -------------------------------------------------------

func TestWrapUpOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  string
		found []string
		want  []string
	}{
		{"bwrap quiet", "bwrap", nil, []string{"exit", "postcheck", "tree_send", "record", "audit", "cleanup"}},
		{"sandbox-exec quiet", "sandbox-exec", nil, []string{"exit", "killpg", "postcheck", "tree_send", "record", "audit", "cleanup"}},
		{"bwrap found", "bwrap", []string{"X"}, []string{"exit", "postcheck", "quarantine", "tree_send", "record", "audit", "cleanup"}},
		{"sandbox-exec found", "sandbox-exec", []string{"X"}, []string{"exit", "killpg", "postcheck", "quarantine", "tree_send", "record", "audit", "cleanup"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookFx(t)
			h.be.kind = sandboxKind(tc.kind)
			h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
			var calls []string
			h.opt.trace = func(s string) {
				calls = append(calls, s)
				if s == "postcheck" {
					h.be.postFound = nil
					if len(tc.found) > 0 {
						h.be.postFound = []string{filepath.Join(h.opt.Layout.WorkDir, "x")}
					}
				}
			}
			// The fake backend records quarantine through the trace too.
			rec, err := h.runHook("test")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(calls, ",") != strings.Join(tc.want, ",") {
				t.Errorf("order = %v\nwant   %v", calls, tc.want)
			}
			if len(tc.found) > 0 && rec.Outcome != model.OutcomeInfra {
				t.Errorf("outcome = %s", rec.Outcome)
			}
			// The tree is gone only after the record exists.
			noJobDir(t, h, rec.JobID)
		})
	}
}

// --- git source of truth -------------------------------------------------

func TestSStartIsTheSnapshotForEveryJob(t *testing.T) {
	h := newHookFx(t)
	h.richRepo(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	rec, err := h.runHook("test")
	if err != nil || !rec.ValidPass() {
		t.Fatalf("%+v %v", rec, err)
	}
	if want := h.realS(h.pinFor("test").Exempt); rec.SStart != want {
		t.Errorf("S_start = %s, snapshot.TreeS = %s", rec.SStart, want)
	}
	// project_json_hash is the blob's hash.
	blob := h.git("cat-file", "blob", string(rec.SStart)+":.ballast/project.json")
	sum := sha256.Sum256([]byte(blob))
	if hex.EncodeToString(sum[:]) != rec.ProjectJSONHash {
		t.Errorf("project_json_hash %s is not the hash of the blob in S_start", rec.ProjectJSONHash)
	}
}

func TestResolveGitDirsMismatchRunsNoGit(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	h.be.resolveErr = fmt.Errorf("sandbox_spec_invalid: gitfile points elsewhere")
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomeInfra || !strings.Contains(rec.Error, "sandbox_spec_invalid") {
		t.Fatalf("%+v %v", rec, err)
	}
	if n := len(h.rg.calls); n != 0 {
		t.Errorf("%d git children ran: %v", n, h.rg.verbs())
	}
	if h.be.called("wrap") != 0 {
		t.Errorf("wrapped")
	}
	noJobDir(t, h, rec.JobID)
	// A RepoInfo whose git dir differs from the resolved one: same.
	h2 := newHookFx(t)
	h2.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	repo := h2.repoInfo()
	repo.GitDir = filepath.Join(h2.sb.Root, "elsewhere", ".git")
	if _, err := Materialize(h2.opt, repo, "job-mismatch", h2.pinFor("test")); !errors.Is(err, ErrMaterialize) {
		t.Errorf("err = %v", err)
	}
	if n := len(h2.rg.calls); n != 0 {
		t.Errorf("%d git children ran", n)
	}
}

// --- CleanupTree --------------------------------------------------------

func TestCleanupTree(t *testing.T) {
	h := newHookFx(t)
	h.richRepo(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	tr, err := h.materialize("test", "job-clean")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(h.sb.Root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tr.Dir, "escape")); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(tr.Dir, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "sub", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := CleanupTree(h.opt, tr); err != nil {
		t.Fatal(err)
	}
	noJobDir(t, h, "job-clean")
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("CleanupTree followed a symlink out of the tree: %v", err)
	}
	if err := CleanupTree(h.opt, tr); err != nil {
		t.Errorf("second CleanupTree: %v", err)
	}
	if err := CleanupTree(h.opt, Tree{}); err != nil {
		t.Errorf("empty Tree: %v", err)
	}
}

// --- code review item: no absolute-path writes in the copy path ----------

func TestTreeSourcesHaveNoAbsolutePathWrites(t *testing.T) {
	for _, name := range []string{"tree.go", "inputs.go"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"os.Create(", "os.OpenFile(", "os.MkdirAll(", "os.WriteFile(", "os.Mkdir(", "os.Symlink(", "os.Link("} {
			if bytes.Contains(src, []byte(bad)) {
				t.Errorf("%s contains %s: copies must go through os.Root", name, bad)
			}
		}
	}
	// inputs.go copies agent-influenced files: it may read through opt.FS
	// (O_RDONLY, no-follow) but every write goes through os.Root. (tree.go's
	// own FS.MkdirAll / FS.OpenFile calls create the runner's private work
	// directories and index copy and are deliberately not covered.)
	inputsSrc, _ := os.ReadFile("inputs.go")
	for n, line := range strings.Split(string(inputsSrc), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		for _, bad := range []string{"FS.MkdirAll(", "FS.WriteFile(", "FS.Symlink(", "FS.Rename(", "FS.Remove(", "FS.RemoveAll("} {
			if strings.Contains(line, bad) {
				t.Errorf("inputs.go:%d uses %s: writes must go through os.Root", n+1, bad)
			}
		}
		if strings.Contains(line, "FS.OpenFile(") && !strings.Contains(line, "os.O_RDONLY") {
			t.Errorf("inputs.go:%d opens through FS for something other than reading: %s", n+1, strings.TrimSpace(line))
		}
	}
	src, _ := os.ReadFile("inputs.go")
	for _, need := range []string{"os.OpenRoot(", "root.Mkdir(", "root.OpenFile(", "root.Symlink(", "root.Lstat("} {
		if !strings.Contains(strings.ReplaceAll(string(src), "c.root.", "root."), need) {
			t.Errorf("inputs.go does not use %s", need)
		}
	}
}

// project.json verify[].inputs_untracked decodes into the pinned entry.
func TestProjectDecodesInputsUntracked(t *testing.T) {
	cfg, err := project.Decode([]byte(`{"verify":[{"name":"t","argv":["x"],"inputs_untracked":["node_modules/**",".venv/**"]},{"name":"u","argv":["y"],"inputs_untracked":[]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Verify[0].InputsUntracked; len(got) != 2 || got[0] != "node_modules/**" || got[1] != ".venv/**" {
		t.Errorf("t: %v", got)
	}
	if len(cfg.Verify[1].InputsUntracked) != 0 {
		t.Errorf("u: %v", cfg.Verify[1].InputsUntracked)
	}
	h := newHookFx(t)
	spec := helperSpec("test", "exit")
	spec.InputsUntracked = []string{"a/**"}
	h.setProject(spec)
	if got := h.pinFor("test").Spec.InputsUntracked; len(got) != 1 || got[0] != "a/**" {
		t.Errorf("pinned entry: %v", got)
	}
}
