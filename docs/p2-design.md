# Ballast P2 设计与契约增补(冻结版 v1.5)

> 状态:**已冻结**(2026-10-16;v1.1、v1.2、v1.3、v1.4、v1.5 分别为第 1、2、3、4、5 轮独立审核修订,变更清单见 §13)。本文是 `docs/architecture.md` **v2.0**(当前 v2.0.5)的配套文档,与计划 `docs/ballast-plan.md` 末尾的 "v0.5 P2 计划" 一起,构成 P2 各实现单元唯一可依赖的契约。
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
| `internal/tier` | L2(model、sys、layout) | 是(`Locked` 每次调用;判定经 `ports.TierProbe`,惰性) | P2-TIER | T0/T1 判定;重定向锁;托管配置字面形式 `ManagedForms` 与受保护绝对路径 `ProtectedPaths`;低层沙箱削弱检查 `WeakenedSandboxKeys`/`RepoSandboxWeakened` |
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
- **新增 `TestOSUserConfined`**:`os/user` 只能由 `internal/sys/passwd*.go` import,任何地方都不得调用 `user.Current`(纯 Go 模式下查询失败会退回 `$HOME`/`$USER`,等于悄悄撤销重定向锁)。
- **新增 `TestBinaryNoNet`**:`go list -deps ./cmd/ballast` 不得含 `net`/`net/*`。这比"热路径不联网"更严:整个 ballast 二进制都不链接网络栈(当前已满足),同时把 `net/http` 的包初始化挡在每次 hook 调用之外。

### 2.3 契约追加清单(全部是追加,已在骨架中落地)

| 位置 | 追加 | 用途 |
|---|---|---|
| `model/tier.go`(新) | `SandboxPolicy`(require/prefer/off,只管 origin=cli)、`NetworkPolicy`(deny/allow;v1.4 增加 `loopback`,且只管 IP)、`WeakenedProjectPolicy`(v1.4,audit/deny)、`TierMissing*` 常量(含记录用的 `TierMissingRunnerSandbox`;v1.3 新增 `TierMissingLowerSandbox = "lower_sandbox_weakened"`)、`TierInfo`(含 `Network`、`RunnerAllowWrite`、`Locked`) | 层级判定结果 |
| `layout/resolvewith.go`(新) | `Options{IgnoreRedirects, PasswdHome}`、`ResolveWith` | 重定向锁(§3.2) |
| `sys/passwd.go`、`passwd_unix.go`、`passwd_other.go`(新,归 P2-TIER) | `LookupHome(uid)`、`Euid()`、`ErrNoPasswdEntry`、`ErrPasswdUnsupported` | 已锁定主机的家目录来源(§3.2"家目录") |
| `tier`(新包内) | `Locked`、`LockState`、`LockedHome`/`HomeOptions`/`HomeFrom*`、`ManagedForms`(+ 共享 golden `internal/tier/testdata/managed_forms.json`)、`OrgConfig.RunnerNetwork/RunnerAllowWrite/HomeRoot`、`WeakenedSandboxKeys`、`RepoSandboxWeakened`(v1.3);v1.4:`WeakenedSandboxKeys(doc, layer)` 改签名并新增 `SettingsLayer`(`LayerUser`/`LayerRepo`)、`ProjectChecker`、`OrgConfig.WeakenedProject`、`HomeOptions.Timeout`/`DefaultLockedHomeTimeout`/`ErrLockedHomeTimeout` | §3.2、§3.3、§3.4、§3.5 |
| `sandbox`(新包内) | 默认拒写模型:`Spec.AllowWrite/RepoRoots/TmpDir/Network/Home`、`RepoMandatoryDeny`、`AncestorPins`、`ValidateSpec`、`PostCheck`、`DarwinEscapeDenials`、`CacheVars`/`CacheEnv`;`Decide` 增加 `origin` 参数。v1.3:`RepoMandatoryDeny` 改为任意深度并增加 `.git`、`.git/config.worktree`;`GitDirMandatoryDeny` 增加 `commondir`、`modules/**/…`、`worktrees/*/…`;新增 `MatchMandatory`、`ResolveGitDirs`、`ScanMandatory`、`MaxMandatoryMatches`、`Quarantine`/`QuarantineEntry`、`Wrapped.Baseline`/`NewSession`;`ValidateSpec(fsys, spec)`、`PostCheck(fsys, spec, w) (found, err)` 改签名;v1.4:`HostEnv`、`Spec.LoaderEnv`,本地 socket 隔离与网络策略脱钩 | §3.4 |
| `runner/t1.go` | `RequestStatus`(`verify --status <request_id>` 回退与"取最差"聚合) | §3.3 |
| `model/verifyreq.go`(新) | `VerifyRequest`、`ParseVerifyRequest`、`VerifyOrigin`、`VerifyRequestFile`、`RequestedJob`、`HookStartRequest`(v1.4 增加 `ProjectDir`)、`HookStartArgv`(v1.4 增加 `--project-dir`)、`ParseHookStartArgv` | hook 代起 verify 的识别与子进程参数 |
| `model/evidence.go` | `VerifyRecord.Sandbox`、`Origin`、`TierMissing`(omitempty) | 证据标注沙箱与来源 |
| `model/client.go` | `EventConfigChange`;Claude 表新增 `ConfigChange` 行(`Install:false, Verified:false`) | ConfigChange 审计 |
| `model/audit.go` | `AuditConfigChange = "config_change"`;v1.4:`Diag*` 常量与闭合枚举 `DiagNames`(§3.3"诊断名清单") | 审计行类型;report 只认这些 diag 名 |
| `ports/ports.go` | `TierProbe{ Probe() model.TierInfo }`;v1.4:`ProjectSandboxChecker{ Weakened(dir) []string }` | hook/runner 取层级;hook 检查会话项目层(§3.3) |
| `layout/layout.go` | `OrgConfig`(`<托管目录>/ballast/org.json`)、`RequestsDir`(`~/.ballast/requests`)、`QuarantineDir`(`~/.ballast/quarantine`,v1.3) | 新文件位置 |
| `hook.Deps` | `Tier ports.TierProbe`(nil ⇒ T0);`RedirectsIgnored []string`(nil ⇒ 没有被忽略的变量);v1.4:`IDs sys.IDGen`(nil ⇒ 代起路径按 spawn 失败处理)、`ProjectSandbox ports.ProjectSandboxChecker`(nil ⇒ 不检查)、`WeakenedProject model.WeakenedProjectPolicy` | §3.3;§3.2 |
| `runner.Options` | `Tier ports.TierProbe`(nil ⇒ T0、SandboxOff) | §3.4 |
| `runner/t1.go`(新) | `StartRequest(opt, model.HookStartRequest)` | §3.3 |
| `model/event.go`(v1.5) | `Event.ConfigFile`(ConfigChange 变更的文件路径;payload 的 `source` 复用既有 `Event.Source`,不另设 `ConfigSource`) | §5.1;P2-CC 只填值,P2-HOOK 只读取(§9) |
| `adapter/claudecode`(v1.5) | `configChangeFileKey`、`fillConfigChange`(映射占位,当前为 no-op;M0b 取得 payload 后由 P2-CC 填实) | §5.1 |
| `model/evidence.go`(v1.5) | `VerifySpec.InputsUntracked`(project.json `verify[].inputs_untracked`,默认空);`VerifyRecord.Workspace`(`WorkspaceWorktree`/`WorkspaceCopy`)、`VerifyRecord.InputsHash` | §3.4"job 私有树" |
| `layout/layout.go`(v1.5) | `WorkDir`(`~/.ballast/work/<job_id>/`) | §3.4"job 私有树" |
| `runner/tree.go`(v1.5,新) | `Materialize`、`TreeSEnd`、`CleanupTree`、`Tree`、`ErrTreeSubmodule`、`ErrInputsTooLarge`、`ErrMaterialize`、`MaxUntrackedInputFiles`/`MaxUntrackedInputBytes` | §3.4"job 私有树""收尾顺序" |
| `sandbox`(v1.5) | `Spec.Tree`、`Spec.WorkRoot`(副本是 W 在 `~/.ballast` 内唯一的例外);`DarwinMachAllow`、`DarwinMachForbidden`;`DarwinEscapeDenials` 改为 IPC 默认拒绝片段(签名不变);darwin profile 增加 `network-bind`/`network-inbound` 规则;包注释冻结收尾顺序 | §3.4 |
| `tier`(v1.5) | `ManagedForms` 增加 `**/` 任意深度形式(共享 golden 同步);`DefaultWeakenedProject`(条件契约)、`EffectiveWeakenedProject`;条件 5 增加 `CLAUDE_CONFIG_DIR` 检查 | §3.2 |
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

**【决定】T1 当且仅当以下六条同时成立**,否则为 T0,并在 `TierInfo.Missing` 中列出每一条不成立的原因(常量见 `model/tier.go`):

| # | 条件 | Missing id |
|---|---|---|
| 1 | GOOS 为 darwin 或 linux(WSL2 报告 linux) | `os` |
| 2 | 测试重定向变量 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT` **没有生效**。在已锁定的主机上(见下文"重定向锁")生产构建忽略它们,所以它们不可能生效;只有未锁定的主机上它们才可能生效,此时判 T0 | `env_override` |
| 3 | `Self`(绝对路径,解析符号链接后)是 uid 0 拥有、组和其他用户不可写的普通文件,且直到 `/` 的每一级父目录同样 root 所有、组和其他不可写 | `binary_root_owned` |
| 4 | **编译期默认托管目录** `ManagedDirFor(GOOS)/ballast/org.json`(不看 `Layout.ManagedDir`)是 root 所有、组和其他不可写的普通文件,通过 `LoadOrgConfig` 校验,且 `"tier":"T1"`;并且(v1.4)它的**父目录链**——`<托管目录>/ballast`、`<托管目录>` 及直到 `/` 的每一级(托管目录先 `EvalSymlinks`,例如 macOS 的 `/etc → /private/etc`;每级 `Lstat`)——都是 root 所有、组和其他不可写的目录 | `org_config` |
| 5 | `ManagedDirFor(GOOS)` 下合并后的 Claude 托管设置(每个文件 root 所有、组和其他不可写,`managed-settings.d` 目录本身与条件 4 的父目录链同样如此;合并顺序以 M0b `managed-d-merge-*` 的结论为准)满足:`allowManagedHooksOnly==true`;存在一个 hook 条目,其 `command` 是**绝对路径**、`EvalSymlinks(command)==EvalSymlinks(Self)`,且 `args[0:2]==["hook","--client"]`;`sandbox.enabled==true`、`allowUnsandboxedCommands==false`、`failIfUnavailable==true`;`allowManagedPermissionRulesOnly==true`;并且令 `(dw, pd) = tier.ManagedForms(lay, command, org.protected_extra)`(`command` 取该 hook 条目里**原样写的字符串**),`dw` 的每个字符串都逐字出现在 `sandbox.filesystem.denyWrite` 中,`pd` 的每个字符串都逐字出现在 `permissions.deny` 中;托管沙箱自身的 `excludedCommands`、`network.allowUnixSockets` 缺省或为 `[]`,`enableWeakerNestedSandbox`、`network.allowAllUnixSockets` 缺省或为 false(否则 `sandbox`;`allowedDomains`、`allowWrite` 属管理员自行决定);**v1.5**:`CLAUDE_CONFIG_DIR` 已设置且 Clean 后不等于 `<home>/.claude` ⇒ `sandbox_deny_write`(Claude 实际加载的用户层不在字面形式 `~/.claude/settings*.json` 之内,托管配置挡不住 agent 改写它,条件 6 只是时点检查) | `managed_settings`、`managed_hooks`、`allow_managed_hooks_only`、`sandbox`、`sandbox_deny_write`、`permissions_deny`、`managed_permissions_only` |
| 6 | **低层不削弱托管沙箱**(v1.3,保守做法,直到 M0b `lower-sandbox-merge` 给出结论):对用户层每个 `<claude dir>/settings*.json`(`<claude dir>` 取 `$CLAUDE_CONFIG_DIR`,**并且**总是另查 `<home>/.claude`)调用 `tier.WeakenedSandboxKeys(doc, LayerUser)`,结果都必须为空;文件不存在没问题,读不了或解析失败算削弱。`Detect` 看不到项目:项目层与本地层 `<dir>/.claude/settings*.json` 由 hook 在会话首个事件对 `$CLAUDE_PROJECT_DIR` 检查(§3.3"项目层削弱"),由 runner 对 job 仓库**和**请求带来的项目目录再查(§3.4),都用 `tier.RepoSandboxWeakened`(`LayerRepo`)。**这一条只约束 `Detect` 的结论;T1 证据的保证另有全机前提,见 §3.3 新鲜度前提 3 与 trust-model §2.3** | `lower_sandbox_weakened` |

任何输入读不了或解析失败 ⇒ T0 + `unreadable`(失败时取保守一侧;条件 6 的低层文件读不了时记 `lower_sandbox_weakened`)。

**条件 6 的理由与规则(【决定】)**:Claude 的数组设置一般跨层合并,托管层写 `excludedCommands: []` 挡不住低层追加;`allowManagedPermissionRulesOnly` 只约束 `permissions`,不约束 `sandbox`。只要任何一层有 `excludedCommands: ["npm"]` 或 `["docker"]`,agent 就能在沙箱外运行 `npm run x` 或 `docker run -v ~:/h …`,直接写 `~/.ballast/evidence`;`network.allowUnixSockets: ["/var/run/docker.sock"]` 同理。项目层 `.claude/settings.json` 随仓库提交、随 clone 或 pull 进来,会话内的 denyWrite 管不到它的来源。`tier.WeakenedSandboxKeys(doc, layer)` 的规则冻结在函数注释里,**每一层都是白名单**(v1.4 起顶层也是):任何键取 `null`/`[]`/`{}`/`false` 都无害;**顶层**两层共同的无害键只有 `$schema`、`model`、`includeCoAuthoredBy`、`cleanupPeriodDays`、`outputStyle`、`spinnerTipsEnabled`、`alwaysThinkingEnabled`、`hooks` 与 `disableAllHooks`(受 `allowManagedHooksOnly` 约束,条件 5)以及向下检查的 `permissions`、`sandbox`;`env`、`statusLine`、`apiKeyHelper`、`awsAuthRefresh`、`awsCredentialExport`、`otelHeadersHelper`、`enabledPlugins`、`extraKnownMarketplaces` **只在用户层无害**——用户层文件在 denyWrite 与 permissions.deny 中、由人编写,这些键只会在沙箱外运行人写的命令或设置人写的环境,等同于人自己的工具;**在项目层与本地层一律报告**(内容随仓库进来;`env` 会进入 Claude、托管 hook 以及 Ballast 在沙箱外启动的每个进程的环境——`DYLD_INSERT_LIBRARIES`/`LD_PRELOAD` 之类的加载器变量、`GIT_CONFIG_*` 注入;helper 键会在沙箱外执行命令;它们在项目层是否生效【未核验】,M0b `lower-sandbox-merge` 记录);其余顶层键(如 `enableAllProjectMcpServers`、`enabledMcpjsonServers`)两层都报告。`permissions` 内 `allow`/`deny`/`ask` 无害(受 `allowManagedPermissionRulesOnly` 约束),`defaultMode` 只有 `default`/`acceptEdits`/`plan` 无害,`disableBypassPermissionsMode` 无害,`additionalDirectories` 非空报告,其他键报告。`sandbox` 内只有 `enabled:true`、`allowUnsandboxedCommands:false`、`failIfUnavailable:true`、`autoAllowBashIfSandboxed`(任意布尔)、`filesystem.denyWrite`/`denyRead`(只会更严)以及其他键的 `null`/`[]`/`{}`/`false` 视为无害;其余一律报告,点名的包括 `excludedCommands`、`network.allowUnixSockets`、`network.allowAllUnixSockets`、`network.allowLocalBinding`、`network.allowedDomains`、`network.httpProxyPort`/`socksProxyPort`、`filesystem.allowWrite`、`enableWeakerNestedSandbox`,以及未来新增、尚未分类的键(失败时取保守一侧)。M0b 证明某个键不跨层合并或被托管层覆盖之后,才能从白名单外移到白名单内(契约变更,只放宽有实测依据的键)。

**"覆盖"的定义(【决定】)**:覆盖就是**精确字符串集合的包含关系**,不做任何 glob 或路径推理。`tier.ManagedForms(lay, self, extra)` 是纯函数,输出与 `org render` 逐字一致的字符串:家目录条目用字面 `~/` 形式,仓库条目用字面 `./` 形式,与具体用户和仓库无关(同一份托管配置对主机上每个用户、每个仓库都适用),二进制目录取 `self` 原样字符串的目录部分(不解析符号链接)。`org render` 和 `Detect` 共用这个函数;共享 golden `internal/tier/testdata/managed_forms.json` 锁住两边(P2-TIER 断言函数输出,P2-INSTALL 断言 `org render` 的输出)。冻结的输出形状:

```
denyWrite = [dir(OrgRules), "~/.ballast", "./.ballast", "**/.ballast", dir(self), extra...,
             "~/.claude/settings*.json", "./.claude/settings*.json", "**/.claude/settings*.json"]
permDeny  = 对 Edit、Write、NotebookEdit 依次:
             T(dir(OrgRules)/**) T(~/.ballast/**) T(./.ballast/**) T(**/.ballast/**) T(dir(self)/**)
             对每个 extra e:T(e) T(e/**)
             T(~/.claude/settings*.json) T(./.claude/settings*.json) T(**/.claude/settings*.json)
```

`extra` 同时写 `T(e)` 与 `T(e/**)`,因为 org.json 不区分文件与目录。重复项保留第一次出现的位置。`**/` 两组是 v1.5 新增的。

**任意深度形式与条件契约(【决定】,v1.5)**:字面的 `./` 形式只保护会话目录这一层。agent 用自己的 Bash 或 Edit 可以在任意子目录写 `<sub>/.claude/settings.json`(例如 `sandbox.excludedCommands:["npm"]`)或 `<sub>/.ballast/…`;会话在 monorepo 子目录启动时,还能写仓库根的 `.claude/settings*.json` 与 `.ballast/project.json`。这些文件可以提交、推送,扩散到同事的机器,之后在这些目录启动的会话就带着削弱的项目层,能绕过 runner 直接写全局证据。所以"管理员一次性确认主机上没有削弱的项目层"不足以维持全机前提(§3.3 前提 3):agent 自己就能埋下。处理:

1. `ManagedForms` 追加 `**/.ballast`(权限规则为 `T(**/.ballast/**)`)与 `**/.claude/settings*.json`,共享 golden 同步(P2-TIER 与 P2-INSTALL 开工前已冻结)。
2. 这些写法在托管 denyWrite 与 permissions.deny 中是否生效、锚点是什么【未核验】,由 M0b 新用例 `managed-denywrite-anydepth` 判定(§6.4)。
3. **条件契约**(冻结在 `tier.DefaultWeakenedProject`):该用例为 `pass`——两组写法都能拒绝会话目录下任意深度的 Bash 与 Edit/Write 写入,**并且**在子目录启动的会话里也能拒绝仓库根的写入——⇒ T1 下 `weakened_project` 缺省为 `audit`;为 `fail`、`needs_credentials` 或 `unverifiable`(未证明有效)⇒ 缺省为 `deny`,`org render` 的默认输出同步。骨架按"未证明"冻结为 `deny`;P2-CLI 集成时按 `docs/facts/m0b-cases.json` 定稿常量与 P2-INSTALL 的 `org render` golden,改为 `audit` 必须有该 pass 行,并记入 §13。org.json 显式写的值始终优先;CLI 向 `hook.Deps.WeakenedProject` 填 `tier.EffectiveWeakenedProject(org)`,生产中不为空。
4. `deny` 让埋下的削弱项目层失效:之后在该目录启动的会话,首个工具调用起就被拒(§3.3"项目层削弱");doctor `tier.repo_sandbox_weakened` 点名文件与键。它仍挡不住 Claude 启动时、工具调用之外执行的设置,这一类留在全机前提里。

另外,Claude 文档对权限规则的路径写法区分 `//`(文件系统绝对路径)、`~/`、单斜杠开头(相对项目根或设置文件)与 `./`【静态依据待核】。若 M0b `managed-denywrite-path-forms` 证明 permissions.deny 中单斜杠开头的条目(org 规则目录、二进制目录、绝对路径的 `protected_extra`)不按绝对路径解释,`ManagedForms` 的这些条目改为 `//` 形式(契约变更,golden 同步)。

**符号链接**:管理员传给 `org render --binary` 的可能是符号链接路径(如 `/usr/local/bin/ballast → /usr/local/libexec/ballast/ballast`)。比较 hook 命令与 `Self` 时两边都先 `EvalSymlinks`,且只接受绝对路径;`ManagedForms` 用 hook 条目里原样的字符串,所以与 `org render` 的输出逐字相同。`testdata/detect.json` 含"hook 命令是指向 Self 的符号链接 ⇒ T1"与"hook 命令是相对路径 ⇒ `managed_hooks`"两行。

**重定向锁(【决定】)**:如果编译期默认的 `ManagedDirFor(GOOS)/ballast/org.json` 存在、root 所有、组和其他不可写、父目录链满足条件 4(v1.4)、通过校验且声明 `tier:"T1"`,主机即为**已锁定**(`tier.Locked`)。已锁定时,生产构建:

- 忽略 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT`(`layout.ResolveWith(env, Options{IgnoreRedirects:true})`);
- 家目录由 `tier.LockedHome` 决定(见下文"家目录"),不直接采信 `$HOME`;决定不了时 layout 解析失败(hook 按 B7 放行并且无副作用,doctor `tier.locked_home` 报 fail 并给出修复提示)——已锁定主机上家目录不确定属于配置错误,不能因此退回采信 `$HOME`;
- 变量存在时,CLI 把被忽略的变量名填进 `hook.Deps.RedirectsIgnored`(固定顺序 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT`,只含非空的;nil 表示没有),hook 在会话的首个事件(`State.StartedAt()` 返回 false)上审计一行 `diag: env_redirect_ignored`,detail `{vars:[变量名]}`,只记名字不记值;并发的首个事件可能各写一行,可以接受。doctor 报 warn。

**家目录(【决定】,P2-TIER 实现,P2-CLI 注入)**:已锁定主机上 `layout.Options.PasswdHome` 由 CLI 设为对 `tier.LockedHome(HomeOptions{FS, Env, Owner, Euid: sys.Euid(), HomeRoot: org.HomeRoot})` 的闭包(org 来自 `tier.LockState`,不重复读 org.json)。规则冻结在 `tier.LockedHome` 注释中:

0. **有时限**(v1.4):`LockedHome` 在 `WireHook` → layout 解析阶段执行,早于 hook 的 150ms watchdog,所以自带时限 `HomeOptions.Timeout`(默认 `tier.DefaultLockedHomeTimeout` = 50ms):查询在 goroutine 中进行,超时立即返回 `ErrLockedHomeTimeout`,不再尝试 home_root 规则(慢不等于"查不到")。layout 因此失败,hook 按 B7 放行(exit 1、stdout 空、stderr 一行说明超时;家目录未知,无法审计);doctor `tier.locked_home` 对同一查询计时,超过时限报 fail。

1. `sys.LookupHome(euid)` 成功 ⇒ 用它,`$HOME` 即使不同也不看。各平台实现冻结在 `internal/sys/passwd.go`:
   - **darwin**:`os/user.LookupId`。即使 `CGO_ENABLED=0`,标准库也经 syscall 跳板调用 libSystem 的 `getpwuid_r`,走 Open Directory(本地、移动与目录账户都能查到)。设计期在本机实测(本地账户,`CGO_ENABLED=0`,每次新进程):p50 ≈ 0.9ms,p95 ≈ 1.3ms;同进程第二次 ≈ 0。未在本地缓存的目录账户延迟【未核验】。**v1.5**:目录账户(AD/LDAP 绑定、未缓存的移动账户)的查询经 opendirectoryd 转发到目录节点,可能联网或阻塞,与 Linux 的 NSS-LDAP 属于同一类"热路径联网",见下文部署选项 ④。
   - **linux**:同样 `os/user.LookupId`。`CGO_ENABLED=0` 构建(发布默认,保持静态与可移植)只解析 `/etc/passwd`;cgo 构建走 NSS(sssd/LDAP),只在 NSS 由本地缓存提供时可接受(见下文部署选项 ②)。"unknown user id" 映射为 `sys.ErrNoPasswdEntry`。
   - 其他平台:`ErrPasswdUnsupported`(`Locked` 在这些平台上恒为 false,生产不会调用)。
   - **禁止 `os/user.Current`**:纯 Go 模式下它在查询失败时退回 `$HOME`/`$USER`,正是重定向锁要忽略的输入(archtest `TestOSUserConfined`)。
2. 查询失败且不是 `ErrNoPasswdEntry`,或 org.json 没有 `home_root` ⇒ 失败。
3. 只有 `ErrNoPasswdEntry` 且 org.json 配置了 `home_root` 时,才按 **home_root 规则**采信 `$HOME`,须同时满足:(a)绝对路径且已 Clean;(b)`dir($HOME)` 恰好是某个 `home_root` 条目(只认直接子目录,嵌套布局写更深的父目录,如 `/home/corp.example`);(c)该条目及其直到 `/` 的每一级都是 root 所有、组和其他不可写的目录(Lstat,不跟随符号链接)——只有 root 能在其下创建或重命名子目录;(d)`$HOME` 本身是目录、不是符号链接、属于有效 uid、组和其他不可写。来源记为 `home_root`,doctor 显示来源。

为什么 home_root 规则伪造不了:agent 与用户同 uid,但不是 root,无法在 root 所有的 `home_root` 下新建或改名目录;它能指向的只有 root 事先分配给这个 uid 的目录。它比 passwd 弱(若 root 给同一 uid 分配了多个这样的目录,agent 可以在其间切换),所以是管理员显式开启的退路,只在 passwd 查不到时生效。

**企业机群(sssd/LDAP 用户)的部署选项**,doctor 与 `org render` 的提示按此顺序给出:① 在 org.json 写 `home_root`(`org render --home-root /home`,**推荐**);② 使用启用 cgo 的构建(走 NSS;Ballast 不提供官方 cgo 构建,由管理员自行构建)——**仅在 NSS 由本地缓存提供时可用**(sssd 的本地缓存或 nscd):在 `nss_ldap` 或未缓存的 sssd 配置下,每次 hook 调用的 passwd 查询可能联网或阻塞,违反"热路径不联网"的禁区;doctor 的 `tier.locked_home` 在 cgo 构建下给出这条提示;③ 把用户加入本地 `/etc/passwd`;④(v1.5)**macOS 目录账户**:`getpwuid_r` 经 opendirectoryd 查询 AD/LDAP 节点,未缓存时可能联网或阻塞,同样违反热路径不联网;只有启用移动账户(mobile account)或目录服务的本地缓存、查询能在本地完成时才符合,50ms 时限只是兜底(超时按 B7 放行),doctor `tier.locked_home` 在 darwin 上对查询计时,超过 5ms 时附注这一条。都不做时,该用户的每次 hook 调用都会因 layout 失败而放行——这是**可见的** fail(doctor `tier.locked_home`、`org render --write` 的预检),不是静默的。

`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME` 不受影响:它们重定向的是客户端本身,而不是 Ballast 的信任输入。`tier.Locked` 每次 hook 调用都执行,代价是一次 `Lstat`,文件存在时再加父目录链的几次 `Lstat` 与读几百字节,目标 ≤ 30µs(`BenchmarkLocked`;v1.4 因父目录链从 20µs 放宽);已锁定时还要加一次 `LockedHome`(一次 passwd 查询,home_root 规则另加不超过路径深度加一次 Lstat),由 `BenchmarkLockedHome` 与 bench 场景 `13` 覆盖(§3.6)。测试不受影响:需要重定向的测试从不在真实托管目录下放 root 所有的 org.json;锁本身的测试用假 FS 和假 `Owner`。

**为什么 agent 伪造不了,也降级不了**:条件 3–5 要求写 root 所有的文件(v1.4 起连同托管目录的父目录链,托管目录被误配为组可写时判 T0 而不是被组成员替换);条件 6 只能被"写进削弱值"破坏,结果是判为 T0(不会误判为 T1)。用户层 `~/.claude/settings*.json` 在 denyWrite 与 permissions.deny 的字面形式里(`CLAUDE_CONFIG_DIR` 指向别处时判 T0,v1.5),会话目录这一层的 `./.claude/settings*.json` 同样;**其他目录的项目层不在字面形式之内**(v1.5 更正此前"项目层在会话内本来就拒写"的说法):agent 可能在子目录或仓库根埋下削弱的项目层,`**/` 任意深度形式是否挡得住待 M0b,未证明前由条件契约下的 `weakened_project:"deny"` 兜底(上文"任意深度形式");拥有 root 的 agent 不在任何 Ballast 边界之内(trust-model 声明)。**降级同样是攻击目标**:如果 agent 能把重定向变量塞进 Claude Code 进程的环境(例如经由一次 runner 逃逸写 shell rc),判定虽然只是"降到 T0",org 规则和 `no-tamper` 却会从 agent 可控的目录读取。重定向锁关闭了这条路:在声明 T1 的主机上这些变量一律无效,org 规则、证据、状态始终在真实位置;而在未声明 T1 的主机上,本来就是 T0,降级没有意义。判定只决定"路由"和"证据上写什么 tier";**证据的 tier 由 runner 自己再判定一次**(§3.4),hook 判断失误不会让证据变成 T1。

**`org.json`**(新文件,`layout.OrgConfig`,管理员下发,`org render` 生成):

```json
{"v": 1, "tier": "T1", "runner_sandbox": "require", "runner_network": "deny",
 "runner_allow_write": [], "home_root": ["/home"], "weakened_project": "deny",
 "protected_extra": ["~/.config/acme/ci-token"]}
```

校验规则见 `tier.OrgConfig` 注释(版本、枚举值(v1.4:`runner_network` 增加 `loopback`;`weakened_project` 为 `audit`/`deny`)、路径必须绝对或 `~/` 开头;`runner_allow_write` 不得是 `~`、`/`、`/home`、`/Users`、`/root` 之类的家目录或其祖先;`home_root` 必须是已 Clean 的绝对路径、不是 `/`、不含 `~` 与通配符)。校验失败等同于不存在(`org_config`,且主机不锁定)。

`runner_allow_write` **建议保持为空**。其中每一项都等价于"把这个目录交给 agent 可以改写的代码":写进去的内容在 job 结束后仍然存在,又不在内容快照 `C` 里;如果它是共享的构建缓存,之后在沙箱外的人工构建会复用被投毒的条目。runner 已经把常见缓存变量指向每个 job 的临时目录(`sandbox.CacheEnv`,§3.4),默认拒写不会因为缓存而让测试失败,代价只是缓存是冷的。确需持久缓存时,优先用仓库内被 `.gitignore` 的目录,并接受 §11 登记的残余风险。

`Declared`(org.json 声明)为 T1 而判定为 T0 时:`doctor` 报 fail(列出 Missing);hook 遇到规范 verify 时写审计 `diag: tier_mismatch`。只有 root 所有的 org.json 才被采信。

**受保护路径的两种形式**:

- `tier.ManagedForms`:托管配置里的字面字符串(`~/`、`./`,v1.5 起另有 `**/`),供 `org render` 与条件 5 使用,与用户和仓库无关。
- `tier.ProtectedPaths(fsys, lay, self, repo, extra)`:某个用户、某个仓库下的绝对路径,供 runner 沙箱与 doctor 列表使用。顺序固定:org 规则目录、`~/.ballast/`、`<repo>/.ballast/`、二进制目录、`~/.claude/settings*.json`(遵循 `CLAUDE_CONFIG_DIR`)、`<repo>/.claude/settings*.json`、`protected_extra`。每项同时给出原始路径与 `EvalSymlinks` 形式(macOS 的 `/var` → `/private/var`),消费方两种都保护。runner 沙箱还会在此之上追加 `sandbox.RepoMandatoryDeny` 与 `sandbox.GitDirMandatoryDeny`(§3.4)。

**性能**:hook 只在"规范 verify 命令"时调用 `Probe`(极少);一次判定约 10 次 stat 加读一个几 KB 的 JSON,目标 ≤ 200µs(`BenchmarkProbe`)。`Prober` 按进程记忆。

### 3.3 hook 代起 verify

**识别**:`model.ParseVerifyRequest(cl)`,规则冻结在函数注释中。要点:恰好一个节点,没有管道、重定向、包装器、`sudo`、循环、后台 `&`、命令替换、动态词;`basename(argv[0])` 为 `ballast`/`ballast.exe`;`argv[1]=="verify"`;其余每个词匹配 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`(所以任何 flag 都不是规范形式,`--status`、`--check`、`--foreground` 照常执行),最多 16 个名字。

**流程**(只对 `claude-code` 的 PreToolUse、工具为 Bash 或 PowerShell;其他客户端 `MaxTier` 为 T0,不调用 Probe):

1. `ParseVerifyRequest` 不成立 ⇒ 什么都不做,照常求值。
2. 成立 ⇒ `Deps.Tier.Probe()`;nil 或 T0 ⇒ 照常求值(T0 下 agent 自己运行 `ballast verify`,现有行为)。若 `Declared==T1` 而结果为 T0,写 `diag: tier_mismatch`。
3. T1 ⇒
   - 用 `Deps.Clock` + `Deps.IDs`(v1.4 冻结的 `hook.Deps.IDs sys.IDGen`,由 CLI 填入;nil ⇒ 按第 4 步 spawn 失败处理)预分配 `request_id`(job id 格式);
   - `argv = model.HookStartArgv(Self, HookStartRequest{RequestID, Cwd: 事件 cwd, Names, Client, SessionID, AgentID, ProjectDir: $CLAUDE_PROJECT_DIR(绝对路径时;v1.4)})`;
   - `Spawner.SpawnDetached(argv, env, logPath)`,`logPath = <RequestsDir>/<repo_inst>/<request_id>.spawn.log`(不在仓库时 repo_inst 用既有的 `norepo-…` 作用域);
   - 形成一个**合成的 deny 决策**(不是规则命中),与同事件的规则结果按既有裁决合并(deny 最严;其他规则的 reason 按顺序拼接,受预算约束);
   - 审计一行 `diag: verify_hook_start`,detail `{request_id, names, spawn_ok}`;
   - deny reason 模板(冻结):`ballast(T1):已在沙箱外后台启动 verify,job=<request_id>(<名字列表或"全部 required">)。不要重复执行;用 \`ballast verify --status <request_id>\` 查看结果,完成后结果也会在下一次注入中回报。`
4. spawn 失败 ⇒ 仍然 deny(在沙箱里跑也写不了证据),reason 改为"无法在后台启动 verify:<错误>;请让用户在沙箱外执行 `ballast verify <names>`",审计 `diag: verify_spawn_failed`。spawn 失败不是 hook 异常(architecture §10)。

这条链路**不依赖 `updatedInput`**(A24)。子进程是 `ballast verify --hook-start …`,即 Ballast 自身;沙箱包装发生在 runner 进程内部,hook 不 spawn 任何别的东西(热路径约束)。

**子进程侧**(P2-CLI 接线):`verify --hook-start` 用 `model.ParseHookStartArgv` 解析参数,调用 `runner.StartRequest`。它写 `VerifyRequestFile`(`~/.ballast/requests/<repo_inst>/<request_id>.json`):第一个(或唯一一个)job 的 id 就是 `request_id`,其余 job 用新 id;所有记录 `Origin:"hook"`。配置错误(无仓库、无 project.json、未知名字)也写请求文件并带 `error`,这样 `ballast verify --status <request_id>` 能解释原因。`--status` 找不到同名 job 时回退读请求文件:单个 job 时退出码同该 job;多个 job 时取最差结果(fail > timeout > signal > infra > running > pass,superseded 记为 infra),输出逐行列出。**回退与聚合由 P2-RUNNER 实现**(`runner.RequestStatus`,返回逐 job 状态、`Worst` 与 `ExitCode`);P2-CLI 只负责调用、逐行打印并以 `ExitCode` 退出。

**项目层削弱(【决定】,v1.4,P2-HOOK 实现,P2-TIER 提供检查,P2-CLI 接线)**:Claude 实际加载的项目层是会话的项目目录(`CLAUDE_PROJECT_DIR`)下的 `.claude/settings*.json`,它与 job 仓库在 monorepo 子目录或父目录启动时并不相同;而且削弱沙箱的会话可以**绕过 runner** 直接写全局 `~/.ballast/evidence`。所以检查必须落在会话上,不能只靠 runner 给自己的记录降级:

- **何时检查**:client 为 `claude-code`,`hook.Deps.ProjectSandbox`(`ports.ProjectSandboxChecker`,实现为 `tier.ProjectChecker`,即 `tier.RepoSandboxWeakened`)非 nil(CLI 只在已锁定主机上接线)。检查的目录:`$CLAUDE_PROJECT_DIR` 为绝对路径时只查它;未设置时查事件 cwd,以及它所在仓库的顶层(若不同)。
- **会话首个事件**(与 `env_redirect_ignored` 同一时机,`State.StartedAt()` 为 false):结果非空 ⇒ 审计一行 `diag: lower_sandbox_weakened`,detail `{entries:["<basename>:<key>"…]}`(不含路径与值)。并发的首个事件可能各写一行,可以接受。
- **不写会话状态**:审核建议"在会话状态里标记",本设计不采纳——`state.Record` 是 U5 已实现的 v1 格式,为一个标记改格式代价大;这个标记的唯一消费者是下面的 deny 开关,而开关打开时逐事件重查(没有 `.claude` 目录时只是一次 `Lstat`)更简单,也不会因状态损坏而失效。runner 对证据的判定另有自己的检查(§3.4),不依赖 hook 的标记。
- **org.json `weakened_project`**(`model.WeakenedProjectPolicy`,CLI 把 `tier.EffectiveWeakenedProject(org)` 填入 `hook.Deps.WeakenedProject`;缺省值按 v1.5 条件契约 `tier.DefaultWeakenedProject`,M0b 未证明 `**/` 形式有效时为 `deny`,见 §3.2"任意深度形式"):`audit` 只做上面的审计;`deny` 时,hook 对该会话的**每一个** PreToolUse 重新检查,非空就形成合成 deny(与规则结果按既有裁决合并),reason 冻结在 `hook.Deps.WeakenedProject` 注释中,审计 `diag: lower_sandbox_deny`(detail `{tool}`)。deny 覆盖全部工具而不只是 Bash:`permissions.additionalDirectories` 之类的削弱经文件工具生效。它挡不住在工具调用之外起作用的设置(例如 Claude 启动时执行的 helper 命令,项目层是否生效【未核验】),这一类仍落在 trust-model §2.3 的全机前提里。
- **代价**:`audit` 下每个会话只查一次;`deny` 下每个 PreToolUse 多一次 `Lstat`(有 `.claude` 目录时再读不超过两个小文件)。bench 新场景 `14` 覆盖(§3.6)。

**诊断名清单(【决定】,v1.4)**:审计 `type:"diag"` 行的 `detail.diag` 是闭合枚举,冻结在 `model.DiagNames`(`internal/model/audit.go` 的 `Diag*` 常量):既有的 `capability_degraded`、`evidence_open_failed`、`gc_spawn_failed`、`lock_timeout`、`not_a_repo`、`predicate_unknown`、`repo_locate_failed`、`snapshot_fallback`、`state_corrupt`、`state_unreadable`,以及 P2 的 `env_redirect_ignored`、`verify_hook_start`、`verify_spawn_failed`、`tier_mismatch`、`sandbox_quarantine`、`sandbox_unavailable`、`lower_sandbox_weakened`、`lower_sandbox_deny`。发出新的 diag 名先在这里追加(契约变更)。`report` 只按这个清单计数,其他值记入 `other`(§4.4)。

**结果回报**:沿用 M2 的"下一次注入回报已完成 job"(按 repo_inst 读 evidence),不需要改动。`evidence-before-commit` 遇到运行中的 job 仍返回 unknown 加提示(v0.4.2)。

**【决定】新鲜度不按 tier 过滤**,前提是下面四条同时成立(都已冻结为契约,并在 P2-RUNNER、boundary-t1 中各有断言;第 4 条为 v1.5 新增):

1. origin=hook 的 job **永远**在 OS 沙箱里执行测试命令;沙箱不可用时**不执行**,结果记 infra、error `sandbox_unavailable`,不受 `runner_sandbox` 影响(§3.4)。
2. runner 沙箱默认拒写:测试命令写不了 `~/.ballast`(evidence、rules、state;唯一的例外是它自己的私有副本)、shell rc、`.git/hooks` 等(§3.4)。
3. agent 自己的 Bash 在 Claude 沙箱里,写不了 `~/.ballast`;文件工具受 permissions.deny 约束。这一条依赖"没有任何低层把命令、socket 或目录排除出沙箱,也没有低层让命令在沙箱外执行":用户层由条件 6 保证(`Detect`);项目层与本地层**只能**由"主机上任何 Claude 会话的项目目录都没有削弱沙箱的 `.claude/settings*.json`"保证。**这是全机范围的前提**:证据目录是全局的,任何一个项目层削弱了沙箱的会话都可以在沙箱外直接写 `~/.ballast/evidence`(任何仓库的),写入者想填什么 tier、sandbox、origin 都行。Ballast 对它的处理分三层,都不能把它变成保证:hook 在会话首个事件审计 `lower_sandbox_weakened`(审计行本身也可能被这样的会话事后改写);org.json `weakened_project:"deny"` 让这类会话的工具调用全部被拒;runner 对 job 仓库与请求带来的项目目录再查(§3.4),不通过时把**它自己写的**记录标为 T0(`tier_missing:["lower_sandbox_weakened"]`)并审计 `tier_mismatch`——这只影响诚实记录。
4. **(v1.5)origin=hook 的 job 不在真实工作树上执行**:runner 把 `S_start` 对应的树实体化到 `~/.ballast/work/<job_id>/tree`(agent 在 Claude 沙箱里写不了),测试在副本里跑,`S_start` 与副本出自同一次读取,`S_end` 在副本上重算;未跟踪与被忽略的文件只有 `inputs_untracked` 白名单命中的才复制,其内容哈希记入证据 `inputs_hash`;真实工作树之后的任何改动(包括 A-B-A 式的换入换出)都影响不到这次运行(§3.4"job 私有树")。v1.4 及以前的设计在 agent 可同时改写的真实工作树上跑测试,而 `S`/`C` 只覆盖已跟踪文件,一个未跟踪的 `zz_test.go`(`TestMain` 里直接 `os.Exit(0)`)或 `conftest.py` 就能得到有效的 T1 pass——"runner 只写它亲自观察到的结果"与"提交里能看到测试被改"在那时都不成立,本条修正它们。

于是在四条前提都成立的 T1 主机上,能写证据的只有:runner 进程本身(只写它亲自观察到的结果——origin=hook 时就是 `S_start` 这棵树加上白名单输入在沙箱里的测试结果),以及人在终端里手动运行的 `ballast verify`(origin=cli;仍在真实工作树上执行,按 `runner_sandbox` 决定是否包装,不包装时记录为 T0;它不受副本保护,agent 放进工作树的未跟踪文件或并发改动可以影响结果,登记在 §11)。T1 证据由此保证的是:结果来自 runner 亲自执行 `project.json` 里的命令,执行的内容是 `S_start`(已跟踪)加上内容哈希已记录的白名单输入;**不**保证白名单输入与测试读取的副本之外的文件(文件读是允许的)没有被 agent 改过——前者可审计,后者要么是已跟踪的依赖(进入提交),要么属于 §11 登记的残余风险。前提 3 不成立的主机上(某个会话的项目层削弱了沙箱),**T1 记录本身也不可信**,而新鲜度不按 tier 过滤;这正是 v1.3 的表述"T1 主机上出现的 T0 记录要么来自人、要么来自削弱的仓库"不成立的地方,v1.4 撤回该表述。部署要求(trust-model §2.3、§4):管理员确认主机上没有削弱沙箱的项目层,建议开启 `weakened_project:"deny"`;tier 仍用于审计与 CI 判读,`verify_fresh` 语义不变。

**攻击面对照**:

| agent 的尝试 | T1 下的结果 |
|---|---|
| 在 `~/.claude/settings.json` 加自己的 hook,或 `disableAllHooks` | `allowManagedHooksOnly` 使低层 hook 不执行,低层不能关闭托管 hook【M0b 验证】;settings 本身在 denyWrite / permissions.deny 中 |
| 在 `PATH` 前面放伪造的 `ballast` | 托管 hook 用绝对路径;hook 代起时用 `Self`,不用 agent 的 argv[0] |
| 在沙箱里跑 `ballast verify --foreground test` | 写不了 evidence,结果为 infra |
| 直接写 evidence / state / rules | Bash:denyWrite;文件工具:permissions.deny;MCP:白名单(残余风险) |
| 让 Claude Code 进程带上 `BALLAST_HOME` 等重定向变量(降级攻击) | 重定向锁:已锁定主机上这些变量被忽略,家目录取自 passwd(查不到时只按 root 所有的 `home_root` 规则,§3.2);org 规则、证据、状态仍在真实位置;审计 `env_redirect_ignored`(§3.2) |
| 改测试代码让它"恰好通过" | 内容快照把改动纳入 `S_end`/`C`;origin=hook 的测试只运行 `S_start` 这棵树(已跟踪内容)加白名单输入,改动要么进入提交(可见),要么让证据过期;由 CI 与评审把关(trust-model §3) |
| 在工作树里放**未跟踪或被忽略**的文件让测试恰好通过(`zz_test.go` 里 `TestMain` 直接 `os.Exit(0)`、`conftest.py`、`sitecustomize.py`,再写进 `.git/info/exclude` 藏起来),然后执行规范 `ballast verify test` | v1.5:origin=hook 的测试在私有副本里跑,副本只含 `S_start` 的已跟踪内容;未跟踪与被忽略文件只有 `inputs_untracked` 白名单命中的才复制(默认空),内容哈希记入 `inputs_hash`(§3.4"job 私有树") |
| A-B-A:取 `S_start` 之后把测试换成平凡版本,测试读完后、取 `S_end` 之前再换回来(job 状态与日志可读,时机好把握) | 测试读的是副本而不是真实工作树,副本在 `~/.ballast/work/` 下,agent 写不了;`S_start` 与副本出自同一次读取(`git add -u` 读到什么就复制什么),实体化那一刻读到平凡版本则 `S_start` 就是平凡版本,之后换回 ⇒ `C != S_end`,过期 |
| 改写白名单内的未跟踪输入(例如 `node_modules` 里被测依赖的代码) | **残余风险**:白名单输入不在 `S`/`C` 里,不影响新鲜度;内容哈希记入 `inputs_hash`,可审计、可与 CI 对照;白名单默认空,每一项都是项目显式接受的风险(§11) |
| 已跟踪的测试代码或符号链接读取副本之外的文件(文件读允许:`/tmp/x.go`、指向仓库外绝对路径的已跟踪符号链接) | 依赖本身是已跟踪内容,进入提交、可见,归入上一类"改测试代码";§11 登记 |
| 人在终端运行 `ballast verify --foreground`(origin=cli)时,agent 并发改真实工作树或放未跟踪文件 | origin=cli 维持在真实工作树执行(v1.5 决定),不受副本保护;登记为残余风险(§11) |
| 在子目录或(会话在 monorepo 子目录启动时)仓库根埋下削弱沙箱的 `.claude/settings*.json`、改 `.ballast/project.json`,提交后扩散,让之后在该目录启动的会话绕过 runner | `ManagedForms` 的 `**/` 任意深度形式(是否生效待 M0b `managed-denywrite-anydepth`);条件契约:未证明有效 ⇒ T1 下 `weakened_project` 缺省 `deny`,之后在该目录启动的会话工具调用全部被拒;会话首个事件审计 `lower_sandbox_weakened`;doctor 点名;仓库根的 `project.json` 是已跟踪文件,改动进入提交与 `project_json_hash`(§3.2"任意深度形式") |
| 让 Claude 带着指向 agent 可写目录的 `CLAUDE_CONFIG_DIR` 启动,加载不受托管配置保护的用户层 | `Detect` 判 T0(`sandbox_deny_write`,v1.5),不代起 |
| 改测试代码,让 hook 代起的测试去写 `~/.zshrc`/`~/.bashrc`/`~/.profile`、`~/.claude.json`、`~/.ballast/evidence` | 默认拒写:可写集合之外一律拒绝(§3.4) |
| 测试代码重命名受保护路径的祖先目录再重建(`mv repo repo2 && … && mv repo2 repo`,或重命名 `.git` 后改 `config`) | 祖先固定:macOS 对祖先 `(literal)` 拒写,Linux 把祖先变成挂载点(rename 返回 EBUSY);仓库外的祖先本来就在可写集合之外(§3.4) |
| 测试代码硬链接受保护文件再经链接写 | macOS:建链被拒;Linux:跨绑定挂载建链返回 EXDEV(§3.4) |
| 测试代码写 `.git/hooks/*` 或 `.git/config`(`core.fsmonitor`、`core.hooksPath`,Ballast 自己的快照或人随后运行 git 时就会在沙箱外执行) | `RepoMandatoryDeny`(§3.4) |
| 测试代码写嵌套的 `.git`:submodule 工作树里把 `sub/.git` gitfile 换成目录再写带 `core.fsmonitor` 的 config;改 gitfile 指向仓库内自建的 git dir;写 `<commondir>/modules/x/config`、`modules/x/hooks/`、其他 worktree 的 `worktrees/*/config.worktree`;用 `git update-index` 加一个 gitlink 再建 `evil/.git/config`(人随后在沙箱外 `git status` 就会执行) | `RepoMandatoryDeny` **任意深度**(含 `.git` 条目本身)与扩展后的 `GitDirMandatoryDeny`;macOS 用锚定的 SBPL regex,对不存在的路径同样生效;Linux 对已存在的匹配项 ro-bind,运行后递归扫描新出现的匹配项并移入 `~/.ballast/quarantine/<job>/`(§3.4) |
| 测试代码在仓库里新建原本不存在的 `.claude/settings.local.json`、`.vscode/tasks.json`、`.mcp.json`,让它们之后生效 | macOS:创建即被拒;Linux:`PostCheck` 发现后隔离出仓库并记 infra,宿主上不留残留(§3.4) |
| 测试代码打开 `/dev/tty`,或对继承的终端 fd 调 `ioctl(TIOCSTI)`,往人的 shell 注入按键 | 被包装的子进程新建会话(`Setsid`,bwrap `--new-session`),stdin 为 `/dev/null`,输出只经文件或管道;可写设备集不含 `/dev/tty*`。设计期实测见 §3.4"终端" |
| 改 `.git` gitfile 或 git dir 下的 `commondir`,或先 cd 进自建的嵌套仓库,让可写集合 `W` 扩到工作树之外(例如另一个仓库的 `.git`) | `sandbox.ResolveGitDirs` 只接受"工作树内"与"标准 worktree 形态且 `gitdir` 回指当前工作树"两种,否则 infra、`sandbox_spec_invalid`(§3.4) |
| 在用户层或项目层 settings 写 `sandbox.excludedCommands: ["npm"]`、`network.allowUnixSockets: ["/var/run/docker.sock"]` 等,让 agent 自己的命令跑到沙箱外再写证据 | 用户层:条件 6 ⇒ 判 T0,不代起,doctor fail;项目层:会话首个事件审计 `lower_sandbox_weakened`,`weakened_project:"deny"` 时该会话的工具调用全部被拒;runner 对 job 仓库与 `ProjectDir` 自判 ⇒ 诚实记录记 T0 并审计 `tier_mismatch`;**`audit` 模式下这条路径没有被挡住**,是 T1 证据的全机前提(§3.3 前提 3,trust-model §2.3);用户层 `~/.claude/settings*.json` 与会话目录这一层的 `./.claude/settings*.json` 在托管配置的字面形式中,其他目录的项目层只靠 `**/` 形式(待 M0b)与 `weakened_project` 条件契约兜底(§3.2,v1.5 更正) |
| 项目层 settings 带 `env`(`DYLD_INSERT_LIBRARIES`、`LD_PRELOAD`、`GIT_CONFIG_PARAMETERS`…)或会执行命令的 helper 键(`apiKeyHelper`、`statusLine`、`awsAuthRefresh`…),在沙箱外执行或改变托管 hook 与 runner 的环境 | `WeakenedSandboxKeys` 在项目层与本地层把它们算作削弱(v1.4,同上一行处理);runner 在沙箱外启动的 git 用 `sandbox.HostEnv` 清洗过的环境,Ballast 自身子进程与沙箱后端去掉 `LD_*`/`DYLD_*`(§3.4);hook 进程自身无法自保,darwin 发布构建必须启用 hardened runtime(doctor `tier.binary_hardening`,§3.5);在 Claude 启动时就执行的 helper 不经过任何工具调用,落在全机前提里 |
| 测试代码联网外传,或连接主机回环上的本地服务(IDE 扩展服务、开发代理、容器守护进程的 TCP 端口) | `runner_network` 默认 deny:macOS 不放行任何出站 IP(**含回环**,v1.4),Linux 用私有网络命名空间(私有回环);`loopback` 是管理员显式选项(macOS 放行 `localhost:*`,登记为残余风险)(§3.4) |
| 测试代码连接沙箱外的本地 socket(容器守护进程、用户会话总线、SSH agent),让沙箱外的进程替它执行 | **与网络策略无关,恒定执行**(v1.4):macOS 只允许连接 `W` 内的 unix socket;Linux 恒定屏蔽 `/run`、`/var/run` 并移除 `SSH_AUTH_SOCK`、`DOCKER_HOST`、`DBUS_SESSION_BUS_ADDRESS`、`DBUS_SYSTEM_BUS_ADDRESS`;`allow` 只放开出站 IP。Linux `allow` 下主机的抽象命名空间 socket 仍可达,登记为残余风险(§3.4、§11) |
| 测试代码经 LaunchServices / AppleEvents / launchd 让沙箱外的进程替它执行(macOS) | v1.5 起 darwin 的 IPC 部分**默认拒绝**:`(deny mach-lookup)` 加最小白名单 `DarwinMachAllow`,显式 `(deny job-creation)`、`(deny mach-register)`、`(deny lsopen)`、`(deny appleevent-send)`(`DarwinEscapeDenials`)【未核验,P2-SANDBOX 集成测试必须证明,且必须在去掉 process-exec 规则的情况下单独证明 IPC 拒绝生效:用拷到 `TMPDIR` 的 `open`/`osascript`/`launchctl` 与 python3 ctypes 直接调用 LaunchServices/AppleEvents】(§3.4) |
| 测试代码经 cfprefsd `defaults write` 改终端应用或 iTerm2 的启动命令(plist 写在 `W` 之外,人下次打开终端时在一切沙箱之外执行);经 WindowServer `CGEventPost` 向任意窗口注入按键(负责进程有辅助功能授权时);写剪贴板(macOS) | 显式 `(deny user-preference-write)`;WindowServer 与 pasteboard 服务不在 mach 白名单内(列入 `DarwinMachForbidden`);集成用例:命名偏好域 `defaults write` 后 plist 不存在或逐字节不变、ctypes `CGEventPost` 与剪贴板写入失败(§3.4) |
| 测试代码在任意接口上监听、接受其他主机的入站连接(macOS) | deny 策略下 `(deny network-bind)`,不能绑定任何 IP 地址(设计期实测),unix socket 只能绑定在 `W` 内;loopback / allow 下放行 `localhost` 绑定与入站,但设计期实测通配地址 `0.0.0.0` 的绑定与来自局域网的入站**没有被挡住**,登记为这两种策略的残余风险(§3.4、§11) |
| 测试代码写其他分支的 refs、`info/exclude` 或会话目录之外的仓库文件 | v1.5:origin=hook 的 `W` 只有副本,真实仓库与 common dir 都不可写;origin=cli 的 `W` 仍是仓库与 git dir,比会话在子目录或 linked worktree 时 Claude 沙箱的可写范围大,登记(§11) |
| 测试命令启动后台进程稍后写受保护路径 | macOS:沙箱继承给全部后代,子进程退出后 runner 对其会话/进程组发 SIGKILL(v1.5 收尾顺序);用 `setsid` 脱离进程组的后代仍在沙箱内,只能写 `W`(origin=hook 时是随后删除的副本、临时目录与 `runner_allow_write`),登记(§11);Linux:bwrap `--unshare-pid --die-with-parent`,runner 结束即全部终止 |

### 3.4 沙箱化 runner(`internal/sandbox` + `internal/runner`)

**runner 进程本身在沙箱外**(它要写 evidence、job 文件与日志),只把**测试命令**包进沙箱;日志经继承的文件描述符写入,不受沙箱路径规则影响。

**【决定】默认拒写模型**(v1.0 的"默认全允许、只拒受保护路径"已废弃:它比 Claude 自己的 Bash 沙箱弱得多,而 hook 代起的 runner 在 Claude 沙箱之外执行 agent 可改写的测试代码,这条链路会变成沙箱逃逸原语;审核实测了 `mv repo repo2 && echo pwn > repo2/.ballast/project.json && mv repo2 repo` 在旧 profile 下成功)。测试命令只能写:

```
W(origin=hook,v1.5)
  = job 私有树 Spec.Tree(~/.ballast/work/<job_id>/tree,见下文"job 私有树";真实工作树、git dir 与 common dir 都不在 W 内)
  ∪ org.json runner_allow_write(展开 ~ 后;默认为空。每一项都是残余风险,见 §3.2 与 §11)
  ∪ 每个 job 的临时目录(macOS:os.TempDir() 下 0700 新建、结束后删除;Linux:私有 tmpfs /tmp),TMPDIR 指向它
  ∪ 固定设备集(/dev/null、/dev/zero、/dev/fd/*;v1.3 起不含 /dev/tty*,见下文"终端")

W(origin=cli,维持 v1.4)
  = 仓库工作树
  ∪ 仓库的 git dir 与 common dir(位于工作树之外时,例如 worktree;只接受 sandbox.ResolveGitDirs 校验过的来源,见下文 ValidateSpec)
  ∪ runner_allow_write ∪ 临时目录 ∪ 固定设备集(同上)
```

在 `W` 内部,以下受保护集合 `D` 仍然不可写:

```
D = tier.ProtectedPaths(…)(原始与解析后两种形式,含 glob)
  ∪ 对每个仓库工作树根(origin=hook 时就是副本根),在任意深度(<root>/E 与 <root>/**/E):sandbox.RepoMandatoryDeny
      .ballast/  .claude/  .git  .git/hooks/  .git/config  .git/config.worktree
      .gitmodules  .mcp.json  .vscode/  .idea/
      (".git" 指这个条目本身——gitfile 或目录——不能新建、替换、改名、删除;
       已有 .git 目录里除上面列出的条目外仍可写)
  ∪ 对 git dir 与 common dir(无论在工作树内外;origin=hook 没有这一项):sandbox.GitDirMandatoryDeny
      hooks/  config  config.worktree  commondir
      modules/**/hooks/  modules/**/config  modules/**/config.worktree  modules/**/commondir
      worktrees/*/config.worktree  worktrees/*/commondir  worktrees/*/gitdir
      ("*" 恰好一级,"**" 一级或多级;唯一匹配器是 sandbox.MatchMandatory)
```

origin=cli 时 git 的索引、对象与引用仍可写(测试里的 `git add`/`commit` 会进入内容快照,是可见的,不是绕过)。origin=hook 时副本里没有 `.git`,真实仓库(含其他分支的 refs、`info/exclude`、common dir)对测试只读(v1.5;审核 minor (a) 指出的"W 比会话在子目录或 linked worktree 时 Claude 沙箱的可写范围大"对 origin=hook 不再成立,对 origin=cli 登记在 §11)。

**job 私有树(【决定】,v1.5,P2-RUNNER 实现;算法冻结在 `runner.Materialize` 注释)**:origin=hook 的 job 不在真实工作树上执行测试——审核实测思路:真实工作树 agent 可以同时改写,而 `S`、`C` 只看已跟踪文件,未跟踪的 `zz_test.go`、`conftest.py` 或取 `S_start` 后的 A-B-A 换入换出都能产生有效的 T1 pass。冻结做法:

1. **位置**:`<layout.WorkDir>/<job_id>/`(`~/.ballast/work/`,0700)下有两个临时索引与 `tree/`。它在 `~/.ballast` 之内,所以 agent 在 Claude 沙箱里写不了(托管 denyWrite 与 permissions.deny 已覆盖 `~/.ballast`,不需要改 `ManagedForms`);对 runner 沙箱,`Spec.Tree` 是 `W` 在受保护目录内**唯一**的例外(`ValidateSpec` 要求它恰好是 `<WorkRoot>/<单级>/tree`,路径上没有符号链接)。副本的祖先 `~/.ballast/work/<job_id>` 在 `W` 之外,测试改不了名。
2. **方法(冻结一种)**:`GIT_INDEX_FILE` 临时索引 + `read-tree` + `checkout-index --prefix`,不用 `git archive`。先 `sandbox.ResolveGitDirs` 校验仓库 git dir 来源(与沙箱 Spec 同一规则);把真实索引复制成 `index`,`git add -u` 刷新、`write-tree` 得到 `T_full`,再 `git rm --cached` 去掉 exempt、`write-tree` 得到 `S_start`——这正是快照 fallback 的算法,所以 `S_start` 与 `snapshot.TreeS` 对同一时刻的工作树逐位相等,`S_end == C` 的含义不变;然后在第二个索引 `index.tree` 上 `read-tree T_full`,`checkout-index -a -f --prefix=<tree>/`。工作树只被 `git add -u` 读一次,副本与 `S_start` 描述的是同一组字节,无论 agent 同时做什么。git 子进程一律用 `sandbox.HostEnv` 清洗过的环境、显式 `GIT_DIR`/`GIT_WORK_TREE`/`GIT_INDEX_FILE` 与 `-c core.fsmonitor=false`。
3. **内容**:副本含 `T_full`(exempt 路径也在内,测试可能读它们;exempt 本来就是项目声明"与结论无关"的路径),**不含 `.git`**;`Wrapped.Env` 设 `GIT_CEILING_DIRECTORIES=<dir(tree)>`,测试里的 git 不会找到副本之上的仓库(例如家目录里的 dotfiles 仓库)。需要 git 元数据的测试会可见地失败(登记;私有 git dir 加 alternates 留作以后的契约追加)。
4. **未跟踪输入**:project.json `verify[].inputs_untracked`(doublestar glob,校验同 exempt,**默认空**)。runner 遍历真实工作树(Lstat,不跟随符号链接,不进入真实 `.git`),把命中任一 glob、且不是 `T_full` 条目的文件或符号链接按相对路径复制进副本(普通文件复制内容与可执行位,边复制边哈希;符号链接按链接复制,哈希目标字节);可以用写时复制克隆(clonefile / FICLONE),**不得**用硬链接(经硬链接的写入会落到真实文件)。被忽略的文件只有被白名单命中才复制。超过 `runner.MaxUntrackedInputFiles`(50 万)或 `MaxUntrackedInputBytes`(8 GiB)⇒ infra `inputs_too_large`。内容摘要记入 `VerifyRecord.InputsHash`(对按路径排序的 `"<mode> <sha256> <path>\n"` 行取 sha256),白名单本身随 `project_json_hash` 记录。**残余风险**:白名单输入可以被 agent 改写,而它们不在 `S`/`C` 里;摘要只让它们可审计(§11)。
5. **子模块**:`checkout-index` 不展开子模块内容。`T_full` 含 gitlink(mode 160000)⇒ 不执行测试,infra、error `tree_has_submodule`,登记在 §11(递归实体化留作以后的契约追加)。
6. **`S_end` 以副本为准**:副本对测试可写,`S_end` 由 `runner.TreeSEnd` 在副本上重算(`index.tree` + `GIT_WORK_TREE=<tree>` 的 `add -u`、去 exempt、`write-tree`)。测试改了副本里的已跟踪文件 ⇒ `S_end != S_start`,不是有效通过;测试新建的文件是未跟踪的,与工作树语义相同。若副本只读,`S_end == S_start` 恒成立、发现不了自改,所以不这样做。
7. **清理**:收尾后 `runner.CleanupTree` 删除 `<WorkDir>/<job_id>/`(不跟随符号链接);删不掉只记入 job 日志,`ballast gc` 删除 job 已结束且超过 24 小时的残留(§8 E2)。
8. **记录**:origin=hook 的记录恒带 `workspace:"copy"` 与(有白名单时)`inputs_hash`;origin=cli 记 `workspace:"worktree"`。origin=cli(T0,或人在 T1 主机上的 `--foreground`)维持在真实工作树执行,不实体化。
9. **代价**(登记,不设门槛):每个 hook 代起的 job 多一次全树检出与白名单复制;副本里没有上次构建的产物,构建缓存是冷的(`CacheEnv` 本来就把缓存指向 job 临时目录)。大仓库或大 `node_modules` 的 job 启动可达数秒到数十秒;runner 不在热路径,hook 的 30ms 门槛不受影响。

**收尾顺序(【决定】,v1.5,两个平台相同,冻结在 `sandbox` 包注释与 `runner.TreeSEnd` 注释)**:测试子进程退出 →(darwin)runner 对子进程的会话/进程组发 SIGKILL,清掉仍在组内的后代;(linux)bwrap 的 PID 命名空间随其 init 退出已全部终止 → `sandbox.PostCheck` → 发现新匹配时 `sandbox.Quarantine` → 计算 `S_end`(origin=hook:`TreeSEnd`;origin=cli:`TreeS`)→ fsync 写记录 → 审计 → `CleanupTree`。`S_end` 因此永远看不到即将被隔离的受保护路径,也不会在仍有组内沙箱进程写入时计算。当前快照多数走原生实现,fallback 也带 `core.fsmonitor=false`,但顺序仍写成契约。

**缓存变量**(`sandbox.CacheEnv`,P2-SANDBOX):`Wrap` 总是把 `sandbox.CacheVars`(`GOCACHE`、`XDG_CACHE_HOME`、`npm_config_cache`、`YARN_CACHE_FOLDER`、`PIP_CACHE_DIR`,只能追加)指向 job 临时目录下的子目录;只有当某个变量在 runner 环境里的当前值已经位于 `runner_allow_write` 的某项之内时才保留(管理员显式接受了风险)。`GOMODCACHE` 不重定向:网络默认 deny 时模块缓存只读【未核验:go 在只读 `GOMODCACHE` 下的行为,P2-SANDBOX 集成测试确认】。

`RepoMandatoryDeny` 除 Ballast 自己的两项外,对齐 Claude Code Bash 沙箱的"危险文件"类别(之后会在沙箱外执行的东西:git hooks;git config 里的 `core.fsmonitor`、`core.hooksPath`——Ballast 自己的快照或人随后的 `git status` 运行 git 时就会触发;把 git 引到攻击者自建 git dir 的 gitfile;编辑器与 MCP 配置)【静态依据待核:P2-SANDBOX 对照 sandbox-runtime 源码,只能追加】。**【决定】v1.3 起按任意深度匹配**(Claude 自己的 Bash 沙箱对危险文件就是按任意深度匹配的,只管顶层会违背"不弱于 Claude 沙箱"的前提):嵌套仓库、submodule 工作树、测试自己用 `git update-index` 加进索引的 gitlink 都在覆盖范围内;`modules/**` 覆盖任意嵌套的 submodule git dir;`worktrees/*/…` 覆盖同一仓库的其他 worktree,以及 `ResolveGitDirs` 要信任的 `commondir`、`gitdir` 两个链接文件。仓库内的 `.claude/` 整个目录拒写(比托管配置里的 `settings*.json` 更严),这样 Linux 上不必为 glob 处理祖先。代价:测试若在仓库内(而不是 `TMPDIR`)新建 git 仓库、`.vscode/` 等,会可见地失败;`modules/**` 下恰好名为 `config`、`hooks` 的 ref 也会被拒,同样是可见的失败,不是安全问题。

**祖先固定**:对 `D` 中每个位于 `W` 内的显式受保护路径 `P`(`ProtectedPaths`、glob 所在目录)以及直接位于工作树根或 git dir 下的强制条目,从所在的 `W` 根开始到 `dir(P)` 的每一级祖先 `A` 都要固定,使"重命名或删除祖先再重建"无法替换 `P`(`sandbox.AncestorPins`,纯函数,golden 测试)。`W` 之外的祖先本来就不可写,不需要处理。更深的强制匹配不做祖先固定:macOS 的 regex 按任意深度匹配,改名后的新位置照样被拒;Linux 由运行后的递归扫描兜底(改名后的旧匹配项仍是只读挂载,新建的匹配项不在 `Baseline` 里,会被发现并隔离)。

**终端(【决定】,v1.3)**:被包装的测试命令不得接触人的终端。否则测试代码可以打开 `/dev/tty`,用 `ioctl(TIOCSTI)` 往人的 shell 注入按键,命令就在一切沙箱之外执行——origin=cli 的 `ballast verify --foreground` 在 T1 下按 `require` 包装后证据可以标 T1,这是一个完整的逃逸原语。冻结要求:

- 被包装的子进程**新建会话**:unix 上 `SysProcAttr.Setsid`(取代现在 `exec_unix.go` 的 `Setpgid`;会话首进程同时是进程组首进程,`pgid == pid`,按组发信号的逻辑不变);bwrap 另加 `--new-session`;`sandbox.Wrapped.NewSession` 恒为 true 作为给 runner 的信号。
- stdin 为 `/dev/null`;stdout/stderr 只能是日志文件或管道;前台模式只经管道转发输出,绝不把终端 fd 交给子进程。
- 可写设备集去掉 `/dev/tty*`(以及 darwin 的 `/dev/dtracehelper`),只保留 `/dev/null`、`/dev/zero`、`/dev/fd/*`。注意这一条只挡**写**;真正切断 TIOCSTI 的是新会话(没有控制终端时打开 `/dev/tty` 返回 ENXIO;Linux 与 macOS 的 TIOCSTI 都要求目标是调用者的控制终端,root 除外)。
- 设计期实测(本机 macOS 26,python 的 `pty.fork` 给父进程一个控制终端,子进程模拟 runner 的两种启动方式):**同一会话**(只 `setpgid`)⇒ `open("/dev/tty")` 成功,对它和继承的 fd 0、2 调 TIOCSTI 都**成功**(审核所述逃逸成立);**新会话 + stdin `/dev/null` + 输出管道** ⇒ `open("/dev/tty")` 返回 ENXIO,fd 0/1/2 上的 TIOCSTI 返回 ENODEV/ENOTTY。Linux 上 6.2 起另有 `dev.tty.legacy_tiocsti` 开关,不作依赖;bwrap `--new-session` 正是为这一类问题(CVE-2017-5226)设计的【静态依据】。

**后端**:

| 平台 | 后端 | 方式 |
|---|---|---|
| macOS | `/usr/bin/sandbox-exec` | `sandbox-exec -p <DarwinProfile(spec)> -- argv`。profile 按此顺序(SBPL 最后匹配的规则生效):`(version 1)(allow default)`;`(deny file-write*)`;`(allow file-write* <W 中除 Spec.Tree 外的 subpath>、<设备的 literal/regex>)`;`(deny file-write* <D 中不在 Spec.Tree 内的 subpath/literal/regex>)`;**v1.5** `(allow file-write* (subpath <Spec.Tree>))`(只在 origin=hook 时出现,受保护的 `~/.ballast` 内唯一的放行);`(deny file-write* <D 中位于 W 根内的条目;强制条目用以 W 根锚定、按任意深度匹配的 regex>)`;`(deny file-write* <每个祖先的 literal>)`;**恒定** `(deny network-outbound)`;IP 按策略:deny 不放行任何 IP(**不含回环**,v1.4),loopback 放行 `(remote ip "localhost:*")`,allow 放行 `(remote ip "*:*")` 并为域名解析放行 `(remote unix-socket (path-literal "/private/var/run/mDNSResponder"))`;**恒定** `(allow network-outbound (remote unix-socket (subpath <每个 W>)))`;**v1.5 恒定** `(deny network-bind)(deny network-inbound)` 与 `(allow network-bind (local unix-socket (subpath <每个 W>)))`,loopback/allow 另放行 `(allow network-bind (local ip "localhost:*"))(allow network-inbound (local ip "localhost:*"))`;最后是 `DarwinEscapeDenials()`(v1.5:IPC **默认拒绝**,见下文"macOS 的非文件逃逸通道")。文件读仍然允许。SBPL 按路径匹配,对尚不存在的路径同样生效 |
| Linux | bubblewrap | `bwrap --die-with-parent --new-session --unshare-pid [--unshare-net,allow 之外] --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp --tmpfs /run --tmpfs /var/run`(v1.4:两个 socket 目录在任何策略下都屏蔽),然后对每个已存在的 `W` 条目(`Spec.Tree` 除外)`--bind A A`,对每个已存在、不在 `Spec.Tree` 内的 `D` 条目 `--ro-bind P P`,**v1.5** 再 `--bind <Spec.Tree> <Spec.Tree>`(只读的 `~/.ballast` 里唯一的可写挂载,只在 origin=hook 时出现),对每个已存在的祖先 `--bind A A`(变成挂载点),对每个已存在、位于 `W` 根内的 `D` 条目 `--ro-bind P P`(glob 在包装时展开;glob 所在目录位于 `W` 内时整个目录 ro-bind),对 `Wrapped.Baseline` 的每一项(包装前 `sandbox.ScanMandatory` 完整遍历工作树与 git dir 找到的、已存在的强制匹配项,含被忽略的目录)`--ro-bind P P`,最后 `--chdir <cwd> --setenv TMPDIR /tmp`、每个 `Spec.LoaderEnv` 条目一个 `--setenv K V`、`-- argv`。后面的挂载覆盖前面的。匹配项超过 `MaxMandatoryMatches`(4096)⇒ infra、`sandbox_spec_invalid`(失败可见,不带着未绑定的匹配项运行)。`W` 内尚不存在的受保护路径无法 bind(会在宿主上建挂载点):显式的列入 `Wrapped.MissingProtected`;运行后 `sandbox.PostCheck(fsys, spec, w)` 检查它们,并**递归重扫**全部强制匹配项,找出不在 `Baseline` 里的新匹配项。发现任何一项 ⇒ `sandbox.Quarantine` 把它移到 `~/.ballast/quarantine/<job_id>/`(保留相对路径;跨文件系统时复制后删除,复制不跟随符号链接;都失败时就地删除),结果记 infra、error `protected_path_created`,审计 `diag: sandbox_quarantine`(detail 只有相对路径);仍删不掉的再加 error `protected_path_residue`。这样攻击不会留在磁盘上继续生效 |
| 原生 Windows 及其他 | 无 | 永远 T0 |

**设计期实测**(本机 macOS 26 的 `sandbox-exec`,以及 Debian 12 容器里的 bubblewrap 0.8.0;均为临时目录,未写入仓库;正式用例由 P2-SANDBOX 的集成测试与 boundary-t1 固化):

| 尝试 | macOS(上表 profile) | Linux(上表参数) |
|---|---|---|
| 写 `W` 内普通文件;`git add` | 成功 | 成功 |
| 写 `W` 外(模拟 `~/.bashrc`、`~/.ballast/rules.json`) | 拒绝 | 拒绝(只读文件系统) |
| 写、新建受保护路径 | 拒绝(含尚不存在的路径) | 拒绝;尚不存在的路径能创建 ⇒ 由 `PostCheck` 发现并隔离 |
| 嵌套 `.git`(v1.3 regex):`mkdir sub/.git`、把 `sub/.git` 写成 gitfile、`mkdir -p a/b && mkdir a/b/.git`、`sub/.gitmodules`、`mv sub sub2 && mkdir sub2/.git`、`.git/hooks/x` | 全部拒绝;同一 profile 下 `.git/index`、`.git/objects/…` 与 `git add` 仍成功 | 未实测;按设计由 `Baseline` ro-bind + `PostCheck` 隔离,P2-SANDBOX 集成测试固化 |
| 重命名仓库根 | 拒绝 | 拒绝(只读 / EBUSY) |
| 重命名受保护路径的祖先(`.git`、`.claude`)后重建 | 拒绝(祖先 literal) | **未固定时成功**(实测 `mv .git .git2 && mkdir .git && echo pwn > .git/config` 生效);固定后 EBUSY |
| 硬链接受保护文件到 `W` 再写 | 建链被拒 | EXDEV |
| 先建符号链接指向受保护文件再写 | 写被拒 | 写被拒(目标只读) |
| 出站 TCP | deny 策略下拒绝;当时的 profile 放行了回环(v1.4 起 deny 不再放行回环,P2-SANDBOX 集成测试固化) | `--unshare-net` 下无网络(私有回环可用) |
| 连接 `W` 外的 unix socket | 拒绝;`W` 内的允许 | 仍可连接 ro 挂载上的 socket 文件 ⇒ 屏蔽 `/run`、`/var/run`,移除 `SSH_AUTH_SOCK`、`DOCKER_HOST`(v1.4 起与网络策略无关,并加 `DBUS_*_BUS_ADDRESS`);其他自定义位置的 socket 登记为残余风险 |
| Docker 内运行 bwrap | — | 需要 `--security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined`(缺最后一项时 `Can't mount proc on /newroot/proc`) |
| **v1.5** IPC 默认拒绝片段(`DarwinEscapeDenials` 冻结内容 + `DarwinMachAllow`)能否被 sandbox-exec 接受;测试工具链是否仍可用 | 接受(macOS 26,含 `job-creation`、`lsopen`、`appleevent-send`、`user-preference-write`、`mach-register`、`signal (target same-sandbox)`);`id -un`、python3 `pwd.getpwuid`、python3 `ssl`、node `os.userInfo()`、`go version` 与一个用 `os/user` 的最小 `go test` 都成功 | — |
| **v1.5** 拷到 `W` 的 `launchctl` 副本 `submit` 一个写 `W` 外文件的作业 | 被终止(退出码 137),文件未创建,沙箱外 `launchctl list` 中没有该作业 | — |
| **v1.5** `defaults write <W 外的绝对路径域>` | 失败;但对照组(去掉 `user-preference-write` 拒绝,甚至去掉 mach 拒绝)**同样失败**,绝对路径域区分不了 cfprefsd 代写 ⇒ 集成用例必须用命名偏好域(见下文) | — |
| **v1.5** 网络绑定 | deny:绑定 `0.0.0.0`、`127.0.0.1`、`[::1]` 均被拒(`httptest.NewServer` 失败,可见);unix socket 只能绑定在 `W` 内。loopback:`localhost` 绑定与回环往返成功,但**通配地址 `0.0.0.0` 的绑定没有被拒,来自本机局域网地址的入站也被接受**(SBPL 的 `local ip` 过滤挡不住)⇒ 登记为 loopback/allow 的残余风险 | — |

**macOS 的非文件逃逸通道(【决定】,v1.5 改为 IPC 默认拒绝)**:v1.4 的 profile 除文件写入与网络外"默认全允许、再逐条拒绝",验收只覆盖 LaunchServices、AppleEvents、launchctl 三条,P2-SANDBOX 可能在逃逸仍存在时通过验收。例如 `user-preference-write` 由沙箱外的 cfprefsd 代写:测试 `defaults write com.apple.Terminal …` 或改 iTerm2 的启动命令,plist 写在 `W` 之外,人下次打开终端时这条命令在一切沙箱之外执行;负责进程(终端应用)有辅助功能授权时,沙箱内进程可以经 WindowServer 向任意窗口注入按键;`job-creation` 走 bootstrap port,不经按名字的 mach-lookup。Claude 自己的 sandbox-runtime 在 macOS 上是默认拒绝加 mach 服务白名单,v1.4 因此违背"不弱于 Claude 沙箱"的前提。冻结(只改 `DarwinProfile`/`DarwinEscapeDenials` 的内部实现与 golden,签名不变):`(deny mach-lookup)` 后接显式白名单 `sandbox.DarwinMachAllow`(go/node/python/java/dotnet 测试常用的最小集合:opendirectoryd 的 libinfo 与 membership、notification_center、system.logger、logd、diagnosticd、cfprefsd 的 daemon 与 agent(只为读;写由下一条拒绝)、trustd.agent、SystemConfiguration.configd、FSEvents;来源是 sandbox-runtime 的 macOS 白名单思路与 Chromium/Codex 的 seatbelt 基础策略,【静态依据待核】,逐条待实测,只能在登记实测依据后追加,且永不包含 `sandbox.DarwinMachForbidden` 中的 LaunchServices、AppleEvents、WindowServer、pasteboard、tccd、SecurityServer、Dock、launchd 域),然后显式 `(deny mach-register)`、`(deny job-creation)`、`(deny user-preference-write)`、`(deny lsopen)`、`(deny appleevent-send)`、`(deny signal)(allow signal (target same-sandbox))`,以及 v1.4 已有的 open/osascript/launchctl 的 process-exec 拒绝;网络部分另加 `(deny network-bind)`、`(deny network-inbound)`(见上表)。文件读、sysctl、IOKit、POSIX/SysV IPC 仍在 `(allow default)` 之下,登记为待实测的残余面(§11)。没有选 `(deny default)`:它要逐项放行 process-fork、sysctl-read、file-read-metadata、iokit 等大量操作,对各语言工具链的破坏面更大,而上面这些显式拒绝已覆盖审核点名的全部通道;若 P2-SANDBOX 的集成用例证明某条通道仍然开着,改为 `(deny default)` 加白名单(同样只改内部实现)。**v1.4 补充仍然有效**:process-exec 规则只匹配标准路径,集成测试必须另用拷到 `Spec.TmpDir` 的 `open`、`osascript`、`launchctl` 副本与 python3 ctypes 直接调用 `LSOpenCFURLRef`、`AESendMessage` 与 launchd 作业提交,并且在一个**去掉 process-exec 拒绝**的测试专用 profile 变体下同样失败,单独证明 IPC 拒绝生效。

**本地 socket(【决定】,v1.4)**:连接沙箱外的本地 socket(容器守护进程、用户会话总线、SSH agent)等于请沙箱外的进程替测试执行,所以 unix socket 的隔离**与 `runner_network` 无关、恒定执行**:macOS 只允许连接 `W` 内的 unix socket(allow 时另放行 mDNSResponder 用于域名解析);Linux 恒定 `--tmpfs /run --tmpfs /var/run`,`Wrapped.Env` 恒定移除 `SSH_AUTH_SOCK`、`DOCKER_HOST`、`DBUS_SESSION_BUS_ADDRESS`、`DBUS_SYSTEM_BUS_ADDRESS`。`allow` 只放开出站 IP。Linux 的 deny 与 loopback 使用私有网络命名空间,主机的抽象命名空间 socket 不可达;`allow` 共享主机网络命名空间,抽象 socket(无法用 tmpfs 屏蔽,例如旧发行版上 dbus 的 `unix:abstract=` 地址)仍可达,登记为 `allow` 的残余风险(§11)。

**回环(【决定】,v1.4)**:deny 时 macOS 不放行任何出站 IP,**包括回环**:SBPL 分不清"测试自己在沙箱内绑定的端口"与"沙箱外的服务监听在 127.0.0.1",而后者(IDE 扩展服务、开发代理、容器守护进程的 TCP 端口)运行在沙箱外,等于又一条非文件的逃逸或外传通道,弱于 Claude 自己的 Bash 沙箱。需要回环的测试在 macOS 上会可见地失败;管理员可以显式选 `runner_network:"loopback"`(macOS 放行 `localhost:*`,Linux 与 deny 相同),这是登记的残余风险(§11、trust-model §2.3、`org render` 的注释)。Linux 的 `--unshare-net` 提供私有回环,测试自己的回环服务照常可用。

**沙箱外进程的环境(【决定】,v1.4)**:项目层的 `env` 经 Claude Code 的环境进入 hook 与 runner。runner 在沙箱外自己启动的 git(内容快照)若原样继承,`GIT_CONFIG_PARAMETERS`/`GIT_CONFIG_COUNT` 可以注入 `core.fsmonitor` 直接在沙箱外执行,`GIT_TRACE*` 可以向任意文件追加。冻结:runner 交给 snapshot/evidence 的 `sys.Env` 经 `sandbox.HostEnv` 过滤(去掉全部 `GIT_*`、`LD_*`、`DYLD_*` 与 `BASH_ENV`、`ENV`、`SHELLOPTS`、`BASHOPTS`、`CDPATH`;snapshot 照旧显式设置自己的 `GIT_INDEX_FILE` 等);Ballast 自身的子进程(`--run-job`)与沙箱后端可执行文件只去掉 `LD_*`、`DYLD_*`——非 setuid 的 bwrap 会在建立命名空间之前加载 `LD_PRELOAD`,即在沙箱外执行;测试确实需要的 `LD_*` 经 `Spec.LoaderEnv` 由 bwrap `--setenv` 在沙箱内重新设置;macOS 的 `/usr/bin/sandbox-exec` 是平台二进制,dyld 对它丢弃 `DYLD_*`,所以 `DYLD_*` 不会到达 macOS 上的测试。hook 与 runner 进程自身已经带着 Claude Code 的环境启动,无法自保:这正是项目层 `env` 算作削弱的原因;darwin 发布构建必须启用 hardened runtime(`codesign -o runtime`,doctor `tier.binary_hardening` 检查,§3.5),Linux 发布构建是静态链接(`LD_*` 不起作用),管理员自建的 cgo 构建不受此保护(§11)。清洗的代价:人若在自己的环境里用 `GIT_CONFIG_GLOBAL` 等改变 git 配置,runner 的快照与 `verify --check` 读到的配置可能不同,结果只会是快照走 fallback 或判为过期(可见),不会把不一致判成新鲜。

**沙箱集成用例**(P2-SANDBOX 在两个平台上都要跑,boundary-t1 在容器里再跑一遍;每条都必须失败——Linux 上"新建"类用例的判据是运行结束后宿主上没有残留、job 为 infra `protected_path_created`、隔离目录里有该文件——同时"写 W 内文件"与 `git add` 必须成功):写 `$HOME/.bashrc`、`$HOME/.zshrc`、`$HOME/.profile`;写 `~/.claude.json`;写 `~/.ballast/evidence/x`;写 `<repo>/.ballast/project.json`;新建原本不存在的 `<repo>/.ballast/x`;`mv <repo> <repo>2` 后写再改回;`mv <repo>/.git <repo>/.git2 && mkdir <repo>/.git && echo x > <repo>/.git/config`;`ln ~/.ballast/rules.json <repo>/hl && echo x > <repo>/hl`;写 `<repo>/.git/hooks/pre-commit`;`curl https://example.com`(deny 策略);连接 `W` 外的 unix socket;**v1.3 追加**:在夹具 submodule `sub` 中写 `sub/.git/config`(先把 gitfile 换成目录)、改写 `sub/.git` gitfile 指向仓库内自建目录、写 `<commondir>/modules/sub/config` 与 `<commondir>/modules/sub/hooks/post-checkout`、写另一个 worktree 的 `worktrees/w2/config.worktree`、`git update-index --add --cacheinfo 160000,<sha>,evil && mkdir -p evil/.git && echo x > evil/.git/config`、新建 `<repo>/.claude/settings.local.json`、`<repo>/.vscode/tasks.json`、`<repo>/.mcp.json`(运行后宿主上都不存在)、在 pty 作为 runner 控制终端的前提下(python3 `pty` 模块;缺少时本地 SKIP,CI 必跑)打开 `/dev/tty`、对继承的 fd 0/1/2 调 `ioctl(TIOCSTI)`;macOS 另加 `open -a`、`osascript`、`launchctl submit` 三条;**v1.4 追加**:deny 策略下连接一个测试**之外**启动的 `127.0.0.1` 监听器(macOS 任何回环连接都失败;Linux 上测试自己的回环监听器仍可用);**allow 策略**下连接 `W` 外的 unix socket、访问 `/run`、`/var/run`,以及子进程环境中出现 `SSH_AUTH_SOCK`/`DOCKER_HOST`/`DBUS_*_BUS_ADDRESS`(断言不存在),同时到本地测试监听器的出站 TCP 必须成功;macOS 三条逃逸通道用 `TMPDIR` 里的副本与 python3 ctypes 在去掉 process-exec 规则的 profile 变体下再跑一遍;`LoaderEnv` 中的 `LD_LIBRARY_PATH` 能到达测试,而 bwrap 进程自身的环境里没有任何 `LD_*`;**v1.5 追加**:`Spec.Tree` 模式下写副本内文件成功,写真实工作树、真实 `.git` 与 `~/.ballast` 的其他位置失败,副本祖先改名失败;macOS 另加:命名偏好域 `defaults write net.ballast.probe.<rand> k v` 之后(沙箱外检查)`~/Library/Preferences/net.ballast.probe.<rand>.plist` 不存在且 `defaults read` 读不到——这一条写的是真实用户偏好,只在 `CI=true` 时运行,本地默认 SKIP,运行后无论成败都 `defaults delete` 清理(绝对路径域的对照组设计期已证明区分不了,不能替代);python3 ctypes `CGEventCreateKeyboardEvent` + `CGEventPost` 连接不到 WindowServer(`CGSMainConnectionID()` 为 0 或报连接失败)、`NSPasteboard`/`PasteboardCreate` 写入失败且沙箱外读回的剪贴板不变;`launchctl submit -l net.ballast.probe.<rand> -- /usr/bin/touch <W 外>`(标准路径与 `TMPDIR` 副本各一)不创建文件、沙箱外 `launchctl list` 中没有该标签(运行后 `launchctl remove` 清理);loopback 策略下在非回环地址上监听的用例记录实际行为(设计期实测为可监听,登记为残余风险,不作为失败条件);所有 IPC 用例同样在去掉 process-exec 拒绝的 profile 变体下运行。

**探测**:`sandbox.Probe` 用平凡命令实跑一次后端,并验证"对一个临时拒写目录的写入确实失败",结果按进程缓存。Linux 的 bwrap 只采用 root 所有、组和其他不可写的那个(防止 agent 在 `PATH` 里放假 bwrap)。常见不可用原因:未安装 bwrap;内核或 AppArmor 禁止非特权 user namespace(Ubuntu 24.04 默认 `kernel.apparmor_restrict_unprivileged_userns=1`)【未核验:CI 镜像】;Docker 容器内缺上表的三个 `--security-opt`(不使用 `--privileged`)。

**`ValidateSpec(fsys, spec)`**(包装前必须通过,否则 infra、error `sandbox_spec_invalid`,不执行):`W` 中任何条目不得是 `/`、真实家目录或其祖先,不得等于或位于 `D` 的目录之内(唯一例外是 v1.5 的 `Spec.Tree`:必须恰好是 `<Spec.WorkRoot>/<单级>/tree`、路径上没有符号链接,此时 `RepoRoots == [Tree]`、`GitDirs` 为空、`Cwd` 在副本内);`Cwd` 必须在 `W` 内;所有路径必须是绝对路径;`Spec.GitDirs` 必须与 `sandbox.ResolveGitDirs(fsys, r)` 对每个 `RepoRoots` 条目 `r` 的结果一致(包装时经 fsys 重查)。

**git dir 与 common dir 的来源校验(【决定】,v1.3)**:二者是从 agent 可写的文件推出来的(工作树里的 `.git` gitfile、git dir 下的 `commondir`),agent 还可以先 cd 进自己新建的嵌套仓库再触发规范 verify。`sandbox.ResolveGitDirs` 只接受两种形态:

1. **工作树之内**:`<wt>/.git` 是目录(不是符号链接),git dir = common dir = `<wt>/.git`;若有 `commondir` 文件,它必须指回同一目录。
2. **标准 worktree**:`<wt>/.git` 是 gitfile,指向 `X/.git/worktrees/<n>`(`<n>` 为单级),`EvalSymlinks` 之后 `commondir` 解析为 `X/.git`,`X/.git` 是目录,并且 `X/.git/worktrees/<n>/gitdir` 的内容(Clean、`EvalSymlinks` 后)恰好是 `<wt>/.git`。

其余一律失败(gitfile 指向别处、submodule 工作树的 git dir 位于父仓库的 `modules/` 下、`commondir` 不等于 `X/.git`、回指缺失或不符、`.git` 是符号链接),job 记 infra、`sandbox_spec_invalid`。submodule 工作树作为 verify 目标留作以后的契约追加。cd 进新建的嵌套仓库本身无害:它的 git dir 在它自己的工作树里(形态 1),`W` 就是那个仓库(origin=hook 时 `W` 是它的副本)。v1.5:origin=hook 的 `W` 不再含 git dir,但 `runner.Materialize` 在沙箱外运行 git 之前同样调用 `ResolveGitDirs`,来源不符即 infra,不运行任何 git 子进程。`commondir`、`gitdir` 两个链接文件本身在 `GitDirMandatoryDeny` 里,沙箱内的测试改不了它们。golden 与集成用例见 §3.6。

**策略与退化**(【决定】;`runner_sandbox` **只管 origin=cli**):

| origin | runner 自判 | 策略(org.json `runner_sandbox`) | 后端可用 | 行为 | 记录 |
|---|---|---|---|---|---|
| hook | 任意 | **忽略**(一律必须沙箱) | 是 | 实体化私有树(v1.5)后在副本里包装执行 | runner 自判 T1 ⇒ `tier:"T1"`;否则 `tier:"T0"`;`sandbox:"bwrap"`/`"sandbox-exec"`,`origin:"hook"`,`workspace:"copy"` |
| hook | 任意 | 忽略 | 否 | **不执行测试命令**,outcome `infra`,error `sandbox_unavailable`,审计 diag | `tier:"T0"`,`sandbox:"none"` |
| cli | T1 | `require`(T1 默认) | 是 | 包装后执行 | `tier:"T1"`,`sandbox` 如实 |
| cli | T1 | `require` | 否 | 不执行,outcome `infra`,error `sandbox_unavailable` | `tier:"T0"`,`sandbox:"none"` |
| cli | T1 | `prefer` | 是 / 否 | 可用则包装(`tier:"T1"`),否则不包装执行 | 不包装时 `tier:"T0"`,`sandbox:"none"`,`tier_missing:["runner_sandbox"]` |
| cli | T1 | `off` | — | 不包装执行 | `tier:"T0"`,`sandbox:"none"`,`tier_missing:["runner_sandbox"]` |
| cli | T0 | 默认 `off` | — | 现有行为 | `tier:"T0"` |
| cli | T0(root 所有的 org.json 写了 `prefer`/`require`) | 同左 | 是 | 包装后执行(纵深防御) | 仍是 `tier:"T0"`,`sandbox` 如实记录 |

`prefer`/`off` 只对 origin=cli 生效,而在 T1 主机上 origin=cli 只可能是人在终端里运行(agent 的 Bash 在 Claude 沙箱里,写不了证据,§3.3)——人选择在沙箱外运行自己仓库的测试,与人直接运行 `npm test` 等价,记录为 T0 如实反映这一点。

**`VerifyRecord.Tier == "T1"` 当且仅当 runner 自己判定为 T1 且 `Sandbox != "none"`。**(v1.5:origin=hook 的记录还恒为 `workspace:"copy"`;实体化失败的 job 是 infra,不会产生 T1 记录。) runner 的判定是证据 tier 的唯一权威,不采信 hook 传来的任何层级信息(`HookStartRequest` 里也没有这个字段;它带来的 `ProjectDir` 只用于**多查**一个目录,不能让结论变好,也不扩大可写集合)。**runner 自判 T1** = `Tier.Probe()` 为 T1(含条件 6 的用户层检查)**并且** `tier.RepoSandboxWeakened(fsys, <job 的仓库>)` 为空(v1.3)**并且** `HookStartRequest.ProjectDir` 非空且与仓库不同时 `tier.RepoSandboxWeakened(fsys, ProjectDir)` 也为空(v1.4);后者不为空时,策略表照常执行(hook 代起的 job 仍然必须沙箱),但记录 `tier:"T0"`、`tier_missing:["lower_sandbox_weakened"]`,并审计 `diag: tier_mismatch`(detail 只含 `<basename>:<key>`)。

**沙箱之外仍然成立的边界**:可写集合内的写入不限制(origin=hook 时可写的只是随后删除的副本与临时目录,外加 `runner_allow_write`;origin=cli 时测试要生成文件、改 git 索引——改动会进入 `C`,是可见的,不是绕过);`runner_network:"allow"` 时**出站 IP** 不限制(管理员显式选择;本地 socket 的隔离照常,见"本地 socket"),Linux 上主机的抽象命名空间 socket 可达;`runner_network:"loopback"` 时 macOS 上主机回环服务可达;自定义位置的 unix socket(例如家目录下的 docker.sock)在 Linux 上仍可连接。以上都登记为残余风险(§11)。

### 3.5 `org render` 与 `doctor` 的变化(P2-INSTALL)

- `org render` 的 denyWrite 与 permissions.deny 改为**直接输出 `tier.ManagedForms(lay, --binary, --protect...)`**,新增 `./.claude/settings*.json`;`--protect` 的值同时写进 org.json 的 `protected_extra`,保证 `Detect` 用同一组 extra 重算出同样的字符串。`internal/install` 的测试读取 `internal/tier/testdata/managed_forms.json`,断言 `org render` 输出逐字一致。Claude 沙箱对 `./`、`~/`、glob 写法的实际解释【需凭据,M0b case `managed-denywrite-path-forms`】不影响覆盖判定(覆盖判定只比字符串);若 M0b 证明某种写法不生效,改 `ManagedForms` 与 golden(契约变更)。
- 输出增加 `org.json` 段(含 `runner_sandbox:"require"`、`runner_network:"deny"`、`runner_allow_write:[]`、`weakened_project:<tier.DefaultWeakenedProject>`(v1.5 条件契约:M0b `managed-denywrite-anydepth` 未证明 `**/` 形式有效时为 `"deny"`,证明有效后为 `"audit"`;注释说明 `deny` 会让带削弱设置的项目里所有工具调用被拒,`audit` 只审计,并写明当前取值依据的 M0b 结论);v1.4 起 `runner_network` 的注释写明"deny 在 macOS 上连回环也拒绝;`loopback` 让 macOS 上的测试能连主机回环上的任何服务,`allow` 放开出站 IP 但不放开本地 socket,两者都是残余风险";stdout 渲染时另附注释:"`runner_allow_write` 的每一项都等于把该目录交给 agent 可改写的测试代码,写入在 job 结束后仍然存在且不进入内容快照;共享构建缓存会被沙箱外的构建复用。runner 已把常见缓存变量指向每个 job 的临时目录,建议保持为空";写入的 org.json 本身是纯 JSON,没有注释);新增 `--home-root <dir>`(可重复)写入 `home_root`;`--write --i-am-admin`(root)同时写 `managed-settings.d/ballast.json` 与 `<托管目录>/ballast/org.json`(0644,root)。
- `--write` 的家目录预检(【决定】):环境里有 `SUDO_UID` 时,对该 uid 调用 `sys.LookupHome`(拿不到该用户的 `$HOME`,所以只测 passwd 一步),并检查将要写入的 org.json 是否带 `home_root`;`ErrNoPasswdEntry` 且没有 `home_root` ⇒ **拒绝写入**,退出码 2,消息列出 §3.2 的三种修复;没有 `SUDO_UID`(例如 MDM 以 root 直接执行)⇒ 照常写入,stderr 提示"无法替最终用户预检,部署后请让每个用户运行 `ballast doctor`"。
- ConfigChange 行在 `Install` 翻为 true 之后才出现在托管 hook 中。
- MCP 白名单键名(`allowManagedMcpServersOnly`、`allowedMcpServers`,当前以 `omitempty` 出现在模板结构里)【未核验,M0b case `mcp-allowlist-nocred`】。
- `doctor`:`tier.level` 改为调用 `tier.Detect`,逐条列出 Missing;新增 `tier.declared_mismatch`(org.json 声明 T1 而判定为 T0 ⇒ fail)、`tier.runner_sandbox`(T1 且后端不可用 ⇒ fail,因为 hook 代起的 verify 将全部是 infra;沙箱探测函数由 CLI 注入)、`tier.redirect_ignored`(已锁定且重定向变量存在 ⇒ warn)、`tier.locked_home`(已锁定时实际调用 `tier.LockedHome`:成功 ⇒ pass 并显示来源 `passwd`/`home_root`;失败 ⇒ fail,提示按 §3.2 三种修复,并据 `debug.ReadBuildInfo` 的 `CGO_ENABLED` 说明"本构建为静态构建,只读 /etc/passwd";**cgo 构建下**无论成败都附注"passwd 查询走 NSS,只有 NSS 由本地缓存(sssd 本地缓存或 nscd)提供时才符合热路径不联网;nss_ldap 或未缓存的 sssd 会让每次 hook 调用可能联网或阻塞,推荐改用 home_root")、`tier.repo_sandbox_weakened`(v1.3:doctor 对当前目录及其仓库顶层调用 `tier.RepoSandboxWeakened`(v1.4 起两者都查,因为 Claude 加载的是启动目录的项目层),不为空 ⇒ T1 主机上 fail、其他主机 warn,列出 `<basename>:<key>`,提示"在此目录启动的 Claude 会话可以绕过沙箱直接写证据(全机前提,trust-model §2.3);该仓库的 hook 代起 verify 只会记为 T0";用户层的削弱已经体现在 `tier.level` 的 Missing `lower_sandbox_weakened` 里)、`tier.binary_hardening`(v1.4:darwin 上用标准库 `debug/macho` 读 `Self` 的代码签名 CodeDirectory,未带 `CS_RUNTIME`(hardened runtime)⇒ T1 主机上 fail、其他主机 warn,提示 `codesign --force -s - -o runtime <binary>`——否则项目层 `env` 里的 `DYLD_INSERT_LIBRARIES` 会被托管 hook 加载;linux 上 `debug/elf` 发现 `PT_INTERP`(动态链接,例如自建的 cgo 构建)⇒ warn,说明 `LD_PRELOAD` 同理)、`tier.locked_home` 追加计时(v1.4:查询超过 `tier.DefaultLockedHomeTimeout` ⇒ fail,提示该账户在每次 hook 调用时都会因超时而放行;v1.5:darwin 上查询超过 5ms 时附注"目录账户(AD/LDAP 绑定、未缓存的移动账户)的 passwd 查询经 opendirectoryd 可能联网或阻塞,违反热路径不联网;请启用移动账户或目录服务本地缓存",与 cgo 构建下的 NSS 提示同列)、`tier.level` 在 `CLAUDE_CONFIG_DIR` 被重定向时列出 `sandbox_deny_write` 并说明原因(v1.5)。doctor 本身在 layout 解析失败时仍要能运行到这一项(doctor 用 `$HOME` 回退只为能输出诊断,不读写任何状态)。

### 3.6 验收(T1 部分)

| 单元 | 可执行验收 |
|---|---|
| P2-TIER | `go test ./internal/tier ./internal/layout`:`testdata/detect.json` 逐条翻转六个条件(每次只翻一条 ⇒ T0 且 Missing 恰好是该 id;条件 6 对 `WeakenedSandboxKeys` 点名的每个键各一行,分别放在用户层 `settings.json` 与 `settings.local.json`,另有"`CLAUDE_CONFIG_DIR` 指向别处而 `<home>/.claude/settings.json` 削弱"一行;托管层 `excludedCommands:["npm"]` ⇒ `sandbox`;v1.5:`CLAUDE_CONFIG_DIR` 指向 `<home>/alt` 而其他全部满足 ⇒ `sandbox_deny_write`,指向 `<home>/.claude/`(带尾斜杠,Clean 后相等)⇒ T1);`testdata/managed_forms.json` 含 v1.5 的 `**/` 两组;`EffectiveWeakenedProject`:空 ⇒ `DefaultWeakenedProject`,显式值原样;`testdata/weakened.json`:`WeakenedSandboxKeys` 白名单表,每行在 `LayerUser` 与 `LayerRepo` 下各跑一次(无害值逐字不报;每个点名键报告;未知键报告;`sandbox`、`permissions` 非对象报告;非 JSON 对象报错;v1.4:顶层 `env`、`statusLine`、`apiKeyHelper`、`awsAuthRefresh`、`awsCredentialExport`、`otelHeadersHelper`、`enabledPlugins`、`extraKnownMarketplaces` 在用户层不报、在项目层报;`enableAllProjectMcpServers:true` 与未知顶层键两层都报;`permissions.defaultMode:"bypassPermissions"` 与 `permissions.foo` 报告);用户层 `statusLine` ⇒ `Detect` 仍为 T1;托管目录或 `<托管目录>/ballast` 组可写 ⇒ `org_config` 且 `Locked` 为 false(v1.4);`ProjectChecker.Weakened == RepoSandboxWeakened`;`RepoSandboxWeakened`:项目层 `excludedCommands:["npm"]`、本地层 `allowUnixSockets` 各报一项,符号链接的 `.claude` 报 `symlink`,不存在 ⇒ 空;全部满足 ⇒ T1;hook 命令为指向 Self 的符号链接 ⇒ T1;相对路径命令 ⇒ `managed_hooks`;`testdata/managed_forms.json` golden;`Locked` 表(不存在 / 非 root / 组可写 / `tier:"T0"` / 校验失败 ⇒ false);`ResolveWith(env, Options{}) == Resolve(env)` 表驱动;`IgnoreRedirects` 下三个变量无效且家目录取自 `PasswdHome`;`LockState` 与 `Locked` 一致;`testdata/locked_home.json`:阻塞的 `Lookup` ⇒ 在 `Timeout` + 5ms 内返回 `ErrLockedHomeTimeout`,且不尝试 home_root 规则(v1.4)/ passwd 成功(`$HOME` 不同也用 passwd)/ 非 `ErrNoPasswdEntry` 错误 ⇒ 失败 / `ErrNoPasswdEntry` 且无 `home_root` ⇒ 失败 / home_root 规则 (a)–(d) 逐条翻转各一行 ⇒ 失败且错误指明该条 / 全部满足 ⇒ `home_root`;`sys.LookupHome` 对当前 euid 返回非空绝对路径(darwin、linux),对一个不存在的 uid 返回 `ErrNoPasswdEntry`,windows 返回 `ErrPasswdUnsupported`;用计数 FS 断言 `Probe` 第二次调用零 I/O;`BenchmarkProbe` ≤ 200µs,`BenchmarkLocked` ≤ 30µs,`BenchmarkLockedHome`(真实 `sys.LookupHome` 查当前用户,结果丢弃;另一子基准走 home_root 规则的假 FS)记录数值,冷进程成本由 bench 场景 `13` 把关 |
| P2-SANDBOX | `go test ./internal/sandbox`:`CacheEnv` 表(默认全部指向临时目录;当前值位于 allowWrite 内的变量保留;Wrap 的 `Env` 包含它);`HostEnv` 表(v1.4:`GIT_CONFIG_PARAMETERS`、`GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_0`、`GIT_TRACE`、`GIT_EXEC_PATH`、`LD_PRELOAD`、`DYLD_INSERT_LIBRARIES`、`BASH_ENV` 被去掉,`PATH`、`HOME`、`LANG` 保留且顺序不变);`Wrapped.Env` 在 deny、loopback、allow 三种策略下都移除 `SSH_AUTH_SOCK`、`DOCKER_HOST`、`DBUS_SESSION_BUS_ADDRESS`、`DBUS_SYSTEM_BUS_ADDRESS` 与全部 `LD_*`/`DYLD_*`;`DarwinProfile`/`BwrapArgs`/`AncestorPins` golden(`testdata/profiles.json`,含空格、引号、括号、非 ASCII 路径;`BwrapArgs` 含 `--new-session`;darwin 设备放行中没有 `/dev/tty*`;强制条目渲染为锚定的任意深度 regex;v1.4:三种网络策略各一组——deny 的 darwin profile 不含任何 `remote ip`,loopback 只含 `localhost:*`,allow 含 `*:*` 与 mDNSResponder;三种策略的 unix-socket 放行都只有 `W`;`BwrapArgs` 在三种策略下都含 `--tmpfs /run --tmpfs /var/run`,只有 allow 不含 `--unshare-net`;`LoaderEnv` 渲染为 `--setenv`);`testdata/mandatory.json`:`MatchMandatory` 表(顶层与嵌套 `.git`、`.git/config`、`.gitmodules`、`a/b/.vscode/x`;`modules/a/b/config`、`modules/x/hooks/h`、`worktrees/w/gitdir` 命中;`.git/index`、`.git/objects/ab/cd`、`worktrees/w/HEAD`、`src/config` 不命中);`testdata/gitdirs.json`:`ResolveGitDirs`/`ValidateSpec` 表(普通仓库、标准 worktree 通过;gitfile 指向仓库外任意目录、指向另一个仓库的 `.git`、`commondir` 被改、`gitdir` 回指缺失或指向别处、`.git` 为符号链接、submodule 工作树 ⇒ `ErrInvalidSpec`);`ScanMandatory` 超过 `MaxMandatoryMatches` ⇒ `ErrInvalidSpec`;`PostCheck` 发现新匹配(含改名后的祖先下)与 `Quarantine`(rename、EXDEV 复制、都失败时删除;符号链接不跟随);`ValidateSpec` 表;`Decide` 全表(origin=hook 在任何策略下无后端都返回 `ErrUnavailable`);§3.4 "沙箱集成用例"在 darwin(有 sandbox-exec)与 linux(有 bwrap)上逐条断言失败、W 内写入与 `git add` 成功;缺后端本地 SKIP,`CI=true` 且 `BALLAST_REQUIRE_BWRAP=1`/`BALLAST_REQUIRE_SANDBOX_EXEC=1` 时 FAIL;**v1.5**:darwin golden 含 IPC 段(`DarwinMachAllow` 按序出现、不含任何 `DarwinMachForbidden` 前缀、显式拒绝齐全、三种网络策略下的 `network-bind`/`network-inbound` 规则),`Spec.Tree` 模式的 golden(darwin 中 `(allow file-write* (subpath <Tree>))` 位于 `~/.ballast` 拒绝之后、强制条目之前;bwrap 中 `--bind <Tree>` 位于 `~/.ballast` 的 `--ro-bind` 之后),`ValidateSpec` 对 `Tree` 的接受与拒绝行(不在 `WorkRoot` 下、多级、经符号链接 ⇒ 拒绝),`Wrapped.Env` 在 `Tree` 模式含 `GIT_CEILING_DIRECTORIES`;§3.4 v1.5 追加的集成用例。**必过验收(只需 Docker,无凭据)**:本单元新增独占脚本 `scripts/sandbox-docker.sh`——在 Debian 12 容器(`golang:<go.mod 版本>-bookworm`,以 digest 固定;构建期 `apt-get install bubblewrap git python3` 并 `go mod download`)里,以 `--security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined`、`--network none`、普通用户(uid 1000,`/usr/bin/bwrap` 为 root 所有)、只读挂载仓库后复制到容器内工作目录的方式运行 `CI=true BALLAST_REQUIRE_BWRAP=1 go test ./internal/sandbox`(参数可替换为其他包),跑全部 Linux 集成用例(含 `Baseline` ro-bind、`PostCheck` 递归扫描与 `Quarantine`——设计期从未实测的部分);无 Docker 时显式 SKIP(退出码 0),`CI=true BALLAST_REQUIRE_DOCKER=1` 时失败;不读取、不传入任何凭据,不写宿主仓库 |
| P2-RUNNER | `go test ./internal/runner`:用假 `TierProbe` 与假沙箱后端覆盖 §3.4 策略表全部行;被包装的命令以 `Setsid` 启动、stdin 为 `/dev/null`、输出为文件或管道(前台模式也是管道;断言子进程 `getsid == getpid`);仓库层 `.claude/settings.json` 带 `excludedCommands` ⇒ 仍在沙箱里执行、记录 `tier:"T0"` 与 `tier_missing:["lower_sandbox_weakened"]`、审计 `tier_mismatch`;v1.4:仓库干净而 `HookStartRequest.ProjectDir`(仓库内子目录 `sub`,`sub/.claude/settings.json` 带 `env`)削弱 ⇒ 同样记 T0;快照的 git 子进程拿到的环境里没有 `GIT_CONFIG_PARAMETERS`、`LD_PRELOAD`(假 GitRunner 记录 env);`--run-job` 子进程与沙箱后端的环境里没有 `LD_*`/`DYLD_*`;`ResolveGitDirs` 失败 ⇒ infra `sandbox_spec_invalid` 且不执行;`PostCheck` 报告新匹配 ⇒ 调用 `Quarantine`(目标 `<QuarantineDir>/<job_id>`)、infra `protected_path_created`、审计 `sandbox_quarantine`;**断言 origin=hook 在 `prefer`、`off`、后端不可用的组合下都不执行测试命令**(假后端记录"未调用"、job outcome infra、error `sandbox_unavailable`、无 pass 证据);`StartRequest` 首个 job id == request_id、请求文件内容、配置错误也写请求文件;`RequestStatus` 的取最差与退出码表;`ValidateSpec` 失败 ⇒ infra;Linux 事后检查 `protected_path_created`;真实后端可用时的端到端(测试命令写受保护路径失败、记录 `tier:"T1"`);**v1.5**:`Materialize` 在静止的夹具仓库上 `S_start == snapshot.TreeS` 且副本内容与 `T_full` 逐文件相等(含 exempt 路径、可执行位、符号链接,不含 `.git`);攻击用例——工作树里未跟踪的 `zz_test.go`(`TestMain` 直接 `os.Exit(0)`)与写进 `.git/info/exclude` 的 `conftest.py` 不出现在副本、job 按真实测试结果记录;测试运行期间(由假后端回调触发)把真实工作树的测试文件换成平凡版本再换回 ⇒ 副本不受影响、记录的结果来自原测试;实体化时刻读到平凡版本 ⇒ `S_start` 即平凡版本的树,恢复后 `verify --check` 判过期;`inputs_untracked:["node_modules/**"]` 复制被忽略的文件、`inputs_hash` 与按冻结格式独立计算的值相等、未命中的被忽略文件不复制、硬链接从不出现(复制后改源文件,副本不变);超限 ⇒ infra `inputs_too_large`;含 gitlink ⇒ infra `tree_has_submodule` 且测试未执行;测试改副本内已跟踪文件 ⇒ `S_end != S_start`;收尾顺序(假后端与假快照记录调用序列:退出 → killpg(darwin)→ `PostCheck` → `Quarantine` → `TreeSEnd` → 记录 → `CleanupTree`);`ResolveGitDirs` 不符 ⇒ infra 且没有 git 子进程;`project.json` 的 `inputs_untracked` 解码与校验(`internal/project` 的追加行,非法 glob 进 `testdata/invalid.json`);origin=cli 仍在工作树执行、`workspace:"worktree"`。**真实后端端到端复用 `scripts/sandbox-docker.sh ./internal/runner`**(Linux,必过,只需 Docker),macOS 上有 sandbox-exec 时本地再跑一遍 |
| P2-HOOK | `go test ./internal/hook`:`pipeline.json` 追加行——v1.4:`ProjectSandbox` 假实现对 `$CLAUDE_PROJECT_DIR` 返回非空且会话首个事件 ⇒ 恰好一行 `lower_sandbox_weakened`,detail 只有 entries;同会话第二个事件 ⇒ 不再写;`CLAUDE_PROJECT_DIR` 未设置 ⇒ 查事件 cwd 与仓库顶层;`WeakenedProject:"deny"` 时同会话的每个 PreToolUse(Bash、Edit、MCP 各一行)都被 deny、reason 为冻结模板、审计 `lower_sandbox_deny`;`ProjectSandbox` 为 nil ⇒ 都不发生;`IDs` 为 nil 且假 T1 ⇒ 按 spawn 失败处理;规范 verify 的 `HookStartArgv` 带 `--project-dir`;`RedirectsIgnored:["BALLAST_HOME"]` 且会话首个事件 ⇒ 恰好一行 `env_redirect_ignored`,detail 只有变量名;同会话第二个事件 ⇒ 不再写;`RedirectsIgnored` 为 nil ⇒ 不写;规范 verify + 假 T1 ⇒ deny 且 reason 含 request_id、Spawner 收到 `HookStartArgv`;T0 ⇒ 无特殊处理;非规范形式(管道、`--status`、`env X=1 ballast verify`、动态词)⇒ 无特殊处理;spawn 失败 ⇒ deny + `verify_spawn_failed`;`ParseVerifyRequest` 与 `HookStartArgv`/`ParseHookStartArgv` 往返的表驱动测试与 fuzz(含 `ProjectDir`;相对 `ProjectDir` 被拒);v1.5:ConfigChange 审计行的 `detail.source` 取 `Event.Source`、`detail.file` 由 `Event.ConfigFile` 改写而来(用直接构造的 `model.Event`,不依赖 P2-CC 的解析),`ConfigFile` 为空 ⇒ `file` 省略;`WeakenedProject` 为空时按 audit 处理(CLI 生产中总会填值) |
| P2-CLI | 接线:`tier.LockState` → `layout.ResolveWith`(`PasswdHome` = 对 `tier.LockedHome` 的闭包,带默认时限)→ 按实际存在的变量填 `hook.Deps.RedirectsIgnored`;v1.4:`hook.Deps.IDs` 恒填 `c.ids`;锁定时填 `ProjectSandbox: tier.ProjectChecker{FS}` 与 `WeakenedProject: tier.EffectiveWeakenedProject(org)`(v1.5,org.json 缺省时取条件契约的默认值);cli 单测用假 FS/Owner 断言锁定时各项都接上、未锁定时 `RedirectsIgnored` 与 `ProjectSandbox` 为 nil 且 `PasswdHome` 不被调用,`IDs` 任何时候非 nil;`PasswdHome` 阻塞时 `WireHook` 在时限内返回错误、hook 退出码 1 且 stdout 为空;bench 新场景 `14`:锁定(`ballast_fault` 构建下强制)且 `weakened_project:"deny"`、项目目录无 `.claude` 与有一个干净 `.claude/settings.json` 两种,PreToolUse p95 ≤ 30ms;bench 新场景 `13`:`ballast_fault` 构建下 `BALLAST_TEST_LOCKED=1` 强制 `Locked=true`,真实调用 `sys.LookupHome` 与 home_root 判定并计时,但**结果丢弃、仍用重定向的家目录**(保证不碰真实 `~/.ballast`),p95 仍满足 B10 门槛;bench 新场景 `12`:假 T1 下的规范 verify PreToolUse p95 ≤ 30ms(用 `BALLAST_TEST_TIER` 之类只在 `ballast_fault` 构建标签下生效的开关,生产构建忽略);既有 bench 场景在加入 `Locked` 后仍满足 B10 门槛;场景 12–14 与 ConfigChange 场景需要改的 `scripts/bench.sh` 与 `scripts/bench/**` 都归本单元(v1.5);集成时按 `docs/facts/m0b-cases.json` 的 `managed-denywrite-anydepth` 定稿 `tier.DefaultWeakenedProject` 与 `org render` golden(§3.2 条件契约),cli 单测断言两者一致 |
| P2-INSTALL(T1 相关) | `go test ./internal/install`:`org render --home-root` 写入 `home_root`;stdout 渲染含 `runner_allow_write` 风险注释、写入文件为纯 JSON;`--write` 预检表(有 `SUDO_UID` 且 `ErrNoPasswdEntry` 无 `home_root` ⇒ 退出码 2 且什么都不写;有 `home_root` ⇒ 写;无 `SUDO_UID` ⇒ 写并提示);doctor `tier.locked_home` 的 pass/fail 与提示文本 golden(含 cgo 构建下的 NSS 本地缓存提示);doctor `tier.repo_sandbox_weakened` 在 T1 下 fail、T0 下 warn,当前目录与仓库顶层都查;v1.4:`org render` 输出含 `weakened_project:"audit"` 及 `runner_network` 的回环、socket 说明注释(golden);doctor `tier.binary_hardening`(darwin 夹具:带与不带 `CS_RUNTIME` 的 Mach-O;linux 夹具:静态与带 `PT_INTERP` 的 ELF);`tier.locked_home` 用阻塞的假 `Lookup` 报 fail 且提示超时;v1.5:`org render` 的 denyWrite 与 permissions.deny 与含 `**/` 两组的共享 golden 逐字一致;org.json 段的 `weakened_project` 等于 `tier.DefaultWeakenedProject`、注释写明依据;darwin 上慢查询(假 `Lookup` 延迟 10ms)时 `tier.locked_home` 附注目录账户提示;`tier.level` 在 `CLAUDE_CONFIG_DIR` 重定向时列出 `sandbox_deny_write` |
| P2-REPORT(v1.4 相关) | `go test ./internal/report`:`model.DiagNames` 的每个名字都原样计入 `Diags`;未知名字与非字符串值计入 `other`,输出中不出现该名字 |
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
- 输入只有审计日志(`ports.AuditSource`);输出结构见 `internal/report.Report`:窗口、版本、GOOS、会话数与仓库数(只计数)、各客户端与各 tier 的记录数、按规则聚合(与 `ballast stats` 同语义)、诊断计数(名称是闭合枚举 `model.DiagNames`,§3.3"诊断名清单";其他值记入 `other`)、verify 结果按 outcome/tier/sandbox/origin 计数、hook 延迟直方图与 p95、按 Kind 的脱敏命中计数、损坏行数。
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
- **字段(【决定】,v1.5,已在设计提交落地)**:`model.Event.ConfigFile string` 承载变更的文件路径(原样);payload 的 `source` 复用既有的 `Event.Source`(`common.FillCommon` 对所有事件都映射 `source` 键),不另设 `ConfigSource`。claudecode adapter 的映射占位是 `configChangeFileKey`(当前按文档写 `file_path`,【未核验】)与 `fillConfigChange`(no-op)。**P2-CC 只负责填值**(M0b 取得 payload 后在 `fillConfigChange` 中设置 `ConfigFile`,键名不符时改 `configChangeFileKey`,补 golden);**P2-HOOK 只负责读取**(审计时只读 `Event.Source` 与 `Event.ConfigFile`,不解析 payload)。两个单元因此都不改 `internal/model/event.go`,可以并行。
- **hook 行为**(P2-HOOK):读取 `Event.Source` 与 `Event.ConfigFile`(为空一律省略);写一行 `type:"config_change"` 审计,detail = `{source, file(由 ConfigFile 按 transcript_ref 的规则改写成 "~/" 或 "<repo>/" 相对形式,不在两者之下时只留 basename), protected(经 PathService 判断是否命中受保护路径)}`;**stdout 永远为空**(【决定】只审计、绝不阻断:阻断配置变更可能把用户锁在外面);不读写会话状态(便宜、不争锁)。任何异常照 B7:stdout 空、exit 1。
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
| t1-runner-sandbox | 夹具仓库的 `project.json` 里 verify `test` 是一个脚本 `scripts/boundary/t1-escape.sh`:依次尝试 `echo > ~/.ballast/evidence/x`、`sed -i` 改 `.ballast/rules.json`、`python3 -c` 写 `~/.ballast/rules.json`、`touch` 二进制目录、创建原本不存在的 `<repo>/.ballast/x`,以及 §3.4 "沙箱集成用例"的全部 Linux 条目——写 `~/.bashrc`/`~/.zshrc`/`~/.profile`/`~/.claude.json`;`mv <repo> <repo>2` 再改回;`mv .git .git2 && mkdir .git && echo x > .git/config`;`ln ~/.ballast/rules.json hl && echo x > hl`;写 `.git/hooks/pre-commit`;连接容器内 `/run` 下预置的 unix socket;`--network bridge` 变体里 `curl` 一个外部地址(构建镜像时可达的同一镜像源);**v1.4 追加**:把 org.json 改为 `runner_network:"allow"` 再跑一次 `--network bridge` 变体——连接 `/run` 下的 socket 仍失败、`SSH_AUTH_SOCK`/`DOCKER_HOST`/`DBUS_SESSION_BUS_ADDRESS` 不在测试环境中,而 `curl` 外部地址成功;**v1.3 追加**(夹具仓库带 submodule `sub` 与第二个 worktree `w2`):把 `sub/.git` 换成目录后写 `sub/.git/config`、改写 `sub/.git` gitfile、写 `<commondir>/modules/sub/config` 与 `modules/sub/hooks/post-checkout`、写 `worktrees/w2/config.worktree`、`git update-index` 加 gitlink 后建 `evil/.git/config`、新建 `.claude/settings.local.json`——每次写或连接失败计 1;全部失败、且同一脚本里写 `<repo>/ok.txt` 与 `git add` 成功才 `exit 0`。job 结束后在宿主侧断言:`sub/.git` 仍是原 gitfile、`evil/.git` 与 `.claude/settings.local.json` 不存在(若曾被创建,应出现在 `~/.ballast/quarantine/<job>/` 且 job 为 infra `protected_path_created`;按 Linux 设计,这些"新建"类条目要么创建失败,要么被隔离,两种都算通过,但宿主仓库里不能有残留)。把合成的 PreToolUse payload(`ballast verify test`)喂给托管 hook 命令 ⇒ stdout 为 deny 且含 job id;等待 job ⇒ 记录 `tier:"T1"`、`sandbox:"bwrap"`、`origin:"hook"`、`workspace:"copy"`、outcome pass;受保护文件的哈希前后一致;**v1.5 追加**:脚本另外尝试写真实仓库的文件与 `.git/info/exclude`(经副本之外的绝对路径)⇒ 失败;job 结束后 `~/.ballast/work/<job>` 不存在。另跑两个伪造用例:① 夹具仓库的 `test` 真实失败,工作树里放一个未跟踪的 `zz_test.go`(`TestMain` 直接 `os.Exit(0)`,路径写进 `.git/info/exclude`)⇒ 代起的 job outcome fail;② 测试脚本运行期间,容器里另一个进程把真实工作树的测试文件换成平凡版本、在 job 结束前换回 ⇒ 结果仍是原测试的 fail;③ 含 submodule 的夹具 ⇒ infra `tree_has_submodule` |
| t1-tty | 容器内用 python3 `pty` 给普通用户 shell 一个控制终端,在其中运行 `ballast verify --foreground tty-probe`(origin=cli,`runner_sandbox:"require"`);`tty-probe` 依次尝试打开 `/dev/tty`、对 fd 0/1/2 调 `ioctl(TIOCSTI)` 注入 `touch /tmp/pwned\n`、写 `/dev/tty`——全部失败才 `exit 0`;断言记录 `tier:"T1"`、`sandbox:"bwrap"`、outcome pass,且 pty 主端读不到注入的字节、`/tmp/pwned` 不存在 |
| t1-lower-sandbox | (v1.5:①②⑤ 先以 root 把 org.json 写为显式 `weakened_project:"audit"`,因为缺省值按条件契约可能是 `deny`;⑥ 用 `deny`,另加 ⑧ 删去该键 ⇒ 行为等于 `tier.DefaultWeakenedProject`)① 夹具仓库的项目层 `.claude/settings.json` 写 `{"sandbox":{"excludedCommands":["npm"]}}`(以 root 预先放入,模拟随仓库 clone 进来):把合成 PreToolUse(`ballast verify test`)喂给托管 hook ⇒ 照常 deny 并代起;job 记录 `tier:"T0"`、`tier_missing:["lower_sandbox_weakened"]`、`sandbox:"bwrap"`(仍在沙箱里执行);审计有 `tier_mismatch`;`ballast doctor --json`(在该仓库内)`tier.repo_sandbox_weakened` 为 fail。② 换成本地层 `settings.local.json` 写 `network.allowUnixSockets`,结果相同。③ 用户层 `~/.claude/settings.json` 写 `{"sandbox":{"filesystem":{"allowWrite":["~"]}}}` ⇒ `tier.Detect` 为 T0、Missing 恰好是 `lower_sandbox_weakened`,hook 不代起;doctor `tier.declared_mismatch` 为 fail。④ 只写无害值(`{"sandbox":{"enabled":true,"filesystem":{"denyWrite":["/x"]}}}`)⇒ 仍为 T1。**v1.4 追加**:⑤ monorepo 子目录:仓库根干净,`sub/.claude/settings.json` 写 `{"env":{"GIT_CONFIG_COUNT":"1"}}`,合成 PreToolUse 的环境带 `CLAUDE_PROJECT_DIR=<repo>/sub`、cwd 为 `<repo>/sub` ⇒ 会话首个事件审计 `lower_sandbox_weakened`;代起的 job 记 `tier:"T0"`、`tier_missing:["lower_sandbox_weakened"]`;runner 快照的 git 子进程环境里没有 `GIT_CONFIG_*`(用一个记录环境的 git 包装脚本在 PATH 最前面观察)。⑥ 以 root 把 org.json 改为 `weakened_project:"deny"` 后重放 ⑤ 的 PreToolUse(Bash 与 Edit 各一)⇒ stdout 为 deny、reason 为冻结模板、审计 `lower_sandbox_deny`,不代起 job。⑦ 用户层 `~/.claude/settings.json` 只写 `{"statusLine":{"type":"command","command":"true"}}` ⇒ 仍为 T1 |
| t1-config-dir | (v1.5)以 `CLAUDE_CONFIG_DIR=/home/agent/alt`(agent 所有)调用托管 hook 与 `ballast doctor --json` ⇒ `tier.level` 为 T0、Missing 含 `sandbox_deny_write`,规范 verify 不代起;`CLAUDE_CONFIG_DIR=/home/agent/.claude/` ⇒ 仍为 T1 |
| t1-forged-binary | 用户可写目录里的 ballast 副本作为 hook 命令 ⇒ `tier.Detect` 为 T0(`binary_root_owned`),不代起;该副本运行的 verify 记录为 T0 |
| t1-orgjson | org.json 删除 / 改为 agent 所有 / `tier:"T0"` / (v1.4)`chmod g+w <托管目录>/ballast` 且该组含 `agent` ⇒ 各自 T0 + `org_config`,`TierInfo.Locked` 为 false;doctor 报 `tier.declared_mismatch` |
| t1-env-override | 以 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT`(都指向 agent 可写的目录,里面放一份放行一切的 rules.json 与伪造的 pass 证据)和伪造的 `HOME` 调用托管 hook ⇒ 判定仍为 T1(重定向锁生效,`TierInfo.Locked:true`);org 规则仍然生效(一条只在 org 规则里的 deny 规则照常拦截);伪造证据不被采信(`evidence-before-commit` 照常 unknown);审计有 `env_redirect_ignored`;agent 目录下没有新文件 |
| t1-passwd-missing | 以一个 `/etc/passwd` 里没有的 uid(`docker exec -u 4242`,`HOME=/home/ghost`,目录由 root 预建、属 4242、0755)运行:① org.json 无 `home_root` ⇒ `ballast doctor --json` 的 `tier.locked_home` 为 fail,提示含 `home_root`;把合成 PreToolUse 喂给托管 hook ⇒ stdout 为空、退出码按 B7(放行),`/home/ghost/.ballast` 没有新文件;② 以 root 执行 `org render --home-root /home --write --i-am-admin` 之后 ⇒ `tier.locked_home` 为 pass 且来源 `home_root`,判定 T1;③ `HOME=/tmp/ghost`(属 4242)⇒ fail(不在 home_root 下);④ `chmod 0777 /home/ghost` ⇒ fail;⑤ 恢复为无 `home_root` 的 org.json 后,以 root 执行 `SUDO_UID=4242 ballast org render --binary … --write --i-am-admin` ⇒ 退出码 2,托管目录下的文件逐字节不变 |
| t1-hooks-outside-sandbox | 引用 M0b `hooks-run-outside-sandbox` 的结论;容器里用 org render 生成的托管配置(`sandbox.enabled:true`)再跑一次:托管 SessionStart 审计行写入 `~/.ballast/audit`(denyWrite 中)成功。前提同 `t1-lower-hooks`;M0b 降级时同样标 needs_credentials |
| t1-sandbox-missing | 去掉 bwrap 的镜像变体 ⇒ hook 代起的 job outcome infra、error `sandbox_unavailable`、测试脚本没有被执行(脚本第一行写一个标记文件,断言标记不存在)、无 pass 证据;把 org.json 改成 `runner_sandbox:"prefer"` 与 `"off"` 各重跑一次,结果相同;doctor `tier.runner_sandbox` 为 fail |
| t1-lower-hooks | **取决于 M0b `nocred-events`**:若无凭据时 SessionStart 能触发——用户层另装一个探针 hook 并设 `disableAllHooks:true`,运行 `claude -p`(假 key + 黑洞端点)⇒ 托管 ballast 的 SessionStart 审计行存在,用户探针没有被调用。若 M0b 证明不触发,该项标 needs_credentials |

**需要 agent 真正尝试的部分【需凭据】**:让 agent 用 Edit、`echo >`、`sed -i`、`python -c` 写受保护路径;伪造 `ballast` 放进 `PATH`;直接写 evidence;MCP 写入(按白名单配置验证);在项目层带 `excludedCommands:["npm"]` 的仓库里让 agent 用 `npm run` 写 `~/.ballast/evidence`(验证条件 6 的前提:写入能成功,而 Ballast 已把该仓库的证据判为 T0);(v1.5)让 agent 在子目录写 `a/b/.claude/settings.json` 与 `a/.ballast/x`,并在子目录启动的会话里写仓库根的 `.claude/settings.json`(验证 `**/` 形式,与 M0b `managed-denywrite-anydepth` 同一断言)。期望除"`npm run` 写证据"外全部失败(`**/` 一项的实际结果决定 §3.2 条件契约的取值)。补测命令:

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
| `lower-sandbox-merge` | (v1.3)托管层按 `org render` 的 T1 模板(`excludedCommands: []` 等);用户层、项目层、本地层分别写 `sandbox.excludedCommands:["npm"]`、`network.allowUnixSockets:["/var/run/docker.sock"]`、`network.allowAllUnixSockets:true`、`filesystem.allowWrite:["/home/agent"]`、`enableWeakerNestedSandbox:true`、`network.allowedDomains:["example.com"]`,以及顶层 `permissions.additionalDirectories:["/home/agent"]` ⇒ 每个键是否并入生效的沙箱配置(数组拼接 / 被托管层替换 / 被忽略)。另记录 `~/.claude.json` 能否承载 `sandbox` 键(全局或按项目)。**v1.4 追加**:项目层与本地层分别写顶层 `env`(`{"BALLAST_PROBE_ENV":"1"}`)、`apiKeyHelper`、`statusLine`、`awsAuthRefresh`、`otelHeadersHelper`(都是写标记文件的探针命令)、`enabledPlugins`、`enableAllProjectMcpServers` ⇒ 每个键在项目层是否生效、是否先经过工作区信任确认(`env` 由托管探针 hook 记录自身环境即可观察;`apiKeyHelper` 在 `none` 模式下由客户端取 key 时执行,看标记文件;`statusLine` 用 ②);在仓库子目录与父目录启动时 `CLAUDE_PROJECT_DIR` 的值、加载的是哪个目录的项目层,父目录的 `.claude/settings.json` 是否也被加载。**观测手段按顺序尝试**:① `claude --debug`(或 `--debug-file`)启动日志是否打印生效的沙箱配置;② 在 pty 里驱动交互式 TUI(`blackhole` 模式,预置 `customApiKeyResponses` 跳过 key 确认),输入本地命令 `/sandbox` 抓屏(本地命令不请求模型)【未核验:可行性】;③ §6.6 模拟模型发出 `npm run x`、`curl --unix-socket …` 的 Bash 调用,看是否在沙箱外执行(`mode:"mock"`);都观察不到 ⇒ `needs_credentials`,`recheck` 为 §6.5 的补测命令加 `--cases lower-sandbox-merge`(让 agent 在各层配置下执行 `npm run write-outside` 与 `curl --unix-socket`)。结论用于放宽 `tier.WeakenedSandboxKeys` 的白名单(只放宽有实测依据的键,契约变更);若父目录的项目层也会被加载,hook 与 runner 的检查目录追加父目录链(契约变更);在结论出来之前条件 6 保持保守 | 否(观测不到时降为需凭据) |
| `sandbox-denywrite-bash` | agent 用 Bash `echo >`、`sed -i`、`python3 -c` 写 denyWrite 路径 ⇒ 失败 | **是** |
| `permissions-deny-edit` | agent 用 Edit/Write 写受保护路径 ⇒ 被拒 | **是** |
| `unsandboxed-refused` | `allowUnsandboxedCommands:false` 时 agent 请求 `dangerouslyDisableSandbox` ⇒ 被拒 | **是** |
| `managed-denywrite-path-forms` | denyWrite 中 `~/`、相对路径、glob 写法的实际效果;(v1.5)permissions.deny 中单斜杠开头的绝对路径是否按绝对路径解释(Claude 文档区分 `//` 与 `/`,【静态依据待核】),不按 ⇒ `ManagedForms` 改为 `//` 形式 | **是** |
| `managed-denywrite-anydepth` | (v1.5)托管层按 `org render` 的 T1 模板(含 `**/.ballast`、`**/.claude/settings*.json` 与对应的 `T(**/…)`);agent 用 Bash(`echo >`、`python3 -c`)与 Edit/Write 分别写 ① `<cwd>/a/b/.claude/settings.json`、② `<cwd>/a/.ballast/project.json`、③ 会话在仓库子目录 `<repo>/sub` 启动时写 `<repo>/.claude/settings.json` 与 `<repo>/.ballast/project.json` ⇒ 全部被拒才 `pass`;任何一项写入成功 ⇒ `fail`,observation 写清是哪种写法、哪个锚点失效。结果按 §3.2 条件契约决定 `weakened_project` 的缺省值。§6.6 模拟模型可作佐证(`mode:"mock"`),但只有 `credentials` 模式的 pass 才能把缺省值改为 `audit` | **是** |
| `mcp-write` | 白名单内一个会写文件的 MCP server 写受保护路径 ⇒ 成功(记录残余风险);白名单外的 server 不可用 | **是** |

合计:**无凭据 15 个**(`nocred-events` 至 `lower-sandbox-merge`),**需凭据 6 个**(v1.5 增加 `managed-denywrite-anydepth`)。`nocred-events` 不通过时,依赖事件触发的无凭据用例(除 `runner-bwrap-in-docker`、`mcp-allowlist-nocred`、`managed-file-owner` 中不依赖事件的部分外)全部降为 `needs_credentials`,其 `recheck` 是同一条补测命令加上这些 case 名。

### 6.5 产物格式与检查

`m0b-cases.json` 是数组,元素字段与 `cases.json` 对齐:`case_id`、`events`、`schema_keys`、`assertion`、`result`、`observation`、`evidence`,另加:

- `result` 取值扩展为 `pass | fail | unverifiable | needs_credentials`(`fail` 表示文档说法被推翻,是有效发现,不是脚本错误);
- `mode`:`none | blackhole | hang | credentials | mock`;
- `claude_version`;
- `recheck`:`needs_credentials` 时必填,是一条可直接运行的补测命令。

`M0b-CONCLUSIONS.md` 按 M0a 的结构:对 A13/A14/A15/A16/A20 的逐条结论表、对设计的影响(特别是 `tier` 条件 5 的合并顺序、条件 6 白名单可以放宽哪些键、`org render` 的路径写法、v1.5 条件契约下 `weakened_project` 的缺省值)、仍未核验清单。

检查器 `scripts/probe-m0b/check/`(独立 `main` 包,不在 archtest 范围内):每个必需 case 都在,字段齐全,`needs_credentials` 必带 `recheck`,payload 目录无本机路径与秘密(调用卫生检查的同一组模式)。

补测(用户明确提供凭据时,一条命令):

```sh
ANTHROPIC_API_KEY=<用户自己提供> scripts/probe-m0b/run.sh --with-credentials --cases sandbox-denywrite-bash,permissions-deny-edit,unsandboxed-refused,managed-denywrite-path-forms,managed-denywrite-anydepth,mcp-write
# lower-sandbox-merge 在无模型手段下观测不到时,另跑:
ANTHROPIC_API_KEY=<用户自己提供> scripts/probe-m0b/run.sh --with-credentials --cases lower-sandbox-merge
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
4. 产物:`docs/facts/hermes-cases.json`(格式同 m0b-cases.json,另加 `client:"hermes"`、`hermes_commit`、`evidence_kind:"offline-hooks-test"`),`docs/facts/payloads/hermes/<case>/`,以及 `docs/facts/HERMES-CONCLUSIONS.md`(结论表 + "待同步文本"一节,§9 交接)。
5. **Verified 的含义**:`hooks test` 证明的是"序列化形状 + 输出解析 + 阻断语义",不证明真实 agent 在什么时机、以什么 kwargs 调用 hook。【决定】可以据此把 Hermes 各行的 `Verified` 改为 true,但 `docs/clients.md` 必须写明"离线核验(hooks test),触发时机未核验";真实会话取证【需凭据】。

**据此修正 adapter/hermes**(契约变更,本文预先批准,前提是第 3 步的取证结果支持):

- `patch` → Edit:replace 模式取 `path`;V4A 补丁模式解析全部目标。【决定】(v1.5)解析器采用**复制**:把 `internal/adapter/codex/patch.go` 的 envelope 解析复制到 `internal/adapter/hermes/patch.go`(文件头注明来源文件与复制时的提交),连同 `internal/adapter/codex/testdata/patches.json` 中适用的用例复制为 hermes 的测试数据;`adapter/codex` 与 `adapter/common` 都不改(`adapter/codex` 不归本单元),两份实现的去重留作 P2 之后的重构。
- `execute_code` 保持原名(规则可以按名字匹配),`no-tamper` 识别不了其中的写入,登记到 trust-model §2.1 第 1 类。
- `pre_verify` → `EventStop`,`deny` 渲染为 `{"decision":"block","reason":R}`,**语义是续跑**(同 Cursor),lint 给 `hermes-stop-continue` 警告;`Capabilities(hermes)` 的 Stop deny 置为支持。只有取证确认字段与输出解析后才落地。
- `Verified` 与 `docs/clients.md` §4.4 同步更新:**交接方式见 §9**——P2-HERMES 不直接改 `docs/clients.md` 与 `docs/trust-model.md`,而是把要同步的文本(clients.md §4.4 Hermes 小节的 Verified 含义与"离线核验,触发时机未核验"说明、trust-model §2.1 第 1 类追加 `execute_code`)写进 `docs/facts/HERMES-CONCLUSIONS.md` 的"待同步文本"一节,由 P2-CLI 在集成时逐字同步。

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
| E2 请求文件与隔离目录 GC | `~/.ballast/requests/` 与 `~/.ballast/quarantine/`(v1.3)超过 30 天的条目由 `ballast gc` 删除;(v1.5)`~/.ballast/work/<job_id>/` 中 job 已结束(或 job 文件不存在)且超过 24 小时的残留一并删除(`runner.GCRequests`,P2-RUNNER 追加导出,P2-CLI 接线) | P2-RUNNER / P2-CLI | runner 单测 + `ops_e2e_test` |
| — cache GC | 已在 v1.4 完成(`config.GCCache`),不再列入 | — | — |

**不做**(记录理由):runner 锁与 state 锁合并为 `sys` 锁原语(动契约包、收益小);SessionEnd 丢弃未收到 SubagentStop 的子 agent 计数(需要先有中断取证);replay 跳过 compact 内部 agent(缺 transcript 侧取证);100KB bash 解析 2.4ms 的优化(满足门槛)。

---

## 9. 单元划分与目录所有权

所有单元在各自 worktree 开发,只依赖本文与 architecture v2.0;修改已导出签名属于契约变更(architecture §12)。"共享文件"一栏列出允许多个单元编辑的文件及各自的**条目边界**——这些文件里不同单元的改动不在同一段落,合并不会冲突。

| 单元 | 独占目录/文件 | 测试包 / 验收命令 | 依赖(after) |
|---|---|---|---|
| **P2-M0B** | `scripts/probe-m0b/**`、`docs/facts/m0b-cases.json`、`docs/facts/M0b-CONCLUSIONS.md`、`docs/facts/payloads/m0b/**` | `bash scripts/probe-m0b/run.sh`;`go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json` | — |
| **P2-REDACT** | `internal/redact/**`、`internal/audit/**` | `go test ./internal/redact ./internal/audit`;60s fuzz | — |
| **P2-TIER** | `internal/tier/**`(含共享 golden `testdata/managed_forms.json`,其他单元只读;`LockState`、`LockedHome`(含时限)、`testdata/locked_home.json`、`ProjectChecker`、`testdata/weakened.json`)、`internal/layout/resolvewith.go` 及其测试 `internal/layout/resolvewith_test.go`、`internal/sys/passwd.go`、`passwd_unix.go`、`passwd_other.go` 及其测试 `internal/sys/passwd_test.go`(`sys` 包其余文件不改) | `go test ./internal/tier ./internal/layout ./internal/sys` | — |
| **P2-SANDBOX** | `internal/sandbox/**`(含 `CacheEnv`、`HostEnv`;v1.5 的 `Spec.Tree` 例外、IPC 默认拒绝的 darwin profile)、`scripts/sandbox-docker.sh`(v1.5,独占;P2-RUNNER 只调用不修改) | `go test ./internal/sandbox`;**`bash scripts/sandbox-docker.sh`(必过,只需 Docker)** | — |
| **P2-PS** | `internal/psparse/**` | `go test ./internal/psparse` | — |
| **P2-HERMES** | `internal/adapter/hermes/**`(v1.5:含从 `adapter/codex/patch.go` 复制来的 `patch.go`;不再触碰 `internal/adapter/common/`)、`internal/dsl/lint.go`(**只追加** `hermes-stop-continue` 警告码及其判定分支,不改既有警告)与 `internal/dsl/lint_hermes_test.go`(新文件)、`scripts/probe-clients/hermes.sh`(**自带辅助函数,不 source `lib.sh`**)、`docs/facts/hermes-cases.json`、`docs/facts/HERMES-CONCLUSIONS.md`(含"待同步文本"一节)、`docs/facts/payloads/hermes/**` | `go test ./internal/adapter/... ./internal/dsl`;`bash scripts/probe-clients/hermes.sh` | — |
| **P2-CLIENTS** | `scripts/probe-clients/{codex,cursor}.sh`、`scripts/probe-clients/lib.sh`(只供 codex/cursor 两个脚本使用)、`docs/facts/codex-static.md` | `bash -n` 语法检查;真实环境检测与拒绝逻辑的脚本自测 | — |
| **P2-RUNNER** | `internal/runner/**`(含 `StartRequest`、`RequestStatus`——`verify --status <request_id>` 的回退与取最差聚合在这里实现;v1.5 的 `tree.go`:`Materialize`、`TreeSEnd`、`CleanupTree`)、`internal/project/project.go` 中 `inputs_untracked` 的解码与校验(只追加 `rawSpec` 字段、校验分支与赋值)及 `internal/project/doc.go` 的格式段、`internal/project/testdata/invalid.json` 追加行 | `go test ./internal/runner ./internal/project`;`bash scripts/sandbox-docker.sh ./internal/runner`(真实后端端到端,必过,只需 Docker) | P2-TIER、P2-SANDBOX |
| **P2-HOOK** | `internal/hook/**`(v1.5:ConfigChange 审计只读 `Event.Source` 与 `Event.ConfigFile`;含消费已冻结的 `Deps.RedirectsIgnored`:会话首个事件写 `env_redirect_ignored`;v1.4 消费 `Deps.IDs`、`Deps.ProjectSandbox`、`Deps.WeakenedProject`:会话首个事件写 `lower_sandbox_weakened`,deny 模式逐 PreToolUse 拒绝)、`internal/model/verifyreq.go` 的函数体与 `internal/model/verifyreq_test.go` | `go test ./internal/hook ./internal/model` | P2-REDACT |
| **P2-REPORT** | `internal/report/**`(diag 名只认 `model.DiagNames`,只读) | `go test ./internal/report` | P2-REDACT |
| **P2-CC** | `internal/adapter/claudecode/**`(v1.5:只填 `fillConfigChange`,不改 `internal/model/event.go`) | `go test ./internal/adapter/...` | P2-M0B |
| **P2-INSTALL** | `internal/install/**`(测试只读 `internal/tier/testdata/managed_forms.json`;`org render --home-root`、`--write` 家目录预检(调用 `sys.LookupHome`)、doctor `tier.locked_home`(调用 `tier.LockedHome`),不另写查询) | `go test ./internal/install`;`bash scripts/install-e2e.sh` | P2-TIER、P2-CC |
| **P2-CLI**(集成) | `internal/cli/**`、`cmd/ballast/**`、`scripts/{boundary-t0.sh,boundary-t1.sh,accept-p2.sh}`、`scripts/boundary/**`、`scripts/bench.sh` 与 `scripts/bench/**`(v1.5 补 `bench.sh`:场景 12–14 与 ConfigChange 场景)、`internal/tier/tier.go` 中 `DefaultWeakenedProject` 常量的定稿(只在 M0b 结论为 pass 时改为 audit,§3.2)、`scripts/lib.sh`、`scripts/README.md`、`.github/workflows/**`(**只能在三平台 CI 修复分支合入集成分支之后修改**,在其结果上追加 P2 步骤)、`README.md`、`docs/trust-model.md`、`docs/clients.md`、`docs/architecture.md` 中 **§7 与 §3.1 的 P2 追加行**(v1.4:`RequestStatus` 找不到时的退出码、`report` 退出码的最终文字、`requests/`/`quarantine/` 行的实现细节;只追加或改 P2 新增的行,其余章节仍归架构师);`cmd_verify` 的 `--status` 回退只调用 `runner.RequestStatus` 并打印;**重定向锁接线**(`tier.LockState` → `layout.Options{IgnoreRedirects, PasswdHome: 对 tier.LockedHome 的闭包}` → 填 `hook.Deps.RedirectsIgnored`),不自己实现任何家目录查询 | `bash scripts/accept-p2.sh`;`go test ./...` | 全部其他单元;workflows 部分另需 CI 修复分支已合入 |

**共享文件的条目边界**:

- `internal/model/event.go`(v1.5):`Event.ConfigFile` 由本设计提交落地;P2-CC 只在 claudecode adapter 里填值,P2-HOOK 只读取;任何 P2 单元都不改 `event.go`。
- `internal/model/evidence.go`、`internal/layout/layout.go`(v1.5):`VerifySpec.InputsUntracked`、`VerifyRecord.Workspace`/`InputsHash`、`layout.WorkDir` 由本设计提交落地,P2-RUNNER 只使用。
- `internal/project/`(v1.5):只有 P2-RUNNER 可以改,且只追加 `inputs_untracked` 的解码、校验与测试数据;`init` 探测等其余部分不改。
- `internal/adapter/common/`(v1.5):P2 中没有单元修改它(Hermes 的补丁解析改为复制进 `adapter/hermes`)。
- `internal/tier/tier.go` 的 `DefaultWeakenedProject`(v1.5):常量由本设计提交按"未证明"冻结为 `deny`;只有 P2-CLI 在集成时依据 M0b pass 行改为 `audit`(同时改 P2-INSTALL 的 `org render` golden),P2-TIER 实现 `EffectiveWeakenedProject` 时不改它。
- `scripts/sandbox-docker.sh`(v1.5):归 P2-SANDBOX;P2-RUNNER 与 P2-CLI(`accept-p2.sh`)只调用。
- `internal/model/client.go`、`internal/model/capability.go`:P2-CC 只改 Claude 表中的 `ConfigChange` 一行;P2-HERMES 只改 `ClientHermes` 的表项与能力集(含新增 `pre_verify` 行)。其他条目任何单元都不得改。
- `scripts/tools/accutil/`:P2 单元**不**修改;M0b 检查器放在自己的目录。
- `internal/dsl/lint.go`:只有 P2-HERMES 可以改,且只追加(见上表);其他 P2 单元不改 `internal/dsl`。
- `internal/layout/`:只有 `resolvewith.go`(及其测试)归 P2-TIER;`layout.go` 本身在 P2 中不改。
- `internal/sys/`:只有 `passwd*.go`(及 `passwd_test.go`)归 P2-TIER;其余文件 P2 中不改。
- 家目录查询的分工:**实现**(`sys.LookupHome`、`tier.LockedHome`)全部归 P2-TIER;**接线**(把闭包放进 `layout.Options`)归 P2-CLI;**诊断与预检**(doctor、`org render`)归 P2-INSTALL,直接调用 `tier.LockedHome` / `sys.LookupHome`,不另写查询。
- `hook.Deps.RedirectsIgnored`:字段已在骨架中冻结;**读取并审计**归 P2-HOOK,**填值**归 P2-CLI。v1.4 的 `hook.Deps.IDs`、`ProjectSandbox`、`WeakenedProject` 同此分工;`ProjectSandbox` 的实现(`tier.ProjectChecker`)归 P2-TIER。
- `internal/model/audit.go` 的 `Diag*` 常量与 `DiagNames`(v1.4):由本设计提交落地;P2-HOOK、P2-RUNNER 发 diag 时只用这些常量,P2-REPORT 只读;需要新名字时走契约变更,不由实现单元自行追加。
- `docs/architecture.md`(v1.4):§7 与 §3.1 中 P2 追加的行归 P2-CLI(集成时把 `RequestStatus` 的未找到退出码等写实);其余章节与修订记录仍归架构师,P2 单元不改。
- `scripts/probe-clients/`:`hermes.sh` 归 P2-HERMES,其余归 P2-CLIENTS;两者没有共享文件,可同批开工。
- **P2-HERMES → P2-CLI 的文档交接**(v1.3):`docs/clients.md`、`docs/trust-model.md` 仍由 P2-CLI 独占。P2-HERMES 需要改的文本(clients.md §4.4 Hermes 小节:各行 Verified 的含义、"离线核验(hooks test),触发时机未核验";trust-model §2.1 第 1 类追加 `execute_code`)写进 `docs/facts/HERMES-CONCLUSIONS.md` 的"待同步文本"一节,每段注明目标文件与小节;P2-CLI 集成时逐字同步,并在 `accept-p2.sh` 的卫生检查里断言 clients.md 含"触发时机未核验"。两个单元因此不会争同一文件,也不会漏改。
- `internal/layout/layout.go`:v1.3 在设计提交中追加了 `QuarantineDir` 字段(与 `RequestsDir` 同样由本提交落地),实现单元不再改 `layout.go`。

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

第一批可以同时开工的:P2-M0B、P2-REDACT、P2-TIER、P2-SANDBOX、P2-PS、P2-HERMES、P2-CLIENTS。(v1.5 依赖不变:`Event.ConfigFile`、`ManagedForms` 的 `**/` 形式与 `DefaultWeakenedProject` 都已在设计提交冻结,P2-HOOK、P2-TIER、P2-INSTALL 无需等待别的单元来定这些接口。)

---

## 10. 集成验收:`scripts/accept-p2.sh`

由 P2-CLI 编写,参数固定,任何断言失败即非 0;三平台 CI 执行(容器与真实客户端部分在缺少前提时显式 SKIP,`CI=true` 加对应 `BALLAST_REQUIRE_*` 时失败):

1. `go build ./...`、`go vet ./...`、`GOOS=windows go vet ./...`、`go test ./internal/archtest`、`go test ./...`。
2. `go test ./internal/redact -run '^$' -fuzz FuzzString -fuzztime 60s`。
3. `ballast report --out` 的 e2e(全部退出码,输出文件过 `Validate`)。
4. `scripts/boundary-t0.sh`。
5. `scripts/sandbox-docker.sh ./internal/sandbox ./internal/runner`(v1.5,需要 Docker;Linux CI 必跑)与 `scripts/boundary-t1.sh`(需要 Docker;Linux CI 必跑)。
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
| macOS 非文件逃逸通道(LaunchServices、AppleEvents、`launchctl submit`、cfprefsd 代写偏好、WindowServer 注入、剪贴板)能否被 v1.5 的 IPC 默认拒绝关闭 | 片段可被 sandbox-exec 接受、工具链可用、`launchctl` 副本提交被拒已在设计期实测;cfprefsd 是否尊重 `user-preference-write`、ctypes 通道未核验 | P2-SANDBOX 集成测试(命名偏好域,CI 必跑);关不住就改为 `(deny default)` 加白名单;cfprefsd 不尊重时把两个 cfprefsd 名字移出白名单 |
| `DarwinMachAllow` 是否够 go/node/python/java/dotnet 的测试使用、是否与 sandbox-runtime 的白名单一致 | 【静态依据待核】;设计期在本机验证了 go、node、python 的基本用法 | P2-SANDBOX 对照源码并按运行时各跑一个最小工程,缺的服务登记依据后追加 |
| macOS 上文件读、sysctl、IOKit、POSIX/SysV IPC 仍在 `(allow default)` 之下 | 残余面(已登记,v1.5) | 不属于审核点名的执行或注入通道;P2-SANDBOX 集成测试若发现可利用通道,改为 `(deny default)` |
| macOS loopback/allow 策略下测试可在通配地址监听并接受局域网入站 | **残余风险(设计期实测,v1.5)** | deny 策略不受影响;`org render` 注释、trust-model §2.3 |
| `RepoMandatoryDeny` 与 Claude sandbox-runtime "危险文件"清单是否一致 | 【静态依据待核】 | P2-SANDBOX 对照源码,只能追加 |
| Linux 上自定义位置的 unix socket(家目录下的 docker.sock 等)仍可从沙箱内连接 | 残余风险(已登记) | 部署指南提示管理员;任何网络策略下都只屏蔽 `/run`、`/var/run` 与四个环境变量(v1.4 起与网络策略无关) |
| **T1 证据的全机前提**:任何一个 Claude 会话的项目层(`<CLAUDE_PROJECT_DIR>/.claude/settings*.json`)削弱了沙箱,该会话就能绕过 runner 直接写全局 `~/.ballast/evidence`,tier、sandbox、origin 任意填写;runner 的降级只影响诚实记录 | **残余风险(已登记,v1.4)** | 会话首个事件审计 `lower_sandbox_weakened`;org.json `weakened_project:"deny"` 拒绝这类会话的全部工具调用;doctor `tier.repo_sandbox_weakened`;trust-model §2.3、§4 写明部署要求;在 Claude 启动时、工具调用之外执行的项目层设置(若生效)挡不住 |
| 项目层顶层键 `env`、`apiKeyHelper`、`statusLine`、`awsAuthRefresh`、`awsCredentialExport`、`otelHeadersHelper`、`enabledPlugins`、`enableAllProjectMcpServers` 是否在项目层生效、是否受工作区信任确认约束;在子目录或父目录启动时加载哪个目录的项目层 | 未核验(按"会生效"保守处理:`LayerRepo` 一律报告) | P2-M0B `lower-sandbox-merge` 追加观测(§6.4) |
| 托管 hook 进程自身继承 Claude Code 的环境:项目层 `env` 中的 `DYLD_INSERT_LIBRARIES`(未启用 hardened runtime 的 darwin 构建)或 `LD_PRELOAD`(动态链接的 Linux 构建,例如自建 cgo 构建)会被加载 | 残余风险(已登记,v1.4) | darwin 发布构建 `codesign -o runtime`,doctor `tier.binary_hardening`(T1 主机 fail);Linux 发布构建静态链接;cgo 构建落在全机前提里 |
| `runner_network:"loopback"`:macOS 上的测试可以连接主机回环上的任何服务(IDE 扩展服务、开发代理、容器守护进程的 TCP 端口) | **残余风险(已登记,管理员显式选择,v1.4)** | 默认 deny 不放行回环;`org render` 注释、trust-model §2.3 |
| `runner_network:"allow"`:Linux 上共享主机网络命名空间,主机的抽象命名空间 unix socket 仍可达 | **残余风险(已登记,管理员显式选择,v1.4)** | 文件形式的 socket 照常屏蔽;`org render` 注释、trust-model §2.3 |
| macOS mach-lookup 拒绝能否脱离 process-exec 规则单独关闭逃逸通道 | 未核验 | P2-SANDBOX 集成测试(副本与 ctypes 调用,去掉 process-exec 规则的 profile 变体);不成立则改为 `(deny default)` |
| 已锁定主机上 passwd 查询阻塞(未缓存的目录账户、阻塞的 NSS) | 已处理(v1.4) | `tier.LockedHome` 自带 50ms 时限,超时按 B7 放行;doctor `tier.locked_home` 计时 |
| macOS 上 passwd 查询家目录的延迟(已锁定主机每次 hook 调用一次) | 本地账户已实测(冷进程 p50 ≈ 0.9ms、p95 ≈ 1.3ms,`CGO_ENABLED=0`);目录账户(AD/LDAP 绑定、未缓存)【未核验】 | P2-TIER `BenchmarkLockedHome` + P2-CLI bench 场景 `13`;超出 B10 预算时另议(契约变更) |
| Linux 静态构建对 sssd/LDAP 用户查不到 passwd | 已知(标准库行为) | 由 `home_root` 规则(推荐)或 cgo 构建解决;doctor `tier.locked_home`、`org render --write` 预检、boundary-t1 `t1-passwd-missing` 把关 |
| cgo/NSS 构建下 passwd 查询是否联网或阻塞 | 已知风险(取决于 NSS 配置) | 只在 NSS 由本地缓存(sssd 本地缓存、nscd)提供时可用;`nss_ldap` 或未缓存的 sssd 违反热路径不联网;doctor `tier.locked_home` 在 cgo 构建下提示;推荐顺序 `home_root` 优先 |
| 低层 `sandbox.*` 数组键与削弱型布尔键是否并入托管沙箱;`permissions.additionalDirectories` 是否受 `allowManagedPermissionRulesOnly` 约束;`~/.claude.json` 能否承载 `sandbox` 键 | 未核验(按"会合并"保守处理:条件 6 + runner 仓库层检查) | P2-M0B `lower-sandbox-merge`(无模型手段观测不到时【需凭据】);若 `~/.claude.json` 能承载,`Detect` 追加检查(契约变更) |
| TTY 注入:新会话 + `/dev/null` stdin + 管道输出能否挡住 `/dev/tty` 与 TIOCSTI | macOS 26 设计期已实测(同会话可注入;新会话后 ENXIO/ENOTTY);Linux 为【静态依据】(TIOCSTI 要求控制终端;bwrap `--new-session`) | P2-SANDBOX、P2-RUNNER 集成测试;boundary-t1 `t1-tty` |
| 任意深度的 git 元数据拒写 | macOS 设计期已实测(SBPL regex 拒绝嵌套 `.git` 新建与改写,`git add` 不受影响);Linux 的 `Baseline` ro-bind + `PostCheck` 隔离未实测 | P2-SANDBOX 集成测试;boundary-t1 `t1-runner-sandbox` |
| `runner_allow_write` 中的每一项(尤其共享构建缓存) | **残余风险(已登记)** | 等价于把该目录交给 agent 可改写的代码,写入跨 job 持久、不进入内容快照 `C`,可被沙箱外的构建复用;默认为空,runner 用 `sandbox.CacheEnv` 把缓存指向 job 临时目录;trust-model §2.3 同步 |
| go 在只读 `GOMODCACHE` 下能否正常构建与测试 | 未核验 | P2-SANDBOX 集成测试 |
| macOS `sandbox-exec` 的长期可用性(Apple 已标记弃用) | 风险 | P2-SANDBOX 探测失败即按 §3.4 退化 |
| Hermes 协议(载荷、输出解析、阻断语义) | 【静态依据】 | P2-HERMES 离线 `hooks test` |
| Hermes 真实会话中的触发时机 | 【需凭据】 | 用户自行补测 |
| Codex、Cursor 全部单元格 | 【需凭据】 | §7.2、§7.3 的补测命令 |
| boundary-t1 中 agent 实际尝试的各项 | 【需凭据】 | `scripts/boundary-t1.sh --with-credentials` |
| 未跟踪/被忽略文件与 A-B-A 伪造 T1 证据(第 5 轮审核) | **已处理(v1.5)**:origin=hook 在私有副本里执行 | P2-RUNNER 单测与 `scripts/sandbox-docker.sh ./internal/runner`;boundary-t1 `t1-runner-sandbox` 的伪造用例 |
| `inputs_untracked` 白名单输入可被 agent 改写,且不在 `S`/`C` 中 | **残余风险(已登记,v1.5)** | 默认空;内容哈希记入 `inputs_hash`,可审计、可与 CI 对照;每一项都是项目显式接受的风险 |
| 测试读取副本之外的文件(文件读允许;已跟踪的符号链接指向仓库外) | 残余风险(已登记,v1.5) | 依赖本身是已跟踪内容、进入提交;与"改测试代码让它恰好通过"同类,由 CI 与评审把关 |
| origin=cli(人在 T1 主机上的 `--foreground`、T0)仍在真实工作树执行:未跟踪文件与并发修改可影响结果;其 `W` 含仓库与 common dir,比会话在子目录或 linked worktree 时 Claude 沙箱的可写范围大 | 残余风险(已登记,v1.5 维持现状) | 人主动运行、人可见;需要更强保证时让 hook 代起 |
| 含子模块的仓库 | 已知限制(v1.5) | origin=hook 记 infra `tree_has_submodule`,不执行;递归实体化留作以后的契约追加 |
| 实体化与快照 fallback 会在沙箱外运行人配置的 git filter 驱动(如 git-lfs),处理的是 agent 可控的内容 | 残余风险(已登记,v1.5;快照 fallback 原本就如此) | 驱动只来自 agent 写不了的配置文件(仓库 config 在 runner 沙箱与 Claude 沙箱的危险文件中【静态依据待核】、全局配置在家目录);`core.fsmonitor=false` |
| 实体化的性能代价(全树检出、白名单复制、冷构建缓存) | 已登记,不设门槛(v1.5) | runner 不在热路径;hook 的 30ms 门槛不变 |
| 需要 git 元数据的测试在副本里失败(副本无 `.git`) | 已知限制(v1.5) | 可见的失败;私有 git dir 加 alternates 留作以后的契约追加 |
| macOS 上用 `setsid` 脱离进程组的沙箱后代在收尾后继续运行 | 残余风险(已登记,v1.5) | 仍在沙箱内,只能写 `W`(origin=hook 时是随后删除的副本、临时目录与 `runner_allow_write`) |
| `ManagedForms` 的 `**/` 任意深度形式在托管 denyWrite 与 permissions.deny 中是否生效、锚点是什么 | 未核验【需凭据】 | M0b `managed-denywrite-anydepth`;未证明前 T1 下 `weakened_project` 缺省 `deny`(§3.2 条件契约) |
| permissions.deny 中单斜杠开头的路径是否按绝对路径解释(Claude 文档区分 `//`) | 【静态依据待核】【需凭据】 | M0b `managed-denywrite-path-forms`;不按 ⇒ `ManagedForms` 改 `//` 形式 |
| macOS 目录账户的 passwd 查询经 opendirectoryd 可能联网或阻塞 | 已知风险(v1.5 登记,与 NSS-LDAP 同类) | 50ms 时限兜底;部署选项 ④;doctor `tier.locked_home` 在 darwin 上慢查询时提示 |

## 12. 风险

1. **无凭据事件不触发**:若 `nocred-events` 失败,M0b 大部分用例变为 needs_credentials,T1 的托管行为仍只有文档依据;层级判定的条件 5 照文档实现,并在 doctor 中标注"托管行为未实测"。
2. **sandbox-exec 弃用**:macOS 未来版本移除时,macOS 上 hook 代起的 verify 全部变 infra(origin=hook 必须沙箱),人工 verify 只能走 `prefer`/`off`,T1 证据只能在 Linux 上产生;届时评估复用 Claude 的 sandbox-runtime。
3. **bwrap 与 user namespace**:部分发行版或容器默认禁用;此时 hook 代起的 verify 全部变 infra(与 `runner_sandbox` 无关),doctor 会给出 fail 与修复提示。
4. **Linux 上 `W` 内不存在的受保护路径**只能事后处理(§3.4):v1.3 起 `PostCheck` 递归扫描任意深度的新匹配项,发现即隔离到 `~/.ballast/quarantine/<job>/` 并记 infra,所以"创建后留在磁盘上、之后生效"(`.claude/settings.local.json`、`.vscode/tasks.json`、`.mcp.json`、嵌套 `.git/config`)不再成立。仍然成立的边界:测试命令在运行中创建、并在同一 job 内被沙箱外的进程读取后又删除(例如人恰好在 job 运行期间在同一仓库执行 `git status`),检测不到;隔离本身失败时只能记 `protected_path_residue` 并审计。曾考虑"包装前为目录类条目预建空目录再 ro-bind",因为会在用户仓库里留下空的 `.vscode/`、`.claude/` 等目录而放弃。
5. **ConfigChange 只审计**:它不能防止篡改,也不覆盖托管刷新(A16);T0 下仍以 `no-tamper` 为主信号。
6. **Hermes `pre_verify` 映射**会改变能力矩阵,只有取证确认后才落地。
7. **默认拒写会让部分测试失败**(写全局包目录、未被 `CacheEnv` 覆盖的缓存位置):这是可见的 `fail`,不是安全问题。首选修复是把该工具的缓存变量追加进 `sandbox.CacheVars`(契约只追加);`runner_allow_write` 是最后手段,每一项都是残余风险(§11)。冷缓存会让沙箱内的测试变慢,这是有意的代价。
8. **重定向锁改变已锁定主机上的行为**:在声明 T1 的主机上无法再用 `BALLAST_HOME` 等变量做本地实验;这是有意的,实验应在未部署 org.json 的机器或容器里进行。
9. **已锁定主机上家目录查不到**(企业 sssd/LDAP + 静态构建):该用户的 hook 全部放行。缓解:`home_root`、doctor fail、`org render --write` 预检拒写、boundary-t1 覆盖;部署指南(trust-model §4,由 P2-CLI 更新)把 `home_root` 列为企业机群的默认步骤。
10. **条件 6 的保守判定会误报**:用户或仓库为了正常开发写了 `excludedCommands`、`allowedDomains` 等,该主机或该仓库就只能是 T0;这是有意的(这些键确实能让 agent 绕过沙箱写证据),M0b `lower-sandbox-merge` 证明某键被托管层覆盖后再放宽。doctor 会点名具体文件与键。v1.4 起项目层的 `env`、helper 键与未知顶层键也算削弱,仓库层误报更多;用户层只看沙箱相关键,`statusLine` 之类不受影响。
12. **`weakened_project:"deny"` 的可用性**:开启后,任何带削弱设置的项目里 Claude 都不能调用工具(包括只带 `env` 的常见配置)。v1.4 时这是管理员的显式选择;v1.5 起在 M0b 证明 `**/` 形式有效之前它是缺省值(见风险 15);doctor 在仓库内会提前点名。
13. **macOS 上 deny 不放行回环**:依赖本地监听端口的测试(HTTP 服务器测试等)在 macOS 的沙箱化 verify 中会可见地失败;Linux 不受影响。管理员可以接受残余风险改用 `loopback`。
14. **实体化的成本**(v1.5):每个 hook 代起的 job 多一次全树检出与白名单复制,副本里没有上次构建的产物;大仓库与大 `node_modules` 的 job 启动可达数秒到数十秒。登记,不设门槛;hook 的 30ms 门槛不受影响。
15. **`weakened_project` 缺省可能是 `deny`**(v1.5 条件契约):M0b 证明 `**/` 形式有效之前,T1 主机上任何带削弱设置的项目(包括只带 `env` 的常见配置)里 Claude 都不能调用工具。这是"未证明有效时取保守一侧"的有意结果;管理员可以在 org.json 显式写 `audit` 并接受全机前提的风险。
16. **副本里的测试行为差异**(v1.5):没有 `.git`、没有未跟踪的生成文件与依赖(除非列进 `inputs_untracked`),部分测试会可见地失败;首选修复是把构建输入列进白名单或改为已跟踪。
11. **任意深度扫描的成本**:Linux 上每个 job 包装前和结束后各完整遍历一次工作树与 git dir(含 `node_modules` 等被忽略的目录);runner 不在热路径,代价是 job 启动与收尾变慢,大仓库可达秒级。匹配项超过 4096 个时 job 直接 infra(可见),不静默降级。

## 13. 修订记录

### v1.5(2026-10-16,第 5 轮独立审核;处理方向由项目负责人裁决)

| 审核项 | 级别 | 处理 | 涉及 |
|---|---|---|---|
| T1 证据可被伪造:runner 在 agent 可同时改写的真实工作树上跑测试,`S`/`C` 只看已跟踪文件,未跟踪的 `zz_test.go`/`conftest.py`(可藏进 `.git/info/exclude`)或 A-B-A 换入换出都能得到有效的 T1 pass;"runner 只写它亲自观察到的结果""提交里能看到测试被改"不成立 | major | 采用技术方案 (a):origin=hook 的 job 把 `S_start` 的树实体化到 `~/.ballast/work/<job_id>/tree`(冻结方法:`GIT_INDEX_FILE` 临时索引 + `read-tree` + `checkout-index --prefix`,`S_start` 与副本出自同一次 `git add -u` 读取,算法与快照 fallback 相同),测试在副本里跑,`W` = 副本 + 临时目录 + `runner_allow_write`,`S_end` 在副本上重算(发现自改);未跟踪/被忽略的构建输入只经 project.json `verify[].inputs_untracked`(默认空)复制,内容摘要记入 `VerifyRecord.InputsHash`;被忽略文件默认不复制;含子模块 ⇒ infra `tree_has_submodule`;副本无 `.git`,`GIT_CEILING_DIRECTORIES` 限住;origin=cli 维持现状。新增 §3.3 前提 4,攻击面表、§11、trust-model §2.3/§3 同步;白名单输入可被改写、测试可读副本外文件、origin=cli 不受保护、性能代价均登记 | §2.3、§3.3、§3.4、§3.6、§5.3、§8、§9、§11、§12、`internal/runner/tree.go`、`internal/runner/t1.go`、`internal/sandbox/sandbox.go`、`internal/model/evidence.go`、`internal/layout/layout.go` |
| darwin profile 以 `(allow default)` 开头,除文件写与网络外 IPC 全放行:cfprefsd 代写偏好(改终端启动命令)、WindowServer 注入按键、`job-creation` 走 bootstrap port;验收只覆盖三条通道;违背"不弱于 Claude 沙箱" | major | IPC 冻结为默认拒绝:`(deny mach-lookup)` 后接 `sandbox.DarwinMachAllow` 最小白名单(来源与待实测写明;`DarwinMachForbidden` 永不进入),显式 `(deny mach-register)`、`(deny job-creation)`、`(deny user-preference-write)`、`(deny lsopen)`、`(deny appleevent-send)`、信号只限同沙箱;网络加 `(deny network-bind)`、`(deny network-inbound)`(loopback/allow 只放行 localhost);文件读仍允许。设计期实测:片段被接受、go/node/python 基本用法可用、`launchctl` 副本提交被拒、绝对路径偏好域区分不了 cfprefsd 代写(集成用例因此用命名域,CI 必跑)、loopback 下通配监听与局域网入站挡不住(登记)。golden 与集成用例追加 `defaults write`、CGEventPost/剪贴板、`launchctl submit` | §3.3、§3.4、§3.6、§11、`internal/sandbox/sandbox.go` |
| 托管 denyWrite / permissions.deny 只有 `./` 字面形式,agent 可在子目录或(子目录会话时)仓库根埋下削弱的项目层并提交扩散,默认 `audit` 下后续会话仍能绕过 runner;`CLAUDE_CONFIG_DIR` 重定向时用户层不受保护;"会话内本来就拒写"表述不实 | major | `ManagedForms` 与共享 golden 增加 `**/.ballast`、`**/.claude/settings*.json`(权限规则 `T(**/…)`);M0b 新增 `managed-denywrite-anydepth`(需凭据);**条件契约** `tier.DefaultWeakenedProject`:pass ⇒ 缺省 `audit`,fail/未证明 ⇒ 缺省 `deny`(`org render` 同步,骨架按未证明取 `deny`,P2-CLI 集成时定稿);`tier.EffectiveWeakenedProject` 由 CLI 填入 hook;`Detect`:`CLAUDE_CONFIG_DIR` 已设置且不等于 `<home>/.claude` ⇒ `sandbox_deny_write`(沿用既有 id);更正 §3.2、§3.3 与 trust-model 的表述;另登记 permissions 规则 `//` 与 `/` 的路径锚点问题【静态依据待核】 | §3.2、§3.3、§3.5、§3.6、§5.3、§6.4、§11、§12、`internal/tier/tier.go`、`internal/tier/testdata/managed_forms.json`、`internal/model/tier.go`、`internal/hook/hook.go` |
| `model.Event` 没有承载 ConfigChange 文件路径的字段,P2-HOOK 与 P2-CC 会各自往 `event.go` 加字段 | major | 冻结 `model.Event.ConfigFile string`(本提交落地),`source` 复用 `Event.Source`;claudecode adapter 落地映射占位 `configChangeFileKey`/`fillConfigChange`(no-op);§9 写明 P2-CC 只填值、P2-HOOK 只读取,两者都不改 `event.go` | §2.3、§5.1、§9、`internal/model/event.go`、`internal/adapter/claudecode/adapter.go` |
| P2-SANDBOX 的 Linux 半边在本单元没有可执行环境,`Baseline`/`PostCheck`/`Quarantine` 可能直到 boundary-t1 才第一次运行 | major | P2-SANDBOX 新增独占脚本 `scripts/sandbox-docker.sh`(Debian 12 容器、三个 `--security-opt`、`--network none`、普通用户、`CI=true BALLAST_REQUIRE_BWRAP=1 go test ./internal/sandbox`),列为必过验收(只需 Docker);P2-RUNNER 的真实后端端到端复用它;accept-p2 调用 | §3.6、§9、§10 |
| (a) runner 的 `W` 比 Claude 沙箱大(含 common dir);(b) PostCheck/Quarantine 与 S_end 的顺序未冻结;(c) darwin 目录账户的 passwd 查询可能联网;(d) `scripts/bench.sh` 无主、Hermes 无法"复用" Codex 解析器;(e) darwin 不限 network-bind 与入站 | minor | (a) 由第 1 条收窄:origin=hook 的 `W` 只有副本,common dir 不再可写;origin=cli 登记;(b) 冻结收尾顺序:退出 →(darwin)killpg → `PostCheck` → `Quarantine` → `S_end` → 记录 → 审计 → 清理副本;(c) 与 NSS-LDAP 同列为部署选项 ④,doctor 在 darwin 慢查询时提示;(d) `scripts/bench.sh` 归 P2-CLI,Hermes 把 `adapter/codex/patch.go` 复制为 `adapter/hermes/patch.go`,不再触碰 `adapter/common`;(e) 由第 2 条覆盖 | §3.2、§3.4、§3.5、§7.1、§9、§11 |

### v1.4(2026-10-16,第 4 轮独立审核)

| 审核项 | 级别 | 处理 | 涉及 |
|---|---|---|---|
| 项目层削弱只让 runner 给自己的记录降级,挡不住削弱沙箱的会话绕过 runner 直接写全局证据;检查的是 job 仓库而 Claude 加载的是 `CLAUDE_PROJECT_DIR`;§3.3 "T1 主机上的 T0 记录要么来自人要么来自削弱的仓库"不成立 | major | (1) 新增 `ports.ProjectSandboxChecker`(实现 `tier.ProjectChecker`)与 `hook.Deps.ProjectSandbox`:hook 在会话首个事件对 `$CLAUDE_PROJECT_DIR`(未设置时为 cwd 与仓库顶层)检查,非空审计 `lower_sandbox_weakened`;CLI 只在已锁定主机接线;**不写会话状态**(说明见 §3.3,理由:`state.Record` 是已实现的 v1 格式,标记唯一的消费者是 deny 开关,逐事件重查更简单)。(2) 撤回原表述,在 §3.3 前提 3、§11、trust-model §2.3 写明 T1 证据保证是全机范围的,前提是没有任何会话的项目层削弱沙箱,runner 的降级只影响诚实记录。(3) 采纳可选项:org.json `weakened_project`(`audit` 默认 / `deny`),deny 时该会话的每个 PreToolUse 都被拒(覆盖全部工具,不只 Bash),审计 `lower_sandbox_deny`。(4) `HookStartRequest.ProjectDir` 与 `--project-dir`,runner 同时检查 job 仓库与项目目录;doctor 同时查当前目录与仓库顶层;boundary-t1 `t1-lower-sandbox` 追加 ⑤–⑦;M0b 追加子目录/父目录启动时加载哪个项目层 | §3.2、§3.3、§3.4、§3.5、§3.6、§5.3、§6.4、§11、§12、`internal/ports/ports.go`、`internal/hook/hook.go`、`internal/model/verifyreq.go`、`internal/model/tier.go`、`internal/tier/tier.go`、`internal/runner/t1.go`、`docs/trust-model.md` |
| `WeakenedSandboxKeys` 顶层只看 `permissions.additionalDirectories`,`env` 与各类 helper 命令键可让命令在沙箱外执行或改变托管 hook/runner 的环境;runner 在沙箱外调用 git 时原样继承 | major | 顶层改为白名单,并按层区分(`SettingsLayer`,`WeakenedSandboxKeys(doc, layer)` 改签名):用户层(agent 不可写、人编写)只把让 agent 控制的命令离开沙箱的键算削弱,项目层与本地层把 `env`、helper 键、插件与 MCP 自动启用以及所有未知顶层键都算削弱;`permissions` 内部也改为白名单。新增 `sandbox.HostEnv`:runner 交给快照的环境去掉 `GIT_*`、`LD_*`、`DYLD_*` 与 shell 启动变量;Ballast 子进程与沙箱后端去掉 `LD_*`/`DYLD_*`,测试需要的 `LD_*` 经 `Spec.LoaderEnv` 在 bwrap 内重设;darwin 发布构建要求 hardened runtime(doctor `tier.binary_hardening`);M0b `lower-sandbox-merge` 记录这些键在各层是否生效,只按实测放宽 | §3.2、§3.3、§3.4、§3.5、§3.6、§6.4、§11、§12、`internal/tier/tier.go`、`internal/sandbox/sandbox.go`、`internal/runner/t1.go` |
| macOS deny 策略放行主机回环任意端口,沙箱内测试可访问沙箱外的本地服务,弱于 Claude 自己的沙箱;逃逸通道的验收只用标准路径,证明不了 mach-lookup 生效 | major | deny 在 macOS 上不放行任何出站 IP(含回环);新增 `runner_network:"loopback"`(`model.NetworkLoopback`)作为管理员显式选项并登记为残余风险(§11、trust-model、`org render` 注释);Linux 保持私有回环。`DarwinEscapeDenials` 的集成测试必须用 `TMPDIR` 中的副本与 python3 ctypes 调用、在去掉 process-exec 规则的 profile 变体下单独证明 mach-lookup 拒绝生效 | §3.3、§3.4、§3.6、§11、§12、`internal/model/tier.go`、`internal/tier/tier.go`、`internal/sandbox/sandbox.go` |
| `runner_network:"allow"` 同时撤掉了 `/run`、`/var/run` 屏蔽、`SSH_AUTH_SOCK`/`DOCKER_HOST` 移除与 macOS 的 unix socket 限制,"允许联网"变成"允许逃逸" | major | 本地 socket 隔离与网络策略脱钩、恒定执行:Linux 恒定 `--tmpfs /run --tmpfs /var/run`,恒定移除 `SSH_AUTH_SOCK`、`DOCKER_HOST`、`DBUS_SESSION_BUS_ADDRESS`、`DBUS_SYSTEM_BUS_ADDRESS`;macOS 恒定只允许 `W` 内的 unix socket(allow 另放行 mDNSResponder);allow 只放开出站 IP;Linux allow 下主机抽象 socket 仍可达,登记为残余风险。§3.3 攻击面表、`Wrapped.Env` 注释、P2-SANDBOX golden 与集成用例、boundary-t1 的 allow 变体同步 | §3.3、§3.4、§3.6、§5.3、§11、`internal/model/tier.go`、`internal/sandbox/sandbox.go` |
| 契约缺口与文档漂移:(a) `hook.Deps` 没有 IDs;(b) diag 名没有冻结清单;(c) `docs/architecture.md` 无主,§7/§3.1 的追加没人写,org.json 一行与"sandbox 只允许 runner import"漂移;(d) `LockedHome` 在 watchdog 之外可能阻塞;(e) 条件 4、5 与 `Locked` 不查托管目录父链 | minor | (a) 冻结 `hook.Deps.IDs sys.IDGen`(nil ⇒ 按 spawn 失败处理);(b) 冻结 `model.Diag*` 与 `model.DiagNames`,report 未知名记 `other`(`report.DiagOther`);(c) architecture §7、§3.1 的 P2 追加行归 P2-CLI,org.json 一行补 `runner_network`、`runner_allow_write`、`home_root`、`weakened_project`,§2 改为"只有 runner 与 cli";(d) `HomeOptions.Timeout`、`DefaultLockedHomeTimeout`(50ms)、`ErrLockedHomeTimeout`,超时按 B7 放行,doctor 计时;(e) 条件 4、5 与 `Locked` 增加托管目录父链检查,detect.json、Locked 表与 boundary-t1 `t1-orgjson` 加用例,`BenchmarkLocked` 门槛放宽到 30µs | §3.2、§3.3、§3.6、§4.4、§5.3、§9、`internal/hook/hook.go`、`internal/model/audit.go`、`internal/report/report.go`、`internal/tier/tier.go`、`internal/layout/resolvewith.go`、`docs/architecture.md` |

### v1.3(2026-10-16,第 3 轮独立审核)

| 审核项 | 级别 | 处理 | 涉及 |
|---|---|---|---|
| T1 判定只看托管层 sandbox 键;低层 `excludedCommands`、`allowUnixSockets`、`allowWrite`、`enableWeakerNestedSandbox`、`allowedDomains` 可能跨层并入,项目层随仓库提交不受会话内 denyWrite 约束,agent 可在沙箱外运行 `npm`/`docker` 写证据而 Detect 仍报 T1 | major | 新增条件 6 与 Missing id `lower_sandbox_weakened`;`tier.WeakenedSandboxKeys`(白名单,未分类的键算削弱,另查 `permissions.additionalDirectories`)、`tier.RepoSandboxWeakened`;`Detect` 查用户层(`$CLAUDE_CONFIG_DIR` 与 `<home>/.claude` 都查),runner 自判时再查 job 仓库的项目层与本地层,不为空 ⇒ 证据 T0、审计 `tier_mismatch`;条件 5 追加托管沙箱自身的 `excludedCommands`/`allowUnixSockets` 为空;M0b 新增 `lower-sandbox-merge`(无模型观测手段:`--debug` 日志、TUI 本地命令 `/sandbox`、§6.6 模拟模型,都不行则 needs_credentials 并给补测命令);boundary-t1 新增 `t1-lower-sandbox`(项目层 `excludedCommands` ⇒ T0);trust-model §2.2/§2.3 登记;新鲜度前提 3 写明依赖条件 6 | §3.2、§3.3、§3.4、§3.5、§3.6、§5.3、§6.4、§11、§12、`internal/model/tier.go`、`internal/tier/tier.go`、`docs/trust-model.md` |
| 被沙箱包装的测试仍持有人的控制终端(`Setpgid` 而非 `Setsid`、bwrap 无 `--new-session`、`/dev/tty*` 可写),可用 TIOCSTI 注入按键逃逸 | major | 冻结:被包装的子进程新建会话(`Setsid`;bwrap `--new-session`;`Wrapped.NewSession`),stdin `/dev/null`,输出只经文件或管道,前台模式也只经管道;设备集只留 `/dev/null`、`/dev/zero`、`/dev/fd/*`;本机 macOS 26 设计期实测:同会话下 `/dev/tty` 打开与 TIOCSTI 均成功(逃逸成立),新会话后 ENXIO/ENOTTY;集成用例与 boundary-t1(`t1-tty`)新增"打开 `/dev/tty` 失败""对继承 fd 调 TIOCSTI 失败" | §3.3、§3.4"终端"、§3.6、§5.3、§11、`internal/sandbox/sandbox.go`、`internal/runner/exec_unix.go`(注释) |
| 强制拒写只覆盖顶层 git 元数据;嵌套 `.git`、`modules/*/config` 与 `modules/*/hooks`、其他 worktree 的 `config.worktree` 可写,测试甚至可自加 gitlink;Linux 上新建的受保护路径只记 infra 而留在磁盘上继续生效 | major | `RepoMandatoryDeny` 改为任意深度并增加 `.git`(条目本身)与 `.git/config.worktree`;`GitDirMandatoryDeny` 增加 `commondir`、`modules/**/…`、`worktrees/*/…`;唯一匹配器 `MatchMandatory`;darwin 用锚定的任意深度 SBPL regex(设计期实测:嵌套 `.git` 新建、gitfile 改写、改名祖先后新建均被拒,`git add` 正常);Linux 包装前 `ScanMandatory` 完整遍历并 ro-bind 已存在的匹配项(`Wrapped.Baseline`,上限 4096,超出即 infra),运行后 `PostCheck` 递归重扫,新匹配项由 `Quarantine` 移到 `~/.ballast/quarantine/<job>/`(`layout.QuarantineDir`)并记 infra、审计 `sandbox_quarantine`;"预建空目录再 ro-bind"因污染用户仓库而放弃;集成用例与 boundary-t1 新增 submodule `.git/config`、`modules/x/hooks`、新建 `.claude/settings.local.json` 之后宿主无残留;§12 风险 4 改写,新增风险 10、11 | §3.3、§3.4、§3.6、§5.3、§8、§11、§12、`internal/sandbox/sandbox.go`、`internal/layout/layout.go` |
| git dir / common dir 由 agent 可写的 gitfile 与 `commondir` 推出,W 可被扩到工作树之外 | minor | 新增 `sandbox.ResolveGitDirs`,只接受"工作树之内"与"标准 worktree 且 `gitdir` 回指当前工作树";`ValidateSpec(fsys, spec)` 改签名并重查;不满足 ⇒ infra `sandbox_spec_invalid`;`testdata/gitdirs.json` golden 与集成用例 | §3.4、§3.6、`internal/sandbox/sandbox.go` |
| P2-HERMES 需改 clients.md §4.4 与 trust-model §2.1,但两文件归 P2-CLI | minor | 交接:P2-HERMES 把待同步文本写进 `docs/facts/HERMES-CONCLUSIONS.md` 的"待同步文本"一节,P2-CLI 集成时逐字同步并在 accept-p2 中断言 | §7.1、§9 |
| cgo/NSS 构建在 nss_ldap 或未缓存 sssd 下可能联网或阻塞,违反热路径禁区 | minor | 部署选项 ② 限定为 NSS 由本地缓存提供;doctor `tier.locked_home` 在 cgo 构建下提示;推荐顺序 `home_root` 优先 | §3.2、§3.5、§11、`internal/sys/passwd.go`(注释) |

### v1.2(2026-10-16,第 2 轮独立审核)

| 审核项 | 级别 | 处理 | 涉及 |
|---|---|---|---|
| `PasswdHome` 无主、各平台取法未冻结;Linux 静态构建只读 `/etc/passwd`,sssd/LDAP 用户在已锁定主机上每次 hook 都因 layout 失败而静默放行,T1 在目标环境整体失效;热路径延迟未核验 | major | 新增 `internal/sys/passwd*.go`(`LookupHome`、`Euid`、`ErrNoPasswdEntry`、`ErrPasswdUnsupported`)与 `tier.LockedHome`/`LockState`/`HomeOptions`,全部归 P2-TIER,P2-CLI 只注入闭包,P2-INSTALL 的 doctor 与 `org render` 直接调用;冻结各平台实现(darwin、linux 都用 `user.LookupId`,禁止 `user.Current`,archtest `TestOSUserConfined`);查不到时的退路冻结为 org.json `home_root`(root 所有的父目录、只认直接子目录、属有效 uid、组和其他不可写);doctor `tier.locked_home`、`org render --home-root` 与 `--write` 预检(`SUDO_UID` 查不到且无 `home_root` ⇒ 拒写);boundary-t1 新增 `t1-passwd-missing`;`BenchmarkLockedHome` 与 bench 场景 `13` 覆盖 passwd 查询本身;macOS 本地账户延迟设计期实测 | §2.2、§2.3、§3.2、§3.5、§3.6、§5.3、§9、§11、§12、`internal/sys/passwd*.go`、`internal/tier/tier.go`、`internal/layout/resolvewith.go`、`internal/archtest` |
| hook 拿不到"锁已生效、哪些变量被忽略",P2-HOOK 与 P2-CLI 会各自发明接口 | minor | 冻结 `hook.Deps.RedirectsIgnored []string`(固定顺序、只含非空、nil 表示没有;会话首个事件审计一次,只记名字);§9 写明 P2-HOOK 读取、P2-CLI 填值;P2-HOOK 的 `pipeline.json` 与 P2-CLI 单测增加对应验收 | §3.2、§3.6、§9、`internal/hook/hook.go` |
| `runner_allow_write`(示例为 `~/.cache/go-build`)形成跨边界、可持久、不进快照的通道,却只被描述为便利项 | minor | 登记为残余风险(本文 §11、trust-model §2.3、`tier.OrgConfig` 注释、`org render` 渲染注释);示例改为空列表;新增 `sandbox.CacheVars`/`CacheEnv`,runner 默认把常见缓存变量指向每个 job 的临时目录 | §3.2、§3.4、§3.5、§11、§12、`internal/tier/tier.go`、`internal/sandbox/sandbox.go`、`docs/trust-model.md` |

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
