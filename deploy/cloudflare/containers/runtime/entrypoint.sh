#!/usr/bin/env bash
set -euo pipefail

# ──────────────────────────────────────────────────────────────────────────
# Container Entrypoint: xconnect-edge-agent + xray (XHTTP) + xray (TCP)
#
# Runs three long-lived processes under tini:
#   1. agent-healthz         — HTTP health/readiness check server
#   2. xray (XHTTP)          — VLESS+XHTTP inbound on /dev/shm/xray.sock
#   3. xray (TCP)            — VLESS+TCP+TLS inbound on port 1443
#   4. xconnect-edge-agent        — agent control plane (config sync, heartbeat)
#
# Configurable environment variables:
#   AGENT_REGION          — agent.region (deployment region code)
#   AGENT_POOL            — agent.pool (deployment pool identifier)
#   AGENT_ID              — agent.id           (fallback: DOMAIN)
#   AGENT_CONTROLLER_URL  — agent.controllerUrl (fallback: CONTROLLER_URL)
#   AGENT_API_TOKEN       — agent.apiToken      (fallback: INTERNAL_SERVICE_TOKEN)
# ──────────────────────────────────────────────────────────────────────────

AGENT_CONFIG_TEMPLATE="${AGENT_CONFIG_TEMPLATE:-/etc/agent/account-agent.container.yaml}"
AGENT_CONFIG_PATH="${AGENT_CONFIG_PATH:-/etc/agent/account-agent.yaml}"
XRAY_CONFIG_PATH="${XRAY_CONFIG_PATH:-/usr/local/etc/xray/config.json}"
XRAY_TCP_CONFIG_PATH="${XRAY_TCP_CONFIG_PATH:-/usr/local/etc/xray/tcp-config.json}"
XRAY_BOOTSTRAP_PATH="${XRAY_BOOTSTRAP_PATH:-/etc/agent/xray.bootstrap.json}"
XRAY_TCP_BOOTSTRAP_PATH="${XRAY_TCP_BOOTSTRAP_PATH:-/etc/agent/xray-tcp.bootstrap.json}"
XRAY_PID_FILE="${XRAY_PID_FILE:-/var/run/xconnect-edge-agent/xray.pid}"
XRAY_TCP_PID_FILE="${XRAY_TCP_PID_FILE:-/var/run/xconnect-edge-agent/xray-tcp.pid}"
AGENT_PID_FILE="${AGENT_PID_FILE:-/var/run/xconnect-edge-agent/agent.pid}"
XRAY_STOP_FILE="${XRAY_STOP_FILE:-/var/run/xconnect-edge-agent/xray.stop}"
HEALTH_PORT="${HEALTH_PORT:-8080}"
AGENT_ID="${AGENT_ID:-${DOMAIN:-}}"
AGENT_CONTROLLER_URL="${AGENT_CONTROLLER_URL:-${CONTROLLER_URL:-}}"
AGENT_API_TOKEN="${AGENT_API_TOKEN:-${INTERNAL_SERVICE_TOKEN:-}}"

mkdir -p /etc/agent /usr/local/etc/xray /usr/local/etc/xray/templates /var/run/xconnect-edge-agent
rm -f "$XRAY_STOP_FILE"

# ── Copy config templates if not yet rendered ──

if [ ! -f "$AGENT_CONFIG_PATH" ]; then
  cp "$AGENT_CONFIG_TEMPLATE" "$AGENT_CONFIG_PATH"
fi

if [ ! -s "$XRAY_CONFIG_PATH" ]; then
  cp "$XRAY_BOOTSTRAP_PATH" "$XRAY_CONFIG_PATH"
fi

if [ ! -s "$XRAY_TCP_CONFIG_PATH" ]; then
  cp "$XRAY_TCP_BOOTSTRAP_PATH" "$XRAY_TCP_CONFIG_PATH"
fi

# ── Inject configurable variables into agent YAML ──

update_yaml_value() {
  local key="$1"
  local value="$2"
  local file="$3"
  local escaped
  escaped="$(printf '%s' "$value" | sed -e 's/[\/&]/\\&/g')"
  if grep -qE "^[[:space:]]*${key}:" "$file"; then
    sed -i -E "s|^([[:space:]]*${key}:[[:space:]]*).*$|\\1\"${escaped}\"|g" "$file"
  else
    sed -i "/^agent:[[:space:]]*$/a\\  ${key}: \"${escaped}\"" "$file"
  fi
}

if [ -n "${AGENT_ID}" ]; then
  update_yaml_value "id" "$AGENT_ID" "$AGENT_CONFIG_PATH"
fi
if [ -n "${AGENT_CONTROLLER_URL}" ]; then
  update_yaml_value "controllerUrl" "$AGENT_CONTROLLER_URL" "$AGENT_CONFIG_PATH"
fi
if [ -n "${AGENT_API_TOKEN}" ]; then
  update_yaml_value "apiToken" "$AGENT_API_TOKEN" "$AGENT_CONFIG_PATH"
fi

if [ -n "${AGENT_REGION:-}" ]; then
  update_yaml_value "region" "$AGENT_REGION" "$AGENT_CONFIG_PATH"
fi
if [ -n "${AGENT_POOL:-}" ]; then
  update_yaml_value "pool" "$AGENT_POOL" "$AGENT_CONFIG_PATH"
fi

# ── Xray restart loops ──
# Each loop runs xray, watches for exit, and restarts unless stop file exists.
# The agent triggers restarts via restart-xray.sh → kill PID → loop auto-restarts.

xray_loop() {
  local config_path="$1"
  local pid_file="$2"
  local label="$3"

  while true; do
    echo "[entrypoint] Starting xray ($label): $config_path"
    /usr/local/bin/xray run -config "$config_path" &
    local xray_pid=$!
    echo "$xray_pid" > "$pid_file"
    wait "$xray_pid" || true

    if [ -f "$XRAY_STOP_FILE" ]; then
      echo "[entrypoint] xray ($label) stop requested, exiting loop"
      break
    fi
    echo "[entrypoint] xray ($label) exited, restarting in 1s..."
    sleep 1
  done
}

# ── Start all processes ──

echo "╔══════════════════════════════════════════════════════╗"
echo "║  xconnect-edge-agent Container Runtime                   ║"
echo "╠══════════════════════════════════════════════════════╣"
echo "║  Agent ID:      ${AGENT_ID:-<not set>}"
echo "║  Controller:    ${AGENT_CONTROLLER_URL:-<not set>}"
echo "║  Health Port:   ${HEALTH_PORT}"
echo "║  XHTTP Config:  ${XRAY_CONFIG_PATH}"
echo "║  TCP Config:    ${XRAY_TCP_CONFIG_PATH}"
echo "╚══════════════════════════════════════════════════════╝"

# 1. Health check server
/usr/local/bin/agent-healthz -listen ":${HEALTH_PORT}" &
HEALTH_PID=$!

# 2. Xray XHTTP instance (listen on Unix socket, proxied by Worker)
xray_loop "$XRAY_CONFIG_PATH" "$XRAY_PID_FILE" "xhttp" &
XRAY_LOOP_PID=$!

# 3. Xray TCP instance (listen on port 1443, direct TCP/TLS)
xray_loop "$XRAY_TCP_CONFIG_PATH" "$XRAY_TCP_PID_FILE" "tcp" &
XRAY_TCP_LOOP_PID=$!

# 4. Agent control plane
/usr/local/bin/xconnect-edge-agent -config "$AGENT_CONFIG_PATH" &
AGENT_PID=$!
echo "$AGENT_PID" > "$AGENT_PID_FILE"

# ── Graceful shutdown ──

shutdown() {
  echo "[entrypoint] Shutting down..."
  touch "$XRAY_STOP_FILE"

  # Stop xray instances
  if [ -s "$XRAY_PID_FILE" ]; then
    kill "$(cat "$XRAY_PID_FILE")" 2>/dev/null || true
  fi
  if [ -s "$XRAY_TCP_PID_FILE" ]; then
    kill "$(cat "$XRAY_TCP_PID_FILE")" 2>/dev/null || true
  fi

  # Stop agent and health server
  kill "$AGENT_PID" 2>/dev/null || true
  kill "$XRAY_LOOP_PID" 2>/dev/null || true
  kill "$XRAY_TCP_LOOP_PID" 2>/dev/null || true
  kill "$HEALTH_PID" 2>/dev/null || true
}

trap shutdown SIGINT SIGTERM

# Wait for any child to exit — if one dies, bring everything down
set +e
wait -n "$AGENT_PID" "$XRAY_LOOP_PID" "$XRAY_TCP_LOOP_PID" "$HEALTH_PID"
EXIT_CODE=$?
set -e

shutdown
wait || true
exit "$EXIT_CODE"
