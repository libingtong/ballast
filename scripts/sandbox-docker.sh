#!/usr/bin/env bash
# sandbox-docker.sh - run Go tests that need a real bubblewrap in a Debian 12 container.
#
#   BALLAST_REQUIRE_DOCKER=1 bash scripts/sandbox-docker.sh [go test arguments...]
#
# Arguments are passed to `go test -count=1` inside the container; the default is
# ./internal/sandbox. Examples (docs/p2-design.md §3.6):
#
#   BALLAST_REQUIRE_DOCKER=1 bash scripts/sandbox-docker.sh
#   BALLAST_REQUIRE_DOCKER=1 bash scripts/sandbox-docker.sh ./internal/sandbox ./internal/runner
#   BALLAST_REQUIRE_DOCKER=1 bash scripts/sandbox-docker.sh ./internal/sandbox -run TestEscapeAttempts -v
#
# What it does
#   * image: golang:<go.mod version>-bookworm, pinned by digest (the table in
#     image_digest below must know the version; a go.mod bump without a new
#     digest fails loudly instead of silently floating), plus bubblewrap, git
#     and python3 installed at build time, and the module cache filled by
#     `go mod download` (the only steps that use the network);
#   * run: --network none, an ordinary user (uid 1000; /usr/bin/bwrap is
#     root-owned), the three --security-opt flags that bubblewrap needs inside
#     Docker (seccomp, apparmor, systempaths = unconfined; --privileged is NOT
#     used), CI=true BALLAST_REQUIRE_BWRAP=1, TMPDIR outside /tmp (inside the
#     sandbox /tmp is a private tmpfs, so fixtures must live elsewhere);
#   * the repository is mounted read-only and copied (without .git) into the
#     container; nothing is written to the host checkout;
#   * no credentials: nothing from the host environment is passed in, only the
#     explicit -e flags below; no socket or home directory is mounted.
#
# Build-time network overrides (for hosts that cannot reach deb.debian.org or
# proxy.golang.org; they change where packages come from, not which ones):
#   BALLAST_DEBIAN_MIRROR=http://mirrors.example.org/debian   (apt sources)
#   BALLAST_BUILD_GOPROXY=https://goproxy.example.org,direct  (go mod download)
#   BALLAST_DOCKER_VERBOSE=1                                   (show the build output)
#   BALLAST_DOCKER_REBUILD=1                                   (ignore an existing image)
#   BALLAST_DOCKER_USER=0:0                                    (run the tests as root instead of uid 1000, for the
#                                                              few tests that need root to create root-owned files,
#                                                              e.g. TestBwrapPathDirectoryMustBeTrusted)
# Package signatures are still verified by apt, module hashes by go.sum.
#
# Without Docker the script prints SKIP and exits 0, unless
# BALLAST_REQUIRE_DOCKER=1, in which case it fails (exit 1). That variable alone
# is enough (CI=true is not needed).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# digest of the multi-arch index of golang:<version>-bookworm
image_digest() {
  case "$1" in
    1.27.2) echo "sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61" ;;
    *) return 1 ;;
  esac
}

die() { echo "sandbox-docker: FAIL: $*" >&2; exit 1; }

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  if [ "${BALLAST_REQUIRE_DOCKER:-}" = 1 ]; then
    die "Docker is not available and BALLAST_REQUIRE_DOCKER=1"
  fi
  echo "sandbox-docker: SKIP: Docker is not available (set BALLAST_REQUIRE_DOCKER=1 to make this a failure)"
  exit 0
fi

go_version="$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' go.mod | head -n 1)"
[ -n "$go_version" ] || die "cannot read the go version from go.mod"
digest="$(image_digest "$go_version")" || die "no pinned golang image digest for go $go_version: add one to image_digest in scripts/sandbox-docker.sh (docker buildx imagetools inspect golang:$go_version-bookworm)"
base_image="golang:${go_version}-bookworm@${digest}"

if [ "$#" -eq 0 ]; then
  set -- ./internal/sandbox
fi

hash_cmd() { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi; }

ctx="$(mktemp -d "${TMPDIR:-/tmp}/ballast-sandbox-docker.XXXXXX")"
trap 'rm -rf "$ctx"' EXIT
cp go.mod "$ctx/go.mod"
if [ -f go.sum ]; then cp go.sum "$ctx/go.sum"; else : >"$ctx/go.sum"; fi
cat >"$ctx/Dockerfile" <<EOF
FROM ${base_image}
ARG DEBIAN_MIRROR=
ARG BUILD_GOPROXY=https://proxy.golang.org,direct
RUN if [ -n "\$DEBIAN_MIRROR" ]; then \\
      for f in /etc/apt/sources.list /etc/apt/sources.list.d/*; do [ -f "\$f" ] && sed -i "s|http://deb.debian.org/debian-security|\$DEBIAN_MIRROR-security|g; s|http://deb.debian.org/debian|\$DEBIAN_MIRROR|g; s|http://security.debian.org/debian-security|\$DEBIAN_MIRROR-security|g" "\$f"; done; \\
    fi \\
 && apt-get update \\
 && apt-get install -y --no-install-recommends bubblewrap git python3 ca-certificates \\
 && rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 -s /bin/bash tester \\
 && mkdir -p /run/ballast-it /home/tester/tmp /home/tester/deps \\
 && chown 1000:1000 /run/ballast-it /home/tester/tmp /home/tester/deps
COPY go.mod go.sum /home/tester/deps/
RUN chown 1000:1000 /home/tester/deps/go.mod /home/tester/deps/go.sum
USER 1000:1000
ENV HOME=/home/tester GOPATH=/home/tester/go GOCACHE=/home/tester/.cache/go-build GOTOOLCHAIN=local
WORKDIR /home/tester/deps
RUN GOPROXY="\$BUILD_GOPROXY" go mod download
EOF
tag="ballast-sandbox-test:$(cat "$ctx/Dockerfile" "$ctx/go.mod" "$ctx/go.sum" | hash_cmd | cut -c1-16)"

# The tag is a hash of everything that goes into the image (Dockerfile, go.mod,
# go.sum), so an existing image with that tag is the image; BALLAST_DOCKER_REBUILD=1
# forces a rebuild.
if [ "${BALLAST_DOCKER_REBUILD:-}" != 1 ] && docker image inspect "$tag" >/dev/null 2>&1; then
  echo "sandbox-docker: using image $tag"
else
  echo "sandbox-docker: building $tag (base $base_image)"
  quiet=-q
  [ "${BALLAST_DOCKER_VERBOSE:-}" = 1 ] && quiet=
  # shellcheck disable=SC2086  # $quiet is a single flag or empty
  docker build $quiet \
    --build-arg "DEBIAN_MIRROR=${BALLAST_DEBIAN_MIRROR:-}" \
    --build-arg "BUILD_GOPROXY=${BALLAST_BUILD_GOPROXY:-https://proxy.golang.org,direct}" \
    -t "$tag" "$ctx" >/dev/null || die "docker build failed (BALLAST_DOCKER_VERBOSE=1 shows the build output)"
fi

echo "sandbox-docker: go test -count=1 $*"
docker run --rm \
  --network none \
  --user "${BALLAST_DOCKER_USER:-1000:1000}" \
  --security-opt seccomp=unconfined \
  --security-opt apparmor=unconfined \
  --security-opt systempaths=unconfined \
  -v "$REPO_ROOT":/src:ro \
  -e CI=true \
  -e BALLAST_REQUIRE_BWRAP=1 \
  -e BALLAST_IT_RUNDIR=/run/ballast-it \
  -e HOME=/home/tester \
  -e TMPDIR=/home/tester/tmp \
  -e GOPROXY=off \
  -e GOFLAGS=-mod=readonly \
  -e GOTOOLCHAIN=local \
  "$tag" \
  bash -c '
    set -euo pipefail
    mkdir -p /home/tester/work
    tar -C /src --exclude=.git --exclude=.wt -cf - . | tar -C /home/tester/work -xf -
    cd /home/tester/work
    bwrap --version
    exec go test -count=1 "$@"
  ' bash "$@"
