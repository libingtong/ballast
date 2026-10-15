# Ballast(压舱石)实施计划 v0.2

> v0.2 根据 `docs/ballast-plan-review.md`(R01–R20,5 blocker / 13 major / 2 minor)重写。逐条修订去向见末尾"审核意见追溯表"。
> 用户已裁决(2026-10-15):信任模型 = **分级边界**;语言 = **Go**;首发 OS = **macOS + Linux + WSL2 + 原生 Windows**;M0 = **用户层探针 + 托管层容器测试都做**。

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
| A10 | Stop 输入 `stop_hook_active, last_assistant_message`;`decision:"block"` 可阻止停止;连续阻止上限 8 次 [D] | **Stop 的 `additionalContext` 会让会话继续**(语义等同反馈,不是放行);阻止计数在工具调用后重置 | 已核验 |
| A11 | PreCompact 不能注入上下文;compact 后用 SessionStart(compact) 恢复 [D] | UserPromptSubmit、PostToolBatch 等事件同样可以合法注入 | 已核验 |
| A12 | command hook 支持 `type, command, args, timeout, async` [D] | 拦截类 hook 必须同步;用 `command` + `args` 作为安装归属标识(B12) | 已核验 |
| A13 | 托管设置路径:macOS `/Library/Application Support/ClaudeCode/`、Linux/WSL `/etc/claude-code/`、Windows `C:\Program Files\ClaudeCode\`,另有 `managed-settings.d`/MDM 等 [D] | 托管值不能被低层覆盖,**但少数安全键例外**:低层更严的值仍生效。hook 另有合并语义(A15)。本机未部署 [L] | 已核验 |
| A14 | `allowManagedHooksOnly` 会阻止 user / project / local / plugin hook,但托管设置强制启用的插件 hook 例外;**低层 `disableAllHooks` 不能关闭托管 hook** [D hooks 配置章节原文] | 尚未在真实托管环境实测 | 文档已核验,实测待 M0b |
| A15 | "Hook entries merge across settings levels rather than replacing each other" [D] | 托管 hook 与低层 hook 同时执行 → 托管部署时用户层**不得再装一份**(B12 安装检测) | 已核验 |
| A16 | ConfigChange 的 matcher 只覆盖 Claude 设置和 skills [D] | **不监视任意 Ballast 文件**;部分托管刷新不触发该事件 | 已核验 |
| A17 | transcript 为每行一个 JSON 的 jsonl,属内部格式,文档没有承诺稳定 | 2026-10-15 快照:顶层 1812 份,递归 2430 份(其中 618 份为子 agent);并非每类行都带全部元数据;有 708 条 `stop_hook_summary` [R E2] | 观察 |
| A18 | 启动成本基线 [L][R E4] | `/usr/bin/true` p95 ≈ 2ms;`python3 -I` p95 ≈ 18.8ms;`node` p95 ≈ 56ms。**只说明启动成本**,不能推出完整 hook 的耗时;Go 未实测 | Go 待 M0c |
| A19 | pinrule(HEAD 2aa78c3):恰好 8 个注册函数;`_TEST_CMD_RE` 不含 mvn / gradle / dotnet / swift;`_FAIL_RE` 会把 "0 failed" 判为失败;只有 deny 一档;单层配置 [R E3] | 正常路径的 100–180ms 耗时**未核验**(审核测的是损坏输入的路径) | 部分核验 |
| A20 | **沙箱只覆盖 Bash**,文件工具、MCP server 和 hook 都在沙箱外运行;**原生 Windows 没有沙箱**;可托管的键有 `sandbox.enabled`、`sandbox.filesystem.denyWrite`、`allowUnsandboxedCommands:false`、`sandbox.failIfUnavailable:true`、`permissions.deny`、`allowManagedPermissionRulesOnly` [D sandboxing/settings/managed-settings] | 这是 T1 边界的基础(B0) | 文档已核验,实测待 M0b |
| A21 | 并行工具调用时 PostToolUse **并发触发**;PostToolBatch 每批只触发一次 [D] | → B6 需要跨进程锁 | 已核验 |
| A22 | 子 agent 的工具事件触发同一套 hook,并携带 `agent_id/agent_type`;SubagentStop 额外提供 `agent_transcript_path`、`last_assistant_message` [D] | → B6 按 agent 划分作用域 | 已核验 |
| A23 | Windows 上文件工具给出的路径一律是带反斜杠的绝对路径(即使 hook 跑在 Git Bash 里);存在 PowerShell 工具,matcher 写作 `Bash\|PowerShell`;没有 Git Bash 时 hook 经 PowerShell 运行 [D] | → B11 | 已核验 |
| A24 | PreToolUse 的 `updatedInput` 可以改写工具输入 [D] | 是否必须与 `allow` 同时使用:**待 M0a 实测**。B3 的 T1 runner 依赖这一点 | 待 M0a |

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

**T1 下的 verify 链路**(Agent 在沙箱里跑的 `ballast verify` 写不了证据,所以由 hook 代为发起):
1. PreToolUse 识别规范形式的 `ballast verify <name>`:单条简单命令,没有管道、重定向或后台。
2. hook(在沙箱外)以 detached 方式启动 runner,拿到 job_id。
3. hook 通过 `updatedInput` 把命令改写为 `<abs>/ballast verify --await <job_id>`。`--await` 只读取受保护目录中的结果并原样返回退出码,在沙箱内可以运行。
4. runner 把**测试命令本身放进 OS 沙箱**执行,保证测试代码同样写不了受保护路径:macOS 用 `sandbox-exec` 生成的 profile,Linux 用 bubblewrap,也可复用 sandbox-runtime,M0b 选型。

T0 下 CLI 直接运行测试并写证据,证据标注 `tier:"T0"`。

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
不再使用代码白名单:默认**所有被跟踪和未忽略的文件**都算验证输入,只有项目显式写入的 `exempt` 例外。这样不会漏掉 `.rs`、`.cs`、`go.mod`、锁文件或配置文件。

**内容快照 S**:用临时索引 `GIT_INDEX_FILE` 生成 tree id(`read-tree HEAD` → `add -A -- . ':!<exempt>'` → `write-tree`),**不改动用户的真实索引**。

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

**CLI 退出码**:
- `verify`:0 通过 / 1 测试失败 / 2 配置错误 / 3 超时 / 4 信号 / 5 基础设施错误(快照失败等)。
- `verify --check [--worktree]`:0 新鲜 / 10 过期 / 11 无证据 / 12 无法判定(非 git)。供可选的 git pre-commit 调用。

**runner**:T1 与 T0 的差别见 B0。runner **不持有**热路径的状态锁;多个 verify 可并行执行,按 `t_start` 排序取最新。

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
8. verify 超时。

正例:只改了 `exempt` 内的文件后提交,仍判为新鲜。

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
每个里程碑都有一个 `scripts/accept-mN.sh`:参数固定,任何断言失败即以非 0 退出,在 macOS / Linux / Windows(用 Git Bash 运行)CI 上执行。

| 里程碑 | 内容 | 验收要点(脚本内断言) |
|---|---|---|
| **M0 取证与原型** | **M0a**:在临时 `CLAUDE_CONFIG_DIR` 中用真实会话抓取用户层 payload。用例:Bash 成功;Bash exit 1;grep exit 1;启动失败;中断;compact;SubagentStop;Stop 返回 `systemMessage`;`updatedInput` 是否必须配 allow;并行 PostToolUse;exit 1 加合法 JSON 的行为。**M0b**:在一次性 Linux 容器中写 `/etc/claude-code/managed-settings.json`,测 `allowManagedHooksOnly`、低层 `disableAllHooks`、hook 合并、沙箱 `denyWrite` 对 Bash 和 Edit 的效果、MCP 写入、runner 沙箱选型。**M0c**:用 Go 写最小完整 hook 原型(放在 `proto/`,M1 前删除),测 p95;同时测 1k / 10k 文件仓库的快照耗时 | `accept-m0.sh`:校验 `docs/facts/cases.json`,每个必需的 case_id 都要有 `event`、`schema_keys`、`assertion`、`result`;`CONCLUSIONS.md` 中 A 区每个"待 M0"条目都有结论;M0c p95 ≤ 30ms,否则**停下汇报** |
| **M1 内核** | schema 与 `dsl-semantics.md` 冻结;`pinrule-mapping.md`;adapter;B7 输出协议;三层配置与热更新;状态存储与锁;最小审计写入器;DSL 引擎;mvdan 解析器与 ps 分词器;B5 注入;事件 × 动作矩阵;默认规则中除 `evidence-before-commit` 外的 4 条;`test-rules` | `go test ./... -race`;`ballast lint rules/ examples/`;`ballast test-rules rules/ examples/`;`regress.sh`(显式 deny 规则集,上文 14 例);`fault.sh`(B7 各注入阶段);`concurrency.sh`(B6);`hotreload.sh`;`bench` 门槛 |
| **M2 证据** | project.json;快照;runner(T0,外加 T1 沙箱封装);verify / `--await` / `--check` 的退出码;`verify_fresh`;`evidence-before-commit`;可选的 `claim-needs-evidence`;`init`;矩阵测试 | `freshness.sh`(8 个反例 + 1 个正例);`matrix.sh --mode real\|stub`;`bench` 中的 commit 检查场景 |
| **M3 审计、统计、回放** | 完整审计字段;轮转与 GC;脱敏;stats / promote;replay;标注 | `ballast audit lint`;脱敏 50+ 样本零泄漏,加 60s fuzz;stats 的 golden 测试;`replay.sh`:读取本地 `~/.ballast/replay-set.txt`(3 份 transcript,其中一份 >10MB,出于隐私不入库),断言报告符合 schema、内存 < 200MB、标注 ≥ 20 条且全部能关联到报告中的样本 id |
| **M4 安装与边界** | install / uninstall;`org render`;`doctor`;ConfigChange 审计 hook;T1 边界 e2e(在容器中)| `install-e2e.sh`:往返 diff 为空;重复安装幂等;安装后用户另加的 hook 卸载后仍在;原文件不存在的情况;托管层已部署时拒绝安装。`boundary-t1.sh`:在容器中让 Agent 尝试 Edit、`echo >`、`sed -i`、`python -c` 写入、伪造 `ballast` 进 PATH、直接写 evidence、MCP 写入,**全部失败**(MCP 一项按白名单配置验证)。`boundary-t0.sh`:同样的尝试**能被检测并写入审计** |

**依赖顺序**:M0 → M1 → M2 → M3;M4 的 T1 边界测试依赖 M2 的 runner。审计和状态的最小接口在 M1 就交付,避免 M2、M3 返工。

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
5. `updatedInput` 的语义和 Stop `systemMessage` 若与预期不符,B0 的 runner 链路和 B1 的 Stop warn 需要改方案。M0a 先验证这两点。
6. 按需求"任何异常都放行",磁盘写满会使强制失效。审计中会记录诊断,`doctor` 也会告警。
7. transcript 是内部格式,replay 需要做版本容错。

## 审核意见追溯表
| 审核 | 处理位置 | 处理 |
|---|---|---|
| R01 | B0, M4 | 分级边界(T1 托管沙箱 + 权限规则,T0 明示只能检测);verify 由 hook 代起并在沙箱内运行 |
| R02 | B3 | 新鲜度以内容快照为准,绑定 required 集合、最新一条记录和 `S_start==S_end`;改为全量输入减 exempt;8 个反例 |
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

## 已定事项(无需再裁决)
- 合并语义:采用 B4 的方案,org 整体不可变,原问题 3 的备选方案升格为正式方案,以消除歧义。
- Codex:只预留 Adapter 接口,不实现,也不创建任何 Codex 配置。
- 计划批准后,将本文件同步覆盖 `docs/ballast-plan.md`,v0.1 留在审核报告记录的 hash 中可追溯。
