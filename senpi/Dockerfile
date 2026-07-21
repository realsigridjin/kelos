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

RUN npm install --prefix /opt/senpi --install-strategy=nested \
      "@code-yeongyu/senpi@${SENPI_VERSION}" \
  && ln -s /opt/senpi/node_modules/.bin/senpi /usr/local/bin/senpi

RUN node <<'NODE'
const fs = require("node:fs");
const path = require("node:path");

const packageRoot = "/opt/senpi/node_modules/@code-yeongyu/senpi";
const packageJSON = JSON.parse(
  fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"),
);
const dependencies = Object.keys(packageJSON.dependencies ?? {});
for (const dependency of dependencies) {
  const source = path.join("/opt/senpi/node_modules", dependency);
  const target = path.join(packageRoot, "node_modules", dependency);
  if (!fs.existsSync(source) || fs.existsSync(target)) continue;
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.symlinkSync(source, target, "dir");
}
NODE

RUN npm cache clean --force

RUN npm install \
      --prefix /opt/senpi/node_modules/@code-yeongyu/senpi \
      --install-strategy=nested \
      --no-save \
      "cross-spawn@7.0.6"

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
