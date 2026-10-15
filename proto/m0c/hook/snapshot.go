//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/libingtong/ballast/proto/m0c/nativetree"
)

// 快照算法 v0.3(B3):复制真实索引 -> GIT_INDEX_FILE 下 [add -u] -> rm --cached exempt -> write-tree。
// hook 内的实现相对计划字面做了两处优化(bench 的 snapshot 模式同时测字面版本作对照):
//  1. 先在 cwd 向上找 .git 目录,直接取 .git/index,找不到(如 worktree 的 .git 文件)才回退 `git rev-parse --git-path index`;
//  2. 用进程内复制代替外部 `cp`。

type snapSteps struct {
	Locate, Copy, AddU, Rm, WriteTree, Native time.Duration
}

func findIndex(cwd string) (indexPath, toplevel string, ok bool) {
	dir := cwd
	for {
		gi := filepath.Join(dir, ".git")
		if fi, err := os.Stat(gi); err == nil {
			if fi.IsDir() {
				return filepath.Join(gi, "index"), dir, true
			}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
	// 回退:git rev-parse
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel", "--git-path", "index").Output()
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", "", false
	}
	idx := lines[1]
	if !filepath.IsAbs(idx) {
		idx = filepath.Join(cwd, idx)
	}
	return idx, lines[0], true
}

func exemptPathspecs(exempt []string) []string {
	ps := make([]string, 0, len(exempt))
	for _, e := range exempt {
		ps = append(ps, ":(glob)"+e)
	}
	return ps
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func gitIn(dir, idx string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	env := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_INDEX_FILE=") {
			env = append(env, e)
		}
	}
	env = append(env, "GIT_INDEX_FILE="+idx, "GIT_OPTIONAL_LOCKS=0")
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// snapshot 返回过滤后的 tree id。addU=true 为 S(及 commit -a 的 C),false 为普通 C。
func snapshot(cwd string, exempt []string, addU bool) (tree string, steps snapSteps, err error) {
	t := time.Now()
	idx, top, ok := findIndex(cwd)
	steps.Locate = time.Since(t)
	if !ok {
		return "", steps, fmt.Errorf("not a git repo")
	}
	// 实验开关:普通 commit 的 C 不需要读工作区,可直接在进程内解析索引并算 tree id。
	if !addU && os.Getenv("BALLAST_SNAPSHOT_MODE") == "native" {
		t = time.Now()
		tree, err = nativetree.TreeID(idx, exempt)
		steps.Native = time.Since(t)
		if err == nil {
			return tree, steps, nil
		}
		// ErrFallback 或读取失败:退回 git 路径
	}
	t = time.Now()
	tmp := filepath.Join(filepath.Dir(idx), fmt.Sprintf("ballast-snap-%d", os.Getpid()))
	if err = copyFile(tmp, idx); err != nil {
		return "", steps, err
	}
	defer os.Remove(tmp)
	steps.Copy = time.Since(t)

	if addU {
		t = time.Now()
		if _, err = gitIn(top, tmp, "add", "-u"); err != nil {
			return "", steps, err
		}
		steps.AddU = time.Since(t)
	}
	if len(exempt) > 0 {
		t = time.Now()
		args := append([]string{"rm", "--cached", "-r", "-q", "--ignore-unmatch", "--"}, exemptPathspecs(exempt)...)
		if _, err = gitIn(top, tmp, args...); err != nil {
			return "", steps, err
		}
		steps.Rm = time.Since(t)
	}
	t = time.Now()
	tree, err = gitIn(top, tmp, "write-tree")
	steps.WriteTree = time.Since(t)
	return tree, steps, err
}
