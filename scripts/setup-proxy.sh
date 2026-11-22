#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat <<'EOF'
Usage: setup-proxy.sh [--operation deploy|standalone|upgrade|print-arch] [options]
Options: --node DOMAIN --entry-point DOMAIN --region CODE --pool NAME
         --resource-class CLASS --team-id ID --proxy-engine ENGINE
         --gateway-domain DOMAIN --gateway-socket PATH
         --runtime systemd|container [--container|--non-container]
         --with-observability --open-stunnel-5443 -h|--help
Engines: fluxgate (default) | haproxy | caddy
Runtimes: systemd (default, bare-metal service) | container (Kubernetes Helm chart)
Inputs may also be provided through the corresponding AGENT_* or XCONNECT_GATEWAY_* environment variables.
Vault credentials must be supplied in the process environment and are never persisted.
EOF
}

die() { printf 'setup-proxy: %s\n' "$1" >&2; exit 2; }

# --- 自我更新 --------------------------------------------------------------
# 这个脚本是通过 `curl ... | bash` 从 raw.githubusercontent.com 拿到的, 而那
# 是 CDN, 按分支缓存且不保证最新 —— 实测 main 已合并新提交后, 分支地址仍返回
# 旧内容, 只有按 SHA 取才拿到新的。也就是说: 不改脚本的话, 节点可能在跑几天
# 前的逻辑, 而且完全无法察觉。
#
# 所以脚本每次启动都重新取一次自身:
#   - 默认取 main 分支, 带 Cache-Control: no-cache 绕过 CDN 缓存
#   - 指定 AGENT_SETUP_PROXY_REF=<tag|sha> 时按该 ref 取(不可变, 可复现)
#   - 拿到的内容与本文件不同 -> 用最新版本 re-exec, 本次进程不再继续
#   - 取不到(离线/网络故障) -> 用当前版本继续, 只提示, 不阻断部署
#
# 关闭方式: AGENT_SETUP_PROXY_SELF_UPDATE=false
SETUP_PROXY_SOURCE_REPO="ai-workspace-xstream/xconnect-edge-agent"
SETUP_PROXY_REF="${AGENT_SETUP_PROXY_REF:-main}"
setup_proxy_self_update() {
  [[ "${AGENT_SETUP_PROXY_SELF_UPDATE:-true}" == "true" ]] || return 0
  # 已经是更新后的副本就不再递归
  [[ "${AGENT_SETUP_PROXY_UPDATED:-}" == "1" ]] && return 0
  command -v curl >/dev/null 2>&1 || return 0

  local url body tmp current
  # ref 为 40 位十六进制时是 commit SHA, 内容不可变; 否则是分支或 tag,
  # 必须显式禁用缓存, 否则 CDN 会返回陈旧副本。
  if [[ "$SETUP_PROXY_REF" =~ ^[0-9a-f]{40}$ ]]; then
    url="https://raw.githubusercontent.com/$SETUP_PROXY_SOURCE_REPO/$SETUP_PROXY_REF/scripts/setup-proxy.sh"
  else
    url="https://raw.githubusercontent.com/$SETUP_PROXY_SOURCE_REPO/$SETUP_PROXY_REF/scripts/setup-proxy.sh?nocache=$(date +%s)-$RANDOM"
  fi

  body="$(curl -fsSL --max-time 30 -H "Cache-Control: no-cache" -H "Pragma: no-cache" "$url" 2>/dev/null || true)"
  [[ -n "$body" ]] || { printf "setup-proxy: cannot fetch latest, continuing with current copy\n" >&2; return 0; }

  current="$(cat "${BASH_SOURCE[0]}" 2>/dev/null || true)"
  [[ "$body" == "$current" ]] && return 0

  printf "setup-proxy: update detected (ref=$SETUP_PROXY_REF), re-executing latest\n" >&2
  tmp="$(mktemp "${TMPDIR:-/tmp}/setup-proxy.XXXXXX")"
  printf "%s" "$body" > "$tmp"
  chmod 0700 "$tmp"
  AGENT_SETUP_PROXY_UPDATED=1 exec bash "$tmp" ${1+"$@"}
}
setup_proxy_self_update "$@"
operation="${AGENT_PROXY_OPERATION:-deploy}"
domain="${AGENT_PROXY_DOMAIN:-}"
entry="${AGENT_ENTRY_POINT:-}"
region="${AGENT_REGION:-}"
pool="${AGENT_POOL:-}"
class="${AGENT_RESOURCE_CLASS:-shared}"
team="${AGENT_TEAM_ID:-}"
proxy_engine="${AGENT_PROXY_ENGINE:-fluxgate}"
runtime="${AGENT_RUNTIME:-systemd}"
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
    --proxy-engine) (($# > 1)) || die "$1 requires a value"; proxy_engine="$2"; shift 2 ;;
    --proxy-engine=*) proxy_engine="${1#*=}"; shift ;;
    --runtime) (($# > 1)) || die "$1 requires a value"; runtime="$2"; shift 2 ;;
    --runtime=*) runtime="${1#*=}"; shift ;;
    --container) runtime="container"; shift ;;
    --non-container) runtime="systemd"; shift ;;
    --with-observability) observability=true; shift ;;
    --open-stunnel-5443) stunnel=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

playbooks_ref="${AGENT_PLAYBOOKS_REF:-}"
gitops_ref="${AGENT_GITOPS_REF:-}"
if [[ -z "$playbooks_ref" ]]; then
  playbooks_ref="$(git ls-remote https://github.com/ai-workspace-infra/playbooks.git HEAD 2>/dev/null | awk '{print $1}' || echo "")"
fi
if [[ -z "$gitops_ref" ]]; then
  gitops_ref="$(git ls-remote https://github.com/ai-workspace-infra/gitops.git HEAD 2>/dev/null | awk '{print $1}' || echo "")"
fi
[[ "$playbooks_ref" =~ ^[a-f0-9]{40}$ ]] || die 'AGENT_PLAYBOOKS_REF must be a full commit SHA'
[[ "$gitops_ref" =~ ^[a-f0-9]{40}$ ]] || die 'AGENT_GITOPS_REF must be a full commit SHA'
# --- runtime dispatch -------------------------------------------------------
# 两种运行形态互斥, 依赖集完全不同:
#
#   systemd  (默认) 裸机 systemd 服务。只需要 curl / tar / systemd / ansible-core,
#                   完全不依赖 docker、containerd、k3s、flux 或任何编排组件。
#                   适合边缘 VPS、最小化 Debian 主机。
#   container       交给 Kubernetes + Helm。只需要 helm 与可达的 apiserver,
#                   不需要在宿主机上装 ansible 或裸机服务。
#
# 默认走 systemd: 这是覆盖绝大多数边缘节点的路径, 也最容易在最小化系统上跑通。
case "$runtime" in
  systemd|container|k8s) ;;
  *) die "unsupported runtime: $runtime (expected systemd or container)" ;;
esac


export AGENT_ID="${AGENT_ID:-${domain:-$(hostname -f 2>/dev/null || hostname)}}"
[[ "$AGENT_ID" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]] || die 'AGENT_ID must be a valid hostname-compatible identifier'

export AGENT_PROXY_DOMAIN="${domain:-$AGENT_ID}" AGENT_ENTRY_POINT="${entry:-${domain:-$AGENT_ID}}"
export AGENT_REGION="$region" AGENT_POOL="$pool" AGENT_RESOURCE_CLASS="$class" AGENT_TEAM_ID="$team"
export AGENT_PROXY_OPERATION="$operation" AGENT_PROXY_OPEN_STUNNEL_5443="$stunnel"
export AGENT_PROXY_ENGINE="$proxy_engine"
export AGENT_PROXY_RUNTIME="$runtime"
export AGENT_PROXY_OBSERVABILITY_ENABLED="$observability"
export AGENT_CONTROLLER_URL="${AGENT_CONTROLLER_URL:-${AUTH_URL:-${ACCOUNTS_URL:-}}}"
export BILLING_SERVICE_BASE_URL="${BILLING_SERVICE_BASE_URL:-${BILLING_SERVICE_URL:-}}"
export AGENT_REPO_URL="${AGENT_REPO_URL:-https://github.com/${GITHUB_REPO:-ai-workspace-xstream/xconnect-edge-agent}.git}"
[[ -z "${VAULT_TLS_SECRET_PATH:-}" ]] || export AGENT_PROXY_VAULT_TLS_ENABLED=true

if [[ "$runtime" == "container" || "$runtime" == "k8s" ]]; then
  printf '==> Executing in container runtime (Kubernetes / Helm: xconnect-edge-proxy)\n'
  # 容器路径不碰宿主机 ansible/裸机服务, 只要求 helm 与可达的 apiserver。
  command -v helm >/dev/null || die 'helm is required for container runtime; install helm before using --container'
  command -v kubectl >/dev/null || die 'kubectl is required for container runtime; install kubectl before using --container'
  kubectl cluster-info >/dev/null 2>&1 || die 'no reachable Kubernetes cluster for container runtime'
  helm_args=(
    upgrade --install xconnect-edge-proxy
    "https://github.com/ai-workspace-infra/artifacts/raw/main/oci/charts/xconnect-edge-proxy-0.1.0.tgz"
    --namespace xconnect --create-namespace
    --set "global.nodeDomain=${AGENT_PROXY_DOMAIN}"
    --set "global.entryPoint=${AGENT_ENTRY_POINT}"
    --set "global.region=${AGENT_REGION}"
    --set "global.pool=${AGENT_POOL}"
    --set "global.resourceClass=${AGENT_RESOURCE_CLASS}"
    --set "ingress.provider=${AGENT_PROXY_ENGINE}"
    --set "observability.enabled=${observability}"
  )
  if [[ -n "${INTERNAL_SERVICE_TOKEN:-}" ]]; then
    helm_args+=(--set "security.internalServiceToken=${INTERNAL_SERVICE_TOKEN}")
  fi
  if [[ -n "${VAULT_TOKEN:-}" ]]; then
    helm_args+=(--set "security.vaultToken=${VAULT_TOKEN}")
  fi
  helm "${helm_args[@]}"
  printf '==> Containerized xconnect-edge-proxy deployment complete\n'
  exit 0
fi

# --- systemd (non-container) path ------------------------------------------
# 到这里才检查裸机依赖: 容器路径已经在上面 exit, 不会走到这里。
printf '==> Executing in systemd runtime (bare-metal service, no docker/k3s/flux required)\n'
command -v systemctl >/dev/null || die 'systemd is required for the non-container runtime'
command -v curl >/dev/null && command -v tar >/dev/null || die 'curl and tar are required'
if ! command -v ansible-playbook >/dev/null; then
  command -v apt-get >/dev/null || die 'Ansible Core is required and apt-get is unavailable'
  apt-get update && apt-get install -y ansible-core
fi
# 非容器路径必须保持无编排依赖。若宿主机恰好装了这些组件, 它们不参与本次部署;
# 这里只做一次显式声明, 避免日后有人往 role 里悄悄加回 k3s/flux 依赖。
for c in docker containerd k3s flux kubectl helm; do
  if command -v "$c" >/dev/null 2>&1; then
    printf '   note: %s is present but unused by the systemd runtime\n' "$c"
  fi
done

work="$(mktemp -d "${TMPDIR:-/tmp}/xconnect-playbooks.XXXXXX")"
trap 'rm -rf "$work"' EXIT
chmod 0700 "$work"
printf '[agent_proxy]\n%s ansible_connection=local\n[xray_exporter]\n%s ansible_connection=local\n[observability_agent]\n%s ansible_connection=local\n' \
  "$AGENT_ID" "$AGENT_ID" "$AGENT_ID" > "$work/inventory.ini"
chmod 0600 "$work/inventory.ini"
printf '[defaults]\nretry_files_enabled = False\n' > "$work/ansible.cfg"

gitops_config="$work/xconnect-regional-pools.yaml"
curl -fsSL --retry 3 --connect-timeout 15 --max-time 180 \
  "https://raw.githubusercontent.com/ai-workspace-infra/gitops/${gitops_ref}/topology/prod/selfhost/xconnect-regional-pools.yaml" \
  -o "$gitops_config" 2>/dev/null || true

if [[ -s "$gitops_config" ]] && grep -q "id: $AGENT_ID" "$gitops_config" 2>/dev/null; then
  export XCONNECT_GITOPS_CONFIG="$gitops_config"
else
  export XCONNECT_GITOPS_CONFIG=""
fi

if [[ "$observability" == true || "$observability" == 1 || "$observability" == yes ]]; then
  export VECTOR_BILLING_INGEST_ENABLED=true
  export VECTOR_BILLING_INGEST_URL="${VECTOR_BILLING_INGEST_URL:-${BILLING_SERVICE_BASE_URL%/}/v1/ingest/snapshots}"
  export VECTOR_SNAPSHOT_URL="${VECTOR_SNAPSHOT_URL:-http://127.0.0.1:8686}"
  if [[ -z "${VECTOR_AUTH_USER:-}" || -z "${VECTOR_AUTH_PASSWORD:-}" ]]; then
    if [[ -n "${VAULT_ADDR:-}" && -n "${VAULT_TOKEN:-}" ]]; then
      obs_vault_path="${AGENT_PROXY_VECTOR_VAULT_KV_PATH:-${VAULT_OBSERVABILITY_SECRET_PATH:-kv/data/CICD/observability}}"
      resp="$(curl -fsSL -H "X-Vault-Token: ${VAULT_TOKEN}" "${VAULT_ADDR%/}/v1/${obs_vault_path#/}" 2>/dev/null || true)"
      if [[ -n "$resp" ]]; then
        export VECTOR_AUTH_USER="$(echo "$resp" | jq -r '.data.data.user // empty' 2>/dev/null || true)"
        export VECTOR_AUTH_PASSWORD="$(echo "$resp" | jq -r '.data.data.password // empty' 2>/dev/null || true)"
      fi
    fi
  fi
fi

export AGENT_BILLING_ENABLED="${AGENT_BILLING_ENABLED:-false}"

curl -fsSL --retry 3 --connect-timeout 15 --max-time 180 \
  "https://github.com/ai-workspace-infra/playbooks/archive/${playbooks_ref}.tar.gz" \
  | tar -xz -C "$work" --strip-components=1
printf 'Using Playbooks commit %s and GitOps commit %s\n' "$playbooks_ref" "$gitops_ref"
ANSIBLE_CONFIG="$work/ansible.cfg" ansible-playbook "$work/deploy_xray_proxy_server.yml" \
  -i "$work/inventory.ini" -e "agent_service_hosts=$AGENT_ID agent_proxy_local_installer=true agent_proxy_engine=$proxy_engine"
