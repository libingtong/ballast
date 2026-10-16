# M0b 托管层取证镜像(docs/p2-design.md §6.2)。只有构建镜像需要联网;运行期 --network none。
# 基础镜像以 digest 固定(node:22-bookworm-slim)。
FROM node:22-bookworm-slim@sha256:c3de60bf2f9dd0ac6370e6117950ff62d6e339527e7472301c9c78a017978392

ARG CLAUDE_CODE_VERSION=2.1.300

RUN apt-get update \
 && apt-get install -y --no-install-recommends bubblewrap socat jq python3 git ca-certificates procps util-linux \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
 && npm cache clean --force

# 基础镜像自带 uid 1000 的 node 用户;取证用户固定为 agent(uid 1000,非 root)。
RUN userdel -r node \
 && useradd -m -u 1000 -s /bin/bash agent

# 探针 hook(root 所有):把 stdin 写到 /tmp/probe/<label>-<event>-<ns>.json,stdout 为空,exit 0。
COPY probe.sh hook-write.sh hang.py /opt/probe/
RUN chown -R root:root /opt/probe && chmod 0755 /opt/probe /opt/probe/*

ENV DISABLE_AUTOUPDATER=1 DISABLE_TELEMETRY=1 DISABLE_ERROR_REPORTING=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
USER agent
WORKDIR /home/agent
