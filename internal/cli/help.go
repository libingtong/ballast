package cli

// Help texts. They are constants so that printing help allocates nothing and
// never touches the file system.

const topUsage = `ballast(压舱石):AI 编码客户端的行为策略引擎

用法:ballast <命令> [参数]

命令:
  hook          由客户端调用的 hook 入口(--client <id> --event <原生事件>)
  lint          检查规则文件(schema、能力矩阵、注入预算)
  test-rules    运行规则文件内的 examples
  config show   显示当前生效的三层配置
  verify        后台运行 project.json 中的验证并记录证据;--status / --check / --foreground
  init          探测技术栈并写入 .ballast/project.json
  stats         统计规则命中;--rule <id> --sample N 抽样复核
  rules promote 把规则升级到更严的动作档位
  replay        对 transcript 离线回放规则
  audit lint    检查审计日志格式
  install       把 hook 安装到客户端配置
  uninstall     按 manifest 卸载
  doctor        自检(层级 T0/T1、各客户端状态)
  org render    输出组织托管设置模板;--part org-json 输出 org.json
  report        把审计日志脱敏聚合成一个本地文件(--out <文件>),不联网
  gc            清理过期状态(内部)
  version       显示版本

任何命令加 --help 只打印帮助并以 0 退出。退出码见 docs/architecture.md §7。
`

const hookUsage = `用法:ballast hook --client <claude-code|codex|cursor|hermes> --event <原生事件名>

从标准输入读取客户端的 hook JSON。正常时退出码为 0(标准输出可能为空,表示无决策);
任何异常一律退出码 1、标准输出为空、标准错误以 "ballast:" 开头。永不使用退出码 2。
`

const lintUsage = `用法:ballast lint [--layer org|repo|user] [--client <id>|all] [--stack] [--examples] [--json] [<文件|目录>...]

检查规则文件。目录只展开其中的 *.json(不递归)。不带路径时检查当前生效的三层规则文件。
  --layer     把输入文件视为指定层(org 层额外检查 ≤12 条)
  --client    按指定客户端的能力矩阵检查;all 表示全部;默认 = manifest 中已安装的客户端 + claude-code
  --stack     把输入作为 org → repo → user 的合并栈检查(重复 id、总预算、≤12)
  --examples  同时运行 examples(失败报 example-failed)
  --json      以 JSON 输出诊断
退出码:0 无错误(可有警告);1 有错误;2 用法;5 读文件失败。
`

const testRulesUsage = `用法:ballast test-rules [--json] [<文件|目录>...]

运行规则文件内每个 example(经真实客户端适配器解析输入)。不带路径时使用当前生效的三层规则文件。
退出码:0 全部通过;1 有失败;2 用法/文件不可解码。
`

const configUsage = `用法:ballast config show [--explain] [--json]

显示当前生效的规则栈(org / repo / user 三层、generation、通知)。
  --explain  逐条显示每条规则的合并结局(生效 / 被遮蔽 / 超限丢弃)
退出码:0;1 有层非法(使用 LKG 或为空,内容仍打印);2 用法。
`

const verifyUsage = `用法:
  ballast verify [名称...]                   后台启动 project.json 中的验证(默认全部 required),立即返回
  ballast verify --status [job_id|名称] [--json]
  ballast verify --check [--commit|--worktree]   供 git pre-commit 使用
  ballast verify --foreground <名称>          前台运行并流式输出
  ballast verify --run-job <job_id>           内部:分离的 runner
  ballast verify --hook-start --request <id> --cwd <目录> --client <id> [--session <id>] [--agent <id>]
                 [--project-dir <目录>] -- [名称...]        内部:T1 下由 hook 代起(参数格式冻结)

--status 的参数可以是 hook 代起时给出的 request id:没有同名 job 时回退读取请求文件(~/.ballast/requests),
多个 job 取最差结果(fail > timeout > signal > infra > 运行中 > pass);请求文件也不存在则按"无记录"退出 11。
退出码(启动):0 已启动;2 配置错误(无仓库 / 无 project.json / 未知名称 / project.json 非法);5 基础设施错误。
退出码(--status / --foreground / --run-job):0 pass;1 fail;3 timeout;4 signal;5 infra;6 运行中;7 superseded;11 无记录。
退出码(--check):0 新鲜;10 过期;11 无证据;12 无法判定;13 同名 job 运行中。
--check 默认 --commit:环境里有 GIT_INDEX_FILE 时(相对路径按当前目录解析)以它作为将要提交的索引。
`

const initUsage = `用法:ballast init [--yes] [--force] [--dry-run]

探测技术栈(go、node、python、rust、maven、gradle、dotnet、swift、ruby)并写入 .ballast/project.json。
  --yes      不询问直接写入
  --force    覆盖已有的 project.json
  --dry-run  只打印将写入的内容
退出码:0 已写入 / 无需写入 / dry-run;1 用户拒绝或未探测到栈;2 用法;3 已有 project.json 且未指定 --force。
`

const statsUsage = `用法:ballast stats [--since 7d] [--by rule|session|repo] [--rule <id> --sample N] [--json]

统计审计日志中的规则命中。--rule 与 --sample 一起使用时抽取最近 N 条命中并写入复核凭据。
退出码:0;2 用法;5 读审计失败。
`

const rulesUsage = `用法:ballast rules promote <id> --to warn|ask|deny [--force]

把 repo 或 user 层规则升级到更严的动作。闸门:近 7 天命中不少于 5 次且存在比规则最近修改更新的复核凭据;
--force 跳过闸门(不跳过能力检查,并写审计)。
退出码:0 已升级;1 闸门不满足;2 用法;4 规则不存在于 repo/user 层、是 org 规则或目标档位不被已安装客户端支持;5 I/O。
`

const replayUsage = `用法:ballast replay <transcript> [--client <id>] [--subagents] [--evidence auto|<文件>]
                       [--initial-state <文件>] [--rules <路径>] [--format json|md]

对 transcript 离线回放规则并分类(known-hit / known-miss / approximate / unknown)。默认格式 md。
退出码:0;2 用法;5 I/O。
`

const auditUsage = `用法:ballast audit lint [--json]

检查 ~/.ballast/audit 下全部审计文件的格式。
退出码:0 全部合规;1 有问题;5 I/O。
`

const installUsage = `用法:ballast install --client <id>[,<id>...] [--binary <绝对路径>] [--dry-run] [--codex-trust] [--force]

把 hook 条目外科式写入客户端配置(备份 + manifest)。--binary 缺省时取当前可执行文件
(os.Executable,解析符号链接);该路径位于临时 / 构建目录(例如 go run)时报错退出 2。
退出码:0 成功或无变化;1 托管层已部署而拒绝;2 用法 / 客户端未检测到且无 --force / 二进制路径不稳定;3 配置解析失败(未写入);5 I/O。
`

const uninstallUsage = `用法:ballast uninstall [--client <id>|--all] [--dry-run]

按 install-manifest.json 卸载(不带参数等价于 --all)。
退出码:0;1 部分条目被用户改过而保留(已警告);3 解析失败;5 I/O。
`

const doctorUsage = `用法:ballast doctor [--json]

检查二进制、目录权限、manifest、规则 lint、层级(T0/T1)与各客户端安装状态。
退出码:0 无 fail(可有 warn);1 有 fail。CI 是最终裁判:会话内规则只负责引导 agent。
`

const orgUsage = `用法:ballast org render [--client claude-code] [--binary <绝对路径>] [--home-root <目录>]... [--protect <路径>]...
                        [--part managed-settings|org-json] [--write --i-am-admin]

stdout 只输出一个合法 JSON,可直接重定向到文件:默认 --part managed-settings 为 Claude 托管设置片段(T1 模板),
--part org-json 为 org.json;org.json 各字段的风险提示写到 stderr。
  --part       输出哪一份文档,默认 managed-settings(Windows 只有 T0,没有 org.json)
  --home-root  写入 org.json 的 home_root(可重复;sssd/LDAP 用户 passwd 查不到时的家目录父目录)
  --protect    额外受保护路径(可重复),同时写入 org.json 的 protected_extra
  --write      需要 --i-am-admin 且以 root 运行:先写 <托管目录>/managed-settings.d/ballast.json,
               再写 <托管目录>/ballast/org.json(写入它即锁定本机);带 SUDO_UID 时先预检该用户的家目录
退出码:0;2 用法(含 --write 预检失败:调用 sudo 的用户 passwd 无条目且没有 home_root);4 --write 但非 root 或缺 --i-am-admin;5 I/O。
`

const reportUsage = `用法:ballast report --out <文件> [--since 30d] [--force]

把审计日志聚合成一个脱敏的本地文件(窗口、会话/仓库计数、各客户端与层级记录数、规则命中、诊断计数、
verify 结果、hook 延迟直方图)。默认不运行、不联网、不发送任何东西;绝不包含 session/repo id、路径、
命令文本、提示词或主机/用户名,非内置规则的 id 只输出哈希。分享前请自行检查文件。
  --out    输出文件(必需;已存在则退出 4,除非 --force)
  --since  统计窗口:Nd、Nh 或 RFC3339,默认 30d
退出码:0 已写入;1 隐私自检失败(什么都不写,这是 bug);2 用法;4 文件已存在且无 --force;5 I/O。
`

const gcUsage = `用法:ballast gc

清理过期的会话状态、审计文件与证据(内部命令,由 SessionStart 每天至多分离触发一次)。
退出码:0;5(写 stderr)。
`

const versionUsage = `用法:ballast version

显示版本。
`
