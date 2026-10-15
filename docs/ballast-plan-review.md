# Ballast 开发计划只读审核报告

审核日期：2026-10-15。对象：`docs/ballast-plan.md` v0.1，原文 SHA-256 为 `301923db83e591bf5f92cc305696b7692af9463eebc23c86e5c8d440ba05d658`。参照源码：`/tmp/pinrule`，HEAD `2aa78c3`。

**总体结论：暂不建议进入 M1 功能实现。** 发现 5 项 blocker、13 项 major、2 项 minor。应先修订权限边界、证据新鲜度、DSL 契约、热路径扩展方式和异常输出协议；可先做保持只读的文档取证、夹具设计和基准设计。

本次只新增本报告，没有修改计划、pinrule 源码、Claude 配置或 transcript，也没有运行真实 Claude 会话、安装探针或执行测试框架。源码行为复现使用 `python3 -B` 和纯内存状态，不调用状态保存 API。以下严格区分官方协议、本机 transcript 旁证和实际 hook payload；没有把后二者混为一谈。

## 证据索引与核验方法

- **D1**：[官方 Hooks reference](https://code.claude.com/docs/en/hooks)，本次实际读取 HTML 页面。文中引用其章节名定位。
- **D2**：[官方 Settings](https://code.claude.com/docs/en/settings)，本次实际读取 HTML 页面。
- **D3**：[官方 Managed settings](https://code.claude.com/docs/en/managed-settings)，本次实际读取 HTML 页面。
- **E1**：运行环境与 pinrule 版本，见下方命令输出。
- **E2**：transcript 结构、Bash 结果键与错误首行扫描，见下方摘要。
- **E3**：pinrule 八个检查的源码与纯内存调用，见下方对照表及输出。
- **E4**：进程启动基准，见下方表格。

文档抓取方法：以 Python `urllib.request` 携带 `User-Agent: Mozilla/5.0` 请求上述三个 HTML URL，以标准库 `HTMLParser` 排除 script/style 后提取正文。三个请求均成功；HTML 长度分别为 3,182,327、707,769、679,463 字符。`.md` 入口返回 HTTP 403，因此没有采用它们作为文档正文。没有依据未读到的页面作结论。

E1 实际执行：

```text
$ claude --version
2.1.300 (Claude Code)
$ python3 --version
Python 3.15.0
$ go version
go version go1.27.2 darwin/arm64
$ node --version
v26.11.1
$ uname -sr
Darwin 25.5.0
$ git -C /tmp/pinrule rev-parse --short HEAD
2aa78c3
$ test -d '/Library/Application Support/ClaudeCode'; printf 'status=%s\n' "$?"
status=1
```

**审核方式说明**：本报告由当前 Codex 会话直接核验形成，模型提供方为 OpenAI；当前工具未返回本会话的精确模型 ID，故不虚构。按 plan-auditor 技能另尝试了 `codex exec --profile deep-review --model glm-5.2 --sandbox read-only --skip-git-repo-check` 和 `--profile minimax-review --model MiniMax-M3`，两者均在初始化时报 `failed to initialize in-process app-server client: Operation not permitted (os error 1)`，没有产生模型审计结果。**本计划未经该技能要求的 GLM-5.2 / MiniMax-M3 异质审计**；本文不是这两个模型的意见。没有为此创建 `.audit/` 产物，遵循用户仅交付本报告的要求。

## 发现：blocker

### R01 · blocker：防篡改方案不能兑现“Agent 不可写规则和状态”的硬约束

**位置**：`docs/ballast-plan.md:127`，B4，尤其第 130 行；风险第 205 行。

**问题**：计划正确承认 repo/user 的 `no-tamper` 是 best-effort，但随后把 `0700`、HMAC、托管 hook 和配置审计组合称为“真正的边界”。`0700` 防其他用户，不能阻止相同用户身份的进程写入。若 hook 能直接读取 org 文件中的 HMAC 密钥，而 Agent 的命令拥有相同文件访问权限，Agent 也能读取密钥；若通过文件权限使该身份读不到，hook 也无法直接读取。HMAC 还不能单独检测整份状态被回滚、删除或替换成旧的有效签名版本。`ConfigChange` 审计不阻止磁盘写入，也不监视任意 `.ballast` 状态文件。

**证据**：D1 “Security considerations / Disclaimer”明确：`Command hooks execute shell commands with your full user permissions.` 计划没有定义不同身份的状态服务、强制沙箱或密钥代理。D1 “ConfigChange”监视的是 Claude 设置/skill 文件，并明确部分托管策略刷新不触发该事件。D1 还已确认低层 `disableAllHooks` 不能关闭 managed hook，因此风险第 205 行的假设不是当前文档中的主要绕过路径；真正的问题仍是共享权限和可写状态。

**修订建议**：明确产品的信任模型。保持原硬约束时，需要 Agent 无法取得的写入能力，例如独立身份的最小状态/验证服务，或覆盖所有工具入口且不可自行退出的强制隔离；指定受保护的规则、状态、验证配置、可执行文件、密钥及其父目录。托管 hook 应使用受保护的绝对可执行路径，不能信任 Agent 可改的 PATH/脚本。CLI 的合法配置维护与 `verify` 写证据须经受控入口完成，不能靠“允许 ballast CLI”整体豁免。若只做同用户 best-effort，必须明确申请变更需求，不能在计划中宣称已满足不可写。

### R02 · blocker：`verify_fresh` 只比较会话 Edit 时间，记录的仓库指纹没有参与判定

**位置**：`docs/ballast-plan.md:108`，B3，第 111～114 行；B1 第 76、87 行。

**问题**：`head_sha`、`dirty_tree_hash` 虽被记录，`verify_fresh` 的定义却不比较它们。成功验证后，经 Bash 脚本、`git checkout/reset/merge/cherry-pick/apply`、编辑器或另一会话改代码，本会话最后 Edit 时间仍不变，可以沿用旧证据提交。一个会话操作多个 repo、`git -C` 切换目标时也没有明确绑定规则。验证运行期间发生修改，结束时间晚于 Edit，并不表示命令验证了最终版本。

还有两个独立漏洞：最近一次**成功**记录会忽略其后失败的 verify；多个具名 verify 只运行任意一个成功命令，可能就满足全局谓词。`paths` 的覆盖语义和必需验证集合未定义。

**证据**：计划第 112 行的完整谓词只有“最近一次 exit 0 的 evidence 晚于该会话最后一次代码类 Edit/Write”。据此可直接推导：`verify(A) 成功 → 另一会话改 A → 当前会话 commit` 仍为真。第 109 行的代码白名单还漏掉 `.rs/.cs/.tsx/.jsx/.c/.h/.cpp`，以及 `go.mod`、`pom.xml`、Gradle、依赖锁文件、生产 YAML/JSON 等可影响测试的输入。它与计划承诺的 Rust、.NET 等栈不一致。

**修订建议**：先给出可判定的新鲜度不变量：证据绑定仓库/工作树实例、实际验证的内容快照、验证命令及配置版本、覆盖范围、开始与结束状态；提交时比较实际将提交的内容与受测快照。规定验证中修改、后续失败、取消/超时、多个必需命令和并行验证的处理。证据失效应覆盖所有写入来源，不能仅依赖 Edit hook。扩充并定义代码/构建输入识别；文档豁免为明确的项目策略。为上述序列建立反例验收，并验证快照检查的成本仍满足热路径预算。

### R03 · blocker：DSL 契约不足以执行计划自己的规则示例和八检查覆盖承诺

**位置**：`docs/ballast-plan.md:55`，B1，第 75～96 行；完成条件第 180 行。

**问题**：封闭谓词表没有第 85 行所需的 `path_exists`；没有用户上轮 prompt 匹配、最近验证失败等谓词；没有定义数字比较、字段缺失、多个 match 字段的组合、数组 `when` 的含义及多条命中后的裁决。示例针对 Edit，却同时要求 `input_regex.command`；如果 match 字段是 AND，Edit 通常没有 command，规则将失效；如果是 OR，又会扩大命中范围。`turn_count` 等裸名称如何与阈值比较也没有语法。

八检查的完整复刻还需要尾部窗口、代码块剥离、邻近上下文、计数/比例等能力，当前只列了原始正则与少量状态谓词。把这些交给 exec 后，就不再满足“通过声明式 DSL 新增拦截、不写代码”的对应覆盖承诺。

**证据**：E3 的 `read_first.py:60` 实际检查文件是否存在；`keep_pushing.py:101` 读取 user_prompt，`104～105` 取末尾 80 字；`bypass_pinrule.py:129～147` 读取最近 Bash 失败状态；`chinese_plain.py:154～177` 计算比例与术语数量。第 76 行封闭词表均未提供对应操作。

**修订建议**：M1 前冻结一份可校验 schema 和求值契约；提供真实可解析的规则，不以伪表达式充当规范。逐个标明“DSL 等价覆盖 / 有意改变 / 不支持”，补充必要的有限字段选择、转换与状态谓词，或收窄八检查的等价承诺。对缺失字段、非法类型、组合优先级和多规则冲突补充正反例。默认规则的示例也必须通过同一解析器。

### R04 · blocker：同步外部 exec 逃生口破坏热路径无联网、无 LLM 和性能约束

**位置**：`docs/ballast-plan.md:79`，B1；B7；禁区第 188 行。

**问题**：repo/user 规则默认可以同步运行任意外部程序，超时为 500ms。该程序可联网、调用模型或持续运行数百毫秒。Go 包不依赖 `net/http`，不能限制子进程及其后代。即使普通负载很快，只要某类典型 payload 每次调用一个耗时 100ms 的检查，该 payload 的 p95 就必然超过 30ms。

**证据**：第 79 行的 `check.exec`、`timeout_ms:500` 和默认允许 repo/user，与第 148 行及用户硬约束直接冲突。第 188 行 `go list -deps` 只能看到 Go 依赖，无法约束外部可执行文件行为。

**修订建议**：从同步 hook DSL 中移除任意 exec。扩展检查移到显式离线 CLI 或后台评估，hook 只消费有期限、可验证的本地结果；或者提供能实际限制网络、执行能力和总耗时的隔离设计并取证。不得以 org 可选 `allow_exec:false` 代替所有部署模式的硬约束。

### R05 · blocker：A7 的“异常退 1 即放行”不成立，必须控制 stdout 的提交时机

**位置**：`docs/ballast-plan.md:24`，A7；关键含义第 41 行；故障验收第 185 行。

**问题**：仅保证异常时退出码不是 2，不能保证放行。如果 hook 已输出一个合法 deny JSON，后续写日志/状态失败而退出 1，Claude 仍可能执行 deny；即使退出 0，也仍会执行该 JSON。入口 `recover()` 无法撤回已写出的 stdout。

**证据**：D1 “Exit code output”原文：`if a PreToolUse hook exits 1 but prints JSON that passes validation, the run is a success and the JSON fields decide what happens.` 表格进一步说明有效 JSON 在其他非零退出码时仍可决定行为。A7 省略了这一关键条件。

**修订建议**：整个处理过程先在内存中形成唯一结果，所有可能失败的操作完成后才输出控制 JSON；错误路径清空未提交的决策，输出无决策结果或空 stdout，并写 stderr。显式不配置 `onFailure:"block"`，同步 hook 不用 async。测试应在“已形成 deny、尚未提交 stdout”等阶段注入错误，并通过真实协议适配器验证最终放行；不能仅断言 `exit≠2`。对输出本身已成功、随后进程崩溃的情况应界定可兑现的保证，不能声称 recover 能撤销已交付决策。

## 发现：major

### R06 · major：四档 action 缺少逐事件映射，Stop 的 warn 会实际阻止停止

**位置**：`docs/ballast-plan.md:75`，B1 第 77 行；M1 第 170 行。

**问题**：DSL 允许六种事件，但 deny/ask 通用映射只适用于 PreToolUse。UserPromptSubmit 的 deny 未映射，SessionStart 无同等阻塞能力，PostToolUse 后无法撤销工具执行。尤其 Stop 的 warn 若使用所提 `additionalContext`，并不是“放行停止”，而是继续会话；Stop ask 也没有文档支持的同构权限提示。

**证据**：D1 “Stop decision control”：`hookSpecificOutput.additionalContext` 是非错误反馈，但 `The conversation continues`，并受与 block 相同的循环保护。D1 “PreToolUse decision control”：`permissionDecision:"allow"` 会跳过通常权限提示，故 warn/log/异常 passthrough 也不能误用 allow 代替“无决策”。D1 同时说明 `-p` 中无人回答 ask 时调用可被拒绝。

**修订建议**：列 event × action 的有效矩阵；不支持的组合在 lint 阶段拒绝，不能静默降级。明确 Stop warn 的产品语义及用户可见渠道，默认放行用无决策输出。补上交互模式、`-p`、权限模式以及其他 hook 冲突下的集成用例。

### R07 · major：B1 对 pinrule 的行为推断有实质错误，八检查验收范围前后不一致

**位置**：`docs/ballast-plan.md:83`，B1 八检查表；第 94、96、180、191 行。

**问题与证据**：

- `keep_pushing` 的“要不要继续”示例方向相反。源码 `keep_pushing.py:136～138` 遇尾部问号就豁免，`171～180` 对没有推进/问号等豁免的陈述默认命中；还考虑用户叫停、饱和、成功汇报等条件。它是确定性规则，不是必须靠语义模型的检查。
- `long_term` 不只是 Stop 关键词。`long_term.py:231～259` 检查 Bash/Write/Edit/NotebookEdit，区分文档/测试描述上下文；`23～95` 还有硬编码列表、Git 跳验证及注释模式。第 29、72 行含 Python 反向引用/前后查找，不能直接复制为 RE2。
- `bypass_pinrule.py:129～147` 还有“测试失败后未读就 Edit”一路，计划只描述写敏感路径。`155～188` 对维护 CLI、语言 `-c`、重定向目标作区分，不能用出现路径就等价替换。
- `evidence.py:105～134` 还检查最终回复的完成/弱声明与附近代码任务语境，计划只保留 commit 前检查。
- `testset.py` 是固定正则和上下文豁免，不调用语义模型；“领域特定，所以不进入默认包”合理，“因此 DSL 不可表达”没有成立的证明。

E3 的实际输出见后文：问号不命中、普通陈述命中；源码另一路 bypass 确实命中。第 96 行只验六条预置规则，第 180 行却要求八检查改写后通过。

**修订建议**：用后文源码对照表替换推断表，明确每条保留/舍弃的行为。不要把“业务上不推荐默认启用”和“引擎表达不了”混为一谈。明确八检查是兼容性目标还是仅借鉴样例，并据此重写完成条件。

### R08 · major：两个 Bash 回归样例可以实现，但“轻量分词”尚不是足够的解析契约

**位置**：`docs/ballast-plan.md:78`，B1 第 86 行；第 182、207 行。

**问题**：**两个指定样例本身可以区分**：若能正确识别 for/do/done 的语法作用域，给循环中的 sleep 命令设置 `in_loop=true`，并按参数匹配 standalone sleep 300，现有字段方向足够。不能据此断言方案做不到。但是词法分词不会自动得到循环语法树、重定向的写目标或 Git 的子命令；计划没有定义每个匹配条件是否必须落在同一条子命令上。

对“所有循环内 sleep 都放行”的规则，`while true; do sleep 300; done` 也会放行。这不同于只豁免有限 curl 重试，也不覆盖 pinrule 的 wait、长任务及语言内等待。`bash.background` 是否同时取 `run_in_background` 与 shell `&` 未定义。`git -C repo commit`、`env X=1 sleep 300`、引号内假命令、heredoc、命令替换、`sh -c` 等都影响覆盖边界。

**证据**：E3 中 pinrule 对原 curl 重试和 sleep 300 均命中，说明 Ballast 需有意改变语义，不能把“逐条人工映射原期望”当无差异回归。计划目前仅规定第 78 行的字段，没有规定上述语法及关联规则。

**修订建议**：定义支持的 shell 语法子集、每个命令节点的匹配作用域、重定向目标和包装器处理；未解析部分记录 unknown/诊断并按既定 fail-open 处理。增加有限重试、无限循环、长 sleep 循环、后台、引号/注释/重定向及 Git 全局参数的正反例。两条核心回归用例使用显式 deny 测试规则，避免默认 log 导致“sleep 300 被拦”的验收自相矛盾。

### R09 · major：三层合并的 locked、action 下限和超限截断缺少一致定义

**位置**：`docs/ballast-plan.md:123`，B4 第 124～126 行；裁决问题第 215 行。

**问题**：`locked:true` 的“不可修改”与“下层可升级 action”存在未说明的例外。对未 locked 的 org 同 id 规则，`org>repo>user` 是整对象胜出还是字段级合并没有定义；前者使 unlocked 的意义不清，后者可能用下层修改 match/when/enabled 绕过 org。用户原约束 org 不可覆盖，也不能仅给可选 locked 才提供保护。action 取最严又跨越普通层级，尤其 R06 中并非每事件都存在可比较的四档。

超限时只取“优先级高的 12 条”没有确定排序、同层 tie-break、enabled 是否计数以及 org 自身 >12 的处理；启动仍继续加载，可能丢弃组织规则。

**证据**：第 124 行既禁止修改，又允许 action 升级；第 125 行同时规定来源优先和 action 特殊合并；第 126 行没有定义“优先级”的字段或算法。

**修订建议**：明确 org 规则不可变字段及仅可加强的例外，给 locked/unlocked、enabled、缺省 action、不同事件组合提供完整真值表。超限应在发布配置时拒绝；运行期异常按统一 fail-open 合同处理并产生明确诊断，不静默截断有效规则。若保留有限加载，至少定义稳定排序并证明不会丢 org 规则，说明这如何符合原约束。

### R10 · major：会话状态与证据缺少跨进程事务、幂等和仓库实例标识

**位置**：`docs/ballast-plan.md:111`，B3；B5 第 136～137 行；B7 第 151 行。

**问题**：单个小状态文件加追加日志，不等于 load → modify → save 原子。并行 PostToolUse 可丢失 read set、edit 序号、prior_hit 和工具计数；重复投递可能双计。多个 verify 并行完成时，完成顺序不代表内容版本顺序。`repo_id(remote 哈希)` 无法区分同 remote 的不同 clone/worktree，也未定义无 remote、多个 remote、remote 改名的情况。B3 `<repo>` 与审计的 remote 哈希是否同一个身份也不明确。

**证据**：D1 “PostToolBatch”明确 `PostToolUse ... fires concurrently when Claude makes parallel tool calls`。pinrule `session_state.py:18～26,379～441` 已有跨进程 flock，明确说明不锁整个读改写事务会丢更新；Ballast 计划没有对应协议。第 137 行审计字段也没有 `tool_use_id` 或事件幂等标识。

**修订建议**：区分 workspace/repo、session、agent 三种作用域；会话键至少包含可信 repo/worktree 实例、session_id、agent_id。定义跨进程锁、短锁时限、原子替换、序号、去重、崩溃恢复及读一致性；验证命令不要持有热路径锁跑测试。加入两个独立 hook 进程、两个 verify、跨会话和跨工作树的集成测试。`go test -race` 只能覆盖进程内竞争，不能替代这些测试。

### R11 · major：子 agent 的独立状态、注入和结束事件未进入设计

**位置**：`docs/ballast-plan.md:18`，A1；B1 第 75～76 行；B5 第 137 行；B6。

**问题**：知道 agent_id 存在不等于处理子 agent。DSL 事件词表没有 SubagentStart/SubagentStop，审计也没有 agent_id。共享 session 状态可能让子 agent 的 Read 满足主 agent 的 read-before-edit；只监听 Stop 会漏掉子 agent 的最终回复检查。没有规定 read 状态继承、证据共享、结束后的保留/清理，以及内部 agent 是否进入统计。

**证据**：D1 “Configuration”说明 settings/managed/plugin hooks 同样在子 agent 的工具调用中运行。D1 “SubagentStop input”明确主 transcript_path 与 `agent_transcript_path` 不同，另有 agent_id、agent_type、last_assistant_message；内部 agent 也可触发此事件。E2 发现 618 个嵌套 subagent transcript，抽样行有 `agentId`。pinrule 已按 `(session_id,agent_id)` 分状态并在 SubagentStop 清理。

**修订建议**：定义主/子 agent 的 scope 与继承矩阵；纳入需要的生命周期事件、独立 read set、按仓库内容绑定的证据共享策略、审计关联和清理。对并行子 agent、内部 agent、正常结束/异常结束/恢复补测试。

### R12 · major：replay 不能仅凭 transcript 中出现 verify 调用重建可信证据

**位置**：`docs/ballast-plan.md:142`，B6 第 143～145 行。

**问题**：transcript 没有直接记录 `ballast verify` 的受测指纹与内部子进程退出码。`ballast verify; true`、`ballast verify || true` 的外层 Bash 可成功，后台调用的即时结果也不代表验证完成。只看“调用出现”会产生假证据；没有调用不等于没有此前或其他会话的合法证据。Stop 虽无完整输入，但本机存在 stop_hook_summary，不能一概说没有任何 Stop 痕迹，也不能拿 summary 冒充完整 payload。

不可精确回放的还包括：历史 `env`、branch、path_exists、实际 read 前文件是否存在、工具被拒绝/执行失败的状态转移、规则热更新、fork/resume/compact 的初始状态和并发先后关系。exec 检查若重跑，既不确定，还会产生副作用。

**证据**：E2 有 708 条 `system.subtype=stop_hook_summary`，以及失败 Bash 的字符串结果；成功 Bash dict 没有结构化 exit_code。B3 的 evidence 字段没有 transcript 的稳定关联 ID，B6 也没有要求输入 evidence 或规则快照。

**修订建议**：把 replay 定位为可解释的离线评估：区分 known-hit / known-miss / unknown / approximate；仅在取得可信 evidence 记录及对应内容/规则快照时回放 verify_fresh。支持显式初始状态与证据 sidecar，以工具 ID、agent 和事件序号关联；外部 exec 默认不执行。对不可重建的规则标 unsupported，不将 unknown 当真实违规计算精确率。Stop 推断单独报告，标注结果绑定稳定样本 ID 和 rules_hash。

### R13 · major：审计记录不足以支持 stats 的上下文采样，增长与并发维护不完整

**位置**：`docs/ballast-plan.md:99`，B2；B5 第 136～139 行。

**问题**：日志只记录命中的摘要，却承诺样本前后各两个事件；没有非命中事件或 transcript 定位，无法还原这些上下文。`would_deny`、configured/effective action、shadow 的关系未定义：缺省 log 本身不携带“未来准备升级为哪一档”的目标。统计“已看过样本”也没有持久化凭据。

按月切分和保留期已有覆盖，但单月无大小上限；没有 state/evidence 清理、轮转与并发 writer/reader、损坏尾行或磁盘满的处理。`O_APPEND` 配合“单行 <4KB”不能单独证明所有平台/文件系统的完整 JSON 行写入；需保证一次完整写调用、检查短写，并协调清理与在途写入。4KB 不是普通文件通用的记录原子性协议。

**证据**：第 101 行要求相邻事件，第 137 行明确不记完整输入且只列 matched 摘要、没有 tool_use_id/transcript 行号。第 136 行没有单次写协议、锁或轮转协调；第 151 行状态又采用追加日志。

**修订建议**：定义小型事件索引或有界会话环形缓冲，给命中记录稳定事件/工具/agent 定位，并规定脱敏；清楚定义影子目标和 would_deny 分母。将月切分补成大小/保留上限、状态与证据 TTL、轮转/清理并发协议、损坏恢复和磁盘满的 fail-open 验收。日志 schema 校验只能证明格式，不能证明防篡改；避免 `audit verify` 名称导致能力混淆。

### R14 · major：性能预算被改成待确认且 CI 放宽至 36ms，语言结论超出证据

**位置**：`docs/ballast-plan.md:35`，A18；B7 第 148～151 行；B8 第 156 行；裁决问题第 214 行。

**问题**：用户已给定 p95≤30ms，计划却写【待确认】；“超预算 +20% 失败”容许 30～36ms。A18 测的是空进程，不能推出 Python 的真实 hook 几乎不可达，更不能证明 Go 含解析、三层加载、状态锁和审计后的 hook 达标。仅六个“小 payload”也不足以代表 12 条上限、缓存失效、锁竞争和大 Bash 输入。

**证据**：E4 复现 Python 空进程 p50 17.580ms、p95 18.761ms。该结果证明启动成本，而非剩余工作必然超过约 11ms。没有 Go hello 的实际测量；计划自己也标“待 M0 实测”。pinrule 的异常输入启动基准也不是正常业务路径耗时。

**修订建议**：把 30ms 固定为验收门槛，CI 直接检查 p95≤30ms，另外才可报告相对回归比例。Go 是合理工程候选，表述为待验证选择。补充包含进程启动的独立父进程计时方法、分位数算法、机型/负载、预热/冷态、12 规则和并发/损坏状态负载。不要用内置 bench 的进程内循环冒充启动到退出计时。真实完整路径测量后再得出 Go/Python 性能结论。

### R15 · major：`CLAUDE_CONFIG_DIR` 不能隔离 M0 的系统托管设置实验

**位置**：`docs/ballast-plan.md:48`，B0；第 133、169、173、217 行。

**问题**：临时用户配置目录可以隔离用户设置、历史和插件，不能把 `/Library/Application Support/ClaudeCode` 等系统策略目录重定向到临时目录。本机托管目录不存在，往 temp config 放一个同名文件不能证明它按 managed 权限生效；写真实系统路径又违背“不碰真实 settings”的承诺。

**证据**：D2 的 CLAUDE_CONFIG_DIR 说明只提 home-directory settings/session history/plugins；D3 “Where each mechanism stores the policy”明确托管文件使用系统路径。E1 确认本机该路径不存在。D1 已明确跨层 hook 合并、低层 disableAllHooks 不关闭托管 hook，因此 A14/A15 可先完成文档核验，不必保持悬而未决。

**修订建议**：分开用户配置隔离测试与托管权限集成测试。后者采用一次性 VM/容器或受控测试机的真实系统策略路径，并验明实际 setting source；不要用普通 `--settings` 模拟 managed 来源。本次只读审核不运行这些写入实验。M0 输出还应包含明确 case ID、事件名、原始 payload schema 与断言结果，而不只要求“4 份样本”。

### R16 · major：里程碑依赖和验收命令还不能机械证明完成

**位置**：`docs/ballast-plan.md:96`，B1；第 166～185 行。

**问题**：M1 要交付 evidence-before-commit，但 verify_fresh 在 M2；M1 的 log/状态审计与 M2 stats 又依赖 M3 才交付的审计。必须先安排最小基础协议或明确 M1 只用夹具，不能在 M1 声称完整预置规则已工作。新规则默认 log，与默认预置规则测试要求 sleep 300 被 deny 之间也没有指定测试配置。

`ls ... && cat ...` 只验证文件能列出/读出；`ballast replay <3 份 transcript>` 是占位文本，按原样不是可执行验收命令。人工抽查≥20可以保留，但应机验标签数量/关联/报告结构。M4 往返 diff 空不能证明安装期间新增的其他 hook/设置被保留、重复安装幂等、原文件不存在时恢复正确。九栈真实/桩测试已有区分，但里程碑没有规定哪些模式必须跑、缺工具链是 skip 还是 fail。

**证据**：对应里程碑表及 B1/B3/B5 的交付定义直接存在上述依赖。当前仓库是计划阶段，`git status` 返回 `not a git repository`；没有实际 Ballast 可执行程序或验收脚本可以代为运行，因此本报告不声称验收命令已通过。

**修订建议**：把最小 config/state/audit/证据接口提前，明确 fixture 与真实功能的完成边界；实际依赖完成后再验收 evidence 规则。提供固定参数、断言和非零失败退出的入口脚本；核心测试使用显式 action。补 cross-process、子 agent、四档事件矩阵、热更新、安装并发和篡改边界用例；标明真实工具链矩阵的必跑 CI job。失败 verify/timeout/信号的 CLI 退出码及 `--check` 语义也要先写明，否则 pre-commit 不能据此可靠判定。

### R17 · major：持续注入与规则热更新没有明确触发和一致性协议

**位置**：`docs/ballast-plan.md:4`，Context；B1 第 75 行；B7 第 151 行；M1 第 170 行。

**问题**：实现范围只明确 SessionStart 与 compact 重注入，没有规定 UserPromptSubmit 的周期性提醒或长 turn 内持续注入。Bash/Stop 会话运行很久而未 compact 时，持续注入目标没有验收。缓存按 rules_hash 命名，也没有说明怎样发现规则源文件变更，导致检查与注入可能使用不同版本。`ConfigChange` 不自动监视任意 `.ballast/rules.json`。

**证据**：D1 “ConfigChange”仅列 Claude 设置和 skills；pinrule `rule.py:127～130,189～198` 明确区分 SessionStart baseline、每 turn anchor 和中段补注入。Ballast 计划没有对应节奏、版本切换或预算条款。

**修订建议**：定义持续注入的具体事件与预算；规则合法发布后在下一次 hook 采用同一完整 generation，并在会话中注入变更摘要/必要全文。规定新增、删除、禁用、promote、org 更新、缓存失效和原子替换；非法/半写文件按统一异常合同放行并诊断，不静默使用不明版本。增加“不重启会话修改规则后生效”的集成验收，审计记录实际使用的 rules_hash。

### R18 · major：Windows 路径、glob 和工具覆盖没有契约

**位置**：`docs/ballast-plan.md:30`，A13；B1 path_glob/Bash；B3 code_globs；B4；B8 单二进制分发。

**问题**：`~/.ballast/**` 与 POSIX 分隔符示例不能直接匹配 Windows 原生路径。没有规定盘符、大小写、UNC、Git Bash 路径、符号链接/联接和 repo 边界归属。glob 的 `**`、花括号、多路径与取反语义也未定义；不能假设 Go 的默认 filepath 匹配自动支持这些扩展。Windows 还有 PowerShell 工具入口，仅分析 Bash 会漏掉写状态/提交等操作。

**证据**：D1 “PreToolUse input”明确 Read/Write/Edit 路径总是绝对路径，Windows 使用反斜杠，即使 hook 运行在 Git Bash；并提醒 `/src/` 比较不会匹配反斜杠路径。D1 的失败工具章节同时说明 Bash 和 PowerShell 的 Exit code N 格式。计划只有 Windows 托管位置，没有后续实现/验收约束。

**修订建议**：明确首发 OS 支持矩阵；若支持 Windows，定义原生路径规范化和 glob 方言，加入 Windows CI、包含空格的托管命令路径、UNC/盘符/联接测试及 PowerShell 策略。如果首发仅 macOS/Linux，应明确收窄范围，避免单二进制分发被理解为已支持所有平台。

## 发现：minor

### R19 · minor：安装归属标记 `_ballast` 未核验为稳定保留的 hook 字段

**位置**：`docs/ballast-plan.md:175`，install/uninstall。

**问题**：官方 handler 字段说明没有给 `_ballast` 的保留承诺。本次没有实际安装，因此**无法核验** Claude 的校验/设置编辑过程是否接受并保留它。不能仅靠一个未经协议保证的自定义键判定可删除条目。时间戳备份也未定义碰撞、权限和保留策略。

**证据**：D1 “Hook handler fields”描述官方字段；计划 A/B 中没有 `_ballast` 实测证据。这是未核验依赖，不是已证明该键会被拒绝。

**修订建议**：M0/M4 验证加载及设置重写后的保留行为；使用受控安装清单与精确 command/args 身份作为可审计归属，避免误删用户仿写/修改的条目。备份使用唯一标识、限制权限，并为 uninstall/dry-run 明确备份与恢复行为。

### R20 · minor：环境事实表需要区分可选字段、历史快照和部署保证

**位置**：`docs/ballast-plan.md:18`，A1；第 25、30、34～36 行。

**问题**：A1 把并非每事件都有的字段写成“所有事件公共字段”；A8 把观测字段写成穷举；**A9 的“非 0 Bash 都走失败事件”忽略工具对非零返回值的语义解释**；A13 把 managed 优先级写成无例外；A17 文件数是动态快照；A18/A19 还把历史数字或部分复核写为整体已核验。A11 的“只能 SessionStart 重注入”也应理解为 compact 恢复的选择，不能排除后续 UserPromptSubmit/PostToolUse 的合法注入。

**证据**：见下面 A 区汇总表的逐项引用。D1 对 prompt_id、scratchpad_dir、permission_mode、effort、agent 字段均有缺省条件，duration_ms 也是可选；D3 明示更严格的低层安全值存在例外。本次顶层 transcript 数为 1812，历史 1810 不能据此判为伪造。

**修订建议**：每项拆成协议事实、可选性/版本门槛、本机观察及推论；历史测量附脚本、样本时间和统计方法。对运行期 payload 尚未实测的事实保留精确标签，避免以“已核验”掩盖证据边界。

## pinrule 八个检查：源码行为与 DSL 覆盖核对

以下阅读覆盖八个函数完整主体，注册表为 `/tmp/pinrule/pinrule/checks/__init__.py:28～37`，没有根据文件头注释推断实现。

| 检查 | 源码实际行为与位置 | 当前 DSL 能力判断 |
|---|---|---|
| read_first | `read_first.py:20～77`：Edit/Write 未读拦截；新建 Write 豁免；支持 canonical 多文件 Add/Update/Delete 语义。 | 基础未读判断可做；缺 path_exists 和多目标量化语义，示例还遗漏 Write。不能标为现规格完整等价。 |
| non_blocking | `non_blocking.py:94～165`：sleep、shell wait（有命令头豁免）、语言 `-c` 真等待、前台长任务；背景与引号处理有专门逻辑。 | 两个核心样例可做；只检测循环外长 sleep 是有意收窄，不能算完整重写。源码并非所有 pytest/npm test 都按长任务拦，实际长任务列表见 76～86 行。 |
| evidence | `evidence.py:80～134`：commit 前查新鲜测试；非代码提交和同命令链测试有豁免；Stop 完成/弱声明附近代码任务词也查证据。 | commit 的简化版本可做；原始豁免不应盲目照搬，且 Stop 行为缺失；verify_fresh 需先修 R02。 |
| bypass_pinrule | `bypass_pinrule.py:129～147,155～202`：最近测试失败后未读就 Edit；敏感字段/路径加实际写入信号；维护 CLI、语言与重定向目标豁免。 | 路径写保护仅覆盖一部分；失败后 Edit 缺状态谓词，实际写目标缺解析契约。仍不是安全边界。 |
| keep_pushing | `keep_pushing.py:85～180`：用户叫停、饱和、推进计划、尾部问号、授权交接、成功汇报等豁免；停顿或普通陈述命中。 | 确定性逻辑，不需要 LLM；原规格缺 user_prompt、尾窗与有序豁免。不是“问继续就拦”。 |
| long_term | `long_term.py:23～95,197～259`：工具输入里的硬编码列表/ID、Git 跳验证、TODO/临时注释及描述上下文豁免；Stop 意图话术、否定窗口、代码块剥离。 | input_regex 可覆盖若干正例；纯 Stop 关键词不是完整覆盖。Python 的 backreference/lookaround 及预处理不可直接搬入 RE2。 |
| testset | `testset.py:31～110`：固定正则检测反喂数据、detail 回流、跨 split 复制、边界硬编码、case ID；文档/测试/探针与语言命令豁免。 | 领域特定，但多个模式可声明式表达；完整豁免要扩展。放 examples 合理，称之为必须语义判断不准确。 |
| chinese_plain | `chinese_plain.py:119～214`：剥离结构内容，计算中文占比、英文词数，术语数量及括号解释窗口。 | 正则不能替代计数比例与预处理；保持 examples/显式可选合理。若仅提供 exec 示例，不应声称八项均改写为 DSL。 |

E3 可复现调用方式：

```python
# python3 -B；纯内存，不调用 save/update_state
import sys
sys.path.insert(0, "/tmp/pinrule")
from pinrule.session_state import SessionState, _FAIL_RE, _TEST_CMD_RE
from pinrule.checks import keep_pushing, long_term, bypass_pinrule, non_blocking, testset, evidence

# 对应模块的 check(**参数)，输出 hit.trigger_key 或 None。
# 例如：
keep_pushing.check(response="要不要继续？")
keep_pushing.check(response="commit 已推到远程。")
s = SessionState("audit-memory")
s.record_bash("pytest", "1 failed")
bypass_pinrule.check(tool_name="Edit", tool_input={"file_path":"/repo/src/a.py"}, session_state=s)
```

实际结果（仅输出检查键，没有读取/改写上述虚拟路径）：

```text
keep-question => None
keep-declarative => check.keep_pushing.default.trigger
keep-user-stop => None
long-todo => check.long_term.todo_marker.trigger
long-doc-exempt => None
long-skip-verify => check.long_term.git_skip_verify.trigger
testset-copy => check.testset.cross_split_copy.trigger
evidence-stop => check.evidence.completion.trigger
non-block-retry => check.non_blocking.sleep.trigger
non-block-sleep => check.non_blocking.sleep.trigger
bypass-backup => None
bypass-edit-after-fail => check.deep_fix.edit_after_test_fail_no_read.trigger
FAIL_RE(0 failed) True
TEST_CMD mvn test False
TEST_CMD gradle test False
TEST_CMD dotnet test False
TEST_CMD swift test False
TEST_CMD pytest True
TEST_CMD go test True
10 passed, 0 failed => evidence False
```

其中 retry 的输入正是 `for i in 1 2 3; do curl -f localhost:8080 && break; sleep 1; done`；sleep 输入为 `sleep 300`。todo 测试的 new_string 为 `# TODO: repair`，源码路径与 docs 路径得到不同结果。bypass-backup 是 `cp ~/.pinrule/rules.json /tmp/backup`，只把命令作为字符串传给 check，没有执行复制。

## E2：transcript 实测摘要

执行方法：`python3 -B` 使用 `Path.home()/'.claude/projects'`，顶层 `glob('*/*.jsonl')` 计数；递归 `rglob('*.jsonl')` 另计。取顶层路径排序后的前 400 份，逐行 `json.loads`，通过 assistant 的 `tool_use.id → name` 关联 user 的 `tool_result.tool_use_id`；只输出 type/键集合/数字错误首行，不复制私人 prompt、命令正文或输出正文。

关键扫描逻辑如下，保证不是把所有工具的 toolUseResult 当作 Bash：

```python
for block in content:
    if block.get("type") == "tool_use":
        names[block.get("id")] = block.get("name")
    elif block.get("type") == "tool_result" and names.get(block.get("tool_use_id")) == "Bash":
        result = row.get("toolUseResult")
        # 对 dict 统计 sorted(result.keys())；对 is_error 只统计首行 Exit code N。
```

实际输出摘要：

```text
direct_files 1812
recursive_files 2430
subagent_jsonl_count 618
largest_bytes 40948983
sample_count 400
parse_errors 0
tool_results 16718
rows_all_8_fields 94571
bash_result_types {'dict': 13236, 'str': 346, 'NoneType': 7}
code_related_keys {'returnCodeInterpretation': 153}
interpretation_types {'returnCodeInterpretation_type=str': 153}
system_subtypes 包含 stop_hook_summary:708、compact_boundary:5、api_error:22
```

type 包含 attachment、assistant、user、last-prompt、queue-operation、system，还包括 mode、permission-mode、file-history-snapshot、atis-latch、cost-state、ai-title、custom-title、api-request 等。不是所有类型的行都携带 A17 全部元数据。

Bash 字典结果的键集合：

- 基础 `{interrupted,isImage,noOutputExpected,stderr,stdout}`：12,383 条。
- 另有 `persistedOutputPath/persistedOutputSize`：179 条。
- 另有字符串 `returnCodeInterpretation`：147 条；与 bashEditDiff 同时存在的另有 6 条。
- 另有 backgroundTaskId、backgroundCwdHint、timedOutAfterMs、bashEditDiff、staleReadFileStateHint、gitOperation 等组合。
- 上述字典扫描没有发现数值 exit_code/exitCode 字段；这只能证明样本，不能当稳定 hook schema。

失败 Bash `tool_result.is_error=true` 的首行统计：`Exit code 1` 283 次、2 为 12 次、5 为 2 次、137 为 1 次、127 为 3 次、128 为 14 次、28 为 1 次、35 为 3 次，另有 29 条不是 Exit code 首行。

脱敏定位样本：`<session-A>.jsonl:993` 与 `:1022` 为 `is_error:true / Exit code 1 / toolUseResult_type=str`，`:1405` 为 `Exit code 2`。这些行位于 `~/.claude/projects/*/` 的抽样集合。工具失败记录是 transcript 证据，不是本次抓到的 PostToolUseFailure stdin。

另抽样 10 个嵌套 subagent 文件，有 727 行包含 `agentId`、sessionId、isSidechain、parentUuid、cwd。

### A9 补充反例：非零进程退出不等于工具执行失败

进一步只读扫描发现 `returnCodeInterpretation` 的 153 条记录均为 `is_error=false`：`No matches found` 141 条、`Files differ` 11 条、`Some directories were inaccessible` 1 条。例如前两类分别见 `<session-A>.jsonl:2751`、`<session-B>.jsonl:8576`。

为确认这些语义确实对应非零码，而非仅依据标签猜测，本次还只读检查了当前 Claude Code 可执行文件中的嵌入 JavaScript：

```text
文件 ~/.local/share/claude/versions/2.1.300
SHA-256 475517caa611e3318a2f298fa071287ec98955538df78c58f709ecc01e5ae2d3
字节区间 [207757565,207758000)：
  cM=(e)=>(n,r,s)=>({isError:n>=2,message:n===1?e:void 0})
  Ets 中 grep/rg/egrep/fgrep、find、diff、test、[ 使用 cM
后续 $rt 调用 Rts(e)(n,r,s)；Bash 执行主体以 $rt(n.command,We.code,...) 判定 xe.isError。
```

复现命令方法：`python3 -B` 读取该二进制，用 `bytes.find(b'var F1n=(e,n,r)=>')` 与后续 `b';function Rts(e)'` 提取**纯返回码分类表达式**，在 `node -e` 中调用 `Ets.get(command)(1,"","")`。没有启动 Claude 会话、执行 grep/diff 工具调用或运行整个嵌入程序；只执行这段纯函数。实际退出码为 0，输出：

```json
{"command":"grep","exit_code":1,"isError":false,"message":"No matches found"}
{"command":"rg","exit_code":1,"isError":false,"message":"No matches found"}
{"command":"diff","exit_code":1,"isError":false,"message":"Files differ"}
{"command":"test","exit_code":1,"isError":false,"message":"Condition is false"}
```

D1 “PostToolUseFailure”将触发条件定义为已开始执行的工具抛错误或 MCP 返回错误结果，并没有承诺所有 shell 非零码均是工具错误。以上实现与 transcript 共同反驳 A9 的无条件表述。应改为“被 Claude Code 判为执行失败的 Bash 走 PostToolUseFailure；通常命令退出非零会失败，但 grep/diff 等语义码存在例外”。真实 hook stdin 仍待探针，verify 仍应自行获取原始进程退出码，不借助这层语义转换。

## E4：性能复测

执行：Python 标准库 `subprocess.run` + `time.perf_counter_ns()`；串行运行，各命令预热 5 次，再测 30 次；计时包含创建子进程至退出，stdout/stderr 指向 DEVNULL。p50 为中位数，p95 为排序后第 `ceil(0.95*n)` 个样本。当前机器同时承载本次审核，未控制 CPU 调度/后台负载，因此不作为发布性能验收。

| 命令 | p50 ms | p95 ms | max ms |
|---|---:|---:|---:|
| `/usr/bin/true` | 1.525 | 2.001 | 2.059 |
| `python3 -I -c pass` | 17.580 | 18.761 | 19.279 |
| `python3 -S -I -c pass` | 15.523 | 36.413 | 38.214 |
| `node -e 0` | 51.423 | 56.349 | 57.508 |
| `python3 -B -m pinrule.hooks.pre_tool_use`，cwd=/tmp/pinrule，stdin=`{` | 93.998 | 190.897 | 202.463 |

最后一行输入为损坏 JSON，只测导入与异常入口，不加载正常规则/写状态；退出码均为 0。**不能把它解释为 pinrule 正常 hook 的 100～180ms 基线。** 没有创建 Go hello 源码/二进制，Go 完整 hook 的性能仍无法核验。

## A 区核验汇总表

“不实”用于原句含有与证据冲突的断言，不表示该条所有部分均错误；“无法核验”说明关键部分证据不足。文档属实与本机 payload 已捕获是两个独立结论。

| 条目 | 结论 | 独立证据与边界 |
|---|---|---|
| A1 | **不实** | D1 “Common input fields”确有这些字段名，但 prompt_id 在首次输入前缺失；scratchpad_dir 可缺失；permission_mode 不是每个事件都有；effort 有模型/事件条件；agent 字段有作用域条件。不能将列表视为所有事件必带字段。 |
| A2 | **属实** | D1 “Common input fields”：transcript 异步写且可能滞后，应使用 Stop/SubagentStop 的 last_assistant_message。 |
| A3 | **属实** | D1 “SessionStart input”列 source 的 startup/resume/clear/compact/fork；matcher 对应启动来源。实际 compact payload 本次未抓取。 |
| A4 | **属实** | D1 “SessionStart decision control”列 additionalContext、initialUserMessage、sessionTitle、watchPaths、reloadSkills，允许纯 stdout 注入；“JSON output”说明每个字符串单独受 10,000 字符上限及最多 2,000 字符预览约束。计划的总注入 <10k 是更严格的产品选择。 |
| A5 | **属实** | D1 “UserPromptSubmit input / decision control”给 prompt、additionalContext、decision=block/reason；正文明确 command/http/mcp_tool 默认超时 30 秒。 |
| A6 | **属实** | D1 “PreToolUse input / decision control”确认工具字段、mcp_server、allow/deny/ask/defer、updatedInput、additionalContext、deny>defer>ask>allow 以及旧顶层 decision 废弃。需补充 allow 可能跳权限提示、ask 的非交互限制。 |
| A7 | **不实** | D1 “Exit code output”：合法控制 JSON 可在 exit 1 等非零时仍生效，故“其他退出码动作照常放行”“异常退 1 即放行”不成立。exit 2 可阻塞和 onFailure=block 的存在属实。见 R05。 |
| A8 | **不实** | E2 支持“所扫成功 Bash dict 未见结构化 exit code”，但“只有四/五个键”被额外键实证反驳。D1 “PostToolUse input”只保证结果依工具而异、duration_ms 可选，并未承诺该 Bash 穷举 schema。本次未抓真实 hook tool_response，不能将 toolUseResult 等同于它。 |
| A9 | **不实** | D1 将失败事件定义为工具执行错误，未承诺所有非零码都失败；E2 补证当前二进制的纯分类函数将 grep/rg/diff/test 的 exit 1 判为 isError=false，transcript 有对应非错误结果。因此“非 0 Bash 都走失败事件”不成立。普通执行失败 error 使用 Exit code N 首行、启动失败可能无此首行，获文档与 transcript 支持。**本机真实 hook stdin 路由仍无法核验**。 |
| A10 | **属实** | D1 “Stop input / decision control”确认各字段、block/reason、exit 2 和 8 次连续继续上限。补充计数在每次调用工具后重置，且不能以此替代产品自己的防循环语义。 |
| A11 | **属实** | D1 “PreCompact”确认 manual/auto、custom_instructions（无值时为 null）、exit 2 或 block 阻止压缩，systemMessage/continue 被丢弃；“Add context for Claude”未将 PreCompact 列为注入事件。SessionStart(compact)可恢复注入；“只能”不应排除其他后续支持注入的事件。 |
| A12 | **属实** | D1 command hook/async 章节支持 async、timeout、type=command；异步结果延后交付，不能用于同步拦截。必须确保 Ballast 的拦截 hook 安装为同步。 |
| A13 | **不实** | D3 “Where each mechanism stores the policy”与 E1 支持各 OS 路径、本机未部署及多种托管来源；但文档开头明确 managed 不能被低层覆盖 **apart from a few security-sensitive exceptions where a stricter value from a lower level still counts**。原句“任何值都不能覆盖”过于绝对；hook 合并也是专门语义。 |
| A14 | **属实** | 键与 lock 类型在 D3 得到确认；D1 配置章节现在明确低层 disableAllHooks **不能关闭 managed hook**。allowManagedHooksOnly 阻止普通 user/project/local/plugin hook，但组织 force-enabled 插件 hook 有例外，还限制部分 command source/statusLine 等。原“未核验”可就文档行为解除；实际部署探针仍未做。巡检只能提供检测，不能满足不可写硬约束。 |
| A15 | **属实** | 现已查明答案：D1 配置章节原文 `Hook entries merge across settings levels rather than replacing each other`，低层添加不会删除 managed hook。不需要再以未知事实保留。org hook 自己读取 org 规则是合理架构，但与重复安装、合并执行次数仍须一起设计。 |
| A16 | **属实** | D1 “ConfigChange”列出 policy_settings/user_settings/project_settings/local_settings/skills。它不监视任意 Ballast 文件；server-managed、MDM/注册表等刷新存在不触发例外，不能当完整防篡改监视器。 |
| A17 | **属实** | E2 实测逐行 JSON、所述 type、user.tool_result 与同级 toolUseResult、元数据字段存在。当前顶层 1812，递归含子 agent 共 2430；历史 1810 是时间快照，不据此判错。元数据并非每类行都必有，失败结果也可为字符串；没有稳定公开 schema，版本容错合理。 |
| A18 | **无法核验** | E1 确认环境，E4 复现 true/Python -I 的数量级，但未提供原测量脚本/原始样本，不能认证所有历史数字；本次 -S 与 Node 数字不同，不足以否定历史测量。Go 数字只是推断，没有实测。见 R14。 |
| A19 | **无法核验** | E3 证实恰好八个注册函数、测试结果正则、缺四类命令、0 failed 被判失败；`_tool_gate.py:126～158` 和 `rule.py:26～36` 支持规则命中仅有 deny、没有四档 action；`rule.py:49～59`、`config.py:67`、`paths.py:29～37` 支持默认单层加载、无 org/repo/user 合并。**正常 hook 的 100～180ms 未核验**，不能给五项整体属实结论。 |

对于原标【未核验】的条目：A9 的真实 payload 探针合理，但须覆盖普通失败、grep/diff 语义非零、取消及启动失败分支，并先修正无条件断言；A14/A15 的文档未知已经解决，巡检备选只能检测、不能成为不可写的替代实现；A19 的档位与层级已从源码补证，性能仍需要可重复的正常路径基准。M0 的托管实验隔离方式须按 R15 修订。

## 硬约束覆盖与总体判断

| 原始硬约束 | 审核结果 |
|---|---|
| ≤12 条，声明式新增拦截 | 数量目标有写，超限裁决不明确；DSL 自身缺项且八检查范围不一致。R03/R07/R09。 |
| deny/ask/warn/log，新规则默认 log | 默认值明确，但逐事件协议不完整，Stop warn 与“放行”冲突；默认 log 和 deny 回归需分离。R06/R08/R16。 |
| verify 退出码，不解析输出 | 方向正确，0 failed 问题有实证；freshness、命令集合、CLI 失败退出协议和 replay 仍未闭环。R02/R12/R16。 |
| org>repo>user；Agent 不可写规则/状态 | 层级写明，合并存在歧义；不可写未形成真实权限边界。R01/R09。 |
| 热路径无 LLM、无联网；异常放行并写 stderr | 任意 exec 可以破坏前两项；非零退出码不足以保证异常放行。R04/R05。 |
| hook p95≤30ms | 空进程基线部分复现，完整实现未验证；CI 门槛实际 36ms，不能接受。R14。 |
| 不把个人偏好写进默认规则 | chinese_plain/testset 排除默认合理；stop-keywords/“可选长期”仍要明确安装默认。pinrule 的持续推进是一项工作方式偏好，不应因来自参照实现就自动进入默认注入包；建议显式 opt-in，并在八检查迁移表中标注。R07/R17。 |
| install/uninstall 外科式、先备份 | 有原则与基本往返测试，归属键未经核验，并发修改/幂等/原文件不存在等验收缺失。R16/R19。 |
| M1～M4 各有验收命令 | 表面已列命令，依赖顺序、占位符、退出码与断言不足；尚非完整机器验收。R16。 |

进入实现前必须解决 **R01～R05 五个 blocker**：确定可兑现的安全边界；以受测内容而非当前会话时间定义证据；冻结能运行实际规则的 DSL；排除任意同步 exec 对热路径的破坏；修正异常输出/退出码协议。随后应同步定稿 action 事件矩阵、并发与子 agent 状态、合并规则和可执行验收，以免在 M2～M4 才返工基础架构。Go 可以保留为推荐候选，但本次没有足够证据把“Go 已达标、Python 基本不可达”当成已核验事实。
