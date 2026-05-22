#!/usr/bin/env bash
#
# setup-indexer.sh — frictionless indexer-host setup
#
# Discovers the codebase-intel server on the LAN via mDNS, prompts for codebase
# paths, writes per-host configs under ~/.config/codebase-intel/, installs a
# user service (launchd on macOS, systemd --user on Linux), and starts it.
#
# Usage:
#   ./setup-indexer.sh                          # interactive
#   ./setup-indexer.sh --server URL --token T   # skip discovery
#
# See issue #15 for the design.

set -euo pipefail

# ── argument parsing ──────────────────────────────────────────────────

SERVER_URL=""
SERVER_TOKEN=""

while [ $# -gt 0 ]; do
  case "$1" in
    --server)
      SERVER_URL="$2"
      shift 2
      ;;
    --token)
      SERVER_TOKEN="$2"
      shift 2
      ;;
    -h|--help)
      cat <<'USAGE'
setup-indexer.sh — frictionless indexer-host setup

Discovers the codebase-intel server on the LAN via mDNS, prompts for codebase
paths, writes per-host configs under ~/.config/codebase-intel/, installs a
user service (launchd on macOS, systemd --user on Linux), and starts it.

Usage:
  ./setup-indexer.sh                          interactive (mDNS discovery + prompts)
  ./setup-indexer.sh --server URL --token T   skip discovery (manual entry)
USAGE
      exit 0
      ;;
    *)
      echo "Unknown flag: $1" >&2
      exit 1
      ;;
  esac
done

# ── prerequisites ─────────────────────────────────────────────────────

if ! command -v go &>/dev/null; then
  echo "Error: Go is required to build the indexer. Install from https://go.dev/dl/" >&2
  exit 1
fi
if ! command -v python3 &>/dev/null; then
  echo "Error: python3 is required (used for JSON parsing). Install Xcode CLI tools on macOS or python3 on Linux." >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INDEXER="$SCRIPT_DIR/bin/codebase-intel-indexer"

if [ ! -x "$INDEXER" ]; then
  echo "Building indexer binary…"
  (cd "$SCRIPT_DIR" && go build -o ./bin/codebase-intel-indexer ./cmd/indexer)
fi

# ── discover server (if not provided) ─────────────────────────────────

if [ -z "$SERVER_URL" ]; then
  echo "Scanning LAN for a codebase-intel server (3s)…"
  DISCOVERY_JSON="$("$INDEXER" -discover 2>/dev/null || echo '{"services":[]}')"
  SERVICE_COUNT="$(echo "$DISCOVERY_JSON" | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("services",[])))')"

  if [ "$SERVICE_COUNT" = "0" ]; then
    echo
    echo "No codebase-intel server found on the LAN."
    echo "Either mDNS is blocked on this network, or the server isn't running with mDNS advertising enabled."
    echo
    read -rp "Server URL (e.g., http://192.168.1.10:8090): " SERVER_URL
    if [ -z "$SERVER_TOKEN" ]; then
      read -rsp "Bearer token: " SERVER_TOKEN
      echo
    fi
  else
    if [ "$SERVICE_COUNT" = "1" ]; then
      # Python f-strings disallow backslash escapes inside the expression
      # portion, so use plain string concatenation here (avoids the bug
      # caught by PR #17's testing pass).
      eval "$(echo "$DISCOVERY_JSON" | python3 -c '
import json, sys, shlex
svc = json.load(sys.stdin)["services"][0]
print("DISCOVERED_URL=" + shlex.quote(svc["URL"]))
print("DISCOVERED_TOKEN=" + shlex.quote(svc.get("Token", "")))
print("DISCOVERED_NAME=" + shlex.quote(svc.get("InstanceName", "")))
')"
      echo "Found: $DISCOVERED_NAME at $DISCOVERED_URL"
    else
      echo "Multiple servers responded:"
      echo "$DISCOVERY_JSON" | python3 -c '
import json, sys
for i, svc in enumerate(json.load(sys.stdin).get("services", [])):
    print("  [" + str(i+1) + "] " + svc.get("InstanceName", "?") + " -> " + svc["URL"])
'
      read -rp "Select [1-$SERVICE_COUNT]: " choice
      eval "$(echo "$DISCOVERY_JSON" | CHOICE="$choice" python3 -c '
import json, os, sys, shlex
i = int(os.environ["CHOICE"]) - 1
svc = json.load(sys.stdin)["services"][i]
print("DISCOVERED_URL=" + shlex.quote(svc["URL"]))
print("DISCOVERED_TOKEN=" + shlex.quote(svc.get("Token", "")))
print("DISCOVERED_NAME=" + shlex.quote(svc.get("InstanceName", "")))
')"
    fi

    SERVER_URL="$DISCOVERED_URL"
    if [ -z "$SERVER_TOKEN" ]; then
      SERVER_TOKEN="$DISCOVERED_TOKEN"
    fi

    if [ -z "$SERVER_TOKEN" ]; then
      echo "The server didn't advertise a bearer token (server.discovery.advertise_token is false on the server side)."
      read -rsp "Bearer token: " SERVER_TOKEN
      echo
    fi
  fi
fi

if [ -z "$SERVER_URL" ] || [ -z "$SERVER_TOKEN" ]; then
  echo "Error: server URL and token are both required." >&2
  exit 1
fi

echo
echo "Using server: $SERVER_URL"

# ── codebase prompts ──────────────────────────────────────────────────

CONFIG_DIR="$HOME/.config/codebase-intel"
CODEBASES_D="$CONFIG_DIR/codebases.d"
mkdir -p "$CODEBASES_D"

echo
echo "Add codebases to index on this host. Enter an empty path to finish."

declare -a CODEBASE_NAMES=()

while true; do
  read -rp "Codebase path (absolute, or empty to finish): " CB_PATH
  if [ -z "$CB_PATH" ]; then
    break
  fi
  if [ ! -d "$CB_PATH" ]; then
    echo "  $CB_PATH is not a directory; skipping."
    continue
  fi

  CB_PATH="$(cd "$CB_PATH" && pwd)"

  DEFAULT_NAME="$(basename "$CB_PATH")"
  read -rp "  Name [$DEFAULT_NAME]: " CB_NAME
  CB_NAME="${CB_NAME:-$DEFAULT_NAME}"

  # Detect languages by extension. The list is intentionally conservative;
  # operators can edit the YAML afterwards to refine.
  LANGS="$(find "$CB_PATH" -type f \( \
    -name '*.go' -o -name '*.py' -o -name '*.ts' -o -name '*.tsx' \
    -o -name '*.js' -o -name '*.jsx' -o -name '*.rs' -o -name '*.c' \
    -o -name '*.h' -o -name '*.cpp' -o -name '*.cc' -o -name '*.hpp' \
    -o -name '*.kt' -o -name '*.kts' -o -name '*.swift' -o -name '*.dart' \) \
    -not -path '*/node_modules/*' -not -path '*/vendor/*' -not -path '*/.git/*' \
    2>/dev/null \
    | python3 -c '
import sys, os
ext_to_lang = {".go":"go",".py":"python",".ts":"typescript",".tsx":"typescript",
               ".js":"javascript",".jsx":"javascript",".rs":"rust",".c":"c",
               ".h":"c",".cpp":"cpp",".cc":"cpp",".hpp":"cpp",".kt":"kotlin",
               ".kts":"kotlin",".swift":"swift",".dart":"dart"}
langs = set()
for line in sys.stdin:
    ext = os.path.splitext(line.strip())[1]
    if ext in ext_to_lang:
        langs.add(ext_to_lang[ext])
print(", ".join(sorted(langs)))
')"

  if [ -z "$LANGS" ]; then
    echo "  No supported languages detected at $CB_PATH; skipping."
    continue
  fi

  echo "  Languages: $LANGS"

  CFG="$CODEBASES_D/$CB_NAME.yaml"
  cat > "$CFG" <<EOF
# Generated by setup-indexer.sh on $(date -Iseconds).
#
# Under the thin-client architecture (#18), the indexer daemon doesn't
# touch Qdrant or Postgres directly — it ships file content to the
# central MCP server, which runs the indexing pipeline using its own
# backend credentials. This config only carries what the daemon needs
# to walk the codebase tree.
codebase:
  path: $CB_PATH
  name: $CB_NAME
  languages: [$(echo "$LANGS" | sed 's/, /,/g')]
  exclude_patterns:
    - "**/node_modules/**"
    - "**/vendor/**"
    - "**/.git/**"
    - "**/dist/**"
    - "**/build/**"

indexing:
  incremental: true
EOF
  echo "  Wrote $CFG"
  CODEBASE_NAMES+=("$CB_NAME")
done

if [ "${#CODEBASE_NAMES[@]}" -eq 0 ]; then
  echo "No codebases added — exiting without installing a service." >&2
  exit 1
fi

# ── write credentials.env + daemon.yaml ───────────────────────────────

CRED_FILE="$CONFIG_DIR/credentials.env"
DAEMON_CFG="$CONFIG_DIR/daemon.yaml"

umask 077
cat > "$CRED_FILE" <<EOF
# Used by the codebase-intel daemon service unit. Mode 0600.
MCP_API_KEY=$SERVER_TOKEN
EOF
umask 022
echo "Wrote $CRED_FILE (mode 0600)"

cat > "$DAEMON_CFG" <<EOF
# Generated by setup-indexer.sh on $(date -Iseconds)
server_url: $SERVER_URL
node_id: $(hostname)
EOF
echo "Wrote $DAEMON_CFG"

# ── install service ───────────────────────────────────────────────────

INDEXER_INSTALLED="$HOME/bin/codebase-intel-indexer"
mkdir -p "$HOME/bin"
cp "$INDEXER" "$INDEXER_INSTALLED"
echo "Installed indexer binary to $INDEXER_INSTALLED"

case "$(uname -s)" in
  Darwin)
    PLIST="$HOME/Library/LaunchAgents/com.codebase-intel.daemon.plist"
    mkdir -p "$HOME/Library/LaunchAgents"
    cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>com.codebase-intel.daemon</string>

    <key>ProgramArguments</key>
    <array>
      <string>$INDEXER_INSTALLED</string>
      <string>-daemon</string>
      <string>-config-dir</string>
      <string>$CODEBASES_D</string>
      <string>-server-url</string>
      <string>$SERVER_URL</string>
      <string>-node-id</string>
      <string>$(hostname)</string>
    </array>

    <key>EnvironmentVariables</key>
    <dict>
      <key>MCP_API_KEY</key>
      <string>$SERVER_TOKEN</string>
    </dict>

    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>

    <key>StandardOutPath</key>
    <string>$HOME/Library/Logs/codebase-intel-daemon.log</string>
    <key>StandardErrorPath</key>
    <string>$HOME/Library/Logs/codebase-intel-daemon.log</string>
  </dict>
</plist>
EOF
    echo "Wrote $PLIST"
    launchctl unload "$PLIST" 2>/dev/null || true
    launchctl load "$PLIST"
    echo "Daemon loaded via launchctl. Logs: ~/Library/Logs/codebase-intel-daemon.log"
    ;;

  Linux)
    UNIT_DIR="$HOME/.config/systemd/user"
    mkdir -p "$UNIT_DIR"
    UNIT="$UNIT_DIR/codebase-intel-daemon.service"
    cat > "$UNIT" <<EOF
[Unit]
Description=codebase-intel indexer daemon
After=network-online.target

[Service]
Type=simple
EnvironmentFile=$CRED_FILE
ExecStart=$INDEXER_INSTALLED -daemon -config-dir $CODEBASES_D -server-url $SERVER_URL -node-id %H
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=default.target
EOF
    echo "Wrote $UNIT"
    systemctl --user daemon-reload
    systemctl --user enable --now codebase-intel-daemon.service
    echo "Daemon enabled + started. Logs: journalctl --user -u codebase-intel-daemon -f"
    ;;

  *)
    echo "Unsupported OS for automatic service install: $(uname -s)"
    echo "Run manually:"
    echo "  $INDEXER_INSTALLED -daemon -config-dir $CODEBASES_D -server-url $SERVER_URL -node-id $(hostname)"
    echo "  (with MCP_API_KEY=$SERVER_TOKEN in the environment)"
    exit 1
    ;;
esac

echo
echo "=== Setup complete ==="
echo "Codebases configured: ${CODEBASE_NAMES[*]}"
echo "Add more later: drop a *.yaml in $CODEBASES_D and restart the service."
