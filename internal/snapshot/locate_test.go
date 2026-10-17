package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func locate(t *testing.T, cwd string) (*model.RepoInfo, error) {
	t.Helper()
	return (&Locator{FS: sys.OSFS{}}).Locate(cwd)
}

func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func remoteHash(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:8])
}

func TestLocateMatchesGit(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	sub := filepath.Join(f.dir, "src", "sub")
	for name, cwd := range map[string]string{"root": f.dir, "subdir": sub, "deep": filepath.Join(sub, "inner")} {
		t.Run(name, func(t *testing.T) {
			info, err := locate(t, cwd)
			if err != nil {
				t.Fatal(err)
			}
			top := realpath(t, strings.TrimSpace(f.gitIn(cwd, nil, "rev-parse", "--show-toplevel")))
			gitDir := realpath(t, strings.TrimSpace(f.gitIn(cwd, nil, "rev-parse", "--absolute-git-dir")))
			common := realpath(t, strings.TrimSpace(f.gitIn(cwd, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")))
			if info.Toplevel != top || info.GitDir != gitDir || info.CommonDir != common {
				t.Errorf("got top=%s git=%s common=%s; git says %s %s %s", info.Toplevel, info.GitDir, info.CommonDir, top, gitDir, common)
			}
			if want := filepath.Join(gitDir, "index"); info.IndexPath != want {
				t.Errorf("IndexPath = %s, want %s", info.IndexPath, want)
			}
			if info.Inst != RepoInst(top, common) || len(info.Inst) != 16 {
				t.Errorf("Inst = %q, want %q", info.Inst, RepoInst(top, common))
			}
			if info.HeadRef != "refs/heads/main" || info.ObjectFormat != "sha1" || info.RemoteHash != "" {
				t.Errorf("HeadRef=%q ObjectFormat=%q RemoteHash=%q", info.HeadRef, info.ObjectFormat, info.RemoteHash)
			}
		})
	}
}

func TestLocateResolvesSymlinkedCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	f := newFixture(t)
	baseRepo(f)
	link := filepath.Join(f.sb.Root, "alias")
	if err := os.Symlink(f.dir, link); err != nil {
		t.Fatal(err)
	}
	info, err := locate(t, filepath.Join(link, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Toplevel != f.dir {
		t.Errorf("Toplevel = %s, want %s", info.Toplevel, f.dir)
	}
}

func TestLocateLinkedWorktree(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	wt := filepath.Join(f.sb.Root, "wt")
	f.git("worktree", "add", "-q", "-b", "feature", wt)
	wt = realpath(t, wt)
	main, err := locate(t, f.dir)
	if err != nil {
		t.Fatal(err)
	}
	lin, err := locate(t, filepath.Join(wt, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if lin.Toplevel != wt {
		t.Errorf("Toplevel = %s, want %s", lin.Toplevel, wt)
	}
	if lin.CommonDir != main.GitDir {
		t.Errorf("CommonDir = %s, want %s", lin.CommonDir, main.GitDir)
	}
	if want := filepath.Join(main.GitDir, "worktrees"); !strings.HasPrefix(lin.GitDir, want) {
		t.Errorf("GitDir = %s, want below %s", lin.GitDir, want)
	}
	if lin.IndexPath != filepath.Join(lin.GitDir, "index") {
		t.Errorf("IndexPath = %s", lin.IndexPath)
	}
	if lin.Inst == main.Inst {
		t.Error("linked worktree must have its own repo_inst")
	}
	if lin.HeadRef != "refs/heads/feature" {
		t.Errorf("HeadRef = %q", lin.HeadRef)
	}
	if b, tri := Branch(sys.OSFS{}, lin); b != "feature" || tri != model.TriTrue {
		t.Errorf("Branch = %q %v", b, tri)
	}
	if b, tri := Branch(sys.OSFS{}, main); b != "main" || tri != model.TriTrue {
		t.Errorf("Branch(main) = %q %v", b, tri)
	}
}

func TestLocateSeparateGitDir(t *testing.T) {
	f := newFixture(t)
	work := filepath.Join(f.sb.Root, "sep-work")
	gitDir := filepath.Join(f.sb.Root, "sep.git")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	f.gitIn(work, nil, "init", "-q", "--separate-git-dir", gitDir, work)
	info, err := locate(t, work)
	if err != nil {
		t.Fatal(err)
	}
	if info.Toplevel != realpath(t, work) || info.GitDir != realpath(t, gitDir) || info.CommonDir != info.GitDir {
		t.Errorf("%+v", info)
	}
}

func TestLocateNotRepo(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	outside := filepath.Join(f.sb.Root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, cwd := range map[string]string{
		"outside":      outside,
		"missing":      filepath.Join(outside, "nope"),
		"inside .git":  filepath.Join(f.dir, ".git", "refs"),
		"the .git dir": filepath.Join(f.dir, ".git"),
		"hooks subdir": filepath.Join(f.dir, ".git", "hooks"),
		"bare filesys": filepath.VolumeName(outside) + string(filepath.Separator), // "\\" alone is not absolute on Windows
	} {
		t.Run(name, func(t *testing.T) {
			_, err := locate(t, cwd)
			if !errors.Is(err, model.ErrNotRepo) {
				t.Fatalf("err = %v, want ErrNotRepo", err)
			}
		})
	}
	if _, err := locate(t, ""); err == nil || errors.Is(err, model.ErrNotRepo) {
		t.Errorf("empty cwd: %v", err)
	}
	if _, err := locate(t, "relative/dir"); err == nil {
		t.Error("relative cwd must be rejected")
	}
}

func TestLocateIgnoresGitEnvironment(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	other := filepath.Join(f.sb.Root, "elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, "fake-index"))
	t.Setenv("GIT_DIR", filepath.Join(other, "fake.git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_COMMON_DIR", other)
	info, err := locate(t, f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.IndexPath != filepath.Join(f.dir, ".git", "index") || info.Toplevel != f.dir {
		t.Errorf("environment leaked into Locate: %+v", info)
	}
}

func TestLocateRemoteHash(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{"no remote", "", ""},
		{"origin", "[remote \"origin\"]\n\turl = https://example.invalid/a/b.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n", remoteHash("https://example.invalid/a/b.git")},
		{"origin after other remote", "[remote \"upstream\"]\n\turl = https://example.invalid/up.git\n[remote \"origin\"]\n\turl = git@example.invalid:me/me.git\n", remoteHash("git@example.invalid:me/me.git")},
		{"only upstream", "[remote \"upstream\"]\n\turl = https://example.invalid/up.git\n", ""},
		{"first url wins", "[remote \"origin\"]\n\turl = https://one.invalid/\n\turl = https://two.invalid/\n[remote \"origin\"]\n\turl = https://three.invalid/\n", remoteHash("https://one.invalid/")},
		{"value trimmed only", "[remote \"origin\"]\n\turl =   https://example.invalid/x   \t\n", remoteHash("https://example.invalid/x")},
		{"crlf config", "[remote \"origin\"]\r\n\turl = https://example.invalid/crlf\r\n", remoteHash("https://example.invalid/crlf")},
		{"section name case", "[REMOTE \"origin\"]\n\tURL = https://example.invalid/case\n", remoteHash("https://example.invalid/case")},
		{"subsection case sensitive", "[remote \"Origin\"]\n\turl = https://example.invalid/Origin\n", ""},
		{"url in other section", "[core]\n\turl = https://example.invalid/core\n[remote \"origin\"]\n\tpushurl = https://example.invalid/push\n", ""},
		{"empty url", "[remote \"origin\"]\n\turl =\n", ""},
		{"comments", "# [remote \"origin\"]\n; url = nope\n[remote \"origin\"] # c\n\turl = https://example.invalid/c\n", remoteHash("https://example.invalid/c")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			cfg := filepath.Join(f.dir, ".git", "config")
			old, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, append(old, []byte(tc.config)...), 0o644); err != nil {
				t.Fatal(err)
			}
			info, err := locate(t, f.dir)
			if err != nil {
				t.Fatal(err)
			}
			if info.RemoteHash != tc.want {
				t.Errorf("RemoteHash = %q, want %q", info.RemoteHash, tc.want)
			}
		})
	}
}

func TestLocateRealRemote(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "add", "origin", "https://example.invalid/real.git")
	info, err := locate(t, f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.RemoteHash != remoteHash("https://example.invalid/real.git") {
		t.Errorf("RemoteHash = %q", info.RemoteHash)
	}
}

func TestLocateObjectFormat(t *testing.T) {
	f := newFixture(t)
	cfg := filepath.Join(f.dir, ".git", "config")
	old, _ := os.ReadFile(cfg)
	if err := os.WriteFile(cfg, append(old, []byte("[extensions]\n\tobjectFormat = SHA256\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := locate(t, f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.ObjectFormat != "sha256" {
		t.Errorf("ObjectFormat = %q", info.ObjectFormat)
	}
}

func TestLocateRealSHA256Repo(t *testing.T) {
	requireGit(t)
	sb, env := sandboxEnv(t)
	dir := filepath.Join(realpath(t, sb.Root), "repo256")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, sb: sb, env: env, dir: dir}
	if out, err := f.gitErr(dir, nil, "init", "-q", "--object-format=sha256", "-b", "main"); err != nil {
		t.Skipf("git cannot create sha256 repositories: %v %s", err, out)
	}
	f.write("a.txt", "hello\n")
	f.git("add", "a.txt")
	f.relocate()
	if f.repo.ObjectFormat != "sha256" {
		t.Fatalf("ObjectFormat = %q", f.repo.ObjectFormat)
	}
	// Native C cannot be computed: ErrFallback without AllowFallback,
	// and the git fallback gives git's own answer.
	form := model.CommitForm{Kind: model.CommitPlain}
	if _, err := TreeC(f.opts(false), f.repo, form, nil); !errors.Is(err, model.ErrFallback) {
		t.Fatalf("err = %v, want ErrFallback", err)
	}
	got, err := TreeC(f.opts(true), f.repo, form, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := f.reference(false, nil, nil)
	if got != want || len(got) != 64 {
		t.Errorf("sha256 repo: %s", describe(got, want))
	}
	if need, why := NeedsFallback(sys.OSFS{}, f.repo); !need || !strings.Contains(why, "sha256") {
		t.Errorf("NeedsFallback = %v %q", need, why)
	}
}

func TestBranch(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	head := filepath.Join(f.repo.GitDir, "HEAD")
	set := func(content string) {
		t.Helper()
		if err := os.WriteFile(head, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name, head string
		want       string
		tri        model.Tri
	}{
		{"main", "ref: refs/heads/main\n", "main", model.TriTrue},
		{"slash", "ref: refs/heads/feature/x-1\n", "feature/x-1", model.TriTrue},
		{"unborn", "ref: refs/heads/brand-new\n", "brand-new", model.TriTrue},
		{"no newline", "ref: refs/heads/main", "main", model.TriTrue},
		{"detached sha1", strings.Repeat("a", 40) + "\n", "", model.TriFalse},
		{"detached sha256", strings.Repeat("b", 64) + "\n", "", model.TriFalse},
		{"other symbolic ref", "ref: refs/remotes/origin/main\n", "", model.TriUnknown},
		{"reftable stub", "ref: refs/heads/.invalid\n", "", model.TriUnknown},
		{"garbage", "hello\n", "", model.TriUnknown},
		{"empty", "", "", model.TriUnknown},
		{"empty ref", "ref: \n", "", model.TriUnknown},
		{"bare heads prefix", "ref: refs/heads/\n", "", model.TriUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set(tc.head)
			got, tri := Branch(sys.OSFS{}, f.repo)
			if got != tc.want || tri != tc.tri {
				t.Errorf("Branch = %q %v, want %q %v", got, tri, tc.want, tc.tri)
			}
			// The Locator method agrees.
			got, tri = (&Locator{FS: sys.OSFS{}}).Branch(f.repo)
			if got != tc.want || tri != tc.tri {
				t.Errorf("Locator.Branch = %q %v", got, tri)
			}
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		set("ref: refs/heads/main\n")
		ffs := sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"ReadFile": "HEAD"}, Err: errors.New("boom")}
		if b, tri := Branch(ffs, f.repo); b != "" || tri != model.TriUnknown {
			t.Errorf("Branch = %q %v", b, tri)
		}
	})
	t.Run("missing HEAD", func(t *testing.T) {
		if err := os.Remove(head); err != nil {
			t.Fatal(err)
		}
		if b, tri := Branch(sys.OSFS{}, f.repo); b != "" || tri != model.TriUnknown {
			t.Errorf("Branch = %q %v", b, tri)
		}
	})
	t.Run("nil repo", func(t *testing.T) {
		if b, tri := Branch(nil, nil); b != "" || tri != model.TriUnknown {
			t.Errorf("Branch(nil) = %q %v", b, tri)
		}
	})
}

func TestLocateConfigReadFailureIsNotFatal(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "add", "origin", "https://example.invalid/x.git")
	ffs := sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"ReadFile": filepath.Join(".git", "config")}, Err: errors.New("boom")}
	info, err := (&Locator{FS: ffs}).Locate(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.RemoteHash != "" || info.ObjectFormat != "sha1" {
		t.Errorf("%+v", info)
	}
}

func TestLocateDetachedAndUnborn(t *testing.T) {
	f := newFixture(t)
	info, err := locate(t, f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.HeadRef != "refs/heads/main" {
		t.Errorf("unborn HeadRef = %q", info.HeadRef)
	}
	baseRepo(f)
	f.git("checkout", "-q", "--detach")
	info, err = locate(t, f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.HeadRef != "" {
		t.Errorf("detached HeadRef = %q", info.HeadRef)
	}
}

func TestLocateNilFSDefaultsToOS(t *testing.T) {
	f := newFixture(t)
	if _, err := (&Locator{}).Locate(f.dir); err != nil {
		t.Fatal(err)
	}
}
