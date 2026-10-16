# boundary-t1.sh 镜像(docs/p2-design.md §5.3):复用 M0b 镜像(scripts/probe-m0b/Dockerfile)——同一个基础镜像
# digest、同一组 apt 包、同一个固定版本的 Claude Code、同一个 agent 用户(uid 1000)。
# Claude Code 只用于 t1-hooks-outside-sandbox 与 t1-lower-hooks(无凭据:假 key + 黑洞端点)和
# --with-credentials 的"agent 真正尝试"部分;其余断言只需要 bwrap、git、python3。
# 只有构建镜像需要联网;运行期 --network none(联网用例用 --network bridge 变体)。
FROM node:22-bookworm-slim@sha256:c3de60bf2f9dd0ac6370e6117950ff62d6e339527e7472301c9c78a017978392

ARG DEBIAN_MIRROR=
ARG CLAUDE_CODE_VERSION=2.1.300
RUN if [ -n "$DEBIAN_MIRROR" ]; then \
      for f in /etc/apt/sources.list /etc/apt/sources.list.d/*; do [ -f "$f" ] && sed -i "s|http://deb.debian.org/debian-security|$DEBIAN_MIRROR-security|g; s|http://deb.debian.org/debian|$DEBIAN_MIRROR|g; s|http://security.debian.org/debian-security|$DEBIAN_MIRROR-security|g" "$f"; done; \
    fi \
 && apt-get update \
 && apt-get install -y --no-install-recommends bubblewrap socat jq python3 git ca-certificates procps util-linux \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
 && npm cache clean --force

# 基础镜像自带 uid 1000 的 node 用户;测试用户固定为 agent(uid 1000,非 root),与 M0b 一致。
RUN userdel -r node \
 && useradd -m -u 1000 -s /bin/bash agent

ENV DISABLE_AUTOUPDATER=1 DISABLE_TELEMETRY=1 DISABLE_ERROR_REPORTING=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
USER agent
WORKDIR /home/agent
