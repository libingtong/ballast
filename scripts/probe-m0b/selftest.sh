#!/usr/bin/env bash
# M0b 脚本级自测:不需要 Docker、不联网、不用任何凭据。
#   bash scripts/probe-m0b/selftest.sh
# run.sh 开始前会先跑它(也可单独用 run.sh --selftest)。覆盖:
#   - lib.sh 的每个运行模式(含 credentials)都能准备环境,credentials 缺 key 时明确失败;
#   - run_claude 在环境准备失败时不残留上一次的 RC、不启动 claude;
#   - credentials 模式没有任何工具调用时 cred_finish 写 unverifiable,而不是 pass/fail;
#   - 只有 needs_credentials 的用例才允许带 --with-credentials 的 recheck(run.sh 只对这类用例传 key)。
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
command -v jq >/dev/null 2>&1 || { echo "probe-m0b selftest: 需要 jq" >&2; exit 1; }
TMP="$(mktemp -d "${TMPDIR:-/tmp}/ballast-m0b-selftest.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
fails=0
fail() { echo "probe-m0b selftest: FAIL $*" >&2; fails=$((fails+1)); }

(
  # 子 shell:lib.sh 会 set -u、改 PATH 之外的全局状态
  export BALLAST_M0B_OUTDIR="$TMP/out" CASE_ID=selftest
  unset ANTHROPIC_API_KEY BALLAST_M0B_CREDENTIALS
  # shellcheck source=lib.sh
  source "$HERE/lib.sh"
  f=0
  bad() { echo "probe-m0b selftest: FAIL $*" >&2; f=$((f+1)); }

  for m in none blackhole hang mock; do
    mode_env "$m" >/dev/null 2>&1 || bad "mode_env $m 应当成功"
  done
  mode_env nonsense >/dev/null 2>&1 && bad "mode_env nonsense 应当失败"
  mode_env credentials >/dev/null 2>&1 && bad "没有 ANTHROPIC_API_KEY 时 mode_env credentials 应当失败"
  ANTHROPIC_API_KEY=dummy-not-a-credential mode_env credentials >/dev/null 2>&1 || bad "有 key 时 mode_env credentials 必须成功"
  out="$(ANTHROPIC_API_KEY=dummy-not-a-credential mode_env credentials 2>&1)"
  case "$out" in *dummy-not-a-credential*) bad "mode_env credentials 不得输出 key" ;; esac

  # run_claude:环境准备失败 => 返回非 0、RC 不是旧值、不写旧输出
  RC=0; echo stale > /tmp/run-last.out
  run_claude credentials 5 >/dev/null 2>&1 && bad "run_claude credentials 在缺 key 时应返回非 0"
  [ "$RC" != 0 ] || bad "run_claude 失败后 RC 仍是旧值 0"
  [ ! -s /tmp/run-last.out ] || bad "run_claude 失败后 /tmp/run-last.out 仍有旧内容"

  # cred_steps:有效结果 num_turns>=2 才算发生过工具调用
  echo '{"type":"result","is_error":false,"num_turns":3}' > /tmp/run-last.out
  [ "$(cred_steps)" = 2 ] || bad "cred_steps 应为 2(得到 $(cred_steps))"
  echo '{"type":"result","is_error":true,"num_turns":1}' > /tmp/run-last.out
  [ "$(cred_steps)" = 0 ] || bad "鉴权失败的运行 cred_steps 应为 0"
  : > /tmp/run-last.out
  [ "$(cred_steps)" = 0 ] || bad "空输出的运行 cred_steps 应为 0"

  # cred_finish:credentials 模式没有工具调用 => unverifiable
  AGENT_MODE=credentials; ATT_NOSTEPS=1; RC=125
  cred_finish 0 "selftest" >/dev/null 2>&1
  [ "$(jq -r .result "$OUT/result.json")" = unverifiable ] || bad "无工具调用时应写 unverifiable(得到 $(jq -r .result "$OUT/result.json"))"
  [ "$(jq -r .mode "$OUT/result.json")" = credentials ] || bad "mode 应为 credentials"
  ATT_NOSTEPS=0
  cred_finish 0 "selftest" >/dev/null 2>&1
  [ "$(jq -r .result "$OUT/result.json")" = pass ] || bad "有工具调用且条件成立应写 pass"
  cred_finish 1 "selftest" >/dev/null 2>&1
  [ "$(jq -r .result "$OUT/result.json")" = fail ] || bad "有工具调用且条件不成立应写 fail"
  AGENT_MODE=mock
  cred_finish 0 "selftest" >/dev/null 2>&1
  [ "$(jq -r .result "$OUT/result.json")" = needs_credentials ] || bad "mock 模式恒为 needs_credentials"
  exit "$f"
) || fails=$((fails+1))

# 带 --with-credentials 的 recheck 只能出现在 needs_credentials=true 的用例里
for meta in "$HERE"/cases/*/case.json; do
  id="$(basename "$(dirname "$meta")")"
  if jq -e '(.recheck // "") | contains("--with-credentials")' "$meta" >/dev/null 2>&1; then
    [ "$(jq -r '.needs_credentials // false' "$meta")" = true ] || fail "$id: recheck 用了 --with-credentials,但 needs_credentials 不是 true(run.sh 不会给它传 key)"
  fi
done

if [ "$fails" -ne 0 ]; then echo "probe-m0b selftest: $fails 项失败" >&2; exit 1; fi
echo "probe-m0b selftest: ok" >&2
