#!/usr/bin/env bash
# Run Tianwu from source on Linux/macOS (no build step required).
#   ./run.sh
#   AISCAN=/opt/cyber/aiscan-full ./run.sh
#   PROJECT=acme-web ./run.sh
set -euo pipefail

ADDR="${ADDR:-127.0.0.1:8080}"
DATA="${DATA:-./data}"
PROJECT="${PROJECT:-}"
AISCAN="${AISCAN:-}"

if ! command -v go >/dev/null 2>&1; then
  echo "go not found. Install Go 1.22+ first." >&2
  exit 1
fi

args=(run ./cmd/tw --addr "$ADDR" --data "$DATA")
if [ -n "$PROJECT" ]; then
  args+=(--project "$PROJECT")
fi
if [ -n "$AISCAN" ]; then
  args+=(--aiscan "$AISCAN")
fi

echo "tianwu -> http://$ADDR"
exec go "${args[@]}"
