#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# boundary-t1.sh - M4 boundary, T1 half (docs/p2-design.md §5.3). Needs Docker
# (a Debian 12 / node:22-bookworm-slim container), no model, no credentials.
#
#   bash scripts/boundary-t1.sh                       # everything that needs no model
#   ANTHROPIC_API_KEY=<yours> bash scripts/boundary-t1.sh --with-credentials
#
# What it does
#   * cross-compiles ballast for linux (static), builds the image
#     scripts/boundary/Dockerfile (same base digest, packages and Claude Code
#     version as the M0b image; only the build step uses the network),
#   * runs ../scripts/boundary/t1-container.sh in throw-away containers:
#       main     --network none : t1-doctor, t1-runner-sandbox (escape script
#                in the runner's bwrap, as hook-started origin=hook and as
#                origin=cli, submodule/worktree fixtures, new-file creations,
#                the five forged-test cases), t1-tty (python pty),
#                t1-lower-sandbox, t1-config-dir, t1-forged-binary,
#                t1-orgjson, t1-env-override, t1-passwd-missing (uid with no
#                passwd entry), t1-hooks-outside-sandbox / t1-lower-hooks
#                (Claude Code with a dummy key and a black-hole endpoint),
#       net      --network bridge : runner_network deny / allow (asserted against
#                a listener on the container's own bridge address; the real-internet
#                probe is an extra that only runs when the container can reach it),
#       nobwrap  --network none : t1-sandbox-missing (bwrap removed);
#     as root it installs ballast at /usr/local/libexec/ballast/ballast and
#     runs `ballast org render --write --i-am-admin`; everything else runs as
#     the ordinary user agent (uid 1000);
#   * the parts that need a model are printed as NEEDS_CREDENTIALS with the
#     one command that re-tests them (the line above). The key is read ONLY
#     from the caller's environment and handed to `docker run` by NAME
#     (-e ANTHROPIC_API_KEY): never written to a file, an image layer or a
#     log, and no keychain or ~/.claude* file is read.
#
# Docker missing: SKIP (exit 0), unless BALLAST_REQUIRE_DOCKER=1 (the Linux CI
# job is to set it once impl/ci-fix lands, docs/p2-design.md section 11): then FAIL. Build-time network overrides: BALLAST_T1_BUILD_PROXY (or the
# caller's HTTPS_PROXY), BALLAST_DEBIAN_MIRROR. BALLAST_T1_IMAGE=<tag> uses an
# existing image (needs bwrap, git, python3, jq, util-linux, user agent uid 1000)
# instead of building one. BALLAST_T1_EXTERNAL_URL overrides the external
# address of the bridge variant (default: the Debian mirror the image build used).
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init boundary-t1

with_cred=0
case "${1:-}" in
  "") ;;
  --with-credentials) with_cred=1 ;;
  *) die "usage: boundary-t1.sh [--with-credentials]" ;;
esac

BOUNDARY_DIR="$LIB_DIR/boundary"
CLAUDE_CODE_VERSION="${BALLAST_T1_CLAUDE_VERSION:-2.1.300}"

# ---------------------------------------------------------------- docker ---
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  if [ "${BALLAST_REQUIRE_DOCKER:-}" = 1 ]; then
    bad "docker is available" "Docker is not usable and BALLAST_REQUIRE_DOCKER=1"
    finish
  fi
  skip "boundary-t1" "no usable Docker (set BALLAST_REQUIRE_DOCKER=1 to make this a failure)"
  finish
fi
if [ "$with_cred" = 1 ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  die "--with-credentials needs ANTHROPIC_API_KEY in the caller's environment (the script does not read a keychain or ~/.claude*)"
fi

hash_cmd() { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi; }

# ----------------------------------------------------------------- build ---
section "build"
case "$(docker info --format '{{.Architecture}}' 2>/dev/null)" in
  aarch64 | arm64) GOARCH_T=arm64 ;;
  x86_64 | amd64 | "") GOARCH_T=amd64 ;;
  *) die "unsupported Docker architecture: $(docker info --format '{{.Architecture}}')" ;;
esac
STAGE="$TMP_ROOT/t1-stage"
mkdir -p "$STAGE"
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_T" gocmd build -trimpath -ldflags='-s -w' -o "$STAGE/ballast" ./cmd/ballast) \
  || die "cannot cross-compile ballast for linux/$GOARCH_T"
cp "$BOUNDARY_DIR"/t1-container.sh "$BOUNDARY_DIR"/t1-escape.sh "$BOUNDARY_DIR"/t1-tty-probe.py "$BOUNDARY_DIR"/t1-net-probe.py "$STAGE/"
cp "$REPO_ROOT/rules/default.json" "$STAGE/default-rules.json"
chmod 0755 "$STAGE/ballast"
ok "linux/$GOARCH_T ballast cross-compiled"

if [ -n "${BALLAST_T1_IMAGE:-}" ]; then
  IMAGE="$BALLAST_T1_IMAGE"
  docker image inspect "$IMAGE" >/dev/null 2>&1 || die "BALLAST_T1_IMAGE=$IMAGE does not exist"
else
  IMAGE="ballast-t1:${CLAUDE_CODE_VERSION}-$(cat "$BOUNDARY_DIR/Dockerfile" | hash_cmd | cut -c1-10)"
  if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    px="${BALLAST_T1_BUILD_PROXY:-${HTTPS_PROXY:-${https_proxy:-${HTTP_PROXY:-${http_proxy:-}}}}}"
    px="$(printf '%s' "$px" | sed -E 's#//(127\.0\.0\.1|localhost)#//host.docker.internal#')"
    bargs=(--build-arg "CLAUDE_CODE_VERSION=$CLAUDE_CODE_VERSION" --build-arg "DEBIAN_MIRROR=${BALLAST_DEBIAN_MIRROR:-}")
    if [ -n "$px" ]; then bargs+=(--build-arg "HTTP_PROXY=$px" --build-arg "HTTPS_PROXY=$px"); fi
    note "building $IMAGE (the only step that uses the network)"
    if ! docker build -q "${bargs[@]}" -t "$IMAGE" "$BOUNDARY_DIR" >/dev/null; then
      die "docker build failed (set BALLAST_T1_BUILD_PROXY / BALLAST_DEBIAN_MIRROR when the host cannot reach the package sources)"
    fi
  fi
fi
ok "image $IMAGE"

# ------------------------------------------------------------------- run ---
# run_container NAME MODE NETWORK [docker run args...]: runs t1-container.sh and
# folds its protocol lines into this script's counters.
NEEDS_N=0
run_container() {
  local name=$1 mode=$2 net=$3 out err rc=0 line summary=0
  shift 3
  section "container: $name (mode $mode, --network $net)"
  out="$(mktmp "t1-$mode.out")"
  err="$(mktmp "t1-$mode.err")"
  "$ACCUTIL" run -timeout 1500s -stdout "$out" -stderr "$err" -- \
    docker run --rm --init --user root --network "$net" \
    --security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined \
    -v "$STAGE:/t1:ro" "$@" "$IMAGE" bash /t1/t1-container.sh "$mode" || rc=$?
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case $line in
      "ok "*) ok "${line#ok }" ;;
      "FAIL "*)
        if [[ $line == *" :: "* ]]; then bad "${line#FAIL }"; else bad "${line#FAIL }"; fi
        ;;
      "SKIP "*)
        SKIP_N=$((SKIP_N + 1))
        printf 'SKIP %s\n' "${line#SKIP }"
        ;;
      "NEEDS_CREDENTIALS "*)
        NEEDS_N=$((NEEDS_N + 1))
        printf 'needs_credentials %s\n' "${line#NEEDS_CREDENTIALS }"
        ;;
      "SUMMARY "*)
        summary=1
        note "$line"
        ;;
      *) printf '%s\n' "$line" ;;
    esac
  done <"$out"
  if [ "$rc" != 0 ] || [ "$summary" != 1 ]; then
    bad "container $name finished normally" "exit $rc, SUMMARY line seen: $summary" "stderr: $(tail -n 15 "$err" | head -c 1500)"
  fi
}

run_container main main none
ext="${BALLAST_T1_EXTERNAL_URL:-http://deb.debian.org/debian/dists/bookworm/InRelease}"
run_container net net bridge -e "T1_EXTERNAL_URL=$ext"
run_container nobwrap nobwrap none

if [ "$with_cred" = 1 ]; then
  # Only the NAME is passed: docker reads the value from this process's environment.
  run_container agent cred bridge -e ANTHROPIC_API_KEY
else
  note "$NEEDS_N agent-attempt checks need a model: not run (no credentials)"
  note "re-test with:  ANTHROPIC_API_KEY=<the key you provide> bash scripts/boundary-t1.sh --with-credentials"
fi

finish
