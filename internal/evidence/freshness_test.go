package evidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/sys"
)

type freshCase struct {
	ID    json.RawMessage `json:"id"`
	Desc  string          `json:"desc"`
	Steps []string        `json:"steps"`
	Argv  string          `json:"argv"`
	Fresh string          `json:"fresh"`
	Check int             `json:"check"`
}

func (c freshCase) name() string { return strings.Trim(string(c.ID), `"`) }

type freshFile struct {
	Counter     []freshCase `json:"counter_examples"`
	Positives   []freshCase `json:"positives"`
	Consistency []struct {
		Desc  string   `json:"desc"`
		Steps []string `json:"steps"`
	} `json:"consistency"`
}

// causes our implementation reports per case (beyond the table's columns).
var wantCause = map[string]string{
	"1": "stale", "2": "stale", "3": "not_valid_pass", "4": "not_valid_pass", "5": "no_record",
	"6": "no_record", "7": "no_record", "8": "not_valid_pass", "9": "stale", "10": "stale",
	"11": "job_running", "p1": "fresh", "p2": "fresh", "p3": "fresh",
}

// harness runs the step scripts of freshness.json against fixture repos.
type harness struct {
	t     testing.TB
	sb    *sandbox
	opt   Options
	clock *sys.FakeClock
	ids   *sys.SeqID
	store *Store
	prov  *Provider

	root, other, wtB string
	cwd              string // directory the "shell" is in
	edits            int
}

func newHarness(t testing.TB) *harness {
	requireGit(t)
	sb := newSandbox(t)
	h := &harness{t: t, sb: sb, clock: sys.NewFakeClock(epoch), ids: &sys.SeqID{}}
	h.root = sb.newRepo(t, filepath.Join(sb.root, "repo"))
	h.cwd = h.root
	h.wtB = filepath.Join(sb.root, "wt-b")
	h.opt = sb.options(t, h.clock, h.ids)
	h.store = NewStore(h.opt)
	h.prov = NewProvider(h.opt)
	return h
}

func (h *harness) ensureOther() {
	if h.other != "" {
		return
	}
	h.other = h.sb.newRepo(h.t, filepath.Join(h.sb.root, "other"))
}

func (h *harness) snapOpts() snapshot.Options {
	return snapshot.Options{FS: sys.OSFS{}, Clock: h.clock, AllowFallback: true, Env: h.sb.env}
}

// treeS computes S of the repo containing cwd with its project's exempt.
func (h *harness) treeS() model.TreeID {
	h.t.Helper()
	repo := h.sb.locate(h.t, h.cwd)
	f, err := snapshot.CompileExempt([]string{"**/*.md", "docs/**"})
	if err != nil {
		h.t.Fatal(err)
	}
	s, err := snapshot.TreeS(h.snapOpts(), repo, f)
	if err != nil {
		h.t.Fatalf("TreeS: %v", err)
	}
	return s
}

func (h *harness) tick() time.Time {
	h.clock.Advance(time.Second)
	return h.clock.Now()
}

func (h *harness) writeRecord(name string, outcome model.Outcome, sStart, sEnd model.TreeID) {
	h.t.Helper()
	repo := h.sb.locate(h.t, h.cwd)
	start := h.tick()
	end := h.tick()
	rec := &model.VerifyRecord{
		V: 1, JobID: "20261016T120000-" + h.ids.NewID(3), Name: name, RepoInst: repo.Inst,
		Tier: model.TierT0, SStart: sStart, SEnd: sEnd, TStart: start, TEnd: end, Outcome: outcome,
	}
	if err := h.store.WriteRecord(rec); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) step(step string) {
	h.t.Helper()
	f := strings.Fields(step)
	if len(f) == 0 {
		h.t.Fatal("empty step")
	}
	arg := func(i int) string {
		if i >= len(f) {
			h.t.Fatalf("step %q: missing argument", step)
		}
		return f[i]
	}
	switch f[0] {
	case "edit", "other-session-edit":
		h.edit(arg(1))
	case "add-u":
		h.sb.mustGit(h.t, h.cwd, "add", "-u")
	case "add", "stage":
		h.sb.mustGit(h.t, h.cwd, "add", arg(1))
	case "commit":
		h.sb.mustGit(h.t, h.cwd, "commit", "-q", "--no-verify", "-m", "step")
	case "checkout":
		h.sb.mustGit(h.t, h.cwd, "checkout", "-q", arg(1))
	case "reset-hard":
		h.sb.mustGit(h.t, h.cwd, "reset", "-q", "--hard")
	case "worktree-b":
		h.sb.mustGit(h.t, h.cwd, "worktree", "add", "-q", h.wtB, "-b", "b")
		real, err := filepath.EvalSymlinks(h.wtB)
		if err != nil {
			h.t.Fatal(err)
		}
		h.cwd = real
	case "verify":
		name, res := arg(1), arg(2)
		s := h.treeS()
		switch res {
		case "pass":
			h.writeRecord(name, model.OutcomePass, s, s)
		case "fail":
			h.writeRecord(name, model.OutcomeFail, s, s)
		case "timeout":
			h.writeRecord(name, model.OutcomeTimeout, s, s)
		default:
			h.t.Fatalf("step %q: unknown result", step)
		}
	case "mutate-during-verify":
		s1 := h.treeS()
		h.edit(arg(1))
		h.writeRecord("test", model.OutcomePass, s1, h.treeS())
	case "cd-other":
		h.ensureOther()
		h.cwd = h.other
	case "cd-root":
		h.cwd = h.root
	case "verify-running":
		repo := h.sb.locate(h.t, h.cwd)
		now := h.tick()
		js := &model.JobStatus{
			V: 1, JobID: "20261016T120000-" + h.ids.NewID(3), Name: arg(1), RepoInst: repo.Inst,
			State: model.JobRunning, PID: os.Getpid(), StartedAt: now, Deadline: now.Add(time.Hour),
		}
		if err := h.store.WriteJob(js); err != nil {
			h.t.Fatal(err)
		}
	default:
		h.t.Fatalf("unknown step %q", step)
	}
}

func (h *harness) edit(rel string) {
	h.t.Helper()
	h.edits++
	p := filepath.Join(h.cwd, filepath.FromSlash(rel))
	old, _ := os.ReadFile(p)
	writeFile(h.t, p, string(old)+fmt.Sprintf("\n// edit %d\n", h.edits))
}

// gitNode turns "git [-C dir] commit args..." into a GitInvocation.
func gitNode(t testing.TB, argv string) *model.GitInvocation {
	t.Helper()
	f := strings.Fields(argv)
	if len(f) < 2 || f[0] != "git" {
		t.Fatalf("bad argv %q", argv)
	}
	g := &model.GitInvocation{}
	i := 1
	if f[i] == "-C" {
		g.Cwd = f[i+1]
		i += 2
	}
	g.Sub = f[i]
	for _, a := range f[i+1:] {
		g.Args = append(g.Args, model.Word{Lit: a})
	}
	return g
}

// installHook writes a pre-commit hook that records its cwd and a copy of
// GIT_INDEX_FILE, then aborts the commit.
func (h *harness) installHook(repoDir string) (out string) {
	h.t.Helper()
	out = filepath.Join(h.sb.root, "hookout-"+filepath.Base(repoDir))
	if err := os.MkdirAll(out, 0o755); err != nil {
		h.t.Fatal(err)
	}
	o := filepath.ToSlash(out)
	script := "#!/bin/sh\n" +
		"cp \"$GIT_INDEX_FILE\" \"" + o + "/index\" || exit 2\n" +
		"pwd -P > \"" + o + "/cwd\"\n" +
		"exit 1\n"
	repo := h.sb.locate(h.t, repoDir)
	hook := filepath.Join(repo.CommonDir, "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// runCheck runs `git <argv>` with the capturing hook installed in the repo
// git will commit in, then calls Check exactly like the CLI would under that
// hook: cwd = the hook's cwd, IndexFile = the captured GIT_INDEX_FILE.
func (h *harness) runCheck(argv string) (model.FreshResult, int) {
	h.t.Helper()
	target := h.cwd
	f := strings.Fields(argv)
	if f[1] == "-C" {
		target = filepath.Join(h.cwd, f[2])
	}
	out := h.installHook(target)
	for _, n := range []string{"cwd", "index"} {
		_ = os.Remove(filepath.Join(out, n))
	}
	if o, err := h.sb.git(h.t, h.cwd, nil, f[1:]...); err == nil {
		h.t.Fatalf("git %s succeeded; the hook should abort it:\n%s", argv, o)
	}
	cwdB, err := os.ReadFile(filepath.Join(out, "cwd"))
	if err != nil {
		h.t.Fatalf("hook did not run for %q: %v", argv, err)
	}
	cwd := strings.TrimSpace(string(cwdB))
	res, err := Check(h.opt, cwd, CheckOptions{Mode: CheckCommit, IndexFile: filepath.Join(out, "index")})
	if err != nil {
		h.t.Fatal(err)
	}
	return res, checkExitCode(res)
}

// checkExitCode is the architecture §7 mapping of `verify --check`.
func checkExitCode(r model.FreshResult) int {
	switch r.Value {
	case model.TriTrue:
		return 0
	case model.TriFalse:
		if r.Cause == "no_record" {
			return 11
		}
		return 10
	}
	if r.Cause == "job_running" {
		return 13
	}
	return 12
}

func triOf(s string) model.Tri {
	switch s {
	case "true":
		return model.TriTrue
	case "false":
		return model.TriFalse
	}
	return model.TriUnknown
}

func loadFreshness(t testing.TB) freshFile {
	t.Helper()
	data, err := os.ReadFile("testdata/freshness.json")
	if err != nil {
		t.Fatal(err)
	}
	var f freshFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFreshnessSuite(t *testing.T) {
	requireGit(t)
	ff := loadFreshness(t)
	if len(ff.Counter) != 11 || len(ff.Positives) != 3 {
		t.Fatalf("freshness.json has %d counter-examples and %d positives; want 11 and 3", len(ff.Counter), len(ff.Positives))
	}
	all := append(append([]freshCase{}, ff.Counter...), ff.Positives...)
	for _, c := range all {
		t.Run(c.name()+"/"+strings.ReplaceAll(c.Desc, " ", "_"), func(t *testing.T) {
			h := newHarness(t)
			if strings.Contains(c.Argv, "../other") {
				h.ensureOther()
			}
			for _, s := range c.Steps {
				h.step(s)
			}

			repo := h.sb.locate(t, h.cwd)
			view, err := h.prov.Open(repo, h.clock.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			res := view.VerifyFresh(model.FreshQuery{Kind: model.FreshCommit, Git: gitNode(t, c.Argv), Cwd: h.cwd})
			if res.Value != triOf(c.Fresh) {
				t.Errorf("verify_fresh = %v (%s), want %s; %+v", res.Value, res.Cause, c.Fresh, res)
			}
			if want := wantCause[c.name()]; res.Cause != want {
				t.Errorf("verify_fresh cause = %q, want %q", res.Cause, want)
			}

			cres, code := h.runCheck(c.Argv)
			if code != c.Check {
				t.Errorf("verify --check exit = %d (%s), want %d; %+v", code, cres.Cause, c.Check, cres)
			}
		})
	}
}

func TestFreshnessConsistency(t *testing.T) {
	requireGit(t)
	ff := loadFreshness(t)
	if len(ff.Consistency) == 0 {
		t.Fatal("no consistency rows")
	}
	for _, row := range ff.Consistency {
		t.Run(row.Desc, func(t *testing.T) {
			h := newHarness(t)
			for _, s := range row.Steps {
				h.step(s)
			}
			repo := h.sb.locate(t, h.cwd)
			filter, err := snapshot.CompileExempt([]string{"**/*.md", "docs/**"})
			if err != nil {
				t.Fatal(err)
			}
			S, err := snapshot.TreeS(h.snapOpts(), repo, filter)
			if err != nil {
				t.Fatal(err)
			}
			Call, err := snapshot.TreeC(h.snapOpts(), repo, model.CommitForm{Kind: model.CommitAll}, filter)
			if err != nil {
				t.Fatal(err)
			}

			// git's own answer on a copy of the index.
			tmp := filepath.Join(h.sb.root, "git-index-copy")
			idx, err := os.ReadFile(repo.IndexPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tmp, idx, 0o600); err != nil {
				t.Fatal(err)
			}
			env := []string{"GIT_INDEX_FILE=" + tmp}
			run := func(args ...string) string {
				out, err := h.sb.git(t, h.root, env, args...)
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(out)
			}
			run("add", "-u")
			run("rm", "--cached", "-r", "-q", "-f", "--ignore-unmatch", "--", ":(glob)**/*.md", ":(glob)docs/**")
			want := model.TreeID(run("write-tree"))

			// C(plain) after the real `git add -u`.
			h.sb.mustGit(t, h.root, "add", "-u")
			Cplain, err := snapshot.TreeC(h.snapOpts(), repo, model.CommitForm{Kind: model.CommitPlain}, filter)
			if err != nil {
				t.Fatal(err)
			}
			for name, got := range map[string]model.TreeID{"S": S, "C(all)": Call, "C(plain after add -u)": Cplain} {
				if got != want {
					t.Errorf("%s = %s, git says %s", name, got, want)
				}
			}
		})
	}
}

// Worktree kind: Stop-family checks compare against S of the work tree.
func TestFreshWorktreeKind(t *testing.T) {
	h := newHarness(t)
	h.step("edit src/a.go")
	h.step("verify test pass")
	repo := h.sb.locate(t, h.cwd)
	view, _ := h.prov.Open(repo, time.Time{})
	q := model.FreshQuery{Kind: model.FreshWorktree}
	if r := view.VerifyFresh(q); r.Value != model.TriTrue || r.Cause != "fresh" {
		t.Fatalf("after verify: %+v", r)
	}
	h.step("edit src/b.go")
	if r := view.VerifyFresh(q); r.Value != model.TriFalse || r.Cause != "stale" || len(r.Stale) != 1 || r.Stale[0] != "test" {
		t.Fatalf("after edit: %+v", r)
	}
	// Exempt files do not matter.
	h.step("verify test pass")
	h.step("edit README.md")
	if r := view.VerifyFresh(q); r.Value != model.TriTrue {
		t.Fatalf("exempt edit: %+v", r)
	}
	// Explicit names: lint was never run.
	q.Names = []string{"test", "lint"}
	if r := view.VerifyFresh(q); r.Value != model.TriFalse || r.Cause != "no_record" || len(r.Stale) != 1 || r.Stale[0] != "lint" {
		t.Fatalf("explicit names: %+v", r)
	}
}

// Check in worktree mode and with the default index.
func TestCheckModes(t *testing.T) {
	h := newHarness(t)
	h.step("edit src/a.go")
	h.step("verify test pass")

	r, err := Check(h.opt, h.root, CheckOptions{Mode: CheckWorktree})
	if err != nil || r.Value != model.TriTrue {
		t.Fatalf("worktree mode: %+v, %v", r, err)
	}
	// Plain commit mode without IndexFile uses <GitDir>/index: a.go is not
	// staged, so C differs from S.
	r, err = Check(h.opt, h.root, CheckOptions{})
	if err != nil || r.Value != model.TriFalse || r.Cause != "stale" {
		t.Fatalf("commit mode, default index: %+v, %v", r, err)
	}
	h.step("add-u")
	r, err = Check(h.opt, h.root, CheckOptions{Mode: CheckCommit})
	if err != nil || r.Value != model.TriTrue {
		t.Fatalf("commit mode after add -u: %+v, %v", r, err)
	}
	// A relative IndexFile is resolved against cwd.
	rel, _ := filepath.Rel(h.root, h.sb.locate(t, h.root).IndexPath)
	r, err = Check(h.opt, h.root, CheckOptions{IndexFile: rel})
	if err != nil || r.Value != model.TriTrue {
		t.Fatalf("relative IndexFile: %+v, %v", r, err)
	}
	// Subdirectory cwd locates the same repo.
	r, _ = Check(h.opt, filepath.Join(h.root, "src"), CheckOptions{Mode: CheckWorktree})
	if r.Value != model.TriTrue {
		t.Fatalf("subdir cwd: %+v", r)
	}
	if _, err := Check(h.opt, h.root, CheckOptions{Mode: "bogus"}); err == nil {
		t.Error("bogus mode: want error")
	}
	// Outside any repo.
	r, _ = Check(h.opt, h.sb.root, CheckOptions{})
	if r.Value != model.TriUnknown || r.Cause != "not_a_repo" {
		t.Errorf("not a repo: %+v", r)
	}
	r, _ = Check(h.opt, "", CheckOptions{})
	if r.Value != model.TriUnknown || r.Cause != "not_a_repo" {
		t.Errorf("empty cwd: %+v", r)
	}
	if checkExitCode(r) != 12 {
		t.Errorf("exit code for unknown = %d", checkExitCode(r))
	}
}

// GIT_INDEX_FILE in the process environment never influences the hot path.
func TestHotPathIgnoresGitIndexFile(t *testing.T) {
	h := newHarness(t)
	h.step("verify test pass")
	bogus := filepath.Join(h.sb.root, "bogus-index")
	writeFile(t, bogus, "not an index")
	t.Setenv("GIT_INDEX_FILE", bogus)
	repo := h.sb.locate(t, h.root)
	view, _ := h.prov.Open(repo, time.Time{})
	r := view.VerifyFresh(model.FreshQuery{Kind: model.FreshCommit, Git: gitNode(t, "git commit -m x"), Cwd: h.root})
	if r.Value != model.TriTrue {
		t.Fatalf("%+v", r)
	}
}

// Commit forms beyond freshness.json: path commits (git's next-index), amend
// and a positive -C.
func TestFreshnessExtraForms(t *testing.T) {
	requireGit(t)
	tests := []struct {
		name  string
		steps []string
		argv  string
		fresh string
		cause string
		check int
	}{
		{"paths form, only that path changed", []string{"edit src/a.go", "verify test pass"}, "git commit -m x src/a.go", "true", "fresh", 0},
		{"paths form, other file changed too", []string{"edit src/a.go", "edit src/b.go", "verify test pass"}, "git commit -m x src/a.go", "false", "stale", 10},
		{"paths form with --only", []string{"edit src/a.go", "verify test pass"}, "git commit -m x --only src/a.go", "true", "fresh", 0},
		{"paths form after a dash-dash", []string{"edit src/a.go", "verify test pass"}, "git commit -m x -- src/a.go", "true", "fresh", 0},
		{"amend with everything staged", []string{"edit src/a.go", "add-u", "verify test pass"}, "git commit --amend --no-edit", "true", "fresh", 0},
		{"amend with unstaged change", []string{"edit src/a.go", "verify test pass"}, "git commit --amend --no-edit", "false", "stale", 10},
		{"-C into a verified repo", []string{"cd-other", "edit src/a.go", "verify test pass", "cd-root"}, "git -C ../other commit -a -m x", "true", "fresh", 0},
		{"-C from another repo back into the verified one", []string{"verify test pass", "cd-other"}, "git -C ../repo commit -m x", "true", "fresh", 0},
		{"unparseable form", []string{"verify test pass"}, "git commit -p", "unknown", "unparseable_commit", 12},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.ensureOther()
			for _, s := range tc.steps {
				h.step(s)
			}
			repo := h.sb.locate(t, h.cwd)
			view, _ := h.prov.Open(repo, time.Time{})
			res := view.VerifyFresh(model.FreshQuery{Kind: model.FreshCommit, Git: gitNode(t, tc.argv), Cwd: h.cwd})
			if res.Value != triOf(tc.fresh) || res.Cause != tc.cause {
				t.Errorf("verify_fresh = %v/%s, want %s/%s", res.Value, res.Cause, tc.fresh, tc.cause)
			}
			if tc.argv == "git commit -p" {
				return // interactive: git would wait for input
			}
			if _, code := h.runCheck(tc.argv); code != tc.check {
				t.Errorf("check = %d, want %d", code, tc.check)
			}
		})
	}
}
