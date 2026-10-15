#!/usr/bin/env bash
# M0c 性能原型一键脚本:构建 -> 生成 1k/10k 文件的 git 仓库 -> hook 场景 bench -> 快照 bench。
# 所有临时文件都在 <repo>/.m0tmp/ 下;不触碰 ~/.claude、~/.ballast 或任何系统路径。
#
# 用法:scripts/m0c-bench.sh [all|build|repos|hook|snapshot]
# 环境变量:N(正式次数,默认 300)、WARM(预热,默认 20)、SNAPN(快照次数,默认 30)、
#          ONLY(场景名正则过滤)、REGEN=1(强制重建测试仓库)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$ROOT/.m0tmp"
BIN="$TMP/bin"
REPOS="$TMP/repos"
OUT="$TMP/results"
N="${N:-300}"
WARM="${WARM:-20}"
SNAPN="${SNAPN:-30}"
STEP="${1:-all}"

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
GIT_ID=(-c user.name=m0c -c user.email=m0c@example.invalid -c commit.gpgsign=false -c core.hooksPath=/dev/null)

mkdir -p "$BIN" "$REPOS" "$OUT"

build() {
  echo "== 构建(-trimpath -ldflags='-s -w')"
  (cd "$ROOT" && go build -trimpath -ldflags='-s -w' -o "$BIN/hook" ./proto/m0c/hook \
    && go build -trimpath -ldflags='-s -w' -o "$BIN/noop" ./proto/m0c/noop \
    && go build -trimpath -ldflags='-s -w' -o "$BIN/bench" ./proto/m0c/bench)
  ls -l "$BIN/hook" "$BIN/noop" "$BIN/bench" | awk '{printf "%s  %d bytes (%.2f MiB)\n", $NF, $5, $5/1048576}'
  # 对照:不裁剪符号的普通构建大小
  (cd "$ROOT" && go build -o "$TMP/hook.dbg" ./proto/m0c/hook && ls -l "$TMP/hook.dbg" | awk '{printf "hook(不加 -s -w/-trimpath) %d bytes\n", $5}'; rm -f "$TMP/hook.dbg")
}

# gen_repo <name> <文件数>:约 80% .go、10% .md、10% docs/**;提交后留 5 处工作区改动(未暂存)。
gen_repo() {
  local name="$1" total="$2" dir="$REPOS/$1"
  if [[ -d "$dir/.git" && "${REGEN:-0}" != 1 ]]; then
    echo "   $name 已存在($(git -C "$dir" ls-files | wc -l | tr -d ' ') 个已跟踪文件),跳过"
    return
  fi
  rm -rf "$dir"; mkdir -p "$dir"
  echo "   生成 $name ($total 文件)..."
  (
    cd "$dir"
    git init -q -b main
    awk -v total="$total" 'BEGIN {
      for (i = 0; i < total; i++) {
        d = sprintf("pkg%03d/sub%02d", i % 100, int(i / 100) % 10)
        r = i % 10
        if (r == 0)      f = sprintf("%s/README%d.md", d, i)
        else if (r == 1) f = sprintf("docs/%s/note%d.txt", sprintf("g%02d", i % 50), i)
        else             f = sprintf("%s/file%d.go", d, i)
        print f
      }
    }' > .filelist
    # 批量创建目录与文件
    xargs -n 500 sh -c 'for f in "$@"; do mkdir -p "$(dirname "$f")"; done' _ < .filelist
    while IFS= read -r f; do
      printf 'package x\n\n// file %s\nfunc F() int { return %d }\n' "$f" "$RANDOM" > "$f"
    done < .filelist
    rm .filelist
    git add -A
    git "${GIT_ID[@]}" commit -q -m init
    sleep 2                      # 越过 racy-git 窗口,使索引 stat 缓存干净
    git update-index -q --refresh
    # 少量改动(5 个已跟踪文件:3 .go、1 .md、1 docs)
    for f in $(git ls-files '*.go' | sed -n '1p;57p;301p') $(git ls-files '*.md' | head -1); do
      echo "// modified" >> "$f"
    done
    g="$(git ls-files 'docs/**' | head -1)"; echo "modified" >> "$g"
  )
  echo "   $name: $(git -C "$dir" ls-files | wc -l | tr -d ' ') 已跟踪, $(git -C "$dir" status --porcelain | wc -l | tr -d ' ') 处改动"
}

repos() {
  echo "== 生成测试仓库"
  gen_repo r1k 1000
  gen_repo r10k 10000
}

hook() {
  echo "== hook 场景 bench(N=$N WARM=$WARM)"
  "$BIN/bench" -mode hook -hook "$BIN/hook" -noop "$BIN/noop" -rules "$ROOT/proto/m0c/rules/rules12.json" \
    -base "$TMP/run" -n "$N" -warm "$WARM" -only "${ONLY:-}" \
    -repo1k "$REPOS/r1k" -repo10k "$REPOS/r10k" -json "$OUT/hook.json" | tee "$OUT/hook.md"
  # hyperfine 交叉校验(可选,未安装则跳过)
  if command -v hyperfine >/dev/null 2>&1; then
    echo "== hyperfine 交叉校验"
    export BALLAST_HOME="$TMP/run/hf-home" BALLAST_RULES="$ROOT/proto/m0c/rules/rules12.json"
    rm -rf "$BALLAST_HOME"
    printf '{"session_id":"hf","cwd":"%s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"hf1","tool_input":{"command":"go test ./... | tail -20"}}' "$TMP/run/work" > "$TMP/run/hf.json"
    hyperfine -N --warmup 20 --runs 300 --input "$TMP/run/hf.json" "$BIN/hook" --export-json "$OUT/hyperfine.json" | tee "$OUT/hyperfine.txt"
  else
    echo "== hyperfine 未安装,跳过交叉校验" | tee "$OUT/hyperfine.txt"
  fi
}

snapshot() {
  echo "== 快照 bench(N=$SNAPN)"
  "$BIN/bench" -mode snapshot -base "$TMP/run" -repo1k "$REPOS/r1k" -repo10k "$REPOS/r10k" \
    -snapn "$SNAPN" -snapwarm 3 -json "$OUT/snapshot.json" | tee "$OUT/snapshot.md"
}

case "$STEP" in
  build) build ;;
  repos) repos ;;
  hook) build; repos; hook ;;
  snapshot) build; repos; snapshot ;;
  all) build; repos; snapshot; hook ;;
  *) echo "用法: $0 [all|build|repos|hook|snapshot]" >&2; exit 2 ;;
esac
