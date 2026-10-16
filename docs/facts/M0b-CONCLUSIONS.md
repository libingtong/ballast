# M0b 取证结论

- 日期:2026-10-16;Claude Code **2.1.300**(npm `@anthropic-ai/claude-code@2.1.300`,linux-arm64 原生二进制;npm 上同日已有 2.1.301,按设计固定在 2.1.300)。
- 环境:`node:22-bookworm-slim`(digest 固定,Debian 12,bubblewrap 0.8.0),运行在 Docker Desktop 的 Linux 虚拟机(arm64,没有 AppArmor 强制)。用户 `agent`(uid 1000),托管文件由 root 写入、0644。
- 方法:`bash scripts/probe-m0b/run.sh`。每个用例一个全新容器,运行期 `--network none`,环境 `DISABLE_AUTOUPDATER=1 DISABLE_TELEMETRY=1 DISABLE_ERROR_REPORTING=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`;沙箱相关用例加 `--security-opt seccomp/apparmor/systempaths=unconfined`。逐条结果见 `m0b-cases.json`,原始 payload(脱敏、路径规范化为 `<repo>`、`<home>`)在 `payloads/m0b/<case>/`,检查器 `go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json`。
- **没有使用任何凭据**。`dummy-not-a-credential` 只用来让客户端走到"发请求"那一步,不对应任何账户。需凭据的 6 个用例以 §6.6 的模拟模型(`mode:"mock"`,本地桩按脚本发出 tool_use)离线运行,**结果一律保持 `needs_credentials`**,mock 的观察只作佐证,不替代 `credentials` 模式的结论。
- 本机另有的观测限制:全部结论针对 `claude -p`(非交互)和一次交互式 TUI 取样(pty),版本 2.1.300;客户端行为随版本变化,升级后需重跑。

## 先决条件:`nocred-events`(pass)

没有凭据时,三种模式都能触发 `SessionStart` 与 `UserPromptSubmit`,其余无凭据用例因此成立:

| 模式 | 结果 |
|---|---|
| `none` | 在首个模型请求前触发 `SessionStart`、`UserPromptSubmit`,随后客户端以 "Not logged in · Please run /login" 退出(退出码 1,`terminal_reason:"api_error"`),退出时触发 `SessionEnd`(`reason:"other"`);**没有 `Stop`** |
| `blackhole` | 同样触发两个事件;请求被拒绝后客户端持续重试,会话保持存活直到被杀(`SIGTERM` 时触发 `SessionEnd`) |
| `hang` | 同上;本地监听器只 accept 不应答,会话存活,可在会话期间改配置(`configchange-payload`) |

因此没有任何用例因 `nocred-events` 降级为 `needs_credentials`。

## 对 A 区的结论

| 条目 | 结论 | 依据 |
|---|---|---|
| A13 托管路径与合并 | **确认**:`/etc/claude-code/managed-settings.json` 与 `managed-settings.d/*.json` 都被读取。**hook 数组跨文件拼接**(基础文件、`10-a`、`20-b` 的探针全部被调用,启动顺序不固定);**标量按"后者胜"合并,`.d` 覆盖基础文件,`20-b` 覆盖 `10-a`**——包括 `allowManagedHooksOnly` 这类收紧型布尔值:基础文件 `true`、`10-a` 为 `false` 时生效值是 `false`,`true` 不"粘住"。同一个对象键(`env.BALLAST_PROBE_ENV`)在三个文件间冲突时同样后者胜。客户端**不检查托管文件的属主与权限**:属于 `agent`、0666、目录属于 `agent` 时仍然读取并执行其中的 hook | managed-d-merge-hooks, managed-d-merge-scalar, managed-file-owner |
| A14 `allowManagedHooksOnly` 与低层 `disableAllHooks` | **确认**:`allowManagedHooksOnly:true` 时用户层、项目层、本地层的 hook 一个都不被调用,只有托管层;低层(用户/项目/本地任一层)设 `disableAllHooks:true` 后托管 hook 仍被调用。**补充**:一个低层的 `disableAllHooks:true` 会同时关掉**所有**非托管层的 hook(不只是它自己那一层);托管层自己设 `disableAllHooks:true` 则连托管 hook 也全部不执行(对照组) | managed-only-blocks-lower, lower-disableallhooks-vs-managed, managed-disableallhooks |
| A15 hook 跨层合并 | **确认**:托管不设 `allowManagedHooksOnly` 时,托管、用户、项目、本地四层探针对 `SessionStart` 与 `UserPromptSubmit` 全部被调用(合并而非覆盖),同一事件的 hook **并发启动**,调用顺序在多次运行间不固定(观察到 `managed project local user` 与 `managed project user local`)。托管部署时用户层不得再装一份(B12)的前提成立 | hooks-merge-all-layers |
| A16 ConfigChange | **取得 payload**。顶层键:`session_id`、`transcript_path`、`cwd`、`prompt_id`、`hook_event_name`、`source`、`file_path`。`source` 取值:`user_settings`、`project_settings`、`local_settings`、`policy_settings`、`skills`;`file_path` 是被改文件的**绝对路径**(`skills` 时是被改的 `SKILL.md`)。会话存活期间改写用户/项目/本地层 `settings*.json`、用户/项目 skill、托管 `managed-settings.json` 都触发;**新增 `managed-settings.d/50-x.json` 与改写 `~/.claude.json` 不触发**(与"部分托管刷新不触发"一致)。不带 matcher 的条目即可收到全部来源 | configchange-payload |
| A20 沙箱只覆盖 Bash | **确认,`hooks-run-outside-sandbox` 为 pass**:托管层 `sandbox.enabled:true`、`failIfUnavailable:true`、`denyWrite` 含 `<home>/protected` 时,托管 `SessionStart` 与 `UserPromptSubmit` 探针都能向该目录写入(`from-hook` 文件存在),与不设 `sandbox` 段的对照组相同;mock 佐证里同一配置下 Bash 写同一目录被沙箱拒绝(`Read-only file system`)、写会话目录成功,说明沙箱确实在工作而 hook 在沙箱外。**因此 P2-RUNNER、P2-HOOK 不需要暂停,T1 的代起设计与 Ballast 自身写 state/audit 的前提成立。**Write 工具与 MCP server 同样在沙箱外运行(只受 permissions 规则、MCP 白名单约束),见 permissions-deny-edit、mcp-write(均为 mock 佐证) | hooks-run-outside-sandbox |
| `failIfUnavailable` | **确认**:去掉 bwrap 与 socat 后,`sandbox.enabled:true` + `failIfUnavailable:true` 使客户端在启动阶段拒绝运行(退出码 1,没有触发任何事件,stderr:`sandbox required but unavailable: ... sandbox.failIfUnavailable is set`);不设 `failIfUnavailable` 时客户端启动并警告 "Commands will run WITHOUT sandboxing";bwrap 可用时对照组正常启动 | sandbox-failifunavailable |
| MCP 白名单键名 | **核对**:`allowedMcpServers`(条目形状 `{"serverName":"..."}`)、`deniedMcpServers`、`allowManagedMcpServersOnly`。`allowedMcpServers:[...]` 时 `claude mcp list` 只列出白名单内的 server(用户层与项目层 `.mcp.json` 的其他 server 都不再出现);空数组时全部不可用;拼错的键(`allowedMcpServer`)不起作用且不报错 | mcp-allowlist-nocred |

## 逐用例结果

| case_id | result | mode | 要点 |
|---|---|---|---|
| nocred-events | pass | none | 见上节 |
| managed-only-blocks-lower | pass | none | 只有托管层被调用 |
| hooks-merge-all-layers | pass | none | 四层都被调用,并发,顺序不固定 |
| lower-disableallhooks-vs-managed | pass | none | 低层 disableAllHooks 关闭全部非托管 hook,托管仍运行 |
| managed-disableallhooks | pass | none | 托管 disableAllHooks 关闭一切 |
| managed-d-merge-hooks | pass | none | hook 数组跨文件拼接 |
| managed-d-merge-scalar | pass | none | 后者胜,`.d` 覆盖基础文件,收紧型布尔值也不例外 |
| managed-hook-path-forms | pass | none | 见"hook 命令写法" |
| managed-file-owner | pass | none | 客户端不检查属主与权限 |
| sandbox-failifunavailable | pass | none | 启动阶段拒绝运行 |
| mcp-allowlist-nocred | pass | none | 白名单键名核对 |
| configchange-payload | pass | hang | `source` + `file_path` |
| runner-bwrap-in-docker | **fail** | none | §3.4 Linux 行按字面执行不成立,见【契约问题】1 |
| hooks-run-outside-sandbox | pass | none | A20 成立 |
| lower-sandbox-merge | pass | mock | 见"条件 6",部分键标"未观测" |
| sandbox-denywrite-bash | needs_credentials | mock | mock 佐证:Bash 的 `echo >`、`sed -i`、`python3 -c` 写 `~/.ballast` 下全部被拒(`allowWrite` 设为 `<home>` 时 denyWrite 仍是屏障) |
| permissions-deny-edit | needs_credentials | mock | mock 佐证:Edit/Write 写受保护路径全部被拒,对照写入成功 |
| unsandboxed-refused | needs_credentials | mock | mock 佐证:`dangerouslyDisableSandbox:true` 与普通 Bash 都写不进受保护路径 |
| managed-denywrite-path-forms | needs_credentials | mock | mock 佐证:单斜杠绝对路径的 permissions.deny 不生效;`//` 形式生效;Linux 上 denyWrite 的 glob 不生效 |
| managed-denywrite-anydepth | needs_credentials | mock | mock 佐证:`**/` 形式对 Bash 不生效、对工具按会话目录锚定;任意深度形式未证明 |
| mcp-write | needs_credentials | mock | mock 佐证:白名单内 MCP server 能写受保护路径;白名单外不可用 |

## 对设计的影响

### 1. `tier` 条件 5:合并顺序(P2-TIER)

`Detect` 读取托管设置时按客户端的实际语义合并:基础文件 `managed-settings.json` 最先,`managed-settings.d/*.json` 随后**按文件名字典序**;**标量与布尔值后者胜**(`.d` 覆盖基础文件);hook 数组**拼接**;对象按键合并。收紧型键(`allowManagedHooksOnly`、`allowManagedPermissionRulesOnly`)**没有"true 粘住"的语义**:任何后到的文件都能把它们改成 `false`,所以条件 5 必须对**合并后的结果**判断,并且 `managed-settings.d` 目录及其中每个文件都要满足 root 所有、组和其他不可写(设计已有此要求,实测证明客户端本身不会替我们检查)。

### 2. 托管 hook 的 command 写法(P2-INSTALL `org render`、P2-TIER 条件 5)

实测 13 种写法(agent 的 `PATH` 把可写目录排在最前):

- **绝对路径**(`/path/ballast` + `args` 数组,或单串)被执行,所写文件本身就是被执行的文件。含空格的路径:`args` 形式和加引号的单串可用,**不加引号的单串被 shell 拆开,不执行**。
- **裸名**(`m0b-bare`)按调用进程的 `PATH` 解析,**agent 可写的 `PATH` 目录排在前面时被劫持**(执行的是 agent 的副本);带不带 `args` 都一样。
- **`./相对路径`** 按会话目录解析(agent 所有),**可被劫持**,带 `args` 也一样。
- **`~/…` 与 `$HOME/…`** 单串形式被 shell 展开并执行 agent 家目录里的文件(**可被劫持**);带 `args` 的形式**不做展开,不执行**。
- 结论与条件 5 一致:`command` 必须是绝对路径;`org render` 的 `command` + `args` 形式没有引号和展开问题,保持不变。条件 5 对相对、裸名、`~`、`$HOME` 形式判 `managed_hooks` 失败是正确的。

### 3. 条件 6:低层削弱沙箱的白名单(P2-TIER `WeakenedSandboxKeys`)——只列有实测依据的放宽,**契约变更需架构师裁决**

观测手段 ③(mock,客户端真实执行 Bash 与 Write):托管层为 `org render` 的 T1 模板;`baseline` 是只有托管层时的行为。下表"生效"指相对 baseline 改变了实际行为,"无效果"指未改变:

| 键 | 用户层 | 项目层 | 本地层 |
|---|---|---|---|
| `sandbox.filesystem.allowWrite:["<home>"]` | **生效**(会话目录外可写;托管 `denyWrite` 的受保护路径**仍不可写**,denyWrite 优先于 allowWrite) | 无效果 | 无效果 |
| `sandbox.network.allowAllUnixSockets:true` | **生效** | 无效果 | 无效果 |
| `sandbox.network.allowedDomains:["example.com"]` | **生效**(沙箱代理对该域名不再返回 403) | 无效果 | 无效果 |
| `permissions.additionalDirectories:["<home>"]` | **生效**(Write 工具可写会话目录外) | 无效果 | **生效** |
| `sandbox.excludedCommands:["npm"]` | 无效果(裸名 `npm` 只匹配精确命令 `npm`,不匹配 `npm run x`) | 无效果 | 无效果 |
| `sandbox.excludedCommands:["npm:*"]` | **命令未执行(被拒)**:T1 的 `allowUnsandboxedCommands:false` 下,被排除的命令既不在沙箱内也不在沙箱外运行 | 无效果(仍在沙箱内) | 无效果 |
| `sandbox.network.allowUnixSockets:["/var/run/docker.sock"]` | 无效果(该路径经 `/var/run` → `/run` 符号链接,是否因此未生效没有区分) | 无效果 | 无效果 |
| `sandbox.enableWeakerNestedSandbox:true` | **未观测**:本容器里沙箱内可见的 `/proc` 进程数不随该键变化,指示器没有区分度 | 未观测 | 未观测 |

关键发现:**项目层与本地层的 `sandbox.*` 键在托管层存在 `sandbox.allowUnsandboxedCommands:false` 时不改变沙箱行为**。逐个剥离托管模板里的键后(`lower-sandbox-merge` 的"托管变体"行):去掉 `allowManagedPermissionRulesOnly`、`allowManagedHooksOnly`、`sandbox.filesystem`、`sandbox.excludedCommands` 都**不**恢复生效;只剩 `sandbox.enabled:true`,或只剩 `enabled` 加 `failIfUnavailable:true`,或根本没有托管 `sandbox` 段(项目/本地层自己开沙箱)时,项目层与本地层的 `allowWrite`、`allowAllUnixSockets`、`excludedCommands:["npm:*"]` **全部生效**;只剩 `enabled` 加 `allowUnsandboxedCommands:false` 时又被压制。也就是说压制低层沙箱键的是托管键 `sandbox.allowUnsandboxedCommands:false`,而条件 5 已经要求它为 `false`。用户层不受这个压制。无托管层的对照组里三个层的 `allowWrite` 都生效(并且能写 `~/.ballast`),`~/.claude.json`(全局或按项目)承载的 `sandbox` 键无效果。

建议(待裁决,保守):

1. 用户层的削弱键清单**保持不变**(`allowWrite`、`allowAllUnixSockets`、`allowedDomains`、`additionalDirectories` 被实测证明有效;`excludedCommands`、`allowUnixSockets`、`enableWeakerNestedSandbox` 未证明无害,继续按削弱处理)。
2. 在条件 5 已满足(托管 `allowUnsandboxedCommands:false`)的前提下,项目层与本地层的 **`sandbox.*` 键**(`allowWrite`、`network.*`、`excludedCommands`)可以从"报告"移到"无害"——**仅限这一类**;这是针对 2.1.300、`-p` 模式的实测,升级后要重跑;`WeakenedSandboxKeys(doc, LayerRepo)` 若放宽,必须把这个依赖写进函数注释(依赖条件 5 的 `allowUnsandboxedCommands:false`),并在 doctor 里保留一条"托管层缺少该键时项目层沙箱键有效"的提示。
3. 项目层与本地层的 **顶层键不能放宽**,实测仍然有效(见下),`permissions.additionalDirectories` 在本地层有效,必须继续报告。

项目层与本地层的顶层键(`-p` 模式,托管层为带 `allowManagedHooksOnly:true` 的 T1 模板;**没有经过信任确认**):

| 键 | 项目层 | 本地层 |
|---|---|---|
| `env`(`{"BALLAST_PROBE_ENV":"1"}`) | **生效**:托管探针 hook 的环境里出现该变量 | **生效** |
| `apiKeyHelper` | **生效**:命令被客户端执行(无 key 时取 key) | **生效** |
| `awsAuthRefresh` | **生效**(`CLAUDE_CODE_USE_BEDROCK=1` 时) | **生效** |
| `otelHeadersHelper` | **生效**(启用遥测导出时) | **生效** |
| `enableAllProjectMcpServers` | 无可观察差异:项目 `.mcp.json` 的 server 在 `-p` 下**本来就自动启动**,不设该键的对照组同样启动 | 同左 |
| `statusLine` | `-p` 下不运行(仅交互 TUI);TUI 取样见下 | 同左 |
| `enabledPlugins` | 未观测到效果(指向不存在的 marketplace,`claude plugin list` 无该条目,不能据此下结论) | 同左 |

`allowManagedHooksOnly:true` **不阻止** `apiKeyHelper`、`awsAuthRefresh`、`otelHeadersHelper` 这类 helper 命令从项目层/本地层执行,`env` 也会进入托管 hook 的环境:设计里"`env`/helper 键在项目层与本地层一律报告"的判断被实测支持。交互式 TUI(pty,blackhole 端点,预置 key 确认)取样:停在工作区信任对话框时,项目层的 hook 与 `statusLine` **都没有运行**(没有标记文件、没有 `SessionStart` 事件);接受信任后项目层 hook 运行,`statusLine` 命令运行;托管层为 T1 模板(含 `allowManagedHooksOnly:true`)时项目层的 `statusLine` 命令没有运行(标记文件未写入),是哪个托管键造成的没有单独验证。`-p` 模式没有信任对话框,项目层的上述 helper 在 `-p` 下直接执行。

### 4. `org render` 的路径写法与 `ManagedForms`(P2-TIER/P2-INSTALL)——【契约问题】2、3

(mock 观察;客户端侧强制,但按 §6.4 只有 credentials 模式的 pass 才算数,故两个用例保持 `needs_credentials`。)

- **`permissions.deny` 中单斜杠开头的绝对路径不按文件系统绝对路径解释**:`Edit(/srv/single/**)` 挡不住对 `/srv/single/x` 的 Write 与 Edit;`Edit(//srv/double/**)`(双斜杠)能挡住。`~/` 与 `./` 形式生效。→ `ManagedForms` 里 org 规则目录、二进制目录与绝对路径 `protected_extra` 的权限条目应改为 `//` 形式。
- **只写 `Write(...)` 族规则挡不住 Write 工具**:`Write(//srv/wonly/**)` 无效,`Edit(...)` 规则同时覆盖 Edit 与 Write 工具(也与模板里已有的 `Edit(~/.ballast/**)` 一致)。`Write(...)` 与 `NotebookEdit(...)` 条目对 Write 工具是冗余的;保留无害,但不能把它们当作屏障。
- **Linux 上 `sandbox.filesystem.denyWrite` 的 glob 不生效**:`/srv/glob/secret*.txt` 挡不住 Bash 对 `/srv/glob/secret1.txt` 的写入;字面的 `~/`、`./`、绝对路径形式生效(denyWrite 优先于 allowWrite)。模板里的 `~/.claude/settings*.json`、`./.claude/settings*.json` 在 Linux 上不能指望靠 denyWrite 的 glob 保护(客户端对 `.claude/` 目录另有内置保护,不能算作托管规则的功劳:对照项 `*-other-control` 用 Write 工具写 `.claude/other.txt`——不匹配 `settings*.json`——同样被拒;Bash 在仓库根的 `.claude/settings*.json` 同样被拒,而在 `a/b/.claude/settings.json` 这种深层路径上写入成功)。
- **`denyWrite` 的 `./` 形式以会话目录为锚点**:会话在子目录时保护不到仓库根。

### 5. `weakened_project` 缺省值(`tier.DefaultWeakenedProject`,P2-CLI)

`managed-denywrite-anydepth` 在 mock 下**不是 pass**:Bash 对 `a/b/.claude/settings.json`、`a/.ballast/project.json` 的写入(`echo >` 与 `python3 -c`)在 `**/` 形式的 denyWrite 下**全部成功**;子目录会话里 Bash 与 Write 工具对仓库根 `.ballast/` 的写入也成功;只有 Edit/Write 工具在会话目录之下的 `**/.ballast/**` 规则被证明生效;`**/.claude/settings*.json` 的"生效"无法与客户端对 `.claude/` 的内置保护区分(对照项 `*-other-control` 同样被拒)。按 §3.2 条件契约(`fail`、`needs_credentials`、`unverifiable` 一律视为未证明),**`DefaultWeakenedProject` 保持 `deny`,P2-CLI 不要改成 `audit`**,`org render` 的默认输出不变。

### 6. 项目层的加载目录(`CLAUDE_PROJECT_DIR`,P2-HOOK、P2-RUNNER)

只加载**启动目录**自己的 `.claude/settings*.json`:在 git 根下的子目录启动时,`CLAUDE_PROJECT_DIR` 就是该子目录,**不向上找 git 根,也不加载父目录或仓库根的项目层**;在非 git 目录的子目录启动(上两级有项目层)时同样不加载。所以设计里"hook 对 `$CLAUDE_PROJECT_DIR` 检查"成立,**不需要追加父目录链**(此前的"若父目录项目层也会被加载"条件未触发,不是契约变更)。

### 7. runner 的 bwrap 参数(P2-SANDBOX)——【契约问题】1

`runner-bwrap-in-docker` 在 Debian 12 容器里实测 §3.4 Linux 行(详见 `payloads/m0b/runner-bwrap-in-docker/results/`):

- 所需 `--security-opt`:本环境(Docker Desktop,无 AppArmor)只需要 `seccomp=unconfined` 与 `systempaths=unconfined`;缺 `seccomp` 时 "No permissions to create new namespace",缺 `systempaths` 时 `Can't mount proc on /newroot/proc`,`apparmor=unconfined` 不是必需。启用 AppArmor 的宿主(Ubuntu 24.04 CI)是否仍需要它【未核验】,在那之前按设计保留三项。
- 按字面加 `--tmpfs /var/run` 时,Debian 12 的 `/var/run` 是指向 `/run` 的符号链接,bwrap 报 `Can't mount tmpfs on /newroot/var/run: No such file or directory` 而无法启动。
- 祖先挂载:按字面(`--bind A A`,排在 W 之后)会让家目录整体可写(`~/.bashrc`、`~/.ballast/evidence` 等 7 项写入成功);改成 `--ro-bind` 但仍排在 W 的 `--bind` 之后,W 一并变成只读(`git add` 失败)。**祖先用 `--ro-bind` 且先于 W 的 `--bind`**、`/var/run` 为符号链接时不再单独 `--tmpfs`,25 个条目全部符合期望。
- 其他实测:新建类条目(`.claude/settings.local.json`、`.vscode/tasks.json`、`.mcp.json`、`evil/.git/config`、`worktrees/w3/config.worktree`)在 W 内可创建,`PostCheck` 重扫能发现;`mv <repo>`、`mv .git`、硬链接、写 `.git/hooks`、`modules/sub/{config,hooks}`、已有的 `worktrees/w2/config.worktree` 全部失败;`--unshare-net` 下宿主回环监听器不可达,allow 下可达,`/run` 下的 unix socket 恒不可达,W 外自定义位置(`<home>/other.sock`)的 unix socket 仍可连接(登记的残余风险);`LoaderEnv` 经 `--setenv` 到达测试;pty 控制终端内 `bwrap --new-session` 里 `open("/dev/tty")` 返回 ENXIO、对 fd 0/1/2 的 `TIOCSTI` 返回 EPERM。

### 8. ConfigChange 与 P2-CC/P2-HOOK(§5.1)

`configchange-payload` 为 pass,可按 §5.1 把 `ConfigChange` 行改为 `Install:true, Verified:true`:`configChangeFileKey` 就是 `file_path`(设计里的占位值正确,不需要改键名),`source` 由 `common.FillCommon` 映射到 `Event.Source`(取值见 A16 行),`fillConfigChange` 把 `file_path` 填进 `Event.ConfigFile`;golden 样本用 `payloads/m0b/configchange-payload/hang/` 里的 payload(已规范化)。注意:托管目录的 `managed-settings.d/` 新增文件与 `~/.claude.json` 不触发 ConfigChange,审计不能当作完整的配置变更记录。

### 9. 其他

- `t1-lower-hooks`(boundary-t1):前提 `nocred-events` 成立,用户层 `disableAllHooks:true` + 托管探针的断言可以无凭据执行。
- `t1-hooks-outside-sandbox`:引用 `hooks-run-outside-sandbox` 的 pass 结论。
- MCP:`org render` 手工补充白名单时用上述键名;白名单内的 server 能写受保护路径(mock 佐证),残余风险不变,只能靠白名单。
- `sandbox.excludedCommands` 的示例 `["npm"]` 只匹配精确命令 `npm`;真正会让 `npm run x` 离开沙箱的写法是 `["npm:*"]`。`WeakenedSandboxKeys` 把任何非空 `excludedCommands` 都当作削弱,与此不冲突。

## 【契约问题】汇总(最小建议)

1. **§3.4 Linux 行**(P2-SANDBOX):祖先挂载改 `--ro-bind` 且先于 W 的 `--bind`;`--tmpfs /var/run` 只在 `/var/run` 不是符号链接时添加。
   **裁决与落地(P2 集成,`docs/p2-design.md` v1.5.5 记入 §3.4 Linux 行与 §13)**:`/var/run` 由 `sandbox.socketDirArgs` 按真实路径屏蔽(符号链接换成解析后的路径,已屏蔽即丢弃);祖先挂载由 `sandbox.AncestorPins` 限定为位于 W 根之内的真实祖先目录,W 之外的祖先(家目录等)从不挂载为可写,所以按字面 `--bind A A` 让家目录整体可写的情形不再出现。`runner-bwrap-in-docker` 保持记 fail(它记录的是"设计按字面不成立"这一事实),不改写取证结果。
2. **§3.2 `ManagedForms`**(P2-TIER、P2-INSTALL):org 规则目录、二进制目录、绝对路径 `protected_extra` 的权限条目改为 `//` 形式(共享 golden 同步);`Write(...)`/`NotebookEdit(...)` 条目对 Write 工具冗余,可保留。需要 credentials 模式确认后再改。
   **裁决与落地(P2 集成,`docs/p2-design.md` v1.5.3)**:项目负责人裁决不等 credentials 模式——绝对路径一律改 `//` 形式;Claude 2.1.300 会把 `Write(...)`/`NotebookEdit(...)` 的路径条目警告为无效,所以不再输出,只保留 `Edit(...)`(`Edit` 规则覆盖 Edit 与 Write 工具,本文第 4 节的 mock 观察与此一致)。`tier.ManagedForms`、共享 golden、`org render` golden 与 `tier.Detect` 条件 5 的测试已同步。
3. **§3.2 denyWrite 的 glob 与 `**/`**(P2-TIER、P2-CLI 文档):Linux 上对 Bash 不生效,不得在 trust-model 里描述为保护;`DefaultWeakenedProject` 保持 `deny`。
4. **§3.2 条件 6**(P2-TIER):见"对设计的影响 3"的保守建议(项目层/本地层 `sandbox.*` 键在条件 5 满足时无害,依赖 `allowUnsandboxedCommands:false`)。
5. **§6.4/§6.5 `lower-sandbox-merge`**(架构师):(a) 设计要求"观测不到 ⇒ needs_credentials 且带可运行的 recheck",但本用例只有 `enableWeakerNestedSandbox` 一个键观测不到,其余键已观测;本实现按"部分观测 pass"记录(该键显式未观测、条件 6 对它保持保守)。请确认这个偏离可以接受,或改写 §6.4 为"部分键观测不到不降级,只对未观测键保持保守"。(b) 该用例目前没有 credentials 分支,§6.5 补测段里的 `--cases lower-sandbox-merge` 命令不能产出真实结果:建议从设计中删去,或另开单元实现 credentials 分支后再恢复。(c) §6.4 末段不依赖事件的用例清单不含 `sandbox-failifunavailable`(它的对照组靠托管 hook 事件判定),检查器已按此收紧。

## 【需凭据】项与补测命令

以下 6 个用例只有 mock 佐证,需要用户明确提供凭据后补测(脚本只从调用者环境读 `ANTHROPIC_API_KEY`,`docker run -e` 只传变量名,运行期改用 `--network bridge`,不落盘、不进日志,不读 keychain 与 `~/.claude*`);补测结果把对应行改写为 `mode:"credentials"`:

```sh
ANTHROPIC_API_KEY=<用户自己提供> scripts/probe-m0b/run.sh --with-credentials --cases sandbox-denywrite-bash,permissions-deny-edit,unsandboxed-refused,managed-denywrite-path-forms,managed-denywrite-anydepth,mcp-write
```

`lower-sandbox-merge` 的 `enableWeakerNestedSandbox` 在无模型手段下观测不到(`/proc` 指示器无区分度),该用例按"部分观测"记为 pass(其余键全部观测,该键显式标"未观测"并保持保守)。**这个键目前没有补测命令**:场景脚本没有 credentials 分支,`run.sh` 也不会给 `needs_credentials` 为 false 的用例传 key,所以此前文档里的 `--with-credentials --cases lower-sandbox-merge` 补测无法产出真实结果,已从 `case.json` 与本文移除,状态为**待实现**(见【契约问题】:设计 §6.4/§6.5 里该用例的 recheck 需要同步)。

补测脚本的自测:`bash scripts/probe-m0b/selftest.sh`(`run.sh` 开始前自动运行),覆盖 credentials 模式环境准备、无工具调用时写 `unverifiable`、以及"带 `--with-credentials` 的 recheck 只能属于 `needs_credentials` 用例"。

## 仍未核验

- 6 个需凭据用例的 `credentials` 模式结论(尤其 `managed-denywrite-anydepth`:只有 credentials 模式的 pass 才能把 `DefaultWeakenedProject` 改为 `audit`,mock 已显示 `**/` 对 Bash 不生效,预期不会 pass)。
- `sandbox.enableWeakerNestedSandbox` 在各层是否生效。
- `enabledPlugins` 在项目层与本地层是否生效(需要一个可用的 marketplace)。
- 交互式 TUI 下 `enableAllProjectMcpServers` 的信任与批准流程、`awsAuthRefresh`/`otelHeadersHelper` 的信任门槛(本次只取样了 `statusLine` 与 hook,以及 `-p` 模式下的 helper)。
- 其他客户端版本、macOS 与 Windows 托管路径(本次只有 Linux 容器与 2.1.300)。
- 启用 AppArmor 的宿主(Ubuntu 24.04 CI)里 bwrap 所需的容器设置。
- `/sandbox` 屏幕(观测手段 ②)只取到了"被更高优先级配置覆盖,不能本地修改"的提示,没有打印生效的沙箱配置;`claude --debug` 启动日志(观测手段 ①)同样没有打印。

## 待同步文本(P2-CLI 集成时同步到 `docs/trust-model.md`、`docs/clients.md`)

- `docs/trust-model.md` 里"未核验,待 M0b"逐条改写为本文结论或 `needs_credentials` 引用:A13/A14/A15 已核验(合并规则见"对设计的影响 1");A20 已核验(hook 在沙箱外,mock 佐证 Write 工具与 MCP 在沙箱外);§2.3 的"任意深度形式"改写为"`**/` 形式对 Bash 不生效,对 Edit/Write 工具按会话目录锚定,需凭据确认";§4 的 MCP 白名单键名写实(`allowedMcpServers`、`deniedMcpServers`、`allowManagedMcpServersOnly`)。
- `docs/clients.md`:Claude Code 一行的"未核验,待 M0b"改为"已在 2.1.300 容器取证(无凭据部分),见 `docs/facts/M0b-CONCLUSIONS.md`"。
- `accept-p2.sh`:追加 `go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json`(不需要 Docker)。
