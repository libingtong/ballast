# Ballast(压舱石)

Claude Code 的行为策略引擎:持续注入关键规则、工具调用前强制检查、违规留痕审计。

- 声明式规则 DSL(≤12 条),不写代码即可新增拦截
- 四档动作 `deny / ask / warn / log`,新规则默认影子模式(log)
- 证据以 `ballast verify` 的退出码与内容快照为准,不解析测试输出
- 三层配置 org > repo > user
- 审计、统计、回放评测

> 状态:开发中。设计文档见 [`docs/ballast-plan.md`](docs/ballast-plan.md)(v0.4,末尾"v0.4 变更"优先),接口契约见 [`docs/architecture.md`](docs/architecture.md),审核记录见 [`docs/ballast-plan-review.md`](docs/ballast-plan-review.md)。

## 构建与命令

```sh
go build -o ballast ./cmd/ballast
./ballast --help          # 任何命令加 --help 只打印帮助,不产生副作用
```

| 命令 | 说明 |
|---|---|
| `ballast hook --client <id> --event <原生事件>` | 客户端调用的 hook 入口;只用退出码 0 / 1,异常时标准输出为空 |
| `ballast lint [--client all] [--examples] <文件\|目录>...` | 检查规则文件(schema、客户端能力矩阵、注入预算) |
| `ballast test-rules <文件\|目录>...` | 运行规则内的 examples |
| `ballast config show [--explain]` | 显示当前生效的 org / repo / user 三层规则 |
| `ballast init` | 探测技术栈,写入 `.ballast/project.json` |
| `ballast verify` / `--status` / `--check` / `--foreground` | 后台运行验证、查询结果;`--check` 供 git pre-commit 使用(读取 `GIT_INDEX_FILE`) |
| `ballast stats` / `ballast rules promote <id> --to deny` | 命中统计、抽样复核、升级动作档位 |
| `ballast replay <transcript>` | 对历史 transcript 离线回放规则 |
| `ballast audit lint` | 检查审计日志格式 |
| `ballast install --client <id>` / `uninstall` / `doctor` | 安装、卸载、自检(层级 T0 / T1 与各客户端状态) |
| `ballast org render` | 输出组织托管设置模板(T1) |

退出码约定见 [`docs/architecture.md` §7](docs/architecture.md)。**CI 是最终裁判**:会话内规则只负责引导 agent,不保证交付质量;`ballast doctor` 会在输出中重申这一点。

`ballast install` 默认用 `os.Executable`(解析符号链接后)得到的绝对路径写入 hook 命令;该路径位于临时或构建目录(例如 `go run`)时会以退出码 2 拒绝,请先把二进制放到稳定位置或用 `--binary <绝对路径>` 指定。

## 测试

```sh
GOPROXY=off go test -race ./...
```

`internal/cli` 的端到端测试会把 `ballast` 构建到临时目录并以子进程运行;`HOME`、`BALLAST_HOME`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`HERMES_HOME`、`BALLAST_INSTALL_ROOT`、`BALLAST_MANAGED_DIR`、`GIT_CONFIG_GLOBAL` 全部重定向到临时目录,不会读写真实的用户配置,也不会运行真实的客户端或联网。
