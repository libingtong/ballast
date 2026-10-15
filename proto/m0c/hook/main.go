//go:build !windows

// Command hook 是 M0c 的最小完整 hook 原型(M1 前删除)。
//
// 每次进程执行完整流程:
//
//	读 stdin JSON -> 加载 12 条规则(gob 缓存,以规则文件 stat 为键)
//	-> mvdan.cc/sh 解析 Bash -> 求值 -> 状态文件 flock(<=20ms) 读改写 + 原子 rename
//	-> 追加审计 JSONL(单次 write) -> stdout 一次性输出 JSON -> exit 0
//
// 任何阶段出错:stdout 为空,stderr 写 "ballast: <err>",exit 1(B7)。150ms 看门狗兜底。
// 环境变量:BALLAST_HOME(必需)、BALLAST_RULES(必需)、BALLAST_FSYNC=1(状态写入 fsync)、
// BALLAST_SNAPSHOT=0(关闭 verify_fresh 的 git 快照)。
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync/atomic"
	"time"
)

var committed atomic.Bool

func main() {
	t0 := time.Now()
	// 短命进程:关掉按比例触发的 GC,仅保留内存上限兜底。
	debug.SetGCPercent(-1)
	debug.SetMemoryLimit(256 << 20)
	time.AfterFunc(150*time.Millisecond, func() {
		if !committed.Load() {
			fmt.Fprintln(os.Stderr, "ballast: watchdog 150ms")
			os.Exit(1)
		}
	})
	out, err := run(t0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ballast: %v\n", err)
		os.Exit(1)
	}
	committed.Store(true)
	if len(out) > 0 {
		if _, err := os.Stdout.Write(out); err != nil {
			os.Exit(1)
		}
	}
	os.Exit(0)
}

func run(t0 time.Time) ([]byte, error) {
	home := os.Getenv("BALLAST_HOME")
	rulesPath := os.Getenv("BALLAST_RULES")
	if home == "" || rulesPath == "" {
		return nil, fmt.Errorf("BALLAST_HOME and BALLAST_RULES are required")
	}
	ph := phases{}
	mark := func(name string, from *time.Time) {
		now := time.Now()
		ph[name] = now.Sub(*from).Microseconds()
		*from = now
	}
	t := time.Now()

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("stdin: %w", err)
	}
	in, err := parseInput(raw)
	if err != nil {
		return nil, err
	}
	mark("stdin_us", &t)

	rs, cacheState, err := loadRules(home, rulesPath)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	mark("rules_us", &t)

	sp := statePath(home, in.Cwd, in.SessionID, in.AgentID)
	st, corruptRead := readState(sp)
	mark("state_read_us", &t)

	ctx := &evalCtx{in: in, rs: rs, st: st, home: home}
	var hits []Hit
	if in.HookEventName != "SessionStart" {
		hits = ctx.evaluate()
	}
	mark("eval_us", &t)
	ph["bash_parse_us"] = ctx.bashUS

	dec := decide(in, rs, hits)
	mark("decide_us", &t)

	// 状态事务(B7:写入失败 = 异常)。
	delta := buildDelta(ctx, hits)
	res, err := commitState(sp, delta, os.Getenv("BALLAST_FSYNC") == "1")
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	mark("state_commit_us", &t)

	rec := auditCtx{in: in, rs: rs, hits: hits, dec: dec, ctx: ctx, tx: res, corruptRead: corruptRead, cache: cacheState, ph: ph}
	if err := writeAudit(home, &rec, time.Since(t0)); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	return dec.Output, nil
}
