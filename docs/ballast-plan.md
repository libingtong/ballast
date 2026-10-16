# Ballast(压舱石)实施计划 v0.4

> v0.5(2026-10-16):核心路径完成并通过系统审计,P2 计划见末尾"v0.5 P2 计划"(含 v0.5.1–v0.5.7 审核修订),详细设计见 `docs/p2-design.md`。

> v0.4(2026-10-16):M0a/M0c 实测完成,并按用户要求加入多客户端支持(参照 pinrule)。改动见末尾"v0.4 变更",与正文冲突时以 v0.4 变更为准。

> v0.2 根据 `docs/ballast-plan-review.md`(R01–R20,5 blocker / 13 major / 2 minor)重写。逐条修订去向见末尾"审核意见追溯表"。
> 用户已裁决(2026-10-15):信任模型 = **分级边界**;语言 = **Go**;首发 OS = **macOS + Linux + WSL2 + 原生 Windows**;M0 = **用户层探针 + 托管层容器测试都做**。
>
> v0.3(2026-10-16)按用户意见修改了四处:
> - 证据快照改为复制真实索引,只看已跟踪文件,S 和 C 两边用同一套 exempt 过滤;部分暂存采用**严格语义**。
> - verify 一律后台运行,不再走前台 `--await`;去掉对 `updatedInput` 的依赖;明确 git pre-commit 和服务端 CI 是最终把关。
> - 里程碑分出**核心增量路径**:M0a → M1 → M2 → M3(stats + replay)。其余项设计不变,顺延到 P2。
> - A10、A24 补充文档原文。
>
> 改动明细见末尾"v0.3 变更"。

## Context
团队长任务里,Claude Code 会逐渐忘记或绕开少量关键规则。pinrule 证明了"hook 注入 + 工具前检查"可行,但它的拦截力来自 8 个写死的 Python 函数:只有 deny 一档,证据靠正则猜测试输出,配置只有单层。Ballast 要做到以下几点:
- 声明式规则,不写代码就能新增拦截;
- 四档动作加影子模式;
- 以 `ballast verify` 的退出码和受测内容快照为证据;
- 三层配置,org 层不可覆盖;
- 可审计、可回放;
- 跨平台。

本文件只含计划,不含实现。**A 区**是环境事实,**B 区**是方案与验收。

---

# A 区:环境事实

来源标记:
- [D] 官方文档 code.claude.com/docs/en/{hooks,settings,managed-settings,sandboxing},2026-10-15 抓取。
- [L] 本机观察:Claude Code 2.1.300,Darwin 25.5.0,Go 1.27.2,Python 3.15.0。
- [R] 审核报告复测,见 `docs/ballast-plan-review.md` 的 E1–E4。

每条事实拆成"协议事实"和"可选性/边界"两部分。本机观察不等于 hook 实际收到的 payload。

| # | 协议事实 | 可选性 / 边界 / 本机观察 | 状态 |
|---|---|---|---|
| A1 | 公共输入字段名:`session_id, transcript_path, cwd, hook_event_name, prompt_id, scratchpad_dir, permission_mode, effort, agent_id, agent_type` [D] | 后 6 个字段**有条件出现**:`prompt_id` 在首次输入前缺失;`scratchpad_dir` 可能缺失;`permission_mode` 并非每个事件都有;`effort` 取决于模型和事件;`agent_*` 只在子 agent 内出现 → **全部按可选字段解析** | 文档已核验 |
| A2 | transcript 异步写入,可能滞后;Stop 和 SubagentStop 应使用 `last_assistant_message` [D] | — | 已核验 |
| A3 | SessionStart 的 `source` ∈ startup / resume / clear / compact / fork [D] | 尚未抓到真实 compact payload | 文档已核验,payload 待 M0a |
| A4 | SessionStart / UserPromptSubmit 等事件可输出 `additionalContext`;**每个字符串**上限 10,000 字符,超出部分落盘,只保留 2,000 字符预览 [D] | Ballast 自定更严的预算:单次注入总量 ≤ 8,000 字符 | 已核验 |
| A5 | UserPromptSubmit 输入 `prompt`;可输出 `additionalContext` 或 `decision:"block"+reason` [D] | — | 已核验 |
| A6 | PreToolUse 输出 `permissionDecision` ∈ allow / deny / ask / defer,另有 `updatedInput`、`additionalContext`;多 hook 冲突时 deny > defer > ask > allow [D] | **allow 会跳过权限提示**,所以 Ballast 在"无决策"时**绝不输出 allow**;ask 在 `-p` 非交互模式下可能被直接拒绝 | 已核验 |
| A7 | 退出码 2 = 阻塞;**退出码为 0、1 或其他非 2 值时,只要 stdout 是合法控制 JSON,就按 JSON 生效**("if a PreToolUse hook exits 1 but prints JSON that passes validation … the JSON fields decide");非 2 退出且没有合法 JSON 时为非阻塞错误;`onFailure:"block"` 可把失败变成阻塞 [D] | v0.1 写的"异常退 1 即放行"**不成立** → 采用 B7 的单一提交点协议 | 已核验(R05) |
| A8 | PostToolUse 的 `tool_response` 结构因工具而异,`duration_ms` 可选 [D] | 本机 transcript 中 Bash 结果的 dict:基础键为 `stdout, stderr, interrupted, isImage, noOutputExpected`;另见 `persistedOutputPath, returnCodeInterpretation, backgroundTaskId, timedOutAfterMs` 等。**没有数值型 exit code**。这只是 transcript 观察,不等于 hook payload | hook payload 待 M0a |
| A9 | PostToolUseFailure 在**被 Claude Code 判定为执行失败**时触发;`error` 首行通常是 `Exit code N`,但启动失败时可能没有这一行 [D] | **非 0 退出 ≠ 失败**:2.1.300 内置的分类函数把 grep / rg / diff / test / find 的 exit 1 判为非错误 [R E2]。证据机制不依赖这一路 | hook stdin 待 M0a |
| A10 | Stop 输入 `stop_hook_active, last_assistant_message`;`decision:"block"` 可阻止停止。连续阻止上限见 hooks 文档 Stop input 一节原文:"Claude Code applies an 8-consecutive-continuation cap: after stop hooks have continued the turn eight times in a row, Claude Code overrides the next block and ends the turn. The count of consecutive continuations resets each time Claude calls a tool. To raise the cap, set CLAUDE_CODE_STOP_HOOK_BLOCK_CAP." [D] | **Stop 的 `additionalContext` 会让会话继续**:原文称其"keeps the conversation going through the same loop protections as decision: "block"",即语义等同反馈,不是放行 | 文档已核验;运行期行为由 M0a 实测确认(case `stop-cap`) |
| A11 | PreCompact 不能注入上下文;compact 后用 SessionStart(compact) 恢复 [D] | UserPromptSubmit、PostToolBatch 等事件同样可以合法注入 | 已核验 |
| A12 | command hook 支持 `type, command, args, timeout, async` [D] | 拦截类 hook 必须同步;用 `command` + `args` 作为安装归属标识(B12) | 已核验 |
| A13 | 托管设置路径:macOS `/Library/Application Support/ClaudeCode/`、Linux/WSL `/etc/claude-code/`、Windows `C:\Program Files\ClaudeCode\`,另有 `managed-settings.d`/MDM 等 [D] | 托管值不能被低层覆盖,**但少数安全键例外**:低层更严的值仍生效。hook 另有合并语义(A15)。本机未部署 [L] | 已核验 |
| A14 | `allowManagedHooksOnly` 会阻止 user / project / local / plugin hook,但托管设置强制启用的插件 hook 例外;**低层 `disableAllHooks` 不能关闭托管 hook** [D hooks 配置章节原文] | 已由 M0b 核验(Claude Code 2.1.300,Linux 容器),见 `facts/M0b-CONCLUSIONS.md`:`managed-only-blocks-lower`、`lower-disableallhooks-vs-managed` | 已核验(M0b) |
| A15 | "Hook entries merge across settings levels rather than replacing each other" [D] | 托管 hook 与低层 hook 同时执行 → 托管部署时用户层**不得再装一份**(B12 安装检测) | 已核验 |
| A16 | ConfigChange 的 matcher 只覆盖 Claude 设置和 skills [D] | **不监视任意 Ballast 文件**;部分托管刷新不触发该事件 | 已核验 |
| A17 | transcript 为每行一个 JSON 的 jsonl,属内部格式,文档没有承诺稳定 | 2026-10-15 快照:顶层 1812 份,递归 2430 份(其中 618 份为子 agent);并非每类行都带全部元数据;有 708 条 `stop_hook_summary` [R E2] | 观察 |
| A18 | 启动成本基线 [L][R E4] | `/usr/bin/true` p95 ≈ 2ms;`python3 -I` p95 ≈ 18.8ms;`node` p95 ≈ 56ms。**只说明启动成本**,不能推出完整 hook 的耗时;Go 未实测 | Go 待 M0c |
| A19 | pinrule(HEAD 2aa78c3):恰好 8 个注册函数;`_TEST_CMD_RE` 不含 mvn / gradle / dotnet / swift;`_FAIL_RE` 会把 "0 failed" 判为失败;只有 deny 一档;单层配置 [R E3] | 正常路径的 100–180ms 耗时**未核验**(审核测的是损坏输入的路径) | 部分核验 |
| A20 | **沙箱只覆盖 Bash**,文件工具、MCP server 和 hook 都在沙箱外运行;**原生 Windows 没有沙箱**;可托管的键有 `sandbox.enabled`、`sandbox.filesystem.denyWrite`、`allowUnsandboxedCommands:false`、`sandbox.failIfUnavailable:true`、`permissions.deny`、`allowManagedPermissionRulesOnly` [D sandboxing/settings/managed-settings] | 这是 T1 边界的基础(B0);**hook 在沙箱外运行已由 M0b 核验**(`hooks-run-outside-sandbox` pass),见 `facts/M0b-CONCLUSIONS.md`;文件工具与 MCP 在沙箱外是 mock 佐证,真实模型调用下的效果【需凭据】 | 已核验(M0b;部分需凭据) |
| A21 | 并行工具调用时 PostToolUse **并发触发**;PostToolBatch 每批只触发一次 [D] | → B6 需要跨进程锁 | 已核验 |
| A22 | 子 agent 的工具事件触发同一套 hook,并携带 `agent_id/agent_type`;SubagentStop 额外提供 `agent_transcript_path`、`last_assistant_message` [D] | → B6 按 agent 划分作用域 | 已核验 |
| A23 | Windows 上文件工具给出的路径一律是带反斜杠的绝对路径(即使 hook 跑在 Git Bash 里);存在 PowerShell 工具,matcher 写作 `Bash\|PowerShell`;没有 Git Bash 时 hook 经 PowerShell 运行 [D] | → B11 | 已核验 |
| A24 | PreToolUse 的 `updatedInput` 可以改写工具输入,原文:"updatedInput directly under hookSpecificOutput replaces a tool's arguments before it runs" [D] | 文档没有说明是否必须与 `allow` 同时返回;唯一示例是与 allow 同用。**v0.3 已不依赖此字段**(B0 的 verify 链路改为后台运行) | 【未核验】,M0a 顺带实测 |

**对设计的关键含义**:
1. 证据只认 `ballast verify` 记录的退出码加内容快照(A8、A9)。
2. 异常放行必须靠"不输出控制 JSON",不能只靠退出码(A7)。
3. 同用户权限下 hook 无法守住状态文件,真实边界只能来自托管沙箱加权限规则(A20),原生 Windows 只能做到 best-effort。
4. 所有可选字段都要容错(A1、A8)。

---

# B 区:方案

## B0 信任模型:分级边界(R01)
| 层级 | 适用条件 | 能兑现的保证 |
|---|---|---|
| **T1 托管强制** | macOS / Linux / WSL2,且组织部署了托管设置 | **Agent 不可写**规则、状态、证据和二进制 |
| **T0 尽力而为** | 无托管部署,或原生 Windows | **篡改可检测、不可防**;文档、`doctor` 输出、审计中都明确标注 |

**T1 的组成**(由 `ballast org render` 生成托管配置片段,管理员自行下发,Ballast 不直接写系统路径):
- 托管 hooks:命令用 root 拥有的绝对路径,例如 `/usr/local/libexec/ballast/ballast`。不经过 PATH,也不调用 Agent 能改的脚本。
- `allowManagedHooksOnly:true`。
- `sandbox`:`enabled:true`、`allowUnsandboxedCommands:false`、`failIfUnavailable:true`、`excludedCommands:[]`,并在 `filesystem.denyWrite` 中列出受保护路径。
- `permissions.deny`:对受保护路径禁止 `Edit/Write/NotebookEdit`,并设置 `allowManagedPermissionRulesOnly:true`。
- MCP 白名单(托管 MCP 允许列表)。MCP server 在沙箱外运行,属于**残余风险**,见风险 2。

**受保护路径**:
- org 规则目录;
- `~/.ballast/`(state、evidence、audit、cache、backups、install-manifest);
- `<repo>/.ballast/`(rules.json、project.json);
- Ballast 二进制及其父目录;
- `~/.claude/settings*.json`、`<repo>/.claude/settings*.json`。

**verify 一律后台运行**,不再提供前台 `--await`(核心路径只实现 T0;T1 属于 P2):
- **T0**:`ballast verify [name]` 以 detached 方式启动 runner,**立即返回** job_id 后退出(exit 0)。runner 运行测试并写入证据,证据标注 `tier:"T0"`。
- **T1**:Agent 在沙箱中运行的 `ballast verify` 写不了证据,所以由 hook 代为发起:
  1. PreToolUse 识别规范形式的 `ballast verify <name>`:单条简单命令,没有管道、重定向或后台。
  2. hook 在沙箱外以 detached 方式启动 runner。
  3. hook 返回 `deny`,reason 写明"已在后台启动 job=<id>,用 `ballast verify --status` 查看"。
  4. runner 把测试命令本身放进 OS 沙箱执行,以免测试代码写受保护路径。沙箱实现在 P2 选型(macOS 用 `sandbox-exec`,Linux 用 bubblewrap,或复用 sandbox-runtime)。
  - 这条链路**不依赖 `updatedInput`**(A24)。
- `ballast verify --status [job|name]` 只读查询结果,在沙箱内也能运行。

**最终把关**:会话内的 `evidence-before-commit` 用于纠正 Agent 行为,不是交付质量的最终保证。可选的 git pre-commit 只调用 `ballast verify --check`,不在提交时重跑测试。**服务端 CI 才是最终裁判**,README 和 `doctor` 都会写明这一点。

`ballast doctor` 输出当前层级以及每个缺失的 T1 组件。

## B1 规则 DSL(R03、R04、R06、R08)
**M1 的第一项交付是冻结的 `schema/rules.v1.json` 和 `docs/dsl-semantics.md`**:默认规则和示例规则全部通过同一个解析器和 lint。

**规则字段**:
- `id`:`^[a-z0-9-]{3,40}$`。
- `enabled`:默认 true。
- `action`:默认 `log`。
- `target`:影子模式下的目标档位,默认取该事件允许的最严一档。
- `event`:单个事件,取值 PreToolUse / UserPromptSubmit / PostToolUse / PostToolUseFailure / Stop / SubagentStop。
- `match`、`when`、`message`(≤300 字符)、`inject`(≤400 字符)。
- `examples`:必填,至少一个正例和一个反例。

**求值契约**:
- `match` 中出现的各字段之间是 **AND**,字段内部的列表是 **OR**。lint 校验字段与事件、工具是否适用:例如对 `Edit` 写 `input_regex.command` 直接报错,不会运行期静默失效。
- `when` 数组表示 AND;支持 `all / any / not` 组合;比较写成 `{"gte":n}`、`{"lt":x}`。
- 所有谓词返回**三值**:true / false / unknown。只要出现 unknown,该规则就**不命中**(fail-open),并累加 `unknown` 诊断计数。
- 字段缺失时匹配结果为 false,不报错。
- 正则统一用 RE2。pinrule 里带反向引用或前后查找的模式需要改写,lint 拒绝无法编译的正则。
- **多条规则同时命中**:取该事件可用档位中最严的一档,message 按规则顺序拼接(受注入预算约束),所有命中都写入审计。

**匹配器**:
- `tool`:对工具名做 glob,支持 `mcp__*`。
- `path`:对规范化后的目标路径做 glob。路径来源包括 `file_path`、`notebook_path` 和多文件补丁的全部目标;多目标时"任一命中"。
- `input_regex`:对 `tool_input` 中的指定字段做正则。
- `bash` / `ps`:见下文。
- `text`:适用于 Stop、SubagentStop、UserPromptSubmit,字段如下:
  - `source`:`last_assistant_message` 或 `prompt`;
  - `preprocess`:`strip_code_blocks`、`strip_quotes`;
  - `window`:`{tail_chars:n}`;
  - `regex`、`not_regex`(列表,有序豁免);
  - `cjk_ratio`、`count_regex` 比较。

**谓词(封闭词表)**:
- `read_this_session(path)`:按 agent 作用域判断。
- `path_exists(path)`。
- `verify_fresh(names?)`:定义见 B3,取值为三值。
- `verify_failed_since_read(path)`:用于 pinrule deep_fix 那一路。
- `tool_count_since_inject {cmp}`。
- `prev_prompt_regex`:基于状态中保存的上一条用户 prompt 末尾 200 字符。
- `prior_hit(rule) {cmp}`。
- `env_equals(name, value)`。
- `branch_glob`。
- `is_subagent`。

**没有 exec 逃生口(R04)**。hook 热路径只执行编译进二进制的确定性谓词。需要新能力时,要么在引擎里新增谓词(代码评审后发布),要么在离线 `ballast replay` 中评估。这是有意的取舍,写入 README。

**事件 × 动作矩阵**(lint 拒绝矩阵外的组合,不做静默降级):

| 事件 | deny | ask | warn | log |
|---|---|---|---|---|
| PreToolUse | `permissionDecision:"deny"`+reason | `"ask"`(`-p` 下可能被拒,文档注明) | 不输出 decision,只给 `additionalContext` | stdout 为空 |
| UserPromptSubmit | `decision:"block"`+reason | ✗ | `additionalContext` | 空 |
| PostToolUse / PostToolUseFailure | ✗(工具已执行,无法撤销) | ✗ | `additionalContext` | 空 |
| Stop / SubagentStop | `decision:"block"`(会话继续) | ✗ | **`systemMessage`**,只给用户看、放行停止;**不用 additionalContext**。可行性待 M0a 实测,不可行则 Stop 只允许 deny/log | 空 |

**Bash 解析契约**:用 `mvdan.cc/sh/v3/syntax`(bash 方言)构建 AST,展开为 `CommandNode` 列表。每个节点包含:
- `argv`:字面词;含变量或命令替换的词标记为 dynamic。
- 已剥离的包装器:`env VAR=`、`nice`、`nohup`、`timeout N`、`time`、`command`、`exec`、`sudo`(剥离后打标记)。
- `loop`:`{kind: for_list(n) | for_c | while | until, bounded}`。
- `async`:shell 的 `&`。
- `in_subst`。
- `redirects`。
- `writes`:从重定向 `> >> &>`、`tee`、`cp/mv/install/rsync` 的目标、`sed -i`、`rm`、`truncate`、`dd of=` 中提取的写入路径。

解析规则:
- `sh/bash/zsh -c '<str>'` 递归解析,最多 2 层。
- heredoc 正文不当作命令解析;遇到 `eval` 结果为 unknown。
- `git` 解析全局参数 `-C / -c / --git-dir / --work-tree / --no-pager`,得到 `git.sub` 和 `git.cwd`。
- **作用域**:同一个 `bash` 匹配器内的所有条件必须落在**同一个** CommandNode 上("存在一个节点同时满足")。
- `tool.run_in_background` 与 `bash.async` 是两个独立字段。
- 解析失败时 bash 匹配器返回 unknown。

**PowerShell(`ps`)**:Go 没有成熟的 PowerShell 解析器,因此只做保守分词:按 `;` 和管道切分语句,识别 cmdlet 名(含别名表 rm / del / sc / Start-Sleep / sleep 等)、参数、`>` 和 `Out-File`、`Set-Content` 的写入目标。无法分词时返回 unknown,文档注明 Windows 覆盖面窄于 Bash。

**默认 `no-long-sleep` 语义**:任一节点满足以下全部条件即命中:sleep 时长 ≥ 60s(支持 `300`、`5m`、`1h` 写法)、不是 async、工具未设置 `run_in_background`。

**回归集**(`testdata/bash-regress.json`,用显式 deny 的测试规则跑,避免与"默认 log"冲突;R08、R16):

| 命令 | 期望 |
|---|---|
| `for i in 1 2 3; do curl -f localhost:8080 && break; sleep 1; done` | 放行 |
| `sleep 300` | 命中 |
| `while true; do sleep 300; done` | 命中 |
| `sleep 300 &` | 放行 |
| `run_in_background:true` 加 `sleep 300` | 放行 |
| `env X=1 sleep 300` | 命中 |
| `timeout 400 sleep 300` | 命中 |
| `bash -c 'sleep 300'` | 命中 |
| `echo "sleep 300"` | 放行 |
| `# sleep 300` | 放行 |
| heredoc 正文中含 `sleep 300` | 放行 |
| `for i in 1 2 3; do sleep 30; done` | 放行(有意语义,单次时长 < 60s) |
| `git -C ../x commit -m m` | `git.sub=commit` |
| `Start-Sleep -Seconds 300`(PowerShell) | 命中 |

**pinrule 8 检查迁移**(R07):M1 开工时先产出 `docs/pinrule-mapping.md`,逐行引用源码,按"DSL 等价 / 有意改变 / 舍弃"三类登记。目标是**8 项都有 DSL 改写版本**,分别放入三处:默认包 `rules/default.json`、可选包 `rules/optional/`、示例 `examples/`。

| pinrule | 去向 | DSL 覆盖 | 有意改变 / 舍弃 |
|---|---|---|---|
| read_first | 默认 `read-before-edit` | Edit / MultiEdit / Write / NotebookEdit;新建文件以 `path_exists=false` 豁免;多文件补丁要求所有目标都已读 | 已读集合按 agent 隔离(比原实现更严) |
| non_blocking | 默认 `no-long-sleep`;可选 `no-foreground-long-task`(命令头列表取自 `non_blocking.py:76-86`)、`no-shell-wait` | sleep、wait 与长任务可以表达 | 循环内单次 < 60s 的 sleep 放行(原实现会拦);语言 `-c` 中的等待只做字面匹配 |
| evidence | 默认 `evidence-before-commit`;可选 `claim-needs-evidence`(Stop 时检查完成声明,结合 `text` 与 `verify_fresh`) | 是 | 舍弃"同一命令链里跑过测试即豁免",证据必须走 verify;"非代码提交"豁免改由 B3 的 `exempt` 快照过滤实现 |
| bypass_pinrule | 默认 `no-tamper`(用 `bash.writes`、`ps` 写入目标、`path` 匹配受保护路径);可选 `read-after-test-fail`(`verify_failed_since_read`) | 是 | 维护 CLI 的豁免改为只放行 `ballast` 的绝对路径。T0 下仍不是安全边界 |
| keep_pushing | **可选**(工作方式偏好,不进默认包) | 用 `text`(tail 80、`not_regex` 有序豁免)与 `prev_prompt_regex` 表达 | 源码第 85–180 行逐条映射,无法映射的项登记为"舍弃" |
| long_term | **可选**;其中 `git --no-verify / -n` 抽出成默认 `no-skip-verify` | 工具输入里的 TODO、硬编码模式用 `input_regex` 加路径豁免;Stop 意图用 `text` | 反向引用模式改写为 RE2 |
| testset | `examples/` | 正则加豁免 glob | 领域特定,不进默认包 |
| chinese_plain | `examples/` | `text.cjk_ratio`、`count_regex`、`strip_code_blocks` | 括号解释窗口简化;属个人偏好,不进默认包 |

默认包共 **5 条**:`read-before-edit`、`no-long-sleep`、`evidence-before-commit`、`no-skip-verify`、`no-tamper`。每条都有 Bash 和 PowerShell 两个变体。

## B2 分级与影子模式(R13)
- 新规则缺省为 `action:"log"`,可用 `target` 声明打算升级到的档位。log 档下的命中记作 `would_<target>`。
- 分母:每个会话的状态里按规则累计"被求值次数",在 Stop / SessionEnd 时以 `eval_summary` 行刷入审计。
- `ballast stats [--since 7d] [--by rule|session|repo]` 输出:`rule | action | target | evals | hits | would | unknown | sessions`。
- `ballast stats --rule <id> --sample 5` 从审计中取出命中记录,上下文来自记录自带的前 2 条事件摘要(B8),同时写入 `~/.ballast/reviews/<id>.json` 作为"已看样本"凭据。
- `ballast rules promote <id> --to warn|ask|deny`:要求近 N 天命中数 ≥ K,且存在 review 凭据;`--force` 可跳过检查,但会写入审计。

## B3 证据机制(R02)
**仓库实例**:`repo_inst = hash(realpath(git rev-parse --show-toplevel) + realpath(--git-common-dir))`,可以区分不同 clone 和 worktree。不在 git 仓库内时,`verify_fresh` 返回 unknown:规则不命中,但写入审计。

**`.ballast/project.json`**:
```json
{"verify":[{"name":"test","argv":["./gradlew","test"],"timeout_s":1800,"required":true},
           {"name":"lint","argv":["./gradlew","lint"],"required":false}],
 "exempt":["**/*.md","docs/**"]}
```
不再使用代码白名单:默认**所有已跟踪的文件**(即索引中的文件)都算验证输入,只有项目显式写入的 `exempt` 例外。这样不会漏掉 `.rs`、`.cs`、`go.mod`、锁文件或配置文件。**未跟踪文件不计入快照**,新文件需要先 `git add` 才算进来,见下文"已知边界"。

**快照算法**(v0.3):先把**真实索引复制**一份,再用复制品计算 tree id。复制品保留了真实索引的 stat 缓存,速度快;真实索引始终不被改动。

- 复制:`idx=$(git rev-parse --git-path index)`,用 `cp` 得到临时文件 `T`,之后所有命令都在 `GIT_INDEX_FILE=T` 下执行。
- 两个原语:
  - `F(T)`:在 T 上执行 `git rm --cached -r -q --ignore-unmatch -- <exempt pathspecs>`,再 `git write-tree`,输出 tree id。
  - **exempt 过滤只定义一次**,S 和 C 两边都调用同一个 `F`。pathspec 由 `exempt` 编译而来(用 `:(glob)` 魔法前缀)。

**S(受测快照,验证开始和结束时各算一次)**:复制索引 → `git add -u`(只纳入已跟踪文件的工作区改动,**不用 `-A`**)→ `F`。

**C(提交快照,PreToolUse 遇到 `git commit` 时计算)**:

| 提交形式 | C 的计算 |
|---|---|
| `git commit`(普通) | 复制索引 → `F` |
| `git commit -a` / `--all` | 复制索引 → `add -u` → `F` |
| `git commit <paths>` / `--only` / `-i` | 复制索引 → 对 `<paths>` 执行 `add -u -- <paths>` → `F`(近似 git 语义;遇到无法解析的参数组合返回 unknown) |
| `--amend` | 与对应形式相同(只比内容,不看父提交) |
| `git -C <dir>` | 在 `<dir>` 所属的 repo_inst 中计算 |

**部分暂存:采用严格语义**(我的决定),比较的是**整棵过滤后的树**:`C == S_end`。
- 只要有未暂存的已跟踪改动,普通 `git commit` 的 C 就不等于 S(S 含这些改动,C 不含),判为过期。
- 拒绝信息给出两种做法:`git add -u` 后重新提交;或者 `git stash --keep-index` 之后重新 verify 再提交。
- 不选宽松语义("提交的文件内容等于测过的内容")的原因:宽松语义下,提交出去的那棵树从未被整体测试过,测试可能依赖未提交的改动才通过,这正是证据机制要防的情况。严格语义下判断是一次 tree id 比较,实现和解释都简单。
- 代价:Agent 有意分批提交时,每批都要单独 verify。这种场景在 Agent 工作流里很少,而且有明确的补救路径。

**verify 记录**:`{job_id, name, argv_hash, project_json_hash, ballast_version, tier, S_start, S_end, t_start, t_end, outcome: pass|fail|timeout|signal|infra, exit_code, session_id, agent_id}`。每个 job 写一个文件,原子写入。只有 `outcome=pass` 且 `S_start == S_end` 时才算有效通过;验证期间内容被改动则作废。

**`verify_fresh` 不变量**:令 `C` = 将被提交内容的快照,对每个 `required` 的 verify 名:
- 按 `t_start` 取**最新一条记录**,不论结果;
- 该记录必须是有效通过,且 `S_end == C`。

`C` 的取法:
- `git commit`:用索引的 tree。
- `-a` / `--all`:索引的临时副本加上 `add -u` 的结果。
- 带路径参数:按 git 语义构造。
- `--amend`:同样取索引 tree。
- Stop 类规则:用当前工作树快照。

这样,后续的失败、其他会话或 Bash 脚本的改动、checkout / reset / merge 都会让快照不一致,自动失效,**不依赖 Edit hook**。

**已知边界**(写入文档):
- 测试依赖了**未跟踪**文件,而该文件没有随提交一起进入仓库时,本地判为新鲜,但 CI 会失败。这类情况交给 CI 兜底。
- 新建文件必须先 `git add`,然后再 verify。否则 S 不含该文件,而提交时 C 含有它,两者不等,判为过期,需要重新 verify。

**后台运行与结果记录**(v0.3 取代前台 `--await`):
- `ballast verify [name]` 立即返回 `job=<id>`,exit 0;runner 在后台执行。
- Agent 用 `ballast verify --status` 查询结果,也可以通过 Claude Code 的后台任务机制等待。
- runner 结束时写入 evidence。Ballast 的 PostToolBatch / UserPromptSubmit hook 发现有**新完成的 job** 时,在下一次注入中附上一行结果,例如 `verify test: pass (S=…)`。结果来自 evidence 文件,不解析任何输出。
- `evidence-before-commit` 遇到同名 job **仍在运行**时返回 unknown(不命中),并提示"verify 进行中"。这样不会因为测试还没跑完就误拦。

**CLI 退出码**:
- `verify` 只负责发起:0 已启动 / 2 配置错误 / 5 基础设施错误。
- `verify --status [job|name]`:0 pass / 1 fail / 3 timeout / 4 signal / 6 运行中 / 11 无记录。
- `verify --check [--commit|--worktree]`:0 新鲜 / 10 过期 / 11 无证据 / 12 无法判定(非 git 或参数无法解析) / 13 有同名 job 运行中。供可选的 git pre-commit 使用,默认取 `--commit`。
- `verify --foreground`:仅供 CI 和人工调试,同步运行,退出码同 `--status`。Agent 侧的规则不依赖它。

**runner**:runner **不持有**热路径的状态锁;多个 verify 可以并行,按 `t_start` 取最新一条;同名 job 重复发起时,先取消旧 job,旧记录的结果记为 `superseded`。T1 下的沙箱化 runner 属于 P2。

**`ballast init`**:探测 9 种栈的标志文件,给出建议命令,**用户确认后才写入**。

**矩阵测试**:9 种栈 ×{成功、失败}× 两个模式:
- **真实模式**:在 CI 指定 job 必跑。Linux 跑 8 栈,swift 在 macOS 跑,dotnet 另在 Windows 跑。
- **桩模式**:始终运行。
- 本地缺少工具链时输出显式 SKIP;CI 缺少工具链时判为失败。

**新鲜度反例套件**(必须全部判为过期或失效):
1. verify 通过后,另一个会话修改了文件;
2. verify 通过后执行 `git checkout` 或 `git reset`;
3. verify 运行期间文件被修改;
4. 通过之后又有一次失败的 verify;
5. 只有非 required 的 verify 通过;
6. 在 worktree A 验证,在 worktree B 提交;
7. `git -C other commit`;
8. verify 超时;
9. 部分暂存:有未暂存的已跟踪改动时执行普通 `git commit`;
10. 新文件在 verify 之后才 `git add`;
11. 同名 job 仍在运行(应返回 unknown 或 13,不能判为新鲜)。

正例:
- 只改了 `exempt` 内的文件后提交,仍判为新鲜(S 和 C 用同一个 `F` 过滤);
- `git add -u` 全部暂存后执行普通 commit,判为新鲜;
- `commit -a` 判为新鲜。

另做一致性测试:S 和 C 对同一份工作区内容必须产生相同的 tree id。exempt 只定义一次,用于防止 S、C 两边过滤规则漂移。

**性能**:索引复制加 `add -u`(利用 stat 缓存)只在 commit 检查时执行。M0c / M2 在 1k、10k 文件仓库上实测,计入 bench 场景 6。

## B4 三层配置与热更新(R09、R17)
**路径**:
- org:`<托管目录>/ballast/rules.json`。
- repo:`<repo>/.ballast/rules.json`。
- user:`~/.ballast/rules.json`(Windows 为 `%USERPROFILE%\.ballast\`)。

`BALLAST_HOME` 仅用于测试;T1 下的托管配置可以通过 `lock_paths` 让它失效。

**合并语义**(不再使用"action 取最严"这类跨层特例):
- **org 规则整体不可变**。下层出现同 id 规则时 lint 报错,运行期忽略下层并写审计 `rule_shadowed`。org 规则无需 `locked` 字段。
- repo 与 user 同 id 时,**repo 整个对象胜出**,不做字段级合并。
- user 只能新增不同 id 的规则,不能禁用或修改上层规则。

**上限 12 条**:只统计 enabled 规则。
- org 自身 ≤ 12,由 `ballast lint --layer org` 在发布时强制检查。
- 加载顺序为 org(全部)→ repo(按文件顺序)→ user(按文件顺序),累计到 12 条为止。超出的规则记 `rule_dropped`,同时写 stderr 诊断。
- 按这个顺序**永远不会丢弃 org 规则**。`ballast lint` 对超限情况直接报错。

**热更新**:
- 每次 hook 运行时 `stat` 三个规则文件和 project.json(取 inode、size、mtime_ns)。有变化就重新编译,编译结果原子写入 `~/.ballast/cache/<rules_hash>.bin`。
- `generation = rules_hash`,同时记入审计和会话状态。会话的 generation 变化后,下一次注入附带变更摘要(B5)。
- 某层文件非法时,该层沿用 **last-known-good**(版本由 hash 明确),并写 stderr、审计 `config_invalid`,在下一次注入中提示。某层没有 LKG 时,该层视为空,并大声报错。
- 验收:不重启会话修改规则后,下一次 hook 即生效。

`ballast config show --explain` 打印每条规则的来源层、generation,以及被遮蔽或丢弃的规则。

## B5 持续注入(R17)
| 时机 | 内容 | 预算 |
|---|---|---|
| SessionStart(startup / resume / clear / compact / fork) | 全部规则的 `inject` 全文 | ≤ 8k |
| UserPromptSubmit | 锚点:每条规则一行(id + 摘要),频率由 `inject.anchor_every` 控制,默认每次 prompt | ≤ 1.2k |
| PostToolBatch | 自上次注入以来工具调用数 ≥ N(默认 25)时,在回合中途补一次锚点 | ≤ 1.2k |
| generation 变化 | 变更摘要,包括新增、删除、promote | ≤ 1k |

`ballast lint` 校验总预算。

## B6 会话状态与并发(R10、R11)
- **作用域**:workspace = `repo_inst`;session = `session_id`;agent = `agent_id`,主会话记为 `"main"`。
- **状态文件**:`~/.ballast/state/<repo_inst>/<session>/<agent>.json`,配一个 `.lock` 文件。
  - 用 flock 加锁(Windows 用 `LockFileEx`),**最长等待 20ms**。超时则跳过状态写入,只用读到的快照求值,并写诊断。
  - 加锁后在一个完整事务内完成读、改、写,通过临时文件 + rename 原子替换(Windows 用 `MoveFileEx(REPLACE_EXISTING)`)。
- **去重与环形缓冲**:按 `tool_use_id` 去重(保留最近 512 个);事件摘要放在 8 条容量的环形缓冲里,内容已脱敏。
- **子 agent**:
  - 已读集合和 `prior_hit` 按 agent 隔离,主 agent 与子 agent **互不继承**。
  - 证据按仓库内容快照共享,共享是安全的。
  - SubagentStop 属于 Stop 族事件;SubagentStop 时清理该 agent 的状态。
  - SessionEnd 时清理会话状态,另有 7 天 TTL 兜底回收。
  - 审计记录 `agent_id/agent_type`。
- **测试**:2–8 个独立 hook 进程并发运行,断言没有丢失更新;重复投递同一个 `tool_use_id`;两个 verify 并行;跨会话、跨 worktree;并行子 agent。`-race` 只是附加检查,不能替代以上测试。

## B7 输出协议与异常放行(R05)
hook 的处理流程:
```
parse → load(cache) → evaluate → 在内存中形成唯一 Decision
      → 写 state 与 audit(写入失败 = 异常)
      → 一次性 write 提交 stdout → exit 0
```
- **任何阶段出现异常**,包括 panic、超时看门狗、状态或审计写入失败:stdout **保持为空**,向 stderr 写入 `ballast: <err>`,**exit 1**。按 A7,非 2 退出且没有 JSON 即为非阻塞错误,动作照常放行。
- 用内置 150ms 看门狗兜底:到时如果 stdout 尚未提交,直接以 exit 1 退出。
- 安装时**显式不配置** `onFailure`,拦截类 hook 也不配置 `async`。
- 无决策时输出空 stdout,**绝不输出 allow**。
- 边界声明:stdout 一旦成功提交,之后进程即使崩溃,决策也已生效,这一点写入文档。
- **故障注入测试**:在每个阶段注入错误,包括"Decision 已是 deny、stdout 尚未提交"这一阶段。断言 stdout 为空、exit=1、stderr 带 `ballast:` 前缀。另在隔离的 `CLAUDE_CONFIG_DIR` 中跑 e2e,确认工具调用确实被放行。

## B8 审计(R13)
**文件**:`~/.ballast/audit/YYYY-MM.jsonl`。

**字段**:`ts, v, event_seq, session_id, agent_id, agent_type, tool_use_id, transcript_ref(~ 相对路径), repo_inst, repo_remote_hash, event, tool, rule_id, rule_layer, generation, action, target, decision, matched(≤200 字符, 已脱敏), context(前 2 条事件摘要, 已脱敏), latency_us, tier, ballast_version`。另有 `eval_summary`、`rule_dropped`、`rule_shadowed`、`config_invalid`、`verify` 等行类型。

**写入协议**:
- 每条记录 ≤ 8KB,超长字段截断;一次 `write` 调用写完整行,并检查短写。
- POSIX 用 `O_APPEND`;Windows 用 `FILE_APPEND_DATA` 加 `LockFileEx`。
- 读取方容忍损坏的尾行。

**增长控制**:
- 按月切分,单文件超过 64MB 轮转为 `.N`。
- 默认保留 90 天;状态 TTL 7 天;证据保留 30 天,但每个 verify 名始终保留最新一条。
- GC 由 SessionStart 以 detached 子进程方式触发,每天最多一次(靠标记文件控制),不占用热路径。
- 磁盘写满按 B7 的异常处理。

**命令与上报**:
- `ballast audit lint`(原名 `audit verify`,已改名)只做格式校验,不代表防篡改。
- 脱敏规则与 v0.1 相同,外加 50+ 条样本和 fuzz 测试。
- 上报默认关闭,也不在热路径上发送。

## B9 回放评测(R12)
- **用法**:`ballast replay <transcript> [--subagents] [--evidence auto|<file>] [--initial-state <file>] [--rules <path>] --format json|md`。
- **分类**:每次判定归为 **known-hit / known-miss / unknown / approximate** 之一。
  - 依赖 `env`、`branch`、历史 `path_exists` 的规则 → approximate。
  - Stop 类规则用最终 assistant 文本近似 → approximate。`stop_hook_summary` 只作参考,不当作 payload。
  - `verify_fresh` **只有在拿到可信的 evidence 记录时才判定**(按 session_id 和时间从 `~/.ballast/evidence` 关联)。transcript 里出现 `ballast verify` 调用不算证据;没有可信记录时判为 unknown。
- **精确率**:只在 known-hit 并且有人工标注的样本上计算。标注键为稳定样本 id:`sha(transcript_ref + uuid + rule_id + rules_hash)`。
- **性能**:流式解析,处理 40MB 文件内存 < 200MB。

## B10 性能(R14)
**门槛**:每个场景 **p95 ≤ 30ms**,CI 超过即失败,不再设 +20% 的容差。相对回归比例只做报告,不作门槛。

**测法**:`scripts/bench`(独立的 Go 程序)从外部父进程 spawn hook 二进制,计时覆盖从启动到退出的全程。每个场景预热 20 次、正式跑 300 次;p95 取最近秩 `ceil(0.95n)`;另用 hyperfine 交叉校验。报告记录机型、OS 和负载情况。

**场景**:
1. SessionStart;
2. UserPromptSubmit;
3. PreToolUse Edit;
4. PreToolUse Bash 短命令;
5. Bash 100KB 命令;
6. `git commit` 检查,仓库规模分 1k / 10k 文件两档;
7. PostToolUse;
8. Stop;
9. 12 条规则且缓存冷启动;
10. 4 个进程并发争锁;
11. 损坏的状态文件。

**平台**:macOS arm64(本机)、Linux CI、Windows CI 三个平台**都按 30ms 设门槛**。Windows 进程创建开销较高,是已知风险(风险 3)。

**语言**:已定为 Go。M0c 用最小完整 hook 原型实测;如果 p95 超过 30ms,**停下来汇报**,不硬推进。

## B11 Windows 原生(R18)
- **路径规范化**:
  - 内部统一成 `c:/users/x/...` 形式:盘符小写、反斜杠转正斜杠。
  - UNC 路径转为 `//server/share/...`;Git Bash 的 `/c/Users` 转为 `c:/Users`。
  - Windows 与 macOS 上路径比较不区分大小写。
  - 受保护路径的判断**同时检查原始路径和 `EvalSymlinks` 解析后的路径**,覆盖符号链接和目录联接。
- **glob 方言**:用 `github.com/bmatcuk/doublestar/v4`,支持 `**` 和 `{a,b}`;取反只能在列表层面表达,写入 `dsl-semantics.md`。
- **PowerShell**:用 B1 的 `ps` 匹配器,每条默认规则都配 PowerShell 变体。
- **托管安装**:路径含空格(`C:\Program Files\...`)时用 `command` + `args` 分开写,不拼接字符串。
- **层级**:原生 Windows 只有 **T0**,`doctor` 会明确提示"如需 T1 请使用 WSL2"。runner 不带沙箱,证据标记 `tier:"T0"`。
- **CI**:windows-latest 跑单元测试、回归、bench、安装 e2e 和 PowerShell 回归。

## B12 安装与卸载(R16、R19)
- **归属标识**:不使用 `_ballast` 自定义键,改用**精确的 `command` 绝对路径 + `args:["hook","<event>"]`**;另在 `~/.ballast/install-manifest.json` 记录插入的条目及其内容 hash。
- **install**:
  - 幂等:重复安装不会产生重复条目。
  - 检测到托管层已部署 Ballast 时拒绝用户层安装,避免 A15 的合并导致重复执行。
  - 写入前备份到 `~/.ballast/backups/<ts>-<rand>/`,权限 0600,保留最近 10 份。
  - `--dry-run` 只输出 diff。
- **uninstall**:
  - 只删除与 manifest 完全一致的条目;用户改动过的条目保留并给出警告。
  - 如果 settings 文件是 install 新建的,且卸载后为空,就删除该文件。
- **`ballast org render`**:输出 T1 托管配置片段,不写系统路径。只有显式加 `--write --i-am-admin` 且具备 root 权限时才写入。

---

## 里程碑与验收
**核心增量路径**(相对 pinrule 的真正增量,优先交付):
- **M0a**:用户层取证;外加 M0c 的 Go 性能闸门,它决定语言选择是否成立。
- **M1**:内核。
- **M2**:按 v0.3 修正后的证据机制。
- **M3 的 stats + replay**。

**P2**(设计保留,排在核心路径之后):M0b 托管容器测试、T1 沙箱化 runner、M3 中的脱敏与上报、M4 的安装/托管/边界,以及审计轮转之外的增强项。审计最小写入器和轮转仍在 M1/M3 的核心范围内,因为 stats 依赖它们。

每个里程碑都有一个 `scripts/accept-mN.sh`:参数固定,任何断言失败即以非 0 退出,在 macOS / Linux / Windows(用 Git Bash 运行)CI 上执行。

| 里程碑 | 内容 | 验收要点(脚本内断言) |
|---|---|---|
| **M0 取证与原型** | **M0a**:在临时 `CLAUDE_CONFIG_DIR` 中用真实会话抓取用户层 payload。用例:Bash 成功;Bash exit 1;grep exit 1;启动失败;中断;compact;SubagentStop;Stop 返回 `systemMessage`;`updatedInput` 是否必须配 allow;并行 PostToolUse;exit 1 加合法 JSON 的行为;Stop 连续阻止上限(case `stop-cap`)。**M0b**:在一次性 Linux 容器中写 `/etc/claude-code/managed-settings.json`,测 `allowManagedHooksOnly`、低层 `disableAllHooks`、hook 合并、沙箱 `denyWrite` 对 Bash 和 Edit 的效果、MCP 写入、runner 沙箱选型。**M0c**:用 Go 写最小完整 hook 原型(放在 `proto/`,M1 前删除),测 p95;同时测 1k / 10k 文件仓库的快照耗时 | `accept-m0.sh`:校验 `docs/facts/cases.json`,每个必需的 case_id 都要有 `event`、`schema_keys`、`assertion`、`result`;`CONCLUSIONS.md` 中 A 区每个"待 M0"条目都有结论;M0c p95 ≤ 30ms,否则**停下汇报** |
| **M1 内核** | schema 与 `dsl-semantics.md` 冻结;`pinrule-mapping.md`;adapter;B7 输出协议;三层配置与热更新;状态存储与锁;最小审计写入器;DSL 引擎;mvdan 解析器与 ps 分词器;B5 注入;事件 × 动作矩阵;默认规则中除 `evidence-before-commit` 外的 4 条;`test-rules` | `go test ./... -race`;`ballast lint rules/ examples/`;`ballast test-rules rules/ examples/`;`regress.sh`(显式 deny 规则集,上文 14 例);`fault.sh`(B7 各注入阶段);`concurrency.sh`(B6);`hotreload.sh`;`bench` 门槛 |
| **M2 证据**(v0.3 修正版) | project.json;索引复制快照与统一的 exempt 过滤 `F`;严格部分暂存语义;后台 runner(T0);`verify` / `--status` / `--check` / `--foreground` 退出码;job 完成后通过注入回报结果;`verify_fresh`;`evidence-before-commit`;可选的 `claim-needs-evidence`;pre-commit 模板与"以 CI 为准"的文档;`init`;矩阵测试 | `freshness.sh`(11 个反例 + 3 个正例 + S/C 一致性测试);`matrix.sh --mode real\|stub`;`bench` 中的 commit 检查场景 |
| **M3 统计与回放**(核心) | 完整审计字段;轮转与 GC;stats / promote;replay;标注 | `ballast audit lint`;stats 的 golden 测试;`replay.sh`:读取本地 `~/.ballast/replay-set.txt`(3 份 transcript,其中一份 >10MB,出于隐私不入库),断言报告符合 schema、内存 < 200MB、标注 ≥ 20 条且全部能关联到报告中的样本 id |
| **P2-a 脱敏与上报** | 脱敏器;可选上报 | 50+ 样本零泄漏,加 60s fuzz |
| **M4 安装与边界** | install / uninstall;`org render`;`doctor`;ConfigChange 审计 hook;T1 边界 e2e(在容器中)| `install-e2e.sh`:往返 diff 为空;重复安装幂等;安装后用户另加的 hook 卸载后仍在;原文件不存在的情况;托管层已部署时拒绝安装。`boundary-t1.sh`:在容器中让 Agent 尝试 Edit、`echo >`、`sed -i`、`python -c` 写入、伪造 `ballast` 进 PATH、直接写 evidence、MCP 写入,**全部失败**(MCP 一项按白名单配置验证)。`boundary-t0.sh`:同样的尝试**能被检测并写入审计** |

**依赖顺序**:核心路径为 M0a(+M0c)→ M1 → M2 → M3。之后进入 P2:M0b → T1 runner → M4,其中 M4 的 T1 边界测试依赖 M0b 的结论和 T1 runner。审计和状态的最小接口在 M1 就交付,避免 M2、M3 返工。

上表中的 M0b 和 M4 两行属于 P2,验收要求不变。

## 完成条件 → 验证方法
| 完成条件 | 验证 |
|---|---|
| 单元测试全绿;8 检查改写为 DSL 后场景测试通过 | `go test ./... -race`。8 项均有 DSL 版本,分布在 default / optional / examples;对照用例从 pinrule 的 `tests/` 人工映射而来,差异只允许出现在 `pinrule-mapping.md` 的"有意改变"列 |
| 多语言证据矩阵 | `matrix.sh` 的真实模式和桩模式,报告中分开标注 |
| curl 重试循环不拦、`sleep 300` 拦 | `regress.sh`,完整 14 例 |
| replay ≥ 3 份真实 transcript | `replay.sh` |
| p95 ≤ 30ms | `bench`,三个平台都设门槛 |
| hook 异常时放行并写 stderr | `fault.sh` 加 e2e |
| Agent 不可写规则和状态 | T1:`boundary-t1.sh`;T0:`boundary-t0.sh`(只验证可检测,文档已声明) |

## 禁区合规
- **热路径不调 LLM、不联网,也没有 exec**。`go list -deps ./internal/hook/...` 不得包含 `net/...`;hook 唯一会 spawn 的进程是 verify runner,也就是 Ballast 自身的二进制,而 runner 不在热路径上。
- 不做规则蒸馏或检索,12 条规则常驻。
- install / uninstall 外科式操作并先备份(B12)。
- 默认 5 条规则都是通用规则。keep_pushing、long_term 设为可选;chinese_plain、testset 只在 examples 中。

## 仓库布局
```
cmd/ballast/  internal/{adapter,hook,dsl,bashparse,psparse,engine,state,evidence,snapshot,runner,sandbox,config,inject,audit,redact,replay,install,paths}/
schema/rules.v1.json  rules/default.json  rules/optional/  examples/
scripts/{accept-m0..m4.sh,regress,fault,concurrency,hotreload,freshness,matrix,replay,install-e2e,boundary-t1,boundary-t0}.sh  scripts/bench/
docs/{facts/,dsl-semantics.md,pinrule-mapping.md,trust-model.md}  testdata/  proto/(仅 M0)
```

## 风险
1. T0(无托管部署或原生 Windows)只能检测、不能防止篡改。已在需求层面接受,文档和 `doctor` 都会明示。
2. T1 下 MCP server 在沙箱外运行,只能靠托管 MCP 白名单收口。白名单内的 MCP 工具如果有写文件能力,仍是残余风险,需在部署指南中列出。
3. Windows 进程创建开销可能突破 30ms。M1 即在 Windows CI 测量;超标时汇报,不放宽门槛。
4. 超大 monorepo 的快照耗时。M0c 实测;超标时考虑以索引 stat 为键做缓存。
5. Stop 的 `systemMessage` 若与预期不符,B1 的 Stop warn 需要改方案:只保留 deny 和 log。M0a 先验证。v0.3 已不依赖 `updatedInput`。
8. 严格的部分暂存语义会让 Agent 分批提交时多跑几次 verify;测试依赖未跟踪文件的情况本地发现不了,交给 CI 兜底(B3 "已知边界")。
6. 按需求"任何异常都放行",磁盘写满会使强制失效。审计中会记录诊断,`doctor` 也会告警。
7. transcript 是内部格式,replay 需要做版本容错。

## 审核意见追溯表
| 审核 | 处理位置 | 处理 |
|---|---|---|
| R01 | B0, M4 | 分级边界(T1 托管沙箱 + 权限规则,T0 明示只能检测);verify 由 hook 代起并在沙箱内运行 |
| R02 | B3 | 新鲜度以内容快照为准,绑定 required 集合、最新一条记录和 `S_start==S_end`;改为全量输入减 exempt;8 个反例(v0.3 扩为 11 个)|
| R03 | B1 | 冻结 schema、三值求值契约、补齐谓词和 text 处理、规定多规则裁决 |
| R04 | B1, 禁区 | 移除 exec |
| R05 | B7, A7 | 单一提交点,异常时 stdout 为空并 exit 1,加看门狗和分阶段故障注入 |
| R06 | B1 矩阵 | 逐事件映射;Stop warn 改用 systemMessage(待 M0a 实测) |
| R07 | B1 迁移表 | 按源码重写;8 项均有 DSL 版本;完成条件对齐 |
| R08 | B1 | 用 mvdan AST、单节点作用域、写入目标提取,回归 14 例使用显式 deny 规则 |
| R09 | B4 | org 整体不可变,repo 整对象胜出,加载顺序保证不丢 org 规则 |
| R10 | B6, B3 | 加锁事务、去重、`repo_inst`、跨进程测试 |
| R11 | B6, B1 | 按 agent 划分作用域,纳入 SubagentStop,审计记录 agent 字段 |
| R12 | B9 | 四类判定、evidence sidecar、标注只针对 known |
| R13 | B2, B8 | 上下文摘要、分母、写入协议、轮转与 GC、改名 audit lint |
| R14 | B10 | 30ms 固定门槛,外部计时,11 个场景,三个平台 |
| R15 | M0 | 用户层探针与托管层容器测试分开,cases.json 结构化 |
| R16 | 里程碑 | 提前交付基础接口,accept 脚本,显式 deny 测试集,安装往返之外的用例 |
| R17 | B4, B5 | 热更新 generation、LKG、持续注入节奏 |
| R18 | B11 | 原生 Windows 的完整契约(用户已选择首发即支持) |
| R19 | B12 | 用 `command`+`args` 加 manifest 标识归属;备份策略 |
| R20 | A 区 | 拆分协议事实与可选性,补 A20–A24 |

## v0.3 变更
| 用户意见 | 处理 |
|---|---|
| 快照改为复制真实索引 | B3 快照算法:复制索引,在 `GIT_INDEX_FILE` 下操作,不动真实索引 |
| 只看已跟踪文件 | S 改用 `add -u`;未跟踪文件写入"已知边界" |
| S 和 C 用同一套 exempt | 唯一的过滤原语 `F`,加 S/C 一致性测试 |
| 部分暂存的语义 | **严格**:过滤后整棵树 `C == S_end`,理由见 B3 |
| 长测试不走前台 `--await` | verify 一律后台运行;hook 通过注入回报 job 结果;运行中返回 unknown 或 13;pre-commit 只做 `--check`,CI 为最终裁判 |
| 保留核心路径 | 里程碑分为核心路径(M0a/M0c、M1、M2、M3 stats+replay)与 P2 |
| A10 原文 | 已在 hooks 文档 Stop input 一节找到原文并引用;运行期行为另列 M0a case `stop-cap` |
| A24 | 标【未核验】;v0.3 不再依赖该字段 |


## v0.4 变更(实施依据,优先于正文)

**依据**:`docs/facts/M0a-CONCLUSIONS.md`、`docs/facts/M0c-PERF.md`、`docs/research/pinrule-install-and-clients.md`,以及用户要求"参考原项目的安装、卸载流程,以及不同 AI IDE 的支持"。

### 1. 多客户端支持(取代原"只支持 Claude Code")

| 客户端 | 级别 | 注入 | 拦截 | Stop | 安装目标 |
|---|---|---|---|---|---|
| Claude Code | 一级,完整支持 | SessionStart、UserPromptSubmit、PostToolBatch | PreToolUse 的 deny/ask/warn;UserPromptSubmit block | block / systemMessage | `~/.claude/settings.json`(遵循 `CLAUDE_CONFIG_DIR`),条目用 `command`+`args` |
| Codex | 二级 | SessionStart、UserPromptSubmit、PostToolUse 的 additionalContext | PreToolUse deny;PermissionRequest 用 `decision.behavior`;**不支持 ask** | block(【未核验】) | `~/.codex/hooks.json`;启用 `[features] hooks=true`;trust 哈希只在显式 `--codex-trust` 时写入 |
| Cursor | 二级 | sessionStart、postToolUse 的 `additional_context`;beforeSubmitPrompt;另生成托管 `~/.cursor/rules/ballast.mdc`(`alwaysApply: true`) | preToolUse、beforeShellExecution、beforeMCPExecution、beforeReadFile 用 `permission:"deny"`;ask【未核验】 | 只支持 `followup_message`(需配 `loop_limit`);lint 把 Stop 的 deny 视为"续跑" | `~/.cursor/hooks.json`(`version:1`,平铺条目) |
| Hermes | 三级,尽力支持 | 仅 `pre_llm_call` 的 `{context}` | `pre_tool_call` 用 `{decision:"block"}` | 不支持 | `~/.hermes/config.yaml` 的 `hooks:` 段,外科式改写 |

- **运行时不嗅探客户端**:安装器把客户端写进命令行,例如 `<abs>/ballast hook --client codex --event PreToolUse`。
- **Adapter 接口**:`ID`、`Detect`、`Capabilities`、`NativeEvents`、`ParseEvent`、`NormalizeTool`、`Render`、`InstallTargets`、`Transcript`、`Doctor`,见 `docs/research/pinrule-install-and-clients.md` 的 C2。
- **lint**:按客户端的能力矩阵拒绝不支持的"事件 × 动作"组合;默认只对已安装的客户端生效。
- **工具名归一化**:`apply_patch` → Edit(解析出多个目标文件);`exec_command`、`Shell`、`terminal` → Bash;`Task` → Agent。
- **安装与卸载升入核心路径**(新的 M4-core):
  - 每个客户端有一组 `InstallTarget`。`Plan` 和 `Remove` 是纯函数,同时用于 `--dry-run` 输出 diff。
  - 已有条目原地替换,不调整顺序。
  - 归属判定同时满足两条:命令前缀等于"二进制绝对路径 + `hook --client X`",且与 manifest 记录的 hash 一致。
  - 每个目标单独备份;`uninstall` 以 manifest 为准(覆盖已卸载的客户端),同时删除托管的 `.mdc` 和由安装新建、现已为空的文件;配置加载失败时返回非 0。
  - 不复制 pinrule 的以下做法:`init` 时自动安装、按子串识别归属、把 venv 解释器绑进命令、备份不轮转。
- **e2e 测试**:`BALLAST_HOME` 和各客户端的配置根目录都可以重定向,不写入真实的 `~`。

### 2. 快照实现(M0c:用 git 子进程算快照 p95 80–157ms,超过门槛)
- **C 和 S 都在进程内原生计算**。解析索引(v2/v3/v4),按 exempt 过滤,在内存中构造 tree 并计算 SHA-1。
- 计算 S 时,对每个索引条目做 lstat:stat 与索引一致且没有 racy 风险就沿用索引里的 blob id;否则读文件内容计算 blob 哈希。还需处理符号链接、可执行位(`core.filemode`)和已删除的文件。
- **以下情况退回 git**:未合并条目、split/sparse index、skip-worktree/intent-to-add、SHA-256 仓库、配置了 clean/smudge/eol 转换(`.gitattributes` 中的 `filter`、`text`、`eol`,或 `core.autocrlf`)。
  - runner 退回时不设时限。
  - hook 退回时最多等到剩余期限;超时返回 unknown(放行并写审计)。
- **一致性测试**:在夹具仓库上与 `git add -u` + `write-tree` 的结果逐位比对。
- exempt 的 glob 统一编译成同一个匹配器,S 和 C 共用。

### 3. 其他决定
- **状态事务**:无锁读取后求值,再加锁重读、合并增量写回,缩短持锁时间。状态文件不 fsync;evidence 文件 fsync,因为它不在热路径上。
- **compact 时的内部 agent**(`agent_type` 为空的 SubagentStop):默认忽略。
- **Stop 族的 warn**:映射到 `systemMessage`,已由 M0a 实测确认。
- **gob 规则缓存**:可选;热更新所需的 stat 检测必须保留。
- **git 回退路径**:用 `exec.CommandContext` 并按进程组 kill;临时索引不得写入用户的 `.git/`;看门狗退出前完成清理(来自 M0c 审计)。
- **bench 门槛**:同时满足 `Bad==0`、样本数齐全、`p95≤30`;每个 `--client` 至少有一个 PreToolUse 场景(来自 M0c 审计)。

### 4. 第 1 轮系统审计后的修订(v0.4.1,优先于正文)
- **M0a 用例"中断"**:未执行,case `interrupt` 登记为 `unverifiable`(中断要在交互会话里按 Esc,无头探针无法脚本化),理由见 `docs/facts/M0a-CONCLUSIONS.md`,并已加入 `accutil m0-check` 的必需列表;由人工在交互会话补抓。
- **M4 的"ConfigChange 审计 hook"**:**归入 P2,本期不实现**。理由:M0a 没有抓取过 ConfigChange 的 payload,事件字段【未核验】;T0 下 `no-tamper` 已能检测对 `settings.json`、`~/.ballast/**` 等的改写并写入审计,ConfigChange 只是更晚的第二信号;做成 log-only 也要把新事件纳入安装事件列表与 install golden,收益小于风险。P2 补抓 payload 后再做。
- **M4 的"往返 diff 为空"**:对 Claude Code、Cursor、Hermes 与 Codex 的 `hooks.json` 逐字节为空;Codex 的 `config.toml` 例外:**除 `[features] hooks = true` 外为空**。这一行是安装器在原文件没有该开关(`absent`)时写入的,契约 §5.2 有意不在卸载时关闭(同一个 `hooks.json` 里可能还有别的工具的 hook 依赖它,关掉会静默失效),只提示。`install-e2e.sh` 用"没有 hooks 键"的夹具显式断言这条残留。原值为显式 `false` 的不在例外内:安装先警告,卸载恢复 `false`(第 5 轮复审,architecture v1.8)。Hermes 的 0 字节与无末尾换行 `config.yaml` 同样逐字节往返(`yaml:blank`、`yaml:noeol`)。
- **备份文件名定长**:`<sha256(绝对路径)前 16 位>-<basename,最长 64 字节>`,原路径记在备份目录的 `index.json` 和 `BackupRecord.Files`;此前整条路径做百分号编码,深目录(`TMPDIR`、`CLAUDE_CONFIG_DIR` ≥ 约 237 字节)下会 `ENAMETOOLONG`,既装不上也卸不了。`accept-m4.sh` 加了深 `TMPDIR`(约 300 字节)的回归。
- **缓存与会话状态不再无界增长**:`stat.json` 满 64 条整表重建时,同时删掉不再被引用的 `cache/rules-*.json`;`ballast gc` 另外清理超过 7 天且未被引用的缓存文件。SessionEnd 清理会话状态时,在持锁状态下尽力删除已无状态文件的 `.lock` 与空会话目录,删不掉的由 7 天 TTL GC 兜底;所有加锁处在加锁后复核 inode,避免孤儿锁分裂。
- **replay 分类按实际求值路径**(契约见 `internal/replay/doc.go`):`match` 本身就不命中的求值记 known-miss,不再因规则用了 `path_exists`/`verify` 而整体降为 approximate/unknown;`path_exists` 只有在不得不读当前磁盘时才是 approximate,能由 transcript 内的读写还原时是精确的。目的是让 `read-before-edit` 这类默认规则也能积累 known 样本与精确率。
- **需要用户在本机完成的验收**(无法在仓库内自动完成,`accept-m3.sh` 对此显式 SKIP):M3 的"replay ≥ 3 份真实 transcript、标注 ≥ 20 条"。步骤:在 `~/.ballast/replay-set.txt` 列出至少 3 份 transcript(其中至少 1 份 > 10 MB);用 `ballast replay <transcript> --format json` 与 `ballast stats --rule <id> --sample N` 取样本;在 `~/.ballast/labels/<rule_id>.jsonl` 写入 ≥ 20 条 `{"sample_id","label":"tp"|"fp","note"}`;然后 `BALLAST_ACCEPT_REAL_REPLAY=1 bash scripts/accept-m3.sh`。默认包的 known-hit 可能不够(默认规则多为 `log`、命中稀少):此时在标注集里加入 `rules/optional/` 的规则,或增加 transcript。该项在用户完成前保持"未核验"。
- **提交作者邮箱**:公开发布前由仓库所有者确认是否接受历史中的工作邮箱;不接受就用 `git filter-repo --mailmap` 改写为 noreply 邮箱并设置仓库级 `user.email`(见 `docs/architecture.md` §13)。这是发布动作,不由实现任务代做。

### 5. 第 3 轮系统审计后的修订(v0.4.2,优先于正文)
- **B2 分母包含触发刷出的那个事件自己的求值**:Stop / SubagentStop / SessionEnd 刷出的 `eval_summary` = 刷出前状态里的计数 + 本事件的求值;从未遇到刷出事件的状态(Codex/Cursor 没有 SessionEnd 时最后一次 Stop 之后的求值、被 7 天 TTL 清掉的会话)由 `ballast gc` 以 `eval_summary`(`detail.cause=gc`)补写。`stats` 中 `evals ≥ hits + would` 成立。
- **B3 "verify 进行中"提示**:同名 verify job 仍在运行时,`verify_fresh` 仍返回 unknown(不命中);对 action 为 warn 或更严的规则,hook 额外输出 warn 级提示,log 档保持静默。
- **`proto/`(M0c 原型)保留到仓库所有者决定删除**,取代"M1 前删除";`accept-m0.sh` 依赖它做 vet,删除时同步调整该脚本的原型段。
- **CI**:`accept-m0.sh` … `accept-m4.sh` 全部在三个平台上执行;`accept-m1.sh` 的 `go test -race` 与 bench 在 CI 里由独立步骤承担,用 `BALLAST_ACCEPT_SKIP_GOTEST` / `BALLAST_ACCEPT_SKIP_BENCH` 显式 SKIP。

### 6. 第 4 轮系统审计后的修订(v0.4.3,优先于正文)
- **M4 的"往返 diff 为空"对 JSON 目标是逐字节的**:安装只插入自己的条目、卸载只删除自己插入的部分,兄弟成员之间的空白与空行、冒号两侧的空格、缩进风格、末尾换行与 CRLF 一律不动;用户原有的空事件数组与 0 字节文件卸载后原样保留(`json:hooks.<event>`、`json:blank` 标记)。
- **Cursor `ballast.mdc` 是安装时的快照**:没有规则时 `install --client cursor` 打警告,规则变更后重新运行该命令刷新;README 快速上手先放规则再安装。
- **`no-tamper` 的写入目标表**加入 perl/ruby `-i`、`git checkout -- <path>`、`git restore <path>`;语言内写入(`python -c`、`node -e`)仍无法识别,已在 trust-model 与 README 登记。
- **历史 blob 中残留的本机标识需由仓库所有者决定是否 `git filter-repo`**(与作者邮箱同属发布动作,见 `docs/architecture.md` §13);实现任务不改写历史。

## 已定事项(无需再裁决)
- 合并语义:采用 B4 的方案,org 整体不可变,原问题 3 的备选方案升格为正式方案,以消除歧义。
- ~~Codex:只预留 Adapter 接口~~ → 已被 v0.4 多客户端支持取代(用户 2026-10-16 要求)。
- 计划批准后,将本文件同步覆盖 `docs/ballast-plan.md`,v0.1 留在审核报告记录的 hash 中可追溯。

## v0.5 P2 计划(2026-10-16,优先于正文中 P2 相关条目)

**依据**:核心路径(M0a/M0c、M1、M2、M3 自动化部分、M4-core 安装卸载)已完成并通过独立系统审计。P2 的详细设计与契约冻结在 `docs/p2-design.md`(v1.0),架构契约同步为 `docs/architecture.md` v2.0。本节只列计划层面的决定与验收,细节以设计文档为准。

### 1. 工作包与验收

| 工作包 | 内容 | 验收(可执行) |
|---|---|---|
| **M0b 托管层容器取证** | 一次性 Linux 容器(Claude Code 固定版本,`/etc/claude-code/managed-settings.json` 与 `managed-settings.d/`),不使用凭据:先核实无凭据时 SessionStart/UserPromptSubmit 是否触发(无 key / 假 key + 拒绝连接端点 / 假 key + 挂起端点三种模式),再测 `allowManagedHooksOnly`、低层 `disableAllHooks`、hook 合并、`.d` 合并规则、托管 hook 的路径要求、ConfigChange payload、MCP 白名单键名、容器内 bwrap 条件。hook 是否在沙箱外(A20)也用无凭据方式实测(托管 SessionStart hook 在 `sandbox.enabled` 下写 denyWrite 路径);沙箱 denyWrite 对 Bash、permissions.deny、MCP 写入标为【需凭据】,给出一条命令的补测方式 | `scripts/probe-m0b/run.sh` 生成 `docs/facts/m0b-cases.json`、`M0b-CONCLUSIONS.md`;`go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json` 通过(每个必需 case 字段齐全,`needs_credentials` 带 `recheck`) |
| **T1 沙箱化 runner 与 hook 代起 verify**(B0) | 层级判定六条件(GOOS、重定向变量未生效、二进制链 root 所有、root 所有的 `org.json` 声明 T1(连同托管目录父链)、托管设置完整(覆盖判定用 `tier.ManagedForms` 的精确字符串)、用户层不削弱托管沙箱;v0.5.3/v0.5.4),agent 无法伪造;声明 T1 的主机上重定向变量一律被忽略(重定向锁),降级也做不到;PreToolUse 识别规范 `ballast verify <name>`,T1 下 hook 以 detached 方式启动 Ballast 自身(`verify --hook-start`),返回带 job id 的 deny,不依赖 `updatedInput`;runner 在进程内把测试命令包进 sandbox-exec(macOS)或 bwrap(Linux),**默认拒写**(只放行仓库、git dir、job 临时目录与 `runner_allow_write`,其中再拒受保护路径与 git hooks/config 等,祖先目录固定;v0.5.5 起 hook 代起的 job 在 `S_start` 的私有副本里执行,可写的只有副本、临时目录与 `runner_allow_write`),网络默认 deny(macOS 的 IPC 默认拒绝,v0.5.5);hook 代起的 job 一律必须沙箱,不可用即不执行、记 infra;`runner_sandbox`(require/prefer/off)只管人工运行的 verify;证据 `tier:"T1"` 当且仅当 runner 自判 T1 且确实用了沙箱 | `go test ./internal/tier ./internal/sandbox ./internal/runner ./internal/hook`;bench 新场景(假 T1 规范 verify)p95 ≤ 30ms;`scripts/boundary-t1.sh` |
| **P2-a 脱敏与可选上报** | 新 L0 包 `internal/redact`,hook 与 audit 共用;`ballast report --out <file>` 只输出脱敏后的聚合统计,默认不产生、不在热路径、不联网;网络上报不在 P2 实现,只冻结约束(单独二进制、显式 endpoint 加二次确认参数、`ballast` 二进制永不链接 `net/*`) | ≥ 50 条正样本零泄漏 + ≥ 20 条负样本不变;`go test ./internal/redact -run '^$' -fuzz FuzzString -fuzztime 60s`;`go test ./internal/report`;archtest `TestBinaryNoNet` |
| **M4 边界** | ConfigChange 只审计(stdout 恒空),payload 取证后再安装;`boundary-t0.sh` 覆盖 trust-model §2.1 的全部"识别的写入"并把"未覆盖的写法"作为 gap 断言;`boundary-t1.sh` 在容器中用 `org render` 生成的托管配置与沙箱 runner 验证 agent 不可写,不需要模型的部分做成断言,agent 实际尝试的部分标【需凭据】 | `scripts/boundary-t0.sh`(三平台);`scripts/boundary-t1.sh`(Linux + Docker);`--with-credentials` 补测 |
| **客户端取证** | Hermes:在隔离环境(Docker 或临时 venv,不碰真实 `~/.hermes`)中用 `hermes hooks test` 离线验证安装后的调用链(载荷、stdout 协议、阻断语义),写 `docs/facts/hermes-cases.json`,据此修正 adapter/hermes(`patch` 工具、`pre_verify` 续跑映射等);Codex、Cursor 不碰凭据无法取证,写补测命令 | `bash scripts/probe-clients/hermes.sh`;`go test ./internal/adapter/...` |
| **其他增强** | psparse 写入目标补全(Tee-Object、-LiteralPath、Rename-Item、`[IO.File]::Write*` 等);请求文件 GC。cache GC 已在 v0.4.1 完成 | `go test ./internal/psparse ./internal/runner` |

集成验收:`scripts/accept-p2.sh`(设计文档 §10),计划三平台 CI 执行(CI 步骤待 `impl/ci-fix` 合入后接入,见设计文档 §11;目前 `ci.yml` 不含它),容器与真实客户端部分缺前提时显式 SKIP。

### 2. 已定事项(本版裁决)

- 层级配置放在独立的 root 所有文件 `<托管目录>/ballast/org.json`,`schema/rules.v1.json` 不变。
- 新鲜度不按 tier 过滤:hook 代起的测试一律在默认拒写的沙箱里执行,T1 主机上能写证据的只有 runner 本身与人工运行的 verify,tier 用于审计与 CI 判读。v0.5.4 补充:这一点以"主机上没有任何会话的项目层削弱沙箱"为全机前提(见 §8)。v0.5.5 补充:hook 代起的测试在 `S_start` 的私有副本里执行,未跟踪文件与并发改动不再能伪造 T1 结果;白名单输入与人工运行的 verify 是登记的残余风险(见 §9)。
- hook 代起 verify 的 deny 是合成决策,与同事件规则结果按既有裁决合并;spawn 失败仍然 deny 并审计。
- 统一脱敏器放在 L0,hook 可直接 import(对"hook 只依赖契约包"的唯一放宽)。
- ConfigChange 只审计、绝不阻断;M0b 未取得 payload 之前不安装。
- Hermes 的 `hooks test` 结果算"离线核验",文档须注明触发时机未核验。

### 3. 单元与顺序

13 个实现单元(P2-M0B、P2-REDACT、P2-TIER、P2-SANDBOX、P2-PS、P2-HERMES、P2-CLIENTS、P2-RUNNER、P2-HOOK、P2-REPORT、P2-CC、P2-INSTALL、P2-CLI),独占目录与依赖见设计文档 §9。第一批可并行:P2-M0B、P2-REDACT、P2-TIER、P2-SANDBOX、P2-PS、P2-HERMES、P2-CLIENTS;P2-CLI 最后集成。

### 4. 需要用户在本机或用凭据完成的事项

- M0b、boundary-t1 中标【需凭据】的用例:用户提供凭据后运行设计文档 §6.5、§5.3 的补测命令(脚本只从环境读取变量并经 `docker run -e` 传入,不落盘)。
- Codex、Cursor 的真实取证:用户在临时配置根中自行登录后运行 `scripts/probe-clients/{codex,cursor}.sh`。
- 在此之前,这些项在文档、`doctor` 与能力矩阵中保持【未核验】。

### 5. v0.5.1 修订(2026-10-16,P2 设计第 1 轮独立审核)

设计文档升至 v1.1、架构升至 v2.0.1,逐条处置见 `docs/p2-design.md` §13。计划层面的变化:

- **runner 沙箱改为默认拒写**(blocker):v0.5 的"默认全允许、只拒受保护路径"会让 hook 代起的 verify 成为沙箱逃逸原语(写 shell rc、git hooks,重命名祖先目录替换受保护文件,联网外传)。改后的规则已在本机 macOS 与 Debian 12 容器里做过设计期实测,正式用例由 P2-SANDBOX 集成测试与 `boundary-t1.sh` 固化。
- **hook 代起的 job 一律必须沙箱**;`runner_sandbox` 只管人工运行的 verify。
- **重定向锁**:声明 T1 的主机上忽略 `BALLAST_HOME` 等测试重定向变量,家目录取自 passwd;`boundary-t1.sh` 断言带重定向变量时 org 规则仍然生效。
- **覆盖判定冻结为精确字符串包含**(`tier.ManagedForms` + 共享 golden),`org render` 与 `Detect` 不会各自实现出分歧。
- **M0b 无凭据用例 14 个、需凭据 5 个**:`hooks-run-outside-sandbox`(A20)改为无凭据实测;它若失败,P2-RUNNER 与 P2-HOOK 暂停,走契约变更。
- 单元划分不变(仍是 13 个),所有权补齐:`internal/dsl/lint.go` 归 P2-HERMES(只追加),`internal/layout/resolvewith.go` 归 P2-TIER,`verify --status` 回退归 P2-RUNNER,`.github/workflows/**` 等三平台 CI 修复分支合入后再由 P2-CLI 修改。

### 6. v0.5.2 修订(2026-10-16,P2 设计第 2 轮独立审核)

设计文档升至 v1.2、架构升至 v2.0.2,逐条处置见 `docs/p2-design.md` §13。计划层面的变化:

- **已锁定主机的家目录**(major):passwd 查询冻结为 `sys.LookupHome`(darwin、linux 都用 `user.LookupId`,禁止会退回 `$HOME` 的 `user.Current`),归 P2-TIER;Linux 静态构建查不到 sssd/LDAP 用户时,只按 org.json 中 root 所有的 `home_root` 规则采信 `$HOME`。doctor `tier.locked_home`、`org render --home-root` 与 `--write` 预检、`boundary-t1.sh` 的 `t1-passwd-missing` 把这种失败变成可见的;bench 新增场景 `13` 覆盖查询本身。部署指南把 `home_root` 列为企业机群的默认步骤。
- **`hook.Deps.RedirectsIgnored`** 冻结:CLI 填值,hook 在会话首个事件审计一次。
- **`runner_allow_write` 是残余风险**,默认空;runner 把常见缓存变量指向每个 job 的临时目录(`sandbox.CacheEnv`)。
- 单元划分不变(仍是 13 个);P2-TIER 增加 `internal/sys/passwd*.go`。

### 7. v0.5.3 修订(2026-10-16,P2 设计第 3 轮独立审核)

设计文档升至 v1.3、架构升至 v2.0.3,逐条处置见 `docs/p2-design.md` §13。计划层面的变化:

- **层级判定增加第 6 条**(major):用户层、项目层、本地层的 `sandbox.excludedCommands`、`network.allowUnixSockets`、`filesystem.allowWrite`、`enableWeakerNestedSandbox`、`network.allowedDomains` 等可能跨层并入托管沙箱,让 agent 在沙箱外直接写证据。M0b 结论出来之前采取保守做法:用户层出现削弱值 ⇒ 判 T0(`lower_sandbox_weakened`);runner 对每个 job 的仓库再查项目层与本地层,出现削弱值 ⇒ 证据记 T0 并审计。M0b 新增用例 `lower-sandbox-merge`(无凭据 15 个、需凭据 5 个),`boundary-t1.sh` 新增 `t1-lower-sandbox`。
- **沙箱内的测试不得接触人的终端**(major):被包装的子进程新建会话、stdin 为 `/dev/null`、输出只经文件或管道,可写设备集去掉 `/dev/tty*`;本机 macOS 实测确认了 TIOCSTI 注入原语与修复效果。`boundary-t1.sh` 新增 `t1-tty`。
- **git 元数据按任意深度拒写**(major):嵌套 `.git`、submodule 的 `modules/**`、其他 worktree 的 `worktrees/*` 都纳入;Linux 上运行后递归扫描,新出现的受保护路径移入 `~/.ballast/quarantine/<job>/` 并记 infra,不在仓库里留下继续生效的文件。
- git dir 来源校验、cgo/NSS 构建的热路径约束、P2-HERMES 与 P2-CLI 的文档交接(minor)。
- 单元划分不变(仍是 13 个);`layout.QuarantineDir` 由设计提交落地,`ballast gc` 同时清理隔离目录。

### 8. v0.5.4 修订(2026-10-16,P2 设计第 4 轮独立审核)

设计文档升至 v1.4、架构升至 v2.0.4,逐条处置见 `docs/p2-design.md` §13。计划层面的变化:

- **项目层削弱改为在会话上检查**(major):Claude 加载的是会话项目目录(`CLAUDE_PROJECT_DIR`)的 `.claude/settings*.json`,而削弱沙箱的会话可以绕过 runner 直接写全局证据。hook 在会话首个事件检查并审计;org.json 新增 `weakened_project`(默认 `audit`,`deny` 时这类会话的工具调用全部被拒);runner 同时检查 job 仓库与请求带来的项目目录。文档如实写明:T1 证据的保证是全机范围的,前提是主机上没有任何削弱沙箱的项目层,runner 的降级只影响诚实记录。
- **低层顶层键改为白名单**(major):项目层与本地层的 `env`、会执行命令的 helper 键、插件与 MCP 自动启用、未知顶层键都算削弱;用户层(agent 不可写)只看让 agent 的命令离开沙箱的键。runner 在沙箱外启动的 git 使用清洗过的环境;darwin 发布构建要求 hardened runtime(doctor 检查)。
- **runner 沙箱的网络与本地 socket**(major ×2):macOS 上 deny 不再放行回环,`runner_network:"loopback"` 作为显式选项并登记为残余风险;本地 socket 隔离与网络策略脱钩,`allow` 只放开出站 IP。macOS 逃逸通道的验收必须脱离 process-exec 规则单独证明 mach-lookup 拒绝生效。
- 契约缺口(minor):`hook.Deps.IDs`、诊断名闭合枚举 `model.DiagNames`、`LockedHome` 自带 50ms 时限、托管目录父链检查;`docs/architecture.md` §7 与 §3.1 的 P2 追加行归 P2-CLI。
- 单元划分不变(仍是 13 个),依赖不变;bench 新增场景 `14`(deny 模式下的 PreToolUse)。

### 9. v0.5.5 修订(2026-10-16,P2 设计第 5 轮独立审核;方向由项目负责人裁决)

设计文档升至 v1.5、架构升至 v2.0.5,逐条处置见 `docs/p2-design.md` §13。计划层面的变化:

- **T1 证据不再能被未跟踪文件或 A-B-A 伪造**(major):hook 代起的 job 把 `S_start` 的树实体化到 `~/.ballast/work/<job>/tree`(临时索引 + `read-tree` + `checkout-index --prefix`),测试在副本里跑,`S_end` 在副本上重算;未跟踪与被忽略的构建输入只经 project.json 新字段 `verify[].inputs_untracked`(默认空)复制,内容哈希记入证据;含子模块的仓库记 infra。人工运行的 verify 维持在工作树上执行。白名单输入可被改写、实体化的性能代价都登记,不设门槛。
- **macOS runner 沙箱的 IPC 改为默认拒绝**(major):mach 服务查找只放行最小白名单,显式拒绝偏好写入、launchd 作业提交、服务注册、LaunchServices、AppleEvents 与绑定/入站;集成用例覆盖 `defaults write`、按键注入与剪贴板、`launchctl submit`。
- **托管配置增加任意深度形式**(major):`**/.ballast`、`**/.claude/settings*.json`;M0b 新增需凭据用例 `managed-denywrite-anydepth`(无凭据 15 个、需凭据 6 个)。条件契约:证明有效 ⇒ `weakened_project` 缺省 `audit`,否则(含未能取证)缺省 `deny`。`CLAUDE_CONFIG_DIR` 被重定向时判 T0。
- **契约缺口**(major):ConfigChange 的文件路径字段 `model.Event.ConfigFile` 由设计提交落地,P2-CC 只填值、P2-HOOK 只读取。
- **P2-SANDBOX 的 Linux 验收可执行**(major):新增独占脚本 `scripts/sandbox-docker.sh`(Debian 12 容器,只需 Docker),列为该单元必过验收,P2-RUNNER 的真实后端端到端复用它。
- minor:收尾顺序冻结(PostCheck/Quarantine 先于 S_end);macOS 目录账户的 passwd 查询与 NSS-LDAP 同列;`scripts/bench.sh` 归 P2-CLI;Hermes 补丁解析改为复制。
- 单元划分不变(仍是 13 个),依赖不变;P2-RUNNER 增加 `internal/project` 中 `inputs_untracked` 的解码。

### 10. v0.5.6 修订(2026-10-16,P2 设计第 6 轮独立审核)

设计文档升至 v1.5.1、架构升至 v2.0.6,逐条处置见 `docs/p2-design.md` §13。计划层面的变化:

- **私有副本的写入位置由构造保证**(major):runner 在沙箱外把白名单输入复制进副本时,副本里可能已有来自索引的符号链接;复制改为只经副本根上的 `os.Root`、逐级只接受真实目录、文件独占且不跟随地创建,路径前缀撞上已跟踪的非目录条目即整个 job 记 infra(`inputs_path_conflict`),不跳过。
- **检出依赖 git 的路径防护**(minor):登记为残余风险;检出前加路径布局预检(符号链接条目是其他条目的大小写不敏感前缀等 ⇒ infra `tree_unsafe_path`),检出显式打开 `core.protectHFS`/`core.protectNTFS`;沙箱内检出留作以后的契约追加。
- `internal/sandbox` 包注释的验收段与设计对齐(minor):监听只在 deny 下断言失败;偏好写入用命名域。
- 单元划分不变(仍是 13 个),依赖不变,性能门槛不变。

### 11. v0.5.7 修订(2026-10-16,P2 设计第 7 轮独立审核)

设计文档升至 v1.5.2、架构升至 v2.0.7,逐条处置见 `docs/p2-design.md` §13。计划层面的变化:

- **执行的命令与 `S_start` 出自同一次读取**(major,已闭合):hook 代起时读到的 `project.json` 条目只作为钉选值写进 sidecar;runner 实体化时要求 `S_start` 里的 `.ballast/project.json` 与它哈希相等、条目逐字相等,否则 infra `project_json_changed`,不执行;`project.json` 不在 `S_start` 中(未提交、被忽略、被 exempt 覆盖)⇒ infra `project_json_untracked`。由此 T1 仓库必须提交 `.ballast/project.json`。
- **实体化的源侧读取**(minor):真实索引与白名单源文件不跟随打开、只接受普通文件,索引设上限;换成管道或设备只会记 infra。
- **验收不可被 SKIP**(minor):P2-SANDBOX、P2-RUNNER 的 Docker 验收命令带 `BALLAST_REQUIRE_DOCKER=1`;P2-SANDBOX 交付须附 macOS 本机 IPC 用例输出(cfprefsd 等),否则这些用例在 macOS CI 接入前不算通过。
- 文档对齐(minor):P2-INSTALL 验收删去与条件契约矛盾的 `"audit"`;trust-model 按来源区分 runner 沙箱的可写集合;SecurityServer 永不放行。
- 单元划分不变(仍是 13 个),依赖不变,性能门槛不变(hook p95 ≤ 30ms)。
