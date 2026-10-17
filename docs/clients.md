# 客户端支持矩阵

> 权威数据在 `internal/model/capability.go`(`model.Capabilities`)与 `internal/model/client.go`(`model.NativeEventTable`),本文是它们的人类可读版本;安装目标与输出协议的冻结契约见 [`architecture.md`](architecture.md) §4–§5。调研来源见 [`research/pinrule-install-and-clients.md`](research/pinrule-install-and-clients.md)。
>
> **「核验」的含义**:Ballast 自己用真实客户端抓取过 payload 并断言过行为,才叫**已核验**。目前只有 Claude Code 做过(M0a:Claude Code 2.1.300,macOS,证据在 [`facts/`](facts/))。Codex、Cursor 的事件名、payload 字段与输出协议来自参照项目 pinrule 的源码与文档,**全部未核验**(Hermes 见下一段);`ballast lint` 对这些组合给 `action-unverified` 警告,`ballast doctor` 会列出它们。「未核验」不等于「错误」,只是没有 Ballast 自己的证据。
>
> **Hermes 的特殊状态(P2)**:Hermes 做过**离线核验**——用 `hermes hooks test`(Hermes 源码提交 9a0a162536)在一次性 `HERMES_HOME` 里跑过序列化形状、输出解析与阻断语义,证据在 [`facts/hermes-cases.json`](facts/hermes-cases.json)、[`facts/HERMES-CONCLUSIONS.md`](facts/HERMES-CONCLUSIONS.md);**触发时机未核验**(真实 agent 何时、以什么参数调用 hook 需要模型会话,【需凭据】,补测命令见结论文档)。

## 1. 分级总览

| 级别 | 客户端 | `--client` | 配置位置 | 最高信任层级 | 核验状态 |
|---|---|---|---|---|---|
| 一级 | Claude Code | `claude-code` | `~/.claude/settings.json` | T1(需组织托管部署;托管层行为已在 Linux 容器里用 Claude Code 2.1.300 取证,见 [`facts/M0b-CONCLUSIONS.md`](facts/M0b-CONCLUSIONS.md),其中 6 项【需凭据】尚未核验) | M0a 已核验(除下表标注的单元格) |
| 二级 | Codex | `codex` | `~/.codex/hooks.json`、`config.toml` | T0 | 全部未核验 |
| 二级 | Cursor | `cursor` | `~/.cursor/hooks.json`、`rules/ballast.mdc` | T0 | 全部未核验 |
| 三级 | Hermes | `hermes` | `~/.hermes/config.yaml` | T0 | 离线核验(hooks test),触发时机未核验 |

- **运行时不嗅探客户端**:客户端身份由安装器写进命令行(`<abs>/ballast hook --client <id> --event <原生事件>`),原生事件经 `NativeEventTable` 归一为 6 个规则事件之一。
- 任何异常都放行:stdout 为空、退出码 1。**无决策时绝不输出 allow**(Codex 要求 allow 为 `{}` 或空)。
- 配置根目录的覆盖变量:Claude `CLAUDE_CONFIG_DIR`、Codex `CODEX_HOME`、Hermes `HERMES_HOME`;Cursor 没有覆盖变量(仅测试用 `BALLAST_INSTALL_ROOT`)。

## 2. 规则事件 × 动作 能力矩阵

Claude Code 一列里只有 PreToolUse deny/warn(`exit1-json-deny`、`warn-additionalcontext`)与 Stop deny/warn(`stop-cap`、`stop-systemmessage`)有 M0a 取证;标 ✓? 的单元格协议依据来自文档,补取证用例(需要真实模型会话,【需凭据】)后再改回 ✓(v2.0.9 更正,此前误标为已核验)。

符号:**✓** 支持且已核验;**✓?** 支持但未核验(`lint` 给警告);**✗** 不支持(`lint` 报错 `action-unsupported`);**—** 该事件在此客户端不存在。`log` 在事件存在时恒可用,stdout 为空。

| 规则事件 | 动作 | Claude Code | Codex | Cursor | Hermes |
|---|---|---|---|---|---|
| PreToolUse | deny | ✓ | ✓? | ✓? | ✓(离线核验,触发时机未核验) |
| | ask | ✓?(`-p` 非交互下可能被直接拒绝;M0a 无对应用例) | ✗(Codex 解析但不执行,fail open) | ✓? | ✗ |
| | warn | ✓(仅 `additionalContext`,不输出决策) | ✗ | ✗ | ✗ |
| UserPromptSubmit | deny | ✓?(M0a 无对应用例) | ✗ | ✗ | ✗ |
| | warn | ✓?(`additionalContext`;M0a 无对应用例) | ✓? | ✓?(嵌套格式沿用 pinrule) | ✓(仅 `pre_llm_call`;离线核验,触发时机未核验) |
| PostToolUse | warn | ✓?(M0a 无对应用例) | ✓? | ✓? | ✗(只能 log) |
| PostToolUseFailure | warn | ✓?(文档列出,未单独取证) | — | — | — |
| Stop | deny | ✓(`block` 使会话继续,连续 8 次上限,已实测) | ✓?(pinrule 亦标待验证) | ✓?(**语义为续跑**,见 §4.3) | ✓(仅 `pre_verify`;**语义为续跑**,见 §4.4;离线核验,触发时机未核验;`lint` 给 `hermes-stop-continue` 警告) |
| | warn | ✓(`systemMessage`,放行停止,已实测) | ✗ | ✗ | — |
| SubagentStop | deny | ✓?(M0a 的 `subagent` 用例只记录 stdin,没有断言 block) | — | ✓?(同 stop) | — |
| | warn | ✓?(按 Stop 推断) | — | ✗ | — |

规则:

- 默认 `target` 取该事件在 Claude Code 上允许的最严一档。
- 运行期遇到不支持的组合(例如没 lint 就安装到 codex):该命中降为 `log`,并写审计 `diag: capability_degraded`,不静默。
- 规则的 `clients` 字段可以把规则限定在部分客户端。

## 3. 注入载体

| 客户端 | 全文(≤8000 字) | 每次提示的锚点 | 回合中途补锚点 | 其他 |
|---|---|---|---|---|
| Claude Code | SessionStart(所有 source) | UserPromptSubmit | PostToolBatch | compact 后由 SessionStart(compact) 恢复 |
| Codex | SessionStart | UserPromptSubmit | PostToolUse | 没有 compact 后重注入 |
| Cursor | sessionStart(`additional_context`) | beforeSubmitPrompt | postToolUse | 另写托管的 `~/.cursor/rules/ballast.mdc`(`alwaysApply: true`)作为**可靠通道**:实测经验(来自 pinrule)是 hook 注入的内容模型在开头不一定能看到 |
| Hermes | 首个 `pre_llm_call`(`is_first_turn` 或会话未注入过全文) | `pre_llm_call` | 无 | `on_session_start` 不能注入 |

非 SessionStart 的单次注入总量不超过 2500 字。

## 4. 各客户端细节

### 4.1 Claude Code(一级)

**原生事件**(全部安装,同名归一):SessionStart、UserPromptSubmit、PreToolUse、PostToolUse、PostToolUseFailure、PostToolBatch、Stop、SubagentStop、SessionEnd,以及 P2 起的 **ConfigChange**(只审计:一行 `config_change`,detail 为 `{source, file, protected}`,stdout 恒空,规则不能引用;不带 matcher)。工具事件(PreToolUse、PostToolUse、PostToolUseFailure)带 `"matcher":"*"`。另有**仅解析、不安装**的 PreCompact、PostCompact、SubagentStart(规则不能引用)。

**条目格式**:`{"type":"command","command":"<二进制绝对路径>","args":["hook","--client","claude-code","--event","<Event>"]}`,命令与参数分开写。

**payload 字段核验状态**(依据:M0a 真实抓取,样本在 `facts/payloads/`):

| 字段 | 状态 | 说明 |
|---|---|---|
| `session_id`、`cwd`、`hook_event_name`、`transcript_path` | 已核验 | 所有事件都有 |
| `permission_mode`、`prompt_id`、`effort` | 已核验(条件出现) | `SessionStart(startup)` 没有;`effort` 是对象 `{"level":…}`,不是字符串 |
| `tool_name`、`tool_input`、`tool_use_id` | 已核验 | PreToolUse / PostToolUse / PostToolUseFailure |
| `tool_response` | 已核验 | Bash 成功时含 `stdout, stderr, interrupted, isImage, noOutputExpected`,可有 `returnCodeInterpretation`;**没有数值退出码** |
| `error`、`is_interrupt`、`duration_ms` | 已核验 | PostToolUseFailure;`error` 首行形如 `Exit code N` |
| `prompt` | 已核验 | UserPromptSubmit |
| `last_assistant_message`、`stop_hook_active` | 已核验 | Stop / SubagentStop |
| `agent_id`、`agent_type` | 已核验 | 只在子 agent 内出现;压缩用的内部 agent 的 `agent_type` 是空字符串,其 SubagentStop 默认被忽略 |
| `agent_transcript_path` | 已核验 | SubagentStop |
| `source`、`file_path` | 已核验(M0b `configchange-payload`,Claude Code 2.1.300) | ConfigChange:`source` 为 `user_settings`/`project_settings`/`local_settings`/`policy_settings`/`skills`,`file_path` 是变更文件的绝对路径;托管目录的 `managed-settings.d/` 新增文件与 `~/.claude.json` 不触发该事件 |
| `scratchpad_dir` | **本次样本均未出现** | 不依赖 |
| `PowerShell` 工具与 Windows 路径形态 | 文档依据,**未实测**(Ballast 只在 macOS 上做过 M0a) | 依据 `ballast-plan.md` A23 |

**输出协议**(已核验,除标注外):

- deny:`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":R}}`;UserPromptSubmit / Stop / SubagentStop 用 `{"decision":"block","reason":R}`。
- Stop 族 warn:`{"systemMessage":R}`(**不要**用 `additionalContext`,后者会让会话继续)。
- 注入:`hookSpecificOutput.additionalContext`。
- 行为事实:hook 退出码 1 但 stdout 是合法 deny JSON 时,工具**仍被拦截**——所以放行只能靠空 stdout。被拦截的工具调用不会产生 PostToolUse / PostToolUseFailure。grep 无匹配(exit 1)走 PostToolUse,不走 PostToolUseFailure。

### 4.2 Codex(二级,全部未核验)

**原生事件**(Ballast 安装的 6 个,均未核验):SessionStart、UserPromptSubmit、PreToolUse、**PermissionRequest→PreToolUse**、PostToolUse、Stop。【静态依据】(`docs/facts/codex-static.md` §1 第 2 行,读 Codex 0.130 与 0.163 源码,**没有观察到一次真实 hook 调用**):0.130 另有 PreCompact、PostCompact(只有通用输出,不能阻止);0.163 再增加 SessionEnd、SubagentStart、SubagentStop、Interrupt;PostToolUseFailure 在两个版本中都没有。所以"没有 compact、SubagentStop 事件"的旧说法不成立;Ballast 不安装这些事件。

**条目格式**:`{"hooks":[{"type":"command","command":"<带引号的整条命令>","timeout":30}]}`,不带 matcher;写入 `hooks.json`,并在 `config.toml` 确保 `[features]` 下 `hooks = true`。manifest 记下该开关的原值(`absent` / `false` / `true`):原本没有这个键时,卸载后保留 `hooks = true`(登记的例外);原值为显式 `false` 时,安装改成 `true` 之前先向 stderr 警告,卸载恢复成 `false`。

**需要的额外步骤**:hook 默认处于未受信状态,要在 Codex TUI 里用 `/hooks` 批准;或显式 `ballast install --client codex --codex-trust` 由 Ballast 写入 trust 哈希(算法照搬 pinrule 引用的 Codex 0.130 源码,**未核验于其他版本**;key 含条目下标,所以每次写入后都会按最终位置重算)。【静态依据】Codex 先对 `CODEX_HOME` 调用 `canonicalize()`(解析全部符号链接)再计算 key,所以 `--codex-trust` 写 key 时用 `EvalSymlinks` 后的 `hooks.json` 路径(manifest 里的条目仍记字面路径),`ballast doctor` 的 `codex.trust` 对旧版本写下的字面路径 key 给出警告,重新运行 `ballast install --client codex --codex-trust` 即可修复(Windows 上规范化形式未核验)。

【静态依据】其他与本节相关、尚未写进上文的源码事实:`hooks` 特性默认开启(`default_enabled: true`),`features.hooks` 只有被显式设成 `false` 时才需要改;0.163 起 `hooks.json` 顶层只允许 `description` 与 `hooks` 两个键(往里加别的键会被整体拒绝);退出码 1 时 Codex **不解析 stdout**(只有 0 才解析),退出码 2 加非空 stderr 才阻止。

**payload / 协议假设**(均来自 pinrule,未核验):

| 项 | 假设 |
|---|---|
| 工具名 | `apply_patch` → Edit(解析 envelope,`Update / Add / Delete / Move to` 的全部目标都进入目标列表);`exec_command`、`shell` → Bash(命令在 `cmd` 字段,字符串或 argv 数组);argv 形如 `["apply_patch", …, "<envelope>"]` 时按 `apply_patch` 处理。【静态依据】(Codex 0.130/0.163 源码,未观察到真实调用)对 hook 而言所有 shell 类工具一律报 `tool_name:"Bash"`、`tool_input.command` 是**字符串**(`exec_command` 的 `cmd` 在 hook 载荷里改名为 `command`,argv 用 `shlex_join` 拼成一条),**hook 载荷里不会出现 argv 数组**;`apply_patch` 报 `tool_name:"apply_patch"`、`tool_input.command` 为补丁全文。`internal/adapter/codex` 同时接受 `cmd`/`command` 与多种工具名,所以实现不受影响 |
| 读文件 | Codex 没有 Read 工具,读取只能经 shell(`sed -n`、`cat` 等);Ballast 把 Bash PostToolUse 解析出的只读目标并入"已读集合" |
| Stop | payload 直接带 `last_assistant_message` |
| allow | 必须是 `{}` 或空;`permissionDecision:"allow"` 会报错——所以 Ballast 从不输出 allow |
| deny | PreToolUse:`permissionDecision:"deny"`;PermissionRequest:`hookSpecificOutput.decision = {"behavior":"deny","message":R}` |
| 注入 | `hookSpecificOutput.additionalContext`,内容为空时输出空 |
| 触发范围 | hook 只在**交互式 TUI** 触发,`codex exec` 不触发(pinrule 观察) |

### 4.3 Cursor(二级,全部未核验)

**原生事件**(10 个,均未核验):sessionStart、beforeSubmitPrompt、preToolUse、beforeShellExecution(→ PreToolUse,工具 Bash)、beforeMCPExecution(→ PreToolUse,工具 `mcp__<server>__<tool>`)、beforeReadFile(→ PreToolUse,工具 Read)、postToolUse、afterAgentResponse(→ AgentResponse,仅记账)、stop、subagentStop。

**条目格式**:`~/.cursor/hooks.json` 为 `{"version":1,"hooks":{"<event>":[{"command":"<带引号的整条命令>"}]}}`;`stop`、`subagentStop` 条目另加 `"loop_limit":10`。

**payload / 协议假设**(来自 pinrule,未核验):

| 项 | 假设 |
|---|---|
| 会话 / 子 agent | 会话 ID 取 `session_id`,回退 `conversation_id`;子 agent 取 `agent_id`,回退 `subagent_id` |
| 工具名 | `Shell` → Bash;`Task` → Agent;Cursor 没有单独的 Edit,编辑也走 Write |
| 专属拦截点 | `beforeShellExecution` 的命令在 `command`;`beforeReadFile` 的路径在 `file_path` 或 `path`;`beforeMCPExecution` 的工具由 `server` 与 `tool_name` 拼出 |
| deny / ask | `{"permission":"deny"\|"ask","user_message":R,"agent_message":R}` |
| 注入 | `sessionStart`、`postToolUse` 用 `{"additional_context":C}`;`beforeSubmitPrompt` 沿用 Claude 的嵌套 `hookSpecificOutput.additionalContext` |
| **Stop** | **不能阻止停止**。规则里的 Stop / SubagentStop `deny` 被渲染成 `{"followup_message":R}`,让 agent 自动接着跑(**续跑**语义),配合 `loop_limit` 限制次数;`lint` 对此给 `cursor-stop-followup` 警告 |
| `afterAgentResponse` | 带 `text`;Ballast 只把它存进会话状态,供后面的 `stop` 规则使用(因为 Cursor 的 stop payload 可能没有 `last_assistant_message`) |
| 其他 | `transcript_path` 在 sessionStart 时通常为 null;安装后需要 Reload Window |

**托管规则文件**:`~/.cursor/rules/ballast.mdc`(front matter `alwaysApply: true` + 托管标记行 + 全文注入),整文件由 Ballast 托管,卸载时删除。它是**安装时按当时生效规则渲染的快照**,不随规则热更新(hook 通道才热更新)。规则变更后需重新运行 `ballast install --client cursor` 刷新(hooks.json 无变化,只重写 mdc);`ballast doctor` 的 `cursor.mdc_current` 检查它是否过期。安装时若当时没有任何生效规则,mdc 只有 front matter,`ballast install --client cursor` 会向 stderr 打印警告("放入规则后重新运行 install")。本版选择"重新安装刷新"这条路径,SessionStart 不做 mdc 过期检测。

### 4.4 Hermes(三级,尽力支持,离线核验,触发时机未核验)

**核验状态**:2026-10 用 `hermes hooks test`(Hermes 源码提交 9a0a162536,一次性 `HERMES_HOME`,不调用模型)核验了序列化形状、输出解析和阻断语义,证据见 `docs/facts/hermes-cases.json`。**离线核验(hooks test),触发时机未核验**:真实 agent 在什么时机、以什么 kwargs 调用 hook,需要模型会话(【需凭据】,补测命令见 `docs/facts/HERMES-CONCLUSIONS.md` §5)。因此下表各行的 Verified 含义是「形状、解析、阻断语义已核验」,不含「会在该时机触发」。

**原生事件**(6 个):on_session_start→SessionStart、pre_llm_call→UserPromptSubmit、pre_tool_call→PreToolUse、post_tool_call→PostToolUse、pre_verify→Stop、on_session_end→SessionEnd。`pre_verify` 在本回合改过代码、即将结束时触发;`on_session_end` 仍不是 Stop(它无法干预停止,也不带最终文本)。

**条目格式**:`~/.hermes/config.yaml` 顶层 `hooks:` 段:

```yaml
hooks:
  pre_tool_call:
    - command: "<带引号的整条命令>"
      timeout: 30
```

Ballast 不依赖 YAML 库,按行外科式编辑,**只认块状 YAML**;`hooks:` 段用 flow 风格或锚点时拒绝写入。段外字节原样保留,往返逐字节一致,包括 0 字节文件(不会被当作新建文件删除)和没有末尾换行的文件(`yaml:blank`、`yaml:noeol` 标记)。**拒绝裸 CR 换行**:文件里出现不属于 CRLF 的单独 `\r`(旧式 Mac 换行)时拒绝写入。需要用户以 `--accept-hooks` 启动 Hermes 表示同意(`hermes hooks list` 把未同意的条目标为 `not allowlisted`;`hermes hooks test` 不检查同意)。**安装器绝不写 `fail_closed`**:否则 Ballast 的任何失败(崩溃、二进制被卸载)都会阻断全部工具调用。

**payload / 协议**(离线核验):

| 项 | 内容 |
|---|---|
| stdin | `{hook_event_name, tool_name, tool_input(取自 args,非对象为 null), session_id, cwd, profile, extra{其余 kwargs}}` |
| 工具名 | `terminal`、`shell`、`execute_shell` → Bash;`read_file` → Read;`write_file`、`create_file` → Write;`patch`、`patch_file`、`edit_file` → Edit(`patch` 的 V4A 模式:信封里每个 `*** Update/Add/Delete File:` 与 `*** Move File: a -> b` 都是目标);`execute_code` 保持原名(规则可按名字匹配,但没有匹配器看得到脚本里的写入,见 trust-model §2.1) |
| `pre_llm_call` | 提示在 `extra.user_message`;首轮标志在 `extra.is_first_turn` |
| deny(`pre_tool_call`) | `{"decision":"block","reason":R}`(Hermes 同时接受 `{"action":"block","message":R}`);退出码 2 也阻断 |
| 注入 | 只有 `pre_llm_call` 能用 `{"context":C}`;其他事件输出为空 |
| 失败 | Ballast 的异常是「退出码 1 + 空 stdout」⇒ Hermes 放行;非 0 退出仍会解析 stdout,所以崩溃前绝不先打印决策 |
| **Stop(`pre_verify`)** | `extra={platform, model, coding, attempt, final_response, changed_paths}`;`final_response` ⇒ `last_assistant_message`,`attempt>0` ⇒ `stop_hook_active`。**deny 渲染为 `{"decision":"block","reason":R}`,Hermes 解析为 `{action:"continue",message:R}`,语义是续跑(agent 继续),不是阻止停止**,受 `agent.max_verify_nudges`(默认 3)限制;`lint` 给 `hermes-stop-continue` 警告 |
| 能力缺口 | 没有 SubagentStop / PostToolUseFailure;`post_tool_call` 只能记 `log`(其上的 block 应答被 Hermes 忽略) |

## 5. 事件映射速查

| 归一事件 | Claude Code | Codex | Cursor | Hermes |
|---|---|---|---|---|
| SessionStart | SessionStart | SessionStart | sessionStart | on_session_start |
| UserPromptSubmit | UserPromptSubmit | UserPromptSubmit | beforeSubmitPrompt | pre_llm_call |
| PreToolUse | PreToolUse | PreToolUse、PermissionRequest | preToolUse、beforeShellExecution、beforeMCPExecution、beforeReadFile | pre_tool_call |
| PostToolUse | PostToolUse | PostToolUse | postToolUse | post_tool_call |
| PostToolUseFailure | PostToolUseFailure | — | — | — |
| PostToolBatch | PostToolBatch | — | — | — |
| Stop | Stop | Stop | stop | pre_verify(语义为续跑) |
| SubagentStop | SubagentStop | — | subagentStop | — |
| SessionEnd | SessionEnd | — | — | on_session_end |
| AgentResponse(仅记账) | — | — | afterAgentResponse | — |

## 6. 如何把「未核验」变成「已核验」

1. 在**临时的**配置根(`CODEX_HOME`、`HERMES_HOME`,或 Cursor 的沙箱账号)下安装一个只记录 stdin 的探针 hook,不要碰真实用户配置。
2. 按 `scripts/probe/` 里 Claude Code 的做法设计用例(成功、失败、拦截、子 agent、Stop 等),抓取脱敏后的 payload 放进 `facts/payloads/`,把结论登记到 `facts/cases.json`。
3. 把 `model.NativeEventTable` 与 `model.Capabilities` 中对应单元格的 `Verified` 改为 `true`——这属于契约变更,走 [`architecture.md`](architecture.md) §12 的流程。

在此之前,请把 Codex、Cursor、Hermes 上的 `deny` / `ask` 当作"尽力而为",并以 CI 为准。
