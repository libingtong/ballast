# 高级:T1 托管强制(实验特性)

> **实验特性,面向团队合规场景;个人用户无需配置。**
>
> Ballast 的产品主线与默认保证是 **T0**(篡改**可检测、不可防**,见 [`README.md`](../README.md#能力边界) 与 [`trust-model.md`](trust-model.md))。T1 在 v0.6 起降为实验特性:代码保留、默认不启用,不属于个人安装的必需步骤,`ballast doctor` 默认也不会提示你去补齐 T1 组件。是否需要它,取决于你的威胁模型里有没有"agent 会故意改写自己的 hook"这一条;目标用户是个人开发者时,这不是目标威胁(定位调整见 [`ballast-plan.md`](ballast-plan.md) 末尾"v0.6 定位调整")。

## 1. T1 是什么

T1 的设计目标是让 agent **不可写**规则、状态、证据和 Ballast 二进制,做法是组织管理员用 Claude Code 的**托管设置**部署 Ballast,再配合沙箱化的 verify runner:

| 组成 | 作用 |
|---|---|
| 托管 hook + `allowManagedHooksOnly` | 低层(用户、项目)的 hook 不能替换或关闭 Ballast 的 hook |
| 托管 `sandbox` + `permissions.deny` | Claude 的 Bash 沙箱与文件工具都写不了受保护路径(规则、状态、证据、二进制) |
| root 所有的二进制与 `org.json` | 二进制和组织声明不能被 agent 改写;`org.json` 声明 `tier:"T1"` 即"锁定本机" |
| 重定向锁 | 已锁定的主机忽略 `BALLAST_HOME`、`BALLAST_MANAGED_DIR`、`BALLAST_INSTALL_ROOT`,家目录取自 passwd,防止"把 hook 降级到 T0"的攻击 |
| hook 代起 verify | agent 运行的规范 `ballast verify <名称>` 由 hook 在 Claude 沙箱外代起,agent 看到的是 deny 加 job id,之后用 `ballast verify --status <job id>` 查询 |
| 沙箱化 runner | 测试命令在 `S_start` 的私有副本里、默认拒写的 OS 沙箱(macOS `sandbox-exec`、Linux `bwrap`)中执行,证据标 `tier:"T1"` |

**T0 主机上这些都不生效**:没有声明 T1、没有被锁定的主机上,PreToolUse 不拦截规范形式的 `ballast verify <名称>`,`verify` 走原有的 detached 后台 runner,证据 `tier:"T0"`。hook 代起 verify 只在 T1 生效(`internal/hook` 与 `internal/cli` 有测试锁定这一点)。

## 2. 要求

- **客户端**:托管设置只渲染 Claude Code(`ballast org render` 目前只支持 `claude-code`)。Codex、Cursor、Hermes 没有托管层,仍是 T0。
- **操作系统**:macOS、Linux、WSL2。原生 Windows 没有沙箱,永远是 T0。
- **二进制位置**:root 所有、普通用户不可写的位置(及其各级父目录),例如 `/usr/local/libexec/ballast/ballast`。macOS 发布构建须以 `codesign -o runtime` 签名;Linux 发布构建是静态链接。
- **沙箱后端**:
  - macOS:`/usr/bin/sandbox-exec`。
  - Linux:bubblewrap(`bwrap`),并且主机**允许非特权用户命名空间**。
  - 没有可用后端时,hook 代起的 verify 不执行测试,记为 infra `sandbox_unavailable`,不产生任何 pass 证据(fail-closed)。
- **Ubuntu 23.10+ / 24.04 的 AppArmor 限制**:这些发行版默认 `kernel.apparmor_restrict_unprivileged_userns=1`,容器或普通用户里的 `bwrap` 建不了用户命名空间(典型报错 `loopback: Failed RTM_NEWADDR`),沙箱后端不可用。已部署 T1 的主机仍判为 T1,但 `doctor --t1` 的 `tier.runner_sandbox` 报 fail,hook 代起的 verify 全部记 infra `sandbox_unavailable`(fail-closed,不产生 pass 证据);未部署时为 T0,该项为 ok 并注明后端不可用。是否放宽该限制(例如为 `bwrap` 单独配置 AppArmor profile,或改用不受限的发行版/内核设置)是管理员的决定,Ballast 不会替你改动任何内核开关或 AppArmor 配置。
- **`.ballast/project.json`** 必须提交到仓库,且 `exempt` 不得覆盖它,否则 hook 代起的 verify 记 infra `project_json_untracked`。

## 3. 管理员部署步骤

完整步骤、每一步的原因与风险见 [`trust-model.md`](trust-model.md) §4。要点如下。

1. **放置二进制**到 root 所有的目录:

   ```sh
   sudo install -d -o root -g 0 -m 0755 /usr/local/libexec/ballast
   sudo install -o root -g 0 -m 0755 ./ballast /usr/local/libexec/ballast/ballast
   ```

2. **渲染模板**(只打印,不写系统路径)。stdout 恒为单个合法 JSON,可以直接重定向;风险提示写 stderr:

   ```sh
   ballast org render --binary /usr/local/libexec/ballast/ballast > ballast.json
   ballast org render --binary /usr/local/libexec/ballast/ballast --part org-json > org.json
   ```

   两次调用要带相同的 `--binary`、`--home-root`、`--protect`。sssd/LDAP 用户在静态构建下 passwd 里查不到,请加 `--home-root /home`,否则这些用户在已锁定的主机上每次 hook 调用都会放行。原生 Windows 没有 `org.json`(`--part org-json` 退出码 2)。

3. **人工审阅并补全**:核对受保护路径;MCP 白名单需要手工补充(CLI 没有对应参数)。
4. **下发**到托管目录(macOS `/Library/Application Support/ClaudeCode`、Linux/WSL2 `/etc/claude-code`),写成 `managed-settings.d/ballast.json` 和 `ballast/org.json`;也可以由 root 运行 `sudo ballast org render --binary … --write --i-am-admin` 代写(写入 `org.json` 即锁定本机)。
5. **放 org 规则**到 `<托管目录>/ballast/rules.json`。org 规则整体不可变。
6. **验证**,每台机器都做:

   ```sh
   ballast doctor --t1
   ```

   层级应为 `T1: managed deployment complete`,`tier.locked_home`、`tier.runner_sandbox`、`tier.binary_hardening` 为 ok;在每个常用仓库里再运行一次,确认 `tier.repo_sandbox_weakened` 为 ok(T1 证据的可信以主机上没有削弱沙箱的项目层为前提)。

已部署托管 Ballast 的主机上,用户层 `ballast install --client claude-code` 会被拒绝(退出码 1),避免 hook 重复执行。

## 4. doctor 与 `--t1`

默认的 `ballast doctor` 在 T0 主机上只输出一行 `tier.level`(ok,"T0(默认模式)"),不列任何缺失的 T1 组件。下列情形才会输出全部 `tier.*` 检查:

- 命令行加了 `ballast doctor --t1`;
- 可信的 `org.json` 已声明 `tier:"T1"`;
- 主机已锁定(存在 root 所有的 `org.json` 声明 T1)。

此时 `tier.level` 逐条列出缺失的 T1 组件,另有 `tier.declared_mismatch`、`tier.runner_sandbox`、`tier.redirect_ignored`、`tier.locked_home`、`tier.repo_sandbox_weakened`、`tier.binary_hardening`。检查逻辑与 v0.5 完全相同,只是默认不展示。

## 5. 验收

不需要模型和凭据的自动化验收:

| 命令 | 内容 |
|---|---|
| `BALLAST_REQUIRE_DOCKER=1 bash scripts/boundary-t1.sh` | 需要 Docker。在 Debian 12 容器里以 root 安装并 `org render --write --i-am-admin`,之后全部以普通用户运行:t1-doctor、t1-runner-sandbox(真实 bwrap 内的逃逸脚本、伪造用例)、t1-tty、t1-lower-sandbox、t1-config-dir、t1-forged-binary、t1-orgjson、t1-env-override、t1-passwd-missing、t1-sandbox-missing 等 |
| `BALLAST_REQUIRE_DOCKER=1 bash scripts/sandbox-docker.sh ./internal/sandbox ./internal/runner` | 沙箱与 runner 的 Go 集成测试在真实 bwrap 下运行 |
| `bash scripts/accept-p2.sh` | P2 集成验收,上面两项都在其中;CI 的 `Linux T1 沙箱(ubuntu-22.04)` job 完整运行它。在限制用户命名空间的主机上,验收改为断言 fail-closed(沙箱不可用、hook 代起 verify 记 infra、doctor 判 T0),`boundary-t1.sh` 显式跳过 |
| `bash scripts/boundary-t0.sh` | T0 的边界登记表,不是 T1 专用,但 T1 的"降级攻击"对照依赖它 |

需要真实模型调用的部分仍是【需凭据】:让真实模型 agent 去尝试逃逸并没有被证明过;macOS 与 Windows 的托管路径没有取过证。补测命令:

```sh
ANTHROPIC_API_KEY=<你自己提供> bash scripts/boundary-t1.sh --with-credentials
```

密钥只从环境读取、以变量名传给 `docker run`,不写入任何文件。

## 6. 命令与参数(实验)

| 命令 | 说明 |
|---|---|
| `ballast org render [--part managed-settings\|org-json] [--home-root <目录>]... [--protect <路径>]... [--binary <绝对路径>] [--write --i-am-admin]` | **[实验]** 输出组织托管设置模板(默认)或 `org.json`;`--write` 由 root 代写两个文件 |
| `ballast doctor --t1` | **[实验]** 输出 T1 的全部 `tier.*` 检查 |
| `ballast verify --hook-start …` | **[实验,内部]** 托管主机上由 hook 代起 verify 的子进程入口,参数格式冻结 |
| `ballast verify --status <job id\|request id>` | 查询 hook 代起 verify 的结果(`--status` 本身在 T0 同样可用) |

## 7. 残余风险

T1 的残余风险(沙箱只覆盖 Bash、MCP server 在沙箱外运行、项目层削弱沙箱的全机范围影响、`runner_allow_write`、`CLAUDE_CONFIG_DIR` 重定向直接判 T0、macOS 沙箱 IPC 等)集中登记在 [`trust-model.md`](trust-model.md) §2.3 与 §2.4,详细设计见 [`p2-design.md`](p2-design.md) §3。**无论哪个层级,服务端 CI 与 git pre-commit 才是最终把关。**
