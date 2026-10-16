#!/usr/bin/env bash
# ballast hook 性能基准的入口(计划 B10):构建真实二进制,由独立的 Go 程序 scripts/bench
# 从外部父进程 spawn 它,按场景计时(启动到退出),门槛 p95 <= 30ms 且 Bad == 0、样本齐全。
#
# 用法:scripts/bench.sh [scripts/bench 的额外参数,如 -only '^(3|4)$']
# 环境变量:
#   N=300       每个场景正式次数
#   WARM=20     每个场景预热次数
#   ONLY=       场景 id 的正则(部分运行,报告会标记为不完整)
#   OUT=        报告目录(默认 <repo>/dist/bench,含 report.md 与 report.json)
#   KEEP=1      保留临时目录(默认运行结束后删除)
#
# 所有临时文件(含 1k/10k 文件的测试仓库、各场景的 BALLAST_HOME)都在 mktemp 目录下;
# 子进程环境由 bench 从零构造(HOME、BALLAST_HOME、CLAUDE_CONFIG_DIR、CODEX_HOME、
# HERMES_HOME、BALLAST_INSTALL_ROOT、GIT_CONFIG_GLOBAL 全部重定向),不触碰任何真实配置,不联网。
# 退出码:0 通过;1 门槛未通过;2 参数或环境错误。可在 macOS / Linux / Windows Git Bash 运行。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
N="${N:-300}"
WARM="${WARM:-20}"
OUT="${OUT:-$ROOT/dist/bench}"

BASE="$(mktemp -d "${TMPDIR:-/tmp}/ballast-bench.XXXXXX")"
cleanup() {
  if [[ "${KEEP:-0}" == 1 ]]; then
    echo "保留临时目录: $BASE" >&2
  else
    rm -rf "$BASE"
  fi
}
trap cleanup EXIT

EXE="$(cd "$ROOT" && GOPROXY=off go env GOEXE)"
BIN="$BASE/bin/ballast$EXE"
BENCH="$BASE/bin/bench$EXE"
mkdir -p "$BASE/bin" "$OUT"

# BALLAST_TEST_WATCHDOG_MS(仅 CI)放宽 150ms 看门狗,且只有带 ballast_test 标签的构建才认它;
# 生产构建忽略该变量。所以变量非空时才加标签。
TAGS=""
if [[ -n "${BALLAST_TEST_WATCHDOG_MS:-}" ]]; then
  TAGS="ballast_test"
fi

echo "== 构建 ballast(-trimpath -ldflags='-s -w'${TAGS:+,-tags $TAGS})与 bench"
(cd "$ROOT" \
  && GOPROXY=off go build -trimpath -ldflags='-s -w' -tags "$TAGS" -o "$BIN" ./cmd/ballast \
  && GOPROXY=off go build -o "$BENCH" ./scripts/bench)

ARGS=(-bin "$BIN" -base "$BASE/run" -root "$ROOT" -n "$N" -warm "$WARM" -json "$OUT/report.json" -md "$OUT/report.md")
if [[ -n "${ONLY:-}" ]]; then
  ARGS+=(-only "$ONLY")
fi

echo "== 运行基准(N=$N,WARM=$WARM)"
status=0
"$BENCH" "${ARGS[@]}" "$@" || status=$?

# 可选的交叉校验:hyperfine 存在时对场景 4(PreToolUse Bash 短命令)再测一次,仅供参考,不参与门槛。
if command -v hyperfine >/dev/null 2>&1 && [[ -d "$BASE/run/homes/4" ]]; then
  echo "== hyperfine 交叉校验(场景 4,仅供参考)"
  PAYLOAD="$BASE/run/hyperfine-payload.json"
  printf '{"session_id":"hf-sess","cwd":"%s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"hf-1","tool_input":{"command":"go test ./... 2>&1 | tail -20 && git status --short"}}\n' "$BASE/run/work" > "$PAYLOAD"
  env -i PATH="$PATH" HOME="$BASE/run/fakehome" BALLAST_HOME="$BASE/run/homes/4" \
    BALLAST_INSTALL_ROOT="$BASE/run/fakehome" BALLAST_MANAGED_DIR="$BASE/run/managed" \
    CLAUDE_CONFIG_DIR="$BASE/run/fakehome/.claude" CODEX_HOME="$BASE/run/fakehome/.codex" \
    HERMES_HOME="$BASE/run/fakehome/.hermes" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 \
    hyperfine -N --warmup "$WARM" --runs "$N" --input "$PAYLOAD" \
    --export-json "$OUT/hyperfine-scenario4.json" \
    "$BIN hook --client claude-code --event PreToolUse" || echo "hyperfine 交叉校验失败(不影响门槛)" >&2
else
  echo "== 未安装 hyperfine,跳过交叉校验"
fi

echo "== 报告: $OUT/report.md"
exit "$status"
