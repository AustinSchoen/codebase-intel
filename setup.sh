#!/usr/bin/env bash
set -euo pipefail

echo "=== Codebase Intelligence — Setup ==="
echo

# ── Prerequisites ──────────────────────────────────────────────

if ! command -v go &>/dev/null; then
  echo "Error: Go is required to build the indexer. Install it from https://go.dev/dl/"
  exit 1
fi

if ! command -v docker &>/dev/null; then
  echo "Error: Docker is required. Install it from https://docs.docker.com/get-docker/"
  exit 1
fi

if ! docker compose version &>/dev/null 2>&1; then
  echo "Error: docker compose (v2) is required."
  exit 1
fi

# ── Voyage API Key ─────────────────────────────────────────────

if [ -f .env ]; then
  echo "Found existing .env file."
  # shellcheck disable=SC1091
  source .env
fi

if [ -z "${VOYAGE_API_KEY:-}" ]; then
  echo "A Voyage AI API key is required for code embeddings."
  echo "Get one at: https://dash.voyageai.com/api-keys"
  echo
  read -rp "Voyage API key: " VOYAGE_API_KEY
  if [ -z "$VOYAGE_API_KEY" ]; then
    echo "Error: Voyage API key cannot be empty."
    exit 1
  fi
fi

# ── Generate secrets ───────────────────────────────────────────

QDRANT_API_KEY="${QDRANT_API_KEY:-$(openssl rand -hex 32)}"
CI_PG_USER="${CI_PG_USER:-codebase_intel}"
CI_PG_PASSWORD="${CI_PG_PASSWORD:-$(openssl rand -hex 32)}"
MCP_API_KEY="${MCP_API_KEY:-$(openssl rand -hex 32)}"

# ── Write .env ─────────────────────────────────────────────────

cat > .env <<EOF
VOYAGE_API_KEY=${VOYAGE_API_KEY}
QDRANT_API_KEY=${QDRANT_API_KEY}
CI_PG_USER=${CI_PG_USER}
CI_PG_PASSWORD=${CI_PG_PASSWORD}
MCP_API_KEY=${MCP_API_KEY}
EOF

echo "Wrote .env"

# ── Write server config ───────────────────────────────────────

mkdir -p configs

cat > configs/server.yaml <<EOF
embeddings:
  api_key_env: VOYAGE_API_KEY

vector_store:
  url: http://qdrant:6333
  api_key_env: QDRANT_API_KEY

metadata_store:
  host: postgres
  port: 5432
  database: codebase_intel
  user_env: CI_PG_USER
  password_env: CI_PG_PASSWORD

server:
  api_key_env: MCP_API_KEY
EOF

echo "Wrote configs/server.yaml"

# ── Start services ─────────────────────────────────────────────

echo
echo "Starting services..."
docker compose up -d --build

# ── Build indexer ──────────────────────────────────────────────

echo
echo "Building indexer..."
mkdir -p bin
go build -o ./bin/codebase-intel-indexer ./cmd/indexer
echo "Built ./bin/codebase-intel-indexer"

# ── Wait for healthy services ──────────────────────────────────

echo
echo "Waiting for services to be healthy..."
for i in $(seq 1 30); do
  if curl -sf http://localhost:8090/health >/dev/null 2>&1; then
    echo "Server is healthy."
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "Warning: Server did not become healthy within 30s. Check: docker compose logs server"
    exit 1
  fi
  sleep 1
done

# ── Run migrations ─────────────────────────────────────────────

echo
echo "Running database migrations..."
# shellcheck disable=SC1091
source .env
export VOYAGE_API_KEY CI_PG_USER CI_PG_PASSWORD QDRANT_API_KEY

./bin/codebase-intel-indexer \
  -config configs/example-codebase.yaml \
  -migrate

# ── Done ───────────────────────────────────────────────────────

echo
echo "=== Setup complete! ==="
echo
echo "Next steps:"
echo
echo "1. Create a codebase config (see configs/example-codebase.yaml):"
echo
echo "   cp configs/example-codebase.yaml configs/my-project.yaml"
echo "   # Edit: set codebase path, name, and languages"
echo
echo "2. Index your codebase:"
echo
echo "   ./scripts/index.sh configs/my-project.yaml"
echo
echo "3. Add to Claude Code:"
echo
echo "   claude mcp add codebase-intel \\"
echo "     --transport http \\"
echo "     --url http://localhost:8090/mcp \\"
echo "     --header \"Authorization: Bearer ${MCP_API_KEY}\""
echo
echo "4. Verify it works:"
echo
echo "   curl -s http://localhost:8090/health"
echo "   curl -s http://localhost:8090/mcp \\"
echo "     -H 'Content-Type: application/json' \\"
echo "     -H 'Authorization: Bearer ${MCP_API_KEY}' \\"
echo "     -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}'"
echo
