#!/usr/bin/env bash
set -uo pipefail

senpi_dir="${SENPI_CODING_AGENT_DIR:-$HOME/.senpi/agent}"
mkdir -p "$senpi_dir"

if [[ -z "${SENPI_PROVIDER:-}" && "${KELOS_MODEL:-}" == kimi/* ]]; then
  export SENPI_PROVIDER=kimi
fi

if [[ "${SENPI_PROVIDER:-}" == "kimi" && ! -f "$senpi_dir/models.json" ]]; then
  python3 - "$senpi_dir/models.json" "${KELOS_MODEL:-kimi/kimi-k2.5}" "${SENPI_BASE_URL:-https://api.moonshot.ai/v1}" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
model = sys.argv[2].split("/", 1)[-1]
base_url = sys.argv[3]
config = {
    "providers": {
        "kimi": {
            "api": "openai-completions",
            "apiKey": "$SENPI_API_KEY",
            "baseUrl": base_url,
            "models": [{"id": model, "name": "Kimi"}],
        }
    }
}
path.write_text(json.dumps(config, indent=2) + "\n")
path.chmod(0o600)
PY
fi

if [[ -n "${KELOS_AGENTS_MD:-}" ]]; then
  printf '%s' "$KELOS_AGENTS_MD" >"$senpi_dir/AGENTS.md"
fi

if [[ -n "${KELOS_SETUP_COMMAND:-}" ]]; then
  python3 - "$KELOS_SETUP_COMMAND" <<'PY'
import json
import os
import subprocess
import sys

command = json.loads(sys.argv[1])
if not isinstance(command, list) or not all(isinstance(item, str) for item in command):
    raise SystemExit("KELOS_SETUP_COMMAND must decode to an array of strings")
result = subprocess.run(command, check=False)
raise SystemExit(result.returncode)
PY
fi

if [[ "${KELOS_SESSION_SETUP_ONLY:-}" == "1" ]]; then
  exit 0
fi

if [[ "$#" -lt 1 ]]; then
  echo "usage: kelos_entrypoint.sh PROMPT" >&2
  exit 2
fi

args=(--mode json --print)
if [[ -n "${KELOS_MODEL:-}" ]]; then
  model="$KELOS_MODEL"
  if [[ "${SENPI_PROVIDER:-}" == "kimi" && "$model" == kimi/* ]]; then
    model="${model#*/}"
  fi
  args+=(--model "$model")
fi
if [[ -n "${KELOS_EFFORT:-}" ]]; then
  args+=(--thinking "$KELOS_EFFORT")
fi
if [[ -n "${SENPI_PROVIDER:-}" ]]; then
  args+=(--provider "$SENPI_PROVIDER")
fi
if [[ -n "${SENPI_API_KEY:-}" ]]; then
  args+=(--api-key "$SENPI_API_KEY")
fi
args+=("$1")

senpi "${args[@]}" | /kelos/kelos-capture
statuses=("${PIPESTATUS[@]}")
agent_exit_code=${statuses[0]}
capture_exit_code=${statuses[1]}

if [[ "$agent_exit_code" -ne 0 ]]; then
  exit "$agent_exit_code"
fi
exit "$capture_exit_code"
