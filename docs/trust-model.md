# Ballast 信任模型与 org 部署指南

> 依据:`docs/ballast-plan.md` B0 与 A13–A20、`docs/architecture.md` §3.3 与 §5.5、`internal/install`(`org render`、`doctor`)。
>
> **标注约定**:「**已核验**」指 Ballast 自己取过证:M0b(在一次性 Linux 容器里写 `/etc/claude-code/managed-settings.json`,用 Claude Code 2.1.300 实测,**不需要凭据的部分**)已执行,证据在 [`facts/M0b-CONCLUSIONS.md`](facts/M0b-CONCLUSIONS.md) 与 [`facts/m0b-cases.json`](facts/m0b-cases.json);「**【需凭据】**」指需要真实模型调用才能取证、至今没有用户提供凭据的项(沙箱 denyWrite 对 Bash 的效果、`permissions.deny` 对 Edit 的效果、`allowUnsandboxedCommands`、denyWrite 路径写法、MCP 写入、任意深度形式),它们只有 mock 佐证,补测命令见结论文档;「**未核验**」指只有文档或推断依据。macOS 与 Windows 的托管路径行为、其他客户端版本都没有取过证。部署前请自行在测试环境里逐项验证,不要把本文当作已经证明的保证。

> **v0.6 定位(2026-10-17 项目负责人裁决)**:**T0 是产品主线与默认保证**(篡改可检测、不可防),目标用户是个人开发者,对抗型 agent 不是目标威胁。**T1(托管设置、沙箱 runner、hook 代起 verify、重定向锁、job 私有副本)是实验特性**:代码保留、默认不启用,面向团队合规场景,个人用户无需配置,使用说明见 [`advanced-t1.md`](advanced-t1.md)。本文 §2.2 至 §2.4 与 §4 描述的 T1 内容均按"实验特性"阅读。

## 1. 一句话

**服务端 CI 是最终裁判。** 会话内的 Ballast 规则用来引导 agent、减少无效往返;它不替代 CI,也不保证交付质量。`ballast doctor` 每次都会重申这一点。

## 2. 两个层级

| 层级 | 适用条件 | 能兑现的保证 |
|---|---|---|
| **T0 默认保证(产品主线)** | 所有安装:个人安装、没有托管部署,原生 Windows 也只有 T0 | 篡改**可检测、不可防**:agent 与 Ballast 同一用户权限;`no-tamper` 与审计让这类行为被发现,git pre-commit 与服务端 CI 是最终把关 |
| **T1 托管强制(实验特性)** | macOS / Linux / WSL2,**且**组织部署了托管设置 | 设计目标:agent **不可写**规则、状态、证据和二进制。托管层的合并与强制行为已在 Linux 容器里取证(M0b);沙箱与文件工具屏障对真实模型调用的效果仍为【需凭据】 |

`ballast doctor` 的第一行就是当前层级。**T0 主机上 `tier.level` 为 ok,消息是"T0(默认模式)",不列缺失的 T1 组件**,也不输出其余 `tier.*` 行(原生 Windows 同样如此)。只有声明了 T1(可信的 `org.json`)、主机已锁定,或加了 `ballast doctor --t1` 时,才会输出 `tier.*` 全部检查,此时处于 T0 会列出缺失的 T1 组件(托管 hook、`allowManagedHooksOnly`、sandbox、`permissions.deny` 等,完整清单见 `docs/p2-design.md` §3.2 的六个条件与 `model.TierMissing*`,名称与含义对照见 §2.2 末尾的表)。**层级判定与 doctor 不检查 MCP 白名单**:没有 MCP 白名单的主机照样判为 T1("managed deployment complete"),白名单需要管理员按 §4.2 第 3 步手工补充并自行核对。

### 2.1 T0:为什么只能检测

hook、规则文件、状态与证据文件都和 agent 以**同一个用户**运行,agent 在原则上能改写它们。Ballast 在 T0 下提供的是"发现",不是"阻止":

- 默认规则 `no-tamper` 检查文件工具和 shell 对 Ballast 规则、状态、证据以及客户端 hook 配置的写入,命中会写入审计。
- 证据只认 `ballast verify` 写下的退出码与内容快照,不采信输出文本;快照与待提交内容不一致就判为过期。
- 审计日志记录每次命中与诊断(见 `ballast audit lint`、`ballast stats`)。
- 这些检测本身也运行在 agent 可触及的环境里,**识别写入靠的是 shell 命令的静态解析**,所以有下面这些已知局限。**不要把 T0 当成安全边界。**

  **识别的写入**(`bashparse` 写入目标表,契约见 `internal/bashparse/doc.go`、`docs/dsl-semantics.md` §6):重定向 `> >> &> >|`;`tee`;`cp`/`mv`/`install`/`rsync` 的**目标**;`sed -i`;`perl -i`/`ruby -i`(含 `-pi`、`-pi.bak`);`git checkout [<rev>] -- <path>`、`git restore <path>`;`rm`、`truncate`、`dd of=`、`touch`、`ln` 的链接;PowerShell 的 `Set-Content`/`Add-Content`/`Out-File`/`Tee-Object`/`Clear-Content`/`Remove-Item`/`Copy-Item`/`Move-Item`/`Rename-Item`/`New-Item`(含 `-LiteralPath`)、重定向,以及 `[IO.File]::WriteAllText`/`AppendAllText`/`WriteAllBytes`/`WriteAllLines` 这类静态调用(覆盖面窄于 bash;类型存在变量里再调用、`.Invoke`、哈希字面量值里的调用、`[global::System.IO.File]::` 不识别,不产生节点)。

  **受检测的路径**:默认 `no-tamper` 的路径表覆盖 `**/.ballast/**`、`~/.ballast/**`、各客户端的 hook 配置文件(字面的 `~/.claude/`、`~/.codex/`、`~/.hermes/` 形式;引擎在编译这三类前缀时还会按 hook 进程环境里的 `CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME` 绝对路径值及其符号链接解析形式展开,与字面形式并存,所以重定向后的配置根同样受检测;值为相对路径时无从解析,不展开),以及 org 规则目录,即三个平台默认托管目录下的 `ballast/`(`/etc/claude-code/ballast/**`、`/Library/Application Support/ClaudeCode/ballast/**`、`c:/Program Files/ClaudeCode/ballast/**`)。org 规则目录在真实部署里是 root 所有,权限本身就拦得住非 root 的写入;把它列进路径表是为了让 agent 以同一用户权限(例如 root 会话或权限被误配的目录)改写它时也留下审计命中。同样只是检测,不是屏障。

  **未覆盖的写法**(尽力列举,不穷尽;命中不了就不会进审计):
  1. 语言运行时内部的写入:`python -c "open(...).write()"`、`node -e`、`php -r`、不带 `-i` 的 `perl -e`/`ruby -e`、`lua`/`awk` 程序里的 `print > file`、`jq`/`yq` 的 `-i`、`gawk -i inplace`;Hermes 的 `execute_code`(模型提交的 Python 脚本在 Hermes 的代码执行环境里运行,脚本内部直接 `open(...).write()` 不会再产生 `write_file`/`patch` 的 `pre_tool_call`;Ballast 把它保留为原名 `execute_code`,规则可以按名字匹配它,但 `no-tamper` 看不到其中的写入);
  2. 先写脚本再执行(`./fix.sh`、`make`、`npm run …`、`python script.py`),或经管道/heredoc 交给解释器(`python <<EOF`、`curl … | sh`、`base64 -d | sh`);
  3. 其他会改写文件的命令:`patch`、`git apply`、`ed`/`ex`/`vim -es`/`emacs --batch`、`sponge`、`curl -o`/`wget -O`、`tar -x`/`unzip`、`find … -delete`/`-exec`、`xargs` 间接调用、`chmod`/`chattr`;
  4. 用 git 把工作区回退到别的状态:`git reset --hard`、`git stash`、`git switch`/`git checkout <分支>`、`git merge`/`pull`/`cherry-pick`、`git clean`,以及不带 `--` 的 `git checkout <path>`(与切换分支无法区分);`git restore` 的 `--pathspec-from-file` 也不展开;
  5. "搬走"与目录级操作:`mv ~/.ballast/rules.json /tmp/x` 的**源**不算写入目标(见 `docs/pinrule-mapping.md` §2.4),`cp -r` 到父目录、`rm -rf ~/.claude` 这类目标是目录本身而非受保护文件的写法不命中路径匹配(`rm -r ~/.ballast` 例外:`**/.ballast/**` 连目录本身一起匹配,会被检测);
  6. 动态内容:命令词本身是变量或 `eval`(`CommandList.Unknown`,按"不确定"放行并审计)、`sh -c "$CMD"`、递归深度超过 2 层的 `sh -c`;
  7. 其他通道:MCP 工具、编辑器/IDE 的自带写文件能力、另一个进程(包括 agent 先启动的后台进程)稍后改写。
  8. 客户端配置根重定向的残余:引擎只按 hook 进程环境里的**绝对路径**值展开 `CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME`,因此下面三种情形下重定向后的 `settings*.json`、`hooks.json`、`config.toml`、`config.yaml` 不被识别:(a) 变量值是相对路径(客户端相对它自己的 cwd 解析,hook 无从得知);(b) 客户端加载配置时设置了该变量,而 hook 进程的环境里没有它(或值不同);(c) `ballast replay` 离线重放(不带这些变量)。

  这些都只能靠 T1(沙箱的 `denyWrite`、`permissions.deny`)才拦得住。

  **本节的两张清单是可执行的**:`scripts/boundary-t0.sh`(三平台,不需要模型和容器)把每一类写法都做成 `scripts/boundary/t0-cases.json` 里的行,喂给真实的 `ballast hook`:"识别的写入"(含对 org 规则目录的重定向、文件工具写入与 `cp`)必须在审计里留下 `no-tamper` 命中,"未覆盖的写法"(第 1–8 类各不少于两例;第 8 类的相对路径与 hook 环境缺变量两种情形有行,离线 replay 无 hook 进程,不在此脚本内)必须**不**留下。**哪个缺口被检测到了,脚本就失败**并提示"覆盖面变了,请同步更新 trust-model §2.1"——所以这两张清单与代码不会悄悄漂移。同一脚本还断言:手写一条 `S_end` 随意填的伪造 pass 记录,`ballast verify --check` 仍判过期(10);伪造者若先算出真实的内容快照 `C` 再写进记录,则**能**骗过 `--check`(0)——这正是 T0 "可检测、不可防"的边界,脚本把它记为一条缺口,不隐藏;以及 ConfigChange 审计行(`type:"config_change"`,detail `{source, file, protected}`,stdout 恒空,绝不阻断)。

原生 Windows 上 Claude Code 没有沙箱,因此永远是 T0(文档依据;真实模型调用下的效果【需凭据】)。

### 2.2 T1(实验特性):由哪些部分组成

T1 完全依赖客户端的**托管设置**机制。组成如下(合并与强制语义已由 M0b 在 Linux 容器里用 Claude Code 2.1.300 取证;标【需凭据】的项只有 mock 佐证):

1. **托管 hook**:命令用 root 拥有的绝对路径(例如 `/usr/local/libexec/ballast/ballast`),不经过 `PATH`,也不调用 agent 能改的脚本。
2. **`allowManagedHooksOnly: true`**:只运行托管层的 hook,阻止用户层、项目层、本地层和插件 hook(托管设置强制启用的插件 hook 例外)。低层的 `disableAllHooks` 不能关闭托管 hook(M0b 已核验:`lower-disableallhooks-vs-managed`、`managed-only-blocks-lower`)。
3. **sandbox**(`failIfUnavailable: true` 已核验:没有 bwrap/socat 时客户端启动阶段拒绝运行;其余键对真实 Bash 调用的效果为【需凭据】):`enabled: true`、`allowUnsandboxedCommands: false`、`failIfUnavailable: true`、`excludedCommands: []`,并在 `filesystem.denyWrite` 里列出受保护路径。M0b `lower-sandbox-merge`(mock)在这套模板下观测到:托管层的 `allowUnsandboxedCommands:false` 压制项目层与本地层的全部 `sandbox.*` 键,低层 `excludedCommands` 不会让命令跑到沙箱外;用户层的 `allowWrite`、`allowAllUnixSockets`、`allowedDomains`、`additionalDirectories` 则生效(细节见 §2.3;以上都以 credentials 模式确认为准)。P2 的层级判定仍取保守做法:要求用户层没有削弱沙箱的键,项目层与本地层则只能按会话检查(见 §2.3 的全机前提)。
4. **`permissions.deny`**:对受保护路径给出 `Edit(...)` 条目(绝对路径用 `//` 开头的形式;`Edit` 规则同时覆盖 Edit 与 Write 工具,不再输出会被客户端警告为无效的 `Write(...)`、`NotebookEdit(...)` 条目),并设 `allowManagedPermissionRulesOnly: true` 使低层不能放宽。
5. **MCP 白名单**:T1 的部署要求之一,但**不属于层级判定的条件**(`tier.Detect` 与 doctor 都不读 `allowedMcpServers`,缺失时不会降为 T0,也不会提示);见下文"残余风险"与 §4.2 第 3 步。

**`TierInfo.Missing` 的名称与含义**(`ballast doctor` 的 `tier.level` 逐项列出;六个判定条件见 `docs/p2-design.md` §3.2,与上面五个组成部分不是一一对应,例如第 1 条 hook 对应 `managed_hooks`,而 `env_override`、`binary_root_owned`、`org_config` 是对"部署本身是否可信"的判定):

| Missing id | 含义 |
|---|---|
| `os` | 原生 Windows 或未知系统:只有 T0 |
| `env_override` | 未锁定的主机上 `BALLAST_HOME`/`BALLAST_MANAGED_DIR`/`BALLAST_INSTALL_ROOT` 生效 |
| `binary_root_owned` | ballast 二进制或其某级父目录不是 root 所有,或组/其他用户可写 |
| `org_config` | 托管目录下的 `ballast/org.json` 不存在、不是 root 所有、校验失败、父目录链可被非 root 写入,或未声明 `"tier":"T1"` |
| `managed_settings` | 没有 root 所有且组/其他不可写的 Claude 托管设置文件 |
| `managed_hooks` | 托管设置中没有命令为绝对路径且指向本二进制的 ballast hook |
| `allow_managed_hooks_only` | `allowManagedHooksOnly` 不是 `true` |
| `sandbox` | 托管 sandbox 未启用,或允许无沙箱命令、不要求沙箱可用,或带有 `excludedCommands` 等削弱项 |
| `sandbox_deny_write` | `sandbox.filesystem.denyWrite` 未逐字覆盖受保护路径;`CLAUDE_CONFIG_DIR` 指向 `<home>/.claude` 之外时也报这一项 |
| `permissions_deny` | `permissions.deny` 未逐字覆盖受保护路径 |
| `managed_permissions_only` | `allowManagedPermissionRulesOnly` 不是 `true` |
| `lower_sandbox_weakened` | 用户层 `settings*.json` 含可能削弱托管沙箱的键 |
| `unreadable` | 某个输入读取或解析失败(判定取保守一侧) |

**受保护路径**(`org render` 默认包含前四项):

- org 规则目录(`<托管目录>/ballast/`);
- `~/.ballast/`(state、evidence、audit、cache、backups、install-manifest);
- `<repo>/.ballast/`(`rules.json`、`project.json`);
- Ballast 二进制所在目录;
- `~/.claude/settings*.json`(`org render` 以字面形式 `~/.claude/settings*.json` 写入,不跟随 `CLAUDE_CONFIG_DIR`;`CLAUDE_CONFIG_DIR` 指向别处时判 T0,见 §2.3;runner 的受保护路径(`tier.ProtectedPaths`)另外遵循 `CLAUDE_CONFIG_DIR`);
- 会话目录这一层的 `./.ballast`、`./.claude/settings*.json`,以及任意深度形式 `**/.ballast`、`**/.claude/settings*.json`(P2 起 `org render` 输出,字符串集合与 `internal/tier/testdata/managed_forms.json` 逐字一致,`tier.Detect` 用同一份判定"覆盖")。

**M0b 对这些写法的实测(Linux,mock 佐证;【需凭据】的真实模型调用确认前不得当作保证)**:

- 字面的 `./` 形式只以**会话目录**为锚点:会话在仓库子目录启动时,保护不到仓库根。
- **`sandbox.filesystem.denyWrite` 里的 glob(`settings*.json`)与 `**/` 形式在 Linux 上对 Bash 不生效**:`**/.claude/settings*.json`、`**/.ballast` 拦不住 Bash 对 `a/b/.claude/settings.json`、`a/.ballast/project.json` 的写入(`echo >`、`python3 -c`);只有 Edit/Write 工具在会话目录之下的 `**/.ballast/**` 规则被证明生效。**不要把 `**/` 形式当作 Bash 写入的保护**。字面的 `~/`、`./`、绝对路径形式生效,denyWrite 优先于 allowWrite。
- `permissions.deny` 里**单斜杠开头的绝对路径不按文件系统绝对路径解释**(`Edit(/srv/x/**)` 挡不住 `/srv/x/y`),`//` 形式才行;`~/` 与 `./` 形式生效。`Edit(...)` 规则同时覆盖 Edit 与 Write 工具,`Write(...)` 与 `NotebookEdit(...)` 条目对 Write 工具是冗余的,Claude Code 2.1.300 还会把它们警告为无效条目。架构师已裁决(M0b 结论【契约问题】2):`ManagedForms` 输出的 `permissions.deny` 只含 `Edit(...)` 条目,org 规则目录、二进制目录与绝对路径 `protected_extra` 一律写成 `Edit(//…)`,共享 golden `internal/tier/testdata/managed_forms.json` 与 `tier.Detect` 条件 5 的"覆盖"判定同步(按字符串集合逐字包含,所以旧的单斜杠或 `Write(...)` 写法不再算覆盖)。`sandbox.filesystem.denyWrite` 里的绝对路径保持单斜杠形式。
- 因此 `org.json` 的 `weakened_project` 缺省值保持 **`deny`**(`tier.DefaultWeakenedProject`;条件契约要求 `managed-denywrite-anydepth` 在 credentials 模式 pass 才能改为 `audit`,目前是 `needs_credentials`)。`scripts/boundary-t1.sh --with-credentials` 的补测会重新核对。

### 2.3 T1(实验特性)的残余风险

这些是设计阶段就已识别、并接受的边界:

- **沙箱只覆盖 Bash**。文件工具、MCP server 和 hook 都在沙箱外运行,所以文件工具要靠 `permissions.deny`,MCP 要靠白名单(M0b 已核验:`hooks-run-outside-sandbox` 为 pass,hook 在沙箱外运行;文件工具与 MCP 在沙箱外是 mock 佐证,真实模型调用下的效果【需凭据】)。
- **MCP server 在沙箱外运行**。白名单内的 MCP 工具如果有写文件能力,仍然能写受保护路径——这是残余风险,部署时必须逐个评估白名单内的 server。
- **原生 Windows 没有沙箱**,无法达到 T1。
- 托管值不能被低层覆盖,但"少数安全键例外:低层更严的值仍生效"。M0b 实测的合并规则:`managed-settings.d/*.json` 按文件名字典序在基础文件之后合并,**标量与布尔值后者胜**(收紧型布尔值也不例外,没有"true 粘住"),hook 数组拼接,所以层级判定对**合并后的结果**判断,并要求该目录及其中每个文件 root 所有、组和其他不可写;客户端本身不检查属主与权限。低层 `disableAllHooks` 关闭全部非托管 hook、托管 hook 仍运行;`allowManagedHooksOnly:true` **不阻止**项目层/本地层的 `apiKeyHelper`、`awsAuthRefresh`、`otelHeadersHelper` 这类 helper 命令执行,项目层 `env` 也会进入托管 hook 的环境(这正是条件 6 对这些键报告的依据)。
- ConfigChange 事件只覆盖 Claude 设置与 skills,**不会**监视任意 Ballast 文件,托管目录新增 `managed-settings.d/` 文件与改写 `~/.claude.json` 也不触发它(M0b 已核验:`configchange-payload`)。
- 磁盘写满会使 hook 按"任何异常都放行"的设计失效强制。
- **低层削弱沙箱(P2 设计,`docs/p2-design.md` §3.2 条件 6)**:数组设置在 Claude 的各层之间一般是合并的,但 M0b `lower-sandbox-merge`(mock,Claude Code 2.1.300,`-p` 模式;以真实模型调用下的结果为准,【需凭据】)在 `org render` 的 T1 模板下观测到的是另一回事:**托管层的 `allowUnsandboxedCommands:false` 压制项目层与本地层的全部 `sandbox.*` 键**(`allowWrite`、`allowAllUnixSockets`、`allowedDomains`、`excludedCommands` 都没有效果)。低层 `excludedCommands:["npm"]` 无效果(裸名只匹配精确命令 `npm`,不匹配 `npm run x`);用户层 `["npm:*"]` 的结果是命令被拒,不会跑到沙箱外。用户层不受压制:`filesystem.allowWrite`、`network.allowAllUnixSockets`、`network.allowedDomains`、`permissions.additionalDirectories` 生效(托管 `denyWrite` 的受保护路径仍不可写,denyWrite 优先于 allowWrite)。项目层与本地层的 `sandbox.*` 键只在托管层缺少 `allowUnsandboxedCommands:false` 时才生效(这是条件 5 要求该键为 `false` 的原因之一);`enableWeakerNestedSandbox` 在无模型手段下观测不到,保持保守。`allowManagedPermissionRulesOnly` 只约束 `permissions`,不约束 `sandbox`。项目层 `.claude/settings.json` 随仓库提交,会话内的 denyWrite 管不到它从 clone 或 pull 进来。所以在条件 5 满足的主机上,"项目层 `excludedCommands` 让 agent 在沙箱外写证据"在 mock 中**没有观测到**;但这是 mock 结论,P2 不据此放宽判定,仍取保守做法:用户层出现任何削弱值(白名单之外的 `sandbox` 键、非空的 `permissions.additionalDirectories` 等)就判 T0、doctor 报 fail;项目层与本地层的白名单更严——`env`(可带 `DYLD_INSERT_LIBRARIES`、`LD_PRELOAD`、`GIT_CONFIG_*` 进入托管 hook 与 runner 的环境)、会执行命令的 helper 键(`apiKeyHelper`、`statusLine`、`awsAuthRefresh` 等)、插件与 MCP 自动启用、任何未知顶层键都算削弱(M0b 实测:项目层与本地层的 `env`、`apiKeyHelper`、`awsAuthRefresh`、`otelHeadersHelper` **生效**,`allowManagedHooksOnly` 不阻止它们;`statusLine` 在 `-p` 下不运行、交互模式下经工作区信任确认后运行;`enabledPlugins` 未观测到效果)。
- **T1 证据的保证是全机范围的(P2 设计)**:证据目录 `~/.ballast/evidence` 是全局的。只要主机上**任何一个** Claude 会话的项目目录(Claude 实际加载的 `CLAUDE_PROJECT_DIR`,在 monorepo 子目录或父目录启动时与仓库根不同)带削弱沙箱的 `.claude/settings*.json`,这个会话就能绕过 runner 在沙箱外直接写证据,而且写入者想填什么 tier、sandbox、origin 都行(这是保守前提:M0b mock 在 T1 模板下没有观测到项目层 `sandbox.*` 键生效,见上一条;但项目层 `env` 与 helper 键实测生效,Claude 启动时执行的 helper 本来就在沙箱外,且 credentials 模式尚未确认)。所以**T1 证据可信的前提是主机上没有任何削弱沙箱的项目层**。这样的项目层不只会随仓库进来,**agent 自己也能埋下**:托管配置的字面形式只保护会话目录这一层,agent 可以在任意子目录(或者,会话在 monorepo 子目录启动时,在仓库根)写 `.claude/settings.json`,提交、推送后扩散到同事的机器,之后在那里启动的会话就能绕过 runner。P2 设计的处理:托管配置追加任意深度形式(M0b 的 mock 观察是 `**/` 形式对 Bash 不生效,见 §2.2,所以不能依赖它);并把 `weakened_project` 的缺省值写成条件契约——M0b 证明任意深度形式有效时缺省 `audit`,否则(包括未能取证)缺省 `deny`,即带削弱设置的项目里所有工具调用被拒。Ballast 能做的是:hook 在会话首个事件检查项目目录并审计 `lower_sandbox_weakened`(审计行本身也可能被这样的会话事后改写);org.json `weakened_project:"deny"` 让这类会话的所有工具调用被拒(挡不住在工具调用之外起作用的设置,例如 Claude 启动时执行的 helper 命令);runner 对 job 仓库与项目目录再查,不通过时把**它自己写的**记录标为 T0——这只影响诚实记录,不能把伪造的记录变回可信。部署时请确认所有仓库都没有这类设置,保持 `weakened_project:"deny"`(除非 `managed-denywrite-anydepth` 已在 credentials 模式证明任意深度形式有效且你接受审计模式);`ballast doctor` 在仓库内运行时会点名具体文件与键。
- **`CLAUDE_CONFIG_DIR`(P2 设计)**:托管配置只保护 `~/.claude/settings*.json`。Claude 带着指向别处的 `CLAUDE_CONFIG_DIR` 启动时,它实际加载的用户层不受保护,层级判定为 T0(`sandbox_deny_write`)。
- **T1 证据保证什么(P2 设计 v1.5)**:hook 代起的 verify 不在真实工作树上执行,而是在 `~/.ballast/work/<job>/tree` 里的私有副本中执行(副本取自 `S_start`,只含已跟踪内容);所以 T1 证据保证的是"runner 亲自执行了 **`S_start` 中那份** `project.json` 里的命令,执行的内容是 `S_start` 加上内容哈希已记录的白名单输入"。(P2 设计 v1.5.2)hook 代起时先读到的 `project.json` 只是钉选值:runner 在执行前要求 `S_start` 里的 `.ballast/project.json` 与它哈希相等、条目逐字相等,否则记 infra `project_json_changed` 不执行——这堵住了"先把命令改成 `true`、等 hook 读完再恢复"的换入换出;`project.json` 未提交、被忽略或被 `exempt` 覆盖时记 infra `project_json_untracked`,所以部署 T1 的仓库必须提交 `.ballast/project.json`,且 `exempt` 不得覆盖它。残余风险:① `project.json` 的 `verify[].inputs_untracked` 白名单(默认空,例如 `node_modules/**`)里的文件不在快照 `S`/`C` 中,agent 可以改写它们而不让证据过期,证据只记录其内容哈希 `inputs_hash`,供审计与 CI 对照;② 测试代码可以读取副本之外的文件(文件读是允许的),这类依赖本身是已跟踪内容、会进入提交;③ 人在终端里手动运行的 verify(origin=cli)仍在真实工作树上执行,不受副本保护,agent 放进工作树的未跟踪文件或并发改动可以影响结果;④ 含子模块的仓库在 hook 代起时记 infra,不产生 T1 证据;⑤(P2 设计 v1.5.1)副本由 runner 在沙箱外实体化,其中 `git checkout-index` 处理的是 agent 可影响的索引内容,不写出副本依赖 git 自身的路径防护(大小写或 Unicode 规范化不敏感的文件系统上的路径碰撞,历史上出过此类漏洞);Ballast 在检出前拒绝可疑的路径布局(记 infra `tree_unsafe_path`)并打开 git 的 `core.protectHFS`/`core.protectNTFS`,但仍建议保持 git 的安全更新;⑥(第 3 轮系统审计登记)**exempt 路径的内容不在证据保证之内**:副本取自 `T_full`(含被 `project.json` 的 `exempt` 匹配的已跟踪文件),而 `S_start`、`S_end` 与待提交内容快照 `C` 都不含它们,所以测试读到的 exempt 文件内容既不被新鲜度判断覆盖,也不像 `inputs_untracked` 那样记哈希。默认建议的 `exempt`(`**/*.md`、`docs/**`)还会匹配 `testdata` 下的 markdown golden 文件:测试若读取 exempt 命中的已跟踪文件,改动它不会让证据过期。处置:**测试要读的输入不要放进 `exempt` 的 glob**(把 exempt 限定在确实与结论无关的路径,避开 `testdata`);设计上的取舍见 `docs/p2-design.md` §3.4 第 3 条,残余登记在 §11,尚未提供自动告警或 exempt 内容哈希。白名单输入的复制则由 Ballast 自己保证写不出副本(经副本中的符号链接或已跟踪文件冲突时记 infra `inputs_path_conflict`);读取真实索引与白名单源文件时不跟随符号链接、只接受普通文件(v1.5.2),agent 把它们换成管道或设备只会让 job 记 infra,不会挂起。
- **macOS 上 runner 沙箱的 IPC(P2 设计 v1.5)**:沙箱内的测试不能请求沙箱外的系统服务替它执行:mach 服务查找默认拒绝,只放行一个最小白名单;偏好写入(cfprefsd 代写)、launchd 作业提交、LaunchServices、AppleEvents 显式拒绝;WindowServer 与剪贴板服务不在白名单内。文件读、sysctl、IOKit、POSIX/SysV IPC 仍然放行(登记的残余面)。管理员选择 `runner_network:"loopback"` 或 `"allow"` 时,macOS 上的测试可以在所有网络接口上监听并接受其他主机的入站连接(设计期实测,SBPL 挡不住)。
- **托管 hook 进程的环境(P2 设计)**:项目层 `env` 会进入托管 hook 本身。未启用 hardened runtime 的 macOS 构建会加载 `DYLD_INSERT_LIBRARIES`,动态链接的 Linux 构建(例如管理员自建的 cgo 构建)会加载 `LD_PRELOAD`。macOS 发布构建须以 `codesign -o runtime` 签名(doctor `tier.binary_hardening` 检查);Linux 发布构建是静态链接,不受影响。runner 在沙箱外启动的 git 使用去掉 `GIT_*`、`LD_*`、`DYLD_*` 的环境。
- **`runner_allow_write`(P2 设计)**:org.json 中的每一项都等价于把该目录交给 agent 可以改写的测试代码。hook 代起的沙箱测试可以写它,写入在 job 结束后仍然存在,而且不在内容快照 `C` 里;如果它是共享的构建缓存(例如家目录下的 Go 构建缓存),之后在沙箱外运行的人工构建会复用被投毒的条目,形成一条跨越边界、能持久存在的通道。默认建议保持为空:runner 会把常见缓存变量(`GOCACHE`、`XDG_CACHE_HOME`、npm/yarn/pip 缓存)指向每个 job 的临时目录;确需持久缓存时优先用仓库内被忽略的目录,并把这一项当作残余风险评估。
- **runner 的临时目录(P2 设计 v1.5.6)**:`TMPDIR` 与缓存变量指向的 job 临时目录属于沙箱可写集合 `W`,测试运行时的构建产物放在这里,不在快照 `S_start`/`S_end` 里。hook 代起的 job 把它建在 `~/.ballast/work/<job>/tmp`(与私有副本同级;agent 自己的 Claude 沙箱写不进 `~/.ballast`),**不再**建在 hook 从 Claude 继承来的 `$TMPDIR` 之下:那个变量由 Claude Code 进程选定,runner 无从判断该目录在 agent 自己的沙箱里是否可写(macOS 上 Claude 的 Bash 沙箱可能放行它,【需凭据】未核验),可写时 agent 就能在测试运行期间改动这些中间产物、绕过私有副本的保护。`sandbox.ValidateSpec` 只接受 `<WorkRoot>/<job>/tmp` 作为 `~/.ballast` 之内的第二个可写例外(第一个是私有副本),集成用例断言该目录可写而同一 job 目录的其余部分不可写。人在终端里手动运行的 verify(origin=cli)仍使用 `$TMPDIR`/系统临时目录,它本来就不受私有副本保护(见"hook 代起的 verify"一条的 ③)。Linux 不受影响(bwrap 给 `/tmp` 私有 tmpfs)。

- **v1.5.4 补充登记**(各条的原因与处置见 `docs/p2-design.md` §11):① 用户层 `settings*.json` 或 `~/.claude` 是指向 `~/.claude` 之外的符号链接时(dotfiles 仓库),条件 6 跟随链接读目标,内容无害就判 T1,但托管 `denyWrite` 的字面形式未必盖住链接目标,待项目负责人裁决;② `RepoMandatoryDeny` 追加的 shell/工具启动文件名(`.bashrc`、`.zshrc`、`.gitconfig` 等)在仓库任意深度都是只读,dotfiles 仓库与相关夹具里的 verify 会可见地失败;③ macOS 上沙箱内的后代进程可以 `setsid` 脱离进程组,躲过收尾时的 `killpg`,在 `PostCheck`、`Quarantine` 与 `S_end` 之后继续写入沙箱允许的范围,仍写不了受保护路径(origin=hook 时副本随后删除,origin=cli 时只会让结果事后过期),所以包注释不再宣称"S_end 之后没有沙箱进程";④ 大小写不敏感的 macOS 卷上 `.CLAUDE`、`.Git` 这类变体的保护来自 sandbox-exec 自己的路径匹配,不是 Ballast 的匹配器,有测试钉住;⑤ 实体化与 `git add -u` 在沙箱外运行 git。(a) 副本里测试可写的 `.gitattributes`,配合**用户或系统级**配置里的 git-lfs 之类过滤器,会在沙箱外处理测试控制的内容;(b) 仓库自己的 git 配置(`<common dir>/config`、`config.worktree`)若定义了 `filter.<name>.clean/smudge/process`,过滤器命令就会在沙箱外执行,所以 runner 在每次沙箱外 git 调用的批次前用 `git config --list --show-scope` 检查(含 include 展开;git 2.26 之前没有 `--show-scope`,退回按文件列出,此时仓库配置里的任何 include 都视为不安全),发现即 infra `git_config_unsafe`、不执行任何命令(只报键名,不报命令内容;因此仓库本地配置了 git-lfs 过滤器的仓库在 hook 代起时会记 infra,请改用全局配置);用户与系统级配置仍被信任。**这道防线依赖一个没有被 Ballast 验证的前提:agent 写不了 `<repo>/.git/config`。**Bash 的写入由沙箱挡住(该文件在两个沙箱的危险文件清单里);agent 的**文件工具**(Edit/Write)能否写它取决于 Claude Code 自己的内置行为,`tier.ManagedForms` 没有这一项(加条目要 M0b 取证,【需凭据】),所以不能说"驱动只来自 agent 写不了的配置"。此外检查与随后的 git 子进程不是原子的,检查通过后、git 读取配置前被改写的窗口看不见。这两点是**【未闭合】**的残余风险(`docs/p2-design.md` §11);利用前提是 agent 先能写 `.git/config`,代码执行发生在沙箱外、hook 代起 verify 期间。快照 fallback 的 git(热路径)同样不做这项检查;⑥ `ballast verify` 的前缀赋值写法(`X=1 ballast verify test`)被当作规范形式,赋值被悄悄丢弃,不影响安全;⑦ `ballast report` 的 `Validate` 不知道内置规则 id,"自定义 id 必须哈希"只由生成端保证;⑧ macOS 的 hardened runtime 检查只看 `CS_RUNTIME`,不看允许 `DYLD_*` 的 entitlement。

### 2.4 T1(实验特性)设计中的两条特权链路(P2 设计,`docs/p2-design.md` §3)

以下防御在 P2 实现并由脚本验收(`scripts/boundary-t1.sh`,Linux 容器内真实的 bwrap;`scripts/sandbox-docker.sh`;`go test ./internal/tier ./internal/runner ./internal/sandbox`):

- **降级攻击**:agent 不需要伪造 T1,只要把 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT` 塞进 Claude Code 进程的环境(例如先设法改写 shell 启动文件),判定就会落到 T0,org 规则和 `no-tamper` 也会改从 agent 可控的目录读取——"降到 T0"本身就是攻击目标,不是防御。P2 的对策是**重定向锁**:只要编译期默认托管目录下存在 root 所有、声明 `tier:"T1"` 的 `org.json`,生产构建就忽略这三个变量,家目录取自 passwd 而不是 `$HOME`,并审计 `env_redirect_ignored`。企业机群里 passwd 可能查不到 sssd/LDAP 用户(静态构建只读 `/etc/passwd`):此时只在 org.json 配置了 root 所有的 `home_root` 父目录、且 `$HOME` 是其下属于该用户、组和其他不可写的直接子目录时才采信;否则该用户的 hook 放行,`ballast doctor` 报 fail 并给出修复提示,`org render --write` 在预检失败时拒绝写入。
- **hook 代起的 verify**:hook 在 Claude 沙箱之外启动 runner,runner 执行的测试代码是 agent 可以改写的。v1.5 起测试不在真实工作树上执行,而是在 runner 实体化的私有副本(`S_start` 的树)里执行,未跟踪与被忽略的文件只有 `inputs_untracked` 白名单命中的才复制;真实工作树、git dir 与 common dir 对测试只读,agent 在测试运行期间对工作树的改动影响不到结果。如果 runner 的沙箱弱于 Claude 自己的 Bash 沙箱,这条链路就是一个沙箱逃逸原语(写 shell 启动文件、git hooks、`~/.claude.json`,重命名受保护路径的祖先目录再重建,联网外传)。P2 的对策:runner 沙箱**默认拒写**,可写集合按来源区分——hook 代起的 job 只放行它的私有副本、job 临时目录与管理员给出的 `runner_allow_write`(真实仓库与 git dir 不可写);人在终端运行的 verify(origin=cli)放行仓库、git dir、job 临时目录与 `runner_allow_write`;其中仍拒受保护路径、git hooks/config 等,并固定这些路径的祖先目录;网络默认拒绝;hook 代起的 job **一律**必须在沙箱里运行,沙箱不可用就不执行。git hooks、git config、gitfile、`.claude/`、`.vscode/`、`.mcp.json` 等"之后会在沙箱外生效"的文件按**任意深度**拒写(嵌套仓库、submodule 也在内),Linux 上沙箱内新建的这类文件在 job 结束后被移出仓库隔离;被包装的测试命令新建会话、不持有人的终端,无法用 `TIOCSTI` 往人的 shell 注入按键。
- **网络与本地 socket**:`runner_network` 默认 `deny`——macOS 上沙箱内的测试连回环也被拒绝(SBPL 分不清测试自己绑定的端口与沙箱外监听在 127.0.0.1 的服务),Linux 使用私有网络命名空间。本地 unix socket(容器守护进程、用户会话总线、SSH agent)的隔离与网络策略无关、恒定执行;`allow` 只放开出站 IP。
- 残余风险:Linux 上自定义位置的 unix socket(例如家目录下的 docker.sock)仍可从沙箱内连接;管理员显式选择 `runner_network:"loopback"` 时,macOS 上的测试可以连接主机回环上的任何服务(IDE 扩展服务、开发代理、容器守护进程的 TCP 端口);选择 `allow` 时,Linux 上主机的抽象命名空间 unix socket 仍可达;macOS 上经 LaunchServices、AppleEvents、launchd、cfprefsd、WindowServer 的逃逸通道由 IPC 默认拒绝关闭。核验状态分两部分:LaunchServices 与 AppleEvents 的拒绝在**去掉 process-exec 拒绝的 profile 变体**下已在本机通过(`internal/sandbox/ipc_darwin_test.go` 的 `TestDarwinLaunchServicesAndAppleEvents/without_process-exec_denials`),即它们不依赖 process-exec 规则也能单独生效;cfprefsd 偏好写入、WindowServer/剪贴板、`launchctl` 等 `CI=true` 才运行的用例(`TestDarwinPreferencesWrite`、`TestDarwinWindowServerAndPasteboard`、`TestDarwinIPCControl`)待 macOS CI 接入后核验,此前仍是【未核验】。

## 3. verify 链路与信任

- `ballast verify` 不带 `--foreground` 时一律**后台**运行(`--foreground` 供 CI 与人工调试,同步执行并按结果退出;它写下的证据同样标 `origin=cli`,不受 hook 代起的私有副本保护,残余风险同 §2.3 ③):detached 的 runner 执行 `project.json` 里的命令,写下 `VerifyRecord`(退出码、起止快照 `S_start`/`S_end`,并 fsync);证据标注 `tier:"T0"`。
- T0 下 agent 自己也能运行 `ballast verify`,所以 agent 理论上可以让测试命令"恰好通过"。Ballast 防不住这一点;`project.json` 里的命令应当是项目真正的测试命令,最终由 CI 复核。
- **T0 主机(未声明 T1、未锁定)上 PreToolUse 不拦截规范形式的 `ballast verify <name>`**,verify 走上面这条 detached 后台 runner,证据 `tier:"T0"`(`internal/hook` 的 `TestT0HostCanonicalVerifyIsNotIntercepted` 与 `internal/cli` 的 `TestT0HostDoesNotInterceptCanonicalVerify` 锁定)。hook 代起 verify 只在 T1 生效。
- (实验特性)T1 下是这样工作的:agent 在沙箱里运行的 `ballast verify` 写不了证据,由 hook 在沙箱外代为发起 runner(`ballast verify --hook-start …` 子进程;agent 看到的是 deny,理由里有 job id,之后用 `ballast verify --status <job id>` 查询,`verify --status <id>` 先看这个 id 是否是一个含多个 job 的请求(hook 代起时 request id 就是第一个 job 的 id,同名 job 总是存在,所以多 job 的请求文件要先于同名 job 查找),是则聚合最差结果;只有一个 job 或 id 不是请求时按 job 查找;都找不到退出 11),runner 把 `S_start` 的树实体化成私有副本,并把测试命令放进默认拒写的 OS 沙箱(Linux bwrap、macOS sandbox-exec)、在副本里执行(§2.3、§2.4)。这堵住了"放一个未跟踪的测试文件让测试恰好通过"与"取快照后临时换掉测试再换回"两种伪造(`scripts/boundary-t1.sh` 的伪造用例逐个验证);白名单输入与人工运行的 verify 仍是残余风险(§2.3)。证据标 `tier:"T1"` 当且仅当 runner 自己判定为 T1 且沙箱真的用上了;后端不可用时 hook 代起的 job 不执行测试(infra `sandbox_unavailable`),不产生任何 pass 证据。
- 新鲜度判断:`S_end == C`(验证结束时的内容快照等于将要提交的内容),并要求所有 required 的验证最新一条记录有效通过。工作区之后任何变化都会让它自动失效,不依赖编辑 hook。

## 4. org 部署指南(实验特性 T1;托管层行为已由 M0b 取证;【需凭据】项见 §2.2)

下面是管理员如何使用 `ballast org render` 的步骤。命令的输出格式有单元测试覆盖,但"这些键在真实 Claude Code 托管环境里是否按预期生效",无模型就能观察的部分已由 M0b 在 Linux 容器里核验(合并规则、hook 写法、`failIfUnavailable`、MCP 白名单键名、hook 在沙箱外运行),需要真实模型调用的部分仍为【需凭据】。macOS 与 Windows 的托管路径没有取过证。请先在一次性的测试机或容器里走通(`scripts/boundary-t1.sh` 就是这样一个容器),再推广。

### 4.1 前提

- 目标是 Claude Code(`org render` 目前只渲染 `claude-code`)。
- macOS / Linux / WSL2。原生 Windows 没有沙箱,模板对它没有 T1 意义。
- 一个**root 拥有、普通用户不可写**的 Ballast 二进制位置,例如 `/usr/local/libexec/ballast/ballast`。

### 4.2 步骤

1. **放置二进制**:把构建好的 `ballast` 复制到 root 拥有的目录,父目录也必须是 root 拥有且不可被普通用户写入:

   ```sh
   sudo install -d -o root -g 0 -m 0755 /usr/local/libexec/ballast
   sudo install -o root -g 0 -m 0755 ./ballast /usr/local/libexec/ballast/ballast
   ```

2. **渲染模板**(只打印,不写系统路径):

   ```sh
   ballast org render --binary /usr/local/libexec/ballast/ballast > ballast.json
   ballast org render --binary /usr/local/libexec/ballast/ballast --part org-json > org.json
   ```

   stdout 恒为**单个合法 JSON 文档**,可以直接重定向成文件:默认 `--part managed-settings` 是托管设置片段(部署为 `managed-settings.d/ballast.json`),`--part org-json` 是 org.json(部署为 `<托管目录>/ballast/org.json`)。两次调用要带相同的 `--binary`、`--home-root`、`--protect`。org.json 各字段的风险提示写到 stderr,不进 stdout。Windows 只有 T0,没有 org.json(`--part org-json` 退出码 2)。

   输出包含:每个 Claude Code 原生事件(含 ConfigChange)的托管 hook(`command` + `args`)、`allowManagedHooksOnly`、`sandbox`、`permissions.deny` 与 `allowManagedPermissionRulesOnly`,`--part org-json` 输出的 **org.json**(`tier`、`runner_sandbox`、`runner_network`、`runner_allow_write`、`weakened_project`,以及 `--home-root`/`--protect` 给出的 `home_root`/`protected_extra`)。stderr 的提示写明了各项的风险(`runner_allow_write` 的每一项都等于把该目录交给 agent 可改写的测试代码,建议保持为空;`runner_network:"loopback"`/`"allow"` 是残余风险)。`--write` 时两个文件各写各的,内容与上面两个 stdout 一致。sssd/LDAP 用户在静态构建下 passwd 里查不到,请加 `--home-root /home`(推荐),否则这些用户在已锁定的主机上每次 hook 调用都会放行,`ballast doctor` 的 `tier.locked_home` 报 fail。

3. **人工审阅并补全**:
   - 核对 `sandbox.filesystem.denyWrite` 与 `permissions.deny` 里的受保护路径,按需追加(例如 `<repo>/.claude/settings*.json`)。
   - **MCP 白名单需要手工补充**:CLI 没有提供对应参数。键名已由 M0b `mcp-allowlist-nocred` 核对:`allowedMcpServers`(条目形状 `{"serverName":"..."}`)、`deniedMcpServers`、`allowManagedMcpServersOnly`;拼错的键不起作用且不报错。白名单内的 MCP server 仍能写受保护路径(残余风险)。只放行确实需要的 server,并评估它们的写文件能力。

4. **下发**:由管理员用自己的流程(配置管理、MDM、手工)把内容放到托管目录:

   | 平台 | 托管目录 |
   |---|---|
   | macOS | `/Library/Application Support/ClaudeCode` |
   | Linux / WSL2 | `/etc/claude-code` |
   | Windows | `C:\Program Files\ClaudeCode` |

   写成 `managed-settings.d/ballast.json`(或合并进 `managed-settings.json`)。也可以让 Ballast 代写:

   ```sh
   sudo ballast org render --binary /usr/local/libexec/ballast/ballast --write --i-am-admin
   ```

   `--write` 只在以 root 运行**并且**带 `--i-am-admin` 时才写,否则退出码 4:先写 `<托管目录>/managed-settings.d/ballast.json`,再写 `<托管目录>/ballast/org.json`(写入 org.json 就是锁定本机:此后生产构建忽略 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT`,家目录取自 passwd)。以 `sudo` 运行时(环境里有 `SUDO_UID`)会先预检调用者的家目录:passwd 里没有该用户的条目又没有 `home_root` ⇒ 退出码 2、什么都不写。Ballast 不会在别的场合写系统路径。

5. **放 org 规则**:把 org 层规则放到 `<托管目录>/ballast/rules.json`。org 规则整体不可变:repo、user 层出现同 id 规则时会被忽略并记 `rule_shadowed`。`ballast lint --layer org` 要求 org 层自身不超过 12 条。

6. **验证**(每台机器都做,**不要跳过**):

   ```sh
   ballast doctor
   ```

   确认 `claude.managed_duplicate` 等检查为 ok,层级输出为 `T1: managed deployment complete`,`tier.locked_home`、`tier.runner_sandbox`、`tier.binary_hardening` 为 ok(macOS 发布构建须 `codesign -o runtime`)。P2 起还要在每个常用仓库(以及在其子目录启动 Claude 的目录)里运行一次 `ballast doctor`,确认 `tier.repo_sandbox_weakened` 为 ok——T1 证据的可信以主机上没有削弱沙箱的项目层为前提(§2.3)。P2 起 `org.json` 的 `weakened_project` 缺省值取决于 M0b 对任意深度形式的取证结果(未证明时为 `deny`),`org render` 的输出会注明依据。P2 起 hook 代起的 verify 要求每个仓库的 `.ballast/project.json` 已提交、不被 `exempt` 覆盖(否则记 infra `project_json_untracked`,见 §2.3)。

### 4.3 上线后的行为

- 已部署托管 Ballast 时,用户层 `ballast install --client claude-code` 会被拒绝(退出码 1),避免托管 hook 与用户 hook 因为"hook 在各层合并执行"而重复运行。
- 其余客户端(Codex、Cursor、Hermes)没有托管层支持,仍然是 T0。

### 4.4 部署后的验收清单

自动化部分(不需要模型,不触碰真实配置):

| 脚本 | 内容 |
|---|---|
| `bash scripts/boundary-t0.sh` | 三平台。T0 能检测什么、检测不了什么:`scripts/boundary/t0-cases.json` 逐行喂给真实的 `ballast hook`(识别的写入必须命中 `no-tamper`,§2.1 的八类缺口必须**不**命中,缺口一旦被检测到脚本即失败);伪造 pass 记录(任意 `S_end` 判过期,先算出真实 `C` 的伪造者骗得过,记为缺口);ConfigChange 审计 |
| `BALLAST_REQUIRE_DOCKER=1 bash scripts/boundary-t1.sh` | 需要 Docker(P2 步骤已接入 CI:`accept-p2` 在 ubuntu-latest 跑,真实 bwrap 的完整 `boundary-t1.sh` 由 `Linux T1 沙箱(ubuntu-22.04)` job 必跑;见 `docs/p2-design.md` §11)。在 Debian 12 容器里以 root 安装 ballast、`org render --write --i-am-admin`,之后全部以普通用户 `agent` 运行:t1-doctor、t1-runner-sandbox(bwrap 内的逃逸脚本,hook 代起与人工运行两种来源,submodule/worktree 夹具,五个伪造用例)、t1-tty(`TIOCSTI` 注入)、t1-lower-sandbox、t1-config-dir、t1-forged-binary、t1-orgjson、t1-env-override(重定向锁:agent 目录里放一份真实的 pass 记录,锁定主机上不被采信,未锁定时才被采信的对照)、t1-passwd-missing、t1-sandbox-missing、t1-hooks-outside-sandbox 与 t1-lower-hooks(Claude Code 2.1.300,假 key + 黑洞端点) |
| `BALLAST_REQUIRE_DOCKER=1 bash scripts/sandbox-docker.sh ./internal/sandbox ./internal/runner` | 沙箱与 runner 的 Go 集成测试在真实 bwrap 下运行 |

需要真实模型调用的部分(**【需凭据】**,没有用户明确提供凭据就不做;下表的"T1 期望"是设计目标,尚未用真实 agent 证明):

| 项 | T1 期望 | T0 期望 |
|---|---|---|
| agent 用 `Edit` / `Write` 改 org 规则、`~/.ballast`、`<repo>/.ballast` | 被拒绝 | 能写入,但被 `no-tamper` 命中并写审计 |
| agent 用 `echo >`、`sed -i`、`perl -pi`、`git checkout -- <path>`、`python -c` 写受保护路径 | 沙箱内被拒绝 | `echo >`、`sed -i`、`perl -pi`、`git checkout -- <path>`/`git restore` 被检测并审计;`python -c`、`node -e` 等语言内写入不被识别(完整清单见 §2.1) |
| agent 把伪造的 `ballast` 放进 `PATH` | 托管 hook 用绝对路径,不受影响 | 不适用(用户层安装同样用绝对路径) |
| agent 直接写 evidence 文件 | 沙箱内被拒绝 | 能写入;`verify --check` 仍要求快照一致 |
| agent 通过 MCP 写受保护路径 | 只能靠白名单收口(残余风险) | 不适用 |
| 在项目层带 `excludedCommands:["npm:*"]` 的仓库里 `npm run` 写证据 | 仍在沙箱内或被拒,写不进去(M0b mock:托管层的 `allowUnsandboxedCommands:false` 压制项目层 `sandbox.*` 键;裸名 `["npm"]` 本来就匹配不到 `npm run`);Ballast 仍把该仓库判为 T0(保守) | 不适用 |
| 在子目录写 `a/b/.claude/settings.json`、`a/.ballast/x`(任意深度形式) | 决定 `weakened_project` 缺省值的取证;mock 观察是 Bash 写入成功 | 不适用 |

`scripts/boundary-t1.sh --with-credentials` 覆盖其中的**一个子集**(第 3 轮系统审计更正口径,以脚本实际发出的三条提示为准:① Edit 工具与 `echo >>`/`sed -i`/`python3 -c` 写 `~/.ballast/rules.json`;② Bash 写 evidence 与 `<repo>/.ballast/project.json`;③ 运行 `ballast verify`)。表中**尚未被该脚本尝试**的写法:Write 工具、org 规则目录、`perl -pi`、`git checkout -- <path>`,它们的 T1 期望在补测前仍只是设计目标。`--with-credentials` 另覆盖 `npm run` 一行(项目层写 `npm:*`,断言**未**写入,并以"托管层缺 `allowUnsandboxedCommands:false`"的同一仓库能写入作正向对照;对照不成立时报 SKIP,不记为通过)(含"伪造 `ballast` 放进 `PATH`":伪造品记录 argv,断言托管 hook 从未调用它);后两行(任意深度写入、MCP 白名单内写入)由 M0b 的 `managed-denywrite-anydepth`、`mcp-write` 覆盖,boundary-t1 不测它们。

补测命令(密钥只从调用者的环境读,以 `docker run -e ANTHROPIC_API_KEY`(只传变量名)传入,不写入镜像、文件或日志,不读 keychain 与 `~/.claude*`):

```sh
ANTHROPIC_API_KEY=<用户自己提供> bash scripts/boundary-t1.sh --with-credentials
ANTHROPIC_API_KEY=<用户自己提供> bash scripts/probe-m0b/run.sh --with-credentials --cases sandbox-denywrite-bash,permissions-deny-edit,unsandboxed-refused,managed-denywrite-path-forms,managed-denywrite-anydepth,mcp-write
```

用户层 `disableAllHooks` 不能关闭托管 hook(M0b 已核验,`t1-lower-hooks` 在容器里再验一遍);T0 下它能关闭 Ballast 的 hook。

## 5. 一页纸总结

- 默认且主线:**T0**。把它当作"行为教练 + 可审计记录",而不是安全产品:篡改可检测、不可防,git pre-commit 与服务端 CI 是最终把关。个人用户无需任何托管部署。
- 实验特性 T1(团队 / 组织,面向合规场景):必须由管理员部署托管设置,见 [`advanced-t1.md`](advanced-t1.md)。托管层的合并与强制行为已在 Linux 容器里取证(M0b),沙箱化 runner 已实现并在真实 bwrap 下验收;**让真实模型 agent 去尝试逃逸的那部分仍是【需凭据】、macOS 与 Windows 的托管路径没有取过证,请把它当作设计目标而非保证。**
- 无论哪个层级:**CI 是最终裁判**。
