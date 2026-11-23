#!/usr/bin/env bash
set -euo pipefail
payload="$(cat)"
path="$(printf '%s' "$payload" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input", {}).get("file_path", "").replace("\\\\", "/"))')"
if [[ "$path" == */go.sum ]]; then
  echo "Blocked generated Go checksum file: $path (use go mod tidy)" >&2
  exit 2
fi
