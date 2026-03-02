#!/usr/bin/env bash
set -euo pipefail

if [ $# -lt 1 ]; then
  echo "Usage: ./scripts/index.sh <config-path> [flags]"
  echo
  echo "Examples:"
  echo "  ./scripts/index.sh configs/my-project.yaml"
  echo "  ./scripts/index.sh configs/my-project.yaml --reindex"
  exit 1
fi

CONFIG="$1"
shift

if [ ! -f "$CONFIG" ]; then
  echo "Error: config file not found: $CONFIG"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
INDEXER="$ROOT_DIR/bin/codebase-intel-indexer"

if [ ! -x "$INDEXER" ]; then
  echo "Indexer binary not found. Building..."
  (cd "$ROOT_DIR" && go build -o ./bin/codebase-intel-indexer ./cmd/indexer)
fi

# Load credentials
if [ -f "$ROOT_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  source "$ROOT_DIR/.env"
  set +a
fi

exec "$INDEXER" -config "$CONFIG" "$@"
