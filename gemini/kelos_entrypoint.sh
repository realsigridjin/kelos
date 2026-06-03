#!/bin/bash
# kelos_entrypoint.sh — Kelos agent image interface implementation for
# Google Gemini CLI.
#
# Interface contract:
#   - First argument ($1): the task prompt
#   - KELOS_MODEL env var: model name (optional)
#   - UID 61100: shared between git-clone init container and agent
#   - Working directory: /workspace/repo when a workspace is configured

set -uo pipefail

PROMPT="${1:?Prompt argument is required}"

ARGS=(
  "--yolo"
  "--output-format" "stream-json"
  "-p" "$PROMPT"
)

MODEL_ARG="${KELOS_MODEL:-}"
NATIVE_EFFORT_CONFIGURED=""

if [ -n "${KELOS_EFFORT:-}" ]; then
  mkdir -p ~/.gemini
  if [ -n "${KELOS_MODEL:-}" ]; then
    EFFORT_MODEL_ALIAS="kelos-effort-${KELOS_MODEL//[^A-Za-z0-9_-]/-}"
    KELOS_EFFORT_MODEL="$EFFORT_MODEL_ALIAS" node -e '
const fs = require("fs");
const os = require("os");
const path = require("path");
const cfgPath = path.join(os.homedir(), ".gemini", "settings.json");
let existing = {};
try { existing = JSON.parse(fs.readFileSync(cfgPath, "utf8")); } catch {}
const model = process.env.KELOS_MODEL;
const effort = process.env.KELOS_EFFORT;
const alias = process.env.KELOS_EFFORT_MODEL;
const normalized = effort.toLowerCase();
function budget(value) {
  if (/^-?\d+$/.test(value)) return Number(value);
  switch (value) {
    case "minimal": return 512;
    case "low": return 1024;
    case "medium": return 4096;
    case "high": return 8192;
    case "xhigh": return 16384;
    case "max": return 24576;
    default: return undefined;
  }
}
function level(value) {
  switch (value) {
    case "minimal":
    case "low":
      return "LOW";
    case "medium":
    case "high":
    case "xhigh":
    case "max":
      return "HIGH";
    default:
      return undefined;
  }
}
let thinkingConfig;
if (model.includes("gemini-3") || model.includes("3.")) {
  const thinkingLevel = level(normalized);
  if (thinkingLevel) thinkingConfig = { thinkingLevel };
} else if (model.includes("gemini-2.5") || model.includes("2.5")) {
  const thinkingBudget = budget(normalized);
  if (thinkingBudget !== undefined) thinkingConfig = { thinkingBudget };
}
if (!thinkingConfig) process.exit(2);
existing.modelConfigs = existing.modelConfigs || {};
existing.modelConfigs.customAliases = existing.modelConfigs.customAliases || {};
existing.modelConfigs.customAliases[alias] = {
  modelConfig: {
    model,
    generateContentConfig: { thinkingConfig },
  },
};
fs.writeFileSync(cfgPath, JSON.stringify(existing, null, 2));
'
    case "$?" in
      0)
        MODEL_ARG="$EFFORT_MODEL_ALIAS"
        NATIVE_EFFORT_CONFIGURED="true"
        ;;
      2)
        echo "Warning: KELOS_EFFORT '$KELOS_EFFORT' cannot be mapped to Gemini model '$KELOS_MODEL'; using prompt steering" >&2
        ;;
      *)
        echo "Warning: failed to write Gemini effort config; using prompt steering" >&2
        ;;
    esac
  else
    echo "Warning: KELOS_EFFORT requires KELOS_MODEL for native Gemini config; using prompt steering" >&2
  fi
fi

if [ -n "$MODEL_ARG" ]; then
  ARGS+=("--model" "$MODEL_ARG")
fi

# Write user-level instructions (global scope read by Gemini CLI)
if [ -n "${KELOS_AGENTS_MD:-}" ] || { [ -n "${KELOS_EFFORT:-}" ] && [ -z "$NATIVE_EFFORT_CONFIGURED" ]; }; then
  mkdir -p ~/.gemini
  {
    if [ -n "${KELOS_AGENTS_MD:-}" ]; then
      printf '%s\n' "$KELOS_AGENTS_MD"
    fi
    if [ -n "${KELOS_EFFORT:-}" ] && [ -z "$NATIVE_EFFORT_CONFIGURED" ]; then
      printf '\n# Kelos Effort\nUse %s reasoning effort for this task.\n' "$KELOS_EFFORT"
    fi
  } >~/.gemini/GEMINI.md
fi

# Install each plugin as a Gemini extension with skills and agents
if [ -n "${KELOS_PLUGIN_DIR:-}" ] && [ -d "${KELOS_PLUGIN_DIR}" ]; then
  for plugindir in "${KELOS_PLUGIN_DIR}"/*/; do
    [ -d "$plugindir" ] || continue
    pluginname=$(basename "$plugindir")
    # Sanitize plugin name for safe JSON interpolation
    safename=$(printf '%s' "$pluginname" | tr -d '"\\\n\r')
    extdir="$HOME/.gemini/extensions/${pluginname}"
    mkdir -p "$extdir"
    printf '{"name":"%s"}' "$safename" >"$extdir/gemini-extension.json"
    # Copy skills directory
    if [ -d "${plugindir}skills" ]; then
      cp -r "${plugindir}skills" "$extdir/skills"
    fi
    # Copy agents directory
    if [ -d "${plugindir}agents" ]; then
      cp -r "${plugindir}agents" "$extdir/agents"
    fi
  done
fi

# Write MCP server configuration to Gemini settings.
# KELOS_MCP_SERVERS contains JSON with an "mcpServers" key that Gemini
# settings.json accepts directly. Merge with existing settings if present.
if [ -n "${KELOS_MCP_SERVERS:-}" ]; then
  settings_file="$HOME/.gemini/settings.json"
  if [ -f "$settings_file" ]; then
    # Merge mcpServers into existing settings using a small Node.js helper.
    # Read KELOS_MCP_SERVERS from the environment to avoid exposing
    # potentially sensitive headers in process argument lists.
    node -e '
const fs = require("fs");
const existing = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const mcp = JSON.parse(process.env.KELOS_MCP_SERVERS);
existing.mcpServers = Object.assign(existing.mcpServers || {}, mcp.mcpServers || {});
fs.writeFileSync(process.argv[1], JSON.stringify(existing, null, 2));
' "$settings_file"
  else
    mkdir -p ~/.gemini
    printf '%s' "$KELOS_MCP_SERVERS" >"$settings_file"
  fi
fi

# Run pre-agent setup command if configured. KELOS_SETUP_COMMAND is the
# JSON-encoded exec-form array from Workspace.spec.setupCommand. A non-zero
# exit aborts the task before the agent starts.
if [ -n "${KELOS_SETUP_COMMAND:-}" ]; then
  printf '\n---KELOS_SETUP_COMMAND_START---\n' >&2
  node -e '
const { spawnSync } = require("child_process");
const cmd = JSON.parse(process.env.KELOS_SETUP_COMMAND);
if (!Array.isArray(cmd) || cmd.length === 0 || cmd.some(a => typeof a !== "string")) {
  console.error("KELOS_SETUP_COMMAND must be a non-empty JSON array of strings");
  process.exit(2);
}
const r = spawnSync(cmd[0], cmd.slice(1), { stdio: "inherit" });
if (r.error) { console.error(r.error.message); process.exit(127); }
process.exit(r.status ?? 1);
'
  SETUP_EXIT_CODE=$?
  if [ "$SETUP_EXIT_CODE" -ne 0 ]; then
    printf '\n---KELOS_SETUP_COMMAND_FAILED--- exit=%s\n' "$SETUP_EXIT_CODE" >&2
    exit "$SETUP_EXIT_CODE"
  fi
  printf '\n---KELOS_SETUP_COMMAND_DONE---\n' >&2
fi

gemini "${ARGS[@]}" | /kelos/kelos-capture
AGENT_EXIT_CODE=${PIPESTATUS[0]}

exit $AGENT_EXIT_CODE
