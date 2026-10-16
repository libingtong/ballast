# Ballast 信任模型与 org 部署指南

> 依据:`docs/ballast-plan.md` B0 与 A13–A20、`docs/architecture.md` §3.3 与 §5.5、`internal/install`(`org render`、`doctor`)。
>
> **标注约定**:凡写着「**未核验,待 M0b**」的内容,表示只有文档层面的依据,没有在真实托管环境里实测过。M0b(在一次性 Linux 容器里写 `/etc/claude-code/managed-settings.json` 并实测)属于 P2,**尚未执行**。部署前请自行在测试环境里逐项验证,不要把本文当作已经证明的保证。

## 1. 一句话

**服务端 CI 是最终裁判。** 会话内的 Ballast 规则用来引导 agent、减少无效往返;它不替代 CI,也不保证交付质量。`ballast doctor` 每次都会重申这一点。

## 2. 两个层级

| 层级 | 适用条件 | 能兑现的保证 |
|---|---|---|
| **T1 托管强制** | macOS / Linux / WSL2,**且**组织部署了托管设置 | 设计目标:agent **不可写**规则、状态、证据和二进制。**未核验,待 M0b** |
| **T0 尽力而为** | 个人安装、没有托管部署,或原生 Windows | 篡改**可检测、不可防** |

`ballast doctor` 的第一行就是当前层级;处于 T0 时会列出缺失的 T1 组件(托管 hook、`allowManagedHooksOnly`、sandbox、`permissions.deny`、MCP 白名单)。

### 2.1 T0:为什么只能检测

hook、规则文件、状态与证据文件都和 agent 以**同一个用户**运行,agent 在原则上能改写它们。Ballast 在 T0 下提供的是"发现",不是"阻止":

- 默认规则 `no-tamper` 检查文件工具和 shell 对 Ballast 规则、状态、证据以及客户端 hook 配置的写入,命中会写入审计。
- 证据只认 `ballast verify` 写下的退出码与内容快照,不采信输出文本;快照与待提交内容不一致就判为过期。
- 审计日志记录每次命中与诊断(见 `ballast audit lint`、`ballast stats`)。
- 这些检测本身也运行在 agent 可触及的环境里,**识别写入靠的是 shell 命令的静态解析**,所以有下面这些已知局限。**不要把 T0 当成安全边界。**

  **识别的写入**(`bashparse` 写入目标表,契约见 `internal/bashparse/doc.go`、`docs/dsl-semantics.md` §6):重定向 `> >> &> >|`;`tee`;`cp`/`mv`/`install`/`rsync` 的**目标**;`sed -i`;`perl -i`/`ruby -i`(含 `-pi`、`-pi.bak`);`git checkout [<rev>] -- <path>`、`git restore <path>`;`rm`、`truncate`、`dd of=`、`touch`、`ln` 的链接;PowerShell 的 `Set-Content` 等(覆盖面窄于 bash)。

  **未覆盖的写法**(尽力列举,不穷尽;命中不了就不会进审计):
  1. 语言运行时内部的写入:`python -c "open(...).write()"`、`node -e`、`php -r`、不带 `-i` 的 `perl -e`/`ruby -e`、`lua`/`awk` 程序里的 `print > file`、`jq`/`yq` 的 `-i`、`gawk -i inplace`;
  2. 先写脚本再执行(`./fix.sh`、`make`、`npm run …`、`python script.py`),或经管道/heredoc 交给解释器(`python <<EOF`、`curl … | sh`、`base64 -d | sh`);
  3. 其他会改写文件的命令:`patch`、`git apply`、`ed`/`ex`/`vim -es`/`emacs --batch`、`sponge`、`curl -o`/`wget -O`、`tar -x`/`unzip`、`find … -delete`/`-exec`、`xargs` 间接调用、`chmod`/`chattr`;
  4. 用 git 把工作区回退到别的状态:`git reset --hard`、`git stash`、`git switch`/`git checkout <分支>`、`git merge`/`pull`/`cherry-pick`、`git clean`,以及不带 `--` 的 `git checkout <path>`(与切换分支无法区分);`git restore` 的 `--pathspec-from-file` 也不展开;
  5. "搬走"与目录级操作:`mv ~/.ballast/rules.json /tmp/x` 的**源**不算写入目标(见 `docs/pinrule-mapping.md` §2.4),`rm -r ~/.ballast`、`cp -r` 到父目录这类目标是目录本身而非受保护文件的写法也不一定命中路径匹配;
  6. 动态内容:命令词本身是变量或 `eval`(`CommandList.Unknown`,按"不确定"放行并审计)、`sh -c "$CMD"`、递归深度超过 2 层的 `sh -c`;
  7. 其他通道:MCP 工具、编辑器/IDE 的自带写文件能力、另一个进程(包括 agent 先启动的后台进程)稍后改写。

  这些都只能靠 T1(沙箱的 `denyWrite`、`permissions.deny`)才拦得住。

原生 Windows 上 Claude Code 没有沙箱,因此永远是 T0(**文档依据,未核验,待 M0b**)。

### 2.2 T1:由哪些部分组成

T1 完全依赖客户端的**托管设置**机制。设计中的组成如下(全部**未核验,待 M0b**):

1. **托管 hook**:命令用 root 拥有的绝对路径(例如 `/usr/local/libexec/ballast/ballast`),不经过 `PATH`,也不调用 agent 能改的脚本。
2. **`allowManagedHooksOnly: true`**:只运行托管层的 hook,阻止用户层、项目层、本地层和插件 hook(托管设置强制启用的插件 hook 例外)。文档称低层的 `disableAllHooks` 不能关闭托管 hook。
3. **sandbox**:`enabled: true`、`allowUnsandboxedCommands: false`、`failIfUnavailable: true`、`excludedCommands: []`,并在 `filesystem.denyWrite` 里列出受保护路径。托管层写空列表**挡不住低层追加**(数组设置一般跨层合并,见 §2.3),所以 P2 的层级判定还要求用户层没有削弱沙箱的键,项目层与本地层则只能按会话检查(见 §2.3 的全机前提)。
4. **`permissions.deny`**:对受保护路径禁止 `Edit`、`Write`、`NotebookEdit`,并设 `allowManagedPermissionRulesOnly: true` 使低层不能放宽。
5. **MCP 白名单**:见下文"残余风险"。

**受保护路径**(`org render` 默认包含前四项):

- org 规则目录(`<托管目录>/ballast/`);
- `~/.ballast/`(state、evidence、audit、cache、backups、install-manifest);
- `<repo>/.ballast/`(`rules.json`、`project.json`);
- Ballast 二进制所在目录;
- `~/.claude/settings*.json`(同样由 `org render` 写入);`<repo>/.claude/settings*.json` 目前**不在**模板里,需要管理员自行追加。

(P2 设计,`docs/p2-design.md` §3.2:`org render` 将另外输出会话目录这一层的 `./.ballast`、`./.claude/settings*.json`,以及任意深度形式 `**/.ballast`、`**/.claude/settings*.json`。字面的 `./` 形式只保护会话目录这一层;任意深度形式在托管设置里是否生效、以什么为锚点**未核验,待 M0b**(需凭据)。在证明有效之前,不能认为子目录或仓库根的 `.claude/settings*.json` 在会话内是拒写的。)

### 2.3 T1 的残余风险

这些是设计阶段就已识别、并接受的边界:

- **沙箱只覆盖 Bash**。文件工具、MCP server 和 hook 都在沙箱外运行,所以文件工具要靠 `permissions.deny`,MCP 要靠白名单(**文档依据,未核验,待 M0b**)。
- **MCP server 在沙箱外运行**。白名单内的 MCP 工具如果有写文件能力,仍然能写受保护路径——这是残余风险,部署时必须逐个评估白名单内的 server。
- **原生 Windows 没有沙箱**,无法达到 T1。
- 托管值不能被低层覆盖,但"少数安全键例外:低层更严的值仍生效"(**未核验,待 M0b**)。
- ConfigChange 事件只覆盖 Claude 设置与 skills,**不会**监视任意 Ballast 文件,部分托管刷新也不触发它(文档依据)。
- 磁盘写满会使 hook 按"任何异常都放行"的设计失效强制。
- **低层削弱沙箱(P2 设计,`docs/p2-design.md` §3.2 条件 6)**:Claude 的数组设置一般跨层合并,托管层的 `excludedCommands: []` 挡不住用户层、项目层、本地层追加 `["npm"]` 或 `["docker"]`;`network.allowUnixSockets`(例如 `docker.sock`)、`filesystem.allowWrite`、`enableWeakerNestedSandbox`、`network.allowedDomains` 同理(**未核验,待 M0b `lower-sandbox-merge`**)。`allowManagedPermissionRulesOnly` 只约束 `permissions`,不约束 `sandbox`。项目层 `.claude/settings.json` 随仓库提交,会话内的 denyWrite 管不到它从 clone 或 pull 进来。只要有这样的值,agent 就能在沙箱外运行被排除的命令,直接写 `~/.ballast/evidence`。P2 的保守做法:用户层出现任何削弱值(白名单之外的 `sandbox` 键、非空的 `permissions.additionalDirectories` 等)就判 T0、doctor 报 fail;项目层与本地层的白名单更严——`env`(可带 `DYLD_INSERT_LIBRARIES`、`LD_PRELOAD`、`GIT_CONFIG_*` 进入托管 hook 与 runner 的环境)、会执行命令的 helper 键(`apiKeyHelper`、`statusLine`、`awsAuthRefresh` 等)、插件与 MCP 自动启用、任何未知顶层键都算削弱(这些键在项目层是否生效**未核验,待 M0b**)。
- **T1 证据的保证是全机范围的(P2 设计)**:证据目录 `~/.ballast/evidence` 是全局的。只要主机上**任何一个** Claude 会话的项目目录(Claude 实际加载的 `CLAUDE_PROJECT_DIR`,在 monorepo 子目录或父目录启动时与仓库根不同)带削弱沙箱的 `.claude/settings*.json`,这个会话就能绕过 runner 在沙箱外直接写证据,而且写入者想填什么 tier、sandbox、origin 都行。所以**T1 证据可信的前提是主机上没有任何削弱沙箱的项目层**。这样的项目层不只会随仓库进来,**agent 自己也能埋下**:托管配置的字面形式只保护会话目录这一层,agent 可以在任意子目录(或者,会话在 monorepo 子目录启动时,在仓库根)写 `.claude/settings.json`,提交、推送后扩散到同事的机器,之后在那里启动的会话就能绕过 runner。P2 设计的处理:托管配置追加任意深度形式(是否生效待 M0b);并把 `weakened_project` 的缺省值写成条件契约——M0b 证明任意深度形式有效时缺省 `audit`,否则(包括未能取证)缺省 `deny`,即带削弱设置的项目里所有工具调用被拒。Ballast 能做的是:hook 在会话首个事件检查项目目录并审计 `lower_sandbox_weakened`(审计行本身也可能被这样的会话事后改写);org.json `weakened_project:"deny"` 让这类会话的所有工具调用被拒(挡不住在工具调用之外起作用的设置,例如 Claude 启动时执行的 helper 命令);runner 对 job 仓库与项目目录再查,不通过时把**它自己写的**记录标为 T0——这只影响诚实记录,不能把伪造的记录变回可信。部署时请确认所有仓库都没有这类设置,保持 `weakened_project:"deny"`(除非 M0b 已证明任意深度形式有效且你接受审计模式);`ballast doctor` 在仓库内运行时会点名具体文件与键。
- **`CLAUDE_CONFIG_DIR`(P2 设计)**:托管配置只保护 `~/.claude/settings*.json`。Claude 带着指向别处的 `CLAUDE_CONFIG_DIR` 启动时,它实际加载的用户层不受保护,层级判定为 T0(`sandbox_deny_write`)。
- **T1 证据保证什么(P2 设计 v1.5)**:hook 代起的 verify 不在真实工作树上执行,而是在 `~/.ballast/work/<job>/tree` 里的私有副本中执行(副本取自 `S_start`,只含已跟踪内容);所以 T1 证据保证的是"runner 亲自执行了 `project.json` 里的命令,执行的内容是 `S_start` 加上内容哈希已记录的白名单输入"。残余风险:① `project.json` 的 `verify[].inputs_untracked` 白名单(默认空,例如 `node_modules/**`)里的文件不在快照 `S`/`C` 中,agent 可以改写它们而不让证据过期,证据只记录其内容哈希 `inputs_hash`,供审计与 CI 对照;② 测试代码可以读取副本之外的文件(文件读是允许的),这类依赖本身是已跟踪内容、会进入提交;③ 人在终端里手动运行的 verify(origin=cli)仍在真实工作树上执行,不受副本保护,agent 放进工作树的未跟踪文件或并发改动可以影响结果;④ 含子模块的仓库在 hook 代起时记 infra,不产生 T1 证据;⑤(P2 设计 v1.5.1)副本由 runner 在沙箱外实体化,其中 `git checkout-index` 处理的是 agent 可影响的索引内容,不写出副本依赖 git 自身的路径防护(大小写或 Unicode 规范化不敏感的文件系统上的路径碰撞,历史上出过此类漏洞);Ballast 在检出前拒绝可疑的路径布局(记 infra `tree_unsafe_path`)并打开 git 的 `core.protectHFS`/`core.protectNTFS`,但仍建议保持 git 的安全更新。白名单输入的复制则由 Ballast 自己保证写不出副本(经副本中的符号链接或已跟踪文件冲突时记 infra `inputs_path_conflict`)。
- **macOS 上 runner 沙箱的 IPC(P2 设计 v1.5)**:沙箱内的测试不能请求沙箱外的系统服务替它执行:mach 服务查找默认拒绝,只放行一个最小白名单;偏好写入(cfprefsd 代写)、launchd 作业提交、LaunchServices、AppleEvents 显式拒绝;WindowServer 与剪贴板服务不在白名单内。文件读、sysctl、IOKit、POSIX/SysV IPC 仍然放行(登记的残余面)。管理员选择 `runner_network:"loopback"` 或 `"allow"` 时,macOS 上的测试可以在所有网络接口上监听并接受其他主机的入站连接(设计期实测,SBPL 挡不住)。
- **托管 hook 进程的环境(P2 设计)**:项目层 `env` 会进入托管 hook 本身。未启用 hardened runtime 的 macOS 构建会加载 `DYLD_INSERT_LIBRARIES`,动态链接的 Linux 构建(例如管理员自建的 cgo 构建)会加载 `LD_PRELOAD`。macOS 发布构建须以 `codesign -o runtime` 签名(doctor `tier.binary_hardening` 检查);Linux 发布构建是静态链接,不受影响。runner 在沙箱外启动的 git 使用去掉 `GIT_*`、`LD_*`、`DYLD_*` 的环境。
- **`runner_allow_write`(P2 设计)**:org.json 中的每一项都等价于把该目录交给 agent 可以改写的测试代码。hook 代起的沙箱测试可以写它,写入在 job 结束后仍然存在,而且不在内容快照 `C` 里;如果它是共享的构建缓存(例如家目录下的 Go 构建缓存),之后在沙箱外运行的人工构建会复用被投毒的条目,形成一条跨越边界、能持久存在的通道。默认建议保持为空:runner 会把常见缓存变量(`GOCACHE`、`XDG_CACHE_HOME`、npm/yarn/pip 缓存)指向每个 job 的临时目录;确需持久缓存时优先用仓库内被忽略的目录,并把这一项当作残余风险评估。

### 2.4 T1 设计中的两条特权链路(P2 设计,`docs/p2-design.md` §3)

以下是 P2 设计冻结的防御,**当前版本尚未实现**:

- **降级攻击**:agent 不需要伪造 T1,只要把 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT` 塞进 Claude Code 进程的环境(例如先设法改写 shell 启动文件),判定就会落到 T0,org 规则和 `no-tamper` 也会改从 agent 可控的目录读取——"降到 T0"本身就是攻击目标,不是防御。P2 的对策是**重定向锁**:只要编译期默认托管目录下存在 root 所有、声明 `tier:"T1"` 的 `org.json`,生产构建就忽略这三个变量,家目录取自 passwd 而不是 `$HOME`,并审计 `env_redirect_ignored`。企业机群里 passwd 可能查不到 sssd/LDAP 用户(静态构建只读 `/etc/passwd`):此时只在 org.json 配置了 root 所有的 `home_root` 父目录、且 `$HOME` 是其下属于该用户、组和其他不可写的直接子目录时才采信;否则该用户的 hook 放行,`ballast doctor` 报 fail 并给出修复提示,`org render --write` 在预检失败时拒绝写入。
- **hook 代起的 verify**:hook 在 Claude 沙箱之外启动 runner,runner 执行的测试代码是 agent 可以改写的。v1.5 起测试不在真实工作树上执行,而是在 runner 实体化的私有副本(`S_start` 的树)里执行,未跟踪与被忽略的文件只有 `inputs_untracked` 白名单命中的才复制;真实工作树、git dir 与 common dir 对测试只读,agent 在测试运行期间对工作树的改动影响不到结果。如果 runner 的沙箱弱于 Claude 自己的 Bash 沙箱,这条链路就是一个沙箱逃逸原语(写 shell 启动文件、git hooks、`~/.claude.json`,重命名受保护路径的祖先目录再重建,联网外传)。P2 的对策:runner 沙箱**默认拒写**(只放行仓库、git dir、job 临时目录与管理员给出的 `runner_allow_write`,其中仍拒受保护路径、git hooks/config 等,并固定这些路径的祖先目录),网络默认拒绝;hook 代起的 job **一律**必须在沙箱里运行,沙箱不可用就不执行。git hooks、git config、gitfile、`.claude/`、`.vscode/`、`.mcp.json` 等"之后会在沙箱外生效"的文件按**任意深度**拒写(嵌套仓库、submodule 也在内),Linux 上沙箱内新建的这类文件在 job 结束后被移出仓库隔离;被包装的测试命令新建会话、不持有人的终端,无法用 `TIOCSTI` 往人的 shell 注入按键。
- **网络与本地 socket**:`runner_network` 默认 `deny`——macOS 上沙箱内的测试连回环也被拒绝(SBPL 分不清测试自己绑定的端口与沙箱外监听在 127.0.0.1 的服务),Linux 使用私有网络命名空间。本地 unix socket(容器守护进程、用户会话总线、SSH agent)的隔离与网络策略无关、恒定执行;`allow` 只放开出站 IP。
- 残余风险:Linux 上自定义位置的 unix socket(例如家目录下的 docker.sock)仍可从沙箱内连接;管理员显式选择 `runner_network:"loopback"` 时,macOS 上的测试可以连接主机回环上的任何服务(IDE 扩展服务、开发代理、容器守护进程的 TCP 端口);选择 `allow` 时,Linux 上主机的抽象命名空间 unix socket 仍可达;macOS 上经 LaunchServices、AppleEvents、launchd、cfprefsd、WindowServer 的逃逸通道由 IPC 默认拒绝关闭,效果待 P2 实测(验收要求脱离 process-exec 规则单独证明 IPC 拒绝生效)。

## 3. verify 链路与信任

- `ballast verify` 一律**后台**运行:detached 的 runner 执行 `project.json` 里的命令,写下 `VerifyRecord`(退出码、起止快照 `S_start`/`S_end`,并 fsync);证据标注 `tier:"T0"`。
- T0 下 agent 自己也能运行 `ballast verify`,所以 agent 理论上可以让测试命令"恰好通过"。Ballast 防不住这一点;`project.json` 里的命令应当是项目真正的测试命令,最终由 CI 复核。
- T1 下的设计是:agent 在沙箱里运行的 `ballast verify` 写不了证据,由 hook 在沙箱外代为发起 runner,把 `S_start` 的树实体化成私有副本,并把测试命令放进默认拒写的 OS 沙箱、在副本里执行(§2.3、§2.4)。这堵住了"放一个未跟踪的测试文件让测试恰好通过"与"取快照后临时换掉测试再换回"两种伪造;白名单输入与人工运行的 verify 仍是残余风险(§2.3)。**这条沙箱化 runner 链路属于 P2,当前未实现。** 所以即使部署了托管设置,当前版本的 verify 证据仍然是 T0 强度。
- 新鲜度判断:`S_end == C`(验证结束时的内容快照等于将要提交的内容),并要求所有 required 的验证最新一条记录有效通过。工作区之后任何变化都会让它自动失效,不依赖编辑 hook。

## 4. org 部署指南(**未核验,待 M0b**)

下面是管理员如何使用 `ballast org render` 的步骤。命令的输出格式有单元测试覆盖,但"这些键在真实 Claude Code 托管环境里是否按预期生效"**未核验,待 M0b**。请先在一次性的测试机或容器里走通,再推广。

### 4.1 前提

- 目标是 Claude Code(`org render` 目前只渲染 `claude-code`)。
- macOS / Linux / WSL2。原生 Windows 没有沙箱,模板对它没有 T1 意义。
- 一个**root 拥有、普通用户不可写**的 Ballast 二进制位置,例如 `/usr/local/libexec/ballast/ballast`。

### 4.2 步骤

1. **放置二进制**:把构建好的 `ballast` 复制到 root 拥有的目录,父目录也必须是 root 拥有且不可被普通用户写入:

   ```sh
   sudo install -d -o root -g root -m 0755 /usr/local/libexec/ballast
   sudo install -o root -g root -m 0755 ./ballast /usr/local/libexec/ballast/ballast
   ```

2. **渲染模板**(只打印,不写系统路径):

   ```sh
   ballast org render --binary /usr/local/libexec/ballast/ballast > ballast-managed.json
   ```

   输出包含:每个 Claude Code 原生事件的托管 hook(`command` + `args`)、`allowManagedHooksOnly`、`sandbox`、`permissions.deny` 与 `allowManagedPermissionRulesOnly`。

3. **人工审阅并补全**:
   - 核对 `sandbox.filesystem.denyWrite` 与 `permissions.deny` 里的受保护路径,按需追加(例如 `<repo>/.claude/settings*.json`)。
   - **MCP 白名单需要手工补充**:CLI 没有提供对应参数。键名要按 Claude 托管设置文档核对(架构文档里的 open question,**未核验,待 M0b**)。只放行确实需要的 server,并评估它们的写文件能力。

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

   `--write` 只在以 root 运行**并且**带 `--i-am-admin` 时才写 `<托管目录>/managed-settings.d/ballast.json`,否则退出码 4。Ballast 不会在别的场合写系统路径。

5. **放 org 规则**:把 org 层规则放到 `<托管目录>/ballast/rules.json`。org 规则整体不可变:repo、user 层出现同 id 规则时会被忽略并记 `rule_shadowed`。`ballast lint --layer org` 要求 org 层自身不超过 12 条。

6. **验证**(每台机器都做,**不要跳过**):

   ```sh
   ballast doctor
   ```

   确认 `claude.managed_duplicate` 等检查为 ok,层级输出里不再缺失 T1 组件。P2 起还要在每个常用仓库(以及在其子目录启动 Claude 的目录)里运行一次 `ballast doctor`,确认 `tier.repo_sandbox_weakened` 为 ok——T1 证据的可信以主机上没有削弱沙箱的项目层为前提(§2.3)。P2 起 `org.json` 的 `weakened_project` 缺省值取决于 M0b 对任意深度形式的取证结果(未证明时为 `deny`),`org render` 的输出会注明依据。

### 4.3 上线后的行为

- 已部署托管 Ballast 时,用户层 `ballast install --client claude-code` 会被拒绝(退出码 1),避免托管 hook 与用户 hook 因为"hook 在各层合并执行"而重复运行。
- 其余客户端(Codex、Cursor、Hermes)没有托管层支持,仍然是 T0。

### 4.4 部署后的验收清单(**待 M0b 才会成为自动化测试**)

计划中的 `boundary-t1.sh` 与 `boundary-t0.sh` 尚未编写。在此之前,建议在测试容器里手工确认:

| 项 | T1 期望 | T0 期望 |
|---|---|---|
| agent 用 `Edit` / `Write` 改 org 规则、`~/.ballast`、`<repo>/.ballast` | 被拒绝 | 能写入,但被 `no-tamper` 命中并写审计 |
| agent 用 `echo >`、`sed -i`、`perl -pi`、`git checkout -- <path>`、`python -c` 写受保护路径 | 沙箱内被拒绝 | `echo >`、`sed -i`、`perl -pi`、`git checkout -- <path>`/`git restore` 被检测并审计;`python -c`、`node -e` 等语言内写入不被识别(完整清单见 §2.1) |
| agent 把伪造的 `ballast` 放进 `PATH` | 托管 hook 用绝对路径,不受影响 | 不适用(用户层安装同样用绝对路径) |
| agent 直接写 evidence 文件 | 沙箱内被拒绝 | 能写入;`verify --check` 仍要求快照一致 |
| agent 通过 MCP 写受保护路径 | 只能靠白名单收口(残余风险) | 不适用 |
| 用户层 `disableAllHooks` | 不能关闭托管 hook | 能关闭 |

## 5. 一页纸总结

- 个人使用:T0。把它当作"行为教练 + 可审计记录",而不是安全产品。
- 团队 / 组织:想要 T1,必须由管理员部署托管设置。**这条路径尚未在真实托管环境验证(待 M0b),沙箱化 runner 也尚未实现。**
- 无论哪个层级:**CI 是最终裁判**。
