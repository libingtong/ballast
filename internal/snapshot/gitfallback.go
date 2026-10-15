package snapshot

// This is the ONLY hot-path file allowed to import os/exec
// (docs/architecture.md §9). Every call is bounded by Options.Deadline.

import (
	"context"
	"os/exec"

	"github.com/libingtong/ballast/internal/model"
)

// GitRunner runs git with a context (deadline) in dir with extra env.
type GitRunner interface {
	Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)
}

// ExecGit runs the git binary found on PATH.
type ExecGit struct{}

// Run implements GitRunner.
func (ExecGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.Output()
}

// fallbackTree implements the v0.3 algorithm under GIT_INDEX_FILE=<copy>:
// [git add -u [-- paths]] → git rm --cached -r -q --ignore-unmatch --
// ':(glob)<exempt>'... → git write-tree. refresh selects add -u; paths
// restricts it. Deadline exceeded ⇒ model.ErrDeadline.
func fallbackTree(opt Options, repo *model.RepoInfo, refresh bool, paths []string, f *Filter) (model.TreeID, error) {
	panic("unimplemented: snapshot.fallbackTree")
}
