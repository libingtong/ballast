# probe-m0b:托管层容器取证

实现 `docs/p2-design.md` §6。在一次性 Linux 容器里写 `/etc/claude-code/managed-settings.json`(以及 `managed-settings.d/`),
用固定版本的 Claude Code 实测托管层行为,产物是 `docs/facts/m0b-cases.json`、`docs/facts/M0b-CONCLUSIONS.md`、
`docs/facts/payloads/m0b/<case>/`。

```sh
bash scripts/probe-m0b/run.sh                    # 全部 21 个用例(无凭据;需凭据的 6 个以 mock 模式离线运行,结果恒为 needs_credentials)
bash scripts/probe-m0b/run.sh --cases a,b        # 只重跑指定用例,其余条目保持不变
bash scripts/probe-m0b/run.sh --list             # 列出用例
go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json   # 检查产物
# 用户明确提供凭据时(只从调用者环境读 ANTHROPIC_API_KEY,docker run -e 只传变量名,运行期用 bridge 网络):
ANTHROPIC_API_KEY=<用户自己提供> bash scripts/probe-m0b/run.sh --with-credentials --cases sandbox-denywrite-bash,permissions-deny-edit,unsandboxed-refused,managed-denywrite-path-forms,managed-denywrite-anydepth,mcp-write
```

本机需要 Docker;没有 Docker 时显式 SKIP(退出码 0),`CI=true BALLAST_REQUIRE_DOCKER=1` 时失败。构建镜像需要联网
(apt、npm);构建走代理时用 `BALLAST_M0B_BUILD_PROXY=http://host:port`(回环地址会改写成 `host.docker.internal`,只用于
`docker build`)。运行期默认 `--network none`;只有 `--with-credentials` 的需凭据用例使用 bridge。

## 目录

| 路径 | 作用 |
|---|---|
| `Dockerfile` | `node:22-bookworm-slim`(digest 固定)+ bubblewrap/socat/jq/python3/git + 固定版本 Claude Code;用户 `agent`(uid 1000) |
| `probe.sh`、`hook-write.sh`、`hang.py` | 镜像内 `/opt/probe/`(root 所有)的探针 hook 与"只 accept 不应答"监听器 |
| `mock-api.py` | §6.6 模拟模型:按脚本发出 tool_use 的最小 Messages API 桩(`mode:"mock"`) |
| `lib.sh` | 容器内公共函数(夹具、claude 运行模式、探针读取、mock、需凭据用例骨架) |
| `fixtures/` | `managed-t1.json`(`org render` 的 T1 模板,由 `check` 的测试与 `internal/tier/testdata/managed_forms.json` 对齐)、`mcp-writer.py`、`tui.py`(pty 驱动交互式 TUI) |
| `cases/ORDER` | 用例顺序(§6.4 表) |
| `cases/<id>/case.json` | 静态字段:`events`、`assertion`、`needs_credentials`、`sandbox`(是否加三项 `--security-opt`)、`recheck` |
| `cases/<id>/scenario.sh` | 在全新容器里以 root 运行:布置夹具、以 `agent` 身份运行 claude、写 `/out/result.json` 与 `/out/payloads/` |
| `check/` | 产物检查器(独立 `main` 包) |

## 运行模式

`none`(无 key)、`blackhole`(dummy key + 拒绝连接端点)、`hang`(同上 + 只 accept 的监听器)、`mock`(本地桩)、
`credentials`(真实 key,只在 `--with-credentials` 时)。`dummy-not-a-credential` 不是凭据,运行期 `--network none` 保证它到不了任何服务。

## 安全

宿主上不写任何托管路径,不触碰 `~/.claude*`、`~/.codex`、`~/.cursor`、`~/.hermes`、`~/.ballast`;凭据不进镜像层、
文件或日志;payload 入库前把容器路径规范化为 `<repo>`、`<home>`,并经 `check` 做本机路径与秘密扫描。
