#!/usr/bin/env bash
set -Eeuo pipefail

export AUTH_URL="https://accounts.svc.plus"
export BILLING_SERVICE_URL="https://billing.svc.plus"
export AGENT_PROXY_DOMAIN="jp-xconnect-01.svc.plus"
export AGENT_ENTRY_POINT="$AGENT_PROXY_DOMAIN"
export AGENT_REGION="JP"
export AGENT_POOL="jp-main"
export AGENT_TEAM_ID=""
export AGENT_RESOURCE_CLASS="plus"
export AGENT_CUSTOM_EMAILS='["805042070@qq.com"]'

export VAULT_ADDR="https://vault.svc.plus"
export VAULT_TLS_SECRET_PATH="kv/data/CICD/domains/svc.plus"
export AGENT_PROXY_VECTOR_VAULT_KV_PATH="kv/data/CICD/observability"
export INTERNAL_SERVICE_TOKEN_VAULT_KV_PATH="kv/data/prod/accounts/runtime"

# Do not store Vault tokens or service credentials in this file.
if [[ -z "${VAULT_TOKEN:-}" ]]; then
  if [[ ! -r /dev/tty ]]; then
    echo "VAULT_TOKEN is unset and no interactive terminal is available." >&2
    exit 1
  fi
  read -rsp "Vault token: " VAULT_TOKEN < /dev/tty
  printf '\n' > /dev/tty
  export VAULT_TOKEN
fi

setup_script=""
cleanup() {
  if [[ -n "$setup_script" ]]; then
    rm -f "$setup_script"
  fi
  unset INTERNAL_SERVICE_TOKEN VAULT_TOKEN
}
trap cleanup EXIT

setup_script="$(mktemp /tmp/xconnect-setup-proxy.XXXXXX.sh)"
curl --fail --silent --show-error --location --retry 3 \
  --output "$setup_script" \
  https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh
bash "$setup_script" --node "$AGENT_PROXY_DOMAIN" --with-observability
