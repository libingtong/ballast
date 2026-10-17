# Codex 静态依据(源码阅读)

- 日期:2026-10-16。性质:**【静态依据】**。只读了开源源码,**没有启动任何 Codex、没有任何凭据、没有联网调用模型**。运行取证见 `scripts/probe-clients/codex.sh`(需要用户在临时 `CODEX_HOME` 里自行登录),在此之前 Codex 的全部单元格保持【未核验】,`model.NativeEventTable` / `model.Capabilities` 里 Codex 的 `Verified` 不动。
- 源码:`openai/codex` 仓库,按固定 tag 浅克隆(只读):
  - `rust-v0.130.0`,提交 `58573da43ab697e8b79f152c53df4b42230395a8`(2026-05-08)。pinrule 引用的信任哈希算法对应的版本。
  - `rust-v0.163.0`,提交 `83086188ae0614bd882818ff1aa23f3194962012`(2026-10-15)。本次阅读时的最新稳定 tag。
- 阅读范围(仓库内路径均在 `codex-rs/` 下):`hooks/src/engine/{discovery,output_parser,command_runner}.rs`、`hooks/src/events/*.rs`、`hooks/src/config_rules.rs`、`hooks/schema/generated/*.json`、`config/src/{hook_config,fingerprint}.rs`、`utils/home-dir/src/lib.rs`、`core/src/tools/hook_names.rs`、`core/src/tools/handlers/{shell,shell/*,unified_exec/exec_command,apply_patch,mcp}.rs`、`core/src/tools/{sandboxing,orchestrator,context}.rs`、`core/src/session/mod.rs`、`features/src/lib.rs`。0.163 只对与下表相关的文件与 0.130 做了差异阅读,没有通读。
- 复现:`git clone --depth 1 --branch rust-v0.130.0 https://github.com/openai/codex`(再换 tag),按上面的路径阅读。
- 对照对象:`docs/clients.md` §4.2 的 Codex 假设与 `internal/install`、`internal/adapter/codex` 的实现。

## 1. 逐项对照

结论列:**一致**(源码支持 Ballast 现有假设)、**不一致**(假设与源码不符)、**补充**(假设没覆盖的事实)。

| # | 项 | Ballast 现有假设 | 源码所示(0.130) | 0.163 的差异 | 结论 |
|---|---|---|---|---|---|
| 1 | hooks.json 结构 | `{"hooks":{"<Event>":[{"hooks":[{"type":"command","command":C,"timeout":30}]}]}}`,不带 matcher | 同:`HooksFile{hooks: HookEventsToml}`,每个事件是 `MatcherGroup{matcher?, hooks[]}`;处理器 `{type:"command", command, timeout(秒,缺省 600), async, statusMessage}`。`UserPromptSubmit`、`Stop` 忽略 matcher | 顶层加了 `deny_unknown_fields`:只允许 `description` 与 `hooks` 两个键;处理器多了 `commandWindows`、`additionalContextLimit`,新增 `mcp_tool` 类型;`async` hook 可运行(但不能产生控制效果) | 一致。Ballast 只写 `type/command/timeout`,在两个版本都合法。**注意**:往 hooks.json 顶层加别的键会在 0.163 被整体拒绝 |
| 2 | 事件集合 | 6 个:SessionStart、UserPromptSubmit、PreToolUse、PermissionRequest、PostToolUse、Stop;"没有 compact、SubagentStop" | 8 个:再加 `PreCompact`、`PostCompact`(只有 universal 输出,不能阻止) | 12 个:再加 `SessionEnd`、`SubagentStart`、`SubagentStop`、`Interrupt`(`SessionEnd`/`Interrupt` 默认 1 秒超时、上限 3 秒) | **补充**。`docs/clients.md` §4.2 "没有 PostToolUseFailure、SubagentStop、compact 事件" 在两个版本都不成立(PostToolUseFailure 仍无)。Ballast 不依赖这些事件,但文档表述需要更新(交 P2-CLI) |
| 3 | `[features] hooks` | 安装器确保 `[features] hooks = true` | 特性 key `hooks`(旧名 `codex_hooks`),`Stage::Stable`,**`default_enabled: true`** | 同 | **补充**:全新配置下 hooks 默认就开着,`features.hooks` 只有在被显式设成 `false` 时才需要改。Ballast 对 `absent`/`false`/`true` 三种原值的处理仍然正确,只是"absent 时写入 true"不是必需的 |
| 4 | 信任哈希算法 | `CodexTrustHash`:`sha256` of 键排序紧凑 JSON `{"event_name":<snake>,"hooks":[{"async":false,"command":C,"timeout":T,"type":"command"}]}`,前缀 `sha256:` | `NormalizedHookIdentity{event_name, MatcherGroup}`,处理器先规范化(`timeout` 缺省 600、最小 1;`statusMessage` 为 None 时省略;`matcher` 为 None 时省略)→ 转 TOML 值 → 转 JSON 值 → **递归按键排序** → `serde_json::to_vec`(紧凑,不转义非 ASCII)→ SHA-256 → `sha256:<hex>` | 规范化多了 `commandWindows: None`(省略)与 `additionalContextLimit`(等于默认值时丢弃)。排序方式换成 `sort_all_objects`,结果相同。**身份字段集合不变** | **一致**。`internal/install` 的 `CodexTrustHash` 与上述算法在 Ballast 写出的字段范围内等价(无 matcher、无 statusMessage、`async:false`、显式 `timeout`)。脚本 `codex.sh --self-test` 用独立的 Python 实现复算了 `internal/install/hash_test.go` 的三个向量与真实 `ballast install --codex-trust` 写出的 6 条哈希,全部相同 |
| 5 | 信任 key | `hooks.state.'<hooks.json 绝对路径>:<snake 事件>:<组下标>:<处理器下标>'`,条目下标变化后重算 | `hook_key(key_source, event, group_index, handler_index)` = `"{path}:{event_label}:{g}:{h}"`,`path` 是 `source_path.display()`;状态只从 **User** 与 **SessionFlags** 层(即 `$CODEX_HOME/config.toml`)的 `[hooks.state."<key>"]{enabled, trusted_hash}` 读取,项目层、托管层、插件层不能写用户状态 | key 格式不变 | **一致**,但见下一行 |
| 6 | `CODEX_HOME` 规范化 | 安装器直接使用 `CODEX_HOME` 的字面值拼 key | `find_codex_home` 对 `CODEX_HOME` 调用 `canonicalize()`(解析全部符号链接),hooks.json 的路径、也就是信任 key 里的路径,用规范化后的形式 | 同 | **不一致**。`layout.Resolve` 与 `internal/install` 都不规范化,所以只要 `CODEX_HOME`(或默认的 `~/.codex`)路径里含符号链接,Ballast 写的 key 与 Codex 计算的 key 不同,`trusted_hash` 永远对不上,hook 停在 *Untrusted*,**静默不运行**,直到用户在 `/hooks` 里手动批准。可复现:macOS 上 `mktemp -d` 给出 `/var/folders/...`,实际规范路径是 `/private/var/folders/...`,本仓库构建的 `ballast install --client codex --codex-trust` 写出的 key 是前者。dotfiles 管理的 `~/.codex` 符号链接同理。见 §3【契约问题】 |
| 7 | PreToolUse 退出码与 stdout | "异常 = exit 1 + 空 stdout" 即放行 | 只有退出码 0 才解析 stdout(空 = 无操作;像 JSON 但解析失败 = hook 失败,**不阻止**);退出码 2 且 stderr 非空 = 阻止,原因取 stderr;退出码 2 且 stderr 为空 = hook 失败(**不阻止**);其他退出码、spawn 失败、超时 = hook 失败,**不阻止**,且 stdout 不解析 | 同(退出码 2 增加 `can_apply_control_effects()`:仅同步 hook;Ballast 的是同步的) | **一致**。与 Claude 不同:Codex 在 exit 1 时**不会**解析 stdout 里的 deny,所以 Ballast 的放行约定在 Codex 上不依赖"空 stdout",但保持空 stdout 仍是对的 |
| 8 | PreToolUse deny 形态 | `hookSpecificOutput.permissionDecision:"deny"` + reason | 接受 `hookSpecificOutput{hookEventName:"PreToolUse", permissionDecision:"deny", permissionDecisionReason}`(**`hookEventName` 必填**,reason 必须去空白后非空,否则整个输出判为无效 → hook 失败 → **不阻止**);也接受旧式 `{"decision":"block","reason":R}`(reason 必填);`permissionDecision:"allow"`、`"ask"`、`updatedInput`、`continue:false`、`stopReason`、`suppressOutput` 都是"不支持",使 hook 失败 | 新增:`allow` **仅在同时带 `updatedInput`** 时合法;`ask` 仍不支持 | **一致**("allow 必须是 `{}` 或空"、"ask 不渲染")。**注意**:deny 的 reason 为空时 Codex 会放行;Ballast 的 reason 由 `[id] message` 拼成,正常非空,但应保证 deny 永远带非空 reason |
| 9 | PermissionRequest | deny:`hookSpecificOutput.decision={"behavior":"deny","message":R}` | 输入字段:`session_id, turn_id, transcript_path, cwd, hook_event_name, model, permission_mode, tool_name, tool_input`(**没有 `tool_use_id`**);输出 `hookSpecificOutput{hookEventName:"PermissionRequest", decision{behavior:"allow"\|"deny", message?}}`,`updatedInput`/`updatedPermissions`/`interrupt:true` 使 hook 失败;deny 无 message 时用默认文案。只在**需要批准**时触发(orchestrator 里优先级最高),shell、`apply_patch` 与 MCP 工具批准都会走它;`PermissionRequest` 的 shell 载荷是 `tool_name:"Bash"`、`tool_input{command, description?}` | 输出 schema 多了几个可选字段 | **一致** |
| 10 | 工具名与 `tool_input` 形状 | `apply_patch` → Edit;`exec_command`、`shell` → Bash,命令在 `cmd` 字段;argv 形式 `["apply_patch",…]` 按 apply_patch | **所有 shell 类工具**(`shell`、`local_shell`、`shell_command`、`container.exec`、统一执行的 `exec_command`)对 hook 一律报 `tool_name:"Bash"`、`tool_input{"command": <字符串>}`(argv 用 `shlex_join` 拼成一条字符串;`exec_command` 的 `cmd` 被改名为 `command`)。`apply_patch`:`tool_name:"apply_patch"`、`tool_input{"command": <补丁全文>}`;`Write`/`Edit` 只是 **matcher 别名**,载荷里仍是 `apply_patch`。MCP:`tool_name` 为显示名,`tool_input` 为解析后的参数对象(解析失败时是字符串) | 新增 `spawn_agent`(matcher 别名 `Agent`) | **不一致(文档层面)**。`internal/adapter/codex` 同时认 `Bash`、`exec_command`、`shell` 等名字,命令键认 `cmd`/`command`,补丁键含 `command`,所以**实现照常工作**;但 `docs/clients.md` §4.2 "命令在 `cmd` 字段"、"argv 数组形态" 的描述与源码不符(argv 数组在 hook 载荷层不会出现)。通过 shell 调用的 `apply_patch <<EOF` 在 hook 层仍是 Bash(已知缺口,不变) |
| 11 | PostToolUse | 无 PostToolUseFailure;读取经 Bash 的 PostToolUse | 输入含 `tool_name, tool_input, tool_response, tool_use_id`;`tool_response` 对 shell 类工具是**一个 JSON 字符串**(格式化后的截断输出,**没有数值退出码**),`apply_patch` 同为字符串,MCP 为序列化的 `CallToolResult`。只有处理器返回 Ok 时才产生载荷。输出:`decision:"block"`+reason、`hookSpecificOutput.additionalContext`;`updatedMCPToolOutput` 不支持 | 工具事件输入多了可选的 `agent_id`、`agent_type` | **一致**。非 0 退出是否仍走 PostToolUse(处理器对"命令失败"返回 Ok 还是 Err)源码层面没有逐路径追踪 → 运行取证用例 `bash-fail` |
| 12 | Stop | payload 直接带 `last_assistant_message`;deny 阻止结束 | 输入:`session_id, turn_id, transcript_path, cwd, model, permission_mode, last_assistant_message(可为 null), stop_hook_active`;输出 `decision:"block"` + 非空 reason = **继续**(把 reason 交给 agent),退出码 2 + stderr 同样是续跑反馈,`continue:false` + `stopReason` 终止 | 同,另有 `SubagentStop` | **一致**。Stop 的 deny 是"续跑"语义(与 Claude、Cursor 相同) |
| 13 | SessionStart / UserPromptSubmit | 注入 `hookSpecificOutput.additionalContext` | SessionStart 输入含 `source ∈ {startup, resume, clear}`;UserPromptSubmit 输入含 `prompt`;两者输出都接受 `hookSpecificOutput{hookEventName, additionalContext}`,UserPromptSubmit 还接受 `decision:"block"` | `SessionStart` 的 `source` 枚举增加 `compact`、`fork`;`additionalContext` 超过约 2500 token 会落盘预览(`additionalContextLimit`) | **一致** |
| 14 | 公共输入字段 | `session_id`、`cwd` | 全部事件:`session_id, transcript_path(可为 null), cwd, hook_event_name, model`;除 PreCompact/PostCompact 外还有 `permission_mode ∈ {default, acceptEdits, plan, dontAsk, bypassPermissions}`;除 SessionStart 外还有 `turn_id`;工具事件有 `tool_name, tool_input` 与(PermissionRequest 除外)`tool_use_id` | 工具事件多了可选的 `agent_id`、`agent_type` | **补充** |
| 15 | 触发范围 | "hook 只在交互式 TUI 触发,`codex exec` 不触发(pinrule 观察)" | hooks 引擎在会话构建时由 `Feature::CodexHooks` 一个开关决定(`core/src/session/mod.rs`),`exec` crate 里没有 hook 相关代码,**没有发现按"是否交互"区分的开关** | 同 | **未能证实**(源码不支持 pinrule 的说法,也不能由此断言 exec 会触发)。保持【未核验】,运行取证用例 `exec-noninteractive` |
| 16 | 发现层与托管 | (未涉及) | 用户层 `$CODEX_HOME/hooks.json`、项目层 `<repo>/.codex/hooks.json`、系统/MDM 托管层;**托管层 hook 自动视为已信任**(`HookTrustStatus::Managed`),其余层必须有匹配的 `trusted_hash`;同一层同时有 hooks.json 与 config.toml `[hooks]` 时给出警告 | 增加 `PackagedDefaults` 层与插件内置 hook | **补充**:`trust-model` 里"Codex 托管 hook 不能被用户关闭"的结论如要成文,需要单独核验托管层的 `enabled` 语义(源码里 `is_managed` 时 `enabled` 恒为真);本次不展开 |

## 2. 对运行取证的预测

下表是 `codex.sh` 各用例的静态预测,运行后与 `docs/facts/codex-cases.json` 对照;偏离即"文档说法被推翻",写入 `result: "fail"`。

| 用例 | 静态预测 |
|---|---|
| `hooks-trust` | `--codex-trust` 写入的 Ballast 哈希与脚本为探针算出的哈希在 `/hooks` 里都显示已受信(`CODEX_HOME` 已被脚本规范化,避开 §1 第 6 行) |
| `bash-ok` / `bash-fail` | `tool_name:"Bash"`、`tool_input.command` 为字符串;`tool_response` 为字符串、不含数值退出码;非 0 退出是否触发 PostToolUse 待测 |
| `apply-patch` | `tool_name:"apply_patch"`,`tool_input.command` 为补丁全文;需要批准时另有 PermissionRequest |
| `pre-deny-json` / `pre-deny-legacy` / `pre-deny-exit2` | 都阻止命令(工作目录里不出现文件) |
| `pre-exit1-deny-stdout` | **不阻止**(退出码 1 不解析 stdout) |
| `permission-record` / `permission-deny` | 需要批准时触发 PermissionRequest;`behavior:"deny"` 直接拒绝,不弹批准提示 |
| `userprompt-context` / `session-start` | `additionalContext` 到达模型 |
| `stop-block` | 第一次 Stop 被 block 后 agent 继续;第二次 Stop 的 `stop_hook_active` 为 `true` |
| `exec-noninteractive` | 无预测(§1 第 15 行) |

## 3. 契约问题与建议(不修改契约,实现按字面)

1. **【契约问题】Codex 信任 key 必须使用规范化后的路径。(已处理:集成后第 3 轮系统审计,`EntrySpec.CodexHomeReal`,见 `docs/p2-design.md` §13 v1.5.7;下文保留原始描述。)** `internal/install` 的 `codexConfigTarget.desiredTrust` 用 `jsonHooksTarget.path`(来自 `layout.CodexDir`,即 `CODEX_HOME` 的字面值)拼 `hooks.state` 的 key,而 Codex 对 `CODEX_HOME` 做 `canonicalize()` 再计算同一个 key。最小建议:`--codex-trust` 写 key 时对 hooks.json 路径做 `filepath.EvalSymlinks`(找不到时回退字面值),manifest 里仍记录字面路径;`doctor` 的信任检查用同一个规范化路径;补一个"CODEX_HOME 经符号链接"的测试(macOS 上 `t.TempDir()` 本身就是 `/var/folders/...`,很容易复现)。归属 P2-INSTALL(`internal/install/**`)。在此之前,`codex.sh` 把规范化后的 `CODEX_HOME` 传给 `ballast install`,避开这个问题。
2. **文档同步(交 P2-CLI,`docs/clients.md` §4.2 与 `docs/trust-model.md`)**:工具名统一为 `Bash`、命令键为 `command`;argv 数组形态不会出现在 hook 载荷里;事件集合比表中多(`PreCompact`/`PostCompact`,0.163 起还有 `SessionEnd`/`SubagentStart`/`SubagentStop`/`Interrupt`);`hooks` 特性默认开启;hooks.json 顶层 0.163 起只允许 `description` 与 `hooks`;exit 1 不解析 stdout。
3. **实现提示(不改契约)**:Codex 的 deny reason 必须非空,否则整条输出作废并放行。`internal/adapter/codex` 渲染 deny 时应保证 `Reason` 非空(目前由 `[id] message` 拼成,实际非空);P2-HOOK/P2-CC 如有机会补一个"空 reason 的 deny 在 Codex 上回退为非空占位"的单测。

## 4. 仍未核验

- 以上全部是静态阅读,没有观察到一次真实的 hook 调用。
- 信任哈希在 0.130 与 0.163 的实现等价,但**其他版本**未读;Codex 的 hook 同意机制仍在快速演进(0.163 新增了内置/插件层的信任豁免)。
- 非 0 退出的 PostToolUse、交互与非交互的差异、`/hooks` 面板对 Ballast 条目的显示、批准提示与 PermissionRequest 的交互,都要靠 `scripts/probe-clients/codex.sh` 在真实 TUI 里取证。补测命令:

```sh
CODEX_HOME=$(mktemp -d) BALLAST_HOME=$(mktemp -d) scripts/probe-clients/codex.sh
```
