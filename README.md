# Ballast(压舱石)

Ballast 是给 AI 编码客户端用的**行为策略引擎**:把一小组关键规则持续注入会话,在工具调用前做强制检查,并把每一次命中留成可审计的记录。

- **声明式规则**:规则是 JSON,不写代码就能新增拦截;规则语言没有 exec 逃生口,总共不超过 12 条同时生效。
- **四档动作** `log < warn < ask < deny`:新规则默认是影子模式(只记 `log`),攒够证据再升级。
- **证据只认事实**:"测试通过"只以 `ballast verify` 记录的退出码和内容快照为准,不解析测试输出。
- **三层配置** org > repo > user,热更新,配置出错时沿用上一份合法配置。
- **热路径很轻**:每次 hook 调用 p95 不超过 30ms,不联网、不调 LLM,任何异常都放行(stdout 为空、退出码 1)。
- **可审计、可回放**:`stats` 看命中,`replay` 用历史 transcript 离线评估规则。

> 状态:接口契约 v1.3 已冻结,U1 到 U12 的实现已合入。设计依据见 [`docs/ballast-plan.md`](docs/ballast-plan.md)(v0.4,末尾"v0.4 变更"优先),接口契约见 [`docs/architecture.md`](docs/architecture.md)。

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

先说清楚 Ballast **不能**做什么,再谈它能做什么。

**CI 是最终裁判。** 会话内的规则只负责引导 agent 的行为、减少无效往返;它们不保证交付质量。`ballast doctor` 每次都会重申这一点。真正决定一份改动能不能合入的,应该是服务端 CI。

Ballast 把保证强度分成两级(详见 [`docs/trust-model.md`](docs/trust-model.md)):

| 层级 | 适用条件 | 能兑现的保证 |
|---|---|---|
| **T1 托管强制** | macOS / Linux / WSL2,且组织用托管设置部署了 Ballast | 设计目标是 agent 不可写规则、状态、证据和二进制。**这套部署尚未在真实托管环境核验(待 M0b)** |
| **T0 尽力而为** | 个人安装、没有托管部署,或原生 Windows | **篡改可检测、不可防**:agent 与 Ballast 同一用户权限,能改规则和状态文件;`no-tamper` 规则与审计只能让这种行为被发现 |

个人安装默认是 T0。`ballast doctor` 会输出当前层级和缺失的 T1 组件;想要 T1,让管理员用 `ballast org render` 生成托管设置模板自行下发。

其他边界:

- hook 一旦把决策写到 stdout,决策即生效;之后进程即使崩溃也不撤销。
- 磁盘写满会使强制失效(hook 按"任何异常都放行"设计),审计和 `doctor` 会告警。
- "新鲜证据"只看**已跟踪文件**的内容快照:测试依赖了未跟踪文件时,本地可能判为新鲜而 CI 失败,这类情况交给 CI 兜底。

## 支持的客户端

| 级别 | 客户端 | 支持程度 |
|---|---|---|
| 一级 | **Claude Code** | 完整支持。注入、拦截(deny / ask / warn)、Stop 干预均有真实 payload 取证(Claude Code 2.1.300) |
| 二级 | **Codex** | 注入与 PreToolUse deny;**不支持 ask**;Stop 的 block 待验证;hook 仅在交互式 TUI 触发。全部行为**未经 Ballast 自己核验** |
| 二级 | **Cursor** | 注入、PreToolUse deny / ask;Stop 只能"续跑"(`followup_message`);另生成托管的 `ballast.mdc` 规则文件兜底。全部行为**未经 Ballast 自己核验** |
| 三级 | **Hermes** | 尽力支持:`pre_tool_call` deny、`pre_llm_call` 注入,其余只能记 `log`。全部行为**未经 Ballast 自己核验** |

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
ballast doctor                                    # 自检:层级、各客户端状态、规则 lint
```

每个客户端的写入位置:

| 客户端 | 写入的文件 | 说明 |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | 遵循 `CLAUDE_CONFIG_DIR`。每个事件一组 `{type, command, args}`,命令与参数分开写,路径含空格也不需要拼接 |
| Codex | `~/.codex/hooks.json`、`~/.codex/config.toml` | 遵循 `CODEX_HOME`。`config.toml` 里只确保 `[features]` 下 `hooks = true`。**trust 哈希默认不写**,需要时显式加 `--codex-trust`;不加则要在 Codex TUI 里用 `/hooks` 批准 |
| Cursor | `~/.cursor/hooks.json`、`~/.cursor/rules/ballast.mdc` | `ballast.mdc` 是整文件托管的 `alwaysApply` 规则,作为注入的可靠通道;安装后需要 Reload Window。规则变更后重新运行 `ballast install --client cursor` 刷新它 |
| Hermes | `~/.hermes/config.yaml` | 遵循 `HERMES_HOME`。只改顶层 `hooks:` 段,段外字节不变;`hooks:` 用 flow 风格或锚点时拒绝写入。启动 Hermes 时需要 `--accept-hooks` |

安全措施:

- **备份**:每个将被改写的文件在写入前都会备份到 `~/.ballast/backups/<UTC 时间>-<随机串>/`(目录 0700、文件 0600),只保留最近 10 份。
- **manifest**:`~/.ballast/install-manifest.json` 记录 Ballast 写入的每个条目及其哈希。归属判定同时要求"命令前缀匹配"和"哈希与 manifest 一致"——你手工改过的条目会被保留并警告,绝不按子串去猜。
- **卸载以 manifest 为准**:即使客户端已经被卸载也能清理;删除 Ballast 新建且现已为空的文件和目录;配置解析失败时返回非 0,不会静默成功。
- **拒绝而不是硬写**:配置文件无法解析时退出码 3 且不写任何内容;组织已通过托管设置部署 Ballast 时,拒绝再做用户层安装(避免 hook 重复执行)。
- 不会在 `init` 时自动安装,不绑定任何解释器,不写 `async` / `onFailure`。

> 安装只写 hook,**不会替你创建规则文件**。规则见下面的"快速上手"。

## 快速上手

### 1. 安装 hook

```sh
ballast install --client claude-code
ballast doctor
```

### 2. 放入规则

把默认规则复制为你的用户层规则(对所有仓库生效),或放进某个仓库的 `.ballast/rules.json`(只对该仓库生效):

```sh
mkdir -p ~/.ballast && cp rules/default.json ~/.ballast/rules.json
ballast lint --client all rules/default.json   # 检查规则
ballast test-rules rules/default.json          # 运行规则内的 examples
ballast config show --explain                  # 查看当前生效的 org / repo / user 三层规则
```

规则文件变更会热更新,不需要重启客户端。

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

`verify` 总是后台运行。任务结束后,Ballast 会在下一次注入里附上一行结果。`evidence-before-commit` 规则据此判断"提交的内容是否真的被验证过"。完整退出码见 [`docs/architecture.md` §7](docs/architecture.md)。

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
| `no-tamper` | PreToolUse | 不得改写 Ballast 的规则、状态、证据,或客户端的 hook 配置(文件工具和 shell 写入都覆盖) |

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
| `ballast verify` / `--status` / `--check` / `--foreground` | 后台运行验证、查询结果、检查新鲜度 |
| `ballast stats` / `ballast rules promote <id> --to deny` | 命中统计、抽样复核、升级动作档位 |
| `ballast replay <transcript>` | 对历史 transcript 离线回放规则 |
| `ballast audit lint` | 检查审计日志格式 |
| `ballast install --client <id>` / `uninstall` / `doctor` | 安装、卸载、自检(层级 T0 / T1 与各客户端状态) |
| `ballast org render` | 输出组织托管设置模板(T1,未核验) |

退出码约定见 [`docs/architecture.md` §7](docs/architecture.md)。

## 已知局限

- **T0 只能检测篡改,不能防止**;T1 的部署方案尚未在真实托管环境核验(待 M0b),见 [`docs/trust-model.md`](docs/trust-model.md)。
- **Codex、Cursor、Hermes 的 payload 字段与输出协议未经 Ballast 自己取证**(M0a 只覆盖 Claude Code),安装后的行为以 `ballast doctor` 与 `ballast lint` 的警告为准。Codex 的 hook 只在交互式 TUI 触发;Cursor 的 Stop 只能续跑;Hermes 只有 `pre_tool_call` 能拦截。
- **PowerShell 只做保守分词**,覆盖面窄于 bash;无法分词时按"不确定"处理(放行并写审计)。
- **`git add … && git commit …` 写在同一条命令里**会被判为证据过期(Ballast 不模拟同一命令里前序节点的副作用)。请分两次执行。
- **新鲜度只看已跟踪文件**。新建的文件要先 `git add` 再 `verify`;测试依赖未跟踪文件的情况交给 CI。
- **shell 的读写目标只覆盖列出的常见命令**。`python -c "open(...).write()"` 这类语言内写入不会被识别;`find … | xargs cat` 之类的读取不计入"已读"。后者只会让 `read-before-edit` 更严,不会更松。
- `flags` 匹配不知道哪些选项带值(`git commit -m -n` 会把 `-n` 当作标志)。
- Codex 上字符串形式的 `apply_patch <<EOF` 仍被当作 shell 命令而不是文件编辑。
- 磁盘写满会使强制失效;审计与 `doctor` 会告警。
- 升级前留下的规则缓存(`~/.ballast/cache/rules-*.json`)不会自动回收,可以手动删除,下次加载会自动重建。

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
- **架构检查**:`internal/archtest` 强制依赖白名单——热路径(hook)不得依赖 `net/*`,`os/exec` 只允许出现在契约指定的隔离文件里,包级变量不得 `regexp.MustCompile`,不得有重的 `init()`。
- **新增第三方依赖需要先走契约变更流程**(`docs/architecture.md` §12)。
- **CI**:`.github/workflows/ci.yml` 在 ubuntu、macos、windows 三个平台上运行 `go vet`、`go test -race`(Windows 缺 cgo 时退回不带 `-race`)、`internal/archtest`、验收脚本和 bench 门槛(p95 不超过 30ms),另有 `matrix-real` job 在真实工具链上跑多语言证据矩阵。
- 契约与取证资料:[`docs/architecture.md`](docs/architecture.md)、[`docs/dsl-semantics.md`](docs/dsl-semantics.md)、[`docs/facts/`](docs/facts/)、[`docs/research/pinrule-install-and-clients.md`](docs/research/pinrule-install-and-clients.md)、[`docs/pinrule-mapping.md`](docs/pinrule-mapping.md)。

## 许可

MIT,见 [`LICENSE`](LICENSE)。
