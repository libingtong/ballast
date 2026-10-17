# Ballast(压舱石)

Ballast 是给 AI 编码客户端用的**行为策略引擎**:把一小组关键规则持续注入会话,在工具调用前做强制检查,并把每一次命中留成可审计的记录。

- **声明式规则**:规则是 JSON,不写代码就能新增拦截;规则语言没有 exec 逃生口,总共不超过 12 条同时生效。
- **四档动作** `log < warn < ask < deny`:新规则默认是影子模式(只记 `log`),攒够证据再升级。
- **证据只认事实**:"测试通过"只以 `ballast verify` 记录的退出码和内容快照为准,不解析测试输出。
- **三层配置** org > repo > user,热更新,配置出错时沿用上一份合法配置。
- **热路径很轻**:每次 hook 调用 p95 不超过 30ms,不联网、不调 LLM,任何异常都放行(stdout 为空、退出码 1)。
- **可审计、可回放**:`stats` 看命中,`replay` 用历史 transcript 离线评估规则。

> 状态:接口契约 v2.0(P2)已冻结,U1 到 U12 与 P2 各单元的实现已合入集成分支。**产品主线与默认保证是 T0**(篡改可检测、不可防):个人开发者安装后即可用,不需要任何托管部署。P2 加入的层级判定与重定向锁、hook 代起 verify、沙箱化 runner、脱敏上报文件与 ConfigChange 审计中,T1 相关部分(托管设置、沙箱 runner、hook 代起 verify、重定向锁)自 v0.6 起是**实验特性**,代码保留、默认不启用,见 [`docs/advanced-t1.md`](docs/advanced-t1.md)。设计依据见 [`docs/ballast-plan.md`](docs/ballast-plan.md)(末尾"v0.6 定位调整"与"v0.5 P2 计划")与 [`docs/p2-design.md`](docs/p2-design.md),接口契约见 [`docs/architecture.md`](docs/architecture.md)。

## 目录

- [能力边界](#能力边界)
- [支持的客户端](#支持的客户端)
- [构建](#构建)
- [安装与卸载](#安装与卸载)
- [快速上手](#快速上手)
- [默认 5 条规则](#默认-5-条规则)
- [可选规则](#可选规则)
- [规则 DSL 简介](#规则-dsl-简介)
- [影子模式与 promote](#影子模式与-promote)
- [stats 与 replay](#stats-与-replay)
- [git pre-commit 模板](#git-pre-commit-模板)
- [命令一览](#命令一览)
- [已知局限](#已知局限)
- [开发与测试](#开发与测试)

## 能力边界

Ballast 的默认保证是 **T0**,面向个人开发者,安装即得,不需要管理员、托管设置或沙箱。完整的信任模型见 [`docs/trust-model.md`](docs/trust-model.md)。

**T0 提供的保证:篡改可检测、不可防。** agent 与 Ballast 以同一用户权限运行,理论上能改写规则和状态文件;Ballast 不假装能阻止这一点,而是保证这类行为**会被发现**:

- **检测**:默认规则 `no-tamper` 检查文件工具和 shell 对 Ballast 规则、状态、证据和客户端 hook 配置的写入,命中写入审计;`ballast doctor` 与 `ballast audit lint` 暴露配置与日志的异常。能识别什么、识别不了什么,由 `scripts/boundary-t0.sh` 作为可执行登记表逐条验证(见"已知局限")。
- **注入**:把一小组关键规则在会话开始和每次提示时持续注入,让 agent 始终看到约束。
- **证据**:"测试通过"只以 `ballast verify` 记录的退出码和内容快照为准,不解析输出;快照与待提交内容不一致就判为过期。
- **审计**:每次规则命中、诊断和 verify 结果都留下记录,可用 `stats` 查看、用 `replay` 离线重放。
- **最终把关**:**git pre-commit 模板与服务端 CI 才是最终把关。** 会话内的规则只负责引导 agent 的行为、减少无效往返,不保证交付质量;真正决定一份改动能不能合入的,应该是服务端 CI。`ballast doctor` 每次都会重申这一点。

其他边界:

- hook 一旦把决策写到 stdout,决策即生效;之后进程即使崩溃也不撤销。
- 磁盘写满会使强制失效(hook 按"任何异常都放行"设计),审计和 `doctor` 会告警。
- "新鲜证据"只看**已跟踪文件**的内容快照:测试依赖了未跟踪文件时,本地可能判为新鲜而 CI 失败,这类情况交给 CI 兜底。

**T1(实验特性)**:面向团队合规场景的托管强制模式(托管设置、沙箱 runner、hook 代起 verify),个人用户无需配置,默认不启用。详见 [`docs/advanced-t1.md`](docs/advanced-t1.md)。

## 支持的客户端

| 级别 | 客户端 | 支持程度 |
|---|---|---|
| 一级 | **Claude Code** | 完整支持。有真实 payload 取证的是各事件的 payload 字段、PreToolUse 的 deny / warn 与 Stop 的 deny / warn(Claude Code 2.1.300);ask、UserPromptSubmit 的 deny 与注入、PostToolUse 的注入、PostToolUseFailure 的注入、SubagentStop 的 deny 与 warn 按文档实现、尚未单独取证(`ballast lint` 给 `action-unverified` 警告) |
| 二级 | **Codex** | 注入与 PreToolUse deny;**不支持 ask**;Stop 的 block 待验证;hook 仅在交互式 TUI 触发。全部行为**未经 Ballast 自己核验** |
| 二级 | **Cursor** | 注入、PreToolUse deny / ask;Stop 只能"续跑"(`followup_message`);另生成托管的 `ballast.mdc` 规则文件兜底。全部行为**未经 Ballast 自己核验** |
| 三级 | **Hermes** | 尽力支持:`pre_tool_call` deny、`pre_llm_call` 注入、`pre_verify` 的 Stop deny(语义是续跑),其余只能记 `log`。**离线核验(`hermes hooks test`),触发时机未核验** |

每个客户端的能力矩阵、事件映射和 payload 字段核验状态见 [`docs/clients.md`](docs/clients.md)。`ballast lint` 会按客户端能力矩阵拒绝不支持的"事件 × 动作"组合(默认检查已安装的客户端加 Claude Code),未核验的组合给警告。

## 构建

需要 Go(版本见 `go.mod`)。

```sh
go build -o ballast ./cmd/ballast
./ballast --help        # 任何命令加 --help 只打印帮助,不产生副作用
```

也可以 `go install github.com/libingtong/ballast/cmd/ballast@latest`,二进制会落在 `$(go env GOPATH)/bin`。

hook 命令里写入的是二进制的**绝对路径**,所以请先把二进制放到一个稳定的位置再安装。`ballast install` 默认取当前可执行文件(解析符号链接);该路径位于临时或构建目录(例如 `go run`)时会以退出码 2 拒绝,可用 `--binary <绝对路径>` 显式指定。

## 安装与卸载

安装器把 hook 条目**外科式**地写进各客户端的配置:只改 Ballast 自己的条目,其余字节保持不变;已有条目原地替换、不调整顺序;重复安装的结果逐字节相同。

```sh
ballast install --client claude-code              # 只装一个
ballast install --client claude-code,codex,cursor # 装多个
ballast install --client claude-code --dry-run    # 只打印 unified diff,不写任何文件
ballast uninstall --dry-run                       # 预览卸载
ballast uninstall --client codex                  # 卸载某个客户端
ballast uninstall                                 # 等价于 --all,以 manifest 为准
ballast doctor                                    # 自检:各客户端状态、规则 lint、层级(T0 为默认模式)
```

每个客户端的写入位置:

| 客户端 | 写入的文件 | 说明 |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | 遵循 `CLAUDE_CONFIG_DIR`。每个事件一组 `{type, command, args}`,命令与参数分开写,路径含空格也不需要拼接 |
| Codex | `~/.codex/hooks.json`、`~/.codex/config.toml` | 遵循 `CODEX_HOME`。`config.toml` 里只确保 `[features]` 下 `hooks = true`:原本没有这个键时,卸载后保留 `hooks = true`(同一个 `hooks.json` 里别的工具的 hook 可能依赖它,是唯一登记的往返残留);原本显式写着 `false` 时,安装会先向 stderr 警告再改成 `true`,卸载时恢复成 `false`。**trust 哈希默认不写**,需要时显式加 `--codex-trust`;不加则要在 Codex TUI 里用 `/hooks` 批准。Codex 先对 `CODEX_HOME` 做规范化(解析符号链接)再计算信任 key,`--codex-trust` 因此写入解析符号链接后的 `hooks.json` 路径;`ballast doctor` 的 `codex.trust` 会提示仍用字面路径的旧条目(重新运行 `ballast install --client codex --codex-trust` 即可修复);Windows 上的规范化形式未核验 |
| Cursor | `~/.cursor/hooks.json`、`~/.cursor/rules/ballast.mdc` | `ballast.mdc` 是整文件托管的 `alwaysApply` 规则,作为注入的可靠通道;安装后需要 Reload Window。它是安装时按当时规则生成的快照:放入或修改规则后重新运行 `ballast install --client cursor` 刷新;没有任何规则时安装会给出警告 |
| Hermes | `~/.hermes/config.yaml` | 遵循 `HERMES_HOME`。只改顶层 `hooks:` 段,段外字节不变(0 字节文件仍是 0 字节、没有末尾换行的文件仍没有);`hooks:` 用 flow 风格或锚点时拒绝写入。启动 Hermes 时需要 `--accept-hooks` |

安全措施:

- **备份**:每个将被改写的文件在写入前都会备份到 `~/.ballast/backups/<UTC 时间>-<随机串>/`(目录 0700、文件 0600),只保留最近 10 份。
- **manifest**:`~/.ballast/install-manifest.json` 记录 Ballast 写入的每个条目及其哈希。归属判定同时要求"命令前缀匹配"和"哈希与 manifest 一致"——你手工改过的条目会被保留并警告,绝不按子串去猜。
- **卸载以 manifest 为准**:即使客户端已经被卸载也能清理;删除 Ballast 新建且现已为空的文件和目录;配置解析失败时返回非 0,不会静默成功。
- **拒绝而不是硬写**:配置文件无法解析时退出码 3 且不写任何内容;组织已通过托管设置部署 Ballast 时,拒绝再做用户层安装(避免 hook 重复执行)。
- 不会在 `init` 时自动安装,不绑定任何解释器,不写 `async` / `onFailure`。

> 安装只写 hook,**不会替你创建规则文件**。规则见下面的"快速上手"。

## 快速上手

顺序很重要:**先放规则,再安装 hook**。Cursor 的 `ballast.mdc` 是安装时按当时的规则生成的快照,先装后放规则会得到一个只有 front matter、没有规则文本的 mdc(此时 `ballast install --client cursor` 会向 stderr 打印警告)。

### 1. 放入规则

把默认规则复制为你的用户层规则(对所有仓库生效),或放进某个仓库的 `.ballast/rules.json`(只对该仓库生效):

```sh
mkdir -p ~/.ballast && cp rules/default.json ~/.ballast/rules.json
ballast lint --client all rules/default.json   # 检查规则
ballast test-rules rules/default.json          # 运行规则内的 examples
ballast config show --explain                  # 查看当前生效的 org / repo / user 三层规则
```

### 2. 安装 hook

```sh
ballast install --client claude-code
ballast doctor
```

规则文件变更后,**hook 通道**(Claude Code、Codex、Hermes,以及 Cursor 的 hook 部分)下一次事件就会读到新规则,不需要重启客户端,也不需要重新安装。

**Cursor 例外**:`~/.cursor/rules/ballast.mdc` 是安装时生成的快照,规则变更后不会自动更新(`ballast doctor` 的 `cursor.mdc_current` 会报告它已过期)。改了规则后重新运行一次

```sh
ballast install --client cursor
```

即可刷新它(hooks.json 无变化,只重写 mdc),然后在 Cursor 里 Reload Window。

### 3. 声明验证命令:init

在仓库里让 Ballast 探测技术栈,并建议验证命令:

```sh
ballast init --dry-run   # 只看将要写入什么
ballast init             # 询问后写入 .ballast/project.json
```

`init` 能识别 9 种栈:go、node、python、rust、maven、gradle、dotnet、swift、ruby。生成的 `.ballast/project.json` 形如:

```json
{
  "version": 1,
  "verify": [{"name": "test", "argv": ["go", "test", "./..."], "timeout_s": 1800, "required": true}],
  "exempt": ["**/*.md", "docs/**"]
}
```

`exempt` 里的文件不计入"内容是否变化"的判断。已有 `project.json` 时,不加 `--force` 不会覆盖(退出码 3)。

### 4. verify:留下证据

```sh
ballast verify                  # 后台启动所有 required 的验证,立即返回 job id
ballast verify --status         # 查询结果:0 pass,1 fail,3 timeout,4 signal,6 运行中,11 无记录
ballast verify --foreground test  # 前台同步运行,供 CI 和人工调试
ballast verify --check          # 当前待提交内容有没有一致且通过的记录
```

不带 `--foreground` 的 `verify` 在后台运行。任务结束后,Ballast 会在下一次注入里附上一行结果。`evidence-before-commit` 规则据此判断"提交的内容是否真的被验证过"。完整退出码见 [`docs/architecture.md` §7](docs/architecture.md)。

### 5. 编写自己的规则

规则是一个 JSON 文件,每条规则必须带 `examples`(至少一个命中、一个未命中):

```json
{
  "version": 1,
  "rules": [
    {
      "id": "no-rm-rf-root",
      "event": "PreToolUse",
      "action": "log",
      "target": "deny",
      "match": { "bash": { "cmd": "rm", "flags": ["-r", "-R", "--recursive"], "args_regex": "(^| )/($| )" } },
      "message": "不要对根目录递归强删。",
      "examples": [
        { "expect": "hit",  "input": { "tool_name": "Bash", "tool_input": { "command": "rm -rf /" } } },
        { "expect": "miss", "input": { "tool_name": "Bash", "tool_input": { "command": "rm -rf ./build" } } }
      ]
    }
  ]
}
```

保存到 `~/.ballast/rules.json`(或仓库的 `.ballast/rules.json`)后:

```sh
ballast lint --client all ~/.ballast/rules.json
ballast test-rules ~/.ballast/rules.json
```

新规则先以 `log` 运行,观察命中是否合理,再按下面的流程升级。

## 默认 5 条规则

`rules/default.json` 恰好 5 条,都是通用规则;凡涉及 shell 的都同时覆盖 bash 与 PowerShell。它们的 `action` 都是 `log`(影子模式),`target` 是 `deny`——也就是说默认只记录"本来会拦"的命中,升级由你决定。

| id | 事件 | 作用 |
|---|---|---|
| `read-before-edit` | PreToolUse | 改已存在的文件前,本会话必须读过它(Read 工具,或 `cat`、`sed -n`、`head` 等只读 shell 命令) |
| `no-long-sleep` | PreToolUse | 不在前台 `sleep` 60 秒以上;等待请用后台运行或有界的短轮询 |
| `evidence-before-commit` | PreToolUse | `git commit` 之前要有与待提交内容一致、已通过的 `ballast verify` 记录 |
| `no-skip-verify` | PreToolUse | 不得用 `--no-verify` / `-n` 跳过 git hooks |
| `no-tamper` | PreToolUse | 不得改写 Ballast 的规则、状态、证据(含各平台托管目录下的 org 规则目录 `ballast/`),或客户端的 hook 配置(文件工具和 shell 写入都覆盖,含经 `CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME` 重定向后的配置根,限制见"已知局限") |

默认包需要你把它**复制**到自己的规则文件(`~/.ballast/rules.json` 或 `<repo>/.ballast/rules.json`)后才生效。它的 `message`、`inject` 文案是中文——这是面向中文使用者的有意取舍,只会影响 Agent 看到的提示语言,不参与匹配;如果你的团队用其他语言,直接改自己副本里的这两个字段即可(改完用 `ballast lint` 和 `ballast test-rules` 复查)。个人偏好类规则(如 `chinese_plain`)不在默认包里。

## 可选规则

放在 `rules/optional/`,按需复制到自己的规则文件里(同时生效的规则不能超过 12 条):

| 文件 | 规则 | 作用 |
|---|---|---|
| `no-foreground-long-task.json` | `no-foreground-long-task`、`no-foreground-install`、`no-foreground-pip-module`、`no-foreground-docker` | 耗时的构建、安装、docker 命令不要前台阻塞 |
| `no-shell-wait.json` | `no-shell-wait`、`no-lang-sleep` | 不用 shell `wait` 或语言内 `sleep` 在前台干等 |
| `claim-needs-evidence.json` | `claim-needs-evidence` | Stop 时宣称"已完成 / 测试通过"但没有一致的 verify 记录 |
| `read-after-test-fail.json` | `read-after-test-fail` | 最近一次 verify 失败后,没读过文件就直接改 |
| `keep-pushing.json` | `keep-pushing` | 没说明卡点就表达暂停时,提醒继续或说明阻塞 |
| `long-term.json` | `no-todo-marker`、`no-hardcoded-list`、`no-patch-intent`、`no-git-force-skip`、`no-hack-commit-message` | 倾向长期解法:不留桩、不硬编码特例、不强推绕过保护 |

`examples/` 里还有个人 / 领域规则的样例(`chinese_plain.json`:要求直白的中文汇报;`testset.json`:防止评测集泄漏进训练逻辑),不属于默认包。

## 规则 DSL 简介

一条规则由这些部分组成:

- `event`:`PreToolUse`、`UserPromptSubmit`、`PostToolUse`、`PostToolUseFailure`、`Stop`、`SubagentStop` 之一。
- `match`:匹配器——`tool`、`path`、`input_regex`、`bash` / `ps`(命令节点)、`text`(Stop / 提示词里的文本)、`agent_type`、`run_in_background`。字段之间是 AND,字段内的列表是 OR。
- `when`:谓词数组(AND),支持 `all` / `any` / `not`,例如 `read_this_session`、`verify_fresh`、`verify_failed_since_read`、`prior_hit`、`branch_glob`。
- `action` / `target` / `message` / `inject` / `examples` / `clients`。

几条核心约定:

- **三值逻辑,fail-open**:谓词求值得到"不确定"(比如动态参数、不在 git 仓库、验证还在跑)时,规则**不命中**,只记诊断。
- 正则统一用 RE2;`lint` 在运行前就拒绝不适用的字段、矩阵外的动作和无法编译的正则。
- 命令不是按字符串匹配,而是先解析成命令节点(bash 用 mvdan AST,PowerShell 做保守分词),所以 `git -C ../x commit` 这类写法也能识别。
- 同一事件多条规则同时命中时,取可用档位里最严的一档,所有命中都写审计。
- 三层合并:org 整体不可变;repo 与 user 同 id 时 repo 整个对象胜出;user 只能新增不同 id 的规则。

完整语义见 [`docs/dsl-semantics.md`](docs/dsl-semantics.md),JSON Schema 见 [`schema/rules.v1.json`](schema/rules.v1.json)。

## 影子模式与 promote

新规则默认 `action: "log"`:不阻止任何事,只把本来会触发的档位记成 `would_<target>`。`target` 缺省时取该事件上允许的最严一档。推荐的节奏:

1. 以 `log` 运行一段时间(客户端照常使用);
2. `ballast stats` 看命中次数,`ballast stats --rule <id> --sample 20` 抽样复核;
3. 确认没有误伤后升级:

```sh
ballast rules promote no-long-sleep --to deny
```

升级有闸门:近 7 天命中(含 would)不少于 5 次,**并且**存在比规则最近一次修改更新的复核凭据(即上一步的 `--sample`)。`--force` 可以跳过闸门,但不跳过能力检查,并会写审计。只能升级 repo 或 user 层的规则;org 规则不可由本机升级;目标档位必须被已安装的客户端支持。

## stats 与 replay

```sh
ballast stats                          # 近 7 天,按规则汇总:evals / hits / would / unknown / sessions
ballast stats --since 24h --by session # 也可以按 session 或 repo 汇总
ballast stats --rule no-long-sleep --sample 20   # 抽样并写入复核凭据
ballast stats --json
ballast audit lint                     # 检查审计日志格式是否合规
```

审计日志在 `~/.ballast/audit/YYYY-MM.jsonl`,自动轮转并有 GC。敏感内容在写入前做最小脱敏。

`replay` 用历史 transcript **离线**评估规则,不需要重新跑 agent:

```sh
ballast replay path/to/transcript.jsonl --client claude-code --format md
ballast replay path/to/transcript.jsonl --rules .ballast/rules.json --subagents
```

每次评估被分成 `known-hit`、`known-miss`、`approximate`、`unknown` 四类。`verify_fresh` 一类依赖证据的谓词,只在能找到可信的证据记录(`~/.ballast/evidence` 或 `--evidence <文件>`)时才给出确定结论;transcript 里出现过一次 `ballast verify` 调用**不算**证据。分类按实际求值路径:规则的 `match` 本身就不命中的求值记为 `known-miss`;`path_exists` 只有在不得不读当前磁盘时才是 `approximate`,能由 transcript 里的读写还原时仍是精确的。

## git pre-commit 模板

[`hooks/pre-commit`](hooks/pre-commit) 只调用 `ballast verify --check`,不会在提交时重跑测试:

```sh
cp hooks/pre-commit .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit
```

| `--check` 退出码 | 含义 | 模板的处理 |
|---|---|---|
| 0 | 新鲜 | 放行 |
| 10 / 11 / 13 | 过期 / 无证据 / 同名验证仍在运行 | 拒绝提交,并提示该做什么 |
| 12 或其他 | 无法判定(非 git、无 `project.json`、快照失败等) | 提示后放行,不让配置缺失卡住提交 |

git 在提交阶段会把 `GIT_INDEX_FILE` 指向真正要提交的索引,`commit -a` 与带路径提交也因此判得正确。这只是本地辅助闸门,`--no-verify` 可以绕过;**最终仍以服务端 CI 为准**。

## 命令一览

| 命令 | 说明 |
|---|---|
| `ballast hook --client <id> --event <原生事件>` | 客户端调用的 hook 入口;只用退出码 0 / 1,异常时标准输出为空 |
| `ballast lint [--client all] [--examples] <文件\|目录>...` | 检查规则文件(schema、客户端能力矩阵、注入预算) |
| `ballast test-rules <文件\|目录>...` | 运行规则内的 examples |
| `ballast config show [--explain]` | 显示当前生效的 org / repo / user 三层规则 |
| `ballast init` | 探测技术栈,写入 `.ballast/project.json` |
| `ballast verify` / `--status` / `--check` / `--foreground` | 后台运行验证、查询结果、检查新鲜度。[实验] 仅在已部署 T1 的主机上,agent 运行的规范 `ballast verify <name>` 才由 hook 在沙箱外代起(hook 给出 job id,`--status <job id>` 查询);T0 主机上不拦截,原样后台运行 |
| `ballast stats` / `ballast rules promote <id> --to deny` | 命中统计、抽样复核、升级动作档位 |
| `ballast replay <transcript>` | 对历史 transcript 离线回放规则 |
| `ballast audit lint` | 检查审计日志格式 |
| `ballast report --out <文件> [--since 30d] [--force]` | 把审计日志脱敏聚合成一个**本地**文件(只含计数、枚举与分桶数值,不含 id、路径、命令文本;非内置规则 id 只输出哈希);默认不运行,不联网,不发送任何数据;分享前请自行检查 |
| `ballast install --client <id>` / `uninstall` / `doctor [--t1]` | 安装、卸载、自检(各客户端状态;T0 为默认模式)。`doctor --t1` [实验] 额外输出 T1 的全部 `tier.*` 检查,已声明 T1 或主机已锁定时自动输出 |
| `ballast org render [--part managed-settings\|org-json] [--home-root <目录>] [--write --i-am-admin]` | **[实验]** 仅服务于 T1:stdout 恒为单个合法 JSON,缺省输出组织托管设置模板,`--part org-json` 输出 `org.json`;个人用户无需使用,见 [`docs/advanced-t1.md`](docs/advanced-t1.md) |

退出码约定见 [`docs/architecture.md` §7](docs/architecture.md)。

## 已知局限

- **T0 只能检测篡改,不能防止**(`scripts/boundary-t0.sh` 是这条边界的可执行登记表:被检测的写法必须命中,登记的缺口一旦被检测到脚本就失败,提示同步 trust-model)。这是产品的默认保证与有意的取舍:目标用户是个人开发者,agent 不会故意改自己的 hook;需要托管强制的团队合规场景见实验特性 [`docs/advanced-t1.md`](docs/advanced-t1.md)(其证据保证是全机范围的、真实模型逃逸测试仍是【需凭据】,见 [`docs/trust-model.md`](docs/trust-model.md) §2.3)。
- **Codex、Cursor 的 payload 字段与输出协议未经 Ballast 自己取证**(M0a 只覆盖 Claude Code),安装后的行为以 `ballast doctor` 与 `ballast lint` 的警告为准。Hermes 做过离线核验(`hermes hooks test`:形状、解析、阻断语义),**真实 agent 的触发时机未核验**。Codex 的 hook 只在交互式 TUI 触发;Cursor 的 Stop 与 Hermes 的 `pre_verify` 都只能续跑;Hermes 只有 `pre_tool_call` 能拦截。
- **PowerShell 只做保守分词**,覆盖面窄于 bash;无法分词时按"不确定"处理(放行并写审计)。
- **`git add … && git commit …` 写在同一条命令里**会被判为证据过期(Ballast 不模拟同一命令里前序节点的副作用)。请分两次执行。
- **新鲜度只看已跟踪文件**。新建的文件要先 `git add` 再 `verify`;测试依赖未跟踪文件的情况交给 CI。
- **shell 的读写目标只覆盖列出的常见命令**,`no-tamper` 因此只能检测、不能保证。会被识别的写入:重定向、`tee`、`cp`/`mv`/`install`/`rsync` 的目标、`sed -i`、`perl -i`/`ruby -i`、`git checkout [<rev>] -- <path>`、`git restore <path>`、`rm`、`truncate`、`dd of=`、`touch`、`ln`。**未覆盖**(尽力列举,完整清单见 [`docs/trust-model.md`](docs/trust-model.md) §2.1):语言运行时内部的写入(`python -c "open(...).write()"`、`node -e`、`php -r`、不带 `-i` 的 `perl -e`/`ruby -e`、`gawk -i inplace`、`yq -i`);先写脚本再执行或经管道交给解释器;`patch`、`git apply`、`ed`/`vim -es`、`curl -o`/`wget -O`、`tar -x`/`unzip`、`find -delete`/`-exec`、`xargs` 间接调用;`git reset --hard`、`git stash`、`git switch`/`git checkout <分支>`、`git merge`/`pull`、不带 `--` 的 `git checkout <path>`;命令词是变量或 `eval`。`find … | xargs cat` 之类的读取不计入"已读",只会让 `read-before-edit` 更严,不会更松。
- **`git -c core.hooksPath=<路径> commit` 能绕过 git hooks**(包括 `hooks/pre-commit` 模板),默认的 `no-skip-verify` 只匹配 `--no-verify` / `-n`,不拦它:DSL 里 `flags` 与 `args_regex` 是 AND,无法在不误报提交说明的前提下并入同一条规则,理由见 [`docs/pinrule-mapping.md`](docs/pinrule-mapping.md) §2.6。`evidence-before-commit` 不受影响:提交前仍要有一致的 `ballast verify` 记录,与 hooks 是否运行无关。
- **`no-tamper` 对重定向的客户端配置根:只认 hook 进程环境里的绝对路径值**:引擎在编译 `~/.claude/…`、`~/.codex/…`、`~/.hermes/…` 这三类路径模式时,会同时按 `CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME`(及其符号链接解析后的形式)展开,与字面形式并存,所以重定向后的 `settings*.json`、`hooks.json`、`config.toml`、`config.yaml` 同样受检测。覆盖不到的(trust-model §2.1 第 8 类缺口,`scripts/boundary/t0-cases.json` 有对应的 gap 行):变量值是相对路径(客户端相对它自己的 cwd 解析,hook 无从得知)、hook 进程环境里没有该变量而客户端加载时有、`ballast replay` 离线重放(不带这些变量)。见 [`docs/trust-model.md`](docs/trust-model.md) §2.1 与 [`docs/dsl-semantics.md`](docs/dsl-semantics.md) 的路径宏一节。
- **`no-tamper` 不拦"搬走"**:`mv ~/.ballast/rules.json /tmp/x` 的源路径不算写入目标(只有目标算),所以不命中;这是 T0 只检测不防止的一个具体表现,见 [`docs/trust-model.md`](docs/trust-model.md)。
- `flags` 匹配不知道哪些选项带值(`git commit -m -n` 会把 `-n` 当作标志)。
- Codex 上字符串形式的 `apply_patch <<EOF` 仍被当作 shell 命令而不是文件编辑。
- 磁盘写满会使强制失效;审计与 `doctor` 会告警。
- 规则缓存(`~/.ballast/cache/rules-*.json`)由每天一次的 `ballast gc`(SessionStart 派生的后台进程)回收超过 7 天且不再被引用的文件,也可以手动删除,下次加载会自动重建。`gc.stamp` 只由 hook 在派生 gc 前更新,`ballast gc` 自己不写它。同时运行的多个 `ballast gc` 由 `~/.ballast/gc.lock` 串行(抢不到锁的直接退出 0),每个过期会话的残余计数只上报一次。

## 开发与测试

```sh
gofmt -l ./internal ./cmd ./scripts   # 不应有输出
go build ./...
go vet ./...
GOOS=windows go vet ./...
GOOS=linux go vet ./...
GOPROXY=off go test -race ./...
```

- **不联网、不碰真实配置**:所有触及文件系统的测试都使用 `internal/testenv` 的沙箱,把 `HOME`、`BALLAST_HOME`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME`、`BALLAST_INSTALL_ROOT`、`BALLAST_MANAGED_DIR`、`GIT_CONFIG_GLOBAL` 等全部重定向到临时目录;不会读写真实的 `~/.claude`、`~/.codex`、`~/.cursor`、`~/.hermes`、`~/.ballast`,也不会运行真实的客户端。`internal/cli` 的端到端测试会把 `ballast` 构建到临时目录并以子进程运行。
- **P2 验收**:`bash scripts/accept-p2.sh` 编排 P2 的集成验收(构建与 vet、archtest、`go test ./...`、redact 60 秒 fuzz、`report` 端到端、`boundary-t0.sh`、Docker 下的实验特性 T1 的 `sandbox-docker.sh` 与 `boundary-t1.sh`、M0b 取证检查、bench 场景 12–14 与 ConfigChange、卫生检查)。缺 Docker 时相应步骤显式 SKIP,`BALLAST_REQUIRE_DOCKER=1` 时失败;需要真实模型的步骤只打印【需凭据】与补测命令(`ANTHROPIC_API_KEY=<你自己提供> bash scripts/boundary-t1.sh --with-credentials`,密钥只从环境读、以变量名传给 `docker run`,不写入任何文件)。
- **架构检查**:`internal/archtest` 强制依赖白名单——热路径(hook)不得依赖 `net/*`,`os/exec` 只允许出现在契约指定的隔离文件里,包级变量不得 `regexp.MustCompile`,不得有重的 `init()`。
- **新增第三方依赖需要先走契约变更流程**(`docs/architecture.md` §12)。
- **CI**:`.github/workflows/ci.yml` 在 ubuntu、macos、windows 三个平台上运行 `go vet`、`go test -race`(Windows 缺 cgo 时退回不带 `-race`)、`internal/archtest`、验收脚本 `accept-m0` 到 `accept-m4`、`accept-p2` 与 bench 门槛;另有 `Linux T1 沙箱(ubuntu-22.04)` job(实验特性 T1 的真实 bwrap 验收)和 `matrix-real` job(真实工具链上的多语言证据矩阵)。P2 的步骤已接入 CI,见 [`scripts/README.md`](scripts/README.md)。
- **bench 门槛**:产品门槛是每次 hook 调用 p95 不超过 **30ms**,**Linux CI 强制 30ms**。托管 macOS 与 Windows runner 上 CI 使用**临时的平台上限**(`BALLAST_BENCH_P95_MS`:macOS 45ms、Windows 150ms;遍历整个工作区的场景另有 `BALLAST_BENCH_P95_HEAVY_MS`:macOS 80ms、Windows 400ms),这些上限只防数量级退化,**不是性能目标,也意味着这两个平台尚未达到 30ms 产品门槛**:只要任一场景 p95 超过 30ms,bench 的结论就写作"通过平台上限,但 N 个场景未达 30ms 产品门槛"并列出场景。依据、实测数据与这是否被接受的说明见 [`scripts/bench/README.md`](scripts/bench/README.md)。开发机上直接运行 `scripts/bench.sh` 始终强制 30ms。
- 契约与取证资料:[`docs/architecture.md`](docs/architecture.md)、[`docs/dsl-semantics.md`](docs/dsl-semantics.md)、[`docs/facts/`](docs/facts/)、[`docs/research/pinrule-install-and-clients.md`](docs/research/pinrule-install-and-clients.md)、[`docs/pinrule-mapping.md`](docs/pinrule-mapping.md)。

## 许可

MIT,见 [`LICENSE`](LICENSE)。
