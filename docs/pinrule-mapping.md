# pinrule 8 项检查到 Ballast 规则 DSL 的映射

> 状态:随单元 U11 交付(计划 v0.4 B1 "pinrule 8 检查迁移"、R07)。内容文件:`rules/default.json`、`rules/optional/*.json`、`examples/*.json`。
>
> 参照项目:pinrule 0.19.0(`pyproject.toml:3`)。下文引用的路径相对于 pinrule 源码树:`pinrule/...` 指 Python 包,`data/signals/...` 指词表。行号以该版本为准,写作 `文件:起-止`。
>
> 登记口径(每个条目恰好落入一类):
>
> - **DSL 等价**:同样的输入得到同样的判定(允许实现手段不同,例如 AST 代替引号剥离)。
> - **有意改变**:Ballast 有意给出不同的判定,理由写在条目里(多为更严、更准,或与计划 B1/B3 的语义对齐)。
> - **舍弃**:不迁移;理由是 DSL 的封闭词表无法表达、假阳率高,或已被其他机制取代。

## 1. 总览

| # | pinrule 检查(源码) | 去向 | 文件 |
|---|---|---|---|
| 1 | `read_first`(`pinrule/checks/read_first.py`) | 默认 `read-before-edit` | `rules/default.json` |
| 2 | `non_blocking`(`pinrule/checks/non_blocking.py`) | 默认 `no-long-sleep`;可选 `no-foreground-long-task`、`no-foreground-install`、`no-foreground-docker`、`no-shell-wait`、`no-lang-sleep` | `rules/default.json`、`rules/optional/no-foreground-long-task.json`、`rules/optional/no-shell-wait.json` |
| 3 | `evidence`(`pinrule/checks/evidence.py`) | 默认 `evidence-before-commit`;可选 `claim-needs-evidence` | `rules/default.json`、`rules/optional/claim-needs-evidence.json` |
| 4 | `bypass_pinrule`(`pinrule/checks/bypass_pinrule.py`) | 默认 `no-tamper`;可选 `read-after-test-fail` | `rules/default.json`、`rules/optional/read-after-test-fail.json` |
| 5 | `keep_pushing`(`pinrule/checks/keep_pushing.py`) | 可选 `keep-pushing` | `rules/optional/keep-pushing.json` |
| 6 | `long_term`(`pinrule/checks/long_term.py`) | 默认 `no-skip-verify`;可选 `no-git-force-skip`、`no-hack-commit-message`、`no-todo-marker`、`no-hardcoded-list`、`no-patch-intent` | `rules/default.json`、`rules/optional/long-term.json` |
| 7 | `testset`(`pinrule/checks/testset.py`) | 示例 `no-testset-leakage`、`no-testset-cross-copy`、`no-testset-detail-append` | `examples/testset.json` |
| 8 | `chinese_plain`(`pinrule/checks/chinese_plain.py`) | 示例 `chinese-plain-ratio`、`chinese-plain-jargon` | `examples/chinese_plain.json` |

8 项检查全部有 DSL 改写版本。默认包恰好 5 条(`read-before-edit`、`no-long-sleep`、`evidence-before-commit`、`no-skip-verify`、`no-tamper`),均为影子模式(`action:"log"`,`target:"deny"`,计划 B2),凡涉及 shell 的都同时带 `bash` 与 `ps` 变体。可选规则同样默认 `log`。默认包的 `inject` 文本合计 330 字(上限 8000)。

### 1.1 全局性差异(适用于每一项)

| 主题 | pinrule | Ballast | 类别 |
|---|---|---|---|
| 命令文本处理 | 先用正则剥掉引号字面、heredoc、`$()` 提升等再扫描(`pinrule/checks/common.py:87-156`) | `mvdan.cc/sh` AST 展开为命令节点:`echo "sleep 300"`、注释、heredoc 正文根本不是节点,不需要剥离 | DSL 等价 |
| 宿主语言 `-c` 豁免 | `python/node/ruby/perl -c\|-e` 内的字面不当作 shell(`common.py:48-64`,`is_python_c_command`) | 节点的命令名是 `python`,`-c` 后的字符串只是参数,不会被当作 `sleep` 等命令;需要检查时用 `args_regex` 显式匹配(见 `no-lang-sleep`) | DSL 等价 |
| 描述上下文豁免 | 文档、数据/配置、测试目录、测试文件名、`/tmp`、探针名下的写入整体豁免(`pinrule/checks/description_context.py:26-86`) | `match.path` 的列表级取反 glob(`rules/optional/long-term.json` 中 `no-todo-marker` 的 `path` 取反项,逐项对应 `description_context.py:57-80`;`examples/testset.json` 用同一组) | DSL 等价 |
| 正则方言 | Python `re`,含反向引用与前瞻 | 统一 RE2;`bad-regex` 在 lint 阶段拒绝。含前瞻/反向引用的模式已改写(见 §3.6) | 有意改变 |
| 异常与无法判定 | 检查函数抛异常时静默吞掉(`pinrule/checks/__init__.py:77-85`) | 三值逻辑:无法判定为 unknown,规则不命中,并累加 unknown 诊断计数与审计 | DSL 等价(更可观测) |
| 命中后的动作 | 检查命中即拦截/反思 | 新规则默认 `log`,命中记 `would_<target>`,`ballast stats` 与 `rules promote` 决定升级(B2) | 有意改变 |
| 状态作用域 | 单个 session 的集合 | `(repo_inst, session, agent)`;主 agent 与子 agent 互不继承(B6) | 有意改变(更严) |
| 默认包文案语言 | pinrule 按 locale 选择中英文词表与提示(`tests/test_i18n.py`、`tests/test_locale_detect.py`) | 默认包与可选规则的 `message`、`inject` 为中文,规则匹配用的词表同时覆盖中英文。这是面向中文使用者的有意取舍(README、错误提示同为中文);默认包需要用户复制到自己的规则文件后才生效,`message` / `inject` 只是文本、不参与匹配,换成其他语言只需改自己的副本(README "默认 5 条规则"一节说明)。是否改为英文或双语由仓库所有者在公开前裁决 | 有意改变 |
| 关键词层 | `rules.json` 的 `violation_keywords`(`data/rules.dev.example.json`)与检查函数并行 | 不迁移;不在 8 项检查范围内 | 舍弃 |

## 2. 逐项登记

### 2.1 read_first → `read-before-edit`

| pinrule 行为(源码) | Ballast | 类别 |
|---|---|---|
| 只检查 `Edit`、`Write`(`read_first.py:21-22`) | `tool: [Edit, MultiEdit, Write, NotebookEdit]`;Codex 的 `apply_patch` 归一为 `Edit` | 有意改变(范围扩大) |
| 没有 session 状态就放行(`read_first.py:23-24`) | `read_this_session` 在状态不可读时为 unknown,规则不命中 | DSL 等价 |
| 多文件补丁:任一 `Update` 目标未读即拦(`read_first.py:27-54`,其中 Update `:43-53`) | `apply_patch` 的每个目标形成一个绑定,`when` 逐绑定求值,任一存在且未读的目标命中 | DSL 等价 |
| 补丁里的 `Add`(新建)豁免(`read_first.py:37-39`) | `path_exists` 为 false 时不命中 | DSL 等价 |
| 补丁里的 `Delete` 豁免(`read_first.py:40-42`) | Delete 也是目标(dsl-semantics §5.2);已存在且未读的文件被删除会命中 | 有意改变(更严) |
| 没有 `file_path` 就放行(`read_first.py:56-58`) | 没有目标、也没有 `path` 时只有空绑定,`path_exists` 绑定不到目标为 unknown | DSL 等价 |
| 只有 `Write` 新建文件豁免,`Path.exists` 抛 OSError 时不豁免(`read_first.py:60-66`) | `path_exists` 对所有工具生效;Lstat 返回 ENOENT/ENOTDIR 为 false,其他错误为 unknown(放行并审计) | 有意改变 |
| 路径比较用 `abspath(expanduser(...))`(`pinrule/session_state.py:65-77`、`:207-208`) | `ports.PathService.Candidates`:`~` 展开、`.`/`..` 消解、Git Bash 与 UNC 路径规范化、EvalSymlinks、按平台折叠大小写 | 有意改变(更准) |
| 工具调用失败不登记已读(`pinrule/hooks/post_tool_use.py:40-55`、`:167-172`) | Claude Code 的非 0 失败走 PostToolUseFailure,不登记;被拦截的调用不产生 Post* 事件 | DSL 等价 |
| `Write`/`Edit`/补丁的 `Update`、`Add` 成功后视为已读,`Delete` 不登记(`post_tool_use.py:173-204`,`:196-197`) | `read_this_session` 的真值定义包含"Edit/Write 等成功写入后的目标"(dsl-semantics §7) | DSL 等价 |
| Codex 无 Read 工具:shell 读命令的路径由 `extract_read_write_paths_from_exec_command` 提取(`pinrule/backends/codex.py:576-612`),在 PostToolUse 登记(`post_tool_use.py:125-138`) | `CommandNode.Reads`(`cat/head/tail/sed -n/grep/rg/awk/wc/nl/< 文件/Get-Content`…),由唯一纯函数 `model.ShellReadPaths` 提取并在 PostToolUse 并入已读集合;覆盖面更宽,不识别的命令只会让规则更严 | DSL 等价(经 `CommandNode.Reads`,覆盖面更宽) |
| 已读集合整 session 共享 | 按 agent 隔离,子 agent 不继承主 agent 的已读 | 有意改变(更严) |
| 无 | message 与 inject 明确提到只读 shell 命令(Codex 无 Read 工具) | 有意改变(提示文案) |

### 2.2 non_blocking → `no-long-sleep` 与可选的长任务、wait 规则

| pinrule 行为(源码) | Ballast | 类别 |
|---|---|---|
| 任何 `sleep N`(N ≥ 1)都拦(`non_blocking.py:20-21`、`:133-141`) | 只拦单个节点的前台 sleep ≥ 60 秒(`sleep_s: {gte: 60}`),支持 `300`、`5m`、`1h`、`1d` 与多参数求和;循环内每次 < 60 秒的 sleep 放行(计划 B1 回归第 12 例) | 有意改变 |
| 先剥引号字面再扫描,避免 `echo "sleep 5"` 误拦(`non_blocking.py:108`、`common.py:87-156`) | AST:引号文本、注释、heredoc 不是节点 | DSL 等价 |
| `sleep` 不看 `run_in_background`,只有长任务看(`non_blocking.py:101`、`:156`) | `no-long-sleep` 的 `run_in_background:false` 与 `async:false`:工具级后台和 shell 的 `&` 都放行 | 有意改变 |
| 整条命令里出现 `&` 就放行长任务(`non_blocking.py:156`,`"&" not in cmd`) | `async` 是 AST 级别,只对带 `&` 的那条语句生效;`a & b` 中的 `b` 仍然命中 | 有意改变(更准) |
| 只检查 `Bash`(`non_blocking.py:95-96`) | 每条涉及 shell 的规则同时带 `bash` 与 `ps` 变体(`Start-Sleep -Seconds N`、`Start-Sleep -Milliseconds N`、别名 `sleep`;B11) | 有意改变(新增 PowerShell) |
| `block_until_ms` 作为阻塞代理指标已被作者删除(`non_blocking.py:102-106`) | 不使用 | 舍弃(沿用原作者的结论) |
| 命令头是宿主语言 `-c` 时跳过 sleep 检测(`non_blocking.py:110-114`、`:134`) | 命令名必须是 `sleep`/`start-sleep` 才是节点 | DSL 等价 |
| `python -c` 内的真阻塞调用:`time.sleep(N)`、`asyncio.sleep(N)`、`subprocess.run(...'sleep N')`、`os.system(...'sleep N')`(`non_blocking.py:28-34`、`:116-131`) | 可选 `no-lang-sleep`:对 `python*` 命令的参数字面做 RE2 匹配;阈值同 60 秒 | 有意改变(阈值;仅 python) |
| `node`/`ruby`/`perl -e` 内的阻塞调用 | 不识别(原实现只匹配 Python 形态,`non_blocking.py:28-34`) | DSL 等价(原实现同样没有) |
| 裸 `wait`、`wait $pid`、`wait $!`(`non_blocking.py:43-70`) | 可选 `no-shell-wait`:`bash.cmd: wait`;`Wait-Job`/`Wait-Process` 为新增 PowerShell 变体 | DSL 等价(PowerShell 为新增) |
| `kubectl/docker/podman/aws/gcloud/az/helm` 的 `wait` 子命令豁免(`non_blocking.py:40`、`:62-66`) | 命令名不是 `wait`,天然不命中,不需要豁免表 | DSL 等价 |
| 长任务命令头:`docker run`、`docker compose up\|run\|build`、`docker build`、`cargo build\|install`、`npm install\|ci`、`yarn install`、`pnpm install`、`pip install`、`make install\|build\|all\|release\|deploy`、`gradle(w) build`、`mvn install\|package\|deploy`(`non_blocking.py:76-86`) | 拆成 `no-foreground-long-task`(cargo/make/mvn/gradle)、`no-foreground-install`(npm/yarn/pnpm/pip/pip3)、`no-foreground-pip-module`(`python* -m pip install`、`py -m pip install`、`uv pip install`)、`no-foreground-docker`(docker),拆分是为了让命令头与子命令不产生有害的叉积(例如 `yarn build` 不应命中)。`args_regex` 允许子命令前出现 `-x` 形式的全局选项。原实现对整条命令做子串匹配,所以 `pip install` 出现在任意位置(如 `echo "pip install x"`、`git commit -m "pip install 修复"` 的字符串参数里)也会命中;DSL 版只看命令节点,字符串参数里的不命中(`bash -c "pip install x"` 会被递归解析出 pip 节点,两边都命中) | DSL 等价(命令头清单一致;子串匹配改为节点匹配,属有意收窄) |
| `npm run`、`yarn build` 等用户脚本不拦(`non_blocking.py:87-89`) | 同样不拦(`no-foreground-install` 的示例覆盖) | DSL 等价 |
| 子命令前的全局选项会使 `\bcargo\s+build` 失配 | `args_regex` 的前缀 `(?:-\S+\s+)*` 容忍 `cargo --locked build`;带值的全局选项(如 `make -C dir install`)仍不识别 | 有意改变(略宽) |
| `docker run -d`、`compose up -d` 也拦 | 同样拦(命令头匹配,不区分是否分离运行),已写进规则描述 | DSL 等价 |
| 动态子命令 | `make $T` 这类 argv 含动态词且正则不匹配时为 unknown(放行并审计) | 有意改变 |

### 2.3 evidence → `evidence-before-commit` 与可选的 `claim-needs-evidence`

| pinrule 行为(源码) | Ballast | 类别 |
|---|---|---|
| `git commit` 前要求"最近有测试通过"(`evidence.py:83-85`);证据来自对 Agent 自己跑的 Bash 输出做正则(`pinrule/session_state.py:210-218`) | `verify_fresh`:`ballast verify` 的 required 记录必须与待提交内容(C)一致(B3,内容快照 S/C);verify 进行中、不在仓库、参数无法解析均为 unknown | 有意改变 |
| conventional commit 的 `docs/chore/style/build/ci/test/refactor` 前缀视为非代码提交,豁免(`evidence.py:37-44`、`:87-88`) | 改由 `project.json` 的 `exempt` 快照过滤实现(按文件而不是按提交说明) | 有意改变 |
| 同一条命令里先跑过测试(`pytest && git commit`)即豁免(`evidence.py:45-52`、`:89-96`) | 证据必须走 verify,不再从命令链推断 | 舍弃 |
| `--collect-only`、`--help` 等假证据标志不算(`evidence.py:53-59`、`:95`) | 证据不再来自 Agent 自己的命令,该问题不复存在 | 舍弃(随上一条) |
| commit 前链 `git add && git commit` | PreToolUse 时 add 尚未执行,C 取当前索引;message 提示"`git add` 与 `git commit` 分两次执行"(dsl-semantics §14) | 有意改变 |
| 回复侧:完成词(`evidence.py:19-22`,`data/signals/completion_words/{zh,en}.txt`)与弱声明词(`evidence.py:24-26`,`data/signals/weak_claims/{zh,en}.txt`)出现在"代码任务词"(`evidence.py:29-33`)前后 40 字内,且无近期测试证据时命中(`evidence.py:34`、`:62-67`、`:105-132`) | `claim-needs-evidence`:Stop 事件,`text` 的四条正则(任务词→完成词、完成词→任务词、任务词→弱声明、弱声明→任务词,各自 `[^\n]{0,40}`)加 `verify_fresh` | DSL 等价(词表为原词表的子集:有意改变)。第五条正则单独匹配自带任务语境的完成声明(`tests passed`、`build green`、`测试通过`、`构建通过`):这些完成词本身含任务词,前四条要求两段互不重叠的文本,单独出现时永远不命中,而原实现的 40 字窗口包含匹配本身,所以会命中 |
| 回复正文不做预处理 | `strip_code_blocks`、`strip_quotes`,取末尾 600 字(与 `schema/examples/05-claim-needs-evidence.json` 一致) | 有意改变(更准) |
| 回复中明确说"没完成"不算 | `not_regex` 有序豁免:`not done`、`未完成`、`还没`、`尚未` 等 | 有意改变(新增) |
| 命中后只拦一次(`evidence.py:120`、`:132` 的 `break`) | 一次求值一个结果,不适用 | DSL 等价 |

### 2.4 bypass_pinrule → `no-tamper` 与可选的 `read-after-test-fail`

| pinrule 行为(源码) | Ballast | 类别 |
|---|---|---|
| 命令含 pinrule 状态文件路径(`~/.pinrule/{session-state,violations.jsonl,rules.json,config.json}`,`bypass_pinrule.py:42-77`)并写入它 | `no-tamper`:`path` 列表含 `**/.ballast/**`、`~/.ballast/**`(以及各平台托管目录下的 org 规则目录 `ballast/**`),对 Bash/PowerShell 取解析出的写入目标 | DSL 等价 |
| 命令含内部字段名(`last_test_pass_ts`、`pending_bg_tasks` 等,`bypass_pinrule.py:33-39`) | 不匹配字段名:Ballast 的状态与证据都在被保护路径下,任何写入都落在路径上 | 舍弃 |
| 写操作信号不含 `cp/mv/rm`,避免拦合法备份和轮转(`bypass_pinrule.py:79-84`) | `cp`、`mv`、`install`、`rsync`、`rm`、`truncate`、`touch`、`sed -i`、`dd of=`、`tee`、重定向都产生写入目标,因而命中;合法维护走 `ballast` CLI,它在进程内写文件,不是 shell 写入目标 | 有意改变(更严) |
| `mv` 把状态文件搬走(`mv ~/.pinrule/rules.json /tmp/x`)不在 pinrule 的写操作信号里,pinrule 同样放行 | `mv` 的**目标**是写入目标;**源路径**不算(bashparse 契约:`cp/mv/install SRC.. DEST` 只取 DEST),所以 `mv ~/.ballast/rules.json /tmp/x` 与 `mv ~/.ballast /tmp/b` 不命中 `no-tamper`;`rm`、`cp` 到被保护路径、重定向、`sed -i` 等都命中。T0 本来就只检测、不是安全边界(T1 的 `denyWrite` 才能拦住) | 有意改变(已知局限) |
| 重定向写入排除 `/dev/null` 等丢弃路径(`bypass_pinrule.py:107-111`) | bashparse 的 `Writes` 同样排除 `/dev/null` | DSL 等价 |
| `python -c` 内的 `>` 是比较运算符,跳过重定向检测(`bypass_pinrule.py:113-114`、`:168`、`:178`) | AST 不会把字符串里的 `>` 当重定向 | DSL 等价 |
| Python 写接口:`.write_text`、`.write`、`json.dump`、`os.system`、`shutil`…(`bypass_pinrule.py:90-105`) | 不识别语言内写入(dsl-semantics §14);T0 本来就不是安全边界,T1 由沙箱 `denyWrite` 兜底 | 舍弃 |
| 官方 CLI(`pinrule init\|reset\|stats…`)命令头豁免(`bypass_pinrule.py:117`、`:155-157`) | 不需要:`ballast verify`、`ballast rules promote` 等不会产生 shell 写入目标。计划中的"只放行 ballast 绝对路径"也因此不必实现 | 有意改变 |
| `PINRULE_HOME` 重定向时把实际路径加入检测(`bypass_pinrule.py:42-73`) | `BALLAST_HOME` 仅用于测试;规则写死 `~/.ballast/**`,自定义 `BALLAST_HOME` 不在覆盖范围 | 有意改变(已知局限) |
| 保护客户端 hook 配置 | 新增:`~/.claude/settings*.json`、`**/.claude/settings*.json`、`~/.codex/{hooks.json,config.toml}`、`~/.cursor/hooks.json`、`~/.cursor/rules/ballast.mdc`、`~/.hermes/config.yaml`,以及 org 规则目录(三个平台默认托管目录下的 `ballast/**`:`/etc/claude-code`、`/Library/Application Support/ClaudeCode`、`c:/Program Files/ClaudeCode`)(原实现没有) | 有意改变(新增) |
| 第二路:最近一条 Bash 是测试命令且失败(`recent_bash[-1].is_test_cmd and output_failed`),又 `Edit` 了没读过的文件(`bypass_pinrule.py:129-147`) | `read-after-test-fail`:`verify_failed_since_read` 加 `path_exists`;信号来自 verify 记录(outcome 为 fail/timeout/signal)而不是对 Bash 输出的猜测 | 有意改变 |
| 第二路只检查 `Edit`(`bypass_pinrule.py:131`) | 同时覆盖 MultiEdit、Write(已存在)与 NotebookEdit | 有意改变 |

### 2.5 keep_pushing → `keep-pushing`

原检查分四段:豁免(`keep_pushing.py:101-144`)、停顿语命中(`:148-157`)、成功汇报豁免(`:162-163`)、默认命中(`:171-180`)。

| pinrule 行为(源码) | Ballast | 类别 |
|---|---|---|
| 用户上一句含明确叫停语,整条豁免(`keep_pushing.py:98-102`;词表 `data/signals/user_stop_hints/zh.txt:7-44`、`en.txt:5-25`、`:53-77`) | `when: not prev_prompt_regex`;只看上一条 prompt 的末尾 200 字,词表取"累了/推卸"与"满意/确认"两类的子集 | 有意改变 |
| 词表第三类"协作等候/暂停"(`user_stop_hints/zh.txt:46-66`、`en.txt:27-51`) | 不迁移:"等一下""hold on"这类词太泛,会让规则大面积失效 | 舍弃 |
| 只看回复末尾 80 字(`keep_pushing.py:68`、`:105`) | `window: {tail_chars: 80}` | DSL 等价 |
| Agent 自己声明饱和或卡点,整条豁免(`keep_pushing.py:107-112`;词表 `data/signals/agent_saturation/{zh,en}.txt`) | `not_regex` 中的饱和/卡点/接力词(子集);只看末尾 80 字窗口,原实现看整条回复 | 有意改变 |
| 推进信号在末尾 80 字里,且其后不是"吧/行不/算了"(`keep_pushing.py:65`、`:114-121`;词表 `data/signals/push_signals/{zh,en}.py`) | `not_regex` 中的"主语+动词"与短语(子集),中文正则末尾加 `(?:[^吧行算]\|$)` 近似"推卸语气"过滤 | DSL 等价(近似) |
| 整段含推进规划且末尾没有停顿语,豁免(`keep_pushing.py:123-134`) | 无需:Ballast 只在末尾出现停顿语时才命中,该分支只影响"默认命中" | 舍弃(随默认命中) |
| 末尾有问号,豁免(`keep_pushing.py:136-138`) | `not_regex: ["[?？]"]` | DSL 等价 |
| 显式交还决定,豁免(`keep_pushing.py:140-144`;词表 `data/signals/explicit_handoff/{zh,en}.txt`) | `not_regex` 中的"请决定/等你确认/please decide"等 | DSL 等价 |
| 末尾出现停顿语即命中(`keep_pushing.py:146-157`;词表 `data/signals/stop_hints/{zh,en}.txt`) | `regex`:停顿语词表(中文与英文各一条,英文放宽弯引号) | DSL 等价 |
| 末尾含"232/232 通过"之类成功汇报,豁免(仅对默认命中,`keep_pushing.py:30-41`、`:159-163`) | 无需:成功汇报只豁免默认命中 | 舍弃(随默认命中) |
| 既无推进、无问号、无停顿语的纯陈述,默认命中(`keep_pushing.py:165-180`) | 不迁移。原作者在 `keep_pushing.py:165-169` 自己记录了短确认("OK""Done")与短汇报无法区分的假阳,未解决;DSL 的 `text` 无法比 80 字窗口更精确地判断"有没有下一步" | 舍弃 |
| 命中后 Stop 返回 block 让会话继续(`pinrule/hooks/stop.py:174-186`) | `target: deny`,Claude Code 上映射为 `decision:"block"`(连续 8 次上限,M0a A10);Cursor 为 `followup_message`(`pinrule/backends/cursor.py:168-178`),lint 报 `cursor-stop-followup` 警告 | DSL 等价 |

### 2.6 long_term → 默认 `no-skip-verify` 与可选 `long-term.json`

| pinrule 行为(源码) | Ballast | 类别 |
|---|---|---|
| `git commit` 带 `--no-verify`(`long_term.py:70-74`) | 默认 `no-skip-verify`:`git_sub: commit`,`flags: [--no-verify, -n]`;`git -C ../x commit` 等全局参数被跳过 | DSL 等价 |
| (原实现没有)`git -c core.hooksPath=<路径> commit`、`git --config-env=core.hooksPath=<变量> commit` 把 hooks 目录指到别处,与 `--no-verify` 效果相同 | **不拦,登记为已知局限**。DSL 的同一个 `bash` 匹配器里 `flags` 与 `args_regex` 是 AND,没有"或":把 `-c core.hooksPath=…` 并进 `no-skip-verify` 只能改用 `args_regex` 匹配 `--no-verify` / `-n`,而 `args_regex` 看到的是空格拼接后的字面值,`git commit -m "别用 -n 跳过"` 这类提交说明会误报;另开一条规则则使默认包超过 5 条,并使"同时生效不超过 12 条"的预算(bench 的 12 条场景)失效。该绕过仍受 `evidence-before-commit` 约束:提交前必须有与待提交内容一致的 `ballast verify` 记录,与 git hooks 是否运行无关;服务端 CI 才是最终裁判。`no-skip-verify` 的 `examples` 里有一个 `miss` 示例钉住这一行为 | 有意改变(已知局限) |
| (原实现没有)`-n` 与合并短选项如 `-anm` | `no-skip-verify` 新增 | 有意改变(新增) |
| `git push\|merge\|rebase\|tag` 带 `--no-verify`、`--force`、`--skip-hooks`(`long_term.py:70-74`) | 可选 `no-git-force-skip`;`--force` 是精确标志匹配,`--force-with-lease` 不命中 | DSL 等价 |
| `--force(?!-with-lease)` 前瞻(`long_term.py:72`) | 标志精确匹配代替前瞻 | 有意改变(RE2) |
| commit 说明标题行(前 80 字)含 `quick fix/hack/temp/workaround/临时/凑数`(`long_term.py:58-64`) | 可选 `no-hack-commit-message`:`args_regex` 匹配 `-m`、`-am` 之类合并短选项、`-m"值"` 紧贴写法与 `--message`(含 `=`)之后 80 字内;不要求位于引号内或第一行;bash 与 ps 变体使用同一个正则(外部可执行文件 git 的参数在 PowerShell 下同样保留在 argv 中) | 有意改变 |
| TODO/FIXME/HACK/XXX 注释(`long_term.py:83`)、打补丁意图注释(`long_term.py:88-95`) | 可选 `no-todo-marker`:`input_regex.new_text`(Edit/MultiEdit/Write/NotebookEdit 以及 `apply_patch` 的新增行)。原 `临时\b` 在 Unicode 语义下对「临时」后紧跟汉字不命中(`// 临时文件路径`),RE2 的 `\b` 是 ASCII 语义,这里改写为 `临时(?:[^\p{L}\p{N}_]\|$)` 保持同样行为 | DSL 等价(RE2 改写) |
| 长 ID 字面写在 `if` 分支,要求 ≥ 12 字符且含数字(`long_term.py:25-32`,用到前瞻 `(?=...)` 与反向引用 `\1`) | 可选 `no-hardcoded-list`:RE2 改写,按数字位置展开成 12 个分支 | 有意改变(RE2 改写,语义等价) |
| 带黑白名单语义的变量名赋字面量列表(`long_term.py:33-42`) | `no-hardcoded-list` | DSL 等价 |
| 全大写常量名加 5 个以上字符串的列表(`long_term.py:43-52`) | `no-hardcoded-list` | DSL 等价 |
| 回复侧话术:第一人称加短期动作、"知道不是长期方案但先这样"及英文对偶(`long_term.py:106-166`) | 可选 `no-patch-intent`(Stop);`\b` 紧跟中文的写法在 RE2 中永不成立,已去掉 | 有意改变(轻微) |
| 否定词在命中前 30 字内则豁免(`long_term.py:173-178`、`:218-221`) | `not_regex`:否定词后 40 字内出现补丁词的整句即豁免,不逐命中判断 | 有意改变(近似) |
| 剥掉围栏与行内代码后再扫描回复(`long_term.py:180-194`) | `preprocess: [strip_code_blocks]` | DSL 等价 |
| 对描述上下文整段豁免(`long_term.py:233-236`) | `path` 列表级取反 glob | DSL 等价 |
| 对 `Bash` 命令文本也扫描 `_PATTERNS_ALL`(`long_term.py:231`、`:241-246`) | `input_regex.new_text` 只覆盖文件编辑工具;shell 里的文本不扫描 | 舍弃 |
| pinrule 自身检测器路径视为自指豁免(`description_context.py:33`、`:82-84`) | 不适用 | 舍弃 |

### 2.7 testset → `examples/testset.json`

| pinrule 行为(源码) | Ballast | 类别 |
|---|---|---|
| 反喂:`gold_cases/gold_data/eval_cases/test_cases` 的 `append/extend/update/write*`(`testset.py:33`) | `no-testset-leakage` | DSL 等价 |
| 评测明细回流:`detail*.json` 后 80 字内写文件(`testset.py:38`) | `no-testset-leakage` | DSL 等价 |
| `cp ... eval\|test\|gold ... train\|fit\|memory`(`testset.py:43`) | `no-testset-cross-copy`:`cp/mv/rsync`(PowerShell 为 `copy-item/move-item`)的 `args_regex`;原实现只覆盖 `cp` | 有意改变(范围略宽) |
| `cat ... detail*.json ... >>`(`testset.py:48`) | `no-testset-detail-append`:`bash.cmd: cat` 加 `args_regex` 和 `writes`;`>` 与 `>>` 都算写入 | 有意改变(范围略宽) |
| `if turn_idx >= 400` 一类切分边界硬编码(`testset.py:53`) | `no-testset-leakage` | DSL 等价 |
| 长 hash 作为分支条件或赋给 `case_id`(`testset.py:60-64`) | `no-testset-leakage` | DSL 等价 |
| 变量名带测试集语义的列表里含长 hex(`testset.py:71-74`) | `no-testset-leakage` | DSL 等价 |
| `cp eval/cache/*.json train/cache/` 这类带通配符的命令同样命中(`testset.py:43`) | 未展开的通配符是动态词,`args_regex` 不匹配时结果为 unknown(规则不命中,记 `bash:dynamic_args` 诊断);写成具体文件名则命中。其他带 `args_regex` 的规则同理 | 有意改变(三值逻辑) |
| 对描述上下文(文档、数据、测试目录、探针)豁免(`testset.py:84-86`) | `path` 列表级取反 glob | DSL 等价 |
| `python -c` 命令头豁免(`testset.py:91-97`) | 内容规则只扫描文件编辑工具的新内容,不涉及 Bash | DSL 等价 |
| 对 Bash 命令文本扫描上述 1、2、5、6、7 号模式(`testset.py:82`、`:88-90`) | 不扫描 shell 命令文本 | 舍弃 |
| 领域特定,不进默认包 | 放在 `examples/` | 有意改变(归类) |

### 2.8 chinese_plain → `examples/chinese_plain.json`

| pinrule 行为(源码) | Ballast | 类别 |
|---|---|---|
| 整条回复为空,或去掉围栏代码后为空,放行(`chinese_plain.py:119-125`) | `strip_code_blocks`;`cjk_ratio` 的分母为 0 时为 false | DSL 等价 |
| 中文占比 < 40%、可见字符 > 50、英文词 ≥ 8 才触发(`chinese_plain.py:108-111`、`:154-167`) | `chinese-plain-ratio`:`cjk_ratio < 0.4` 且 `count_regex`(英文词)≥ 8。比例口径是"CJK/(CJK+拉丁字母)",原实现分母是全部可见字符 | 有意改变 |
| 算比例前先剥 URL、表格行、版本号、emoji、标识符、路径、commit message(`chinese_plain.py:29-86`、`:138-152`) | 只剥代码块 | 舍弃(口径更宽松) |
| 英文术语表(`chinese_plain.py:93-106`,32 个词条),术语后 12 字内有括号且括号内 ≥ 2 个汉字视为已解释(`:191-204`) | `chinese-plain-jargon`:术语表全部保留;"紧跟全角或半角左括号"即视为已解释 | 有意改变(简化) |
| 术语处于未闭合括号内豁免(`chinese_plain.py:181-189`);表格行里术语 < 3 个豁免(`:172-177`) | 不迁移 | 舍弃 |
| 总是启用 | `examples/chinese_plain.json` 总是启用;`schema/examples/06-chinese-plain.json` 是"上一条 prompt 含汉字才启用"的门控版本 | DSL 等价 |
| 个人偏好,不进默认包 | `warn` 档只在 Claude Code 的 Stop 上可用,所以 `clients: ["claude-code"]` | 有意改变(归类) |

## 3. 客户端映射

规则只写归一化的工具名与事件;客户端差异由 `internal/adapter/*` 处理。下表说明 pinrule 在各客户端上的做法如何落到 Ballast 的内容文件上。能力矩阵的权威数据是 `internal/model/capability.go` 与 `docs/architecture.md` §4。

### 3.1 Claude Code(一级)

- 事件:pinrule 注册 `UserPromptSubmit/PreToolUse/PostToolUse/Stop/SessionStart/PreCompact/SubagentStart/SubagentStop`(`pinrule/backends/claude_code.py:20-44`),工具类事件带 `matcher:"*"`(`claude_code.py:46-58`)。Ballast 规则可写 `PreToolUse/UserPromptSubmit/PostToolUse/PostToolUseFailure/Stop/SubagentStop`,注入走 SessionStart/UserPromptSubmit/PostToolBatch。
- 工具名:Claude Code 的 `Bash/PowerShell/Edit/MultiEdit/Write/NotebookEdit/Read/Agent` 就是规范名,不需要映射。
- 动作:PreToolUse 的 deny/ask/warn 与 Stop 的 block/`systemMessage` 全部可用。所有默认规则和可选规则在这里都有 `examples`(缺省 `client` 即 claude-code)。
- 例外:`chinese-plain-*` 的 `warn` 只在这里可用。

### 3.2 Codex(二级,能力全部【未核验】)

- 工具名:`apply_patch → Edit`、`exec_command → Bash`(`pinrule/backends/codex.py:56-64`)。Ballast 的 Codex 适配器同样归一,并把补丁解析成多个目标。
- 多文件补丁:pinrule 产出 `multi_file_targets`,`read_first` 逐个检查(`codex.py:904-952`、`read_first.py:27-54`)。Ballast 中每个补丁目标是一个绑定(dsl-semantics §5.2、§9),`read-before-edit` 的 Codex 示例覆盖"两个目标一个未读命中""全部已读不命中""只新建不命中";`no-tamper` 与 `no-todo-marker` 各有一个补丁示例。
- 没有 Read 工具:pinrule 靠 `exec_command` 的只读命令提取读路径(`codex.py:576-612`);Ballast 用 `CommandNode.Reads`,在 PostToolUse 登记,所以"先 `sed -n` 再 `apply_patch`"必须是两次工具调用。端到端验收在 U12。
- shell 参数:Codex 的 `exec_command` 用 `cmd` 键,`no-long-sleep` 与 `evidence-before-commit` 各有一个示例。
- 动作:只有 PreToolUse 的 deny(含 PermissionRequest);不支持 ask;warn 只在 UserPromptSubmit/PostToolUse;Stop 的 block 未核验(`pinrule/backends/native_capabilities.py:38-46`)。默认包全是 `log` + `target: deny`,在 Codex 上可以升级到 deny。

### 3.3 Cursor(二级,能力全部【未核验】)

- 工具名:`Shell → Bash`、`Task → Agent`,`Read/Write` 同名;没有独立的 Edit,文件修改都归 `Write`(`pinrule/backends/cursor.py:46-57`)。`read-before-edit` 的 `tool` 列表含 `Write`,所以同样生效(默认包有一个 Cursor 示例)。
- 事件:`beforeShellExecution` 对应工具 Bash,`beforeMCPExecution` 对应 `mcp__<server>__<tool>`,`beforeReadFile` 对应 Read,`preToolUse` 对应其余(`native_capabilities.py:23-36`)。`no-long-sleep` 与 `evidence-before-commit` 各有一个 `beforeShellExecution` 示例。
- Stop:没有 block,只有 `followup_message`(需配 `loop_limit`),语义是续跑(`cursor.py:168-178`);lint 对 Stop 的 deny 报 `cursor-stop-followup` 警告。`keep-pushing` 因此在 Cursor 上表现为"自动续跑"。
- 注入:除 hook 外还要生成托管的 `~/.cursor/rules/ballast.mdc`(`alwaysApply: true`),对应 pinrule 的 `cursor_rules_sync.py`(`pinrule/cursor_rules_sync.py:1-40`);`no-tamper` 保护这个文件。

### 3.4 Hermes(三级,尽力支持)

- 工具名:`terminal/shell/execute_shell → Bash`,`read_file → Read`,`write_file/create_file → Write`,`patch_file/edit_file → Edit`(`pinrule/backends/hermes.py:53-68`)。`no-long-sleep` 有一个 `terminal` 示例。
- 事件:只有 `pre_tool_call`(PreToolUse)、`post_tool_call`(PostToolUse)、`pre_llm_call`(UserPromptSubmit 与注入);`on_session_end` 不能干预,没有 Stop(`native_capabilities.py:57-78`)。所有 Stop 规则(`claim-needs-evidence`、`keep-pushing`、`no-patch-intent`、`chinese-plain-*`)在 Hermes 上永不触发,lint 报 `event-unavailable` 警告。
- 动作:只有 PreToolUse 的 `{"decision":"block"}`;没有 ask/warn。

## 4. 内容文件的自检

- `go test ./internal/model -run 'TestContentFilesDecode|TestSchemaExamplesDecode'`:全部内容文件严格解码,id 合规且不重复,每条规则至少一个 hit 与一个非命中示例,message ≤ 300 字、inject ≤ 400 字。
- 默认包 5 条规则的 `inject` 文本合计 330 字(核对方法:对 `rules/default.json` 统计每条 `inject.text`(字符串形式取全文)的 Unicode 码点数之和,上限 `model.BudgetSessionStart = 8000`)。
- `testdata/pinrule-parity.json`:计划"完成条件 1"要求的对照用例。逐条对应 pinrule 的 `tests/test_checks.py`(92 个)与 `tests/test_bypass_pinrule.py`(34 个)测试函数:每个函数要么有一到多个 `cases`(输入、pinrule 的期望、对应的 Ballast 规则及其求值结果),要么在 `not_applicable` 里写明理由(pinrule 的检查函数注册表、`violations.jsonl` 与 `trigger_key` 属于 pinrule 内部,不迁移)。Ballast 与 pinrule 结果不同的用例必须在 `divergence` 里引用本文件的"有意改变"或"舍弃"行(如 `§2.2`),并给出同一意图在 Ballast 阈值下的 `adapted` 变体。`go test ./internal/cli -run TestPinruleParity`(`scripts/accept-m1.sh` 会单独跑一次)用 dsl 引擎逐条核对;设置环境变量 `PINRULE_SRC=<pinrule 源码目录>` 时还会核对测试函数清单与 pinrule 源码一致。
- 集成验收(U12,需要真实的适配器、路径服务与解析器):`ballast lint --client all` 对所有内容文件无错误,`ballast test-rules` 对所有内容文件与 `testdata/bash-regress.json` 全部通过。
- `testdata/bash-regress.json` 的 14 个回归用例(计划 B1)与规则的对应关系:第 1–12 例与第 14 例是 `regress-no-long-sleep` 的 `examples`(名字以序号开头),第 13 例 `git -C ../x commit -m m` 是 `regress-git-commit` 的 `hit`。该文件另有写入目标、循环、sudo、管道、`eval`/解析失败的回归规则。
