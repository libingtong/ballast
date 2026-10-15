# Ballast 规则 DSL 语义(v1,冻结)

> 状态:**已冻结**(2026-10-16;v1.1 与 v1.3 修订见文末"修订记录"),与 `schema/rules.v1.json`(JSON Schema draft 2020-12)、`internal/model/rule.go` 一一对应。计划依据:`docs/ballast-plan.md` B1、B2、B4、B5、B11 与 v0.4 变更。
>
> JSON Schema 只能表达结构;本文规定的语义检查(RE2 编译、glob 语法、字段适用性、客户端能力矩阵、跨文件预算、examples 执行)由 `ballast lint` / `ballast test-rules`(`internal/dsl`,U3)完成。所有默认规则、可选规则、示例与 `schema/examples/*` 都必须通过同一个解析器与 lint。
>
> 本文中的 JSON 代码块(语言标记为 `json` 的)都是完整、可被严格解码的规则文件;`internal/model` 的测试会逐个解码它们。

---

## 1. 设计原则

1. **声明式、封闭词表**:没有 exec 逃生口(R04)。新能力只能通过在引擎中新增谓词(代码评审后发布)或在离线 `replay` 中评估。
2. **三值逻辑,fail-open**:每个匹配器与谓词返回 true / false / unknown;规则结果为 unknown 时**不命中**,只累加 unknown 计数并写诊断。
3. **字段缺失 = false**:payload 中缺失的字段使对应匹配为 false,不报错(B1)。"无法确定"(动态参数、解析失败、证据进行中)才是 unknown。
4. **lint 先行**:不适用的字段、矩阵外的动作、不合法的正则都在 lint 阶段报错,运行期不静默失效。
5. **正则统一 RE2**(Go `regexp`):不支持反向引用与前后查找;lint 拒绝无法编译的正则。
6. **长度预算以 Unicode 码点(rune)计**。

---

## 2. 文件格式

```
RuleFile := {
  "$schema"?:  string,                 // 可选,通常 "../schema/rules.v1.json"
  "version":   1,                      // 必填,只接受 1
  "settings"?: { "inject"?: { "anchor_every"?: int≥0, "batch_after_tools"?: int≥0 } },
  "rules":     [ Rule, ... ]
}
```

- 未知字段一律报错(严格解码)。
- `settings` 按键合并,层级优先 org > repo > user(第一个设置该键的层生效)。`anchor_every` 默认 1(0 = 关闭 UserPromptSubmit 锚点);`batch_after_tools` 默认 25(0 = 关闭回合中途补锚点)。

三层位置(B4):org `<托管目录>/ballast/rules.json`;repo `<repo>/.ballast/rules.json`;user `~/.ballast/rules.json`。合并语义见 §10.2。

---

## 3. 规则字段

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `id` | string | 必填 | `^[a-z0-9-]{3,40}$`;同一文件内唯一;跨层同 id 见 §10.2 |
| `description` | string ≤500 | — | 仅文档用途,不参与 generation |
| `enabled` | bool | `true` | `false` 的规则不加载、不计入 12 条上限 |
| `action` | `log\|warn\|ask\|deny` | `log` | 新规则默认影子模式(B2) |
| `target` | 同上 | 该事件在 Claude Code 上允许的最严档(`model.DefaultTarget`):PreToolUse/UserPromptSubmit/Stop/SubagentStop → `deny`;PostToolUse/PostToolUseFailure → `warn` | 影子模式下的目标档;log 命中记为 `would_<target>`。`target` 比 `action` 弱时 lint 警告 `target-weaker-than-action` |
| `event` | 见 §4 | 必填 | 单个事件 |
| `clients` | `["claude-code","codex","cursor","hermes"]` 子集 | 全部 | 规则只在这些客户端上求值;lint 只对其中已安装/指定的客户端做能力检查 |
| `match` | Match | 空 = 恒真 | 字段之间 AND,字段内列表 OR(§5) |
| `when` | `[Cond]` | 空 = 恒真 | 数组为 AND;支持 `all`/`any`/`not`(§7、§8) |
| `message` | string ≤300 | — | deny/ask/warn 时展示给 Agent,格式化为 `[<id>] <message>` |
| `inject` | string ≤400 或 `{"text"≤400, "anchor"?≤80}` | — | SessionStart 全文注入;`anchor` 缺省取 `text` 第一行截断到 80 字 |
| `examples` | `[Example]` | 必填 | 至少 1 个 `hit` 和 1 个非命中(`miss` 或 `unknown`),见 §12 |

一条规则若既没有 `match` 也没有 `when`,lint 报 `empty-rule`(错误)。

---

## 4. 事件取值与客户端映射

`event` 只接受 6 个归一事件。各客户端原生事件到归一事件的映射(安装器按此注册,`model.NativeEventTable`):

| 规则 `event` | Claude Code | Codex | Cursor | Hermes |
|---|---|---|---|---|
| `PreToolUse` | PreToolUse | PreToolUse、PermissionRequest | preToolUse、beforeShellExecution(工具=Bash)、beforeMCPExecution(工具=`mcp__<server>__<tool>`)、beforeReadFile(工具=Read) | pre_tool_call |
| `UserPromptSubmit` | UserPromptSubmit | UserPromptSubmit | beforeSubmitPrompt | pre_llm_call |
| `PostToolUse` | PostToolUse | PostToolUse | postToolUse | post_tool_call |
| `PostToolUseFailure` | PostToolUseFailure | —(不存在) | — | — |
| `Stop` | Stop | Stop | stop | —(on_session_end 归一为 SessionEnd,不能干预) |
| `SubagentStop` | SubagentStop | — | subagentStop | — |

- 事件在某客户端不存在时,该规则在该客户端上永不触发;lint 给 `event-unavailable` 警告。
- 动作能否在某客户端的某事件上执行,见 `docs/architecture.md` §4.1;lint 对矩阵外组合报 `action-unsupported`(错误),对未核验组合报 `action-unverified`(警告);Cursor 的 Stop/SubagentStop `deny` 报 `cursor-stop-followup`(警告,语义为续跑)。
- Claude 压缩用内部 agent 触发的 SubagentStop(`agent_type` 存在且为空字符串)不参与规则求值。
- SessionStart、PostToolBatch、SessionEnd 等事件只用于注入与记账,不能写在规则里。

---

## 5. 匹配器(`match`)

`match` 中出现的字段之间是 **AND**(Kleene 与),字段内部的列表是 **OR**。唯一例外是 `bash` 与 `ps`:二者按工具的 shell 种类**分派**(§5.4)。

### 5.1 `tool`:工具名 glob

- 对**归一化后的规范名**(`Bash`、`PowerShell`、`Edit`、`MultiEdit`、`Write`、`NotebookEdit`、`Read`、`Agent`、`mcp__server__tool` …)**或**原生名(`apply_patch`、`exec_command`、`Shell`、`terminal` …)做 glob,任一匹配即可。
- glob 方言:`doublestar.Match` 直接匹配(**在所有平台上都区分大小写**,不经 `PathService`,不做 `~/` 展开);支持 `*`、`**`、`{a,b}`、`?`、`[…]`;取反规则与 §5.2 相同(列表层面的 `!`)。`mcp__*` 匹配所有 MCP 工具。
- 同一方言("名称 glob")也用于 `bash.cmd`/`ps.cmd`、`pipe_from`、`pipe_to`、`agent_type` 与谓词 `branch_glob`。只有 `path`、`writes` 与路径谓词的路径参数走 §5.2 的路径 glob(规范化、候选、`~/` 展开、按平台折叠大小写)。
- 规则有 `bash`/`ps` 而没有 `tool` 时,隐含 `tool: ["Bash","PowerShell"]`。
- 非工具事件上写 `tool` → lint `field-not-applicable`。

### 5.2 `path`:目标路径 glob 列表

**目标(target)来源**:

| 工具 | 目标 |
|---|---|
| Edit / Write / Read | `file_path` |
| MultiEdit | `file_path` |
| NotebookEdit | `notebook_path` |
| apply_patch(归一为 Edit) | 补丁里的**全部**目标(Update/Add/Delete/Move to) |
| Bash / PowerShell | 解析出的**写入目标**(`CommandNode.Writes`,见 §6) |
| 其他 | 无 |

多目标时"任一命中"(每个目标形成一个绑定,§9)。

**候选字符串**(`ports.PathService.Candidates`):对每个目标,依次生成

1. 规范化绝对路径(内部形式:`/` 分隔、盘符小写如 `c:/Users/x`、UNC 为 `//server/share/…`、Git Bash 的 `/c/Users` 转 `c:/Users`、`~` 展开、`.`/`..` 消解);
2. 相对项目根(git toplevel,不在仓库时为 cwd)的相对路径(仅当目标在项目根内);
3. 上述两种形式在 `EvalSymlinks` 解析后的版本(不存在的文件解析其最近的已存在父目录;覆盖符号链接与 Windows 目录联接);
4. (v1.3,仅 POSIX)目标含 `..` 时,再加一个**物理** `..` 形式:`..` 作用于已到达的真实目录(先解析符号链接),与内核和 shell 的语义一致,`link/../x` 落在 link 目标的父目录下。第 1 项的词法折叠形式保留。Windows(Win32 按词法折叠 `..`)以及在 POSIX 主机上出现的 Windows 客户端盘符路径(`c:/x/../y`)只有词法形式。

去重后,只要任一候选满足 glob 列表即算匹配。

**glob 方言**(`github.com/bmatcuk/doublestar/v4`):

- `*` 不跨 `/`;`**` 跨任意层;`{a,b}` 选择;`?`、`[…]` 同 doublestar。
- 不含 `/` 的模式匹配任意层级的文件名:`*.md` ≡ `**/*.md`。
- 以 `~/` 开头的模式先用家目录展开再匹配(`~/.ballast/**`)。
- **取反只能在列表层面表达**:列表中以 `!` 开头的项是排除模式。列表匹配 ⇔ **存在**某候选匹配至少一个正模式,**且所有**候选都不匹配任何排除模式(顺序无关)。只有排除模式的列表非法(lint `glob-only-negation`)。
- 大小写:Windows 与 macOS 上模式与候选都折叠为小写再比较;Linux 区分大小写。

**动态目标**:shell 写入目标为动态(`> "$F"`)时,该候选的匹配结果为 unknown。

### 5.3 `input_regex`:工具输入字段正则

```text
"input_regex": {"<字段路径>": "<RE2>" | ["<RE2>", ...]}
```

- 键之间 AND;每个键的正则列表 OR;字段有多个值(数组展开)时任一值匹配即可。
- 字段路径 = `tool_input` 中字符串叶子的点分路径,数组下标省略(`edits.new_string`)。字段缺失 → false。
- **虚拟字段**:`new_text` / `old_text` 读取归一化后的 `ToolCall.Edits[].NewString` / `OldString`,对 Edit、MultiEdit、Write(`content`)、NotebookEdit(`new_source`)与 apply_patch(增删行)统一可用。
- **字段登记表**(`dsl.InputFields`,lint 用):

| 工具 | 可用字段 |
|---|---|
| Bash / PowerShell | `command`, `description`, `timeout`, `run_in_background` |
| Edit | `file_path`, `old_string`, `new_string`, `replace_all`, `new_text`, `old_text` |
| MultiEdit | `file_path`, `edits.old_string`, `edits.new_string`, `new_text`, `old_text` |
| Write | `file_path`, `content`, `new_text`, `old_text` |
| NotebookEdit | `notebook_path`, `new_source`, `cell_id`, `cell_type`, `edit_mode`, `new_text`, `old_text` |
| Read | `file_path`, `offset`, `limit` |
| Grep | `pattern`, `path`, `glob`, `type`, `output_mode` |
| Glob | `pattern`, `path` |
| WebFetch | `url`, `prompt` |
| WebSearch | `query` |
| Agent | `prompt`, `description`, `subagent_type` |
| `mcp__*` 及未登记工具 | 任意键 |

  键必须对规则 `tool` 列表可能匹配的**每一个**已登记工具都有效,否则 lint `input-field-unknown`(错误)。例如对 `Edit` 写 `input_regex.command` 直接报错。没有 `tool` 时不能使用 `input_regex`(lint `field-not-applicable`)。

### 5.4 `bash` / `ps`:命令节点匹配

**分派**:工具的 shell 种类为 bash(Bash 及其归一来源)时只用 `bash` 匹配器;为 PowerShell 时只用 `ps`。规则可以同时写两者(默认规则的 Bash/PowerShell 双变体就是这样写的);工具的 shell 种类没有对应匹配器时,该字段为 false。

**作用域**:同一个匹配器内的所有条件必须落在**同一个** CommandNode 上("存在一个节点同时满足全部条件",B1)。满足的节点各形成一个绑定(§9)。

| 字段 | 含义(对单个节点) |
|---|---|
| `cmd` | 名称 glob 列表(§5.1 方言,区分大小写),匹配 `CommandNode.Name`(bash:argv[0] 去包装器后的 basename;ps:别名解析后的小写 cmdlet 名或去掉 `.exe` 的可执行名,所以 `ps.cmd` 应写小写)。argv[0] 动态 → unknown |
| `args_regex` | 正则列表,匹配 argv[1:] 的字面值以空格拼接的字符串;含动态词时,若正则不匹配则为 unknown |
| `flags` | 任一标志出现即真。`--long` 精确匹配或匹配 `--long=…`;`-x` 匹配独立的 `-x`,也匹配全字母的合并短选项(`-anm` 含 `-n`)。不识别"哪个选项带值"(已知局限) |
| `git_sub` | `CommandNode.Git.Sub` 在列表中(git 全局参数 `-C/-c/--git-dir/--work-tree/--no-pager` 已跳过);子命令动态 → unknown |
| `sleep_s` | 比较 `CommandNode.Sleep.Seconds`;`Known=false`(动态时长)→ unknown;非 sleep 节点 → false |
| `async` | 节点处于 shell `&` 后台(与工具级 `run_in_background` 无关) |
| `in_loop` | 节点在 for/while/until/foreach 循环内 |
| `loop_bounded` | 最内层循环 `Bounded`(三值;不在循环中 → false) |
| `in_subst` | 节点在 `$(…)`、反引号、进程替换内 |
| `sudo` | 剥离过 sudo |
| `dynamic` | argv 中含动态词 |
| `writes` | glob 列表(语义同 §5.2),匹配该节点的写入目标;动态目标 → unknown |
| `pipe_from` | glob 列表:节点位于管道右侧,且左侧相邻节点的 Name 匹配(`curl … \| sh` 中的 `sh`) |
| `pipe_to` | glob 列表:节点位于管道左侧,且右侧相邻节点的 Name 匹配 |

**解析失败**(引号不闭合等)→ 整个匹配器 unknown。`CommandList.Unknown`(出现 `eval`、PowerShell `iex` 等)时:若有节点肯定匹配则照常命中,否则 unknown。

### 5.5 `text`:自由文本

| 字段 | 说明 |
|---|---|
| `source` | `last_assistant_message`(Stop、SubagentStop 默认)、`prompt`(UserPromptSubmit 默认)、`tool_response`(PostToolUse 默认:对象取 `stdout + "\n" + stderr`,否则紧凑 JSON;字符串原样)、`error`(PostToolUseFailure 默认)。与事件不符 → lint `text-source-mismatch` |
| `preprocess` | 按顺序执行:`strip_code_blocks` 删除 ```` ``` ````/`~~~` 围栏块(未闭合则到文末)与行内 `` `…` ``;`strip_quotes` 删除 `"…"`、`“…”`、`‘…’`、`「…」`、`『…』` 与以 `>` 开头的引用行(引号只在同一行内配对,跨行的引文只能靠 `>` 引用行排除) |
| `window` | `{"head_chars": n}` 取前 n 字,`{"tail_chars": n}` 取后 n 字;两者都有时先 head 后 tail |
| `regex` | 正则列表,任一匹配为真 |
| `not_regex` | 有序豁免列表:任一匹配则整个 `text` 为假(按列表顺序短路求值,结果与顺序无关) |
| `cjk_ratio` | 比较 CJK 字符(汉字、平假名、片假名、谚文)占"CJK + 拉丁字母"的比例;分母为 0(无字母)时 **false** |
| `count_regex` | `{"regex": "...", "gte": n, ...}`:比较不重叠匹配次数 |

`text` 内各子条件之间为 AND,至少要有 `regex`、`not_regex`、`cjk_ratio`、`count_regex` 之一。源文本缺失 → false(Stop 族缺 `last_assistant_message` 时先回退到状态中存下的文本,即 Cursor `afterAgentResponse` 的最后 4000 字;回退也没有文本 → false;状态不可读、无法查询回退 → unknown,原因 `text:state_unreadable`)。字段存在但为空字符串时就是空文本,不是缺失。

### 5.6 `agent_type`

名称 glob 列表(§5.1 方言,区分大小写),匹配 `Event.AgentType`;主会话(无 agent)视为 `"main"`。适用于全部事件。

### 5.7 `run_in_background`

布尔。`true` 要求工具输入 `run_in_background` 为 true;`false` 要求它缺失或为 false。只适用于 Bash/PowerShell。与 `bash.async` 是两个独立字段(B1)。

### 5.8 字段适用性表(lint `field-not-applicable`)

| 字段 | PreToolUse | UserPromptSubmit | PostToolUse | PostToolUseFailure | Stop | SubagentStop |
|---|---|---|---|---|---|---|
| `tool` | ✓ | ✗ | ✓ | ✓ | ✗ | ✗ |
| `path` | ✓(有目标的工具) | ✗ | ✓ | ✓ | ✗ | ✗ |
| `input_regex` | ✓(需 `tool`) | ✗ | ✓ | ✓ | ✗ | ✗ |
| `run_in_background` | ✓(Bash/PowerShell) | ✗ | ✓ | ✓ | ✗ | ✗ |
| `bash` / `ps` | ✓ | ✗ | ✓ | ✓ | ✗ | ✗ |
| `text` | ✗ | ✓ `prompt` | ✓ `tool_response` | ✓ `error` | ✓ `last_assistant_message` | ✓ `last_assistant_message` |
| `agent_type` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| 谓词 `$target` 形式 | ✓(有目标的工具) | ✗ | ✓ | ✓ | ✗ | ✗ |
| 其他谓词 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |

此外:`path` 与非目标工具(`tool` 列表全是无目标工具)组合、`bash`/`ps` 与非 shell 工具组合 → `field-not-applicable`。

---

## 6. 命令解析契约(摘要)

完整契约见 `internal/bashparse/doc.go` 与 `internal/psparse/doc.go`(含期望表)。

- **bash**:`mvdan.cc/sh/v3/syntax`(LangBash)AST 展开为 CommandNode 列表;字面词与动态词区分;剥离包装器 `env VAR=`、`nice`、`nohup`、`timeout N`、`time`、`command`、`exec`、`sudo`(打标记);记录 loop 种类与 bounded、`&` async、`in_subst`、redirects;从 `> >> &> >|`、`tee`、`cp/mv/install/rsync` 目标、`sed -i`、`rm`、`truncate`、`dd of=`、`touch`、`ln` 提取写入目标;从 `< 文件`、`cat/tac/nl/wc/file/less/more`、`head/tail`、不带 `-i` 的 `sed`、`grep/egrep/fgrep/rg`、`awk/gawk/mawk` 提取**读取目标** `Reads`(只用于已读集合,不参与 `path`/`writes` 匹配;选项取值表见 `internal/bashparse/doc.go`);`sh/bash/zsh/dash -c '<字面串>'` 递归 ≤2 层;heredoc 正文不当命令;`eval` → `CommandList.Unknown`;git 全局参数解析出 `git.sub`/`git.cwd`;sleep 支持 `300`、`5m`、`1h`、`1d` 与多参数求和。
- **PowerShell**:保守分词(`;`/换行切语句,`|` 切管道),别名表(`rm/del → remove-item`、`sleep → start-sleep`、`sc → set-content` …),参数表,`>`/`Out-File`/`Set-Content`/`Add-Content`/`Remove-Item`/`Copy-Item`/`Move-Item`/`New-Item` 写入目标,`Start-Sleep -Seconds/-Milliseconds`;读取目标:`Get-Content`(`cat`/`gc`/`type`)、`Select-String -Path` 及外部可执行文件按 bash 读表。无法分词 → 错误 → unknown。**覆盖面窄于 bash**,写入用户文档。
- 节点作用域与 `bash`/`ps` 分派见 §5.4。

---

## 7. 谓词(封闭词表)

每个 `Cond` 对象**恰好一个键**(lint `cond-arity`);未知键 → `unknown-predicate`(严格解码阶段即报错)。

| 谓词 | 参数 | 真 | 假 | unknown |
|---|---|---|---|---|
| `read_this_session` | `{"path"?}` | 本 agent 本会话读过该路径:Read 类工具的目标;Edit/Write 等成功写入后的目标;以及 Bash/PowerShell **PostToolUse** 时解析出的只读命令读取目标(`CommandNode.Reads`:`cat`/`head`/`tail`/`sed -n`/`grep`/`rg`/`awk`/`wc`/`nl`/`< 文件`/`Get-Content`…,见 §6;Codex 没有 Read 工具,靠这一来源) | 未读过 | 状态文件不可读(I/O 错误);路径动态或无绑定目标 |
| `path_exists` | `{"path"?}` | Lstat 成功 | ENOENT/ENOTDIR | 其他错误;路径动态或无绑定目标 |
| `verify_fresh` | `{"names"?: [..]}` | 每个 name 的最新记录都是有效通过且 `S_end == C` | 某 name 无记录、最新记录非有效通过、或 `S_end ≠ C` | 不在 git 仓库;无/非法 project.json;无 required;同名 job 运行中;commit 参数无法解析;快照退回超时/失败 |
| `verify_failed_since_read` | `{"path"?}` | 在 max(该路径最后读取时间, 会话开始时间) 之后,本仓库有 outcome 为 fail/timeout/signal 的 verify 记录 | 没有 | 不在仓库;状态不可读;路径动态 |
| `tool_count_since_inject` | Cmp | 比较本 agent 自上次锚点注入以来的工具调用数 | | 状态不可读 |
| `prev_prompt_regex` | `{"regex"}` | 上一条用户 prompt 末尾 200 字匹配 | 不匹配或无记录 | 状态不可读 |
| `prior_hit` | `{"rule", Cmp…}` | 比较本 agent 中该规则(含 would)的历史命中数 | | 状态不可读 |
| `env_equals` | `{"name","value"}` | hook 进程环境变量等于 value | 不等或未设置 | — |
| `branch_glob` | `{"patterns"}` | 当前分支短名匹配名称 glob 列表(§5.1 方言,区分大小写;`*` 不跨 `/`,`feature/**` 匹配多级) | 不匹配;detached HEAD | 不在仓库;HEAD 不可读 |
| `is_subagent` | `{}` | 事件来自子 agent(有 `agent_id`) | 主会话 | — |

**路径参数**:`path` 省略或为 `"$target"` 时绑定到当前目标(§9);否则是字面路径(相对项目根,`~/` 展开),按 §5.2 规范化。字面路径的谓词对所有事件可用。

**`verify_fresh` 的 C**:规则的 `bash`/`ps` 绑定节点(只指由规则的 `bash`/`ps` 匹配器产生的绑定;没有 shell 匹配器的规则按写入目标形成的绑定不算,v1.3)是 `git commit` 时,C 由该节点的 commit 形式计算(普通 → 索引;`-a` → 索引 + `add -u`;带路径 → 按 git 语义;`--amend` 同对应形式;`git -C <dir>` → 该目录所属仓库实例);否则(包括 Stop 族)C = 当前工作树快照 S。算法与"严格部分暂存语义"见计划 B3 与 `internal/evidence/doc.go`。

**状态作用域**:已读集合、`prior_hit`、`tool_count_since_inject` 按 (repo_inst, session, agent) 隔离,主 agent 与子 agent **互不继承**(B6)。证据按仓库内容快照共享。

---

## 8. 比较语法

```text
{"gte": 60}            // ≥ 60
{"gte": 1, "lt": 5}    // 1 ≤ x < 5(多个键 AND)
```

键:`eq`、`ne`、`gt`、`gte`、`lt`、`lte`,值为数字;至少一个键(lint `bad-cmp`)。用于 `sleep_s`、`cjk_ratio`、`count_regex`、`tool_count_since_inject`、`prior_hit`。输入未知(如动态 sleep 时长)时比较结果为 unknown。

---

## 9. 三值求值

**真值表**(Kleene):`AND`:有 false 即 false,全 true 才 true,否则 unknown;`OR`:有 true 即 true,全 false 才 false,否则 unknown;`NOT unknown = unknown`。`when` 数组与 `all` 为 AND,`any` 为 OR,空 `all` = true,空 `any` = false。

**绑定**:一次求值中,`match` 产生一组绑定(binding),`when` 在每个绑定上独立求值:

```
evalRule(r, in):
  if r.event ≠ in.event 或 !r.AppliesTo(client): 跳过(不计 evals)
  m := AND(tool, agent_type, run_in_background, input_regex, text)   // 非绑定类字段
  if m == false: return false
  B, U := bindings(r, in)
    // 有 bash/ps:B = 肯定满足 shell 匹配器的节点(若同时有 path:节点×其满足 path 的写入目标)
    // 无 shell 匹配器、工具有目标:B = 满足 path 的目标(无 path 时为全部目标)
    //   Bash/PowerShell 的"目标"= 解析出的写入目标;无 path 时只有 when 引用 $target 才解析(见下)
    // 否则:B = {空绑定}
    // U = 是否有候选的匹配结果为 unknown(动态目标、解析失败、CommandList.Unknown…)
  w := OR_{b ∈ B} AND_{c ∈ when} c(b)
  if U: w := w OR unknown
  return m AND w
```

- B 为空且 U 为假 → false(匹配失败);B 为空且 U 为真 → unknown。
- 工具没有目标、规则也没有 `path`:单个空绑定,其中 `$target` 形式的路径谓词为 unknown。
- (v1.3)Bash/PowerShell 工具、规则既无 `bash`/`ps` 也无 `path`:只有当 `when` 中(任意 `all`/`any`/`not` 深度)存在绑定当前目标的路径谓词(`read_this_session`、`path_exists`、`verify_failed_since_read` 省略 `path` 或写 `"$target"`)时,才解析命令并以非动态写入目标为 B;动态目标、解析失败、`CommandList.Unknown` 计入 U;没有写入目标且 U 为假时退回单个空绑定(`$target` 谓词 unknown)。`when` 不引用 `$target` 的规则不解析命令,只有单个空绑定,所以与目标无关的 `when` 不会让解析失败或 `eval` 变成 unknown。
- 有 `bash`/`ps` 或 `path` 的规则,B/U 的计算与是否带 `when` 无关;不带 `when` 时 `when` 恒真,`w` 只取决于 B 是否为空与 U。
- 结果为 unknown 时规则不命中;`RuleResult.UnknownCause` 记录第一个导致 unknown 的原因(如 `bash:parse_error`、`verify_fresh:job_running`),累加 `EvalCount.Unknown` 并在审计中留痕。

---

## 10. 多规则裁决与分层合并

### 10.1 同一事件多条规则命中(B1)

1. 每条命中先做**能力检查**:`Effective = action`;若该客户端不支持 (event, action),`Effective = log` 并写 `diag: capability_degraded`。
2. 本次决策档位 = 所有命中中最严的 `Effective`(deny > ask > warn > log)。
3. `Reason` = 档位等于决策档位(且为 warn/ask/deny)的命中,按规则加载顺序拼接 `[id] message`(每条一行),总长 ≤ 2000 字,超出以 `…(+N)` 截断:N 是**未完整显示的命中行数**(连第一行都放不下时,保留第一行的前缀,它也计入 N)。
4. **所有**命中(含 log 档的 `would_<target>`)都写入审计。
5. 无命中或只有 log 命中 → 不输出决策(stdout 只可能含注入文本)。

### 10.2 三层合并(B4,`internal/config/doc.go` 为准)

- **org 整体不可变**:下层出现同 id 规则时,运行期忽略下层并写 `rule_shadowed`;`lint --stack` 报 `org-id-shadowed`(错误)。org 规则无需 `locked` 字段。
- repo 与 user 同 id:**repo 整个对象胜出**,不做字段级合并(`rule_shadowed`)。
- user 只能新增不同 id 的规则,不能禁用或修改上层规则。
- 上限 **12 条 enabled 规则**:按 org(全部)→ repo(文件顺序)→ user(文件顺序)累计,超出的记 `rule_dropped` 并写 stderr;`lint --layer org` 要求 org 自身 ≤12;`lint --stack` 对超限报 `too-many-rules`。
- 某层文件非法(任一 error 级诊断)时,该层整体沿用 last-known-good;没有 LKG 则该层视为空,写 `config_invalid` 并在下一次注入中提示。
- `generation = rules_hash`(`config.Generation`:合并后规则去掉 examples/description、显式化默认值,与 settings 一起做规范 JSON 的 sha256 前 16 位)。

---

## 11. lint 诊断码

| 代码 | 级别 | 含义 |
|---|---|---|
| `schema-invalid` / `unknown-field` / `version-unsupported` | 错误 | 结构不符(严格解码) |
| `bad-id` / `duplicate-id` | 错误 | id 不合规或重复 |
| `bad-event` / `bad-action` | 错误 | 枚举值非法 |
| `target-weaker-than-action` | 警告 | target 比 action 弱 |
| `action-unsupported` | 错误 | 客户端 × 事件 × 动作不在矩阵内 |
| `action-unverified` | 警告 | 组合未核验 |
| `target-unsupported` | 警告 | target 不被某客户端支持(promote 时会失败) |
| `event-unavailable` | 警告 | 事件在某客户端不存在 |
| `cursor-stop-followup` | 警告 | Cursor Stop 的 deny 语义为续跑 |
| `field-not-applicable` | 错误 | 字段与事件/工具不适用(§5.8) |
| `input-field-unknown` | 错误 | `input_regex` 键不在登记表(实现可同时报 `field-not-applicable`,两者都是错误,退出码相同) |
| `text-source-mismatch` | 错误 | `text.source` 与事件不符 |
| `bad-regex` | 错误 | 不是合法 RE2 |
| `bad-glob` / `glob-only-negation` | 错误 | glob 非法 / 只有排除模式 |
| `bad-cmp` | 错误 | 比较对象为空 |
| `cond-arity` / `unknown-predicate` | 错误 | Cond 键数 ≠ 1 / 未知谓词 |
| `prior-hit-unknown-rule` | 警告 | `prior_hit.rule` 不在同一规则集中 |
| `verify-name-unknown` | 警告 | `verify_fresh.names` 不在 project.json |
| `message-too-long` / `inject-too-long` / `anchor-too-long` | 错误 | 超过 300 / 400 / 80 字 |
| `inject-budget-exceeded` | 错误(全文 > 8000)/ 警告(锚点 > 1200,运行期截断) | 注入总预算 |
| `examples-missing` | 错误 | 缺少 hit 或非命中示例 |
| `example-bad-input` | 错误 | 示例输入无法被适配器解析 |
| `example-failed` | 错误 | 示例结果与 `expect` 不符(`--examples` 或 `test-rules`) |
| `too-many-rules` | 错误 | 超过 12 条 enabled |
| `org-id-shadowed` | 错误 | 下层重定义 org 规则 id |
| `empty-rule` | 错误 | 无 match 也无 when |

---

## 12. examples 与 `ballast test-rules`

```
Example := {
  "name"?: string,
  "expect": "hit" | "miss" | "unknown",     // 规则结果 true / false / unknown
  "client"?: 客户端,默认 "claude-code",
  "native"?: 原生事件名,默认为规则事件在该客户端上的原生名,
  "input": { … },                             // 该客户端原生 hook stdin 的子集
  "fixture"?: {
    "cwd"?: "/repo", "home"?: "/home/user", "goos"?: "linux",
    "read"?: [路径], "exists"?: [路径], "verify_fresh"?: "true"|"false"|"unknown"(默认 unknown),
    "failed_since_read"?: [路径], "env"?: {k: v}, "branch"?: string(缺省 = 未知),
    "tool_count_since_inject"?: int, "prev_prompt"?: string, "prior_hits"?: {rule: n},
    "not_a_repo"?: bool
  }
}
```

- 每个示例**单独**求值:只编译该规则本身,动作临时视为 deny(避免能力降级掩盖命中),输入经真实适配器 `ParseEvent`/`NormalizeTool`(缺少的 `session_id`、`cwd`、`hook_event_name` 自动补全),谓词环境来自 fixture(`dsl.FixtureEnv`)。`exists` 未列出的路径 `path_exists` 为 false。
- 每条规则至少一个 `hit` 和一个非命中;`test-rules` 退出码见架构文档 §7。

---

## 13. 完整示例

以下 6 个文件也以独立文件形式存在于 `schema/examples/`(另有一个综合示例 `07-misc-org-style.json`,演示 `settings`、`clients`、`ask`、`pipe_from`、`text.source=tool_response`、`input_regex` 虚拟字段与 `path` 取反、`is_subagent` 与 `tool_count_since_inject`)。

### 13.1 改文件前先读(read_first → `read-before-edit`)

多目标补丁:任一已存在的目标未读即命中;新建文件豁免。

```json
{
  "version": 1,
  "rules": [
    {
      "id": "read-before-edit",
      "action": "deny",
      "event": "PreToolUse",
      "match": {"tool": ["Edit", "MultiEdit", "Write", "NotebookEdit"]},
      "when": [
        {"path_exists": {}},
        {"not": {"read_this_session": {}}}
      ],
      "message": "该文件已存在但本会话尚未读取。请先读取它(Read 工具,或 cat、sed -n、head 等只读命令),再修改。",
      "inject": {"text": "修改已存在的文件前,必须先在本会话中读过它(Read 工具,或 cat、sed -n、head 等只读 shell 命令);新建文件不受此限制。", "anchor": "改已有文件前先读"},
      "examples": [
        {"name": "unread existing file", "expect": "hit",
         "input": {"tool_name": "Edit", "tool_input": {"file_path": "/repo/src/a.go", "old_string": "x", "new_string": "y"}},
         "fixture": {"exists": ["src/a.go"]}},
        {"name": "read first", "expect": "miss",
         "input": {"tool_name": "Edit", "tool_input": {"file_path": "/repo/src/a.go", "old_string": "x", "new_string": "y"}},
         "fixture": {"exists": ["src/a.go"], "read": ["src/a.go"]}},
        {"name": "codex apply_patch, second target unread", "expect": "hit", "client": "codex",
         "input": {"tool_name": "apply_patch", "tool_input": {"input": "*** Begin Patch\n*** Update File: src/a.go\n@@\n-x\n+y\n*** Update File: src/b.go\n@@\n-x\n+y\n*** End Patch\n"}},
         "fixture": {"exists": ["src/a.go", "src/b.go"], "read": ["src/a.go"]}}
      ]
    }
  ]
}
```

### 13.2 禁止前台长 sleep(Bash + PowerShell 双变体)

```json
{
  "version": 1,
  "rules": [
    {
      "id": "no-long-sleep",
      "action": "deny",
      "event": "PreToolUse",
      "match": {
        "run_in_background": false,
        "bash": {"cmd": "sleep", "sleep_s": {"gte": 60}, "async": false},
        "ps": {"cmd": "start-sleep", "sleep_s": {"gte": 60}, "async": false}
      },
      "message": "不要在前台 sleep 60 秒以上。需要等待时用 run_in_background,或用有界的短轮询。",
      "inject": "不要在前台 sleep 60 秒以上;需要等待时用 run_in_background 或短间隔轮询。",
      "examples": [
        {"expect": "hit", "input": {"tool_name": "Bash", "tool_input": {"command": "while true; do sleep 300; done"}}},
        {"expect": "miss", "input": {"tool_name": "Bash", "tool_input": {"command": "for i in 1 2 3; do sleep 30; done"}}},
        {"expect": "miss", "input": {"tool_name": "Bash", "tool_input": {"command": "cat <<EOF\nsleep 300\nEOF"}}},
        {"expect": "unknown", "input": {"tool_name": "Bash", "tool_input": {"command": "sleep $WAIT"}}},
        {"expect": "hit", "input": {"tool_name": "PowerShell", "tool_input": {"command": "Start-Sleep -Seconds 300"}}}
      ]
    }
  ]
}
```

### 13.3 提交前要有新鲜证据

```json
{
  "version": 1,
  "rules": [
    {
      "id": "evidence-before-commit",
      "action": "deny",
      "event": "PreToolUse",
      "match": {
        "bash": {"cmd": "git", "git_sub": "commit"},
        "ps": {"cmd": "git", "git_sub": "commit"}
      },
      "when": [{"not": {"verify_fresh": {}}}],
      "message": "提交前需要与待提交内容一致的 ballast verify 通过记录:运行 `ballast verify`,用 `ballast verify --status` 确认通过后再提交。`git add` 与 `git commit` 请分两次执行(同一条命令里的 add 在判定时尚未执行)。",
      "examples": [
        {"name": "add and commit in one command", "expect": "hit", "input": {"tool_name": "Bash", "tool_input": {"command": "git add -A && git commit -m fix"}}, "fixture": {"verify_fresh": "false"}},
        {"expect": "hit", "input": {"tool_name": "Bash", "tool_input": {"command": "git commit -m fix"}}, "fixture": {"verify_fresh": "false"}},
        {"expect": "miss", "input": {"tool_name": "Bash", "tool_input": {"command": "git commit -m fix"}}, "fixture": {"verify_fresh": "true"}},
        {"expect": "unknown", "input": {"tool_name": "Bash", "tool_input": {"command": "git commit -am wip"}}, "fixture": {"verify_fresh": "unknown"}}
      ]
    }
  ]
}
```

### 13.4 不得篡改受保护路径(文件工具 + shell 写入目标)

```json
{
  "version": 1,
  "rules": [
    {
      "id": "no-tamper",
      "action": "deny",
      "event": "PreToolUse",
      "match": {
        "tool": ["Edit", "MultiEdit", "Write", "NotebookEdit", "Bash", "PowerShell"],
        "path": ["**/.ballast/**", "~/.claude/settings*.json", "**/.claude/settings*.json", "~/.codex/hooks.json", "~/.cursor/hooks.json"]
      },
      "message": "不得改写 Ballast 的规则、状态、证据,或 AI 客户端的 hook 配置。",
      "examples": [
        {"expect": "hit", "input": {"tool_name": "Bash", "tool_input": {"command": "echo '{}' > ~/.ballast/rules.json"}}},
        {"expect": "hit", "input": {"tool_name": "PowerShell", "tool_input": {"command": "Set-Content -Path /home/user/.claude/settings.json -Value x"}}},
        {"expect": "miss", "input": {"tool_name": "Bash", "tool_input": {"command": "cat ~/.ballast/rules.json"}}},
        {"expect": "unknown", "input": {"tool_name": "Bash", "tool_input": {"command": "echo x > \"$TARGET\""}}}
      ]
    }
  ]
}
```

### 13.5 完成声明需要证据(Stop,影子模式)

```json
{
  "version": 1,
  "rules": [
    {
      "id": "claim-needs-evidence",
      "action": "log",
      "target": "deny",
      "event": "Stop",
      "match": {
        "text": {
          "source": "last_assistant_message",
          "preprocess": ["strip_code_blocks", "strip_quotes"],
          "window": {"tail_chars": 600},
          "regex": ["(?i)\\b(all tests pass(ed)?|done|completed)\\b", "(已完成|全部通过|测试通过)"],
          "not_regex": ["(?i)\\bnot (yet )?(done|completed)\\b", "(未完成|还没|尚未)"]
        }
      },
      "when": [{"not": {"verify_fresh": {}}}],
      "message": "你声称已完成或测试通过,但当前工作树没有一致的 ballast verify 通过记录。",
      "examples": [
        {"expect": "hit", "input": {"last_assistant_message": "修复已完成,全部通过。"}, "fixture": {"verify_fresh": "false"}},
        {"expect": "miss", "input": {"last_assistant_message": "Not done yet; two tests still fail."}, "fixture": {"verify_fresh": "false"}},
        {"expect": "miss", "input": {"last_assistant_message": "All tests passed."}, "fixture": {"verify_fresh": "true"}}
      ]
    }
  ]
}
```

### 13.6 中文回复偏好(examples 级,仅 Claude Code)

```json
{
  "version": 1,
  "rules": [
    {
      "id": "chinese-plain",
      "action": "warn",
      "event": "Stop",
      "clients": ["claude-code"],
      "match": {
        "text": {"source": "last_assistant_message", "preprocess": ["strip_code_blocks"], "cjk_ratio": {"lt": 0.3}}
      },
      "when": [{"prev_prompt_regex": {"regex": "\\p{Han}"}}],
      "message": "用户使用中文提问,请用中文回复(代码与命令除外)。",
      "examples": [
        {"expect": "hit", "input": {"last_assistant_message": "All done, the build is green now."}, "fixture": {"prev_prompt": "帮我修一下构建"}},
        {"expect": "miss", "input": {"last_assistant_message": "构建已经修好。"}, "fixture": {"prev_prompt": "帮我修一下构建"}},
        {"expect": "miss", "input": {"last_assistant_message": "Fixed."}, "fixture": {"prev_prompt": "fix the build"}}
      ]
    }
  ]
}
```

---

## 14. 已知局限(写入用户文档)

- PowerShell 只做保守分词,覆盖面窄于 bash;无法分词时 unknown(放行并审计)。
- `flags` 不知道哪些选项带值(`git commit -m -n` 会把 `-n` 当作标志)。
- shell 写入目标只覆盖 §6 列出的命令;`python -c "open(...).write()"` 之类语言内写入不被识别(T0 下本来就不是安全边界;T1 由沙箱 `denyWrite` 兜底)。
- `verify_fresh` 只看已跟踪文件;测试依赖未跟踪文件的情况交给 CI(B3 已知边界)。
- **`git add … && git commit …` 写在同一条命令里**:PreToolUse 时 add 尚未执行,C 取当前索引,严格语义下通常判为过期并拒绝(或影子模式下记 would_deny)。这是有意的:Ballast 不模拟同一命令中前序节点的副作用。`evidence-before-commit` 的 message 会提示"`git add` 与 `git commit` 分两次执行"。`git commit -a` 不受影响(C 按 `-a` 计算)。
- **shell 读取目标**只覆盖 §6 列出的只读命令;`python -c "open(…).read()"`、`find … | xargs cat`、`git show HEAD:a.go` 等不计入已读集合(只会让 read-before-edit 更严,不会更松)。相对路径按事件 cwd 解析;同一命令里出现 `cd`/`pushd`/`popd`(含 bash 的 `builtin cd`,v1.3;PowerShell `Set-Location` 族)时只登记绝对路径。读取在 PostToolUse 时登记,所以 Codex 上"先 `sed -n` 再 `apply_patch`"必须是两次工具调用。登记不看退出码:客户端触发 PostToolUse 即视为已执行(Claude Code 的非 0 失败走 PostToolUseFailure,不登记;Codex 非 0 退出是否仍触发 PostToolUse 未取证,属于放宽方向、影响有限,列为 U6/U12 取证项)。提取规则由唯一纯函数 `model.ShellReadPaths` 定义,hook 与 replay 共用。
- 规则语言没有 exec;需要的新能力走谓词扩展或离线 replay。

---

## 修订记录

### v1.3(2026-10-16,各单元实现后的契约跟进)

- §5.2:POSIX 上候选同时包含词法折叠形式与物理 `..` 形式;Windows 客户端盘符路径在 POSIX 主机上只有词法形式。
- §5.5:源文本缺失为 false(先查状态中的回退文本);状态不可读时为 unknown;`strip_quotes` 不跨行配对。
- §7:`verify_fresh` 只按规则 `bash`/`ps` 匹配器产生的绑定节点取 commit 形式。
- §9:无 shell 匹配器、无 `path` 的 Bash/PowerShell 规则,只有 `when` 引用 `$target` 时才以写入目标为绑定;有 `bash`/`ps` 或 `path` 的规则,B/U 与是否带 `when` 无关。
- §10.1:`…(+N)` 中 N 的含义。§11:`input-field-unknown` 可与 `field-not-applicable` 同时出现。
- §14:`builtin cd` 视为切换目录。
- `schema/examples/07`:`secret-in-output` 以 `clients` 排除 Hermes(post_tool_call 不能 warn)。

### v1.1(2026-10-16,独立审核修订)

- §6、§7:新增 shell 只读命令的读取目标(`CommandNode.Reads`),PostToolUse 时并入已读集合,使 `read-before-edit` 在 Codex(无 Read 工具)上可用。对应 pinrule `codex.py:576-612` 的读路径提取(DSL 等价,覆盖面更宽,见 `internal/bashparse/doc.go`)。
- §5.1、§5.4、§5.6、§7:明确"名称 glob"(`tool`、`cmd`、`pipe_*`、`agent_type`、`branch_glob`)直接用 doublestar、在所有平台区分大小写;只有路径类字段走 `PathService`。
- §13.1 / §13.3 / §14:read-before-edit 的提示包含只读 shell 命令;evidence-before-commit 提示 add 与 commit 分开执行;已知局限新增两条。
