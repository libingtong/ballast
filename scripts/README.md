# 验收脚本

这些脚本按 [`docs/architecture.md` §8.3](../docs/architecture.md) 与计划里程碑表编排集成验收。全部是 bash(macOS 自带的 3.2、Linux、Windows Git Bash 都能跑),参数固定,任何断言失败即以非 0 退出。

**隔离**:每个脚本开头把 `HOME`、`USERPROFILE`、`BALLAST_HOME`、`BALLAST_INSTALL_ROOT`、`BALLAST_MANAGED_DIR`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME`、`XDG_*`、`GIT_CONFIG_GLOBAL` 等全部重定向到 `mktemp` 目录,启动 `ballast` 前还会校验一遍(`guard_sandbox`),不在临时目录里就拒绝运行。不会启动真实的 claude / codex / cursor / hermes,不联网(`GOPROXY=off`)。唯一的例外是 `replay.sh`:它按你给的 `HOME` 与 `BALLAST_HOME` 读取 transcript(样本 id 由 `~/` 相对路径哈希得到),其余位置仍然重定向,且只读。

| 脚本 | 内容 |
|---|---|
| `regress.sh` | 计划 B1 的 14 例(显式 deny 规则 + 真实 hook 进程)、解析失败时放行、PowerShell 回归(`scripts/fixtures/powershell-regress.json`)、bashparse/psparse 回归表 |
| `fault.sh` | 用 `-tags ballast_fault` 构建,每个 FaultPoint × {error, panic, hang} × 四个客户端:退出码 1、标准输出为空、stderr 以 `ballast:` 开头;生产构建忽略 `BALLAST_FAULT` |
| `concurrency.sh` | 2–8 个真实 hook 进程并发:`event_seq` = 调用数 − 审计中的锁超时数(无丢失更新);同一 `tool_use_id` 并发或重投只生效一次;外部持锁时 hook 放行并审计 `lock_timeout`;子 agent / 会话 / worktree 状态分离;审计无撕裂行 |
| `hotreload.sh` | 不重启会话,改规则文件下一次 hook 就生效:同尺寸原地改、改动作、rename 替换、变更摘要、repo 覆盖 user、org 不可变、非法编辑回退 LKG、无 LKG 视为空、缓存损坏 |
| `freshness.sh` | 用真实 git pre-commit hook(调 `ballast verify --check --commit`)跑 11 个反例、3 个正例,以及 S/C 一致性(与独立的 git 计算对照) |
| `matrix.sh --mode stub` | 9 种栈 × {成功, 失败},用桩工具链(`accutil` 的拷贝),始终运行 |
| `matrix.sh --mode real` | 真实工具链;本地缺工具链时显式 SKIP,`CI=true` 时缺失即失败。CI 下每个系统只负责计划里分配的栈(Linux 8 个、macOS swift、Windows dotnet)。要下载插件/依赖的栈(maven、gradle、dotnet、ruby)本地默认 SKIP,`CI=true` 或 `BALLAST_MATRIX_ALLOW_NET=1` 时才跑 |
| `replay.sh` | 读 `$BALLAST_HOME/replay-set.txt`(每行一个 transcript 路径,`#` 为注释):至少 3 份、其中一份 > 10 MB,报告符合 schema,内存 < 200 MB,`$BALLAST_HOME/labels` 中标注 ≥ 20 条且都能关联到样本 id。文件不存在时显式 SKIP(退出 0);`BALLAST_REQUIRE_REPLAY=1` 时视为失败。只输出统计,不输出路径和内容 |
| `install-e2e.sh` | 四个客户端:dry-run、往返(逐字节相同)、幂等、保留用户已有与事后另加的 hook、用户改过的条目、原文件缺失、客户端未检测到、托管层已部署、配置损坏、备份上限 10、doctor |
| `accept-m0.sh` … `accept-m4.sh` | 里程碑编排,见各文件头注释。M1 含 `go test -race ./...`;M2 含 freshness 与 matrix stub;M3 含 audit lint、stats golden 与用合成 transcript 跑的 replay;M4 含 install-e2e、org render、doctor |

`lib.sh` 是共用函数(沙箱、断言、运行 ballast、构建二进制)。`tools/accutil/` 是脚本用的小工具(JSON 取值、带超时的运行与峰值内存、目录哈希、桩工具链、合成 transcript 等),每次运行时自动构建到临时目录。

`matrix.sh --mode real` 的前提:CI 作业自己装好工具链(例如 python 需要 `python` 命令与 pytest,ruby 需要 `bundler` 与 rspec gem);脚本先用原生命令跑一遍成功/失败两个夹具作为对照,对照不成立(工具链缺失或不能运行)时本地 SKIP、`CI=true` 时失败。`BALLAST_MATRIX_OS=linux|darwin|windows` 可在别的系统上演示 CI 的分工规则。

## 环境变量

| 变量 | 作用 |
|---|---|
| `CI=true` | 缺工具链、缺 `scripts/bench` 视为失败 |
| `BALLAST_BIN` / `BALLAST_FAULT_BIN` / `ACCUTIL` | 复用已构建的二进制(`accept-m*.sh` 构建一次后导出给子脚本) |
| `BALLAST_KEEP_TMP=1` | 退出时保留临时目录,便于排查 |
| `BALLAST_REQUIRE_REPLAY=1` | `replay.sh` 找不到 `replay-set.txt` 时失败 |
| `BALLAST_REPLAY_OUT` | `replay.sh` 把 JSON 报告写到这个目录(默认临时目录) |
| `BALLAST_ACCEPT_REAL_REPLAY=1` | `accept-m3.sh` 额外对你本机的 `replay-set.txt` 跑 `replay.sh` |
| `BALLAST_ACCEPT_SKIP_GOTEST=1` / `BALLAST_ACCEPT_SKIP_BENCH=1` | `accept-m1.sh` 把 `go test -race ./...` / 全套 bench 显式 SKIP(CI 在独立步骤里跑,避免重复耗时) |
| `BALLAST_ACCEPT_MATRIX_REAL=1` | `accept-m2.sh` 额外跑 `matrix.sh --mode real` |
| `BALLAST_MATRIX_ALLOW_NET=1` | `matrix.sh --mode real` 允许需要联网下载依赖的栈在本地运行 |
| `BALLAST_TEST_WATCHDOG_MS=<毫秒>` | **仅测试用**:放宽 hook 的 150ms 看门狗(150..60000,其余值忽略)。**只有 `-tags ballast_test` 的构建才认这个变量,生产构建忽略它**(与 `BALLAST_FAULT` 同一原则,见 `docs/architecture.md` §9);变量非空时 `lib.sh`(`build_ballast` / `build_ballast_fault`)与 `bench.sh` 才给被测二进制加该标签,Go 测试需 `go test -tags ballast_test`。CI 设为 15000(托管 Windows runner 上观察到单个进程被卡住 4.6s);`fault.sh` 的 hang 用例会把它置空,继续断言产品自己的 150ms |
| `BALLAST_BENCH_P95_MS=<毫秒>` | bench 的 p95 门槛:不设为产品的 30ms;只能抬高(30..1000),坏值直接报错。CI 在托管 macOS(80)与 Windows(250)上设了固定的平台上限,依据见 `scripts/bench/README.md` |

## 需要你在本机完成的验收

下面这一项无法在仓库内自动完成(transcript 含私人内容,不入库),`accept-m3.sh` 对它显式 SKIP;完成之前它在计划里保持"未核验":

1. 在 `~/.ballast/replay-set.txt` 列出至少 3 份本机 transcript(每行一个路径,`#` 为注释),其中至少 1 份大于 10 MB。
2. 用 `ballast replay <transcript> --format json` 与 `ballast stats --rule <规则 id> --sample 20` 取样本。
3. 在 `~/.ballast/labels/<规则 id>.jsonl` 里写入至少 20 条标注,每行 `{"sample_id":"…","label":"tp"|"fp","note":"…"}`。
4. `BALLAST_ACCEPT_REAL_REPLAY=1 bash scripts/accept-m3.sh`。

默认规则包的 known-hit 可能凑不够 20 条(默认规则大多是 `log`、命中稀少):此时把 `rules/optional/` 里的规则加进标注集,或增加 transcript。

## 与 `scripts/bench` 的约定

性能门槛由 U12c 的 `scripts/bench`(Go 程序)负责,真实接口是 `go run ./scripts/bench -bin <二进制> -base <临时目录> -root <仓库根> [-only <场景 id 正则>] [-n N] [-warm N]`(`-bin` 与 `-base` 必填,缺一退出 2;用 `-list` 列出场景 id)。`accept-m1.sh` 与 `accept-m2.sh` 通过 `lib.sh` 的 `run_bench` 调用它:`-bin` 是脚本已构建的共用二进制,`-base` 是 `$TMP_ROOT/bench`(临时目录内,不在任何 git 仓库里),其余参数原样透传。`accept-m1.sh` 不加参数,跑全部场景;`accept-m2.sh` 传 `-only '^6[abcd]$'`,只测 `git commit` 检查场景(6a/6b 为 1k/10k 文件的 `git commit`,6c/6d 为 `git commit -a`)。没有 `scripts/bench` 目录时显式 SKIP(`CI=true` 时失败)。也可以直接用 U12c 的 `scripts/bench.sh`(用 `ONLY` 环境变量缩小范围)。
