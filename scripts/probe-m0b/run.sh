#!/usr/bin/env bash
# M0b 托管层容器取证驱动(docs/p2-design.md §6)。
#
# 用法:
#   bash scripts/probe-m0b/run.sh                      # 跑全部无凭据用例,生成产物
#   bash scripts/probe-m0b/run.sh --cases a,b          # 只重跑指定用例(其余条目保持不变)
#   ANTHROPIC_API_KEY=<用户自己提供> bash scripts/probe-m0b/run.sh --with-credentials --cases sandbox-denywrite-bash,...
#
# 选项:
#   --with-credentials  运行需凭据的用例(只从调用者环境读 ANTHROPIC_API_KEY,docker run -e 只传变量名,
#                       运行期用 --network bridge;不落盘、不进日志;不读 keychain 与 ~/.claude*)
#   --cases <a,b,..>    只跑这些 case_id
#   --rebuild           强制重建镜像
#   --list              列出全部 case_id 与是否需凭据
# 环境变量:
#   BALLAST_M0B_OUT           产物根目录(默认 <repo>/docs/facts)
#   BALLAST_M0B_BUILD_PROXY   构建镜像时使用的 HTTP(S) 代理(只用于 docker build;回环地址会改写成 host.docker.internal)
#   BALLAST_REQUIRE_DOCKER=1  与 CI=true 同时设置时,没有 Docker 即失败(否则显式 SKIP,退出码 0)
#
# 只有构建镜像需要联网;每个用例在全新容器里运行,默认 --network none,宿主上不写任何托管路径,
# 不修改 ~/.claude*、~/.codex、~/.cursor、~/.hermes、~/.ballast。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT_ROOT="${BALLAST_M0B_OUT:-$ROOT/docs/facts}"
CLAUDE_CODE_VERSION="${BALLAST_M0B_CLAUDE_VERSION:-2.1.300}"

with_cred=0; only=""; rebuild=0; list=0
while [ $# -gt 0 ]; do
  case "$1" in
    --with-credentials) with_cred=1 ;;
    --cases) shift; only="${1:-}" ;;
    --rebuild) rebuild=1 ;;
    --list) list=1 ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
  shift
done

mapfile_cases() { grep -v '^\s*#' "$HERE/cases/ORDER" | grep -v '^\s*$'; }
ALL_CASES=()
while IFS= read -r c; do ALL_CASES+=("$c"); done < <(mapfile_cases)
needs_cred() { jq -r '.needs_credentials // false' "$HERE/cases/$1/case.json"; }

if [ "$list" = 1 ]; then
  for c in "${ALL_CASES[@]}"; do printf '%s\tneeds_credentials=%s\n' "$c" "$(needs_cred "$c")"; done
  exit 0
fi

# --- Docker 可用性 ---------------------------------------------------------
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  if [ "${CI:-}" = true ] && [ "${BALLAST_REQUIRE_DOCKER:-}" = 1 ]; then
    echo "probe-m0b: 需要 Docker(CI=true 且 BALLAST_REQUIRE_DOCKER=1),但 docker 不可用" >&2
    exit 1
  fi
  echo "SKIP probe-m0b: 本机没有可用的 Docker" >&2
  exit 0
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "probe-m0b: 宿主需要 jq" >&2; exit 1
fi

# --- 选择用例 --------------------------------------------------------------
selected=()
if [ -n "$only" ]; then
  IFS=',' read -r -a want <<< "$only"
  for w in "${want[@]}"; do
    found=0
    for c in "${ALL_CASES[@]}"; do [ "$c" = "$w" ] && found=1; done
    [ "$found" = 1 ] || { echo "probe-m0b: 未知 case_id: $w" >&2; exit 2; }
    selected+=("$w")
  done
else
  selected=("${ALL_CASES[@]}")
fi

if [ "$with_cred" = 1 ]; then
  if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    echo "probe-m0b: --with-credentials 需要调用者环境里有 ANTHROPIC_API_KEY(脚本不读 keychain 与 ~/.claude*)" >&2
    exit 2
  fi
fi

# --- 镜像 ------------------------------------------------------------------
img_hash="$(cat "$HERE/Dockerfile" "$HERE/probe.sh" "$HERE/hook-write.sh" "$HERE/hang.py" | shasum -a 256 | cut -c1-10)"
IMAGE="ballast-m0b:${CLAUDE_CODE_VERSION}-${img_hash}"
if [ "$rebuild" = 1 ] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  px="${BALLAST_M0B_BUILD_PROXY:-${HTTPS_PROXY:-${https_proxy:-${HTTP_PROXY:-${http_proxy:-}}}}}"
  px="$(printf '%s' "$px" | sed -E 's#//(127\.0\.0\.1|localhost)#//host.docker.internal#')"
  bargs=(--build-arg "CLAUDE_CODE_VERSION=$CLAUDE_CODE_VERSION")
  if [ -n "$px" ]; then bargs+=(--build-arg "HTTP_PROXY=$px" --build-arg "HTTPS_PROXY=$px"); fi
  echo "probe-m0b: 构建镜像 $IMAGE" >&2
  if ! docker build -q "${bargs[@]}" -t "$IMAGE" "$HERE" >/dev/null; then
    echo "probe-m0b: 镜像构建失败(构建阶段需要联网;可用 BALLAST_M0B_BUILD_PROXY 指定代理)" >&2
    exit 1
  fi
fi

# --- 逐个用例 --------------------------------------------------------------
WORK="$(mktemp -d "${TMPDIR:-/tmp}/ballast-m0b.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$OUT_ROOT/payloads/m0b"
CASES_JSON="$OUT_ROOT/m0b-cases.json"
[ -s "$CASES_JSON" ] || echo '[]' > "$CASES_JSON"

# 路径规范化:容器内路径 -> 占位符(与 M0a 样本约定一致:<repo>、<home>、claude-<uid>)。
normalize() {
  sed -E \
    -e 's#-home-agent-proj#-<project-key>#g' \
    -e 's#/home/agent/proj#<repo>#g' \
    -e 's#/home/agent#<home>#g' \
    -e 's#claude-[0-9]+-#claude-<uid>-#g'
}

rc_all=0
for c in "${selected[@]}"; do
  cdir="$HERE/cases/$c"
  meta="$cdir/case.json"
  nc="$(needs_cred "$c")"
  odir="$WORK/$c"; mkdir -p "$odir"; chmod 0777 "$odir"
  if [ "$nc" = true ] && [ "$with_cred" != 1 ] && [ ! -f "$cdir/scenario.sh" ]; then
    jq -n --slurpfile m "$meta" --arg ver "$CLAUDE_CODE_VERSION" '$m[0] | {case_id, events, schema_keys:(.schema_keys // {}), assertion, result:"needs_credentials",
        mode:"none", claude_version:$ver, observation:(.not_run_observation // "未运行:该用例需要真实模型调用,没有用户明确提供的凭据。"),
        evidence:[], recheck}' > "$odir/entry.json"
  else
    dflags=(--rm --init --user root --name "ballast-m0b-$c-$$" -v "$HERE:/m0b:ro" -v "$odir:/out" -e "CASE_ID=$c")
    if [ "$(jq -r '.sandbox // false' "$meta")" = true ]; then
      dflags+=(--security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined)
    fi
    if [ "$nc" = true ] && [ "$with_cred" = 1 ]; then
      # 凭据只从调用者环境读,docker run -e 只传变量名;运行期需要访问 API,所以用 bridge 网络
      dflags+=(--network bridge -e ANTHROPIC_API_KEY -e BALLAST_M0B_CREDENTIALS=1)
    else
      # 无凭据:不联网。需凭据的用例在这里以 mock 模式离线运行(结果恒为 needs_credentials)
      dflags+=(--network none)
    fi
    echo "probe-m0b: [$c] ..." >&2
    if [ -f "$cdir/matrix.sh" ]; then
      # 在不同 --security-opt 组合下各跑一次 matrix.sh,结果写到 /out/matrix/<组合>.txt
      mkdir -p "$odir/matrix"
      for v in all3 no-seccomp no-apparmor no-systempaths default; do
        mflags=()
        case "$v" in
          all3) mo="seccomp apparmor systempaths" ;;
          no-seccomp) mo="apparmor systempaths" ;;
          no-apparmor) mo="seccomp systempaths" ;;
          no-systempaths) mo="seccomp apparmor" ;;
          default) mo="" ;;
        esac
        for o in $mo; do mflags+=(--security-opt "$o=unconfined"); done
        docker run --rm --init --user root --network none ${mflags[@]+"${mflags[@]}"} -v "$HERE:/m0b:ro" "$IMAGE" bash "/m0b/cases/$c/matrix.sh" > "$odir/matrix/$v.txt" 2>&1
      done
    fi
    # 容器以 root 运行并写 /out;结束时把属主还给宿主用户(Linux 宿主上否则无法规范化或删除这些文件)
    dflags+=(-e "HOST_UID=$(id -u)" -e "HOST_GID=$(id -g)")
    docker run "${dflags[@]}" "$IMAGE" bash -c 'bash "/m0b/cases/$CASE_ID/scenario.sh"; rc=$?; chown -R "$HOST_UID:$HOST_GID" /out 2>/dev/null; exit $rc' > "$odir/docker.log" 2>&1
    drc=$?
    if [ ! -s "$odir/result.json" ]; then
      echo "probe-m0b: [$c] 没有产出 result.json(docker rc=$drc),日志:" >&2
      tail -n 20 "$odir/docker.log" >&2
      cat "$odir/run.log" >&2 2>/dev/null || true
      rc_all=1
      continue
    fi
    # 规范化 payload 目录
    if [ -d "$odir/payloads" ]; then
      find "$odir/payloads" -type f | while IFS= read -r f; do
        tmp="$f.n"; normalize < "$f" > "$tmp" && mv "$tmp" "$f"
      done
    fi
    ev='[]'
    if [ -d "$odir/payloads/$c" ] || [ -d "$odir/payloads" ]; then ev="$(jq -n --arg p "payloads/m0b/$c/" '[$p]')"; fi
    jq -n --slurpfile m "$meta" --slurpfile r "$odir/result.json" --argjson ev "$ev" '
      $m[0] as $m | $r[0] as $r
      | {case_id: $m.case_id, events: $m.events,
         schema_keys: (if (($r.schema_keys // {}) | length) > 0 then $r.schema_keys else ($m.schema_keys // {}) end),
         assertion: $m.assertion, result: $r.result, mode: $r.mode,
         claude_version: $r.claude_version, observation: ($r.observation | gsub("/home/agent/proj";"<repo>") | gsub("/home/agent";"<home>")),
         evidence: $ev}
        + (if $m.recheck then {recheck: $m.recheck} else {} end)' > "$odir/entry.json"
    if [ "$(jq -r .result "$odir/entry.json")" = needs_credentials ]; then
      # 场景自己降级为需凭据(例如观测不到):补上 recheck
      jq --slurpfile m "$meta" '. + {recheck: ($m[0].recheck // "")}' "$odir/entry.json" > "$odir/entry.n" && mv "$odir/entry.n" "$odir/entry.json"
    fi
    # 替换 payload 目录
    rm -rf "${OUT_ROOT:?}/payloads/m0b/$c"
    if [ -d "$odir/payloads" ] && [ -n "$(ls -A "$odir/payloads" 2>/dev/null)" ]; then
      mkdir -p "$OUT_ROOT/payloads/m0b"
      cp -R "$odir/payloads" "$OUT_ROOT/payloads/m0b/$c"
      # 场景把 payload 放在 payloads/<子目录>/...;顶层即 case 目录内容
    fi
  fi
  # 合并到 m0b-cases.json(按 ORDER 排序)
  jq --slurpfile e "$odir/entry.json" '(map(select(.case_id != $e[0].case_id))) + [$e[0]]' "$CASES_JSON" > "$WORK/merged.json" && mv "$WORK/merged.json" "$CASES_JSON"
  echo "probe-m0b: [$c] => $(jq -r .result "$odir/entry.json")" >&2
done

# 按 ORDER 重排并格式化
order_json="$(printf '%s\n' "${ALL_CASES[@]}" | jq -R . | jq -s .)"
jq --argjson order "$order_json" 'sort_by(.case_id as $id | ($order | index($id)) // 999)' "$CASES_JSON" > "$WORK/sorted.json" && mv "$WORK/sorted.json" "$CASES_JSON"

# 卫生:产物里不得有本机路径或秘密
if grep -rEn '/Users/[a-z]|[~]/NewCode|sk-ant-|Bearer [A-Za-z0-9]' "$OUT_ROOT/payloads/m0b" "$CASES_JSON" >/dev/null 2>&1; then
  echo "probe-m0b: 产物中发现本机路径或疑似秘密" >&2
  grep -rEn '/Users/[a-z]|[~]/NewCode|sk-ant-|Bearer [A-Za-z0-9]' "$OUT_ROOT/payloads/m0b" "$CASES_JSON" >&2
  rc_all=1
fi
echo "probe-m0b: 完成,产物 $CASES_JSON" >&2
exit "$rc_all"
