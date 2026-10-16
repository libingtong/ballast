# Ballast P2 设计与契约增补(冻结版 v1.0)

> 状态:**已冻结**(2026-10-16)。本文是 `docs/architecture.md` **v2.0** 的配套文档,与计划 `docs/ballast-plan.md` 末尾的 "v0.5 P2 计划" 一起,构成 P2 各实现单元唯一可依赖的契约。
>
> 依据:计划 v0.4 及 v0.4.1–v0.4.3 修订;`docs/architecture.md` v1.8;`docs/dsl-semantics.md`;`docs/trust-model.md`;`docs/clients.md`;`docs/facts/`(M0a、M0c);`docs/research/pinrule-install-and-clients.md`;现有代码 `internal/*`、`cmd/ballast`、`scripts/*`。
>
> 本次只冻结设计与 Go 签名骨架(函数体 `panic("unimplemented: …")`),不含实现。骨架已通过 `go build ./...`、`go vet ./...`、`GOOS=windows go vet ./...`、`go test ./internal/archtest`,且全量 `go test ./...` 仍为绿。

## 0. 标注约定

| 标注 | 含义 |
|---|---|
| 【未核验】 | 只有文档或推断依据,Ballast 没有自己取证。实现单元要么取证,要么维持该标注并让 `doctor`/文档如实提示 |
| 【需凭据】 | 取证需要真实模型调用。没有用户**明确提供**的凭据就不做,标为 `needs_credentials`,并给出"一条命令补测"。实现单元**不得**设法获取凭据(不读 keychain、`~/.claude*`、`~/.codex` 的 auth 文件,不复制任何 token) |
| 【静态依据】 | 读了客户端源码或官方文档得出,没有运行取证 |
| 【决定】 | 本文作出的设计裁决,实现必须遵守;要改走 architecture §12 |

**全局禁区**(每个单元都适用,验收时逐条检查):

1. 热路径(`archtest.hotPath`)不调 LLM、不联网、不 `os/exec`(唯一例外仍是 `internal/snapshot/gitfallback.go`);hook 唯一会 spawn 的进程是 Ballast 自身。
2. 不做规则蒸馏或检索。
3. install/uninstall 外科式、先备份(既有契约不变)。
4. 个人偏好不进默认规则。
5. **绝不修改真实用户配置**:`~/.claude*`、`~/.codex`、`~/.cursor`、`~/.hermes`、`~/.ballast`、系统托管路径。所有脚本与测试一律重定向到临时目录或一次性容器;托管路径只在容器里写。
6. 不把任何凭据(OAuth token、API key、keychain 内容)复制进容器、镜像层、仓库或日志。
7. 公开仓库不得出现本机绝对路径或隐私内容;取证样本入库前规范化(`<repo>`、`<home>`、`claude-<uid>`),CI 卫生检查已覆盖这些模式。

---

## 1. 范围

| 工作包 | 本文章节 | 计划出处 |
|---|---|---|
| M0b 托管层容器取证 | §6 | 计划 M0 行、A13–A15、A20 |
| T1 沙箱化 runner 与 hook 代起 verify | §3 | 计划 B0 |
| P2-a 脱敏与可选上报 | §4 | 计划 B8、P2-a 行 |
| M4 边界:ConfigChange 审计、boundary-t0/t1 | §5 | 计划 M4 行、v0.4.1 |
| 客户端取证(Hermes、Codex、Cursor) | §7 | 计划 v0.4 §1、clients.md §6 |
| 其他增强(少量) | §8 | architecture §14 延后项 |

**非目标**:网络上报的实现(§4.5 只给约束);原生 Windows 的 T1(永远 T0);MCP 写入的强制(只能靠白名单,残余风险不变);对 Codex、Cursor 的托管层支持。

---

## 2. 新包、依赖层次与契约追加

### 2.1 新包

| 包 | 层 | 热路径 | 单元 | 职责 |
|---|---|---|---|---|
| `internal/redact` | L0(只用标准库) | 是 | P2-REDACT | 统一脱敏器:hook 状态环、audit 写入、report 共用 |
| `internal/tier` | L2(model、sys、layout) | 是(经 `ports.TierProbe`,惰性) | P2-TIER | T0/T1 判定;受保护路径的唯一来源 |
| `internal/sandbox` | L2(model、sys) | 否(可用 `os/exec`) | P2-SANDBOX | runner 测试命令的 OS 沙箱包装(sandbox-exec / bwrap) |
| `internal/report` | L4(model、ports、sys、layout、redact、stats) | 否 | P2-REPORT | `ballast report --out` 的聚合与隐私校验 |

### 2.2 依赖层次(architecture §2.2 的增量,archtest 已同步)

```
L0  model  sys  version  redact(新,仅标准库)
L1  ports  layout  testenv
L2  … tier(新)  sandbox(新)
L3  audit(+redact)  …
L4  hook(+redact)  runner(+tier,+sandbox)  install(+tier)  report(新)  …
L5  cli
```

- **hook 直接 import `redact`** 是对"hook 只依赖契约包"的唯一放宽。【决定】理由:`redact` 是只用标准库的纯函数叶子包(L0),与 `model` 同级;改用 ports 注入会让每个 hook 假实现都要提供脱敏器,且状态环脱敏不应是"可选能力"。`tier` 不放宽:hook 经 `ports.TierProbe` 使用,便于测试伪造 T1。
- `sandbox` 只允许 `runner` import;hook 的传递闭包里不可能出现它(allowlist 保证)。
- `install` 用 `tier.ProtectedPaths` 与 `tier.Detect`,不 import `sandbox`;`doctor` 的沙箱可用性检查由 CLI 通过 `install.DoctorOptions` 的函数字段注入(与 `MDC` 同样的做法),字段名由 P2-INSTALL 追加。

archtest 的变化(`internal/archtest/archtest_test.go`):

- `allowed` 新增 `redact`、`tier`、`sandbox`、`report` 四行;`audit`、`hook` 增加 `redact`;`runner` 增加 `tier`、`sandbox`;`install` 增加 `tier`。
- `hotPath` 与 `noExecTransitive` 增加 `redact`、`tier`。
- **新增 `TestBinaryNoNet`**:`go list -deps ./cmd/ballast` 不得含 `net`/`net/*`。这比"热路径不联网"更严:整个 ballast 二进制都不链接网络栈(当前已满足),同时把 `net/http` 的包初始化挡在每次 hook 调用之外。

### 2.3 契约追加清单(全部是追加,已在骨架中落地)

| 位置 | 追加 | 用途 |
|---|---|---|
| `model/tier.go`(新) | `SandboxPolicy`(require/prefer/off)、`TierMissing*` 常量、`TierInfo` | 层级判定结果 |
| `model/verifyreq.go`(新) | `VerifyRequest`、`ParseVerifyRequest`、`VerifyOrigin`、`VerifyRequestFile`、`RequestedJob`、`HookStartRequest`、`HookStartArgv`、`ParseHookStartArgv` | hook 代起 verify 的识别与子进程参数 |
| `model/evidence.go` | `VerifyRecord.Sandbox`、`Origin`、`TierMissing`(omitempty) | 证据标注沙箱与来源 |
| `model/client.go` | `EventConfigChange`;Claude 表新增 `ConfigChange` 行(`Install:false, Verified:false`) | ConfigChange 审计 |
| `model/audit.go` | `AuditConfigChange = "config_change"` | 审计行类型 |
| `ports/ports.go` | `TierProbe{ Probe() model.TierInfo }` | hook/runner 取层级 |
| `layout/layout.go` | `OrgConfig`(`<托管目录>/ballast/org.json`)、`RequestsDir`(`~/.ballast/requests`) | 新文件位置 |
| `hook.Deps` | `Tier ports.TierProbe`(nil ⇒ T0) | §3.3 |
| `runner.Options` | `Tier ports.TierProbe`(nil ⇒ T0、SandboxOff) | §3.4 |
| `runner/t1.go`(新) | `StartRequest(opt, model.HookStartRequest)` | §3.3 |
| `internal/{redact,tier,sandbox,report}` | 见各包 `doc` 注释(契约段即验收要点) | — |

`schema/rules.v1.json` **不变**:层级配置不进规则文件,而是独立的 root 所有文件 `org.json`(§3.2)。

---

## 3. T1:层级判定、hook 代起 verify、沙箱化 runner(计划 B0)

### 3.1 威胁模型回顾

T1 的目标:agent 不能写规则、状态、证据和二进制。T1 由三层叠加:

1. Claude 托管设置:`allowManagedHooksOnly`(低层 hook 失效)、`sandbox`(Bash 子进程 denyWrite)、`permissions.deny`(文件工具)、MCP 白名单。
2. root 所有的 Ballast 二进制,托管 hook 用绝对路径调用。
3. **verify 的测试命令本身**也可能写受保护路径:由沙箱化 runner 防住(本节)。

agent 在 Bash 沙箱里跑的 `ballast verify` 写不了 `~/.ballast`,所以 T1 下由 hook(在沙箱外)代起 runner。hook 运行在沙箱外这一点是文档依据(A20)【需凭据,M0b case `hooks-run-outside-sandbox`】。

### 3.2 层级判定(`internal/tier`)

**【决定】T1 当且仅当以下五条同时成立**,否则为 T0,并在 `TierInfo.Missing` 中列出每一条不成立的原因(常量见 `model/tier.go`):

| # | 条件 | Missing id |
|---|---|---|
| 1 | GOOS 为 darwin 或 linux(WSL2 报告 linux) | `os` |
| 2 | 环境中没有 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT`(测试重定向变量;agent 可控的环境绝不能让判定去读 agent 拥有的文件) | `env_override` |
| 3 | `Self`(解析符号链接后)是 uid 0 拥有、组和其他用户不可写的普通文件,且直到 `/` 的每一级父目录同样 root 所有、组和其他不可写 | `binary_root_owned` |
| 4 | `<托管目录>/ballast/org.json` 是 root 所有、组和其他不可写的普通文件,且 `"tier":"T1"` | `org_config` |
| 5 | 合并后的 Claude 托管设置(每个文件 root 所有、组和其他不可写;合并顺序以 M0b `managed-d-merge-*` 的结论为准)满足:`allowManagedHooksOnly==true`;存在 `command==Self` 且 `args[0:2]==["hook","--client"]` 的 hook 条目;`sandbox.enabled==true`、`allowUnsandboxedCommands==false`、`failIfUnavailable==true`;`sandbox.filesystem.denyWrite` 覆盖 `ProtectedPaths` 的每一项;`permissions.deny` 对每一项都有 Edit/Write/NotebookEdit 规则;`allowManagedPermissionRulesOnly==true` | `managed_settings`、`managed_hooks`、`allow_managed_hooks_only`、`sandbox`、`sandbox_deny_write`、`permissions_deny`、`managed_permissions_only` |

任何输入读不了或解析失败 ⇒ T0 + `unreadable`(失败时取保守一侧)。

**为什么 agent 伪造不了**:条件 3–5 要求写 root 所有的文件;拥有 root 的 agent 不在任何 Ballast 边界之内(trust-model 声明)。agent 能控制的只有自己 Bash 子进程的环境,而 hook 的环境来自 Claude Code 进程;即便 agent 设法带着 `BALLAST_HOME` 调用 hook,条件 2 也会让判定落到 T0。**判定只决定"路由"和"证据上写什么 tier";证据的 tier 由 runner 自己再判定一次**(§3.4),hook 判断失误不会让证据变成 T1。

**`org.json`**(新文件,`layout.OrgConfig`,管理员下发,`org render` 生成):

```json
{"v": 1, "tier": "T1", "runner_sandbox": "require", "protected_extra": ["~/.config/acme/ci-token"]}
```

`Declared`(org.json 声明)为 T1 而判定为 T0 时:`doctor` 报 fail(列出 Missing);hook 遇到规范 verify 时写审计 `diag: tier_mismatch`。只有 root 所有的 org.json 才被采信。

**受保护路径**:`tier.ProtectedPaths` 是唯一来源,`org render`、runner 沙箱、条件 5 的覆盖检查都调用它。顺序固定:org 规则目录、`~/.ballast/`、`<repo>/.ballast/`、二进制目录、`~/.claude/settings*.json`(遵循 `CLAUDE_CONFIG_DIR`)、`<repo>/.claude/settings*.json`、`protected_extra`。每项同时给出原始路径与 `EvalSymlinks` 形式(macOS 的 `/var` → `/private/var`),消费方两种都保护。

**性能**:hook 只在"规范 verify 命令"时调用 `Probe`(极少);一次判定约 10 次 stat 加读一个几 KB 的 JSON,目标 ≤ 200µs(`BenchmarkProbe`)。`Prober` 按进程记忆。

### 3.3 hook 代起 verify

**识别**:`model.ParseVerifyRequest(cl)`,规则冻结在函数注释中。要点:恰好一个节点,没有管道、重定向、包装器、`sudo`、循环、后台 `&`、命令替换、动态词;`basename(argv[0])` 为 `ballast`/`ballast.exe`;`argv[1]=="verify"`;其余每个词匹配 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`(所以任何 flag 都不是规范形式,`--status`、`--check`、`--foreground` 照常执行),最多 16 个名字。

**流程**(只对 `claude-code` 的 PreToolUse、工具为 Bash 或 PowerShell;其他客户端 `MaxTier` 为 T0,不调用 Probe):

1. `ParseVerifyRequest` 不成立 ⇒ 什么都不做,照常求值。
2. 成立 ⇒ `Deps.Tier.Probe()`;nil 或 T0 ⇒ 照常求值(T0 下 agent 自己运行 `ballast verify`,现有行为)。若 `Declared==T1` 而结果为 T0,写 `diag: tier_mismatch`。
3. T1 ⇒
   - 用 `sys.Clock` + `sys.IDGen` 预分配 `request_id`(job id 格式);
   - `argv = model.HookStartArgv(Self, HookStartRequest{RequestID, Cwd: 事件 cwd, Names, Client, SessionID, AgentID})`;
   - `Spawner.SpawnDetached(argv, env, logPath)`,`logPath = <RequestsDir>/<repo_inst>/<request_id>.spawn.log`(不在仓库时 repo_inst 用既有的 `norepo-…` 作用域);
   - 形成一个**合成的 deny 决策**(不是规则命中),与同事件的规则结果按既有裁决合并(deny 最严;其他规则的 reason 按顺序拼接,受预算约束);
   - 审计一行 `diag: verify_hook_start`,detail `{request_id, names, spawn_ok}`;
   - deny reason 模板(冻结):`ballast(T1):已在沙箱外后台启动 verify,job=<request_id>(<名字列表或"全部 required">)。不要重复执行;用 \`ballast verify --status <request_id>\` 查看结果,完成后结果也会在下一次注入中回报。`
4. spawn 失败 ⇒ 仍然 deny(在沙箱里跑也写不了证据),reason 改为"无法在后台启动 verify:<错误>;请让用户在沙箱外执行 `ballast verify <names>`",审计 `diag: verify_spawn_failed`。spawn 失败不是 hook 异常(architecture §10)。

这条链路**不依赖 `updatedInput`**(A24)。子进程是 `ballast verify --hook-start …`,即 Ballast 自身;沙箱包装发生在 runner 进程内部,hook 不 spawn 任何别的东西(热路径约束)。

**子进程侧**(P2-CLI 接线):`verify --hook-start` 用 `model.ParseHookStartArgv` 解析参数,调用 `runner.StartRequest`。它写 `VerifyRequestFile`(`~/.ballast/requests/<repo_inst>/<request_id>.json`):第一个(或唯一一个)job 的 id 就是 `request_id`,其余 job 用新 id;所有记录 `Origin:"hook"`。配置错误(无仓库、无 project.json、未知名字)也写请求文件并带 `error`,这样 `ballast verify --status <request_id>` 能解释原因。`--status` 找不到同名 job 时回退读请求文件:单个 job 时退出码同该 job;多个 job 时取最差结果(fail > timeout > signal > infra > running > pass),输出逐行列出。

**结果回报**:沿用 M2 的"下一次注入回报已完成 job"(按 repo_inst 读 evidence),不需要改动。`evidence-before-commit` 遇到运行中的 job 仍返回 unknown 加提示(v0.4.2)。

**【决定】新鲜度不按 tier 过滤**:T1 主机上只有沙箱外的进程能写证据(hook 代起的 runner,或用户在终端里手动运行),所以 T0 记录在 T1 主机上出现只可能来自人;tier 用于审计与 CI 判读,`verify_fresh` 语义不变。

**攻击面对照**:

| agent 的尝试 | T1 下的结果 |
|---|---|
| 在 `~/.claude/settings.json` 加自己的 hook,或 `disableAllHooks` | `allowManagedHooksOnly` 使低层 hook 不执行,低层不能关闭托管 hook【M0b 验证】;settings 本身在 denyWrite / permissions.deny 中 |
| 在 `PATH` 前面放伪造的 `ballast` | 托管 hook 用绝对路径;hook 代起时用 `Self`,不用 agent 的 argv[0] |
| 在沙箱里跑 `ballast verify --foreground test` | 写不了 evidence,结果为 infra |
| 直接写 evidence / state / rules | Bash:denyWrite;文件工具:permissions.deny;MCP:白名单(残余风险) |
| 带着 `BALLAST_HOME` 调用 hook | 判定条件 2 ⇒ T0,不代起,不产生 T1 证据 |
| 改测试代码让它"恰好通过" | 内容快照把改动纳入 `S_end`/`C`;提交里能看到测试被改,由 CI 与评审把关(trust-model §3,本设计不改变) |
| 测试命令启动后台进程稍后写受保护路径 | macOS:沙箱继承给全部后代;Linux:bwrap `--unshare-pid --die-with-parent`,runner 结束即全部终止 |

### 3.4 沙箱化 runner(`internal/sandbox` + `internal/runner`)

**runner 进程本身在沙箱外**(它要写 evidence、job 文件与日志),只把**测试命令**包进沙箱;日志经继承的文件描述符写入,不受沙箱路径规则影响。

**后端**:

| 平台 | 后端 | 方式 |
|---|---|---|
| macOS | `/usr/bin/sandbox-exec` | `sandbox-exec -p <profile> -- argv`;profile = `(version 1)(allow default)` + 每个受保护路径的 `(deny file-write* (subpath P))`/`(literal P)`/`(regex …)`,原始与解析后两种形式都写。SBPL 按路径匹配,对尚不存在的路径同样生效 |
| Linux | bubblewrap | `bwrap --die-with-parent --unshare-pid --bind / / --dev /dev --proc /proc` + 对每个**已存在**的受保护路径 `--ro-bind-try P P` + `--chdir <cwd> -- argv`。尚不存在的受保护路径无法 bind(会在宿主上创建挂载点),runner 改为**事后检查**:运行后若它出现了,结果记 infra、error `protected_path_created` |
| 原生 Windows 及其他 | 无 | 永远 T0 |

**探测**:`sandbox.Probe` 用平凡命令实跑一次后端,并验证"对一个临时拒写目录的写入确实失败",结果按进程缓存。Linux 上常见的不可用原因:未安装 bwrap;内核或 AppArmor 禁止非特权 user namespace(Ubuntu 24.04 默认 `kernel.apparmor_restrict_unprivileged_userns=1`)【未核验:CI 镜像】;Docker 默认 seccomp 禁止 `unshare`/`clone(CLONE_NEWUSER)`,容器内需要 `--security-opt seccomp=unconfined --security-opt apparmor=unconfined`(不使用 `--privileged`)【未核验,M0b case `runner-bwrap-in-docker`】。

**策略与退化**(【决定】):

| 判定 | 策略(org.json `runner_sandbox`) | 后端可用 | 行为 | 记录 |
|---|---|---|---|---|
| T1 | `require`(T1 默认) | 是 | 包装后执行 | `tier:"T1"`,`sandbox:"bwrap"`/`"sandbox-exec"` |
| T1 | `require` | 否 | **不执行测试命令**,outcome `infra`,error `sandbox_unavailable`,审计 diag | `tier:"T0"`,`sandbox:"none"`,`tier_missing:["runner_sandbox"]` |
| T1 | `prefer` | 否 | 不包装执行 | `tier:"T0"`,`sandbox:"none"`,`tier_missing:["runner_sandbox"]` |
| T1 | `off` | — | 不包装执行 | `tier:"T0"`,`sandbox:"none"` |
| T0 | 默认 `off` | — | 现有行为 | `tier:"T0"` |
| T0(root 所有的 org.json 写了 `prefer`/`require`) | 同左 | 是 | 包装后执行(纵深防御) | 仍是 `tier:"T0"`,`sandbox` 如实记录 |

**`VerifyRecord.Tier == "T1"` 当且仅当 runner 自己判定为 T1 且 `Sandbox != "none"`。** runner 的判定是证据 tier 的唯一权威,不采信 hook 传来的任何层级信息(`HookStartRequest` 里也没有这个字段)。

**沙箱不改变的东西**:网络不限制;受保护路径之外的写入不限制(测试需要临时文件);`.git` 不在受保护路径中(测试里的 `git add` 会改索引,进而改变 `C`,这是可见的,不是绕过)。

### 3.5 `org render` 与 `doctor` 的变化(P2-INSTALL)

- `org render` 的 denyWrite 与 permissions.deny 改由 `tier.ProtectedPaths` 生成,新增 `<repo>/.claude/settings*.json`(此前需要管理员手工追加)。Claude 沙箱路径里 `<repo>` 的写法(相对路径、`./`、glob 是否支持)【需凭据,M0b case `managed-denywrite-path-forms`】;在此之前保留现在的占位写法并在输出里注释。
- 输出增加 `org.json` 段;`--write --i-am-admin`(root)同时写 `managed-settings.d/ballast.json` 与 `<托管目录>/ballast/org.json`(0644,root)。
- ConfigChange 行在 `Install` 翻为 true 之后才出现在托管 hook 中。
- MCP 白名单键名(`allowManagedMcpServersOnly`、`allowedMcpServers`,当前以 `omitempty` 出现在模板结构里)【未核验,M0b case `mcp-allowlist-nocred`】。
- `doctor`:`tier.level` 改为调用 `tier.Detect`,逐条列出 Missing;新增 `tier.declared_mismatch`(org.json 声明 T1 而判定为 T0 ⇒ fail)与 `tier.runner_sandbox`(T1 + `require` 且后端不可用 ⇒ fail;沙箱探测函数由 CLI 注入)。

### 3.6 验收(T1 部分)

| 单元 | 可执行验收 |
|---|---|
| P2-TIER | `go test ./internal/tier`:`testdata/detect.json` 逐条翻转五个条件(每次只翻一条 ⇒ T0 且 Missing 恰好是该 id);全部满足 ⇒ T1;用计数 FS 断言 `Probe` 第二次调用零 I/O;`BenchmarkProbe` ≤ 200µs |
| P2-SANDBOX | `go test ./internal/sandbox`:profile/bwrap 参数 golden(`testdata/profiles.json`,含空格、引号、括号、非 ASCII 路径);darwin 上有 sandbox-exec 时集成测试:`sh -c 'echo x > $P/f'` 被拒、写允许的临时目录成功;linux 上有 bwrap 时同样,缺 bwrap 本地 SKIP,`CI=true BALLAST_REQUIRE_BWRAP=1` 时 FAIL |
| P2-RUNNER | `go test ./internal/runner`:用假 `TierProbe` 与假沙箱后端覆盖 §3.4 全表;`StartRequest` 首个 job id == request_id、请求文件内容、配置错误也写请求文件;Linux 事后检查 `protected_path_created`;真实后端可用时的端到端(测试命令写受保护路径失败、记录 `tier:"T1"`) |
| P2-HOOK | `go test ./internal/hook`:`pipeline.json` 追加行——规范 verify + 假 T1 ⇒ deny 且 reason 含 request_id、Spawner 收到 `HookStartArgv`;T0 ⇒ 无特殊处理;非规范形式(管道、`--status`、`env X=1 ballast verify`、动态词)⇒ 无特殊处理;spawn 失败 ⇒ deny + `verify_spawn_failed`;`ParseVerifyRequest` 与 `HookStartArgv`/`ParseHookStartArgv` 往返的表驱动测试与 fuzz |
| P2-CLI | bench 新场景 `12`:假 T1 下的规范 verify PreToolUse p95 ≤ 30ms(用 `BALLAST_TEST_TIER` 之类只在 `ballast_fault` 构建标签下生效的开关,生产构建忽略) |
| 边界 | `scripts/boundary-t1.sh`(§5.3) |

---

## 4. P2-a:脱敏与可选上报

### 4.1 依赖层次方案

现状有两套脱敏:`internal/audit/redact.go`(正则 + 前缀预筛,较宽)与 `internal/hook/redact.go`(按词扫描,较窄,因为 hook 不能 import audit)。【决定】新建 L0 包 `internal/redact`(只用标准库),hook 与 audit 都直接 import 它(§2.2)。迁移:

- `audit.Redact` 保留为一行包装 `return redact.String(s)`,签名不变,调用方不改。
- `internal/hook/redact.go` 删除,`summaryText` 改调 `redact.String`。状态环与审计从此是同一套规则,`internal/hook/doc.go` 中"状态环是唯一保留较窄脱敏的地方"一句随之删除。
- `report` 对每个输出字符串再过一遍 `redact.String`,再经 `report.Validate` 自检。

### 4.2 检测类别

`redact.String` 只替换**值**,保留键名、`Bearer ` 前缀、URL 的协议与主机,使审计行仍然可读。类别是两套旧实现的并集,再加若干新增:

| Kind | 覆盖 | 来源 |
|---|---|---|
| `private_key` | PEM 私钥块(被截断的块到文本末尾为止) | audit |
| `url_password` | `scheme://user:PASSWORD@host`(含 git 远程地址) | 两者 |
| `auth_header` | `Authorization:` / `Proxy-Authorization:` 任意方案 | audit + 新增 Proxy |
| `bearer` | 头部之外的 `Bearer`/`Basic` 值 | 两者 |
| `key_value` | 键名含 password、passwd、passphrase、secret、token、api_key/apikey/api-key、access_key、private_key、credential、client_secret,或整键等于 pwd、pass、auth 的 `=`、`:`、`"k": "v"` 形式(含 `export K=V`、Azure 连接串的 `AccountKey=`) | 两者 + 新增 |
| `flag` | `--password X`、`--token X`、`--api-key X`、`--secret X` | audit |
| `aws` | `AKIA`/`ASIA`/… 访问密钥 id | 两者 |
| `vendor_token` | GitHub `ghp_/gho_/ghu_/ghs_/ghr_/github_pat_`、GitLab `glpat-`、npm `npm_`、Slack `xox[abposr]-` 与 `hooks.slack.com/services/…`、OpenAI `sk-`/`sk-proj-`、Anthropic `sk-ant-`、Stripe `sk_live_/sk_test_/rk_live_/rk_test_`、Google `AIza…` 与 `ya29.`、Hugging Face `hf_`、PyPI `pypi-AgEI…`、Docker Hub `dckr_pat_`、DigitalOcean `dop_v1_`、SendGrid `SG.x.y` | 两者 + 新增 |
| `jwt` | `eyJ….….sig` | 两者 |
| `high_entropy` | 保留,默认关闭(git 哈希、uuid 误报太多) | — |

**不脱敏的值**(避免 `max_tokens: 4096` 这类误报):`key_value` 的值若是 ≤ 6 位纯数字或 `true/false/null`,保持原样。【决定】

**已知不覆盖**(写进包文档):`mysql -pPASSWORD` 这类紧贴短参数的写法(与普通 `-p` 无法区分);`.netrc` 的空格分隔 `password X`(只在同一行出现 `machine`/`login` 时覆盖,实现单元可选);自定义名称的环境变量(`FOO=<值>`)。

### 4.3 样本与 fuzz(验收)

- `internal/redact/testdata/samples.json`:≥ 50 条正样本(每个 Kind 至少 3 条,含多行、JSON 内嵌、shell 命令内嵌、URL 内嵌、CRLF、非 ASCII 上下文),每条给出植入的秘密与期望输出;≥ 20 条负样本必须逐字节不变(`token count`、`keyboard`、40 位 git 哈希、uuid、`task-1234567890abcdef`、`max_tokens: 4096`、`passport`、`authorize()`、普通 base64 图片片段等)。
- **零泄漏断言**:任何正样本经 `String` 后都不含植入的秘密子串。
- 旧实现的回归:`internal/audit/redact_test.go` 与 `internal/hook/redact_test.go` 的全部用例迁入 `samples.json`,结果只能更严(旧的被遮住的,新的必须也遮住)。
- **60 秒 fuzz**:`go test ./internal/redact -run '^$' -fuzz FuzzString -fuzztime 60s`。性质:不 panic;幂等;`!MayContainSecret(s) ⇒ String(s)==s`;按 Kind 生成器在随机文本中间(两侧用分隔符)植入秘密,输出不含该秘密;输出长度不超过输入长度加上植入的每个秘密带来的增量上限(Mask 比任何受检秘密都短)。
- 性能:`BenchmarkClean4K`(4 KiB 无秘密文本)≤ 10µs/op;首次编译全部正则的一次性开销计入 hook bench(B10 门槛不变)。

### 4.4 `ballast report --out <file>`(本地文件)

- **默认关闭**:只有用户显式运行该命令才产生文件;不在热路径;不联网。
- 输入只有审计日志(`ports.AuditSource`);输出结构见 `internal/report.Report`:窗口、版本、GOOS、会话数与仓库数(只计数)、各客户端与各 tier 的记录数、按规则聚合(与 `ballast stats` 同语义)、诊断计数(名称是闭合枚举)、verify 结果按 outcome/tier/sandbox/origin 计数、hook 延迟直方图与 p95、按 Kind 的脱敏命中计数、损坏行数。
- **隐私契约**(冻结,写在包注释里):绝不输出 session/agent/tool_use id、repo_inst、remote hash、transcript 引用、路径、matched 文本、上下文摘要、reason、message、prompt、主机名、用户名;非内置规则的 id 输出为 `h:` + sha256 前 12 位;时间截断到小时/天。
- 命令与退出码(P2-CLI 接线,architecture §7 追加):`report --out <file> [--since 30d] [--force]`。0 已写入;1 隐私自检失败(什么都不写,这是 bug);2 用法错误(缺 `--out`、`--since` 非法);4 目标文件已存在且未给 `--force`;5 读审计或写文件失败。
- 验收:`go test ./internal/report`——夹具审计目录到 golden 报告;`Validate` 表驱动(植入路径、id、秘密均被拒);性质测试(随机审计记录里植入秘密与路径,序列化后的报告都不含);CLI e2e 覆盖全部退出码。

### 4.5 网络上报(不在 P2 实现,只冻结约束)

若将来要做,必须同时满足:

1. **单独的二进制**(例如 `cmd/ballast-upload`),不链接进 `ballast`;`TestBinaryNoNet` 保证 `ballast` 永不链接 `net/*`,hook 依赖图自然也没有。
2. 只读取 `ballast report` 生成的文件,不读审计日志。
3. 没有编译进去的默认 endpoint;`--endpoint https://…` 必须显式给出,并且 `--confirm-endpoint` 必须**逐字重复同一个值**(二次确认参数);只允许 HTTPS。
4. 发送前在终端打印完整载荷并要求输入 `y`;没有 TTY 时拒绝(不提供 `--yes`)。
5. 上传内容 = 报告文件原样,再过一次 `report.Validate`。

---

## 5. M4 边界

### 5.1 ConfigChange 审计 hook

- **来源事实**:A16——ConfigChange 只覆盖 Claude 设置与 skills,部分托管刷新不触发。payload 字段【未核验】,由 M0b case `configchange-payload` 取得(§6.4 的"挂起端点"方法,无需凭据)。
- **归一**:`NativeEventTable[claude-code]` 已新增 `ConfigChange → EventConfigChange`,`Roles: [audit]`,`Install:false, Verified:false`。规则不能引用。
- **hook 行为**(P2-HOOK):解析 payload(字段缺失或类型不符一律忽略);写一行 `type:"config_change"` 审计,detail = `{source, file(按 transcript_ref 的规则改写成 "~/" 或 "<repo>/" 相对形式,不在两者之下时只留 basename), protected(经 PathService 判断是否命中受保护路径)}`;**stdout 永远为空**(【决定】只审计、绝不阻断:阻断配置变更可能把用户锁在外面);不读写会话状态(便宜、不争锁)。任何异常照 B7:stdout 空、exit 1。
- **安装**(P2-CC + P2-INSTALL):M0b 取到 payload 且 `configchange-payload` 为 pass 之后,P2-CC 把该行改为 `Install:true, Verified:true` 并补 claudecode 适配器的解析与 golden;P2-INSTALL 更新 `internal/install/testdata/golden.json`、`install-e2e.sh` 的事件数与 `org render`。条目不带 matcher。没取到 payload 时保持 `Install:false`,在 M0b 结论里写明原因。
- bench 增加 ConfigChange 场景,p95 ≤ 30ms。

### 5.2 `scripts/boundary-t0.sh`(三平台,不需要模型,不需要容器)

- 沙箱化环境(`lib.sh` 的重定向与 `guard_sandbox`);构建 ballast;`ballast install --client claude-code`(临时 `CLAUDE_CONFIG_DIR`);默认规则复制到用户规则文件;建一个临时 git 仓库(带 `.ballast/project.json`)。
- 夹具 `scripts/boundary/t0-cases.json`:每行 `{id, client, tool, input, target, expect: "detected"|"gap", via}`。
  - **detected**:覆盖面与 `docs/trust-model.md` §2.1 "识别的写入"逐项一致——重定向 `> >> &> >|`;`tee`;`cp`/`mv`/`install`/`rsync` 的目标;`sed -i`;`perl -i`/`ruby -i`(含 `-pi`、`-pi.bak`);`git checkout [<rev>] -- <path>`、`git restore <path>`;`rm`、`truncate`、`dd of=`、`touch`、`ln`;PowerShell `Set-Content`/`Add-Content`/`Out-File`/`Remove-Item`/`Copy-Item -Destination`/`Move-Item -Destination`/`New-Item` 与 `>`;文件工具 Edit/MultiEdit/Write/NotebookEdit;Codex `apply_patch`(Update/Add/Delete/Move)、Cursor `Write`、Hermes `write_file`。目标覆盖 `~/.ballast/rules.json`、`~/.ballast/evidence/…`、`<repo>/.ballast/project.json`、`~/.claude/settings.json`、org 规则目录(`BALLAST_MANAGED_DIR` 重定向)。
  - **gap**:§2.1 "未覆盖的写法" 1–7 类每类至少 2 例(`python -c`、`node -e`、`./fix.sh`、`curl … | sh`、`patch`、`git apply`、`git reset --hard`、`mv <受保护文件> /tmp/x` 的源、`rm -r ~/.ballast`、`eval "$CMD"`、MCP 工具)。
- 每行把合成 payload 喂给真实 `ballast hook`,断言:detected ⇒ 审计中有 `rule_id:"no-tamper"` 的 hit 或 `would_*` 行,且 hook 退出码 0;gap ⇒ 没有该行。**gap 行若被检测到,脚本以非 0 退出并提示"覆盖面变了,请同步更新 trust-model §2.1"**,使脚本同时是缺口登记表。
- 额外两项:手写一条伪造的 pass 记录(`S_end` 随意填)后 `ballast verify --check` 仍判过期(返回 10),因为新鲜要求 `S_end == C`;伪造者若先算出真实 `C` 再填进去则能骗过,这正是 T0"可检测、不可防"的边界,脚本把它记为 gap 行;ConfigChange 安装后,喂合成 ConfigChange payload(字段取自 M0b 样本)⇒ 审计有 `config_change` 行且 `protected:true`。
- `accept-p2.sh` 调用它;三平台 CI 执行。

### 5.3 `scripts/boundary-t1.sh`(Linux 容器)

镜像复用 M0b 的 Dockerfile(§6.2),外加:linux 版 ballast 由 root 安装到 `/usr/local/libexec/ballast/ballast`(0755,root);容器内以 root 执行 `ballast org render --binary … --write --i-am-admin`;之后全部以普通用户 `agent` 运行。容器参数:`--security-opt seccomp=unconfined --security-opt apparmor=unconfined`(bwrap 需要),`--network none`(只在构建镜像时联网)。

**不需要模型、做成可执行断言的部分**:

| id | 断言 |
|---|---|
| t1-doctor | `ballast doctor --json`:`tier.level` 为 T1,Missing 为空 |
| t1-runner-sandbox | 夹具仓库的 `project.json` 里 verify `test` 是一个脚本:依次尝试 `echo > ~/.ballast/evidence/x`、`sed -i` 改 `.ballast/rules.json`、`python3 -c` 写 `~/.ballast/rules.json`、`touch` 二进制目录、创建原本不存在的 `<repo>/.ballast/x`,每次写失败计 1;全部失败才 `exit 0`。把合成的 PreToolUse payload(`ballast verify test`)喂给托管 hook 命令 ⇒ stdout 为 deny 且含 job id;等待 job ⇒ 记录 `tier:"T1"`、`sandbox:"bwrap"`、`origin:"hook"`、outcome pass;受保护文件的哈希前后一致 |
| t1-forged-binary | 用户可写目录里的 ballast 副本作为 hook 命令 ⇒ `tier.Detect` 为 T0(`binary_root_owned`),不代起;该副本运行的 verify 记录为 T0 |
| t1-orgjson | org.json 删除 / 改为 agent 所有 / `tier:"T0"` ⇒ 各自 T0 + 对应 Missing;doctor 报 `tier.declared_mismatch` |
| t1-env-override | 带 `BALLAST_HOME` 调用 hook ⇒ T0(`env_override`) |
| t1-sandbox-missing | 去掉 bwrap 的镜像变体 ⇒ `require` 策略下 job outcome infra、error `sandbox_unavailable`、无 pass 证据;doctor `tier.runner_sandbox` 为 fail |
| t1-lower-hooks | **取决于 M0b `nocred-events`**:若无凭据时 SessionStart 能触发——用户层另装一个探针 hook 并设 `disableAllHooks:true`,运行 `claude -p`(假 key + 黑洞端点)⇒ 托管 ballast 的 SessionStart 审计行存在,用户探针没有被调用。若 M0b 证明不触发,该项标 needs_credentials |

**需要 agent 真正尝试的部分【需凭据】**:让 agent 用 Edit、`echo >`、`sed -i`、`python -c` 写受保护路径;伪造 `ballast` 放进 `PATH`;直接写 evidence;MCP 写入(按白名单配置验证)。期望全部失败。补测命令:

```sh
ANTHROPIC_API_KEY=<用户自己提供> scripts/boundary-t1.sh --with-credentials
```

脚本只从调用者的环境读这个变量,以 `docker run -e ANTHROPIC_API_KEY`(只传变量名)传入,不写入镜像层、文件或日志,不读取 keychain 或任何 `~/.claude*` 文件;未设置时这些用例输出 `needs_credentials` 并以 0 退出(其余断言照常)。可选的模拟模型路径见 §6.6。

---

## 6. M0b:托管层容器取证(`scripts/probe-m0b/`)

### 6.1 目标

实测 A13–A15、A20 与 `org render` 模板依赖的托管行为:`allowManagedHooksOnly` 是否阻止用户层/项目层/本地层 hook;低层 `disableAllHooks` 能否关闭托管 hook;托管 hook 与低层 hook 是否合并执行;`managed-settings.d` 的合并规则;托管 hook 的 command/args 路径要求;沙箱、permissions、MCP 的强制效果(后者【需凭据】)。

产物:`docs/facts/m0b-cases.json`、`docs/facts/M0b-CONCLUSIONS.md`、`docs/facts/payloads/m0b/<case>/`(脱敏、路径规范化),格式与 M0a 对齐(§6.5)。

### 6.2 容器

- `scripts/probe-m0b/Dockerfile`:`node:22-bookworm-slim`(以 digest 固定)+ `apt-get install bubblewrap socat jq python3 git ca-certificates`;`npm install -g @anthropic-ai/claude-code@<固定版本>`(与 M0a 同为 2.1.300,若 npm 上已不可得则取最近版本并在结论中记录;也可用官方安装脚本,二者择一并记录)。只有构建镜像需要联网。
- 用户 `agent`(uid 1000,非 root);托管文件 `/etc/claude-code/managed-settings.json`、`managed-settings.d/*.json` 由 root 写入、0644。
- 探针 hook `/opt/probe/probe.sh <label>`(root 所有):把 stdin 写到 `/tmp/probe/<label>-<event>-<ns>.json`,stdout 为空,exit 0。label 区分 `managed`、`managed-d10`、`managed-d20`、`user`、`project`、`local`。
- 运行参数:`--rm --network none`(运行期不联网,确保没有凭据外流,也没有真实模型请求),`--security-opt seccomp=unconfined --security-opt apparmor=unconfined`(只在沙箱相关用例上加),环境 `DISABLE_AUTOUPDATER=1 DISABLE_TELEMETRY=1 DISABLE_ERROR_REPORTING=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`。
- 每个用例一个全新容器,配置文件由 `scripts/probe-m0b/cases/<case>/` 下的夹具复制进去;宿主机上不写任何托管路径。

### 6.3 先核实:没有凭据时哪些事件会触发

这是其余无凭据用例成立的前提,作为第一个 case `nocred-events`,三种模式都跑:

| 模式 | 环境 | 预期(【未核验】) |
|---|---|---|
| `none` | 不给任何 key | 可能在启动认证阶段直接退出,SessionStart 是否已触发未知 |
| `blackhole` | `ANTHROPIC_API_KEY=dummy-not-a-credential`、`ANTHROPIC_BASE_URL=http://127.0.0.1:9`(拒绝连接) | SessionStart、UserPromptSubmit 在首个模型请求之前触发;随后请求失败 |
| `hang` | 同上,但端口上是一个只 accept 不应答的本地监听器(容器内 `python3 -c`,脚本自带) | 会话在等待模型响应期间保持存活 N 秒,用于 ConfigChange 与"会话中修改配置"类用例 |

`dummy-not-a-credential` 不是凭据:它不对应任何账户,只用于让客户端走到"发请求"那一步;运行期 `--network none` 保证它到不了任何外部服务。

**如果 SessionStart / UserPromptSubmit 在三种模式下都不触发**,如实记录(`result:"fail"`,observation 写清客户端在哪一步退出),把所有依赖事件触发的用例改为 `needs_credentials`,并给出补测命令(§6.5)。

### 6.4 用例

| case_id | 内容 | 需凭据 |
|---|---|---|
| `nocred-events` | §6.3 | 否 |
| `managed-only-blocks-lower` | 托管 `allowManagedHooksOnly:true`;托管、用户、项目、本地四层都配 SessionStart/UserPromptSubmit 探针 ⇒ 只有 managed 被调用 | 否 |
| `hooks-merge-all-layers` | 托管不设 `allowManagedHooksOnly` ⇒ 四层探针都被调用(A15),记录调用顺序 | 否 |
| `lower-disableallhooks-vs-managed` | 用户层、项目层、本地层分别设 `disableAllHooks:true` ⇒ 托管探针仍被调用(A14),该层自己的探针不被调用 | 否 |
| `managed-disableallhooks` | 托管 `disableAllHooks:true` ⇒ 没有任何探针被调用(对照组) | 否 |
| `managed-d-merge-hooks` | `managed-settings.json` 与 `managed-settings.d/10-a.json`、`20-b.json` 各配一个探针 ⇒ hook 数组是拼接还是替换、顺序 | 否 |
| `managed-d-merge-scalar` | 标量键(`allowManagedHooksOnly`)在基础文件与 `.d` 文件间冲突、在 10/20 间冲突 ⇒ 谁生效(按文件名顺序后者胜?基础文件与 `.d` 谁优先?) | 否 |
| `managed-hook-path-forms` | 托管 hook 的 command 分别为绝对路径、裸名(经 `PATH`)、`./相对路径`、`~/…`、`$HOME/…`、含空格的绝对路径 + `args` ⇒ 哪些被执行、按什么解析;agent 可写的 `PATH` 目录是否能劫持裸名 | 否 |
| `managed-file-owner` | 托管文件属于 `agent` 用户(非 root)⇒ Claude 是否仍读取(Ballast 的层级判定自己检查属主,此项只为记录客户端行为) | 否 |
| `sandbox-failifunavailable` | 去掉 bwrap 的镜像变体 + `sandbox.enabled:true, failIfUnavailable:true` ⇒ 客户端是否在启动阶段拒绝运行(可能无需模型即可观察) | 否 |
| `mcp-allowlist-nocred` | 用户层配置一个不在托管白名单里的 MCP server ⇒ `claude mcp list` 的输出(白名单键名核对) | 否 |
| `configchange-payload` | `hang` 模式下会话存活期间改写用户 `settings.json` ⇒ ConfigChange 探针的 payload 字段 | 否 |
| `runner-bwrap-in-docker` | 不经 Claude:在容器里直接 `bwrap --ro-bind …` 跑写入测试,确定所需的 `--security-opt` | 否 |
| `hooks-run-outside-sandbox` | 托管 hook 写一个 denyWrite 路径 ⇒ 成功(hook 在沙箱外) | **是** |
| `sandbox-denywrite-bash` | agent 用 Bash `echo >`、`sed -i`、`python3 -c` 写 denyWrite 路径 ⇒ 失败 | **是** |
| `permissions-deny-edit` | agent 用 Edit/Write 写受保护路径 ⇒ 被拒 | **是** |
| `unsandboxed-refused` | `allowUnsandboxedCommands:false` 时 agent 请求 `dangerouslyDisableSandbox` ⇒ 被拒 | **是** |
| `managed-denywrite-path-forms` | denyWrite 中 `~/`、相对路径、glob 写法的实际效果 | **是** |
| `mcp-write` | 白名单内一个会写文件的 MCP server 写受保护路径 ⇒ 成功(记录残余风险);白名单外的 server 不可用 | **是** |

### 6.5 产物格式与检查

`m0b-cases.json` 是数组,元素字段与 `cases.json` 对齐:`case_id`、`events`、`schema_keys`、`assertion`、`result`、`observation`、`evidence`,另加:

- `result` 取值扩展为 `pass | fail | unverifiable | needs_credentials`(`fail` 表示文档说法被推翻,是有效发现,不是脚本错误);
- `mode`:`none | blackhole | hang | credentials | mock`;
- `claude_version`;
- `recheck`:`needs_credentials` 时必填,是一条可直接运行的补测命令。

`M0b-CONCLUSIONS.md` 按 M0a 的结构:对 A13/A14/A15/A16/A20 的逐条结论表、对设计的影响(特别是 `tier` 条件 5 的合并顺序、`org render` 的路径写法)、仍未核验清单。

检查器 `scripts/probe-m0b/check/`(独立 `main` 包,不在 archtest 范围内):每个必需 case 都在,字段齐全,`needs_credentials` 必带 `recheck`,payload 目录无本机路径与秘密(调用卫生检查的同一组模式)。

补测(用户明确提供凭据时,一条命令):

```sh
ANTHROPIC_API_KEY=<用户自己提供> scripts/probe-m0b/run.sh --with-credentials --cases hooks-run-outside-sandbox,sandbox-denywrite-bash,permissions-deny-edit,unsandboxed-refused,managed-denywrite-path-forms,mcp-write
```

凭据处理同 §5.3:只从调用者环境读,`docker run -e` 只传变量名,运行期改用 `--network bridge`(需要访问 API),不落盘、不进日志;脚本拒绝读取 keychain 与 `~/.claude*`。补测结果把对应行改写为 `mode:"credentials"`。

### 6.6 可选:模拟模型(不需要凭据)【未核验:可行性】

沙箱、permissions 与 MCP 白名单都由**客户端**执行,模型只负责发出 tool_use。因此可以在容器里起一个按脚本应答的本地 Messages API 桩(`ANTHROPIC_BASE_URL` 指向它,key 为 dummy),让"模型"发出指定的 Bash/Edit/MCP 调用,从而在无凭据下观察客户端的强制效果。风险:客户端可能请求桩没有实现的端点(计数、标题生成等),流式格式需要逐字节兼容。结果一律标 `mode:"mock"`,**不替代** `credentials` 模式的结论,只作为"强制在客户端侧生效"的佐证;做不通就记录原因放弃,不影响 M0b 完成。

### 6.7 验收

`bash scripts/probe-m0b/run.sh`(本机需要 Docker;无 Docker 时显式 SKIP,`CI=true BALLAST_REQUIRE_DOCKER=1` 时失败)跑完全部无凭据用例并生成产物;`go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json` 通过;`trust-model.md` 中所有"未核验,待 M0b"都改写为结论或 `needs_credentials` 引用(由 P2-CLI 在集成时同步文档)。

---

## 7. 客户端取证

### 7.1 Hermes

**本机事实(本次核查)**:

- `hermes hooks test <event> [--for-tool T] [--payload-file F]` 存在:它用 Hermes 自带的合成 kwargs(可用 `--payload-file` 合并覆盖),经**生产同一个序列化函数**送到配置里的 shell hook,并打印 stdout/stderr、退出码以及"解析后的 Hermes wire 形状"。它不检查 hook 同意(consent)名单,所以无需 `--accept-hooks`。【静态依据:本机 Hermes 源码,提交 9a0a162536(2026-09-27)】
- **本机 `hermes` 在 `HERMES_HOME` 重定向到空目录时启动即失败**(`ModuleNotFoundError: No module named 'ruamel'`):依赖环境按 `$HERMES_HOME/installs/<key>` 解析,空目录里没有。所以不能直接在重定向的 HERMES_HOME 里运行本机 Hermes。

**【静态依据】协议事实**(从同一份源码读出,待运行取证确认):

| 项 | 源码所示 | 与 adapter/hermes 现有假设对比 |
|---|---|---|
| stdin 形状 | `{"hook_event_name","tool_name","tool_input"(取自 args,非对象时为 null),"session_id"(回退 parent_session_id),"cwd","profile","extra":{其余 kwargs}}` | 一致(`extra.user_message`、`extra.is_first_turn`、`extra.result`) |
| 阻断事件 | 只有 `pre_tool_call` | 一致 |
| pre_tool_call 输出 | 接受 Hermes 方言 `{"action":"block","message"}` 与 Claude 方言 `{"decision":"block","reason"}`;`action:"modify"` 改参数;`action:"approve"` 转人工审批 | Ballast 输出 Claude 方言,被接受 |
| 退出码 | 阻断事件 exit 2 ⇒ 阻断;其他非 0 退出**仍解析 stdout**;spawn 错误/超时默认放行,`fail_closed` 时阻断 | 与 Claude 的 A7 同理:Ballast 的"异常 = exit 1 + 空 stdout"依然放行;安装器**绝不能**写 `fail_closed` |
| 注入 | 非阻断事件默认按 `{"context": …}` 解析 | 一致(只用于 pre_llm_call) |
| 工具名 | `terminal`、`read_file`、`write_file`、**`patch`**、`search_files`、`execute_code` | **不一致**:adapter 映射的是 `patch_file`/`edit_file`,漏了 `patch`;`execute_code`(执行 Python 代码)没有归一 |
| 额外事件 | `pre_verify`(本回合改过代码、即将结束时触发;返回 `{"decision":"block","reason"}` 或 `{"action":"continue","message"}` 让 agent 继续,受 `agent.max_verify_nudges` 限制)、`subagent_stop`、`on_session_finalize`、`post_llm_call` 等 | Ballast 认为 Hermes 没有 Stop 族;`pre_verify` 实际上是"续跑"语义的 Stop |

**运行取证方案**(P2-HERMES,`scripts/probe-clients/hermes.sh`):

1. **隔离运行 Hermes**,二选一并在结论中记录:(a)Docker:用本机 Hermes 源码的只读副本(`git -C <源码目录> archive HEAD`)作为构建上下文,在镜像内安装依赖(构建期联网),容器内 `HERMES_HOME=/work/hh`;(b)临时 venv:把源码副本装进 `mktemp -d` 下的 `uv venv`(只装基础依赖)。**禁止**:符号链接或克隆真实的 `~/.hermes/installs`(Python 可能回写字节码与状态)、复制 `auth.json`/`config.yaml`/任何凭据。`hooks test` 不调用模型,不需要凭据。
2. `BALLAST_HOME`、`HERMES_HOME` 都在临时目录;`ballast install --client hermes`;`hermes hooks list` 记录同意状态。
3. 用例(每个都同时装一个只记录 stdin 的探针 hook,与 Ballast 并列,用于抓 payload):
   - `pre_tool_call` + `terminal` 执行 `sleep 300`,显式 deny 测试规则 ⇒ parsed 为 block,message 为规则 reason;
   - `pre_tool_call` + `write_file` 写 `~/.ballast/rules.json`(deny 版 no-tamper)⇒ block;
   - `pre_tool_call` + `patch`(replace 模式与 V4A 补丁模式各一)⇒ 记录当前 adapter 的实际行为(预计漏判,作为修正依据);
   - `pre_llm_call`,`is_first_turn:true` ⇒ parsed `{"context": 全文注入}`;第二次 ⇒ 锚点;
   - `post_tool_call` ⇒ parsed 为空,审计有记录;`on_session_start`/`on_session_end` ⇒ 空;
   - `pre_verify` ⇒ 记录 payload 字段(为是否映射到 Stop 提供依据);
   - 故障构建(`-tags ballast_fault`)⇒ exit 1 + 空 stdout ⇒ parsed 为空(放行)。
4. 产物:`docs/facts/hermes-cases.json`(格式同 m0b-cases.json,另加 `client:"hermes"`、`hermes_commit`、`evidence_kind:"offline-hooks-test"`),`docs/facts/payloads/hermes/<case>/`。
5. **Verified 的含义**:`hooks test` 证明的是"序列化形状 + 输出解析 + 阻断语义",不证明真实 agent 在什么时机、以什么 kwargs 调用 hook。【决定】可以据此把 Hermes 各行的 `Verified` 改为 true,但 `docs/clients.md` 必须写明"离线核验(hooks test),触发时机未核验";真实会话取证【需凭据】。

**据此修正 adapter/hermes**(契约变更,本文预先批准,前提是第 3 步的取证结果支持):

- `patch` → Edit:replace 模式取 `path`;V4A 补丁模式解析全部目标(复用 Codex `apply_patch` 的 envelope 解析,放在 `adapter/common`——P2-HERMES 可以在 `adapter/common` 新增文件,但不得改既有导出签名)。
- `execute_code` 保持原名(规则可以按名字匹配),`no-tamper` 识别不了其中的写入,登记到 trust-model §2.1 第 1 类。
- `pre_verify` → `EventStop`,`deny` 渲染为 `{"decision":"block","reason":R}`,**语义是续跑**(同 Cursor),lint 给 `hermes-stop-continue` 警告;`Capabilities(hermes)` 的 Stop deny 置为支持。只有取证确认字段与输出解析后才落地。
- `Verified` 与 `docs/clients.md` §4.4 同步更新。

### 7.2 Codex

不碰凭据就无法取证:Codex 的 hook 只在交互式 TUI 中触发(pinrule 观察),需要登录;本机的 `codex` 是包装账号切换工具的 shell 函数,调用它可能使用真实凭据,**禁止**在取证中调用。

- **静态依据(可选,不需要凭据)**:P2-CLIENTS 可以按固定 tag 拉取 Codex 开源源码,核对 hooks.json 结构、事件名、`PermissionRequest` 输出、`additionalContext` 与 trust 哈希算法,结论写入 `docs/facts/codex-static.md`,标【静态依据】。
- **补测命令**(用户自己执行,需要用户在临时 `CODEX_HOME` 里自行登录):

```sh
CODEX_HOME=$(mktemp -d) BALLAST_HOME=$(mktemp -d) scripts/probe-clients/codex.sh
```

脚本安装 Ballast 与一个只记录 stdin 的探针 hook(带 `--codex-trust`),打印需要在 TUI 中输入的提示语清单(Bash 成功/失败、apply_patch、Stop、PermissionRequest),会话结束后收集、脱敏 payload 并生成 `docs/facts/codex-cases.json`。在此之前 Codex 全部单元格保持【未核验】。

### 7.3 Cursor

本机没有 cursor CLI;Cursor 的配置根没有覆盖变量,取证需要用一个一次性 `HOME` 并在其中登录。【需凭据】补测命令(在装有 Cursor / `cursor-agent` 的机器上,由用户执行):

```sh
BALLAST_CURSOR_PROBE_HOME=$(mktemp -d) scripts/probe-clients/cursor.sh
```

脚本在该一次性 `HOME` 下安装 Ballast 与探针 hook,提示用户在其中登录并按清单操作,收集 `docs/facts/cursor-cases.json`。脚本检测到 `HOME` 是真实家目录时拒绝运行。

---

## 8. 其他 P2 增强(少量)

从 architecture §14 的延后项与已知局限里挑价值高、改动面小的:

| id | 内容 | 单元 | 验收 |
|---|---|---|---|
| E1 psparse 覆盖面 | 写入目标补:`Tee-Object [-FilePath\|-LiteralPath] X`;`Set-Content`/`Add-Content`/`Out-File` 的 `-LiteralPath`;`Clear-Content`;`Rename-Item`(`-Path` 与 `-NewName` 都算);`Copy-Item`/`Move-Item` 的第二个位置参数作目标;`New-Item -ItemType File`;`Remove-Item -LiteralPath`;`[IO.File]::WriteAllText/AppendAllText/WriteAllBytes/WriteAllLines('<字面路径>', …)` | P2-PS | `internal/psparse/testdata/expect.json` 追加行(只追加不改既有行);fuzz 不 panic;100KB 解析仍 < 5ms;boundary-t0 夹具追加对应 detected 行 |
| E2 请求文件 GC | `~/.ballast/requests/` 超过 30 天的文件由 `ballast gc` 删除(`runner.GCRequests`,P2-RUNNER 追加导出,P2-CLI 接线) | P2-RUNNER / P2-CLI | runner 单测 + `ops_e2e_test` |
| — cache GC | 已在 v1.4 完成(`config.GCCache`),不再列入 | — | — |

**不做**(记录理由):runner 锁与 state 锁合并为 `sys` 锁原语(动契约包、收益小);SessionEnd 丢弃未收到 SubagentStop 的子 agent 计数(需要先有中断取证);replay 跳过 compact 内部 agent(缺 transcript 侧取证);100KB bash 解析 2.4ms 的优化(满足门槛)。

---

## 9. 单元划分与目录所有权

所有单元在各自 worktree 开发,只依赖本文与 architecture v2.0;修改已导出签名属于契约变更(architecture §12)。"共享文件"一栏列出允许多个单元编辑的文件及各自的**条目边界**——这些文件里不同单元的改动不在同一段落,合并不会冲突。

| 单元 | 独占目录/文件 | 测试包 / 验收命令 | 依赖(after) |
|---|---|---|---|
| **P2-M0B** | `scripts/probe-m0b/**`、`docs/facts/m0b-cases.json`、`docs/facts/M0b-CONCLUSIONS.md`、`docs/facts/payloads/m0b/**` | `bash scripts/probe-m0b/run.sh`;`go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json` | — |
| **P2-REDACT** | `internal/redact/**`、`internal/audit/**` | `go test ./internal/redact ./internal/audit`;60s fuzz | — |
| **P2-TIER** | `internal/tier/**` | `go test ./internal/tier` | — |
| **P2-SANDBOX** | `internal/sandbox/**` | `go test ./internal/sandbox` | — |
| **P2-PS** | `internal/psparse/**` | `go test ./internal/psparse` | — |
| **P2-HERMES** | `internal/adapter/hermes/**`、`internal/adapter/common/`(只新增文件)、`scripts/probe-clients/hermes.sh`、`docs/facts/hermes-cases.json`、`docs/facts/payloads/hermes/**` | `go test ./internal/adapter/...`;`bash scripts/probe-clients/hermes.sh` | — |
| **P2-CLIENTS** | `scripts/probe-clients/{codex,cursor}.sh`、`scripts/probe-clients/lib.sh`、`docs/facts/codex-static.md` | `bash -n` 语法检查;真实环境检测与拒绝逻辑的脚本自测 | — |
| **P2-RUNNER** | `internal/runner/**` | `go test ./internal/runner` | P2-TIER、P2-SANDBOX |
| **P2-HOOK** | `internal/hook/**`、`internal/model/verifyreq.go` 的函数体与 `internal/model/verifyreq_test.go` | `go test ./internal/hook ./internal/model` | P2-REDACT |
| **P2-REPORT** | `internal/report/**` | `go test ./internal/report` | P2-REDACT |
| **P2-CC** | `internal/adapter/claudecode/**` | `go test ./internal/adapter/...` | P2-M0B |
| **P2-INSTALL** | `internal/install/**` | `go test ./internal/install`;`bash scripts/install-e2e.sh` | P2-TIER、P2-CC |
| **P2-CLI**(集成) | `internal/cli/**`、`cmd/ballast/**`、`scripts/{boundary-t0.sh,boundary-t1.sh,accept-p2.sh}`、`scripts/boundary/**`、`scripts/bench/**`、`scripts/lib.sh`、`scripts/README.md`、`.github/workflows/**`、`README.md`、`docs/trust-model.md`、`docs/clients.md` | `bash scripts/accept-p2.sh`;`go test ./...` | 全部其他单元 |

**共享文件的条目边界**:

- `internal/model/client.go`、`internal/model/capability.go`:P2-CC 只改 Claude 表中的 `ConfigChange` 一行;P2-HERMES 只改 `ClientHermes` 的表项与能力集(含新增 `pre_verify` 行)。其他条目任何单元都不得改。
- `scripts/tools/accutil/`:P2 单元**不**修改;M0b 检查器放在自己的目录。

**依赖图**:

```
P2-M0B ─────▶ P2-CC ─────┐
                         ├─▶ P2-INSTALL ─┐
P2-TIER ─────────────────┤               │
         └───────────────┼─▶ P2-RUNNER ──┤
P2-SANDBOX ──────────────┘               │
P2-REDACT ──┬─▶ P2-HOOK ─────────────────┼─▶ P2-CLI(集成:接线、boundary、accept-p2、文档)
            └─▶ P2-REPORT ───────────────┤
P2-PS、P2-HERMES、P2-CLIENTS ─────────────┘
```

(P2-INSTALL 依赖 P2-TIER 与 P2-CC;P2-RUNNER 依赖 P2-TIER 与 P2-SANDBOX。)

第一批可以同时开工的:P2-M0B、P2-REDACT、P2-TIER、P2-SANDBOX、P2-PS、P2-HERMES、P2-CLIENTS。

---

## 10. 集成验收:`scripts/accept-p2.sh`

由 P2-CLI 编写,参数固定,任何断言失败即非 0;三平台 CI 执行(容器与真实客户端部分在缺少前提时显式 SKIP,`CI=true` 加对应 `BALLAST_REQUIRE_*` 时失败):

1. `go build ./...`、`go vet ./...`、`GOOS=windows go vet ./...`、`go test ./internal/archtest`、`go test ./...`。
2. `go test ./internal/redact -run '^$' -fuzz FuzzString -fuzztime 60s`。
3. `ballast report --out` 的 e2e(全部退出码,输出文件过 `Validate`)。
4. `scripts/boundary-t0.sh`。
5. `scripts/boundary-t1.sh`(需要 Docker;Linux CI 必跑)。
6. `go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json`;`hermes-cases.json` 同一检查器。
7. bench:新增场景 `12`(假 T1 规范 verify)与 ConfigChange 场景,p95 ≤ 30ms。
8. 卫生检查:新增产物里没有本机绝对路径、uid、秘密(复用 CI 的模式表)。

---

## 11. 【未核验】汇总

| 项 | 状态 | 由谁 / 怎样消除 |
|---|---|---|
| 无凭据时 SessionStart/UserPromptSubmit 是否触发 | 未核验 | P2-M0B `nocred-events` |
| `allowManagedHooksOnly`、低层 `disableAllHooks`、hook 合并、`managed-settings.d` 合并、托管 hook 路径要求 | 未核验(文档依据) | P2-M0B,无需凭据 |
| ConfigChange payload 字段 | 未核验 | P2-M0B `configchange-payload`(`hang` 模式) |
| hook 在沙箱外运行、denyWrite 对 Bash 的效果、permissions.deny 对 Edit 的效果、`allowUnsandboxedCommands`、denyWrite 路径写法、MCP 写入 | 【需凭据】 | 用户提供凭据后 `scripts/probe-m0b/run.sh --with-credentials …`;可选 §6.6 模拟模型 |
| MCP 白名单键名 | 未核验 | P2-M0B `mcp-allowlist-nocred` |
| bwrap 在 Docker / Ubuntu 24.04 CI 中可用所需的设置 | 未核验 | P2-M0B `runner-bwrap-in-docker`、P2-SANDBOX、P2-CLI(CI) |
| macOS `sandbox-exec` 的长期可用性(Apple 已标记弃用) | 风险 | P2-SANDBOX 探测失败即按 §3.4 退化 |
| Hermes 协议(载荷、输出解析、阻断语义) | 【静态依据】 | P2-HERMES 离线 `hooks test` |
| Hermes 真实会话中的触发时机 | 【需凭据】 | 用户自行补测 |
| Codex、Cursor 全部单元格 | 【需凭据】 | §7.2、§7.3 的补测命令 |
| boundary-t1 中 agent 实际尝试的各项 | 【需凭据】 | `scripts/boundary-t1.sh --with-credentials` |

## 12. 风险

1. **无凭据事件不触发**:若 `nocred-events` 失败,M0b 大部分用例变为 needs_credentials,T1 的托管行为仍只有文档依据;层级判定的条件 5 照文档实现,并在 doctor 中标注"托管行为未实测"。
2. **sandbox-exec 弃用**:macOS 未来版本移除时,macOS 只能走 `prefer`/`off`,T1 证据只能在 Linux 上产生;届时评估复用 Claude 的 sandbox-runtime。
3. **bwrap 与 user namespace**:部分发行版或容器默认禁用;`require` 策略会让 verify 全部变 infra,doctor 会给出 fail 与修复提示。
4. **Linux 上不存在的受保护路径**只能事后检查(§3.4);测试命令在运行中创建又删除的情况检测不到,登记为已知边界。
5. **ConfigChange 只审计**:它不能防止篡改,也不覆盖托管刷新(A16);T0 下仍以 `no-tamper` 为主信号。
6. **Hermes `pre_verify` 映射**会改变能力矩阵,只有取证确认后才落地。
