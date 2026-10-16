# Hermes 离线取证结论(P2-HERMES)

> 单元:P2-HERMES(`docs/p2-design.md` §7.1、§9)。证据:`docs/facts/hermes-cases.json`(19 个用例:18 个 `pass`、1 个 `needs_credentials`)与 `docs/facts/payloads/hermes/<case>/`(脱敏、路径规范化)。复现:`bash scripts/probe-clients/hermes.sh`(`--write` 重写证据)。
>
> **证据种类**:`offline-hooks-test`。`hermes hooks test` 证明的是「序列化形状 + 输出解析 + 阻断语义」,**不证明**真实 agent 在什么时机、以什么 kwargs 调用 hook。因此本文及所有同步文本一律写作「离线核验(hooks test),**触发时机未核验**」;真实会话取证【需凭据】,补测命令见 §5。

## 1. 方法与环境

| 项 | 值 |
|---|---|
| Hermes 源码 | 本机源码的提交 `9a0a1625367242596d338ae2da541c4a1fc785a2`(2026-09-27),只用 `git archive HEAD` 读取,副本放在 `mktemp` 目录;**没有**符号链接或复制真实的 `~/.hermes`(含 `installs/`、`auth.json`、`config.yaml`) |
| 运行方式 | 设计 §7.1 方案 (b):`uv venv --python 3.14` + `uv pip install -e <源码副本>`(构建期联网安装依赖,运行期不需要);实测解释器 Python 3.14.8。未使用 Docker |
| 隔离 | `HOME`、`HERMES_HOME`、`BALLAST_HOME`、`BALLAST_INSTALL_ROOT`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`XDG_*`、`GIT_CONFIG_GLOBAL` 全部指向临时目录;Hermes 在 `env -i` 下运行,调用者的任何凭据变量都进不去;脚本有路径守卫,临时根之外一律拒绝 |
| 凭据 | 不需要:`hooks test` 不调用模型。没有读取、复制任何 token、API key 或 keychain |
| 探针 | 每个用例在 Ballast 之外并列装一个只记录 stdin 的探针 hook(`cat > $PROBE_OUT`),抓到的就是 Hermes 交给 hook 的原始 stdin。Ballast 的**精确**应答另用「同一份 stdin 重放进同一个二进制(状态取自该步之前的快照)」得到(Hermes 打印的 stdout 会截断到 400 字符) |
| Ballast 版本 | 当前工作树构建;「修复前」对照用基线提交 `cee8a4c963ce152a24e8da7b38cb289fa1b21ecc` 的 adapter 构建 |
| 规则 | 取自 `schema/examples/`(`no-long-sleep`、`no-tamper`)加一条脚本内定义的 Stop 规则 `probe-stop-deny`(只用于 `pre_verify` 用例) |

## 2. 结论表

| 用例 | 结论 | 对实现的影响 |
|---|---|---|
| `install-config` | `ballast install --client hermes` 写入 6 个 hook(`on_session_start`、`pre_llm_call`、`pre_tool_call`、`post_tool_call`、`pre_verify`、`on_session_end`),`timeout: 30`,**不写 `fail_closed`**。`hermes hooks list` 把它们都列为 `✗ not allowlisted`(同意是另一回事;`hooks test` 不需要同意) | `pre_verify` 作为新 `NativeEvent` 行(`Install: true`)落地 |
| `terminal-deny` | `pre_tool_call` + `terminal`(`sleep 300`):stdin 为 `{hook_event_name, tool_name, tool_input, session_id, cwd, profile, extra{task_id, tool_call_id}}`;Ballast 输出 `{"decision":"block","reason":"[no-long-sleep] …"}`(退出码 0),Hermes 解析为 `{action:"block", message:<reason>}` | 与原假设一致 |
| `write-file-tamper` | `write_file` 写 `<home>/.ballast/rules.json` 被 `no-tamper` 拦下 | 与原假设一致 |
| `patch-replace` | 修复前 adapter:`patch` 不在工具表里,没有任何以 `Edit` 为范围的规则看得到它,**漏判**(parsed: none)。修复后:`patch` ⇒ Edit,`Targets=[path]`,`no-tamper` 命中 | 工具表加入 `patch` |
| `patch-v4a` | `mode:"patch"` + `patch` 信封(含 `src/ok.go` 与 `<home>/.ballast/rules.json`):修复前漏判;修复后信封里每个 `*** Update/Add/Delete File:`(以及 Hermes 方言 `*** Move File: a -> b`)都是目标,第二个文件触发 `no-tamper` | `internal/adapter/hermes/patch.go`(复制自 codex 解析器并扩展,见 §3) |
| `execute-code` | `execute_code`(`args:{code}`)写 `<home>/.ballast/rules.json` **不会被拦**:工具保留原名(规则可按名字匹配),但没有任何匹配器看得到 Python 脚本里的写入 | 登记到 trust-model §2.1 第 1 类(见 §6 待同步文本) |
| `prellm-turns` | 首轮(`extra.is_first_turn:true`):`{"context":"Ballast rules (generation …):\n- [rule] …"}` 全文;同一会话下一轮:`{"context":"Ballast: [rule] …; …"}` 锚点(更短)。`pre_llm_call` 的 `extra` 为 `{user_message, conversation_history, is_first_turn, model, platform}` | 与原假设一致 |
| `post-tool-call` | `extra` 为 `{task_id, tool_call_id, result(JSON 字符串), duration_ms}`;Ballast 输出空、parsed none;会话状态里登记了一条 `PostToolUse`/`Bash`(`terminal` 已归一)。普通 `post_tool_call` 不写审计行(审计只有 hit/diag/eval_summary) | 与原假设一致 |
| `session-lifecycle` | `on_session_start`(`extra:{}`)与 `on_session_end` 都是空应答、parsed none | 与原假设一致 |
| `pre-verify` | `pre_verify` 无工具字段,`extra={platform, model, coding, attempt, final_response, changed_paths}`;Stop deny 渲染的 `{"decision":"block","reason":R}` 被 Hermes 解析为 `{action:"continue", message:R}`(**续跑**);审计里是 `event:"Stop"`、`native_event:"pre_verify"` 的 hit 与 eval_summary | `pre_verify` ⇒ `EventStop`;`final_response` ⇒ `LastAssistantMessage`;`attempt>0` ⇒ `StopHookActive`;Stop deny 置为支持;lint 警告 `hermes-stop-continue` |
| `pre-verify-dialects` | `pre_verify` 两种方言都等价:`{action:"continue",message}` 与 Claude Stop 方言 `{decision:"block",reason}`;`post_tool_call` 上的 block 应答被忽略(parsed none) | 无;确认只有 `pre_tool_call` 能阻断 |
| `exit2-blocks` | 退出码 2 ⇒ 阻断,stderr 为消息(与 stdout 无关) | 无 |
| `exit1-json-still-parsed` | 退出码 1 但 stdout 是 block JSON ⇒ 仍解析为 block | Ballast 的失败协议是「退出码 1 + **空** stdout」,保持;崩溃前绝不能先打印决策 |
| `exit1-empty-allows` | 退出码 1 + 空 stdout ⇒ 放行 | 同上,即 Ballast 的 fail-open |
| `output-dialects` | `pre_tool_call` 接受 Hermes 方言 `{action:"block",message}` 与 Claude 方言 `{decision:"block",reason}`,都译为 `{action:"block",message}` | Ballast 继续输出 Claude 方言 |
| `garbage-and-missing-fail-open` | stdout 不是 JSON、命令不存在(`command not found`)⇒ 默认放行 | 与 Claude 的 A7 同理 |
| `failclosed-semantics` | `fail_closed: true` 时命令不存在或 stdout 不是 JSON 对象 ⇒ **阻断** | 安装器**绝不能**写 `fail_closed`(`install-config` 与 `internal/adapter/hermes/evidence_test.go` 都断言了这一点):一旦写入,卸载二进制或一次崩溃就会阻断全部工具调用 |
| `fault-build` | `-tags ballast_fault` 构建 + `BALLAST_FAULT=decided:error`:退出码 1、stdout 空、stderr `ballast: fault injected at decided`,Hermes parsed none(放行);不设该变量的同一构建正常阻断 | 无 |
| `live-session-trigger-timing` | **`needs_credentials`**,见 §5 | — |

## 3. 对 `adapter/hermes` 与模型表的改动

按设计 §7.1「据此修正 adapter/hermes」落地(取证结果支持):

- **工具表**:`patch` ⇒ Edit(替换模式取 `path`;V4A 模式取信封里的全部目标,与 `path` 取并集,`path` 在前)。`patch_file`/`edit_file` 走同一路径。`execute_code` 保持原名。其余不变。
- **补丁解析器**:`internal/adapter/hermes/patch.go` 是 `internal/adapter/codex/patch.go`(该文件最后一次变更的提交 `b084d95cf79af695cbb08d3c91f027ba50f096bf`)的**复制**,复制时仓库提交为 `cee8a4c963ce152a24e8da7b38cb289fa1b21ecc`;`adapter/codex` 与 `adapter/common` 没有改动。相对原版的三处扩展(`testdata/patches.json` 里 20 行原用例保留,仅 1 行按 Hermes 语义改写并在行名里说明,另加 6 行 Hermes 专用):
  1. Hermes 方言 `*** Move File: a -> b`(源与目的都是目标)。
  2. 全文表头扫描:Hermes 自己的路径检查用整段文本的行正则(`tools/file_tools.py` 的 `_V4A_*_HEADER_RE`)收集表头,而它的应用器在第一条严格的 `*** End Patch` 处停止;Codex 版解析器对 `*** End Patchx` 这类相似行也会提前结束,会漏掉后面的目标。扫描把第 0 列出现的任何表头并入目标集(取超集:宁可多报,不能漏报)。
  3. 空白判定与 Python 一致:Hermes 用 Python 正则(`\*\*\*\s*Update\s+File:\s*(.+)`)识别表头并以 `str.strip()` 处理路径,Python 的 `\s` / `isspace()` 把 U+001C..U+001F 也算空白,Go 的 `strings.Fields`/`TrimSpace` 不算。`patch.go` 的全部空白判定(标记归一化、全文扫描、路径修剪、`Move File` 拆分)统一走 `isPySpace`(`unicode.IsSpace` 加 U+001C..U+001F)。并对第 0 列上「忽略空白、控制符与格式符后像表头、却解析不出路径」的行给出诊断(`looks like a file header`)。已用 Hermes 提交 `9a0a162536` 的 `parse_v4a_patch` 对 4000 条带随机空白/控制字符的表头做差分比对,Python 识别的目标 Go 侧无一遗漏(Go 侧为超集)。
- **`pre_verify`**:`NativeEventTable[hermes]` 新增行(`EventStop`,`Install: true`);`hermesCaps` 的 `RuleEvents` 加入 `Stop`,Stop deny 置为支持;`Render` 无需改动(deny ⇒ `{"decision":"block","reason":R}`,warn/ask 在 Stop 上仍不支持)。
- **Verified**:`NativeEventTable[hermes]` 六行与三个能力单元格(PreToolUse deny、UserPromptSubmit warn、Stop deny)的 `Verified` 置为 `true`;单元格 `Note` 写明「离线核验(hooks test),触发时机未核验」。`evidence_test.go` 要求每个 `Verified` 行都被某个 `pass` 用例的 `events` 覆盖。
- **lint**:`internal/dsl/lint.go` 只追加警告码 `hermes-stop-continue`(Hermes 上 Stop 的 deny 是续跑,不是阻止停止;受 `agent.max_verify_nudges` 限制,默认 3),判定分支与 Cursor 的 `cursor-stop-followup` 平行。

## 4. 与设计 §7.1 协议表的逐行核对

| 项 | 设计所列(静态依据) | 离线运行结果 |
|---|---|---|
| stdin 形状 | `{hook_event_name, tool_name, tool_input, session_id, cwd, profile, extra}` | 一致。`tool_input` 取自 `args`(非对象时 null,`pre_verify`/`pre_llm_call`/生命周期事件都是 null);`extra` 是其余 kwargs |
| 阻断事件 | 只有 `pre_tool_call` | 一致(`post_tool_call` 上的 block 被忽略) |
| `pre_tool_call` 输出 | Hermes 与 Claude 两种方言 | 一致 |
| 退出码 | 2 ⇒ 阻断;其他非 0 仍解析 stdout;spawn 错误/超时默认放行,`fail_closed` 时阻断 | 退出码 2、非 0 仍解析、spawn 错误放行、`fail_closed` 阻断均已运行确认;**超时**只有静态依据(没有跑 60 秒级超时) |
| 注入 | 非阻断事件默认按 `{"context":…}` 解析 | 一致(只对 `pre_llm_call` 使用) |
| 工具名 | `terminal`、`read_file`、`write_file`、`patch`、`search_files`、`execute_code` | `terminal`、`write_file`、`patch`、`execute_code` 已用 `hooks test --for-tool` 跑过;`read_file`、`search_files` 只有静态依据(归一逻辑由单元测试覆盖) |
| 额外事件 `pre_verify` | 续跑语义的 Stop | 确认:`final_response`/`attempt`/`changed_paths` 字段、两种方言、Stop deny ⇒ `{action:"continue"}`;`agent.max_verify_nudges` 上限只有静态依据 |

## 5. 仍未核验 / 需凭据

- 【需凭据】`live-session-trigger-timing`:真实会话里每个 hook 在什么时机触发、kwargs 是否与 `hooks test` 的合成载荷一致、被解析为 block 的工具调用是否真的不执行、`pre_verify` 的续跑次数是否受 `agent.max_verify_nudges` 限制。补测命令(用户在临时 `HERMES_HOME` 里先登录某个模型提供方,然后执行;Ballast 的 `ballast` 需在 `PATH` 上):

```sh
HERMES_HOME="$(mktemp -d)" BALLAST_HOME="$(mktemp -d)" sh -c 'ballast install --client hermes --binary "$(command -v ballast)" --force && hermes chat --oneshot --accept-hooks -q "Run the shell command: sleep 300"'
```

  预期:`sleep` 被拒绝并带 `[no-long-sleep]` 文字;事后检查 `$BALLAST_HOME/audit`。
- 【未核验】Hermes 真实 `pre_llm_call` 的 `conversation_history` 在长会话里可能很大(`hooks test` 里是空数组);Ballast 只解码顶层键,理论上线性代价,但没有用大载荷测过 30ms 门槛。集成时可用 `scripts/bench.sh` 的大载荷场景补测。
- 【未核验】`action:"modify"`(改写工具参数)与 `action:"approve"`(转人工审批)Ballast 从不输出,没有取证;`subagent_stop`、`on_session_finalize`、`post_llm_call` 等其他事件没有映射。
- 【未核验】原生 Windows:本探针只支持 macOS/Linux。

## 6. 待同步文本(交接给 P2-CLI,逐字同步;`docs/clients.md` 与 `docs/trust-model.md` 由 P2-CLI 独占)

> P2-CLI 在 `scripts/accept-p2.sh` 的卫生检查里断言 `docs/clients.md` 含「触发时机未核验」(设计 §9)。

### 6.1 `docs/clients.md` 开头「核验」的含义(第 5 行引用块)末尾追加

```
**Hermes 的特殊状态(P2)**:Hermes 做过**离线核验**——用 `hermes hooks test`(Hermes 源码提交 9a0a162536)在一次性 `HERMES_HOME` 里跑过序列化形状、输出解析与阻断语义,证据在 [`facts/hermes-cases.json`](facts/hermes-cases.json)、[`facts/HERMES-CONCLUSIONS.md`](facts/HERMES-CONCLUSIONS.md);**触发时机未核验**(真实 agent 何时、以什么参数调用 hook 需要模型会话,【需凭据】,补测命令见结论文档)。
```

### 6.2 `docs/clients.md` §1 客户端表 Hermes 行的最后一格

把「全部未核验」改为:

```
离线核验(hooks test),触发时机未核验
```

### 6.3 `docs/clients.md` §2 能力矩阵 Hermes 列

- PreToolUse / deny:`✓?` 改为 `✓(离线核验,触发时机未核验)`。
- UserPromptSubmit / warn:`✓?(仅 pre_llm_call)` 改为 `✓(仅 pre_llm_call;离线核验,触发时机未核验)`。
- Stop / deny:`—` 改为 `✓(仅 pre_verify;**语义为续跑**,见 §4.4;离线核验,触发时机未核验;lint 给 hermes-stop-continue 警告)`。
- Stop / warn 仍为 `✗`;SubagentStop 两行仍为 `—`。

### 6.4 `docs/clients.md` §4.4 Hermes 小节整体替换为

````
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
````

### 6.5 `docs/trust-model.md` §2.1「未覆盖的写法」第 1 类追加

在第 1 项末尾(`gawk -i inplace`;之后、该行结束的分号之前)追加:

```
Hermes 的 `execute_code`(模型提交的 Python 脚本在 Hermes 的代码执行环境里运行,脚本内部直接 `open(...).write()` 不会再产生 `write_file`/`patch` 的 `pre_tool_call`;Ballast 把它保留为原名 `execute_code`,规则可以按名字匹配它,但 `no-tamper` 看不到其中的写入)
```

### 6.6 不在 §9 交接范围、供架构师与 P2-CLI 参考

- `docs/dsl-semantics.md` §11 诊断码表:新增一行 `hermes-stop-continue | 警告 | Hermes Stop(pre_verify)的 deny 语义为续跑`;§3.x 中提到 `cursor-stop-followup` 的句子旁补一句 Hermes。
- `docs/architecture.md` §4.1 矩阵 Hermes 行、§4.3 事件映射表(加 `pre_verify→Stop`)、§4.4 工具表(加 `patch`、`execute_code`)、§5 修订记录(`NativeEventTable[hermes]` 新增 `pre_verify`,`Install: true`)。
- `internal/adapter/doc.go` 的 NormalizeTool 说明里 Hermes 工具表(`patch_file/edit_file ⇒ Edit`)需补 `patch`(该文件不归 P2-HERMES)。

## 7. 本单元越出独占目录的连带改动(需集成时确认)

这些改动是设计 §7.1 预先批准的契约变更的必然结果,但落在别的单元的文件里,已做最小修改并在返回说明中列出:

- `internal/model/client.go`、`internal/model/capability.go`:只改 `ClientHermes` 的表项与能力集(§9 明确允许)。
- `internal/dsl/arbitrate_test.go`、`internal/dsl/decode_test.go`、`internal/dsl/testdata/lint_cases.json`:三个旧用例把「Hermes 没有 Stop」「Hermes 的 UserPromptSubmit warn 未核验」写成了期望;契约变更后这两个前提不再成立。改法:换用 Hermes 上仍不存在的 `SubagentStop` 保留「事件缺失则降级」的覆盖;「未核验」期望改为无诊断。没有删除任何用例或放宽任何断言。
- `internal/install`(P2-INSTALL 的目录,**未改**)的测试会因为新增 `pre_verify` 行而变红:`TestPlanGolden/hermes_absent` 与另外几个 golden 需要加一个 `pre_verify` 条目,并发测试里的 manifest 条目数 30 ⇒ 31。见返回说明。
