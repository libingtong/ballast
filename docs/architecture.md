# Ballast 架构与接口契约(冻结版 v1.4)

> 状态:**已冻结**(2026-10-16;v1.1 为独立审核后的修订,v1.2 为第二轮审核 minor,v1.3 为 U1–U11 实现后的契约跟进,v1.4 为第 1 轮系统审计修复;变更清单见 §14)。依据 `docs/ballast-plan.md` v0.4(末尾"v0.4 变更"优先于正文)、`docs/facts/M0a-CONCLUSIONS.md`、`docs/facts/cases.json`、`docs/facts/M0c-PERF.md`、`docs/research/pinrule-install-and-clients.md`。
>
> 本文与 `docs/dsl-semantics.md`、`schema/rules.v1.json` 以及 `internal/model`、`internal/ports`、`internal/sys`、`internal/layout`、`internal/testenv`、`internal/version` 六个契约包一起构成**冻结契约**。12 个开发单元(U1–U12)各自在独立 worktree 中开发,彼此只能依赖本契约。契约变更流程见 §12。
>
> 凡标注【未核验】的条目,表示 Ballast 自己没有取证(M0a 只覆盖 Claude Code);实现按本文执行,`ballast doctor` 与 lint 会把它们列为警告。

---

## 1. 总体数据流

```
                    ┌──────────── AI 客户端(Claude Code / Codex / Cursor / Hermes)────────────┐
                    │  hook 配置:<abs>/ballast hook --client <id> --event <native>             │
                    └───────────────┬───────────────────────────────────────▲──────────────────┘
                         stdin JSON │                                       │ stdout(空 = 无决策)
                                    ▼                                       │ exit 0;异常 exit 1 + 空 stdout
┌──────────────────────────────── ballast hook(热路径,p95 ≤ 30ms)──────────────────────────────────┐
│ adapter.ParseEvent ─▶ snapshot.Locate(repo_inst) ─▶ config.Load(3 层 + 缓存 + LKG) ─▶ state.Load(无锁) │
│        │                                                                                              │
│        ▼                                                                                              │
│ EvalInput{Event, Commands(惰性 bashparse/psparse), Session, Evidence(evidence/snapshot), World, Paths}│
│        │                                                                                              │
│        ▼                                                                                              │
│ dsl.Evaluate ─▶ 能力检查(model.Capabilities)─▶ inject.Build ─▶ 内存中唯一 Decision                   │
│        │                                                                                              │
│        ▼                                                                                              │
│ state.Commit(加锁 ≤20ms,重读合并)─▶ audit.Append(单次 write)─▶ adapter.Render ─▶ 一次性写 stdout  │
└───────────────┬───────────────────────────────────────────────────────────────────────────────────────┘
                │ SessionStart:每天至多一次 detached `ballast gc`
                ▼
┌──────────────── 非热路径 ────────────────┐      ┌─────────────── ~/.ballast ───────────────┐
│ ballast verify → runner(detached)       │─────▶│ evidence/ jobs/ audit/ state/ cache/      │
│   TreeS(S_start) → 跑测试 → TreeS(S_end)│      │ backups/ reviews/ labels/ manifest        │
│   → VerifyRecord(fsync)→ audit verify  │      └───────────────────────────────────────────┘
│ ballast stats / promote / replay        │◀──── 读 audit/、evidence/、transcript
│ ballast install / uninstall / doctor    │────▶ 客户端配置(外科式改写 + 备份 + manifest)
│ ballast lint / test-rules / config show │
└──────────────────────────────────────────┘
```

要点:

- **运行时不嗅探客户端**:客户端身份由安装器写进命令行(`--client`),原生事件名由 `--event` 给出,经 `model.NativeEventTable` 归一。
- **单一提交点**(计划 B7):决策只在内存中形成一次;state 与 audit 写入成功后,才一次性写 stdout。任何异常(含 panic、看门狗)都保持 stdout 为空并 exit 1(M0a `exit1-json-deny` 证明 exit 1 + 合法 JSON 仍会生效,所以放行只能靠空 stdout)。
- **无决策时绝不输出 allow**(A6;Codex 要求 allow = `{}`/空)。
- **证据**只认 `ballast verify` 记录的退出码与内容快照(S/C tree id),不解析测试输出(A8/A9)。
- **快照原生计算**(v0.4 §2):C 和 S 都在进程内解析索引计算;只有不支持的情形才在 deadline 内退回 git 子进程。

---

## 2. 包职责与依赖层次

### 2.1 包清单

| 包 | 单元 | 职责 | 热路径 |
|---|---|---|---|
| `internal/model` | U0 契约 | 全部跨单元数据类型:Event、ToolCall、CommandNode、Rule、CompiledRuleSet、Tri、Action、Decision、Verdict、StateDelta、SessionState/EvidenceView/WorldView 接口、AuditRecord、VerifyRecord、能力矩阵与原生事件表、InstallManifest | 是 |
| `internal/ports` | U0 契约 | 服务接口:Adapter、RulesLoader、RuleCompiler、RuleEvaluator、BashParser、PowerShellParser、PathService、GlobMatcher、StateStore、AuditSink、AuditSource、RepoLocator、EvidenceProvider、ProjectLoader、Injector、Spawner | 是 |
| `internal/sys` | U0 契约 | Clock / IDGen / Env / FS 抽象及实现与假实现;WriteFileAtomic | 是 |
| `internal/layout` | U0 契约 | `~/.ballast`、repo `.ballast`、托管目录、各客户端配置根的路径解析 | 是 |
| `internal/testenv` | U0 契约 | 测试隔离沙箱(重定向 HOME 等) | 测试 |
| `internal/version` | U0 契约 | 版本号 | 是 |
| `internal/archtest` | U0 契约 | 依赖白名单、热路径禁 net/exec 的自动检查 | 测试 |
| `internal/paths` | U1 | 路径规范化、大小写比较、EvalSymlinks 双检、glob 列表 | 是 |
| `internal/bashparse` | U2 | mvdan AST → CommandList | 是 |
| `internal/psparse` | U2 | PowerShell 保守分词 → CommandList | 是 |
| `internal/dsl` | U3 | 规则解码/校验/lint/编译、匹配器、谓词、三值求值、裁决、test-rules | 是 |
| `internal/config` | U4 | 三层加载、合并、≤12、热更新、LKG、缓存、generation、explain | 是 |
| `internal/inject` | U4 | 注入文本与预算、Cursor `.mdc` 渲染 | 是 |
| `internal/state` | U5 | 会话状态存储、跨平台锁、事务、GC | 是 |
| `internal/audit` | U5 | 审计写入/轮转/读取/lint/GC、最小脱敏 | Writer 是 |
| `internal/adapter`(+`common`,`claudecode`,`codex`,`cursor`,`hermes`) | U6 | 解析、工具归一化、渲染、默认注册表 | 是 |
| `internal/hook` | U7 | 热路径流水线、看门狗、故障注入、`hook` 入口 | 是 |
| `internal/snapshot` | U8 | 仓库定位、repo_inst、索引解析、S/C tree id、git 退回 | 是(git 退回除外) |
| `internal/evidence` | U8 | verify 记录、job 状态、verify_fresh、commit 形式解析、`--check` | 是(Provider/View) |
| `internal/project` | U8 | project.json 读取校验、`init` 9 栈探测 | 是(Load) |
| `internal/runner` | U8 | 后台 runner、前台 runner、detached Spawner | 否 |
| `internal/stats` | U9 | stats、sample/review 凭据、promote | 否 |
| `internal/replay` | U9 | transcript 读取、离线回放、分类、标注、精确率 | 否 |
| `internal/install` | U10 | 多客户端安装/卸载、manifest、备份、托管检测、doctor、org render | 否 |
| `internal/cli`、`cmd/ballast` | U12 | 子命令解析、接线(wiring)、退出码 | `hook` 分支是 |
| `rules/`、`examples/`、`testdata/bash-regress.json`、`docs/pinrule-mapping.md` | U11 | 规则内容 | — |

### 2.2 依赖层次(禁止循环;由 `internal/archtest` 强制)

```
L0  model   sys   version
L1  ports(model)   layout(model,sys)   testenv(layout,sys)
L2  paths  bashparse  psparse(+bashparse)  dsl  inject  adapter/common  snapshot  project
    (都只依赖 L0/L1)
L3  config(ports 注入 dsl)  state  audit  adapter/{claudecode,codex,cursor,hermes}(+common)
    evidence(+snapshot)
L4  adapter(注册表,依赖四个子包)  hook(只依赖 L0/L1)  stats  replay  install
    runner(+evidence,snapshot,project)
L5  cli(接线,依赖全部)  →  cmd/ballast
```

每个包允许直接 import 的内部包以 `internal/archtest/archtest_test.go` 中的 `allowed` 表为准(即本节的机器可读版本)。新增内部依赖必须先改本节与该表(§12)。

关键约束:

- `hook` **只**依赖契约包;所有能力通过 `hook.Deps` 中的 ports 接口注入,由 `cli.WireHook` 接线。U7 可以完全用假实现开发与测试。
- `dsl` 不 import `paths`/`bashparse`/`psparse`/`adapter`:通过 `dsl.Options` 注入 `ports.PathService`、`ports.BashParser`、`ports.PowerShellParser`、`ports.AdapterRegistry`。`dsl` 直接 import `github.com/bmatcuk/doublestar/v4`:名称 glob(`tool`、`cmd`、`pipe_from`、`pipe_to`、`agent_type`、`branch_glob`)用区分大小写的 `doublestar.Match`,只有路径类(`path`、`writes`、谓词路径参数)走 `PathService`(dsl-semantics §5.1)。
- `evidence` 的 project.json 由 `evidence.Options.Project`(`ports.ProjectLoader`)**惰性**加载;`EvidenceProvider.Open(repo, deadline)` 不做 I/O,`hook.Deps` 不再含 ProjectLoader。`git -C <其他仓库> commit` 使用目标仓库的 project.json(required 与 exempt)。
- `replay` 不 import `hook`/`state`:自行实现内存版 `model.SessionState`,更新规则与 hook 文档一致;`--initial-state` 格式在 `internal/replay/doc.go` 冻结(`model.Fixture` 的状态子集)。
- `config` 通过 `ports.RuleCompiler` 使用 dsl;`stats`/`replay` 通过 `ports.AuditSource`、`ports.RuleCompiler` 等使用其他单元。
- `install` 不 import `inject`:Cursor `.mdc` 内容由 CLI 调 `inject.RenderMDC` 后经 `install.Options.MDC` 传入;`doctor` 的 `cursor.mdc_current` 同样经 `install.DoctorOptions.MDC` 传入。
- `psparse` 可调用 `bashparse.ParseGit`(同属 U2)。

---

## 3. 磁盘布局与文件格式

### 3.1 `~/.ballast/`(`BALLAST_HOME` 可重定向,仅供测试;Windows 为 `%USERPROFILE%\.ballast`)

| 路径 | 所有者 | 格式 | 写入方式 |
|---|---|---|---|
| `rules.json` | 用户 | RuleFile(user 层) | 用户手写 |
| `state/<repo_inst>/<session>/<agent>.json` | U5 | `state.Record` JSON,`"v":1` | 加锁事务 + 原子替换,不 fsync |
| `state/<repo_inst>/<session>/<agent>.json.lock` | U5 | 空文件,flock / LockFileEx | DropAgent/DropSession 只在持锁时删状态文件;DropAgent 不删锁文件,DropSession(SessionEnd)随后在持锁状态下尽力删除已无状态文件的锁文件和空会话目录(v1.4,加锁后复核 inode,避免孤儿锁分裂),删不掉的(如 Windows)由 TTL GC 兜底 |
| `evidence/<repo_inst>/<name>/<t_start 20 位纳秒>-<job_id>.json` | U8 | `model.VerifyRecord` | 原子 + **fsync**,0600 |
| `jobs/<repo_inst>/<name>/<job_id>.json` | U8 | `model.JobStatus` | 原子 |
| `jobs/<repo_inst>/<name>/<job_id>.log` | U8 | runner 合并输出 | 追加 |
| `audit/YYYY-MM.jsonl`、`audit/YYYY-MM.N.jsonl` | U5 | 每行一个 `model.AuditRecord`,≤8 KiB | `O_APPEND` 单次 write |
| `cache/stat.json` | U4 | `{key(stat 指纹+版本) → generation}` | 原子,失败不算异常 |
| `cache/rules-<generation>-<key[:8]>.json` | U4 | `{sources, rules([]LoadedRule), settings, notices}`;key = stat 指纹键,避免同 generation 的不同仓库互相覆盖(v1.3) | 原子,失败不算异常;回收(v1.4):`stat.json` 满 64 条重建时同时删除不再被引用的规则缓存文件,`ballast gc` 另删超过 7 天且未被引用的文件;升级前的 `rules-<gen>.json` 旧名文件同样会被 GC 回收 |
| `cache/lkg/<layer>-<sha256(abs path)[:12]>.json` | U4 | 该层最近一次合法的原始字节 | 原子 |
| `backups/<UTC yyyymmddThhmmssZ>-<rand6>/` | U10 | 被改写文件的原样副本(文件名为 `<sha256(绝对路径)前 16 位十六进制>-<basename,最长 64 字节>`,原路径记在目录内 `index.json` 与 `BackupRecord.Files`,保证定长且可逆) | 目录 0700、文件 0600,保留最近 10 个 |
| `reviews/<rule_id>.json` | U9 | `stats.Review` | 原子 |
| `labels/<rule_id>.jsonl` | 用户/U9 | `{"sample_id","label":"tp"\|"fp","note"}` | 用户追加 |
| `install-manifest.json` | U10 | `model.InstallManifest` | 原子 + fsync |
| `gc.stamp` | U7 | 空文件,mtime 即上次 GC 时间 | 只由 hook 在派生 `ballast gc` 之前 touch;派生失败时删除以便下次重试;`ballast gc` 本身不写(v1.3) |
| `replay-set.txt` | 用户 | 每行一个 transcript 路径(不入库) | — |

目录权限 0700,文件 0600。所有路径经 `layout.Layout` 解析,任何单元不得手拼 `~/.ballast`。

**状态文件 `state.Record`(v1)字段**:`v, started_at, event_seq, seen[](≤512,FIFO), read{norm_path: time}(≤4096), tool_count, tool_count_since_inject, prompt_count, prev_prompt_tail(≤200 字), prior_hits{rule:n}, generation, rule_digest{rule:action}, full_injected, reported_jobs[](≤64), last_job_report_at, last_assistant_text(≤4000 字), last_tool_failure_at, ring[](≤8,EventSummary), evals{rule: EvalCount}`。路径分量用 `state.SanitizeComponent` 规整(`^[A-Za-z0-9_-]{1,64}$` 原样,否则 `h`+sha256 前 16 位)。

**repo_inst**:`hex(sha256(realpath(toplevel) + "\x00" + realpath(common_dir)))[:16]`(`snapshot.RepoInst`,已实现)。不在 git 仓库时,状态作用域用 `"norepo-" + hex(sha256(规范化 cwd))[:16]`,证据谓词返回 unknown。

**job_id**:`UTC yyyymmddThhmmss` + `-` + 6 位十六进制随机数(`sys.IDGen`)。

### 3.2 仓库内 `<repo>/.ballast/`

| 文件 | 说明 |
|---|---|
| `rules.json` | repo 层规则(RuleFile)。不在 git 仓库时,repo 根取 cwd |
| `project.json` | verify 定义与 exempt(见 `internal/project` 包文档,冻结格式) |

### 3.3 托管(org)目录

| 平台 | 目录(`layout.ManagedDirFor`,测试用 `BALLAST_MANAGED_DIR` 重定向) |
|---|---|
| macOS | `/Library/Application Support/ClaudeCode` |
| Linux / WSL2 | `/etc/claude-code` |
| Windows | `C:\Program Files\ClaudeCode` |

- org 规则:`<托管目录>/ballast/rules.json`。
- Claude 托管设置:`managed-settings.json`、`managed-settings.d/*.json`(用于托管层已部署检测与 `org render --write`)。

### 3.4 客户端配置根

| 客户端 | 根目录 | 覆盖变量 |
|---|---|---|
| Claude Code | `~/.claude` | `CLAUDE_CONFIG_DIR`(Claude 自身支持) |
| Codex | `~/.codex` | `CODEX_HOME` |
| Cursor | `~/.cursor` | 无(测试用 `BALLAST_INSTALL_ROOT`) |
| Hermes | `~/.hermes` | `HERMES_HOME` |

`BALLAST_INSTALL_ROOT` 替换上述 `~`(仅测试/e2e)。

---

## 4. 客户端能力矩阵

权威数据在 `internal/model/capability.go`(`model.Capabilities`)和 `internal/model/client.go`(`model.NativeEventTable`)。下表是其人类可读版本。R = 合并后的 reason,C = 注入文本。

### 4.1 规则事件 × 动作(`log` 在事件存在时恒可用,stdout 为空)

| 客户端 | 事件 | deny | ask | warn |
|---|---|---|---|---|
| **Claude Code**(一级) | PreToolUse | ✓ `permissionDecision:"deny"` | ✓ `"ask"`(`-p` 下可能被拒) | ✓ 仅 `additionalContext`,不输出决策(M0a) |
| | UserPromptSubmit | ✓ `decision:"block"` | ✗ | ✓ `additionalContext` |
| | PostToolUse | ✗(已执行) | ✗ | ✓ `additionalContext` |
| | PostToolUseFailure | ✗ | ✗ | ✓ `additionalContext`【未核验:文档列出,未单独取证】 |
| | Stop | ✓ `decision:"block"`(会话继续,8 次上限,A10 已实测) | ✗ | ✓ `systemMessage`(放行停止,M0a 已实测) |
| | SubagentStop | ✓ `decision:"block"` | ✗ | ✓ `systemMessage`【未核验:按 Stop 推断】 |
| **Codex**(二级,全部【未核验】) | PreToolUse(含 PermissionRequest) | ✓ PreToolUse:`permissionDecision:"deny"`;PermissionRequest:`decision.behavior:"deny"` | ✗(Codex 解析但不执行,fail open) | ✗ |
| | UserPromptSubmit | ✗ | ✗ | ✓ `additionalContext` |
| | PostToolUse | ✗ | ✗ | ✓ `additionalContext` |
| | Stop | ✓ `decision:"block"`(pinrule 亦标待验证) | ✗ | ✗ |
| | PostToolUseFailure / SubagentStop | 事件不存在 | | |
| **Cursor**(二级,全部【未核验】) | PreToolUse(preToolUse / beforeShellExecution / beforeMCPExecution / beforeReadFile) | ✓ `{"permission":"deny","user_message":R,"agent_message":R}` | ✓ `permission:"ask"` | ✗ |
| | UserPromptSubmit(beforeSubmitPrompt) | ✗ | ✗ | ✓ 嵌套 `hookSpecificOutput.additionalContext`(沿用 pinrule) |
| | PostToolUse | ✗ | ✗ | ✓ `{"additional_context":…}` |
| | Stop / SubagentStop | ✓ **语义为续跑**:`{"followup_message":R}`,条目带 `loop_limit:10`;lint 给 `cursor-stop-followup` 警告 | ✗ | ✗ |
| | PostToolUseFailure | 事件不存在 | | |
| **Hermes**(三级,尽力,全部【未核验】) | PreToolUse(pre_tool_call) | ✓ `{"decision":"block","reason":R}` | ✗ | ✗ |
| | UserPromptSubmit(pre_llm_call) | ✗ | ✗ | ✓ `{"context":…}` |
| | PostToolUse(post_tool_call) | ✗ | ✗ | ✗(只能 log) |
| | Stop / SubagentStop / PostToolUseFailure | 事件不存在(on_session_end 归一为 SessionEnd,不能干预) | | |

**规则**:

- `lint` 按客户端拒绝矩阵外组合(`action-unsupported`,错误),未核验格给警告(`action-unverified`)。默认检查的客户端 = manifest 中已安装的客户端 + claude-code;规则可用 `clients` 字段限定适用客户端。
- 运行期若遇到不支持的组合(例如用户未 lint 就安装到 codex):该命中的 `Effective` 降为 `log`,写审计 `diag: capability_degraded`(显式降级,不静默)。
- 默认 `target` = 该事件在 Claude Code 上允许的最严一档(`model.DefaultTarget`)。

### 4.2 注入载体

| 客户端 | 全文(≤8k) | 锚点(UserPromptSubmit) | 回合中途补锚点 | 其他 |
|---|---|---|---|---|
| Claude Code | SessionStart(全部 source) | ✓ | PostToolBatch | — |
| Codex | SessionStart | ✓ | PostToolUse | 无 compact 后重注入 |
| Cursor | sessionStart(`additional_context`) | ✓ | postToolUse | **另写托管 `~/.cursor/rules/ballast.mdc`(alwaysApply)** 作为可靠通道 |
| Hermes | 首个 pre_llm_call(`is_first_turn` 或会话未注入过全文) | ✓ | ✗ | on_session_start 不能注入 |

### 4.3 原生事件映射

| 客户端 | 原生事件 → 归一事件(安装全部) |
|---|---|
| Claude Code | SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch, Stop, SubagentStop, SessionEnd(同名);另有**仅解析**的 PreCompact、PostCompact、SubagentStart(表中 `Install=false`:适配器能解析,安装器与 doctor 跳过,规则不能引用;v1.3) |
| Codex | SessionStart, UserPromptSubmit, PreToolUse, **PermissionRequest→PreToolUse**, PostToolUse, Stop |
| Cursor | sessionStart→SessionStart, beforeSubmitPrompt→UserPromptSubmit, preToolUse→PreToolUse, beforeShellExecution→PreToolUse(Bash), beforeMCPExecution→PreToolUse(mcp__*), beforeReadFile→PreToolUse(Read), postToolUse→PostToolUse, afterAgentResponse→AgentResponse(仅记账), stop→Stop, subagentStop→SubagentStop |
| Hermes | on_session_start→SessionStart, pre_llm_call→UserPromptSubmit, pre_tool_call→PreToolUse, post_tool_call→PostToolUse, on_session_end→SessionEnd |

字段回退:session = `session_id` → `conversation_id`(Cursor);agent = `agent_id` → `subagent_id`(Cursor)。`effort` 可为对象 `{level}` 或字符串。Claude 压缩用内部 agent(`agent_type` 存在且为空的 SubagentStop)默认忽略(M0a)。

### 4.4 工具名归一化

| 原生 | 归一 | 说明 |
|---|---|---|
| Codex `apply_patch` | Edit | 解析 envelope:Update/Add/Delete/Move 全部目标进入 `Targets`;Add → `TargetAdds`;Delete → `TargetDeletes` |
| Codex `exec_command` / `shell` | Bash | `cmd` 为字符串或 argv 数组(数组按 shell 引号拼接)。例外(v1.3):argv 形如 `["apply_patch"\|"applypatch", …, "<envelope>"]` 时按 `apply_patch` 归一为 Edit(RawName 保留原名);字符串形式的 `apply_patch <<EOF` 仍是 Bash(已知局限) |
| Cursor `Shell` | Bash | |
| Hermes `terminal` / `shell` / `execute_shell` | Bash | |
| Hermes `read_file` / `write_file` / `create_file` / `patch_file` / `edit_file` | Read / Write / Write / Edit / Edit | |
| `Task` | Agent | |
| Claude `PowerShell` | PowerShell | `Shell=powershell`,走 `ps` 匹配器 |

---

## 5. 安装目标清单

通用规则见 `internal/install/doc.go`(冻结)。要点:

- **命令行冻结**:`<abs binary> hook --client <id> --event <native>`(`install.HookArgv`)。Claude 用 `command` + `args` 分开写(A12,路径含空格无需拼接);其他客户端只有单个命令字符串,用 `install.QuoteCommand`(unix POSIX 单引号;Windows `CommandLineToArgvW` 规则)。
- **归属判定**:命令前缀 = `<binary> hook --client <id>` **且** 条目规范 JSON 的 sha256 与 manifest 记录一致。只满足前者 = "用户改过",安装不动它也不重复添加,卸载保留并警告。禁止子串匹配。
- **原地替换**已有条目,不调整顺序;重复安装字节级幂等。
- **绝不写** `async`、`onFailure`(B7)。
- 每个将被改写的文件写入前备份;`--dry-run` 只输出 unified diff。
- **卸载以 manifest 为准**(覆盖已卸载的客户端);配置解析失败返回非 0。
- 托管层(Claude managed settings)已部署 Ballast 时拒绝用户层安装(A15)。
- 不在 `init` 时自动安装;不按子串识别;不绑定解释器;备份轮转。
- **结构标记**(v1.3):安装时在 `ManagedFlag` 中记录被填充的容器(JSON `hooks` 对象、Hermes `hooks:` 表头与各事件键)原本是否存在及其原文,以及新建文件上方最近的已存在目录;卸载只删除 Ballast 创建的空容器,还原用户原有的空 `hooks` 对象、`hooks: {}`/`hooks: # 注释` 表头与 `pre_tool_call: []`,目录只清理到安装前已存在的那一层(`--force` 新建的客户端根目录一并清理)。键名见 `internal/install/doc.go`。

### 5.1 Claude Code

文件:`<CLAUDE_CONFIG_DIR 或 ~/.claude>/settings.json`。每个原生事件一组:

```json
{"hooks": {
  "PreToolUse": [{"matcher": "*", "hooks": [
    {"type": "command", "command": "/opt/ballast/bin/ballast",
     "args": ["hook", "--client", "claude-code", "--event", "PreToolUse"]}]}],
  "SessionStart": [{"hooks": [
    {"type": "command", "command": "/opt/ballast/bin/ballast",
     "args": ["hook", "--client", "claude-code", "--event", "SessionStart"]}]}]
}}
```

工具事件(PreToolUse、PostToolUse、PostToolUseFailure)带 `"matcher":"*"`,其余不带 matcher。manifest 记录内层 `{type,command,args}` 对象的 hash;外层分组在其 `hooks` 为空时随卸载删除。完整 9 个事件的期望输出见 `internal/install/testdata/golden.json`。

### 5.2 Codex

- `<CODEX_HOME>/hooks.json`:`{"hooks":{"<Event>":[{"hooks":[{"type":"command","command":"<quoted argv>","timeout":30}]}]}}`,不带 matcher;6 个原生事件。
- `<CODEX_HOME>/config.toml`:确保 `[features]` 下 `hooks = true`(按行外科式编辑,原子写);manifest 记 `ManagedFlag{set_by_ballast}`;卸载**不自动关闭**,只提示。
- **trust 默认不写**。仅 `--codex-trust` 时写 `[hooks.state.'<hooks.json 绝对路径>:<snake_event>:<组下标>:<条目下标>']`、`enabled = true`、`trusted_hash = "sha256:…"`(`install.CodexTrustHash`,算法照搬 pinrule 引用的 Codex 0.130 源码【未核验于其他版本】);记入 manifest,卸载删除。因 key 含下标,每次写入后按最终位置重算。
- 提示:hook 仅在交互式 TUI 触发(pinrule 观察【未核验】)。

### 5.3 Cursor

- `~/.cursor/hooks.json`:`{"version":1,"hooks":{"<event>":[{"command":"<quoted argv>"}]}}`;`stop`、`subagentStop` 条目另加 `"loop_limit":10`;10 个原生事件。`version:1` 仅在缺失时添加;添加到已有文件时记入 manifest,卸载后若已无任何 hooks 则一并删除(v1.3)。
- `~/.cursor/rules/ballast.mdc`:整文件托管(`inject.RenderMDC`,front matter `alwaysApply: true` + 托管标记行 `inject.MDCMarker` + 全文注入),记为 `ManagedFile{whole:true}`,卸载删除。规则变更后由 `ballast install --client cursor` 重新生成;`doctor` 检查是否过期(`cursor.mdc_current`)。
- 安装后提示 Reload Window。

### 5.4 Hermes

- `<HERMES_HOME>/config.yaml` 的顶层 `hooks:` 段,按行外科式编辑,段外字节不变:

```yaml
hooks:
  pre_tool_call:
    - command: "/opt/ballast/bin/ballast hook --client hermes --event pre_tool_call"
      timeout: 30
```

- 只认块状 YAML;`hooks:` 段使用 flow 风格或锚点时报 `ErrSettingsParse` 并拒绝写入。
- 提示用户以 `--accept-hooks` 同意(pinrule 记录)。

### 5.5 `ballast org render`(T1 托管模板,【未核验】待 M0b)

输出 Claude 托管设置片段(只打印;`--write --i-am-admin` 且为 root 时写 `<托管目录>/managed-settings.d/ballast.json`):

```json
{
  "allowManagedHooksOnly": true,
  "hooks": { "<每个 claude-code 原生事件>": [{"hooks": [{"type": "command",
      "command": "/usr/local/libexec/ballast/ballast",
      "args": ["hook", "--client", "claude-code", "--event", "<Event>"]}]}] },
  "sandbox": {
    "enabled": true, "allowUnsandboxedCommands": false, "failIfUnavailable": true,
    "excludedCommands": [],
    "filesystem": {"denyWrite": ["<org 规则目录>", "~/.ballast", "<repo>/.ballast", "<二进制目录>", "~/.claude/settings*.json"]}
  },
  "permissions": {"deny": ["Edit(<受保护路径>/**)", "Write(<受保护路径>/**)", "NotebookEdit(<受保护路径>/**)"]},
  "allowManagedPermissionRulesOnly": true
}
```

MCP 白名单键名按 Claude 托管设置文档在实现时核对(open question)。

---

## 6. 持续注入(B5)

由 `inject.Builder` 实现,规则在 `internal/inject/doc.go`(冻结)。预算以 rune 计:

| 部分 | 时机 | 预算 |
|---|---|---|
| full | `caps.FullInjectEvent` | ≤ 8000(总) |
| anchor | UserPromptSubmit 每 `anchor_every` 次(默认 1);`caps.MidTurnEvent` 上 `tool_count_since_inject ≥ batch_after_tools`(默认 25) | ≤ 1200 |
| gen_summary | 会话记录的 generation 与当前不同 | ≤ 1000 |
| job_results | 有未回报的已完成 verify job(UserPromptSubmit 或 MidTurnEvent) | ≤ 400 |
| notices | config_invalid / rule_dropped | ≤ 400 |

非 SessionStart 的单次注入总量 ≤ 2500。warn 的 reason 与注入文本合并到同一字段(`Reason + "\n\n" + Context`)。

---

## 7. CLI 命令与退出码

命令解析在 `internal/cli`(U12),常量在 `internal/cli/exitcodes.go`。通用码:**0** 成功;**1** 结果为否(lint 有错误、测试失败、doctor 有 fail、闸门拒绝、托管层已部署);**2** 用法/参数错误(verify 中为配置错误);**3** 需读写的文件无法解析,已拒绝且未写入;**4** 不允许的操作;**5** I/O/基础设施错误。任何命令带 `--help`/`-h` 只打印帮助并 exit 0,不产生副作用。

| 命令 | 退出码 |
|---|---|
| `hook --client X --event E` | **只用 0 / 1**:0 = 正常(stdout 可能为空);1 = 任何异常(stdout 必为空,stderr `ballast: …`)。**永不使用 2**(会阻塞工具调用,A7)。参数错误、未知客户端/事件、stdin 非法均走 1 |
| `lint [--layer] [--client] [--stack] [--examples] <paths>` | 0 无错误(可有警告);1 有错误;2 用法;5 读文件失败 |
| `test-rules <paths>` | 0 全部通过;1 有失败;2 用法/文件不可解码 |
| `config show [--explain] [--json]` | 0;1 有层非法(LKG 或空,内容仍打印);2 用法 |
| `verify [name...]` | 0 已启动;2 配置错误(无仓库/无 project.json/未知 name/project.json 非法);5 基础设施错误 |
| `verify --status [job\|name]` | 0 pass;1 fail;3 timeout;4 signal;5 infra;6 运行中;7 superseded(扩展);11 无记录 |
| `verify --check [--commit\|--worktree]` | 0 新鲜;10 过期;11 无证据;12 无法判定(非 git、参数无法解析、快照失败、project.json 缺失或非法);13 同名 job 运行中。用于 git pre-commit:`--commit`(默认)模式下若环境有 `GIT_INDEX_FILE`,CLI 把它(相对值按 cwd)解析为绝对路径,经 `evidence.CheckOptions.IndexFile` 传入,C = F(该索引)——`commit -a`/带路径提交时 git 此刻指向 `index.lock` 或临时 next-index,即真正要提交的树。仓库由 cwd 决定(git 在被提交仓库的顶层运行 hook,所以 `git -C ../other commit` 在 ../other 中检查)。热路径(hook)**永不**读取 `GIT_INDEX_FILE` |
| `verify --foreground <name>` | 同 `--status`;2 配置错误 |
| `verify --run-job <id>`(内部) | 同 `--status` |
| `init [--yes] [--force] [--dry-run]` | 0 已写入/无需写入/dry-run;1 用户拒绝或未探测到栈;2 用法;3 已有 project.json 且无 `--force` |
| `stats [--since] [--by] [--rule --sample N] [--json]` | 0;2 用法;5 读审计失败 |
| `rules promote <id> --to X [--force]` | 0 已升级;1 闸门不满足(无 review 凭据或命中不足);2 用法;4 规则不存在于 repo/user 层、是 org 规则、或目标档位不被已安装客户端支持;5 I/O |
| `replay <transcript> …` | 0;2 用法;5 I/O |
| `audit lint [--json]` | 0 格式全部合规;1 有问题;5 I/O |
| `install --client … [--dry-run] [--codex-trust] [--binary] [--force]` | 0 成功或无变化;1 托管层已部署而拒绝;2 用法/客户端未检测到且无 `--force`/二进制路径不稳定;3 配置解析失败(未写入);5 I/O |
| `uninstall [--client\|--all] [--dry-run]` | 0;1 部分条目被用户改过而保留(已警告);3 解析失败;5 I/O |
| `doctor [--json]` | 0 无 fail(可有 warn);1 有 fail |
| `org render [--write --i-am-admin]` | 0;2 用法;4 `--write` 但非 root 或缺 `--i-am-admin` |
| `gc`(内部) | 0;5(写 stderr,不影响任何人) |
| `version` | 0 |

---

## 8. 单元所有权与跨单元接口

### 8.1 目录所有权

| 单元 | 独占目录/文件 | 可读不可改 |
|---|---|---|
| U0 架构师(本契约) | `internal/{model,ports,sys,layout,testenv,version,archtest}`、`schema/`、`docs/architecture.md`、`docs/dsl-semantics.md`、各包 `doc.go` 的契约段 | — |
| U1 | `internal/paths/**` | 契约 |
| U2 | `internal/bashparse/**`、`internal/psparse/**`、`internal/model/shellreads.go` 的函数体与 `internal/model/shellreads_test.go`(v1.2) | 契约 |
| U3 | `internal/dsl/**` | 契约、`schema/` |
| U4 | `internal/config/**`、`internal/inject/**` | 契约 |
| U5 | `internal/state/**`、`internal/audit/**` | 契约 |
| U6 | `internal/adapter/**` | 契约、`docs/facts/payloads/**` |
| U7 | `internal/hook/**` | 契约 |
| U8 | `internal/snapshot/**`、`internal/evidence/**`、`internal/project/**`、`internal/runner/**` | 契约 |
| U9 | `internal/stats/**`、`internal/replay/**` | 契约 |
| U10 | `internal/install/**` | 契约 |
| U11 | `rules/**`、`examples/**`、`testdata/bash-regress.json`、`docs/pinrule-mapping.md` | 契约、`/tmp/pinrule` |
| U12 | `cmd/ballast/**`、`internal/cli/**`、`scripts/**`(新增脚本;`scripts/probe`、`scripts/m0c-bench.sh` 保留)、`scripts/bench/`、`.github/workflows/**`、`README.md` | 全部 |

各单元可以在自己目录内新增未导出代码、测试与 testdata;**修改已导出签名属于契约变更**(§12)。各包 `doc.go` 中的"Acceptance"段落是该单元的验收要点。

### 8.2 跨单元只能依赖的接口

| 提供方 | 允许其他单元依赖的接口 | 主要使用方 |
|---|---|---|
| U0 | `model.*`、`ports.*`、`sys.*`、`layout.*`、`testenv.*`、`version.*` | 全部 |
| U1 | `paths.Service`(实现 `ports.PathService`)、`paths.NewService`;U8 的 exempt 过滤**不**依赖 U1 | U7(经 ports)、U3(经 ports)、U9(经 ports)、U12 |
| U2 | `bashparse.Parser`、`psparse.Parser`(实现两个 ports);`bashparse.ParseGit` 仅供 psparse | 经 ports |
| U3 | `dsl.Engine`(实现 `ports.RuleCompiler`、`ports.RuleEvaluator`)、`dsl.NewEngine`、`dsl.Options`、`(*Engine).Lint`、`(*Engine).TestRules`、`dsl.LintInput/LintOptions/TestReport`、`dsl.CheckCapabilities`、`dsl.HasErrors`、`dsl.Arbitrate`、`dsl.Code*` 常量 | U4(ports)、U9(ports + CheckCapabilities 注入)、U12 |
| U4 | `config.Loader`(`ports.RulesLoader`)、`config.NewLoader/Options`、`(*Loader).Explain`、`config.Merge`、`config.Generation`;`inject.Builder`(`ports.Injector`)、`inject.RenderMDC`、`inject.Digest` | U7(ports)、U12 |
| U5 | `state.Store`(`ports.StateStore`)、`state.New/Options`、`(*Store).GC`、`state.Record`;`audit.Writer`(`ports.AuditSink`)、`audit.Reader`(`ports.AuditSource`)、`audit.Lint`、`audit.GC` | U7、U8(runner 写 verify 审计)、U9(ports)、U12 |
| U6 | `adapter.Default`、`adapter.Registry`(`ports.AdapterRegistry`)、四个子包的 `New` | U3(ports,test-rules)、U7(ports)、U9(ports)、U12 |
| U7 | `hook.Main`、`hook.Deps`、`hook.ParseArgs`、`hook.FaultsFromEnv`、`hook.AllFaultPoints`、`hook.BuildDelta`、`hook.ScopeFor` | U12(replay 不依赖 hook,见 §2.2) |
| U8 | `snapshot.Locator`(`ports.RepoLocator`)、`snapshot.RepoInst`、`snapshot.Branch`;`evidence.Provider`(`ports.EvidenceProvider`)、`evidence.Options`(含 `Project` 惰性加载器)、`evidence.Store`、`evidence.Check` + `evidence.CheckOptions`、`evidence.ParseCommitForm`;`project.Loader`(`ports.ProjectLoader`)、`project.Detect/Propose/Decode/Hash`;`runner.Start/RunJob/Foreground/Spawner` | U7(ports)、U12 |
| U9 | `stats.Compute/Sample/Promote/ParseSince`、`replay.Run/RenderMarkdown/SampleID` | U12 |
| U10 | `install.Install/Uninstall/Doctor/OrgRender/LoadManifest/HookArgv/QuoteCommand`、`install.Options`/`DoctorOptions`(`MDC` 由 CLI 渲染后传入) | U12 |
| U11 | 规则文件本身(必须通过 `ballast lint` 与 `test-rules`) | U12 验收脚本 |

### 8.3 U11 / U12 的验收要点(无独立 Go 包文档)

**单元验收与集成验收的划分**:各包 `doc.go` 的 "Acceptance (Ux)" 段只列**单元内**用契约包 + 假实现即可完成的项;凡需要其他单元真实实现(真实适配器、路径服务、解析器、完整二进制)的项一律标为 "Integration acceptance (U12)",在 U12 合并全部单元后执行。

**U11 规则内容**:

- `rules/default.json`:恰好 5 条 `read-before-edit`、`no-long-sleep`、`evidence-before-commit`、`no-skip-verify`、`no-tamper`;每条凡涉及 shell 都同时写 `bash` 与 `ps` 变体(§13 "分派");默认 `action` 按计划 B2 取值(新规则 log,可用 target 声明升级目标),`id` 与 `schema/examples` 中同名规则语义一致。`read-before-edit` 的 message/inject 要提到只读 shell 命令(Codex 无 Read 工具);`evidence-before-commit` 的 message 要提示"`git add` 与 `git commit` 分两次执行"(dsl-semantics §14)。
- `rules/optional/*.json`:`no-foreground-long-task`(命令头列表取自 pinrule `non_blocking.py:76-86`)、`no-shell-wait`、`claim-needs-evidence`、`read-after-test-fail`(`verify_failed_since_read`)、keep_pushing 与 long_term 的可映射部分。
- `examples/*.json`:testset、chinese_plain 等个人/领域规则。
- `testdata/bash-regress.json`:一个**合法的 RuleFile**,含显式 `action:"deny"` 的回归规则(如 `regress-no-long-sleep`、`regress-git-commit`),计划 B1 的 14 个回归用例写成这些规则的 `examples`(`git -C ../x commit -m m` 用 `bash.git_sub: commit` 的规则断言 `hit`)。
- `docs/pinrule-mapping.md`:逐行引用 `/tmp/pinrule` 源码(文件:行号),按"DSL 等价 / 有意改变 / 舍弃"登记 8 项检查,并给出每个客户端的映射说明;其中 Codex 的 shell 读路径提取(`pinrule/backends/codex.py:576-612`)登记为"DSL 等价(经 `CommandNode.Reads`,覆盖面更宽)"。
- **U11 单元验收**(只依赖契约包):`go test ./internal/model -run 'TestContentFilesDecode|TestSchemaExamplesDecode'` 通过(全部内容文件严格解码、id 合规且不重复、每条规则至少一个 hit 与一个非命中示例、message/inject 长度);人工核对默认包注入文本合计 ≤ 8000 字。
- **U12 集成验收**(需要 U1/U2/U3/U4/U6 的真实实现与完整二进制):全部文件 `ballast lint --client all` 无错误、`ballast test-rules` 全通过(含 codex apply_patch 示例)、`ballast test-rules testdata/bash-regress.json` 全通过、默认包全文注入 ≤ 8000 字(`inject` 实测)。

**U12 CLI 与集成**:

- §7 全部命令与退出码;`--help` 无副作用;`hook` 分支最先分派、不做多余初始化。
- 验收脚本(参数固定,失败即非 0,可在 macOS/Linux/Windows Git Bash 运行):`scripts/accept-m1.sh`(go test -race、lint、test-rules、regress、fault、concurrency、hotreload、bench)、`accept-m2.sh`(freshness、matrix stub、commit bench)、`accept-m3.sh`(audit lint、stats golden、replay)、`accept-m4.sh`(install-e2e,每个客户端)。
- `scripts/bench/`:由 `proto/m0c/bench` 移植,11 个场景,预热 20、正式 300、p95 取 `ceil(0.95n)`,门槛 30ms。
- **集成验收清单**(从各单元移交):U3 的"rules/、examples/ 全部 lint 与 test-rules 通过";U8 的 `commitform.json` 端到端(`cmd` 列经 bashparse 取 git 节点 → `ParseCommitForm`,与 `want` 一致)与 `scripts/freshness.sh`(真实 pre-commit hook 调 `ballast verify --check`);U7 的 bench p95 ≤ 30ms;U11 的 lint/test-rules;hook 端到端:Codex `exec_command` 执行 `sed -n 1,80p a.go` 后对 `a.go` 的 `apply_patch` 不命中 `read-before-edit`。
- `.github/workflows/ci.yml`:ubuntu、macos、windows 三平台;`go vet ./...`、`go test ./... -race`、`internal/archtest`、accept 脚本、bench 门槛;Windows 跑 PowerShell 回归。
- v1.3 新增接线项:`replay.Deps.Project` 注入 `project.Loader`(否则 replay 退回宽松解码);退出码映射可继续用 `install.Err*`(已是 `model` 同值别名)。
- 合并 U1–U11 后删除 `internal/cli/wire.go` 中的 `wiringSurface`,换成真实接线;`proto/` 在 M1 结束时按计划删除(由用户决定时机)。

---

## 9. 热路径预算与性能约束

- **门槛**:B10 的 11 个场景 p95 ≤ 30ms,三平台(macOS arm64、Linux、Windows)都设门槛。M0c 原型最差 p95 17.13ms(繁忙机器);进程创建 + Go 运行时初始化约占 3–5ms。
- **热路径包**(`archtest.hotPath`):model、ports、sys、layout、version、paths、bashparse、psparse、dsl、config、inject、state、audit、adapter/*、hook、snapshot、evidence、project。
  - 不得依赖 `net/*`(传递闭包也不行)。
  - 不得 import `os/exec`,**唯一例外** `internal/snapshot/gitfallback.go`(受 deadline 约束)。hook、dsl、adapter、state、audit、config、inject、paths、bashparse、psparse 传递闭包也不得含 `os/exec`。
  - 不得在包级变量中 `regexp.MustCompile`(惰性编译);不做重的 `init()`。**这两条适用于 cmd/ballast 链接进来的全部包**(`./internal/...` 与 `./cmd/...`,含 install、stats、replay、runner),因为每次 hook 调用都会执行所有包的初始化;`internal/archtest` 的 `TestNoHeavyPackageInit` 用 AST 检查包级 var 初始化中的 `regexp.(Must)Compile(POSIX)` 并禁止 `func init()`(白名单 `initAllowed`,当前为空)。
  - 时间、随机数、环境变量、文件系统一律经 `sys.Clock`/`sys.IDGen`/`sys.Env`/`sys.FS` 注入。
- **分项预算**(进程内,p50 目标):读 stdin + 解析 ≤ 0.3ms;规则加载(缓存命中)≤ 0.5ms;冷编译 12 条 ≤ 1ms;状态无锁读 ≤ 0.2ms;求值(不含快照)≤ 0.5ms;100KB bash 解析 ≤ 3ms;原生 C 快照 10k 文件 ≤ 5ms;状态事务 ≤ 1ms(锁等待上限 20ms);审计写 ≤ 0.3ms。
- **看门狗** 150ms;昂贵谓词(快照退回 git)的 deadline = 启动时刻 + 120ms。退回 git 超时 → unknown + 审计 diag。
- 原生快照的回退条件以 `internal/snapshot/doc.go` 为准;v1.3 把子模块相关条件(HEAD 无法原生解析、git 目录缺 `objects/`/`refs/`、gitlink 被非目录替换、已跟踪文件被含 `.git` 的目录替换)和 racy 判定中的 smudged 规则(索引中 size 为 0 且 id 不是空 blob)写入契约。
- 状态文件**不 fsync**;evidence、manifest、备份 **fsync**(不在热路径)。
- gob 规则缓存可选(M0c:12 条规则只省约 0.3ms);热更新所需的 stat 检测必须保留。
- 输出协议:stdout 只写一次;GC 只在 SessionStart 以 detached 子进程触发,每天至多一次(`gc.stamp`)。

---

## 10. 错误处理约定

| 层 | 约定 |
|---|---|
| **hook(U7)** | 唯一的"放行决策者"。任何返回的 error、panic、看门狗 → stdout 空、stderr `ballast: <msg>`、exit 1。只有 stdout 成功写出后才 exit 0。 |
| hook 降级的审计频率(v1.3) | `ErrNotRepo` 每会话只在 SessionStart 写一次 `diag: not_a_repo`;其他 Locate 错误按无仓库降级并写 `diag: repo_locate_failed`;配置 notice 只在注入实际展示它时写审计;GC 派生或写 stamp 失败在提交后补写 `diag: gc_spawn_failed`(第二次 Append,失败忽略)。详见 `internal/hook/doc.go` |
| 必须向 hook 返回 error(= 放行)的情形 | stdin 非 JSON 对象、未知客户端/事件、`Rules.Load` 的意外 I/O 错误、`State.Commit` 的非超时错误(磁盘满等)、`Audit.Append` 任何错误(含短写)、`Render` 错误、内部不变量被破坏。 |
| **不得**作为 error 返回(内部降级 + 审计 diag) | 不在 git 仓库(`ErrNotRepo`);状态锁超时(`LockTimeout`);状态文件损坏(重置);状态无锁读失败(相关谓词 unknown);规则文件非法(LKG/空层 + `config_invalid`);缓存/LKG 写失败(stderr 诊断);谓词 unknown;快照退回超时;Spawner 失败;能力降级。 |
| dsl(U3) | `Evaluate` 不得 panic;内部错误转为该规则 unknown 并在 `UnknownCause` 说明。`ParseFile` 只在输入无法解码时返回 error,其余为 Diagnostic。 |
| adapter(U6) | 可选字段缺失或类型不符一律忽略,不报错(M0a 结论 6);只有非对象输入与未知事件报错。 |
| config(U4) | 规则问题不报错(见上);只返回意外 I/O 错误。 |
| state/audit(U5) | 锁超时不报错;其余 I/O 错误如实返回。 |
| snapshot/evidence(U8) | `ErrFallback`、`ErrDeadline`、`ErrNotRepo` 用 `errors.Is` 区分;View 方法不返回 error,而以 `FreshResult.Cause` 表达。 |
| 非热路径命令(CLI) | 返回 error,由 U12 映射为 §7 的退出码;不吞错(卸载加载失败必须非 0)。 |
| 哨兵错误 | 跨单元共享的哨兵统一在 `model/errors.go`,各单元用 `%w` 包装。v1.3 澄清:非热路径命令包(install、stats、replay)自身的用法/闸门类哨兵允许是包级变量,由 CLI 按包变量 `errors.Is` 映射退出码,但必须在包内集中定义并写明退出码;install 的 5 个哨兵已移入 model(`ErrInstallUsage`、`ErrClientNotDetected`、`ErrBinaryUnstable`、`ErrOrgWriteDenied`、`ErrInstallLocked`),包内原名保留为同值别名,调用方无需修改。 |

边界声明(写入用户文档):stdout 一旦提交,决策即生效;之后进程即使崩溃也不撤销。磁盘写满会使强制失效(风险 6),审计与 doctor 告警。

---

## 11. 测试约定

- **隔离**:所有触及文件系统的测试必须使用 `testenv.New(t)`(单元测试,`sys.MapEnv`)或 `testenv.Isolate(t)`(e2e,设置进程环境)。沙箱重定向 `HOME`、`USERPROFILE`、`BALLAST_HOME`、`BALLAST_INSTALL_ROOT`、`BALLAST_MANAGED_DIR`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME`、`XDG_CONFIG_HOME`、`GIT_CONFIG_GLOBAL`、`GIT_CONFIG_NOSYSTEM`。
- **禁止**读写真实 `~/.ballast`、`~/.claude`、`~/.codex`、`~/.cursor`、`~/.hermes` 及系统托管目录;e2e 只在沙箱里运行 `ballast`。
- **`go test ./...` 不得联网**(archtest 以 `GOPROXY=off` 调用 go list)。需要 git 的测试用本地 `git init` 夹具,缺少 git 时 `t.Skip`;CI 中缺工具链判为失败(B3 矩阵约定)。
- 时间与随机数用 `sys.FakeClock`、`sys.SeqID`;状态/审计的并发测试用独立进程(`-race` 只是附加)。
- 每个包的 `testdata/*.json` 期望表是契约的一部分:实现者必须让它们通过,可以追加行,不得删改既有行(改行 = 契约变更)。
- 故障注入只在 `-tags ballast_fault` 构建中经 `BALLAST_FAULT=<point>:<mode>` 生效;生产构建忽略该变量。
- `internal/archtest` 在每个单元的 CI 中运行;`internal/model` 的测试校验 `schema/examples/*` 与 `docs/dsl-semantics.md` 中的 JSON 示例可被严格解码。

---

## 12. 契约变更流程

1. 只允许**追加**(新字段带 `omitempty`、新常量、新 testdata 行)时,提供方单元可直接追加,并在合并说明中列出。
2. 改名、删字段、改语义、改 JSON 标签、改导出签名、新增内部依赖:必须先修改本文/`dsl-semantics.md` 与 `archtest` 白名单,由架构师(U0)批准,再通知所有受影响单元。
3. `schema/rules.v1.json` 冻结;不兼容变更需要 `rules.v2`。

---

## 13. 与计划的差异与补充决定

| 项 | 决定 | 理由 |
|---|---|---|
| v0.4 的 Adapter 接口(含 Detect/InstallTargets/Transcript/Doctor) | 按单元所有权拆分:运行时协议 → `ports.Adapter`(U6);Detect/InstallTargets/Doctor → `install.Client`(U10);Transcript → `replay.TranscriptReader`(U9) | 避免 U6/U9/U10 争用同一文件,接口总和不变 |
| `bash`/`ps` 两个变体 | 同一条规则可同时写 `bash` 与 `ps`,按工具的 shell 种类分派(不是 AND) | 默认 5 条规则各带两个变体而不占用 12 条上限 |
| `path` 对 shell 工具 | Bash/PowerShell 的"目标路径" = 解析出的写入目标 | `no-tamper` 一条规则覆盖文件工具与 shell |
| `input_regex` 虚拟字段 | `new_text` / `old_text` 读 `ToolCall.Edits` | 跨 Edit/MultiEdit/Write/NotebookEdit/apply_patch 统一写入内容 |
| `text.source` 扩展 | 增加 `tool_response`(PostToolUse)、`error`(PostToolUseFailure) | 原型 `secret-in-output` 需要;字段适用性表约束 |
| `agent_type` 匹配、`clients` 字段 | 新增 | M0a:规则可按 agent_type 过滤;能力差异需要按客户端启用 |
| Generation 计算 | `config.Generation`,去掉 examples/description 后的规范 JSON | examples 不改变行为,不应触发变更摘要 |
| 运行期能力降级 | 降为 log + `diag: capability_degraded` | lint 拒绝在前;运行期显式降级而非静默 |
| 审计轮转 | 不重命名,按后缀递增创建新文件 | 并发写者无竞态 |
| verify `--status` 扩展码 7 | superseded | 计划未定义该结果的码 |
| 故障注入 | 仅 `ballast_fault` 构建标签 | 防止 Agent 通过环境变量关闭强制 |
| Hermes `on_session_end` | 归一为 SessionEnd(不是 Stop) | 无法干预停止,且不带最终文本 |
| Cursor `afterAgentResponse` | 归一为 AgentResponse,仅存最终文本供后续 `stop` 规则使用 | Cursor stop payload 可能缺 `last_assistant_message` |
| shell 读取目标 | `CommandNode.Reads`(bashparse/psparse 提取),Bash/PowerShell 的 PostToolUse 把非动态目标并入已读集合 | Codex 没有 Read 工具;否则 `read-before-edit` 在 Codex 上对每个已存在文件的 `apply_patch` 都命中(审核 major 1) |
| `verify --check` 与 `GIT_INDEX_FILE` | 热路径忽略;`--check --commit` 由 CLI 读取并经 `CheckOptions.IndexFile` 传入 | pre-commit 阶段 git 指向将要提交的索引,`commit -a`/带路径提交才能判对(审核 major 2) |
| ConfigChange 审计 hook(计划 M4 内容列) | 归入 P2,本期不实现,也不在安装事件列表中 | M0a 没抓过该事件的 payload(字段【未核验】);T0 下 `no-tamper` 已能检测对 settings 的改写,ConfigChange 只是更晚的第二信号;补抓 payload 后再做(计划 v0.4 变更 §4) |
| 卸载不关闭 `[features] hooks = true`(§5.2) | 维持;计划 M4"往返 diff 为空"改为"除 `features.hooks` 外为空",`install-e2e.sh` 对"原文件没有 hooks 键"的夹具显式断言该残留 | 同一 `hooks.json` 里可能有别的工具的 hook 依赖该开关,关掉会静默失效;manifest 里的 `set_by_ballast` 只用于提示 |
| 提交作者邮箱 | 发布前由仓库所有者决定是否改写历史(`git filter-repo --mailmap` → noreply 邮箱,并设置仓库级 `user.email`) | 历史里的工作邮箱公开后无法撤回;发布动作不由实现任务代做 |
| `git add && git commit` 同一命令 | 不并入 commit 形式,列为已知局限并在 message 中提示 | 模拟前序节点副作用会让 FreshQuery 依赖整条 CommandList,收益小、风险大 |

---

## 14. 修订记录

### v1.4(2026-10-16,第 1 轮系统审计修复)

| 审计项 | 级别 | 处理 | 涉及文件 |
|---|---|---|---|
| 备份文件名随绝对路径变长,深目录下 `ENAMETOOLONG` | major | 备份文件名改为 `<sha256(路径)前 16 位>-<basename 至多 64 字节>`,目录内 `index.json` 记录映射,`BackupRecord.Files` 仍记原路径;`accept-m4.sh` 加深 `TMPDIR` 回归 | `internal/install/backup.go`、§3.1、`scripts/accept-m4.sh`、`scripts/install-e2e.sh` |
| `cache/rules-*.json` 无界增长 | minor | `stat.json` 重建时删除不再被引用的规则缓存;`ballast gc` 清理 7 天未引用的缓存文件(`config.GCCache`) | `internal/config/cache.go`、`internal/config/cache_gc.go`、`internal/cli/cmd_misc.go`、§3.1 |
| SessionEnd 后残留 `.lock` 与会话目录 | minor | `DropSession` 在持锁状态下尽力删除已无状态文件的锁文件与空目录;加锁后复核 inode(`os.SameFile`),避免孤儿锁分裂;`DropAgent` 仍不删锁文件;这是对 v1.3"永不删锁文件"的收窄修订 | `internal/state/store.go`、`ports.StateStore` 注释、§3.1 |
| replay 对带 `path_exists`/`verify` 的规则一律 approximate | minor | 分类改为按实际求值路径(matcher 不命中 → known-miss;`path_exists` 仅在回退磁盘时 approximate),先改 `internal/replay/doc.go` 契约再改代码,补单测与端到端用例 | `internal/replay/doc.go`、`analyze.go`、`run.go` |
| M0a 缺"中断"用例 | minor | 登记为 `unverifiable` 并说明理由,加入 `m0-check` 必需列表 | `docs/facts/`、`scripts/tools/accutil/m0check.go` |
| 计划 M4 的 ConfigChange 未登记 | minor | 归入 P2,见 §13 | §13、计划 v0.4 变更 §4 |
| Codex 往返残留 `features.hooks` | minor | 维持契约,同步计划措辞并加显式 e2e 断言,见 §13 | `scripts/install-e2e.sh`、计划 v0.4 变更 §4 |
| M3 真实 replay 与 ≥ 20 条标注未执行 | minor | 依赖用户本机操作,步骤写入计划与 `scripts/README.md`;保持"未核验" | 计划 v0.4 变更 §4、`scripts/README.md` |
| 提交作者邮箱 | minor | 发布前决定,见 §13 | — |

导出签名:未修改已有导出签名;追加 `config.GCCache`、`config.CacheTTL`。

### v1.3(2026-10-16,U1–U11 实现后的契约跟进)

各单元在实现与审核中提出的契约问题、已知缺口与审核 minor,逐条裁决如下(采纳 = 本版落实;延后 = 记录在案、本版不改;不采纳 = 维持原契约)。`internal/cli`、`cmd/ballast` 不在本次范围内。

| 来源 | 条目 | 裁决 | 处理 / 涉及文件 |
|---|---|---|---|
| U1 契约 | §5.2 先词法折叠 `..` 与内核物理语义不符 | 采纳 | dsl-semantics §5.2 写明 POSIX 候选含物理 `..` 形式;`internal/paths/doc.go` |
| U1 契约 | `paths.CompileGlobs(GlobOptions)` 无法表达 `~` 双重展开 | 采纳(文档) | 其他单元只经 `ports.PathService` 的 `Service.CompileGlobs`;包级函数是实现辅助;`internal/paths/doc.go` |
| U1 缺口 | 物理父目录解析只在 OSFS 验证;Service 缓存假定布局稳定 | 延后 / 接受 | 等 Windows CI;单进程内成立 |
| U1 minor | `physicalDotDot` 把盘符路径拼到 cwd | 采纳 | `internal/paths/paths.go` 加 `isDriveAbs` 判断,`candidates_test.go` 补用例 |
| 多个单元 | `GOOS=windows go vet` 在 `proto/m0c/hook` 失败 | 采纳 | `proto/m0c/hook/*.go` 加 `//go:build !windows`(原型保留) |
| U2 缺口 | 100KB 解析约 2.4ms,高于 M0c 目标 2ms | 延后 | 满足 <5ms 验收;优化留给 bench 阶段 |
| U2 minor | `builtin cd` 不被视为切换目录 | 采纳 | 先改 `model.ShellReadPaths` 冻结规则文字,再实现;`shellreads_test.go` 补用例;dsl-semantics §14 |
| U3 契约 | Bash 无写入目标时的绑定、B/U 是否适用于无 `when` 的规则 | 采纳 | 无 shell 匹配器、无 `path` 时以 `when` 是否引用 `$target`(`usesTarget`)门控解析;dsl-semantics §9 |
| U3 契约 | 07 示例 hermes PostToolUse warn | 采纳 | `schema/examples/07` 的 `secret-in-output` 加 `clients` |
| U3 契约 | `input-field-unknown` 与 `field-not-applicable` 措辞 | 采纳(文档) | 以 §11 为准,允许同时报两者;`internal/dsl/doc.go` |
| U3 契约 | `…(+N)` 中 N 的含义 | 采纳(文档) | N = 未完整显示的命中行数;dsl-semantics §10.1 |
| U3 契约 | 文本源缺失为 unknown 还是 false | 采纳 | 按 §5.5 为 false;仅状态不可读时 unknown;`internal/dsl/eval.go` |
| U3 契约 | `TestCase/TestReport.Skipped` | 采纳 | 作为追加字段写入 `internal/dsl/doc.go` |
| U3 缺口 | `strip_quotes` 不跨行配对 | 接受 | dsl-semantics §5.5 写明 |
| U3 minor | `verify_fresh` 绑定节点含无匹配器的写入目标绑定 | 采纳 | `binding.matcher` 字段;`cond.go` 只认匹配器绑定;`predicates_test.go` |
| U3 minor | `len(p.when) > 0` 门控 | 采纳 | 编译期 `program.usesTarget`;`matchers_test.go` |
| U4 缺口 | 同 generation 不同仓库互相覆盖 `rules-<generation>.json` | 采纳 | 文件名改为 `rules-<generation>-<key[:8]>.json`(§3.1);`internal/config/cache.go` |
| U4 缺口 | 不可用层不缓存、`fitCut` 省略 omitted 行 | 接受 | 维持实现 |
| U4 minor | `degrade_test.go` 注释与实现不符 | 采纳 | 改注释 |
| U5 契约 | `Matched` 由谁裁剪 | 采纳(文档) | `audit.Writer` 兜底脱敏与裁剪;`model/audit.go` 注释 |
| U5 契约 | `ClipRunes` 语义 | 采纳(核对) | 结果 ≤ n rune、省略号计入;hook 本地 `clipRunes` 语义一致 |
| U5 缺口 | Windows 锁只做了 vet | 延后 | 等 Windows CI |
| U5 minor | DropAgent/DropSession 删除锁文件的竞态 | 采纳 | 只在持锁时删状态文件,永不删锁文件,锁超时留给 GC;`internal/state/store.go`、`ports.StateStore` 注释、§3.1 |
| U6 契约 | InstallTargets/Transcript/Doctor 未在 adapter 实现 | 不采纳(维持 §13 拆分) | — |
| U6 契约 | PreCompact/PostCompact/SubagentStart 不在表中 | 采纳 | `NativeEventTable[claude-code]` 增加三行 `Install=false`,删除 claudecode 私有白名单;§4.3 |
| U6 契约 | Hermes post_tool_call 只能 log | 接受 | 与 §4.1 一致 |
| U6 缺口 | Codex/Cursor/Hermes 字段未取证、Cursor `loop_count` 推断、heredoc 形式的 apply_patch | 延后 | 【未核验】项随 doctor/取证跟进 |
| U6 minor | apply_patch 标记行首空白/多空格 | 采纳 | `normMarker`;`testdata/patches.json` 追加 3 行 |
| U6 minor | `shell` argv `["apply_patch", envelope]` 漏判 | 采纳(契约变更) | 归一为 Edit(§4.4);`testdata/parse.json` 追加 3 行 |
| U6 minor | 非法 JSON 写入 `ToolCall.Input` 导致无法序列化 | 采纳 | 只在 `json.Valid` 时设置 |
| U6 minor | `FlattenStrings` 32 层截断 | 采纳 | 去掉截断,路径改为线性构造 |
| U7 契约 | `ShellReadPaths` 桩 | 采纳 | U2 已合并;删除 hook 测试中的替身 |
| U7 契约 | `Readable`/`Exists` 语义 | 采纳(核对) | 缺失文件 = Readable、!Exists、nil error;Readable=false 当且仅当 err≠nil;`ports.StateStore.Load`、`model.StateLoadInfo` 注释 |
| U7 契约 | `FlushedEvals` 分工、`RuleDigest` 与 Generation 关系 | 采纳(核对) | RuleDigest 非 nil 当且仅当事件可注入;两者同时记录;`model.Injection` 注释、`internal/hook/doc.go` |
| U7 契约 | Spawner 的 env/logPath、gc.stamp | 采纳(核对) | env 为完整环境(nil = 继承),logPath 空 = 空设备;stamp 只由 hook 维护;`ports.Spawner`、§3.1 |
| U7 契约 | hook 本地脱敏、notice 审计频率、阻断决策不渲染注入、job 回报窗口、Stop 文本回退 | 采纳(文档) | 写入 `internal/hook/doc.go` |
| U7 契约 / minor | `ErrNotRepo` 每会话一次 diag | 采纳 | §10、`internal/hook/doc.go` |
| U7(核对发现) | Cursor `afterAgentResponse` 文本保留头部,与 state "保留最后 4000 字"不一致 | 采纳 | hook 改为保留尾部;`model.StateDelta` 注释 |
| U7 缺口 | bench、真实 ports 端到端 | 延后 | U12 集成验收 |
| U7 minor | Spawner 失败未审计 | 采纳 | `diag: gc_spawn_failed`,派生失败回滚 stamp;`lifecycle_test.go` |
| U7 minor | transcriptRef 大小写敏感 | 采纳 | windows/darwin 上前缀比较忽略大小写 |
| U8a 契约 | 回退条件、smudged 规则、`Options.Env` | 采纳 | `internal/snapshot/doc.go` 契约段;§9 |
| U8a 缺口 | sha256、reftable 子模块 | 接受 | 回退 git |
| U8a minor | 损坏子模块仍给出原生 gitlink id | 采纳 | `submoduleHead` 要求 `objects/`、`refs/`;`TestDamagedSubmoduleFallsBack` |
| U8b 契约 | `JobStatus.Deadline` 注释 | 采纳 | `timeout_s + JobDeadlineSlack` |
| U8b 契约 | runner 自带锁与 state 锁重复 | 延后 | 将来在 `internal/sys` 提供锁原语后合并 |
| U8b 契约 | 动态 `-m` 值放行(DEVIATION) | 采纳 | 值类选项的动态值不使 commit 形式不可解析(Claude 常用 `-m "$(cat <<EOF…)"`);`internal/evidence/doc.go` |
| U8b 缺口 | PID 复用、timeout 改小漏判、无锁 runner | 接受 | 均为保守一侧,已写入包文档 |
| U8b minor | supersede 测试未断言持锁 | 采纳 | `detached_test.go` |
| U9 缺口 | 不跳过 compact agent 的 SubagentStop | 延后 | 缺 transcript 取证 |
| U9 缺口 | 无 project.json 一律 unknown;按本机 project.json 判定 | 接受 | — |
| U9 minor | 主流程看不到之后回放的子 agent 写入 | 采纳 | 回放前预扫描子 agent 文件写入;`TestRunMainEventSeesEarlierSubagentWrites` |
| U9 minor | 回放端宽松解码 project.json | 采纳 | 新增可选 `replay.Deps.Project`(U12 接线,见 §8.3) |
| U10 契约 | 哨兵错误位置 | 采纳 | 移入 `model/errors.go`,install 保留同值别名;§10 |
| U10 契约 | manifest 不记录创建的目录 | 采纳(以结构标记实现) | `dir:kept` 等 `ManagedFlag`,未新增 `ManagedFile` 字段;§5 |
| U10 缺口 | 锁清理残余窗口、YAML 深层检查、python3 校验 | 接受 | — |
| U10 minor | Cursor `version:1` 卸载后残留 | 采纳 | `json:version` 标记,无 hooks 时卸载删除;§5.3 |
| U10 minor | Hermes 卸载删掉用户原有表头/注释/空事件键 | 采纳 | `yaml:hooks`、`yaml:hooks.<event>` 标记,卸载按原文还原 |
| U10 minor | 用户原有空 `~/.cursor/rules` 被删 | 采纳 | `dir:kept`;`TestUninstallKeepsPreexistingCursorRulesDir` |
| U11 缺口 | lint/test-rules 待 U12 | 延后 | 本次用真实引擎临时核对:全部内容文件 300 个示例通过 |
| U11 minor | `no-hack-commit-message` 缺 ps 变体 | 采纳 | 补 ps 变体与 PowerShell 示例 |
| U11 minor | 映射文档子串匹配示例错误、表格 `\|` 未转义 | 采纳 | `docs/pinrule-mapping.md` |

导出签名:未修改任何已有导出签名;只做了追加(`model` 新哨兵与 `NativeEventTable` 行、`replay.Deps.Project` 字段)。

### v1.2(2026-10-16,第二轮审核 minor)

- 新增纯函数 `model.ShellReadPaths`、`model.IsAbsShellPath`(签名冻结,U2 实现),hook 与 replay 共用,避免 cd 规则与绝对路径判定各写一套;shell 读取登记不看退出码(dsl-semantics §14)。

### v1.1(2026-10-16,独立审核修订)

| 审核项 | 级别 | 处理 | 涉及文件 |
|---|---|---|---|
| Codex 读文件只能经 shell,read_this_session 缺来源 | major | 采用方案 (1):`model.CommandNode.Reads []WriteTarget`;bashparse/psparse 冻结提取规则并补期望行;hook 在 Bash/PowerShell PostToolUse 把非动态 Reads 规范化后并入 `StateDelta.ReadPaths`(含 `cd` 规则);replay 同规则 | `internal/model/command.go`、`internal/bashparse/doc.go`、`internal/psparse/doc.go`、两个 `expect.json`、`internal/hook/doc.go`、`internal/hook/testdata/pipeline.json`、`internal/replay/doc.go`、dsl-semantics §6/§7/§14 |
| `verify --check` 忽略 GIT_INDEX_FILE 与 p3 矛盾 | major | `evidence.Check(opt, cwd, CheckOptions{Mode, IndexFile})`;CLI 从环境解析;freshness.json 注释写明 check 列在真实 pre-commit 环境下取得、案例 7 的 cwd 为 ../other;热路径仍忽略 | `internal/evidence/evidence.go`、`internal/evidence/doc.go`、`internal/evidence/testdata/freshness.json`、`internal/model/evidence.go`、`internal/snapshot/doc.go`、§7 |
| commitform.json 依赖 bashparse | minor | 每行增加 `git`(GitInvocation),U8 直接构造;`cmd` 列留给 U12 端到端 | `internal/evidence/testdata/commitform.json` |
| U3/U11 验收不可在单元内完成 | minor | 划分单元验收与 "U12 集成验收";U3 用假 Adapter/Path/Bash 跑 claude-code 示例 + tri.json;U11 新增 `TestContentFilesDecode` | `internal/dsl/doc.go`、§8.3、`internal/model/model_test.go` |
| replay 白名单与"复用 BuildDelta"不符 | minor | 方案 (a):删除复用说法,replay 自建内存 SessionState,`--initial-state` 格式在 replay 包冻结 | §8.2、`internal/hook/hook.go`、`internal/replay/doc.go`、`internal/replay/replay.go` |
| evidence 缺 ProjectLoader,Open 与惰性加载冲突 | minor | `evidence.Options.Project`;`EvidenceProvider.Open(repo, deadline)` 不做 I/O;-C 跨仓库用目标仓库 project.json;新增 Cause `project_invalid`;`hook.Deps.Project` 删除 | `internal/ports/ports.go`、`internal/evidence/*`、`internal/hook/hook.go`、`internal/model/evidence.go` |
| dsl 需要 doublestar | minor | dsl 允许直接 import doublestar;名称 glob 区分大小写,路径 glob 走 PathService | `internal/dsl/doc.go`、§2.2、dsl-semantics §5 |
| 包级 regexp/init 检查只覆盖热路径 | minor | 扩大到 `./internal/...` 与 `./cmd/...`,AST 检查,并禁止 `func init()` | `internal/archtest/archtest_test.go`、`internal/archtest/doc.go`、§9 |
| doctor 缺 MDC 输入 | minor | `DoctorOptions.MDC []byte` | `internal/install/doctor.go` |
| `git add -A && git commit` 必判过期 | minor | 列为已知局限,默认规则 message 提示分两次执行,schema 示例补一行 | dsl-semantics §13.3/§14、`schema/examples/03-evidence-before-commit.json`、`internal/evidence/doc.go` |
| repo_remote_hash 无提供方 | minor | `RepoInfo.RemoteHash`,Locate 顺带扫描 config;hook 审计取该字段 | `internal/model/evidence.go`、`internal/snapshot/doc.go`、`internal/hook/doc.go` |
| 锁超时后仍写 eval_summary 会重复统计 | minor | Commit 返回 LockTimeout 或 Duplicate 时不写 eval_summary;pipeline.json 补行 | `internal/hook/doc.go`、`internal/model/state.go`、`internal/hook/testdata/pipeline.json` |
