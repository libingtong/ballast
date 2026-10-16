# Ballast P2 设计与契约增补(冻结版 v1.1)

> 状态:**已冻结**(2026-10-16;v1.1 为第 1 轮独立审核修订,变更清单见 §13)。本文是 `docs/architecture.md` **v2.0** 的配套文档,与计划 `docs/ballast-plan.md` 末尾的 "v0.5 P2 计划" 一起,构成 P2 各实现单元唯一可依赖的契约。
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
| `internal/tier` | L2(model、sys、layout) | 是(`Locked` 每次调用;判定经 `ports.TierProbe`,惰性) | P2-TIER | T0/T1 判定;重定向锁;托管配置字面形式 `ManagedForms` 与受保护绝对路径 `ProtectedPaths` |
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
- **只有 `runner` 和 `cli` 可以 import `sandbox`**(allowlist:`runner` 列出它,`cli` 为 `*`);hook 的传递闭包里不可能出现它。
- `install` 用 `tier.ManagedForms`、`tier.ProtectedPaths` 与 `tier.Detect`,不 import `sandbox`;`doctor` 的沙箱可用性检查由 CLI(import `sandbox`)通过 `install.DoctorOptions` 的函数字段注入(与 `MDC` 同样的做法),字段名由 P2-INSTALL 追加。
- `layout.ResolveWith`(新文件 `internal/layout/resolvewith.go`)实现重定向锁(§3.2),函数体归 P2-TIER;`tier.Locked` 在 CLI 解析 layout 之前调用,结果作为 `layout.Options.IgnoreRedirects` 传入(layout 是 L1,不能反向 import tier)。

archtest 的变化(`internal/archtest/archtest_test.go`):

- `allowed` 新增 `redact`、`tier`、`sandbox`、`report` 四行;`audit`、`hook` 增加 `redact`;`runner` 增加 `tier`、`sandbox`;`install` 增加 `tier`。
- `hotPath` 与 `noExecTransitive` 增加 `redact`、`tier`。
- **新增 `TestBinaryNoNet`**:`go list -deps ./cmd/ballast` 不得含 `net`/`net/*`。这比"热路径不联网"更严:整个 ballast 二进制都不链接网络栈(当前已满足),同时把 `net/http` 的包初始化挡在每次 hook 调用之外。

### 2.3 契约追加清单(全部是追加,已在骨架中落地)

| 位置 | 追加 | 用途 |
|---|---|---|
| `model/tier.go`(新) | `SandboxPolicy`(require/prefer/off,只管 origin=cli)、`NetworkPolicy`(deny/allow)、`TierMissing*` 常量(含记录用的 `TierMissingRunnerSandbox`)、`TierInfo`(含 `Network`、`RunnerAllowWrite`、`Locked`) | 层级判定结果 |
| `layout/resolvewith.go`(新) | `Options{IgnoreRedirects, PasswdHome}`、`ResolveWith` | 重定向锁(§3.2) |
| `tier`(新包内) | `Locked`、`ManagedForms`(+ 共享 golden `internal/tier/testdata/managed_forms.json`)、`OrgConfig.RunnerNetwork/RunnerAllowWrite` | §3.2、§3.4、§3.5 |
| `sandbox`(新包内) | 默认拒写模型:`Spec.AllowWrite/RepoRoots/TmpDir/Network/Home`、`RepoMandatoryDeny`、`AncestorPins`、`ValidateSpec`、`PostCheck`、`DarwinEscapeDenials`;`Decide` 增加 `origin` 参数 | §3.4 |
| `runner/t1.go` | `RequestStatus`(`verify --status <request_id>` 回退与"取最差"聚合) | §3.3 |
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

agent 在 Bash 沙箱里跑的 `ballast verify` 写不了 `~/.ballast`,所以 T1 下由 hook(在沙箱外)代起 runner。hook 运行在沙箱外这一点(A20)是整个 T1 设计的地基,它**不需要模型调用**:M0b case `hooks-run-outside-sandbox` 用托管 SessionStart hook 在 `sandbox.enabled` 下写 denyWrite 中的路径来实测(前提是 `nocred-events` 通过;不通过才降为 needs_credentials,§6.4)。

**代起本身是一个特权提升点**:hook 代起的 runner 在 Claude 沙箱之外执行 agent 可以改写的测试代码。因此 runner 沙箱必须**不弱于** Claude 自己的 Bash 沙箱(默认拒写、只放行仓库与临时目录、网络受限),否则这条链路就成了沙箱逃逸原语(§3.4)。

### 3.2 层级判定(`internal/tier`)

**【决定】T1 当且仅当以下五条同时成立**,否则为 T0,并在 `TierInfo.Missing` 中列出每一条不成立的原因(常量见 `model/tier.go`):

| # | 条件 | Missing id |
|---|---|---|
| 1 | GOOS 为 darwin 或 linux(WSL2 报告 linux) | `os` |
| 2 | 测试重定向变量 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT` **没有生效**。在已锁定的主机上(见下文"重定向锁")生产构建忽略它们,所以它们不可能生效;只有未锁定的主机上它们才可能生效,此时判 T0 | `env_override` |
| 3 | `Self`(绝对路径,解析符号链接后)是 uid 0 拥有、组和其他用户不可写的普通文件,且直到 `/` 的每一级父目录同样 root 所有、组和其他不可写 | `binary_root_owned` |
| 4 | **编译期默认托管目录** `ManagedDirFor(GOOS)/ballast/org.json`(不看 `Layout.ManagedDir`)是 root 所有、组和其他不可写的普通文件,通过 `LoadOrgConfig` 校验,且 `"tier":"T1"` | `org_config` |
| 5 | `ManagedDirFor(GOOS)` 下合并后的 Claude 托管设置(每个文件 root 所有、组和其他不可写;合并顺序以 M0b `managed-d-merge-*` 的结论为准)满足:`allowManagedHooksOnly==true`;存在一个 hook 条目,其 `command` 是**绝对路径**、`EvalSymlinks(command)==EvalSymlinks(Self)`,且 `args[0:2]==["hook","--client"]`;`sandbox.enabled==true`、`allowUnsandboxedCommands==false`、`failIfUnavailable==true`;`allowManagedPermissionRulesOnly==true`;并且令 `(dw, pd) = tier.ManagedForms(lay, command, org.protected_extra)`(`command` 取该 hook 条目里**原样写的字符串**),`dw` 的每个字符串都逐字出现在 `sandbox.filesystem.denyWrite` 中,`pd` 的每个字符串都逐字出现在 `permissions.deny` 中 | `managed_settings`、`managed_hooks`、`allow_managed_hooks_only`、`sandbox`、`sandbox_deny_write`、`permissions_deny`、`managed_permissions_only` |

任何输入读不了或解析失败 ⇒ T0 + `unreadable`(失败时取保守一侧)。

**"覆盖"的定义(【决定】)**:覆盖就是**精确字符串集合的包含关系**,不做任何 glob 或路径推理。`tier.ManagedForms(lay, self, extra)` 是纯函数,输出与 `org render` 逐字一致的字符串:家目录条目用字面 `~/` 形式,仓库条目用字面 `./` 形式,与具体用户和仓库无关(同一份托管配置对主机上每个用户、每个仓库都适用),二进制目录取 `self` 原样字符串的目录部分(不解析符号链接)。`org render` 和 `Detect` 共用这个函数;共享 golden `internal/tier/testdata/managed_forms.json` 锁住两边(P2-TIER 断言函数输出,P2-INSTALL 断言 `org render` 的输出)。冻结的输出形状:

```
denyWrite = [dir(OrgRules), "~/.ballast", "./.ballast", dir(self), extra...,
             "~/.claude/settings*.json", "./.claude/settings*.json"]
permDeny  = 对 Edit、Write、NotebookEdit 依次:
             T(dir(OrgRules)/**) T(~/.ballast/**) T(./.ballast/**) T(dir(self)/**)
             对每个 extra e:T(e) T(e/**)
             T(~/.claude/settings*.json) T(./.claude/settings*.json)
```

`extra` 同时写 `T(e)` 与 `T(e/**)`,因为 org.json 不区分文件与目录。重复项保留第一次出现的位置。

**符号链接**:管理员传给 `org render --binary` 的可能是符号链接路径(如 `/usr/local/bin/ballast → /usr/local/libexec/ballast/ballast`)。比较 hook 命令与 `Self` 时两边都先 `EvalSymlinks`,且只接受绝对路径;`ManagedForms` 用 hook 条目里原样的字符串,所以与 `org render` 的输出逐字相同。`testdata/detect.json` 含"hook 命令是指向 Self 的符号链接 ⇒ T1"与"hook 命令是相对路径 ⇒ `managed_hooks`"两行。

**重定向锁(【决定】)**:如果编译期默认的 `ManagedDirFor(GOOS)/ballast/org.json` 存在、root 所有、组和其他不可写、通过校验且声明 `tier:"T1"`,主机即为**已锁定**(`tier.Locked`)。已锁定时,生产构建:

- 忽略 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT`(`layout.ResolveWith(env, Options{IgnoreRedirects:true})`);
- 家目录取自 passwd 数据库(有效 uid),不取 `$HOME`;取不到时 layout 解析失败(hook 按 B7 放行并且无副作用,doctor 报 fail)——已锁定主机上家目录不确定属于配置错误,不能因此退回采信 `$HOME`;
- 变量存在时,hook 每个会话审计一次 `diag: env_redirect_ignored`,doctor 报 warn。

`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME` 不受影响:它们重定向的是客户端本身,而不是 Ballast 的信任输入。`tier.Locked` 每次 hook 调用都执行,代价是一次 `Lstat`,文件存在时再读几百字节,目标 ≤ 20µs(`BenchmarkLocked`)。测试不受影响:需要重定向的测试从不在真实托管目录下放 root 所有的 org.json;锁本身的测试用假 FS 和假 `Owner`。

**为什么 agent 伪造不了,也降级不了**:条件 3–5 要求写 root 所有的文件;拥有 root 的 agent 不在任何 Ballast 边界之内(trust-model 声明)。**降级同样是攻击目标**:如果 agent 能把重定向变量塞进 Claude Code 进程的环境(例如经由一次 runner 逃逸写 shell rc),判定虽然只是"降到 T0",org 规则和 `no-tamper` 却会从 agent 可控的目录读取。重定向锁关闭了这条路:在声明 T1 的主机上这些变量一律无效,org 规则、证据、状态始终在真实位置;而在未声明 T1 的主机上,本来就是 T0,降级没有意义。判定只决定"路由"和"证据上写什么 tier";**证据的 tier 由 runner 自己再判定一次**(§3.4),hook 判断失误不会让证据变成 T1。

**`org.json`**(新文件,`layout.OrgConfig`,管理员下发,`org render` 生成):

```json
{"v": 1, "tier": "T1", "runner_sandbox": "require", "runner_network": "deny",
 "runner_allow_write": ["~/.cache/go-build"], "protected_extra": ["~/.config/acme/ci-token"]}
```

校验规则见 `tier.OrgConfig` 注释(版本、枚举值、路径必须绝对或 `~/` 开头;`runner_allow_write` 不得是 `~`、`/`、`/home`、`/Users`、`/root` 之类的家目录或其祖先)。校验失败等同于不存在(`org_config`,且主机不锁定)。

`Declared`(org.json 声明)为 T1 而判定为 T0 时:`doctor` 报 fail(列出 Missing);hook 遇到规范 verify 时写审计 `diag: tier_mismatch`。只有 root 所有的 org.json 才被采信。

**受保护路径的两种形式**:

- `tier.ManagedForms`:托管配置里的字面字符串(`~/`、`./`),供 `org render` 与条件 5 使用,与用户和仓库无关。
- `tier.ProtectedPaths(fsys, lay, self, repo, extra)`:某个用户、某个仓库下的绝对路径,供 runner 沙箱与 doctor 列表使用。顺序固定:org 规则目录、`~/.ballast/`、`<repo>/.ballast/`、二进制目录、`~/.claude/settings*.json`(遵循 `CLAUDE_CONFIG_DIR`)、`<repo>/.claude/settings*.json`、`protected_extra`。每项同时给出原始路径与 `EvalSymlinks` 形式(macOS 的 `/var` → `/private/var`),消费方两种都保护。runner 沙箱还会在此之上追加 `sandbox.RepoMandatoryDeny` 与 `sandbox.GitDirMandatoryDeny`(§3.4)。

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

**子进程侧**(P2-CLI 接线):`verify --hook-start` 用 `model.ParseHookStartArgv` 解析参数,调用 `runner.StartRequest`。它写 `VerifyRequestFile`(`~/.ballast/requests/<repo_inst>/<request_id>.json`):第一个(或唯一一个)job 的 id 就是 `request_id`,其余 job 用新 id;所有记录 `Origin:"hook"`。配置错误(无仓库、无 project.json、未知名字)也写请求文件并带 `error`,这样 `ballast verify --status <request_id>` 能解释原因。`--status` 找不到同名 job 时回退读请求文件:单个 job 时退出码同该 job;多个 job 时取最差结果(fail > timeout > signal > infra > running > pass,superseded 记为 infra),输出逐行列出。**回退与聚合由 P2-RUNNER 实现**(`runner.RequestStatus`,返回逐 job 状态、`Worst` 与 `ExitCode`);P2-CLI 只负责调用、逐行打印并以 `ExitCode` 退出。

**结果回报**:沿用 M2 的"下一次注入回报已完成 job"(按 repo_inst 读 evidence),不需要改动。`evidence-before-commit` 遇到运行中的 job 仍返回 unknown 加提示(v0.4.2)。

**【决定】新鲜度不按 tier 过滤**,前提是下面三条同时成立(都已冻结为契约,并在 P2-RUNNER、boundary-t1 中各有断言):

1. origin=hook 的 job **永远**在 OS 沙箱里执行测试命令;沙箱不可用时**不执行**,结果记 infra、error `sandbox_unavailable`,不受 `runner_sandbox` 影响(§3.4)。
2. runner 沙箱默认拒写:测试命令写不了 `~/.ballast`(evidence、rules、state)、shell rc、`.git/hooks` 等(§3.4)。
3. agent 自己的 Bash 在 Claude 沙箱里,写不了 `~/.ballast`;文件工具受 permissions.deny 约束。

于是在 T1 主机上能写证据的只有:runner 进程本身(只写它亲自观察到的结果),以及人在终端里手动运行的 `ballast verify`(origin=cli;按 `runner_sandbox` 决定是否包装,不包装时记录为 T0)。T0 记录在 T1 主机上出现只可能来自人;tier 用于审计与 CI 判读,`verify_fresh` 语义不变。

**攻击面对照**:

| agent 的尝试 | T1 下的结果 |
|---|---|
| 在 `~/.claude/settings.json` 加自己的 hook,或 `disableAllHooks` | `allowManagedHooksOnly` 使低层 hook 不执行,低层不能关闭托管 hook【M0b 验证】;settings 本身在 denyWrite / permissions.deny 中 |
| 在 `PATH` 前面放伪造的 `ballast` | 托管 hook 用绝对路径;hook 代起时用 `Self`,不用 agent 的 argv[0] |
| 在沙箱里跑 `ballast verify --foreground test` | 写不了 evidence,结果为 infra |
| 直接写 evidence / state / rules | Bash:denyWrite;文件工具:permissions.deny;MCP:白名单(残余风险) |
| 让 Claude Code 进程带上 `BALLAST_HOME` 等重定向变量(降级攻击) | 重定向锁:已锁定主机上这些变量被忽略,家目录取自 passwd;org 规则、证据、状态仍在真实位置;审计 `env_redirect_ignored`(§3.2) |
| 改测试代码让它"恰好通过" | 内容快照把改动纳入 `S_end`/`C`;提交里能看到测试被改,由 CI 与评审把关(trust-model §3,本设计不改变) |
| 改测试代码,让 hook 代起的测试去写 `~/.zshrc`/`~/.bashrc`/`~/.profile`、`~/.claude.json`、`~/.ballast/evidence` | 默认拒写:可写集合之外一律拒绝(§3.4) |
| 测试代码重命名受保护路径的祖先目录再重建(`mv repo repo2 && … && mv repo2 repo`,或重命名 `.git` 后改 `config`) | 祖先固定:macOS 对祖先 `(literal)` 拒写,Linux 把祖先变成挂载点(rename 返回 EBUSY);仓库外的祖先本来就在可写集合之外(§3.4) |
| 测试代码硬链接受保护文件再经链接写 | macOS:建链被拒;Linux:跨绑定挂载建链返回 EXDEV(§3.4) |
| 测试代码写 `.git/hooks/*` 或 `.git/config`(`core.fsmonitor`、`core.hooksPath`,Ballast 自己的快照或人随后运行 git 时就会在沙箱外执行) | `RepoMandatoryDeny`(§3.4) |
| 测试代码联网外传 | `runner_network` 默认 deny:只允许回环与可写集合内的 unix socket;Linux 另外屏蔽 `/run`、`/var/run` 并移除 `SSH_AUTH_SOCK`、`DOCKER_HOST`(§3.4) |
| 测试代码经 LaunchServices / AppleEvents / launchd 让沙箱外的进程替它执行(macOS) | `DarwinEscapeDenials`【未核验,P2-SANDBOX 集成测试必须证明】(§3.4) |
| 测试命令启动后台进程稍后写受保护路径 | macOS:沙箱继承给全部后代;Linux:bwrap `--unshare-pid --die-with-parent`,runner 结束即全部终止 |

### 3.4 沙箱化 runner(`internal/sandbox` + `internal/runner`)

**runner 进程本身在沙箱外**(它要写 evidence、job 文件与日志),只把**测试命令**包进沙箱;日志经继承的文件描述符写入,不受沙箱路径规则影响。

**【决定】默认拒写模型**(v1.0 的"默认全允许、只拒受保护路径"已废弃:它比 Claude 自己的 Bash 沙箱弱得多,而 hook 代起的 runner 在 Claude 沙箱之外执行 agent 可改写的测试代码,这条链路会变成沙箱逃逸原语;审核实测了 `mv repo repo2 && echo pwn > repo2/.ballast/project.json && mv repo2 repo` 在旧 profile 下成功)。测试命令只能写:

```
W = 仓库工作树
  ∪ 仓库的 git dir 与 common dir(位于工作树之外时,例如 worktree)
  ∪ org.json runner_allow_write(展开 ~ 后;用于构建缓存,例如 ~/.cache/go-build)
  ∪ 每个 job 的临时目录(macOS:os.TempDir() 下 0700 新建、结束后删除;Linux:私有 tmpfs /tmp),TMPDIR 指向它
  ∪ 固定设备集(/dev/null、/dev/zero、/dev/tty*、/dev/fd/*)
```

在 `W` 内部,以下受保护集合 `D` 仍然不可写:

```
D = tier.ProtectedPaths(…)(原始与解析后两种形式,含 glob)
  ∪ 对每个仓库工作树根:sandbox.RepoMandatoryDeny
      .ballast/  .claude/  .git/hooks/  .git/config  .gitmodules  .mcp.json  .vscode/  .idea/
  ∪ 对 git dir 与 common dir(无论在工作树内外):sandbox.GitDirMandatoryDeny
      hooks/  config  config.worktree
```

git 的索引、对象与引用仍可写(测试里的 `git add`/`commit` 会进入内容快照,是可见的,不是绕过)。

`RepoMandatoryDeny` 除 Ballast 自己的两项外,对齐 Claude Code Bash 沙箱的"危险文件"类别(之后会在沙箱外执行的东西:git hooks;git config 里的 `core.fsmonitor`、`core.hooksPath`——Ballast 自己的快照运行 git 时就会触发;编辑器与 MCP 配置)【静态依据待核:P2-SANDBOX 对照 sandbox-runtime 源码,只能追加】。仓库内的 `.claude/` 整个目录拒写(比托管配置里的 `settings*.json` 更严),这样 Linux 上不必为 glob 处理祖先。

**祖先固定**:对 `D` 中每个位于 `W` 内的路径 `P`,从所在的 `W` 根开始到 `dir(P)` 的每一级祖先 `A` 都要固定,使"重命名或删除祖先再重建"无法替换 `P`(`sandbox.AncestorPins`,纯函数,golden 测试)。`W` 之外的祖先本来就不可写,不需要处理。

**后端**:

| 平台 | 后端 | 方式 |
|---|---|---|
| macOS | `/usr/bin/sandbox-exec` | `sandbox-exec -p <DarwinProfile(spec)> -- argv`。profile 按此顺序(SBPL 最后匹配的规则生效):`(version 1)(allow default)`;`(deny file-write*)`;`(allow file-write* <W 的 subpath>、<设备的 literal/regex>)`;`(deny file-write* <D 的 subpath/literal/regex>)`;`(deny file-write* <每个祖先的 literal>)`;网络 deny 时 `(deny network-outbound)`、`(allow network-outbound (remote ip "localhost:*"))`、`(allow network-outbound (remote unix-socket (subpath <每个 W>)))`;最后是 `DarwinEscapeDenials()`。SBPL 按路径匹配,对尚不存在的路径同样生效 |
| Linux | bubblewrap | `bwrap --die-with-parent --unshare-pid [--unshare-net] --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp [--tmpfs /run --tmpfs /var/run]`,然后对每个已存在的 `W` 条目 `--bind A A`,对每个已存在的祖先 `--bind A A`(变成挂载点),对每个已存在的 `D` 条目 `--ro-bind P P`(glob 在包装时展开;glob 所在目录位于 `W` 内时整个目录 ro-bind),最后 `--chdir <cwd> --setenv TMPDIR /tmp -- argv`。后面的挂载覆盖前面的。`W` 内尚不存在的受保护路径无法 bind(会在宿主上建挂载点),列入 `Wrapped.MissingProtected`,运行后由 `sandbox.PostCheck` 检查:出现了 ⇒ 结果记 infra、error `protected_path_created` |
| 原生 Windows 及其他 | 无 | 永远 T0 |

**设计期实测**(本机 macOS 26 的 `sandbox-exec`,以及 Debian 12 容器里的 bubblewrap 0.8.0;均为临时目录,未写入仓库;正式用例由 P2-SANDBOX 的集成测试与 boundary-t1 固化):

| 尝试 | macOS(上表 profile) | Linux(上表参数) |
|---|---|---|
| 写 `W` 内普通文件;`git add` | 成功 | 成功 |
| 写 `W` 外(模拟 `~/.bashrc`、`~/.ballast/rules.json`) | 拒绝 | 拒绝(只读文件系统) |
| 写、新建受保护路径 | 拒绝(含尚不存在的路径) | 拒绝;尚不存在的路径能创建 ⇒ 由 `PostCheck` 兜底 |
| 重命名仓库根 | 拒绝 | 拒绝(只读 / EBUSY) |
| 重命名受保护路径的祖先(`.git`、`.claude`)后重建 | 拒绝(祖先 literal) | **未固定时成功**(实测 `mv .git .git2 && mkdir .git && echo pwn > .git/config` 生效);固定后 EBUSY |
| 硬链接受保护文件到 `W` 再写 | 建链被拒 | EXDEV |
| 先建符号链接指向受保护文件再写 | 写被拒 | 写被拒(目标只读) |
| 出站 TCP | deny 策略下拒绝,回环可用 | `--unshare-net` 下无网络 |
| 连接 `W` 外的 unix socket | 拒绝;`W` 内的允许 | 仍可连接 ro 挂载上的 socket 文件 ⇒ 屏蔽 `/run`、`/var/run`,移除 `SSH_AUTH_SOCK`、`DOCKER_HOST`;其他自定义位置的 socket 登记为残余风险 |
| Docker 内运行 bwrap | — | 需要 `--security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined`(缺最后一项时 `Can't mount proc on /newroot/proc`) |

**macOS 的非文件逃逸通道**:`(allow default)` 仍允许沙箱内进程请求沙箱外的服务替它执行(LaunchServices `open -a`、AppleEvents `osascript`、`launchctl submit`)。`DarwinEscapeDenials` 拒绝相应的 mach-lookup 与这几个可执行文件的 process-exec【未核验】;P2-SANDBOX 的集成测试必须证明这三条通道都无法写 `W` 之外,做不到就把 darwin profile 改为 `(deny default)` 加显式白名单(只改 `DarwinProfile` 的内部实现,不动签名)。

**沙箱集成用例**(P2-SANDBOX 在两个平台上都要跑,boundary-t1 在容器里再跑一遍;每条都必须失败,同时"写 W 内文件"与 `git add` 必须成功):写 `$HOME/.bashrc`、`$HOME/.zshrc`、`$HOME/.profile`;写 `~/.claude.json`;写 `~/.ballast/evidence/x`;写 `<repo>/.ballast/project.json`;新建原本不存在的 `<repo>/.ballast/x`;`mv <repo> <repo>2` 后写再改回;`mv <repo>/.git <repo>/.git2 && mkdir <repo>/.git && echo x > <repo>/.git/config`;`ln ~/.ballast/rules.json <repo>/hl && echo x > <repo>/hl`;写 `<repo>/.git/hooks/pre-commit`;`curl https://example.com`(deny 策略);连接 `W` 外的 unix socket;macOS 另加 `open -a`、`osascript`、`launchctl submit` 三条。

**探测**:`sandbox.Probe` 用平凡命令实跑一次后端,并验证"对一个临时拒写目录的写入确实失败",结果按进程缓存。Linux 的 bwrap 只采用 root 所有、组和其他不可写的那个(防止 agent 在 `PATH` 里放假 bwrap)。常见不可用原因:未安装 bwrap;内核或 AppArmor 禁止非特权 user namespace(Ubuntu 24.04 默认 `kernel.apparmor_restrict_unprivileged_userns=1`)【未核验:CI 镜像】;Docker 容器内缺上表的三个 `--security-opt`(不使用 `--privileged`)。

**`ValidateSpec`**(包装前必须通过,否则 infra、error `sandbox_spec_invalid`,不执行):`W` 中任何条目不得是 `/`、真实家目录或其祖先,不得等于或位于 `D` 的目录之内;`Cwd` 必须在 `W` 内;所有路径必须是绝对路径。

**策略与退化**(【决定】;`runner_sandbox` **只管 origin=cli**):

| origin | runner 自判 | 策略(org.json `runner_sandbox`) | 后端可用 | 行为 | 记录 |
|---|---|---|---|---|---|
| hook | 任意 | **忽略**(一律必须沙箱) | 是 | 包装后执行 | runner 自判 T1 ⇒ `tier:"T1"`;否则 `tier:"T0"`;`sandbox:"bwrap"`/`"sandbox-exec"`,`origin:"hook"` |
| hook | 任意 | 忽略 | 否 | **不执行测试命令**,outcome `infra`,error `sandbox_unavailable`,审计 diag | `tier:"T0"`,`sandbox:"none"` |
| cli | T1 | `require`(T1 默认) | 是 | 包装后执行 | `tier:"T1"`,`sandbox` 如实 |
| cli | T1 | `require` | 否 | 不执行,outcome `infra`,error `sandbox_unavailable` | `tier:"T0"`,`sandbox:"none"` |
| cli | T1 | `prefer` | 是 / 否 | 可用则包装(`tier:"T1"`),否则不包装执行 | 不包装时 `tier:"T0"`,`sandbox:"none"`,`tier_missing:["runner_sandbox"]` |
| cli | T1 | `off` | — | 不包装执行 | `tier:"T0"`,`sandbox:"none"`,`tier_missing:["runner_sandbox"]` |
| cli | T0 | 默认 `off` | — | 现有行为 | `tier:"T0"` |
| cli | T0(root 所有的 org.json 写了 `prefer`/`require`) | 同左 | 是 | 包装后执行(纵深防御) | 仍是 `tier:"T0"`,`sandbox` 如实记录 |

`prefer`/`off` 只对 origin=cli 生效,而在 T1 主机上 origin=cli 只可能是人在终端里运行(agent 的 Bash 在 Claude 沙箱里,写不了证据,§3.3)——人选择在沙箱外运行自己仓库的测试,与人直接运行 `npm test` 等价,记录为 T0 如实反映这一点。

**`VerifyRecord.Tier == "T1"` 当且仅当 runner 自己判定为 T1 且 `Sandbox != "none"`。** runner 的判定是证据 tier 的唯一权威,不采信 hook 传来的任何层级信息(`HookStartRequest` 里也没有这个字段)。

**沙箱之外仍然成立的边界**:可写集合内的写入不限制(测试要生成文件、改 git 索引——改动会进入 `C`,是可见的,不是绕过);`runner_network:"allow"` 时网络不限制(管理员显式选择);自定义位置的 unix socket(例如家目录下的 docker.sock)在 Linux 上仍可连接,登记为残余风险(§11)。

### 3.5 `org render` 与 `doctor` 的变化(P2-INSTALL)

- `org render` 的 denyWrite 与 permissions.deny 改为**直接输出 `tier.ManagedForms(lay, --binary, --protect...)`**,新增 `./.claude/settings*.json`;`--protect` 的值同时写进 org.json 的 `protected_extra`,保证 `Detect` 用同一组 extra 重算出同样的字符串。`internal/install` 的测试读取 `internal/tier/testdata/managed_forms.json`,断言 `org render` 输出逐字一致。Claude 沙箱对 `./`、`~/`、glob 写法的实际解释【需凭据,M0b case `managed-denywrite-path-forms`】不影响覆盖判定(覆盖判定只比字符串);若 M0b 证明某种写法不生效,改 `ManagedForms` 与 golden(契约变更)。
- 输出增加 `org.json` 段(含 `runner_sandbox:"require"`、`runner_network:"deny"`、`runner_allow_write:[]`,并以注释提示管理员按需加入构建缓存目录);`--write --i-am-admin`(root)同时写 `managed-settings.d/ballast.json` 与 `<托管目录>/ballast/org.json`(0644,root)。
- ConfigChange 行在 `Install` 翻为 true 之后才出现在托管 hook 中。
- MCP 白名单键名(`allowManagedMcpServersOnly`、`allowedMcpServers`,当前以 `omitempty` 出现在模板结构里)【未核验,M0b case `mcp-allowlist-nocred`】。
- `doctor`:`tier.level` 改为调用 `tier.Detect`,逐条列出 Missing;新增 `tier.declared_mismatch`(org.json 声明 T1 而判定为 T0 ⇒ fail)、`tier.runner_sandbox`(T1 且后端不可用 ⇒ fail,因为 hook 代起的 verify 将全部是 infra;沙箱探测函数由 CLI 注入)、`tier.redirect_ignored`(已锁定且重定向变量存在 ⇒ warn)。

### 3.6 验收(T1 部分)

| 单元 | 可执行验收 |
|---|---|
| P2-TIER | `go test ./internal/tier ./internal/layout`:`testdata/detect.json` 逐条翻转五个条件(每次只翻一条 ⇒ T0 且 Missing 恰好是该 id);全部满足 ⇒ T1;hook 命令为指向 Self 的符号链接 ⇒ T1;相对路径命令 ⇒ `managed_hooks`;`testdata/managed_forms.json` golden;`Locked` 表(不存在 / 非 root / 组可写 / `tier:"T0"` / 校验失败 ⇒ false);`ResolveWith(env, Options{}) == Resolve(env)` 表驱动;`IgnoreRedirects` 下三个变量无效且家目录取自 `PasswdHome`;用计数 FS 断言 `Probe` 第二次调用零 I/O;`BenchmarkProbe` ≤ 200µs,`BenchmarkLocked` ≤ 20µs |
| P2-SANDBOX | `go test ./internal/sandbox`:`DarwinProfile`/`BwrapArgs`/`AncestorPins` golden(`testdata/profiles.json`,含空格、引号、括号、非 ASCII 路径);`ValidateSpec` 表;`Decide` 全表(origin=hook 在任何策略下无后端都返回 `ErrUnavailable`);§3.4 "沙箱集成用例"在 darwin(有 sandbox-exec)与 linux(有 bwrap)上逐条断言失败、W 内写入与 `git add` 成功;缺后端本地 SKIP,`CI=true` 且 `BALLAST_REQUIRE_BWRAP=1`/`BALLAST_REQUIRE_SANDBOX_EXEC=1` 时 FAIL |
| P2-RUNNER | `go test ./internal/runner`:用假 `TierProbe` 与假沙箱后端覆盖 §3.4 策略表全部行;**断言 origin=hook 在 `prefer`、`off`、后端不可用的组合下都不执行测试命令**(假后端记录"未调用"、job outcome infra、error `sandbox_unavailable`、无 pass 证据);`StartRequest` 首个 job id == request_id、请求文件内容、配置错误也写请求文件;`RequestStatus` 的取最差与退出码表;`ValidateSpec` 失败 ⇒ infra;Linux 事后检查 `protected_path_created`;真实后端可用时的端到端(测试命令写受保护路径失败、记录 `tier:"T1"`) |
| P2-HOOK | `go test ./internal/hook`:`pipeline.json` 追加行——规范 verify + 假 T1 ⇒ deny 且 reason 含 request_id、Spawner 收到 `HookStartArgv`;T0 ⇒ 无特殊处理;非规范形式(管道、`--status`、`env X=1 ballast verify`、动态词)⇒ 无特殊处理;spawn 失败 ⇒ deny + `verify_spawn_failed`;`ParseVerifyRequest` 与 `HookStartArgv`/`ParseHookStartArgv` 往返的表驱动测试与 fuzz |
| P2-CLI | 接线:`tier.Locked` → `layout.ResolveWith`;bench 新场景 `12`:假 T1 下的规范 verify PreToolUse p95 ≤ 30ms(用 `BALLAST_TEST_TIER` 之类只在 `ballast_fault` 构建标签下生效的开关,生产构建忽略);既有 bench 场景在加入 `Locked` 后仍满足 B10 门槛 |
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

镜像复用 M0b 的 Dockerfile(§6.2),外加:linux 版 ballast 由 root 安装到 `/usr/local/libexec/ballast/ballast`(0755,root);容器内以 root 执行 `ballast org render --binary … --write --i-am-admin`;之后全部以普通用户 `agent` 运行。容器参数:`--security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined`(bwrap 需要,设计期实测缺最后一项时 bwrap 无法挂载 `/proc`),`--network none`(只在构建镜像时联网;`t1-runner-sandbox` 的联网用例改用容器内的本地监听器与 `--network bridge` 变体,见下)。

**不需要模型、做成可执行断言的部分**:

| id | 断言 |
|---|---|
| t1-doctor | `ballast doctor --json`:`tier.level` 为 T1,Missing 为空 |
| t1-runner-sandbox | 夹具仓库的 `project.json` 里 verify `test` 是一个脚本 `scripts/boundary/t1-escape.sh`:依次尝试 `echo > ~/.ballast/evidence/x`、`sed -i` 改 `.ballast/rules.json`、`python3 -c` 写 `~/.ballast/rules.json`、`touch` 二进制目录、创建原本不存在的 `<repo>/.ballast/x`,以及 §3.4 "沙箱集成用例"的全部 Linux 条目——写 `~/.bashrc`/`~/.zshrc`/`~/.profile`/`~/.claude.json`;`mv <repo> <repo>2` 再改回;`mv .git .git2 && mkdir .git && echo x > .git/config`;`ln ~/.ballast/rules.json hl && echo x > hl`;写 `.git/hooks/pre-commit`;连接容器内 `/run` 下预置的 unix socket;`--network bridge` 变体里 `curl` 一个外部地址(构建镜像时可达的同一镜像源)——每次写或连接失败计 1;全部失败、且同一脚本里写 `<repo>/ok.txt` 与 `git add` 成功才 `exit 0`。把合成的 PreToolUse payload(`ballast verify test`)喂给托管 hook 命令 ⇒ stdout 为 deny 且含 job id;等待 job ⇒ 记录 `tier:"T1"`、`sandbox:"bwrap"`、`origin:"hook"`、outcome pass;受保护文件的哈希前后一致 |
| t1-forged-binary | 用户可写目录里的 ballast 副本作为 hook 命令 ⇒ `tier.Detect` 为 T0(`binary_root_owned`),不代起;该副本运行的 verify 记录为 T0 |
| t1-orgjson | org.json 删除 / 改为 agent 所有 / `tier:"T0"` ⇒ 各自 T0 + 对应 Missing;doctor 报 `tier.declared_mismatch` |
| t1-env-override | 以 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT`(都指向 agent 可写的目录,里面放一份放行一切的 rules.json 与伪造的 pass 证据)和伪造的 `HOME` 调用托管 hook ⇒ 判定仍为 T1(重定向锁生效,`TierInfo.Locked:true`);org 规则仍然生效(一条只在 org 规则里的 deny 规则照常拦截);伪造证据不被采信(`evidence-before-commit` 照常 unknown);审计有 `env_redirect_ignored`;agent 目录下没有新文件 |
| t1-hooks-outside-sandbox | 引用 M0b `hooks-run-outside-sandbox` 的结论;容器里用 org render 生成的托管配置(`sandbox.enabled:true`)再跑一次:托管 SessionStart 审计行写入 `~/.ballast/audit`(denyWrite 中)成功。前提同 `t1-lower-hooks`;M0b 降级时同样标 needs_credentials |
| t1-sandbox-missing | 去掉 bwrap 的镜像变体 ⇒ hook 代起的 job outcome infra、error `sandbox_unavailable`、测试脚本没有被执行(脚本第一行写一个标记文件,断言标记不存在)、无 pass 证据;把 org.json 改成 `runner_sandbox:"prefer"` 与 `"off"` 各重跑一次,结果相同;doctor `tier.runner_sandbox` 为 fail |
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
- 运行参数:`--rm --network none`(运行期不联网,确保没有凭据外流,也没有真实模型请求),`--security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined`(只在沙箱相关用例上加;设计期在 Debian 12 容器中实测,bwrap 缺 `systempaths=unconfined` 时报 `Can't mount proc on /newroot/proc`),环境 `DISABLE_AUTOUPDATER=1 DISABLE_TELEMETRY=1 DISABLE_ERROR_REPORTING=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`。
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
| `runner-bwrap-in-docker` | 不经 Claude:在容器里直接用 §3.4 的 bwrap 参数跑 §3.4 "沙箱集成用例"的 Linux 条目,确认所需的 `--security-opt`(设计期结论:三项,见 §6.2) | 否 |
| `hooks-run-outside-sandbox` | **前提:`nocred-events` 通过**(否则降为 needs_credentials 并带 recheck)。托管层 `sandbox.enabled:true`、`failIfUnavailable:true`,`sandbox.filesystem.denyWrite` 含 `/home/agent/protected`;托管 SessionStart 探针除记录 stdin 外还执行 `echo hook > /home/agent/protected/from-hook`(UserPromptSubmit 再做一次)。写入成功 ⇒ `pass`(hook 在沙箱外运行,A20 成立);写入失败 ⇒ `fail`(hook 在沙箱内,T1 的代起设计与 Ballast 自身写 state/audit 都要重做,P2-RUNNER/P2-HOOK 暂停并升级为 architecture §12 契约变更)。对照:同一容器去掉 `sandbox` 段再跑一次,写入必须成功;确认 bwrap 确实可用(否则 `failIfUnavailable` 会使客户端拒绝启动,此时结果记 `unverifiable` 并给出原因) | 否 |
| `sandbox-denywrite-bash` | agent 用 Bash `echo >`、`sed -i`、`python3 -c` 写 denyWrite 路径 ⇒ 失败 | **是** |
| `permissions-deny-edit` | agent 用 Edit/Write 写受保护路径 ⇒ 被拒 | **是** |
| `unsandboxed-refused` | `allowUnsandboxedCommands:false` 时 agent 请求 `dangerouslyDisableSandbox` ⇒ 被拒 | **是** |
| `managed-denywrite-path-forms` | denyWrite 中 `~/`、相对路径、glob 写法的实际效果 | **是** |
| `mcp-write` | 白名单内一个会写文件的 MCP server 写受保护路径 ⇒ 成功(记录残余风险);白名单外的 server 不可用 | **是** |

合计:**无凭据 14 个**(`nocred-events` 至 `hooks-run-outside-sandbox`),**需凭据 5 个**。`nocred-events` 不通过时,依赖事件触发的无凭据用例(除 `runner-bwrap-in-docker`、`mcp-allowlist-nocred`、`managed-file-owner` 中不依赖事件的部分外)全部降为 `needs_credentials`,其 `recheck` 是同一条补测命令加上这些 case 名。

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
ANTHROPIC_API_KEY=<用户自己提供> scripts/probe-m0b/run.sh --with-credentials --cases sandbox-denywrite-bash,permissions-deny-edit,unsandboxed-refused,managed-denywrite-path-forms,mcp-write
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
| **P2-TIER** | `internal/tier/**`(含共享 golden `testdata/managed_forms.json`,其他单元只读)、`internal/layout/resolvewith.go` 及其测试 `internal/layout/resolvewith_test.go` | `go test ./internal/tier ./internal/layout` | — |
| **P2-SANDBOX** | `internal/sandbox/**` | `go test ./internal/sandbox` | — |
| **P2-PS** | `internal/psparse/**` | `go test ./internal/psparse` | — |
| **P2-HERMES** | `internal/adapter/hermes/**`、`internal/adapter/common/`(只新增文件)、`internal/dsl/lint.go`(**只追加** `hermes-stop-continue` 警告码及其判定分支,不改既有警告)与 `internal/dsl/lint_hermes_test.go`(新文件)、`scripts/probe-clients/hermes.sh`(**自带辅助函数,不 source `lib.sh`**)、`docs/facts/hermes-cases.json`、`docs/facts/payloads/hermes/**` | `go test ./internal/adapter/... ./internal/dsl`;`bash scripts/probe-clients/hermes.sh` | — |
| **P2-CLIENTS** | `scripts/probe-clients/{codex,cursor}.sh`、`scripts/probe-clients/lib.sh`(只供 codex/cursor 两个脚本使用)、`docs/facts/codex-static.md` | `bash -n` 语法检查;真实环境检测与拒绝逻辑的脚本自测 | — |
| **P2-RUNNER** | `internal/runner/**`(含 `StartRequest`、`RequestStatus`——`verify --status <request_id>` 的回退与取最差聚合在这里实现) | `go test ./internal/runner` | P2-TIER、P2-SANDBOX |
| **P2-HOOK** | `internal/hook/**`、`internal/model/verifyreq.go` 的函数体与 `internal/model/verifyreq_test.go` | `go test ./internal/hook ./internal/model` | P2-REDACT |
| **P2-REPORT** | `internal/report/**` | `go test ./internal/report` | P2-REDACT |
| **P2-CC** | `internal/adapter/claudecode/**` | `go test ./internal/adapter/...` | P2-M0B |
| **P2-INSTALL** | `internal/install/**`(测试只读 `internal/tier/testdata/managed_forms.json`) | `go test ./internal/install`;`bash scripts/install-e2e.sh` | P2-TIER、P2-CC |
| **P2-CLI**(集成) | `internal/cli/**`、`cmd/ballast/**`、`scripts/{boundary-t0.sh,boundary-t1.sh,accept-p2.sh}`、`scripts/boundary/**`、`scripts/bench/**`、`scripts/lib.sh`、`scripts/README.md`、`.github/workflows/**`(**只能在三平台 CI 修复分支合入集成分支之后修改**,在其结果上追加 P2 步骤)、`README.md`、`docs/trust-model.md`、`docs/clients.md`;`cmd_verify` 的 `--status` 回退只调用 `runner.RequestStatus` 并打印 | `bash scripts/accept-p2.sh`;`go test ./...` | 全部其他单元;workflows 部分另需 CI 修复分支已合入 |

**共享文件的条目边界**:

- `internal/model/client.go`、`internal/model/capability.go`:P2-CC 只改 Claude 表中的 `ConfigChange` 一行;P2-HERMES 只改 `ClientHermes` 的表项与能力集(含新增 `pre_verify` 行)。其他条目任何单元都不得改。
- `scripts/tools/accutil/`:P2 单元**不**修改;M0b 检查器放在自己的目录。
- `internal/dsl/lint.go`:只有 P2-HERMES 可以改,且只追加(见上表);其他 P2 单元不改 `internal/dsl`。
- `internal/layout/`:只有 `resolvewith.go`(及其测试)归 P2-TIER;`layout.go` 本身在 P2 中不改。
- `scripts/probe-clients/`:`hermes.sh` 归 P2-HERMES,其余归 P2-CLIENTS;两者没有共享文件,可同批开工。

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
| hook 在沙箱外运行(A20,T1 地基) | 未核验 | P2-M0B `hooks-run-outside-sandbox`,无需凭据(前提 `nocred-events` 通过,否则降为需凭据) |
| denyWrite 对 Bash 的效果、permissions.deny 对 Edit 的效果、`allowUnsandboxedCommands`、denyWrite 路径写法、MCP 写入 | 【需凭据】 | 用户提供凭据后 `scripts/probe-m0b/run.sh --with-credentials …`;可选 §6.6 模拟模型 |
| MCP 白名单键名 | 未核验 | P2-M0B `mcp-allowlist-nocred` |
| bwrap 在 Docker 中可用所需的设置 | 设计期已实测(Debian 12:seccomp、apparmor、systempaths 三项 unconfined) | P2-M0B `runner-bwrap-in-docker` 固化为用例 |
| bwrap 在 Ubuntu 24.04 CI(user namespace 受 AppArmor 限制)中可用所需的设置 | 未核验 | P2-SANDBOX、P2-CLI(CI) |
| macOS 非文件逃逸通道(LaunchServices、AppleEvents、`launchctl submit`)能否被 `DarwinEscapeDenials` 关闭 | 未核验 | P2-SANDBOX 集成测试;关不住就改为 `(deny default)` 加白名单 |
| `RepoMandatoryDeny` 与 Claude sandbox-runtime "危险文件"清单是否一致 | 【静态依据待核】 | P2-SANDBOX 对照源码,只能追加 |
| Linux 上自定义位置的 unix socket(家目录下的 docker.sock 等)仍可从沙箱内连接 | 残余风险(已登记) | 部署指南提示管理员;`runner_network:"deny"` 只屏蔽 `/run`、`/var/run` 与两个环境变量 |
| macOS 上 passwd 查询家目录的延迟(已锁定主机每次 hook 调用一次) | 未核验 | P2-TIER 基准;超出 B10 预算时按进程外缓存另议(契约变更) |
| macOS `sandbox-exec` 的长期可用性(Apple 已标记弃用) | 风险 | P2-SANDBOX 探测失败即按 §3.4 退化 |
| Hermes 协议(载荷、输出解析、阻断语义) | 【静态依据】 | P2-HERMES 离线 `hooks test` |
| Hermes 真实会话中的触发时机 | 【需凭据】 | 用户自行补测 |
| Codex、Cursor 全部单元格 | 【需凭据】 | §7.2、§7.3 的补测命令 |
| boundary-t1 中 agent 实际尝试的各项 | 【需凭据】 | `scripts/boundary-t1.sh --with-credentials` |

## 12. 风险

1. **无凭据事件不触发**:若 `nocred-events` 失败,M0b 大部分用例变为 needs_credentials,T1 的托管行为仍只有文档依据;层级判定的条件 5 照文档实现,并在 doctor 中标注"托管行为未实测"。
2. **sandbox-exec 弃用**:macOS 未来版本移除时,macOS 上 hook 代起的 verify 全部变 infra(origin=hook 必须沙箱),人工 verify 只能走 `prefer`/`off`,T1 证据只能在 Linux 上产生;届时评估复用 Claude 的 sandbox-runtime。
3. **bwrap 与 user namespace**:部分发行版或容器默认禁用;此时 hook 代起的 verify 全部变 infra(与 `runner_sandbox` 无关),doctor 会给出 fail 与修复提示。
4. **Linux 上 `W` 内不存在的受保护路径**只能事后检查(§3.4);测试命令在运行中创建又删除的情况检测不到,登记为已知边界(仓库外的受保护路径不受此影响:根绑定只读)。
5. **ConfigChange 只审计**:它不能防止篡改,也不覆盖托管刷新(A16);T0 下仍以 `no-tamper` 为主信号。
6. **Hermes `pre_verify` 映射**会改变能力矩阵,只有取证确认后才落地。
7. **默认拒写会让部分测试失败**(写家目录下的构建缓存、全局包目录):这是可见的 `fail`,不是安全问题;管理员按需在 `runner_allow_write` 中加入缓存目录,`org render` 的注释给出常见例子。
8. **重定向锁改变已锁定主机上的行为**:在声明 T1 的主机上无法再用 `BALLAST_HOME` 等变量做本地实验;这是有意的,实验应在未部署 org.json 的机器或容器里进行。

## 13. 修订记录

### v1.1(2026-10-16,第 1 轮独立审核)

| 审核项 | 级别 | 处理 | 涉及 |
|---|---|---|---|
| runner 沙箱"默认全允许、只拒受保护路径",hook 代起 verify 因而成为逃逸原语(写 shell rc、`.git/hooks`、`~/.claude.json`,联网外传;重命名祖先目录可替换受保护文件,审核在 macOS 上实测成功) | blocker | **改为默认拒写**:可写集合 = 仓库 + git dir + job 临时目录 + `runner_allow_write`;集合内再拒受保护路径与 `RepoMandatoryDeny`/`GitDirMandatoryDeny`;受保护路径在可写集合内的各级祖先固定(macOS literal 拒写,Linux 变成挂载点);网络默认 deny;macOS 非文件逃逸通道单列。设计期在本机 macOS 与 Debian 12 容器里实测了改后的规则(§3.4 表),其中也证实了 Linux 上"未固定的祖先可被重命名"。新增集成用例清单,boundary-t1 的 `t1-runner-sandbox` 覆盖全部 Linux 条目 | §3.1、§3.3、§3.4、§5.3、`internal/sandbox`、`model.NetworkPolicy`、`tier.OrgConfig` |
| T1 下 `prefer` 无后端与 `off` 时,hook 代起的测试在沙箱外不加包装执行,新鲜度又不按 tier 过滤 | major | origin=hook **一律必须沙箱**,不可用即不执行(infra、`sandbox_unavailable`),不受 `runner_sandbox` 影响;`runner_sandbox` 只管 origin=cli;策略表按 origin 重写;`sandbox.Decide` 增加 origin 参数;P2-RUNNER 与 boundary-t1(`t1-sandbox-missing` 在 prefer/off 下重跑)加断言;新鲜度裁决写明三条前提 | §3.3、§3.4、§3.6、§5.3、`model/tier.go`、`sandbox.Decide` |
| 条件 2 只降到 T0,而降级本身就是攻击目标(org 规则与 no-tamper 改从 agent 可控目录读取) | major | **重定向锁**:默认托管目录下存在 root 所有、声明 T1 的 org.json 时,生产构建忽略三个重定向变量,家目录取自 passwd;tier 只读编译期默认托管目录;审计 `env_redirect_ignored`、doctor warn;boundary-t1 `t1-env-override` 改为断言 org 规则仍生效、伪造证据不被采信;trust-model 补充降级路径 | §3.2、§3.3、§5.3、`tier.Locked`、`layout.ResolveWith`、`docs/trust-model.md` |
| A20(hook 在沙箱外)不需要模型,却被归为需凭据 | major | `hooks-run-outside-sandbox` 移入无凭据用例(前提 `nocred-events`),写明方法、对照组与"fail 时暂停 P2-RUNNER/P2-HOOK";无凭据 14 个、需凭据 5 个;§11 与 boundary-t1(`t1-hooks-outside-sandbox`)同步 | §3.1、§5.3、§6.4、§6.5、§11 |
| 条件 5 的"覆盖"没有定义,`ProtectedPaths` 与 org render 的写法不同,`Detect` 拿不到 repo | major | 冻结纯函数 `tier.ManagedForms(lay, self, extra)`,输出与 org render 逐字一致的 `~/`、`./` 字符串;覆盖 = 精确字符串集合包含;共享 golden `internal/tier/testdata/managed_forms.json`;`ProtectedPaths` 只用于 runner 沙箱与 doctor 列表 | §3.2、§3.5、`internal/tier` |
| 所有权缺口:`internal/dsl/lint.go` 无主;"只允许 runner import sandbox"与 CLI 注入矛盾;`lib.sh` 依赖;workflows 与 CI 修复分支冲突;`--status` 回退归属 | minor | lint.go 只追加,归 P2-HERMES;措辞改为"只有 runner 和 cli";`hermes.sh` 自带辅助函数;workflows 在 CI 修复分支合入后修改;新增 `runner.RequestStatus`,归 P2-RUNNER | §2.2、§3.3、§9、`internal/runner/t1.go` |
| 条件 5 以 `command == Self` 字面比较,符号链接路径会被误判 | minor | 两边先 `EvalSymlinks`,只接受绝对路径;`ManagedForms` 用 hook 条目原样字符串;detect.json 加符号链接与相对路径两行 | §3.2、`internal/tier` |
