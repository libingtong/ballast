package snapshot

// This is the ONLY hot-path file allowed to import os/exec
// (docs/architecture.md §9). Every call is bounded by Options.Deadline.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// GitRunner runs git with a context (deadline) in dir with extra env.
type GitRunner interface {
	Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)
}

// ExecGit runs the git binary found on PATH.
type ExecGit struct{}

// Run implements GitRunner. env is the complete child environment. When ctx
// ends, the whole process group of the child is killed (git may spawn
// helpers that would otherwise outlive it and keep the pipes open).
func (ExecGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = newProcAttr()
	cmd.Cancel = func() error { return killProcessTree(cmd.Process) }
	cmd.WaitDelay = 200 * time.Millisecond
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 512 {
			msg = msg[:512]
		}
		return out, fmt.Errorf("git %s: %w: %s", firstArg(args), err, msg)
	}
	return out, nil
}

func firstArg(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			return a
		}
	}
	return ""
}

// inheritedGitVars are removed from the child environment: `verify --check`
// runs inside git's own pre-commit environment, where they point at the
// commit in progress rather than at the repository being snapshotted.
var inheritedGitVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_PREFIX",
}

// gitEnv builds the child environment: inherited repository-pointing
// variables removed, then GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE (the
// temporary copy) set explicitly.
func gitEnv(base []string, repo *model.RepoInfo, indexCopy string, windows bool) []string {
	out := make([]string, 0, len(base)+4)
loop:
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if windows {
			name = strings.ToUpper(name)
		}
		for _, bad := range inheritedGitVars {
			if name == bad {
				continue loop
			}
		}
		out = append(out, kv)
	}
	return append(out,
		"GIT_DIR="+repo.GitDir,
		"GIT_WORK_TREE="+repo.Toplevel,
		"GIT_INDEX_FILE="+indexCopy,
		"GIT_OPTIONAL_LOCKS=0",
	)
}

// tempRoot is where the temporary index lives: never inside the user's
// .git directory.
func tempRoot(env sys.Env) string {
	keys := []string{"TMPDIR"}
	if env.GOOS() == "windows" {
		keys = []string{"TMP", "TEMP"}
	}
	for _, k := range keys {
		if v, ok := env.Getenv(k); ok && v != "" {
			return v
		}
	}
	return os.TempDir()
}

// fallbackTree implements the v0.3 algorithm under GIT_INDEX_FILE=<copy>:
// [git add -u [-- paths]] → git rm --cached -r -q --ignore-unmatch --
// ':(glob)<exempt>'... → git write-tree. refresh selects add -u; paths
// restricts it. Deadline exceeded ⇒ model.ErrDeadline.
//
// paths are repo-root relative pathspecs. `git rm` runs with -f: without it
// git refuses to drop an exempt file whose staged content differs from both
// HEAD and the work tree, which would make the snapshot fail for no reason.
func fallbackTree(opt Options, repo *model.RepoInfo, refresh bool, paths []string, f *Filter) (model.TreeID, error) {
	opt = opt.withDefaults()
	ctx := context.Background()
	if !opt.Deadline.IsZero() {
		left := opt.Deadline.Sub(opt.Clock.Now())
		if left <= 0 {
			return "", errDeadline()
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, left)
		defer cancel()
	}
	tmpDir, indexCopy, err := copyIndex(opt, repo)
	if tmpDir != "" {
		defer func() { _ = opt.FS.RemoveAll(tmpDir) }()
	}
	if err != nil {
		return "", err
	}
	env := gitEnv(opt.Env.Environ(), repo, indexCopy, opt.Env.GOOS() == "windows")
	run := func(args ...string) ([]byte, error) {
		args = append([]string{"-c", "core.fsmonitor=false"}, args...)
		out, err := opt.Git.Run(ctx, repo.Toplevel, env, args...)
		if err != nil && ctx.Err() != nil {
			return nil, errDeadline()
		}
		if err == nil && ctx.Err() != nil {
			return nil, errDeadline()
		}
		return out, err
	}
	if refresh {
		args := []string{"add", "-u"}
		if len(paths) > 0 {
			args = append(append(args, "--"), paths...)
		}
		if _, err := run(args...); err != nil {
			return "", err
		}
	}
	if !f.empty() {
		args := append([]string{"rm", "--cached", "-r", "-q", "-f", "--ignore-unmatch", "--"}, f.pathspecArgs()...)
		if _, err := run(args...); err != nil {
			return "", err
		}
	}
	out, err := run("write-tree")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if !isHexID(id) {
		return "", fmt.Errorf("snapshot: git write-tree printed %q", id)
	}
	return model.TreeID(id), nil
}

// copyIndex copies the real index into a private temporary directory (not
// under .git) and returns that directory and the copy's path. The copy keeps
// the original modification time: git's racy-timestamp logic compares entry
// times with the index file time, and a fresher copy would hide racily clean
// entries.
func copyIndex(opt Options, repo *model.RepoInfo) (tmpDir, indexCopy string, err error) {
	tmpDir = filepath.Join(tempRoot(opt.Env), "ballast-snap-"+sys.RandomID{}.NewID(8))
	if err := opt.FS.MkdirAll(tmpDir, 0o700); err != nil {
		return "", "", err
	}
	indexCopy = filepath.Join(tmpDir, "index")
	fi, err := opt.FS.Stat(repo.IndexPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return tmpDir, indexCopy, nil // no index: git treats the missing copy as empty
		}
		return tmpDir, "", err
	}
	data, err := opt.FS.ReadFile(repo.IndexPath)
	if err != nil {
		return tmpDir, "", err
	}
	f, err := opt.FS.OpenFile(indexCopy, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return tmpDir, "", err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return tmpDir, "", werr
	}
	_ = os.Chtimes(indexCopy, fi.ModTime(), fi.ModTime())
	return tmpDir, indexCopy, nil
}
