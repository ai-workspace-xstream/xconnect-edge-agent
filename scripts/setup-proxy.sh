#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat <<'EOF'
Usage: setup-proxy.sh [--operation deploy|standalone|upgrade|print-arch] [options]
Options: --node DOMAIN --entry-point DOMAIN --region CODE --pool NAME
         --resource-class CLASS --team-id ID --with-observability
         --open-stunnel-5443 -h|--help
Inputs may also be provided through the corresponding AGENT_* environment variables.
Vault credentials must be supplied in the process environment and are never persisted.
EOF
}

die() { printf 'setup-proxy: %s\n' "$1" >&2; exit 2; }
operation="${AGENT_PROXY_OPERATION:-deploy}"
domain="${AGENT_PROXY_DOMAIN:-}"
entry="${AGENT_ENTRY_POINT:-}"
region="${AGENT_REGION:-}"
pool="${AGENT_POOL:-}"
class="${AGENT_RESOURCE_CLASS:-shared}"
team="${AGENT_TEAM_ID:-}"
observability="${INSTALL_OBSERVABILITY:-false}"
stunnel="${OPEN_STUNNEL_5443:-false}"

while (($#)); do
  case "$1" in
    --operation) (($# > 1)) || die "$1 requires a value"; operation="$2"; shift 2 ;;
    --operation=*) operation="${1#*=}"; shift ;;
    --node) (($# > 1)) || die "$1 requires a value"; domain="$2"; shift 2 ;;
    --node=*) domain="${1#*=}"; shift ;;
    --entry-point) (($# > 1)) || die "$1 requires a value"; entry="$2"; shift 2 ;;
    --entry-point=*) entry="${1#*=}"; shift ;;
    --region) (($# > 1)) || die "$1 requires a value"; region="$2"; shift 2 ;;
    --region=*) region="${1#*=}"; shift ;;
    --pool) (($# > 1)) || die "$1 requires a value"; pool="$2"; shift 2 ;;
    --pool=*) pool="${1#*=}"; shift ;;
    --resource-class) (($# > 1)) || die "$1 requires a value"; class="$2"; shift 2 ;;
    --resource-class=*) class="${1#*=}"; shift ;;
    --team-id) (($# > 1)) || die "$1 requires a value"; team="$2"; shift 2 ;;
    --team-id=*) team="${1#*=}"; shift ;;
    --with-observability) observability=true; shift ;;
    --open-stunnel-5443) stunnel=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

playbooks_ref="${AGENT_PLAYBOOKS_REF:-main}"
[[ "$playbooks_ref" =~ ^([a-f0-9]{40}|main)$ ]] || die 'AGENT_PLAYBOOKS_REF must be main or a full commit SHA'
command -v curl >/dev/null && command -v tar >/dev/null || die 'curl and tar are required'
if ! command -v ansible-playbook >/dev/null; then
  command -v apt-get >/dev/null || die 'Ansible Core is required and apt-get is unavailable'
  apt-get update && apt-get install -y ansible-core
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/xconnect-playbooks.XXXXXX")"
trap 'rm -rf "$work"' EXIT
chmod 0700 "$work"
curl -fsSL --retry 3 --connect-timeout 15 --max-time 180 \
  "https://github.com/ai-workspace-infra/playbooks/archive/${playbooks_ref}.tar.gz" \
  | tar -xz -C "$work" --strip-components=1
printf '[agent_proxy]\nlocalhost ansible_connection=local\n[xray_exporter]\nlocalhost ansible_connection=local\n[observability_agent]\nlocalhost ansible_connection=local\n' > "$work/inventory.ini"
chmod 0600 "$work/inventory.ini"
printf '[defaults]\nretry_files_enabled = False\n' > "$work/ansible.cfg"

export AGENT_ID="${AGENT_ID:-${domain:-$(hostname -f 2>/dev/null || hostname)}}"
export AGENT_PROXY_DOMAIN="${domain:-$AGENT_ID}" AGENT_ENTRY_POINT="${entry:-${domain:-$AGENT_ID}}"
export AGENT_REGION="$region" AGENT_POOL="$pool" AGENT_RESOURCE_CLASS="$class" AGENT_TEAM_ID="$team"
export AGENT_PROXY_OPERATION="$operation" AGENT_PROXY_OPEN_STUNNEL_5443="$stunnel"
export AGENT_PROXY_OBSERVABILITY_ENABLED="$observability"
export AGENT_CONTROLLER_URL="${AGENT_CONTROLLER_URL:-${AUTH_URL:-${ACCOUNTS_URL:-}}}"
export BILLING_SERVICE_BASE_URL="${BILLING_SERVICE_BASE_URL:-${BILLING_SERVICE_URL:-}}"
export AGENT_REPO_URL="${AGENT_REPO_URL:-https://github.com/${GITHUB_REPO:-ai-workspace-xstream/xconnect-edge-agent}.git}"
[[ -z "${VAULT_TLS_SECRET_PATH:-}" ]] || export AGENT_PROXY_VAULT_TLS_ENABLED=true

if [[ "$observability" == true || "$observability" == 1 || "$observability" == yes ]]; then
  export VECTOR_BILLING_INGEST_ENABLED=true
  export VECTOR_BILLING_INGEST_URL="${VECTOR_BILLING_INGEST_URL:-${BILLING_SERVICE_BASE_URL%/}/v1/ingest/snapshots}"
  export VECTOR_SNAPSHOT_URL="${VECTOR_SNAPSHOT_URL:-http://127.0.0.1:8686}"
fi
ANSIBLE_CONFIG="$work/ansible.cfg" ansible-playbook "$work/deploy_xray_proxy_server.yml" \
  -i "$work/inventory.ini" -e 'agent_service_hosts=localhost agent_proxy_local_installer=true'
