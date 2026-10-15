package snapshot

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

// requireGit skips (locally) or fails (in CI) when git is missing.
func requireGit(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("git is required in CI: %v", err)
		}
		t.Skip("git not on PATH")
	}
}

// sandboxEnv is the isolated environment of a fixture: the testenv sandbox
// plus PATH so that git can be found.
func sandboxEnv(t testing.TB) (*testenv.Sandbox, sys.MapEnv) {
	t.Helper()
	sb := testenv.New(t)
	vars := map[string]string{}
	for k, v := range sb.Vars {
		vars[k] = v
	}
	for _, k := range []string{"PATH", "SYSTEMROOT", "TMP", "TEMP", "ComSpec"} {
		if v, ok := os.LookupEnv(k); ok {
			vars[k] = v
		}
	}
	vars["TMPDIR"] = filepath.Join(sb.Root, "tmp")
	if err := os.MkdirAll(vars["TMPDIR"], 0o700); err != nil {
		t.Fatal(err)
	}
	return sb, sys.MapEnv{Vars: vars}
}

// fixture is a throw-away git repository under a testenv sandbox.
type fixture struct {
	t    testing.TB
	sb   *testenv.Sandbox
	env  sys.MapEnv
	dir  string // work tree root (realpath)
	repo *model.RepoInfo
}

type fixtureOpt func(args *[]string)

func withObjectFormat(f string) fixtureOpt {
	return func(a *[]string) { *a = append(*a, "--object-format="+f) }
}

func newFixture(t testing.TB, opts ...fixtureOpt) *fixture {
	t.Helper()
	requireGit(t)
	sb, env := sandboxEnv(t)
	root, err := filepath.EvalSymlinks(sb.Root)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, sb: sb, env: env, dir: filepath.Join(root, "repo")}
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"init", "-q", "-b", "main"}
	for _, o := range opts {
		o(&args)
	}
	f.git(args...)
	f.git("config", "user.email", "t@example.invalid")
	f.git("config", "user.name", "t")
	f.git("config", "core.autocrlf", "false")
	f.git("config", "core.fsmonitor", "false")
	f.relocate()
	return f
}

// relocate refreshes f.repo with Locate.
func (f *fixture) relocate() {
	f.t.Helper()
	repo, err := (&Locator{FS: sys.OSFS{}}).Locate(f.dir)
	if err != nil {
		f.t.Fatalf("Locate(%s): %v", f.dir, err)
	}
	f.repo = repo
}

func (f *fixture) gitIn(dir string, extraEnv []string, args ...string) string {
	f.t.Helper()
	out, err := f.gitErr(dir, extraEnv, args...)
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (f *fixture) gitErr(dir string, extraEnv []string, args ...string) (string, error) {
	f.t.Helper()
	return f.gitStdin(dir, extraEnv, "", args...)
}

func (f *fixture) gitStdin(dir string, extraEnv []string, stdin string, args ...string) (string, error) {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Env = append(f.env.Environ(), extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out) + stderr.String(), err
	}
	return string(out), nil
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	return f.gitIn(f.dir, nil, args...)
}

func (f *fixture) path(rel string) string { return filepath.Join(f.dir, filepath.FromSlash(rel)) }

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) chmod(rel string, mode os.FileMode) {
	f.t.Helper()
	if err := os.Chmod(f.path(rel), mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) remove(rel string) {
	f.t.Helper()
	if err := os.RemoveAll(f.path(rel)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) symlink(target, rel string) {
	f.t.Helper()
	if runtime.GOOS == "windows" {
		f.t.Skip("symlinks need privileges on windows")
	}
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatal(err)
	}
}

// commitAll stages everything and commits, then refreshes the index.
func (f *fixture) commitAll(msg string) {
	f.t.Helper()
	f.git("add", "-A")
	f.git("commit", "-q", "-m", msg, "--allow-empty")
	f.settle()
}

// settle refreshes the index so entries carry real stat data (smudged racy
// entries get repaired).
func (f *fixture) settle() {
	f.t.Helper()
	f.git("update-index", "--refresh", "-q")
}

// opts returns Options wired to the sandbox. AllowFallback is off unless the
// test asks for it.
func (f *fixture) opts(allowFallback bool) Options {
	return Options{FS: sys.OSFS{}, Clock: sys.RealClock{}, Env: f.env, AllowFallback: allowFallback}
}

// reference computes the expected tree with real git on a COPY of the
// index: [add -u [-- paths]] ; rm --cached -f exempt ; write-tree.
func (f *fixture) reference(refresh bool, paths []string, exempt []string) model.TreeID {
	f.t.Helper()
	id, err := f.referenceErr(refresh, paths, exempt)
	if err != nil {
		f.t.Fatalf("reference: %v", err)
	}
	return id
}

// referenceErr is reference, but reports a git failure instead of failing
// the test (git itself cannot snapshot some states, e.g. unmerged entries).
func (f *fixture) referenceErr(refresh bool, paths []string, exempt []string) (model.TreeID, error) {
	f.t.Helper()
	tmp := f.t.TempDir()
	cp := filepath.Join(tmp, "index")
	if data, err := os.ReadFile(f.repo.IndexPath); err == nil {
		fi, _ := os.Stat(f.repo.IndexPath)
		if err := os.WriteFile(cp, data, 0o600); err != nil {
			f.t.Fatal(err)
		}
		_ = os.Chtimes(cp, fi.ModTime(), fi.ModTime())
	}
	ex := []string{"GIT_INDEX_FILE=" + cp, "GIT_OPTIONAL_LOCKS=0"}
	if refresh {
		args := []string{"add", "-u"}
		if len(paths) > 0 {
			args = append(append(args, "--"), paths...)
		}
		if out, err := f.gitErr(f.dir, ex, args...); err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	if len(exempt) > 0 {
		args := []string{"rm", "--cached", "-r", "-q", "-f", "--ignore-unmatch", "--"}
		for _, g := range exempt {
			args = append(args, ":(glob)"+g)
		}
		if out, err := f.gitErr(f.dir, ex, args...); err != nil {
			return "", fmt.Errorf("git rm: %v: %s", err, out)
		}
	}
	out, err := f.gitErr(f.dir, ex, "write-tree")
	if err != nil {
		return "", fmt.Errorf("git write-tree: %v: %s", err, out)
	}
	return model.TreeID(strings.TrimSpace(out)), nil
}

func mustFilter(t testing.TB, globs ...string) *Filter {
	t.Helper()
	fl, err := CompileExempt(globs)
	if err != nil {
		t.Fatal(err)
	}
	return fl
}

// ---- hand-made index files -------------------------------------------------

type bentry struct {
	path          string
	mode          uint32
	sha           [20]byte
	ctime, mtime  tspec
	dev, ino      uint32
	size          uint32
	flags         uint16 // stage / assume-valid bits (name length is added)
	ext           uint16 // extended flags (v3); sets the extended bit when nonzero
	forceExtended bool
	uid, gid      uint32
}

func encodeVarint(v uint64) []byte {
	var buf [16]byte
	pos := len(buf) - 1
	buf[pos] = byte(v & 127)
	for v >>= 7; v != 0; v >>= 7 {
		v--
		pos--
		buf[pos] = 128 | byte(v&127)
	}
	return buf[pos:]
}

// buildIndex serialises entries as a git index of the given version, with
// optional extensions (signature, payload). Entries must be sorted.
func buildIndex(version int, ents []bentry, exts ...[2]string) []byte {
	var b bytes.Buffer
	b.WriteString("DIRC")
	binary.Write(&b, binary.BigEndian, uint32(version))
	binary.Write(&b, binary.BigEndian, uint32(len(ents)))
	prev := ""
	for _, e := range ents {
		start := b.Len()
		for _, v := range []uint32{e.ctime.sec, e.ctime.nsec, e.mtime.sec, e.mtime.nsec, e.dev, e.ino, e.mode, e.uid, e.gid, e.size} {
			binary.Write(&b, binary.BigEndian, v)
		}
		b.Write(e.sha[:])
		flags := e.flags &^ 0xfff
		n := len(e.path)
		if n > 0xfff {
			n = 0xfff
		}
		flags |= uint16(n)
		extended := e.ext != 0 || e.forceExtended
		if extended {
			flags |= flagExtended
		}
		binary.Write(&b, binary.BigEndian, flags)
		if extended {
			binary.Write(&b, binary.BigEndian, e.ext)
		}
		if version == 4 {
			common := 0
			for common < len(prev) && common < len(e.path) && prev[common] == e.path[common] {
				common++
			}
			b.Write(encodeVarint(uint64(len(prev) - common)))
			b.WriteString(e.path[common:])
			b.WriteByte(0)
			prev = e.path
			continue
		}
		b.WriteString(e.path)
		size := b.Len() - start
		pad := (size+8)&^7 - size
		b.Write(make([]byte, pad))
	}
	for _, x := range exts {
		b.WriteString(x[0])
		binary.Write(&b, binary.BigEndian, uint32(len(x[1])))
		b.WriteString(x[1])
	}
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	return b.Bytes()
}

func blobSHA(content string) [20]byte { return hashBlob([]byte(content)) }

// concurrentDo runs fn n times in parallel and returns once all are done.
func concurrentDo(n int, fn func(i int)) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fn(i)
		}(i)
	}
	wg.Wait()
}

func describe(a, b model.TreeID) string { return fmt.Sprintf("native=%s git=%s", a, b) }

var _ = context.Background
