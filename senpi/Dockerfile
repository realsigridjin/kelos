FROM node:22-bookworm-slim

ARG TARGETARCH
ARG SENPI_VERSION=2026.7.20

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    git \
    python3 \
    ripgrep \
  && rm -rf /var/lib/apt/lists/*

RUN npm install --global --install-strategy=nested \
      "@code-yeongyu/senpi@${SENPI_VERSION}" \
      "cross-spawn@7.0.6" \
  && mkdir -p /usr/local/lib/node_modules/@code-yeongyu/senpi/node_modules \
  && ln -s /usr/local/lib/node_modules/cross-spawn \
      /usr/local/lib/node_modules/@code-yeongyu/senpi/node_modules/cross-spawn \
  && npm cache clean --force

COPY --chmod=755 senpi/kelos_entrypoint.sh /kelos_entrypoint.sh
COPY --chmod=755 bin/kelos-capture-linux-${TARGETARCH} /kelos/kelos-capture

RUN useradd --uid 61100 --create-home --shell /bin/bash agent \
  && mkdir -p /home/agent/.local/bin /workspace \
  && chown -R 61100:61100 /home/agent /workspace

ENV HOME=/home/agent \
  PATH=/home/agent/.local/bin:/usr/local/bin:/usr/bin:/bin \
  SENPI_CODING_AGENT_DIR=/home/agent/.senpi/agent

USER 61100
WORKDIR /workspace

ENTRYPOINT ["tail", "-f", "/dev/null"]
