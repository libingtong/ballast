package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// --- project.json pinning (v1.5.2) --------------------------------------

func (h *hookFx) writeProjectRaw(data string) {
	h.t.Helper()
	h.write(".ballast/project.json", data)
}

func projectJSON(exempt []string, specs ...specJSON) string {
	b, _ := jsonIndent(map[string]any{"version": 1, "verify": specs, "exempt": exempt})
	return b + "\n"
}

// pinFixture commits an honest project.json whose "test" entry fails, and
// returns the honest bytes, a forged project.json whose "test" entry would
// run the marker command, and the marker path.
func pinFixture(t *testing.T) (h *hookFx, honest, forged, marker string) {
	t.Helper()
	h = newHookFx(t)
	marker = filepath.Join(h.sb.Root, "forged-ran")
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=1"))
	honest = readFileT(t, filepath.Join(h.root, ".ballast", "project.json"))
	forged = projectJSON([]string{"**/*.md", "docs/**"}, helperSpec("test", "touch", "BALLAST_HELPER_FILE="+marker))
	return h, honest, forged, marker
}

func expectPinInfra(t *testing.T, h *hookFx, rec *model.VerifyRecord, err error, wantPrefix, marker string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, wantPrefix) {
		t.Fatalf("outcome %s error %q, want infra %s", rec.Outcome, rec.Error, wantPrefix)
	}
	if rec.ValidPass() || rec.Tier != model.TierT0 {
		t.Errorf("record: %+v", rec)
	}
	if h.be.called("wrap") != 0 {
		t.Errorf("the command was wrapped")
	}
	if marker != "" {
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("the forged command ran")
		}
	}
	noJobDir(t, h, rec.JobID)
}

func TestProjectJSONSwappedBackBetweenReadAndSnapshot(t *testing.T) {
	h, honest, forged, marker := pinFixture(t)
	h.writeProjectRaw(forged)
	started, _, err := h.startHook("test") // the sidecar pins the forged entry
	if err != nil {
		t.Fatal(err)
	}
	done := false
	h.rg.before = func(args []string) {
		if !done && gitVerb(args) == "add" {
			done = true
			h.writeProjectRaw(honest) // restore right before `git add -u`
		}
	}
	rec, err := RunJob(h.opt, started[0].JobID)
	expectPinInfra(t, h, rec, err, "project_json_changed", marker)
}

func TestProjectJSONForgedAfterTheRead(t *testing.T) {
	h, _, forged, marker := pinFixture(t)
	started, _, err := h.startHook("test") // honest pin
	if err != nil {
		t.Fatal(err)
	}
	h.writeProjectRaw(forged) // the snapshot will read this
	rec, err := RunJob(h.opt, started[0].JobID)
	expectPinInfra(t, h, rec, err, "project_json_changed", marker)
}

func TestProjectJSONOtherEntryOrFormatChanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(honest string) string
	}{
		{"other entry", func(honest string) string {
			return projectJSON([]string{"**/*.md", "docs/**"}, helperSpec("test", "exit", "BALLAST_HELPER_CODE=1"), specJSON{Name: "other", Argv: []string{"true"}})
		}},
		{"format only", func(honest string) string { return "\n" + strings.ReplaceAll(honest, " ", "  ") }},
		{"exempt list", func(honest string) string {
			return projectJSON([]string{"**/*.md"}, helperSpec("test", "exit", "BALLAST_HELPER_CODE=1"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, honest, _, _ := pinFixture(t)
			started, _, err := h.startHook("test")
			if err != nil {
				t.Fatal(err)
			}
			h.writeProjectRaw(tc.mutate(honest))
			rec, err := RunJob(h.opt, started[0].JobID)
			expectPinInfra(t, h, rec, err, "project_json_changed", "")
		})
	}
}

func TestProjectJSONNotARegularTrackedFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *hookFx)
	}{
		{"not in the index", func(h *hookFx) { h.git("rm", "--cached", "-q", ".ballast/project.json") }},
		{"ignored", func(h *hookFx) {
			h.git("rm", "--cached", "-q", ".ballast/project.json")
			if err := os.WriteFile(filepath.Join(h.root, ".git", "info", "exclude"), []byte(".ballast/\n"), 0o644); err != nil {
				h.t.Fatal(err)
			}
		}},
		{"symlink", func(h *hookFx) {
			h.write("real.json", readFileT(h.t, filepath.Join(h.root, ".ballast", "project.json")))
			p := filepath.Join(h.root, ".ballast", "project.json")
			if err := os.Remove(p); err != nil {
				h.t.Fatal(err)
			}
			if err := os.Symlink("../real.json", p); err != nil {
				h.t.Fatal(err)
			}
			h.git("add", "-A")
			h.git("commit", "-q", "-m", "symlink")
		}},
		{"exempt", func(h *hookFx) {
			h.writeProjectRaw(projectJSON([]string{".ballast/**"}, helperSpec("test", "exit", "BALLAST_HELPER_CODE=1")))
			h.git("add", "-A")
			h.git("commit", "-q", "-m", "exempt")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, _ := pinFixture(t)
			tc.setup(h)
			rec, err := h.runHook("test")
			expectPinInfra(t, h, rec, err, "project_json_untracked", "")
			// Directly as well: the sentinel is the typed error.
			if _, err := h.materialize("test", "job-pin-direct"); !errors.Is(err, ErrProjectJSONUntracked) {
				t.Errorf("Materialize err = %v", err)
			}
		})
	}
}

func TestProjectJSONSkipWorktreeDiffersFromBlob(t *testing.T) {
	h, _, forged, marker := pinFixture(t)
	h.git("update-index", "--skip-worktree", ".ballast/project.json")
	h.writeProjectRaw(forged) // the work tree differs; the index keeps the honest blob
	rec, err := h.runHook("test")
	expectPinInfra(t, h, rec, err, "project_json_changed", marker)
}

func TestProjectJSONQuietRunExecutesThePinnedEntry(t *testing.T) {
	h, _, _, _ := pinFixture(t)
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomeFail || rec.ExitCode == nil || *rec.ExitCode != 1 {
		t.Fatalf("%+v %v", rec, err)
	}
}

// --- sources the agent can write (v1.5.2) --------------------------------

func TestInputsSourceSwappedBetweenLstatAndOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap func(t *testing.T, h *hookFx, path string)
	}{
		{"fifo", func(t *testing.T, h *hookFx, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			mkfifo(t, path)
		}},
		{"symlink to /dev/zero", func(t *testing.T, h *hookFx, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/dev/zero", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"another file", func(t *testing.T, h *hookFx, path string) {
			other := path + ".new"
			if err := os.WriteFile(other, []byte("evil\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(other, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := inputsRepo(t, "node_modules/**")
			swapped := false
			h.opt.FS = hookFS{onOpen: func(name string, flag int) {
				if !swapped && hasSuffix(name, "node_modules/pkg/index.js") {
					swapped = true
					tc.swap(t, h, name)
				}
			}}
			done := make(chan error, 1)
			go func() { _, err := h.materialize("test", "job-swap"); done <- err }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrInputsConflict) {
					t.Fatalf("err = %v", err)
				}
			case <-after10s():
				t.Fatal("Materialize hangs on a swapped source")
			}
			if !swapped {
				t.Fatal("the swap hook never ran")
			}
			noJobDir(t, h, "job-swap")
		})
	}
}

func TestInputsSourceGrowingFileStopsAtTheLimit(t *testing.T) {
	h := inputsRepo(t, "node_modules/**")
	defer func(b int64) { maxInputBytes = b }(maxInputBytes)
	maxInputBytes = 4096
	h.opt.FS = hookFS{wrapFile: func(name string, f sys.File) sys.File {
		if hasSuffix(name, "node_modules/pkg/index.js") {
			return &growingFile{File: f}
		}
		return f
	}}
	done := make(chan error, 1)
	go func() { _, err := h.materialize("test", "job-grow"); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrInputsTooLarge) {
			t.Fatalf("err = %v", err)
		}
	case <-after10s():
		t.Fatal("Materialize does not stop at the limit")
	}
	noJobDir(t, h, "job-grow")
}

func TestRealIndexNotARegularFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, h *hookFx, idx string)
	}{
		{"fifo", func(t *testing.T, h *hookFx, idx string) {
			if err := os.Remove(idx); err != nil {
				t.Fatal(err)
			}
			mkfifo(t, idx)
		}},
		{"symlink to /dev/zero", func(t *testing.T, h *hookFx, idx string) {
			if err := os.Remove(idx); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/dev/zero", idx); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, h *hookFx, idx string) {
			if err := os.Remove(idx); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(idx, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"larger than the limit", func(t *testing.T, h *hookFx, idx string) { maxIndexBytes = 16 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(m int64) { maxIndexBytes = m }(maxIndexBytes)
			h := newHookFx(t)
			h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
			tc.setup(t, h, filepath.Join(h.root, ".git", "index"))
			done := make(chan *model.VerifyRecord, 1)
			go func() {
				rec, _ := h.runHook("test")
				done <- rec
			}()
			select {
			case rec := <-done:
				if rec == nil || rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "materialize_failed") {
					t.Fatalf("record: %+v", rec)
				}
				if h.be.called("wrap") != 0 {
					t.Errorf("the command was wrapped")
				}
			case <-after10s():
				t.Fatal("the job hangs on a non-regular index")
			}
		})
	}
}

func TestMissingIndexIsAnEmptyIndex(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	if err := os.Remove(filepath.Join(h.root, ".git", "index")); err != nil {
		t.Fatal(err)
	}
	// The pinned project.json is then "untracked" (nothing is tracked).
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "project_json_untracked") {
		t.Fatalf("%+v %v", rec, err)
	}
}

// --- placement (v1.5.1) --------------------------------------------------

// symlinkRepo commits a symlink "lnk" -> target, marks it skip-worktree
// (`git add -u` will not refresh it) and replaces it in the work tree by a
// real directory holding an untracked file that inputs_untracked matches.
func symlinkRepo(t *testing.T, target string) *hookFx {
	t.Helper()
	h := newHookFx(t)
	spec := helperSpec("test", "exit", "BALLAST_HELPER_CODE=0")
	spec.InputsUntracked = []string{"lnk/**"}
	h.setProject(spec)
	if err := os.Symlink(target, filepath.Join(h.root, "lnk")); err != nil {
		t.Fatal(err)
	}
	h.git("add", "lnk")
	h.git("commit", "-q", "-m", "symlink")
	h.git("update-index", "--skip-worktree", "lnk")
	if err := os.Remove(filepath.Join(h.root, "lnk")); err != nil {
		t.Fatal(err)
	}
	h.write("lnk/x.txt", "payload\n")
	return h
}

func TestInputsThroughASymlinkOfTheTreeAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(h *hookFx) string // before h exists the target is not known: computed lazily
	}{
		{"absolute target", nil},
		{"relative target", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			target := outside
			if tc.name == "relative target" {
				target = "../../.."
			}
			h := symlinkRepo(t, target)
			_, err := h.materialize("test", "job-lnk")
			if !errors.Is(err, ErrInputsConflict) {
				t.Fatalf("err = %v", err)
			}
			if ents, _ := os.ReadDir(outside); len(ents) != 0 {
				t.Errorf("files were written outside the tree: %v", ents)
			}
			for _, p := range []string{filepath.Join(h.sb.BallastHome, "x.txt"), filepath.Join(h.sb.BallastHome, "work", "x.txt")} {
				if _, err := os.Lstat(p); err == nil {
					t.Errorf("%s was created", p)
				}
			}
			noJobDir(t, h, "job-lnk")
			// As a job: infra, never executed.
			rec, err := h.runHook("test")
			if err != nil || rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "inputs_path_conflict") {
				t.Errorf("job: %+v %v", rec, err)
			}
			if h.be.called("wrap") != 0 {
				t.Errorf("the command was wrapped")
			}
		})
	}
}

func TestInputsUnderATrackedFileAreRefused(t *testing.T) {
	h := newHookFx(t)
	spec := helperSpec("test", "exit", "BALLAST_HELPER_CODE=0")
	spec.InputsUntracked = []string{"blob/**"}
	h.setProject(spec)
	h.write("blob", "a tracked file\n")
	h.git("add", "blob")
	h.git("commit", "-q", "-m", "blob")
	h.git("update-index", "--skip-worktree", "blob")
	if err := os.Remove(filepath.Join(h.root, "blob")); err != nil {
		t.Fatal(err)
	}
	h.write("blob/sub.txt", "x\n")
	_, err := h.materialize("test", "job-blob")
	if !errors.Is(err, ErrInputsConflict) {
		t.Fatalf("err = %v", err)
	}
	noJobDir(t, h, "job-blob")
}

func TestInputsCaseInsensitiveCollision(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "PROBE")); err != nil {
		t.Skip("this file system is case-sensitive")
	}
	h := newHookFx(t)
	spec := helperSpec("test", "exit", "BALLAST_HELPER_CODE=0")
	spec.InputsUntracked = []string{"Foo"}
	h.setProject(spec)
	h.write("foo", "tracked\n")
	h.git("add", "foo")
	h.git("commit", "-q", "-m", "foo")
	h.git("update-index", "--skip-worktree", "foo")
	if err := os.Remove(filepath.Join(h.root, "foo")); err != nil {
		t.Fatal(err)
	}
	h.write("Foo", "untracked\n")
	_, err := h.materialize("test", "job-case")
	if !errors.Is(err, ErrInputsConflict) {
		t.Fatalf("err = %v", err)
	}
}

// --- path layout pre-check (v1.5.1) ---------------------------------------

// addIndexEntry puts an entry into the index without touching the work tree
// and marks it skip-worktree.
func (h *hookFx) addIndexEntry(mode, content, path string) {
	h.t.Helper()
	blob := strings.TrimSpace(h.gitStdin(content, "hash-object", "-w", "--stdin"))
	h.git("update-index", "--add", "--cacheinfo", mode+","+blob+","+path)
	h.git("update-index", "--skip-worktree", path)
}

func TestLayoutPrecheckSymlinkPrefix(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	outside := t.TempDir()
	h.addIndexEntry("120000", outside, "A")
	h.addIndexEntry("100644", "x\n", "a/x")
	rec, err := h.runHook("test")
	if err != nil || rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "tree_unsafe_path") {
		t.Fatalf("%+v %v", rec, err)
	}
	for _, v := range h.rg.verbs() {
		if v == "checkout-index" || v == "read-tree" {
			t.Errorf("git %s ran before the layout check rejected the tree: %v", v, h.rg.verbs())
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("something was written through the link: %v", ents)
	}
	if h.be.called("wrap") != 0 {
		t.Errorf("wrapped")
	}
}

func TestLayoutPrecheckAllowsPlainCaseCollisions(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	h.addIndexEntry("100644", "lower\n", "foo")
	h.addIndexEntry("100644", "upper\n", "FOO")
	tr, err := h.materialize("test", "job-collide")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	CleanupTree(h.opt, tr)
}

func TestCheckTreeLayoutTable(t *testing.T) {
	e := func(mode, p string) indexEntry { return indexEntry{mode, p} }
	for _, tc := range []struct {
		name string
		in   []indexEntry
		bad  bool
	}{
		{"plain", []indexEntry{e("100644", "a/b"), e("100755", "c")}, false},
		{"symlink alone", []indexEntry{e("120000", "lnk")}, false},
		{"symlink with unrelated sibling", []indexEntry{e("120000", "ab"), e("100644", "a/x")}, false},
		{"symlink exact prefix", []indexEntry{e("120000", "a"), e("100644", "a/x")}, true},
		{"symlink case prefix", []indexEntry{e("120000", "A"), e("100644", "a/x")}, true},
		{"symlink case prefix deep", []indexEntry{e("120000", "d/L"), e("100644", "D/l/x")}, true},
		{"file case collision", []indexEntry{e("100644", "foo"), e("100644", "FOO")}, false},
		{"symlink collides only with file", []indexEntry{e("120000", "foo"), e("100644", "FOO")}, false},
		{"dot git", []indexEntry{e("100644", "x/.git/config")}, true},
		{"dot GIT", []indexEntry{e("100644", ".GIT/x")}, true},
		{"dot git file", []indexEntry{e("100644", "a/.Git")}, true},
		{"dot", []indexEntry{e("100644", "a/./b")}, true},
		{"dotdot", []indexEntry{e("100644", "../b")}, true},
		{"empty component", []indexEntry{e("100644", "a//b")}, true},
		{"gitignore is fine", []indexEntry{e("100644", ".gitignore"), e("100644", ".github/x")}, false},
		{"unicode fold", []indexEntry{e("120000", "straße"), e("100644", "STRASSE/x")}, false}, // ß does not simple-fold to SS
		{"unicode simple fold", []indexEntry{e("120000", "ǆ"), e("100644", "ǅ/x")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkTreeLayout(tc.in); (err != nil) != tc.bad {
				t.Errorf("checkTreeLayout = %v, want bad=%v", err, tc.bad)
			}
		})
	}
}

func FuzzCheckTreeLayout(f *testing.F) {
	f.Add("120000\x00A\x00100644\x00a/x")
	f.Add("100644\x00a//b")
	f.Fuzz(func(t *testing.T, s string) {
		parts := strings.Split(s, "\x00")
		var es []indexEntry
		for i := 0; i+1 < len(parts); i += 2 {
			es = append(es, indexEntry{parts[i], parts[i+1]})
		}
		_ = checkTreeLayout(es)
		_, _ = parseIndexEntries([]byte(s))
	})
}

// A job in a linked worktree: ResolveGitDirs vouches for the (git dir,
// common dir) pair, Materialize uses them, the copy has no .git.
func TestLinkedWorktreeJob(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	realRoot, err := filepath.EvalSymlinks(h.sb.Root)
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(realRoot, "wt")
	h.git("worktree", "add", "-q", "-b", "wt-branch", wt)
	if err := os.WriteFile(filepath.Join(wt, "src", "main.txt"), []byte("edited in the linked worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := model.HookStartRequest{RequestID: h.opt.newJobID(), Cwd: wt, Names: []string{"test"}, Client: model.ClientClaude}
	started, err := StartRequest(h.opt, req)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := RunJob(h.opt, started[0].JobID)
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v %v", rec, err)
	}
	if rec.Tier != model.TierT1 || rec.Workspace != model.WorkspaceCopy || !rec.ValidPass() {
		t.Errorf("record: %+v", rec)
	}
	repo, err := h.opt.Repo.Locate(wt)
	if err != nil || repo.GitDir == repo.CommonDir {
		t.Fatalf("not a linked worktree: %+v %v", repo, err)
	}
	if rec.RepoInst != repo.Inst {
		t.Errorf("repo_inst %s != %s", rec.RepoInst, repo.Inst)
	}
	// The edit of the linked worktree is what was snapshotted.
	blob := h.git("cat-file", "blob", string(rec.SStart)+":src/main.txt")
	if blob != "edited in the linked worktree\n" {
		t.Errorf("S_start has %q", blob)
	}
}

// --- repo-local filter drivers (audit r1) ----------------------------------

// A filter driver in the repository's own config would run outside every
// sandbox during `git add -u` / `checkout-index`: the job fails closed,
// nothing runs, and the marker the driver would create does not appear.
func TestRepoLocalFilterDriverIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"clean", "filter.evil.clean"},
		{"smudge", "filter.evil.smudge"},
		{"process", "filter.evil.process"},
		{"dotted name", "filter.a.b.clean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookFx(t)
			marker := filepath.Join(h.sb.Root, "driver-ran")
			h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
			h.write(".gitattributes", "*.txt filter=evil\n")
			h.write("a.txt", "x\n")
			h.git("add", ".gitattributes", "a.txt")
			h.git("commit", "-q", "-m", "attrs")
			h.git("config", tc.key, "touch '"+marker+"'") // after the commit: the fixture's own git must not run it
			h.write("a.txt", "y\n")                       // tracked and dirty: `git add -u` would filter it
			_, err := h.materialize("test", "job-filter")
			if !errors.Is(err, ErrGitConfigUnsafe) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "touch") {
				t.Errorf("the driver command leaked into the error: %v", err)
			}
			noJobDir(t, h, "job-filter")
			if _, err := os.Stat(marker); err == nil {
				t.Errorf("the filter driver ran")
			}
			rec, err := h.runHook("test")
			if err != nil || rec.Outcome != model.OutcomeInfra || !strings.HasPrefix(rec.Error, "git_config_unsafe") || rec.ValidPass() {
				t.Fatalf("%+v %v", rec, err)
			}
			if h.be.called("wrap") != 0 {
				t.Errorf("the command was wrapped")
			}
		})
	}
}

func TestRepoLocalInnocentConfigIsAccepted(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	h.git("config", "filter.lfs.required", "true") // a driver name without a command
	h.git("config", "core.autocrlf", "false")
	if _, err := h.materialize("test", "job-ok"); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestIsFilterCommandKey(t *testing.T) {
	for k, want := range map[string]bool{
		"filter.x.clean": true, "filter.x.smudge": true, "filter.x.process": true, "filter.a.b.clean": true,
		"filter.x.required": false, "filter.clean": false, "core.fsmonitor": false, "user.clean": false,
	} {
		if got := isFilterCommandKey(k); got != want {
			t.Errorf("isFilterCommandKey(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestFindUnsafeConfigKey(t *testing.T) {
	scoped := func(parts ...string) string { return strings.Join(parts, "\x00") + "\x00" }
	for _, tc := range []struct {
		name, out string
		scoped    bool
		wantScope string
		wantKey   string
	}{
		{"scoped local clean", scoped("local", "filter.lfs.clean\ngit-lfs clean -- %f"), true, "local", "filter.lfs.clean"},
		{"scoped worktree process", scoped("worktree", "filter.x.process\ncmd"), true, "worktree", "filter.x.process"},
		{"scoped global is trusted", scoped("global", "filter.lfs.clean\ngit-lfs clean -- %f", "system", "filter.s.smudge\nx"), true, "", ""},
		{"scoped harmless local", scoped("local", "core.autocrlf\nfalse", "local", "filter.lfs.required\ntrue"), true, "", ""},
		{"scoped second entry", scoped("local", "core.bare\nfalse", "local", "Filter.Evil.Smudge\nx"), true, "local", "Filter.Evil.Smudge"},
		{"unscoped clean", "core.bare\nfalse\x00filter.a.clean\nx\x00", false, "local", "filter.a.clean"},
		{"unscoped include", "include.path\n../evil\x00", false, "local", "include.path"},
		{"unscoped includeIf", "includeif.gitdir:/x/.path\nf\x00", false, "local", "includeif.gitdir:/x/.path"},
		{"unscoped harmless", "core.bare\nfalse\x00user.name\nfilter\x00", false, "", ""},
	} {
		scope, key := findUnsafeConfigKey(tc.out, tc.scoped)
		if scope != tc.wantScope || key != tc.wantKey {
			t.Errorf("%s: (%q, %q), want (%q, %q)", tc.name, scope, key, tc.wantScope, tc.wantKey)
		}
	}
}

// git before 2.26 has no --show-scope: the check falls back to listing the
// repository's own files and still refuses a driver.
func TestCheckLocalGitConfigOldGitFallback(t *testing.T) {
	run := func(local string) error {
		git := func(index, workTree, dir string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "--show-scope"):
				return nil, errors.New("error: unknown option `show-scope'")
			case strings.Contains(joined, "--local"):
				return []byte(local), nil
			case strings.Contains(joined, "--worktree"):
				return nil, errors.New("not enabled")
			}
			return nil, errors.New("unexpected " + joined)
		}
		return checkLocalGitConfig(git, "idx", "/top")
	}
	if err := run("core.bare\nfalse\x00"); err != nil {
		t.Errorf("harmless config refused: %v", err)
	}
	if err := run("filter.x.clean\nevil\x00"); err == nil || strings.Contains(err.Error(), "evil") {
		t.Errorf("driver not refused (or the command leaked): %v", err)
	}
}
