# scripts/bench:ballast hook 性能基准(计划 B10)

由 `proto/m0c/bench` 移植而来。入口是 `scripts/bench.sh`,它构建真实的 `ballast` 二进制
(`-trimpath -ldflags='-s -w'`)和本程序,然后由本程序从**外部父进程** spawn
`ballast hook --client X --event E`,计时覆盖进程创建、Go 运行时启动、包级初始化和整条 hook 流水线直到进程退出。

```bash
scripts/bench.sh                         # 全部场景,预热 20 次、正式 300 次
ONLY='^(4|6a|6b)$' scripts/bench.sh      # 部分运行(报告标记为不完整)
N=100 WARM=10 scripts/bench.sh           # 缩小规模(只用于调试,门槛判定不变)
KEEP=1 scripts/bench.sh                  # 保留 mktemp 目录(含 1k/10k 仓库、各场景的 BALLAST_HOME)
go run ./scripts/bench -list             # 列出场景 id
```

报告写到 `dist/bench/report.md` 与 `report.json`(`OUT=` 可改)。退出码:0 通过;1 门槛未通过;2 参数或环境错误。

## 门槛(修正 M0c 审计指出的判定缺陷)

每个场景必须**同时**满足:

1. `Bad == 0`:每次运行(含预热)都 exit 0、stderr 为空、stdout 与场景预期一致(空,或含指定子串);
2. 样本齐全:恰好得到 `n × 进程数` 个样本(spawn 全部失败时样本为 0,不会被报告为通过);
3. `p95 <= 30ms`,p95 取最近秩 `ceil(0.95n)`;门槛 30ms 是常量,没有命令行开关。

**托管 macOS / Windows runner 上的平台上限**(环境变量 `BALLAST_BENCH_P95_MS`,不设即产品的 30ms;只能抬高,值必须是 30..1000 的整数,否则 bench 以退出码 2 报错):
CI 在 Linux 上强制 30ms,在托管 macOS 设 80、Windows 设 250。**这是 CI 修复期间的临时措施,是否接受待负责人确认,架构规格(§9 的 30ms 三平台门槛)没有改。**
依据:bench 每次运行开头先测一次裸 `ballast version` 的 p95(报告里的 `process_floor_p95_ms`),即还没进入钩子流水线前平台自己
花掉的时间。GitHub 托管 runner 上实测:Linux 约 1.7ms,macOS(3 vCPU 虚拟机)约 11–13ms,Windows 约 9–12ms。

| 平台 | 22 个场景 p95 | 其中最大 | 上限 | 余量 |
|---|---|---|---|---|
| Linux(4 vCPU) | 全部在 30ms 内(最大 28.5ms) | 28.5ms | 30ms | 5% |
| macOS(3 vCPU) | 20 个在 28ms 内;10k 文件 S 快照的 6d / 8c 为 51.0 / 50.2ms(此前观察到 50–57ms) | 约 57ms | 80ms | 约 40% |
| Windows(4 vCPU) | 44.6–89.2ms;10k 文件 S 快照 6d / 8c 为 169.3 / 162.0ms | 约 170ms | 250ms | 约 45% |

上限留出约 40% 的余量吸收 runner 间的波动,所以它只防数量级的退化(例如多出一次全量扫描或一次进程启动),不是性能目标;其余条件(Bad == 0、样本齐全、审计命中)在所有平台照常适用。
CI 把看门狗放宽到 15s(`BALLAST_TEST_WATCHDOG_MS`),所以 Bad 基本不会因慢触发,p95 上限才是这里唯一的性能守卫。
Windows 上钩子自身比进程创建下限多约 30ms(最轻的 SessionStart 场景 p50 约 40ms,而裸 `ballast version` 约 12ms;疑为 Defender 对每个文件操作的扫描,根因未用 profile 确认),
状态事务持锁 p50 约 1.1ms(macOS 约 0.3ms)。这是产品性能缺口,登记在 `docs/architecture.md` §9 "已知缺口",需要单独跟踪,不是放宽门槛就能解决的。
开发机(M 系列 Mac)上全部 22 个场景 p95 都在 16.3ms 以内(下限 2.8ms)。开发机上直接运行 `scripts/bench.sh` 始终强制 30ms。

整次运行还要求:所有场景都在(完整运行)、每个 `--client`(claude-code、codex、cursor、hermes)至少有一个 PreToolUse 场景。
带审计校验的场景还要求审计里确有预期规则的命中记录(证明走了预期的代码路径,而不是静默放行);
场景 10 核对"无丢失更新"(状态 `event_seq` + `lock_timeout` 诊断数 == 执行次数);场景 11 核对损坏状态确实被检测并重置。

## 隔离

每个场景有独立的 `BALLAST_HOME`(含 12 条规则的用户层 `rules.json`、新鲜的 `gc.stamp`);子进程环境从零构造,
只继承 PATH(Windows 另加系统变量),`HOME`、`BALLAST_HOME`、`BALLAST_INSTALL_ROOT`、`BALLAST_MANAGED_DIR`、
`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME`、`GIT_CONFIG_GLOBAL` 全部指向 `-base`(临时目录)之内,不联网、
不碰任何真实配置。`-base` 必须不在任何 git 仓库内(否则"非仓库"场景会命中外层仓库)。

## 场景

12 条规则 = `rules/default.json`(5)+ `rules/optional/long-term.json`(5)+ `keep-pushing` + `claim-needs-evidence`;
`read-before-edit`、`no-long-sleep`、`evidence-before-commit` 提升为 deny,使场景走到阻断渲染路径。
测试仓库约 80% `.go`、10% `.md`、10% `docs/**`,含 `.ballast/project.json`(exempt 为 `**/*.md`、`docs/**`),提交后让索引越过
racy 窗口,再留 5 处未暂存改动。

| id | 场景 |
|---|---|
| 1 | SessionStart(每次新 session) |
| 2 | UserPromptSubmit |
| 3 | PreToolUse Edit(未读文件,deny) |
| 4 | PreToolUse Bash 短命令 |
| 5 | PreToolUse Bash 100KB 命令(约 2500 条语句) |
| 6a / 6b | `git commit` 检查(C 快照),1k / 10k 文件仓库,`evidence-before-commit` deny |
| 7 | PostToolUse(Read) |
| 8 | Stop(无完成声明) |
| 9 | 12 条规则且每次迭代前删除 `cache/`(缓存冷启动) |
| 10 | 4 个进程并发争同一 session 的状态锁 |
| 11 | 每次迭代前把状态文件改写成截断 JSON |
| 5b | 补充:100KB heredoc |
| 6c / 6d | 补充:`git commit -a` 检查(S 工作区快照),1k / 10k |
| 8b / 8c | 补充:Stop 含完成声明(`verify_fresh` 计算 S),1k / 10k |
| C-codex-patch / C-codex-exec | codex PreToolUse:`apply_patch`(deny)/ `exec_command` 短命令 |
| C-cursor-shell / C-cursor-pre | cursor `beforeShellExecution`(deny)/ `preToolUse` Shell 短命令 |
| C-hermes | hermes `pre_tool_call` terminal(block) |

可选的 hyperfine 交叉校验:`scripts/bench.sh` 在检测到 `hyperfine` 时对场景 4 再测一次(只供参考,不参与门槛)。
**本机未安装 hyperfine,该步骤在本机没有实际运行过。**

## 实测数据(2026-10-16,macOS arm64)

机型 Mac16,10(Apple M4,10 核,16GB),macOS 26.5(Darwin 25.5.0),go1.27.2,git 2.50.1,
`ballast` 约 5.5MB(`-trimpath -ldflags='-s -w'`)。**机器负载偏高且不受控**:运行期间 `vm.loadavg` 约 10–30(开始 17.18 / 29.54 / 22.73,
结束 10.37 / 26.23 / 21.83),有其它开发进程在跑;所以这些数字对空闲机器是保守的。N=300,预热 20,单轮。

| 场景 | 样本 | p50 ms | p95 ms | max ms | Bad | 结果 |
|---|---|---|---|---|---|---|
| 1 SessionStart | 300/300 | 3.41 | 4.10 | 4.90 | 0 | 通过 |
| 2 UserPromptSubmit | 300/300 | 3.37 | 3.96 | 4.31 | 0 | 通过 |
| 3 PreToolUse Edit | 300/300 | 3.68 | 3.83 | 4.02 | 0 | 通过 |
| 4 PreToolUse Bash 短命令 | 300/300 | 3.82 | 4.37 | 4.77 | 0 | 通过 |
| 5 Bash 100KB 命令 | 300/300 | 8.21 | 8.66 | 9.43 | 0 | 通过 |
| 6a git commit 1k(C) | 300/300 | 3.86 | 4.41 | 4.79 | 0 | 通过 |
| 6b git commit 10k(C) | 300/300 | 5.04 | 5.79 | 7.45 | 0 | 通过 |
| 7 PostToolUse | 300/300 | 3.57 | 4.29 | 5.77 | 0 | 通过 |
| 8 Stop | 300/300 | 3.74 | 4.50 | 10.72 | 0 | 通过 |
| 9 12 条规则冷缓存 | 300/300 | 6.55 | 7.03 | 7.97 | 0 | 通过 |
| 10 4 进程并发争锁 | 1200/1200 | 4.98 | 5.87 | 6.97 | 0 | 通过 |
| 11 损坏状态文件 | 300/300 | 3.73 | 4.86 | 9.81 | 0 | 通过 |
| 5b 100KB heredoc | 300/300 | 5.14 | 5.79 | 7.06 | 0 | 通过 |
| 6c git commit -a 1k(S) | 300/300 | 6.36 | 8.06 | 28.95 | 0 | 通过 |
| 6d git commit -a 10k(S) | 300/300 | 13.14 | 14.82 | 18.28 | 0 | 通过 |
| 8b Stop 含完成声明 1k | 300/300 | 6.40 | 6.85 | 7.94 | 0 | 通过 |
| 8c Stop 含完成声明 10k | 300/300 | 13.02 | 14.09 | 15.34 | 0 | 通过 |
| C-codex-patch | 300/300 | 3.89 | 4.44 | 23.04 | 0 | 通过 |
| C-codex-exec | 300/300 | 3.86 | 4.39 | 6.13 | 0 | 通过 |
| C-cursor-shell | 300/300 | 3.51 | 3.78 | 5.38 | 0 | 通过 |
| C-cursor-pre | 300/300 | 3.46 | 3.71 | 4.97 | 0 | 通过 |
| C-hermes | 300/300 | 3.53 | 4.08 | 4.43 | 0 | 通过 |

场景 10:共 1280 次执行,状态 `event_seq` = 1280,`lock_timeout` 诊断 0 次,无丢失更新。场景 11:320 次执行中检测并重置损坏状态 319 次
(第 1 次尚无状态文件)。

### 压力测试(不属于门槛,只说明余量)

同一台机器上另起 12 个 `yes > /dev/null` 把 10 核打满(`vm.loadavg` 17 → 57),其它条件不变:

- 所有 B10 编号场景(1–11)的 p95 为 10.7–21.5ms,全部通过;补充与客户端场景同样通过,只有 6d / 8c(10k 文件仓库的 S 快照)为 32.14 / 31.62ms,
  越过 30ms。这是 CPU 被超卖到负载 57 时的结果,而不是空闲机器上的结果。
- 作为对照,同样的压力下并行 Lstat 预取之前,6d / 8c 为 47.33 / 43.75ms。

### 已知的噪声

高负载时单个场景偶尔会因调度抖动越过 30ms(一次开发中的运行里 6a 的 p50 为 4.31ms,而 p95 为 30.85ms,同一时刻机器负载约 25,
重跑三次 p95 为 4.1–4.9ms)。门槛没有因此放宽,也没有重试掩盖;CI 若出现这种偶发失败,应先看 `max` 与机器负载。

## 定位与优化(pprof)

1. **S 快照(场景 6c/6d/8b/8c)**:压力测试下首次越线。对 `TreeS` 做 CPU profile,10k 文件仓库上约 77% 在
   `snapshot.(*refresher).refreshOne` → `os.Lstat` 的串行系统调用(10001 次 lstat,约 16ms)。优化:`internal/snapshot` 的
   `refresher.prefetch` 在条目数 ≥ 2048 时用最多 4 个 goroutine 预取叶子 Lstat,决策循环仍按索引顺序串行消费预取结果,所以
   与串行路径和 git 的结果逐位一致(`prefetch_test.go` 对串行、并行、git 三者比对)。10k 文件 S 的 p50 从约 20ms 降到约 13ms。
   剩余时间仍以 Lstat 为主(4 线程下约 7ms)与 1000 个目录的串行 lstat,继续提速收益有限。
2. **场景 5(100KB 命令)** p50 约 8ms,比短命令多约 4.6ms(100KB 的 JSON 解码,以及 2500 条语句的解析、归一化与求值),在预算内,未改动。
3. **场景 9(缓存冷启动)** 比热启动多约 2.8ms(读 JSON、编译 12 条规则、写缓存与 LKG),在预算内,未改动。
4. **GC**:实验性设置 `GOGC=off` 只带来 0.2–0.5ms 的差异(场景 5、9),且大 stdin 下有内存风险,不采用。

## hook 冷启动路径检查

- **包级初始化**:`GODEBUG=inittrace=1` 显示全部包 init 合计不足 0.3ms(最大的是 `os` 约 0.05–0.19ms、`unicode` 约 0.02–0.09ms、
  `mvdan.cc/sh/v3/fileutil` 0.03ms);`internal/archtest` 已禁止包级 `regexp.MustCompile` 与 `func init()`,本次未发现新增项。
- **进程内耗时**:同进程循环调用 `cli.Main` 的 profile 显示场景 4 整条流水线约 0.5ms,其中约 70% 是状态事务的系统调用;
  进程总耗时(约 3.5ms)主要是进程创建与 Go 运行时启动。
- **I/O 清单**(稳态,场景 4,临时插桩统计后已还原):对 cwd 做 `EvalSymlinks` 与向上逐级 `Lstat .git`;stat 三层规则文件;读 `cache/stat.json` 与
  规则缓存;读状态(无锁)→ 开锁文件 → 再读状态 → 临时文件写入 + `rename`;有审计记录时目录列表 + 追加。没有发现多余的 I/O;
  SessionStart 因审计与 `gc.stamp` 多几次调用,场景 1 p95 仍约 4ms。
- **缓存**:规则缓存命中时只读一个 JSON,不再读规则源文件;缓存键含图层文件的 stat,规则文件刚写入的 racy 窗口内键会再变一次,之后稳定。

## 限制

- 只在 macOS arm64 上实测;Linux / Windows 在 CI(`.github/workflows`)里跑,本机没有验证。Windows 的 lstat 与进程创建都更慢,
  10k 文件 S 场景(6d / 8c)在 Windows 上可能超标,需要 CI 数据确认。
- 没有安装 hyperfine,交叉校验步骤未实际执行。
- 机器负载不可控(见上),每个场景只跑一轮;没有空闲机器上的基线。
- 场景里的命令都是 bash;没有 PowerShell 命令的场景。
