# Ballast(压舱石)实施计划 v0.1

## Context
团队长任务里,Claude Code 会逐渐忘记或绕开少量关键规则。pinrule 证明了 "hook 强制注入 + 工具前检查" 可行,但拦截力来自写死的 8 个 Python 函数,只有 deny 一档,证据靠正则猜输出,单层配置,每次 hook 100-180ms。Ballast 要做的是:声明式规则(不写代码即可新增拦截)、分级与影子模式、以退出码为准的证据、三层配置(org 可锁)、可审计、可回放量化误拦、多语言通吃。
本文件只含计划,不含实现。**A 区**是环境事实与证据,**B 区**是方案与验收。末尾有"需你裁决的问题"。

---

# A 区:环境事实(逐条附证据)

证据来源标记:
- [D] = 官方文档 `https://code.claude.com/docs/en/hooks`、`/settings`、`/managed-settings`(2026-10-15 抓取;`docs.claude.com` 301 到此)。
- [L] = 本机实测。本机 Claude Code 2.1.300,macOS Darwin 25.5.0,Python 3.15.0,Go 1.27.2。
- 【未核验】= 未能核验,附备选设计。

| # | 事实 | 证据 | 状态 |
|---|---|---|---|
| A1 | 所有事件的 stdin 公共字段:`session_id, prompt_id, transcript_path, cwd, scratchpad_dir, permission_mode, effort, hook_event_name, agent_id/agent_type` | [D] hooks "Common input fields" | 已核验 |
| A2 | `transcript_path` 异步写入,可能滞后;Stop 事件要用 `last_assistant_message`,不要读 transcript 取最后一条 | [D] hooks | 已核验 |
| A3 | SessionStart 的 `source` ∈ `startup / resume / clear / compact / fork`,matcher 取同值。**可区分 compact 后重启** | [D] hooks SessionStart | 已核验 |
| A4 | SessionStart 输出 `hookSpecificOutput.additionalContext`(另有 `initialUserMessage / sessionTitle / watchPaths / reloadSkills`);exit 0 时纯 stdout 也作为上下文;`additionalContext/systemMessage/stdout` 超 10,000 字符会落盘并只留 2,000 字符预览 | [D] hooks | 已核验。**约束:注入文本必须 <10k 字符** |
| A5 | UserPromptSubmit 输入 `prompt`;输出 `additionalContext`,或 `decision:"block"+reason`;默认超时 30s | [D] hooks | 已核验 |
| A6 | **PreToolUse** 输入 `tool_name, tool_input, tool_use_id`(MCP 另有 `mcp_server`);输出 `hookSpecificOutput.permissionDecision ∈ allow/deny/ask/defer` + `permissionDecisionReason`,另有 `updatedInput`、`additionalContext`。多 hook 冲突优先级 `deny > defer > ask > allow`。顶层 `decision/reason` 对此事件已废弃 | [D] hooks PreToolUse | 已核验。**deny/ask/allow + 原因均可返回,ask 可行** |
| A7 | 退出码:0=成功(stdout 按 JSON 解析);**2=阻塞错误**(stderr 为原因,JSON 不能覆盖);**其他(含 1)=非阻塞错误,动作照常放行**。另有 `"onFailure":"block"`(v2.1.295+)可让失败/超时变阻塞 | [D] hooks "Exit codes" | 已核验。**fail-open 天然成立:异常退 1 即放行。注意绝不能让 panic 退出码为 2** |
| A8 | **PostToolUse** 输入含 `tool_name, tool_input, tool_response, tool_use_id, duration_ms`。**Bash 的 `tool_response` 只有 `stdout, stderr, interrupted, isImage`(本机 transcript 另见 `noOutputExpected`),没有 exit code 字段** | [D] hooks PostToolUse;[L] 对 `~/.claude/projects/*/*.jsonl` 里 Bash `toolUseResult` 的键扫描,输出 `{stdout, stderr, interrupted, isImage, noOutputExpected}` | 已核验 |
| A9 | **非 0 退出的 Bash 走 `PostToolUseFailure`**,输入含 `error`,"第一行为 `Exit code N`"(文档说"其余当展示文本")。本机 transcript 里失败的 Bash 其 `tool_result` 为 `is_error:true`、内容以 `Exit code 1` 开头 | [D] hooks PostToolUseFailure;[L] transcript 抽样(`gh pr view` 失败样例) | 文档与 transcript 一致,但**hook 实际收到的 payload 未实测** → 【待 M0 探针实测】 |
| A10 | Stop 输入含 `stop_hook_active, last_assistant_message`(另有 `background_tasks, session_crons`);`decision:"block"+reason` 或 exit 2 可阻止停止;连续继续有 8 次上限(`CLAUDE_CODE_STOP_HOOK_BLOCK_CAP` 可调) | [D] hooks Stop | 已核验 |
| A11 | PreCompact 输入 `trigger`(manual/auto)、`custom_instructions`;exit 2 可阻止压缩;`systemMessage/continue` 被丢弃 | [D] hooks PreCompact | 已核验。**PreCompact 不能注入上下文,重注入只能靠 SessionStart(source=compact)** |
| A12 | hook 条目支持 `async:true`(不能阻塞)、`timeout`、`type:"command"` | [D] hooks | 已核验 |
| A13 | **托管设置**位置:macOS `/Library/Application Support/ClaudeCode/managed-settings.json`,Linux/WSL `/etc/claude-code/managed-settings.json`,Windows `C:\Program Files\ClaudeCode\`;另有 `managed-settings.d/` 与 MDM/注册表/服务端托管。"任何 user/project/local/`--settings` 的值都不能覆盖托管键" | [D] managed-settings、settings;[L] 本机 `/Library/Application Support/ClaudeCode` **不存在**(尚未部署) | 已核验 |
| A14 | `allowManagedHooksOnly`(托管作用域,"只运行组织部署的 hook",属 Locks 类:取最严值);`disableAllHooks` 可出现在任意 settings 文件 | [D] managed-settings 表格、settings-reference 索引 | **键存在已核验;"只运行托管 hook"的完整影响清单、以及低层 `disableAllHooks:true` 能否关掉托管 hook,文档片段未读到 → 【未核验】**。备选:M0 探针实测;若能被关掉,防护改为"托管 `ConfigChange` hook 审计 + 外部 `ballast doctor` 巡检" |
| A15 | managed hook 与低层 hook 是否合并(数组并集)? | 未读到 | 【未核验】。备选:不依赖合并,org 层规则由托管 hook 命令自己读 org 规则文件 |
| A16 | `ConfigChange` 事件存在,matcher `policy_settings / user_settings / project_settings / local_settings / skills`,可用于审计配置改动 | [D] hooks ConfigChange | 已核验 |
| A17 | transcript:`~/.claude/projects/<cwd 转义>/<session_id>.jsonl`,每行一个 JSON;`type` 分布(400 个文件抽样):attachment、assistant、user、last-prompt、queue-operation、system 等。工具结果在 `type:"user"` 行的 `message.content[].type=="tool_result"` 与同级 `toolUseResult` 中;行含 `parentUuid, isSidechain, uuid, timestamp, sessionId, cwd, version, gitBranch` | [L] 本机扫描;本机共 1810 份 jsonl | 已核验。**格式为内部格式,文档未承诺稳定 → replay 需版本容错** |
| A18 | **性能基线**(本机,30 次):`/usr/bin/true` p50 1.5ms;`python3 -I -c pass` p50 17.6/p95 18.4ms;`python3 -S -I -c pass` p50 12.3ms;`node -e 0` p50 44.8ms | [L] 脚本计时 | 已核验。**含义:Python 空进程已占 18ms,留给 30ms 预算的余量极小;Go 静态二进制无此问题** |
| A19 | pinrule 缺陷 1-5 的复核 | [L] `/tmp/pinrule`(HEAD 2aa78c3):`pinrule/checks/__init__.py` 的 `REGISTRY` 恰为 8 个固定函数;`session_state.py:91-120` 的 `_PASS_RE/_FAIL_RE/_TEST_CMD_RE` 确为正则猜测,`_TEST_CMD_RE` 只含 pytest/jest/cargo test/go test/npm test/tox/mocha/vitest/rspec/phpunit,**无 mvn/gradle/dotnet/swift**;`_FAIL_RE` 含 `\b\d+\s+failed\b`,会把 "0 failed" 判失败 | 缺陷 1、3 已复核成立。缺陷 2(只有 deny)、4(耗时 100-180ms)、5(单层)**我尚未逐项复核**,实现前 M0 补测:`hyperfine` 测 pinrule hook;检查 deny 档位代码 |

**对设计的关键含义**
1. A8+A9 决定证据机制:**不能靠 PostToolUse 的 Bash 输出判成败**。所以证据一律由 `ballast verify` 自己执行并记录退出码(与用户已定方向一致)。PostToolUseFailure 的 `Exit code N` 仅作为辅助信号,标记为"弱证据"。
2. A11 决定 compact 后恢复:只能 SessionStart(source=compact) 重新注入,PreCompact 只用于落盘状态快照。
3. A7 决定 fail-open:任何异常退 0 或 1;并在入口 `recover()` 兜底,**永不输出 exit 2,除非规则明确 deny**。
4. A4 决定注入预算:12 条规则全文 <10k 字符,`ballast lint` 校验。

---

# B 区:方案

## B0 里程碑前置:M0 探针(无功能,只取证)
目的:把 A9、A14、A15、A19 的【未核验】项实测掉。做法:写一个只落盘 stdin 的最小 hook 脚本,装到一个临时 `CLAUDE_CONFIG_DIR`(不碰真实 settings),跑一次真实会话,覆盖:Bash 成功/失败、PostToolUseFailure payload、SessionStart(compact)、托管文件下 `allowManagedHooksOnly` 与 `disableAllHooks` 的行为。
验收:`docs/facts/` 下产出 4 份 payload 样本与一份结论,A 区对应行更新为已核验或确认备选。

## B1 规则 DSL
**文件**:JSON 为主(Go 标准库可读,无额外依赖;可选 YAML 不做)。`rules.json`:

```json
{
  "version": 1,
  "rules": [{
    "id": "read-before-edit",
    "enabled": true,
    "action": "log",
    "match": {
      "event": "PreToolUse",
      "tool": ["Edit", "MultiEdit"],
      "path_glob": ["**/*"],
      "input_regex": {"command": "..."}
    },
    "when": [{"not": {"read_this_session": "$tool_input.file_path"}}],
    "message": "编辑前先 Read:$tool_input.file_path",
    "inject": "编辑已有文件前必须先读取它。"
  }]
}
```

- **match**:`event`(SessionStart / UserPromptSubmit / PreToolUse / PostToolUse / PostToolUseFailure / Stop)、`tool`(名称或列表,支持 `mcp__*` glob)、`input_regex`(对 `tool_input` 指定字段做正则,Go RE2,无回溯爆炸)、`path_glob`(对解析出的文件路径)、`bash`(见下)、`text_regex`(Stop 时对 `last_assistant_message`)。
- **condition(会话谓词,封闭词表,不是通用表达式)**:`read_this_session(path)`、`edited_since(verify)`、`verify_fresh`(最近一次成功 verify 晚于最后一次代码类 Edit)、`recent_tool_count(tool,n)`、`turn_count`、`prior_hit(rule,n)`、`env(NAME)`、`branch_matches(glob)`。布尔组合 `all/any/not`。
- **action**:`deny | ask | warn | log`。映射:deny→`permissionDecision:"deny"`;ask→`"ask"`;warn→放行 + `additionalContext`;log→只落审计。Stop 事件的 deny 映射为 `decision:"block"`。
- **Bash 解析**:内置轻量 shell 分词器(引号、`;`/`&&`/`||`/管道/循环),把命令拆成"命令头+参数+是否在循环/后台"结构,供 `bash` 匹配器用(`bash.head`、`bash.arg_regex`、`bash.in_loop`、`bash.background`)。这是满足回归用例的关键。
- **逃生口**:`"check": {"exec": ["./scripts/my-check", "--json"], "timeout_ms": 500}`。外部命令得到事件 JSON,返回 `{"hit":true,"message":"..."}`;超时/出错 = 放行。默认仅允许 repo 层和 user 层使用,org 层可禁用(`allow_exec:false`)。

**pinrule 8 检查重写**(`rules.json` 预置包,默认不含个人偏好):

| pinrule 检查 | 能否用 DSL | 说明 |
|---|---|---|
| read_first | ✅ | `read_this_session` 谓词 + Write 新文件豁免(`path_exists`) |
| non_blocking(sleep/wait/长任务) | ✅(需 Bash 分词器) | 判定用 `bash.head=="sleep"` 且不在 `bash.in_loop`,且参数 ≥ 阈值。**回归用例**:`for ... curl -f && break; sleep 1; done` 里 sleep 1 在循环内→放行;`sleep 300`→命中 |
| evidence(提交前需测试证据) | ✅ | `match bash.head=="git" && arg=="commit"` + `when: not verify_fresh` |
| bypass_pinrule(篡改检测) | ✅(部分) | `path_glob` 命中 `~/.ballast/**`、`.ballast/**`、托管路径的写操作。真正的防篡改靠 B4 文件权限,不靠正则 |
| keep_pushing(Stop 时不该停) | ⚠️ 部分 | 需 `text_regex` + Stop 事件,可表达"文本含'要不要继续'"一类,语义判断则不行 → 外部 exec |
| long_term(关键词) | ⚠️ 部分 | `text_regex` 关键词可做,但是关键词级别,本质不可靠,默认 `log` |
| testset(未来泄漏,ML 场景) | ❌ 原样不写 | 领域特定、语义判断 → 作为 `examples/` 的 exec 检查示例,不进默认包 |
| chinese_plain(语言风格) | ❌ 不进默认包 | 属个人偏好(禁区),放 `examples/` |

**说明**:我只细读了 pinrule 的 read_first、non_blocking 前 60 行、bypass 前 50 行;其余 5 个的行为是从注释与注册表推断的。**M1 开工第一步是逐个读源码把上表复核一遍**,不一致处以源码为准并更新本表。

验收:`ballast test-rules`(规则自测:每条规则必带 `examples: [{input, expect}]` 正反例);预置包 6 条(read-before-edit、no-long-sleep、evidence-before-commit、no-tamper、stop-keywords、可选长期)全部通过;`go test ./internal/dsl/...`。

## B2 分级与影子模式
- 新规则缺省 `action:"log"`(文件里不写 action 也是 log)。`ballast rules promote <id> --to warn|ask|deny` 做升级,附带检查:要求该规则在近 N 天有 ≥K 次命中样本且已被 `ballast stats --sample` 看过(可 `--force`)。
- `ballast stats [--since 7d] [--by rule|session|repo]`:表头 `rule | action | hits | sessions | would_deny | 升级建议`。
- `ballast stats --rule <id> --sample 5`:从审计日志取 5 个命中现场(带前后各 2 个事件)。
- log 档下 "would_deny" 单独计数(影子判定),这是决定升级的依据。
验收:`ballast stats` 在合成日志上输出符合 golden file;`promote` 在样本不足时拒绝。

## B3 证据机制
- `.ballast/project.json`:
  ```json
  {"verify": [{"name":"test","cmd":"./gradlew test","timeout_s":1800,"paths":["src/**","app/**"]}],
   "code_globs": ["**/*.{java,kt,go,php,py,ts,swift}"], "doc_exempt": ["**/*.md","docs/**"]}
  ```
- `ballast verify [name]`:在 repo 根 `exec` 该命令(不经 hook 解析输出),记录 `{ts, cmd, exit_code, duration, head_sha, dirty_tree_hash}` 到 `~/.ballast/state/<repo>/evidence.jsonl`。**以退出码为准,绝不解析文本**,因此与测试框架无关。
- `verify_fresh` 谓词 = 最近一次 exit 0 的 evidence 晚于该会话最后一次"代码类" Edit/Write(`code_globs` 外的 Edit 如 md 不触发过期)。
- 未声明 verify 命令的项目:`ballast init` 通过探测 `go.mod / package.json / Cargo.toml / pom.xml / build.gradle(.kts) / *.csproj / Package.swift / composer.json / pyproject.toml` 给出建议命令,**用户确认后写入**,不自动写。
- **与 git pre-commit 分工**:ballast 规则在 Agent 想执行 `git commit` 时(PreToolUse)拦截,面向"Agent 行为",可 ask/warn;pre-commit 面向"所有提交者"且 Agent 可用 `--no-verify` 绕过。两者互补:`ballast install-git-hook` 可选,仅调用 `ballast verify --check`(只检查最近 evidence,不重跑)。
- 辅助弱证据:PostToolUseFailure 的 `Exit code N` 仅写入审计,**不作为通过依据**。

多语言矩阵测试(见完成条件)不跑真实框架的全套,而用**夹具**:每种栈一个最小 fixture 项目,里面的测试命令用该栈的真实命令(pytest/jest/go test/cargo test/mvn/gradle/dotnet/swift/phpunit),CI 里装对应工具链的 job 才跑;缺工具链的本机跑"命令桩"版(shell stub 返回 0/1)。两种版本都要有,报告里分开标注"真实"与"桩"。

验收:`ballast verify` 对 9 种栈的 fixture,成功→`exit_code=0` 记录通过,失败→非 0 记录失败;包括 "0 failed" 输出但 exit 0 的用例记为通过。

## B4 三层配置与防篡改
- **层级**:org(托管路径下 `ballast/rules.json`,由 root 写)> repo(`<repo>/.ballast/rules.json`)> user(`~/.ballast/rules.json`)。
- **合并语义**(待你确认,见问题 3):规则按 `id` 合并;
  - org 规则带 `locked:true` 不可被下层修改或禁用;org 的 `action` 是下限(下层可升级到更严,不可降级);
  - repo/user 可新增规则和对自己层的规则调整;同 id 冲突时 org>repo>user,但 action 取三者中最严;
  - 总规则数(合并后)≤12,超限 `ballast lint` 报错,启动加载时只取优先级高的 12 条并记审计。
- **Agent 不可写**:
  1. org 层:root 拥有的文件,Agent 进程无权限写。
  2. user/repo 层:由 ballast 自己的 PreToolUse 规则 `no-tamper` 拦截对 `.ballast/**`、`~/.ballast/**`、托管路径、`.claude/settings*.json` 的 Write/Edit/重定向/`sed -i`/`mv`/`rm` 等。这是**best-effort,不是安全边界**(Bash 可以拼出无数写法),在计划中明确这一点。
  3. 真正的边界:org 托管 + `allowManagedHooksOnly`(见 A14,待 M0 实测)+ `ConfigChange` 事件审计 + 状态目录用 `0700`、状态文件带 HMAC(密钥在 org 文件里,agent 读不到时可检测篡改)。HMAC 仅在 org 部署时启用。
- 备选(如 A14 实测 `disableAllHooks` 可关闭托管 hook):`ballast doctor` 外部巡检 + 审计缺口告警。

验收:`ballast config show --explain` 打印每条规则的来源层与最终 action;合并语义的表驱动单测;M4 在隔离的 `CLAUDE_CONFIG_DIR` 里做篡改尝试的集成测试。

## B5 审计
- `~/.ballast/audit/YYYY-MM.jsonl`,追加写,`O_APPEND` 单行 <4KB 保证原子;按月切分,保留期可配。
- 字段:`ts, v, session_id, repo_id(remote 哈希), event, tool, rule_id, rule_layer, action_configured, action_effective(shadow 时与前者不同), decision, matched(摘要,≤200 字符), cwd_rel, latency_us, ballast_version, rules_hash`。**不记录完整 tool_input**,只记命中的摘要。
- 可选上报:`BALLAST_REPORT_URL`;**默认关闭**;不在热路径上报(由 `ballast report` 或后台批量发送)。上报前脱敏:绝对路径→`~/…` 或 `<repo>/…`;疑似密钥(`AKIA…`、`ghp_`、`sk-`、`xox[bp]-`、JWT 形态、`password=`、高熵 ≥32 字符串)→ `<redacted:kind>`;用户名/主机名哈希。脱敏器有单测与模糊测试,**宁可多脱**。
验收:`ballast audit verify`(JSONL 逐行 schema 校验);脱敏单测含 50+ 密钥样本零泄漏。

## B6 回放评测
- `ballast replay <transcript.jsonl> [--rules path] [--format json|md]`:解析 transcript 的 assistant `tool_use` 与对应 `tool_result`,按序重放 PreToolUse/PostToolUse 事件,**重建会话状态**(read set、edit 时间序、evidence 以 transcript 里 `ballast verify` 的调用重建,没有则视为无证据),输出每条规则命中数、命中样本(转录行号 + 命令摘要)。
- 限制:transcript 里没有 Stop 事件与 hook payload,Stop 类规则用 assistant 最终文本近似;`transcript_path` 格式为内部格式(A17),解析器容错未知字段并带版本号。
- 误拦量化:replay 本身不知道"是否误拦",所以提供 `--label labels.jsonl`(人工标注命中样本 TP/FP)并输出每条规则的精确率,供 B2 的升级决策使用。
验收:用本机 1810 份 transcript 中挑 3 份真实会话(含一份大于 10MB 的),`ballast replay` 输出统计报告,并**由人工抽查 ≥20 个命中样本**写入 `docs/replay-report.md`。流式解析,40MB 文件内存 <200MB。

## B7 性能预算
- 预算:每次 hook p95 ≤ 30ms【待确认】,指进程启动到退出的墙钟时间。
- 测量:`scripts/bench.sh` 用 `hyperfine`(未装时回退到内置 `ballast bench`)对 6 种典型 payload(各事件)各跑 200 次,输出 p50/p95/max;同时对比 pinrule 作为基线。CI 在固定机型上阻断回归(p95 超预算 +20% 失败)。
- A18 的实测说明:Python 空进程 p50 已 17.6ms(`-S` 12.3ms),再加规则加载与正则预算几乎不可能稳定 <30ms;Go 空进程接近 `/usr/bin/true`(1.5ms)量级。**因此建议 Go**。
- 加载优化:规则合并结果按 `rules_hash` 缓存为二进制/预编译 JSON;会话状态用单个小文件 + 追加日志,避免 SQLite。

## B8 实现语言:Go vs Python
| 维度 | Go 单二进制(推荐) | Python(`ballast-cc`) |
|---|---|---|
| hook 启动 | ≈ 个位数 ms(A18 基线推断,**待 M0 实测 Go hello 的 p95**) | 空进程 17.6ms;再加 import 与规则加载,p95 很可能 >30ms |
| 分发 | 单文件,无运行时依赖,适合 Android/iOS/Java/Go/PHP 团队(不一定装 Python) | 需 Python 环境,PyPI 包名 `ballast-cc` |
| 正则 | RE2(线性时间,无灾难回溯)——hook 路径安全 | `re` 有回溯风险 |
| 迭代速度 | 稍慢 | 更快 |
| 与 pinrule 生态 | 无代码可复用(本来也不 fork) | 可参考其测试用例 |
结论:**推荐 Go**,命名约定保持 `ballast`;若你选 Python 则按 `ballast-cc` 发布。该项在"需你裁决"里。

## B9 客户端范围
只支持 Claude Code。内部定义 `Adapter` 接口(`ParseEvent(stdin) Event`、`Render(Decision) (stdout, exitCode)`、`InstallTargets()`),只实现 `claudecode`。Codex 为【待确认】,不实现、不创建任何 Codex 配置。

## 里程碑与验收命令
| 里程碑 | 内容 | 验收命令 |
|---|---|---|
| M0 | 探针取证(B0) | `ls docs/facts/*.json && cat docs/facts/CONCLUSIONS.md` |
| M1 | 引擎、DSL、注入(SessionStart 含 compact 重注入)、PreToolUse 拦截、log/warn/deny/ask、fail-open、预置规则 | `go test ./...`;`ballast test-rules`;`scripts/regress.sh`(curl 重试循环不拦,`sleep 300` 拦);`ballast bench` |
| M2 | `project.json`、`verify`、`verify_fresh`、三层配置合并、`init` 探测、`stats` 雏形 | `go test ./internal/config/... ./internal/evidence/...`;`scripts/matrix.sh`(9 栈) |
| M3 | 审计 JSONL、脱敏、`stats`、`replay`、标注与精确率 | `ballast replay <3 份 transcript>`;`go test ./internal/redact/...` |
| M4 | `install/uninstall`(外科式、先备份)、托管部署指南、防篡改、`doctor`、`ConfigChange` 审计 | 隔离 `CLAUDE_CONFIG_DIR` 下的 `scripts/e2e_install.sh`(安装→卸载后 settings 与备份 diff 为空) |

`ballast install`:只增删带 `"_ballast":true` 标记的 hook 条目;写入前把原文件复制到 `~/.ballast/backups/<ts>/`;`--dry-run` 先出 diff;不创建任何与未安装客户端相关的配置。

## 完成条件 → 验证方法
| 完成条件 | 验证方法 |
|---|---|
| 单元测试全绿;8 检查改写为 DSL 后场景测试通过 | `go test ./... -race`;预置规则的 `examples` 由 `ballast test-rules` 逐条执行;对 8 个检查各建对照用例集(从 pinrule 的 `tests/test_checks.py` 抽取输入/期望,**逐条人工映射**,不自动转换) |
| 多语言证据矩阵 | `scripts/matrix.sh`:9 栈 × {成功, 失败}= 18 例,断言 evidence 的 `exit_code` 与记录结果一致 |
| `for … curl -f … && break; sleep 1; done` 不被拦;`sleep 300` 被拦 | `scripts/regress.sh`,把两条命令喂给 `ballast hook pre-tool-use`,断言 stdout 的 `permissionDecision` |
| replay 对 ≥3 份真实 transcript | 见 B6 验收 |
| 性能 p50/p95 满足预算 | `scripts/bench.sh` 输出表格,CI 阻断 |
| hook 异常 → 放行 + stderr 记录 | 故障注入测试:损坏的 rules.json、损坏的 stdin、panic 注入、状态目录只读、超时,均断言 exit≠2 且 stdout 无 deny,stderr 有 `ballast:` 前缀日志 |

## 禁区合规
- 热路径无 LLM、无联网:构建时用 `go vet` + 依赖白名单(不引入 `net/http` 到 hook 包,CI 用 `go list -deps` 检查)。
- 不做规则蒸馏与检索:规则全量常驻,≤12。
- 安装/卸载外科式,见上。
- 默认规则不含个人偏好:chinese_plain、testset 仅在 `examples/`。

## 仓库布局(拟)
```
cmd/ballast/            # CLI 入口
internal/{adapter,dsl,engine,state,evidence,config,audit,redact,replay,install}/
rules/default.json      # 预置规则包(≤12)
examples/               # 个人偏好 / 领域检查示例(exec 型)
scripts/{bench,regress,matrix}.sh
docs/facts/             # M0 探针样本与结论
testdata/{transcripts,fixtures}/
```

## 风险
1. A14/A15 若实测发现 `disableAllHooks` 可关托管 hook → M4 防篡改降级为"可检测"而非"不可绕"。已有备选。
2. transcript 格式是内部格式,版本升级可能破坏 replay → 解析器容错 + 记录 `version`。
3. Bash 分词器永远不完备 → 规则只拦"高置信模式",其余 log;靠 replay 量化误拦。
4. 12 条上限 + 10k 注入上限限制了每条规则的篇幅 → `lint` 强校验。

---

# 需你裁决的问题
1. **实现语言**:Go(推荐)还是 Python?依据见 B8。
2. **性能预算**:p95 ≤ 30ms 是否确认?(Go 可达;Python 基本不可达。)
3. **三层合并语义**:我提议"按 id 合并、org 的 action 为下限、同 id 冲突取最严、总数 ≤12"。是否接受?备选是 "org 规则整体只追加、不允许同 id 覆盖"。
4. **Codex**:是否保留 Adapter 接口预留(仅接口,不实现)——我默认是。
5. **M0 探针**:需要在隔离 `CLAUDE_CONFIG_DIR` 里真实起一个 Claude Code 会话(会消耗少量 token)来取证 A9/A14/A15,是否同意?
