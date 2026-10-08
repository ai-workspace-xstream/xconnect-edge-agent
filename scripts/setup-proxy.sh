#!/bin/bash
set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}Starting XConnect Edge Agent Installation...${NC}"

XRAY_TCP_USER="caddy"
OPEN_STUNNEL_5443="${OPEN_STUNNEL_5443:-false}"
STANDALONE_MODE=false
INSTALL_OBSERVABILITY="${INSTALL_OBSERVABILITY:-false}"
STANDALONE_UUID_FILE="/usr/local/etc/xray/standalone.uuid"
AGENT_DATA_DIR="${AGENT_DATA_DIR:-/opt/xconnect-edge-agent}"
LEGACY_AGENT_SERVICE_NAME="agent-svc-plus"
CLOUDFLARE_ZONE_NAME="${CLOUDFLARE_ZONE_NAME:-}"
CLOUDFLARE_API_BASE="https://api.cloudflare.com/client/v4"
GITHUB_REPO="${GITHUB_REPO:-ai-workspace-xstream/xconnect-edge-agent}"
AGENT_RELEASE_TAG="${AGENT_RELEASE_TAG:-latest}"
AGENT_RELEASE_BASE_URL="https://github.com/${GITHUB_REPO}/releases"
VAULT_AGENT_VERSION="${VAULT_AGENT_VERSION:-1.21.4}"
VAULT_AGENT_TLS_STAGE_DIR="${VAULT_AGENT_TLS_STAGE_DIR:-/var/lib/vault-agent/tls}"
VAULT_TLS_CERT_FIELD="${VAULT_TLS_CERT_FIELD:-tls_fullchain_pem_b64}"
VAULT_TLS_KEY_FIELD="${VAULT_TLS_KEY_FIELD:-tls_key_pem_b64}"

update_agent_metadata() {
    local file="$1" key="$2" value="$3" temporary
    case "$value" in
        ""|*[!a-zA-Z0-9._-]*)
            echo "Agent $key must use letters, digits, dots, underscores or hyphens." >&2
            return 1
            ;;
    esac
    temporary="$(mktemp)"
    if ! awk -v key="$key" -v value="$value" '
        /^agent:[[:space:]]*($|#)/ {
            in_agent = 1
            found = 1
            print
            print "  " key ": \"" value "\""
            next
        }
        /^[^[:space:]#]/ { in_agent = 0 }
        in_agent && $0 ~ "^[[:space:]]+" key ":[[:space:]]*" { next }
        { print }
        END { if (!found) exit 1 }
    ' "$file" > "$temporary"; then
        rm -f "$temporary"
        return 1
    fi
    cat "$temporary" > "$file"
    rm -f "$temporary"
}

ensure_agent_dynamic_users() {
    local file="$1" temporary
    temporary="$(mktemp "${file}.XXXXXX")"
    chmod 0600 "$temporary"
    # Existing installations predate HandlerService hot additions. Preserve all
    # operator settings and explicit dynamicUsers blocks; only backfill missing
    # blocks for the two standard proxy targets supplied by this installer.
    if ! awk '
        function flush_target() {
            if (!target) return
            printf "%s", block
            if (!has_dynamic && (target == "xhttp" || target == "tcp")) {
                print indent "  dynamicUsers:"
                print indent "    enabled: true"
                print indent "    executable: \"/usr/local/bin/xray\""
                print indent "    server: \"127.0.0.1:" (target == "xhttp" ? "10086" : "10087") "\""
            }
            target = ""; block = ""; has_dynamic = 0
        }
        /^xray:[[:space:]]*($|#)/ { in_xray = 1 }
        /^[^[:space:]#]/ && !/^xray:/ { flush_target(); in_xray = 0; in_targets = 0 }
        in_xray && /^    targets:[[:space:]]*($|#)/ { in_targets = 1 }
        in_targets && /^[[:space:]]+- name:/ {
            flush_target()
            target = $0
            sub(/^[[:space:]]+- name:[[:space:]]*/, "", target)
            sub(/[[:space:]]+#.*/, "", target)
            gsub(/[\042\047[:space:]]/, "", target)
            indent = $0; sub(/-.*/, "", indent)
        }
        target && /^    [^[:space:]#]/ { flush_target(); in_targets = 0 }
        target {
            block = block $0 "\n"
            if ($0 ~ /^[[:space:]]+dynamicUsers:/) has_dynamic = 1
            next
        }
        { print }
        END { flush_target() }
    ' "$file" > "$temporary"; then
        rm -f "$temporary"
        return 1
    fi
    if cmp -s "$file" "$temporary"; then
        rm -f "$temporary"
    else
        mv -f "$temporary" "$file"
    fi
}

is_truthy() {
    case "${1:-}" in
        1|true|TRUE|yes|YES|y|Y|on|ON)
            return 0
            ;;
        *)
            return 1
            ;;
    esac
}

caddy_fragments_for_domain() {
    local domain="$1" fragment
    [ -d /etc/caddy/conf.d ] || return 0
    while IFS= read -r fragment; do
        if awk -v domain="$domain" '$1 == domain && $NF == "{" { found = 1 } END { exit !found }' "$fragment"; then
            printf '%s\n' "$fragment"
        fi
    done < <(find /etc/caddy/conf.d -maxdepth 1 -type f -name '*.caddy' -print | sort)
}

write_caddy_config() {
    local existing_fragment
    local -a fragments=()
    mapfile -t fragments < <(caddy_fragments_for_domain "$DOMAIN")
    if [ "${#fragments[@]}" -gt 1 ]; then
        echo "Multiple Caddy fragments declare ${DOMAIN}: ${fragments[*]}" >&2
        return 1
    fi

    # Playbooks may own a complete site fragment. Import it as the sole site
    # definition instead of emitting a second definition in Caddyfile.
    if [ "${#fragments[@]}" -eq 1 ]; then
        existing_fragment="${fragments[0]}"
        if ! grep -Eq 'handle_path[[:space:]]+/xray-exporter/(xhttp|tcp)/\*' "$existing_fragment" \
            || ! grep -Eq 'path[[:space:]]+/split' "$existing_fragment"; then
            echo "Existing Caddy fragment for ${DOMAIN} is missing the agent proxy routes: ${existing_fragment}" >&2
            return 1
        fi
        cat > /etc/caddy/Caddyfile <<'EOF'
import /etc/caddy/conf.d/*.caddy
EOF
        return 0
    fi

    cat > /etc/caddy/Caddyfile <<EOF
${DOMAIN} {
    ${TLS_CONFIG}

    handle_path /xray-exporter/xhttp/* {
        reverse_proxy 127.0.0.1:8080
    }

    handle_path /xray-exporter/tcp/* {
        reverse_proxy 127.0.0.1:8081
    }

    @xhttp {
        path /split /split/*
    }

    @xhttp_root path /split
    rewrite @xhttp_root /split/

    handle @xhttp {
        uri query -x_padding

        reverse_proxy unix//dev/shm/xray.sock {
             transport http {
                 versions h2c 2
             }
        }
    }

    # Fallback/Default site content
    respond "$( [ "$STANDALONE_MODE" = true ] && printf '%s' 'Standalone Xray Node' || printf '%s' 'XConnect Edge Agent' )"
}

import /etc/caddy/conf.d/*.caddy
EOF
}

configure_vault_agent_tls() {
    local vault_zip vault_sha256sums expected_sha256 vault_arch

    if [ -z "$VAULT_ADDR" ] && [ -z "$VAULT_TOKEN" ] && [ -z "$VAULT_TLS_SECRET_PATH" ]; then
        return 0
    fi
    if [ -z "$VAULT_ADDR" ] || [ -z "$VAULT_TOKEN" ] || [ -z "$VAULT_TLS_SECRET_PATH" ]; then
        echo -e "$RED Vault TLS sync requires VAULT_ADDR, VAULT_TOKEN, and VAULT_TLS_SECRET_PATH.$NC" >&2
        return 1
    fi
    case "$VAULT_TLS_SECRET_PATH" in
        */data/*)
            ;;
        *)
            echo -e "$RED VAULT_TLS_SECRET_PATH must be a Vault KV v2 API path such as kv/data/CICD/domains/<domain>.$NC" >&2
            return 1
            ;;
    esac

    vault_arch="$(detect_goarch)"
    if ! command -v unzip >/dev/null 2>&1; then
        apt-get update
        apt-get install -y unzip
    fi
    if ! command -v vault >/dev/null 2>&1; then
        vault_zip="/var/cache/vault_"$VAULT_AGENT_VERSION"_linux_"$vault_arch".zip"
        vault_sha256sums="/tmp/vault_"$VAULT_AGENT_VERSION"_SHA256SUMS"
        curl -fsSL --retry 3 -o "$vault_zip" "https://releases.hashicorp.com/vault/$VAULT_AGENT_VERSION/vault_"$VAULT_AGENT_VERSION"_linux_"$vault_arch".zip"
        curl -fsSL --retry 3 -o "$vault_sha256sums" "https://releases.hashicorp.com/vault/$VAULT_AGENT_VERSION/vault_"$VAULT_AGENT_VERSION"_SHA256SUMS"
        expected_sha256="$(awk -v file="vault_"$VAULT_AGENT_VERSION"_linux_"$vault_arch".zip" '$2 == file { print $1 }' "$vault_sha256sums")"
        if [ -z "$expected_sha256" ]; then
            echo -e "$RED Could not resolve the Vault Agent archive checksum.$NC" >&2
            return 1
        fi
        printf '%s  %s\n' "$expected_sha256" "$vault_zip" | sha256sum -c -
        unzip -oq "$vault_zip" -d /usr/local/bin
        chmod 0755 /usr/local/bin/vault
    fi

    mkdir -p /etc/vault.d /run/vault-agent "$VAULT_AGENT_TLS_STAGE_DIR/current" /etc/caddy/tls
    chmod 0750 /etc/vault.d /run/vault-agent "$VAULT_AGENT_TLS_STAGE_DIR" "$VAULT_AGENT_TLS_STAGE_DIR/current"
    chown root:root /etc/vault.d /run/vault-agent
    chown root:caddy "$VAULT_AGENT_TLS_STAGE_DIR" "$VAULT_AGENT_TLS_STAGE_DIR/current"
    umask 077
    printf '%s\n' "$VAULT_TOKEN" > /etc/vault.d/token
    chmod 0600 /etc/vault.d/token

    cat > /etc/vault.d/tls.ctmpl <<'EOF'
{{- with secret (env "VAULT_TLS_SECRET_PATH") -}}
{{ .Data.metadata.version }}
{{ index .Data.data (env "VAULT_TLS_CERT_FIELD") | base64Decode | writeToFile (printf "%s/current/fullchain.pem" (env "VAULT_AGENT_TLS_STAGE_DIR")) "root" "caddy" "0640" }}
{{ index .Data.data (env "VAULT_TLS_KEY_FIELD") | base64Decode | writeToFile (printf "%s/current/key.pem" (env "VAULT_AGENT_TLS_STAGE_DIR")) "root" "caddy" "0640" }}
{{- end -}}
EOF
    chmod 0600 /etc/vault.d/tls.ctmpl

    cat > /etc/vault.d/agent.hcl <<EOF
vault {
  address = "$VAULT_ADDR"
}

auto_auth {
  method "token_file" {
    config = {
      token_file_path = "/etc/vault.d/token"
    }
  }
}

template_config {
  static_secret_render_interval = "5m"
}

template {
  source               = "/etc/vault.d/tls.ctmpl"
  destination          = "/run/vault-agent/tls-version"
  perms                = "0600"
  error_on_missing_key = true

  exec {
    command = ["/usr/local/sbin/sync-vault-agent-caddy-tls"]
    timeout = "30s"
  }
}
EOF
    chmod 0600 /etc/vault.d/agent.hcl

    cat > /usr/local/sbin/sync-vault-agent-caddy-tls <<'EOF'
#!/bin/sh
set -eu
stage_dir=$VAULT_AGENT_TLS_STAGE_DIR
cert_source="$stage_dir/current/fullchain.pem"
key_source="$stage_dir/current/key.pem"
cert_dest=/etc/caddy/tls/agent-proxy.crt
key_dest=/etc/caddy/tls/agent-proxy.key
openssl x509 -in "$cert_source" -noout -checkend 86400
openssl x509 -in "$cert_source" -noout -checkhost "$VAULT_AGENT_TLS_DOMAIN"
cert_public_key="$(openssl x509 -in "$cert_source" -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum | awk '{print $1}')"
key_public_key="$(openssl pkey -in "$key_source" -pubout -outform DER | sha256sum | awk '{print $1}')"
[ "$cert_public_key" = "$key_public_key" ]
install -o root -g caddy -m 0644 "$cert_source" "$cert_dest.next"
install -o root -g caddy -m 0640 "$key_source" "$key_dest.next"
mv -f "$cert_dest.next" "$cert_dest"
mv -f "$key_dest.next" "$key_dest"
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
if systemctl is-active --quiet caddy; then
    systemctl reload caddy
fi
EOF
    chmod 0750 /usr/local/sbin/sync-vault-agent-caddy-tls

    cat > /etc/systemd/system/vault-agent-tls.service <<EOF
[Unit]
Description=Vault Agent certificate sync
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
Environment="VAULT_TLS_SECRET_PATH=$VAULT_TLS_SECRET_PATH"
Environment="VAULT_TLS_CERT_FIELD=$VAULT_TLS_CERT_FIELD"
Environment="VAULT_TLS_KEY_FIELD=$VAULT_TLS_KEY_FIELD"
Environment="VAULT_AGENT_TLS_STAGE_DIR=$VAULT_AGENT_TLS_STAGE_DIR"
Environment="VAULT_AGENT_TLS_DOMAIN=$DOMAIN"
ExecStart=/usr/local/bin/vault agent -config=/etc/vault.d/agent.hcl
Restart=on-failure
RestartSec=5s
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 /etc/systemd/system/vault-agent-tls.service
    systemctl daemon-reload
    systemctl enable vault-agent-tls.service
    systemctl restart vault-agent-tls.service
    for _ in $(seq 1 60); do
        if [ -s "$VAULT_AGENT_TLS_STAGE_DIR/current/fullchain.pem" ] && [ -s "$VAULT_AGENT_TLS_STAGE_DIR/current/key.pem" ]; then
            echo -e "$GREEN Vault Agent rendered the TLS certificate and key.$NC"
            return 0
        fi
        sleep 2
    done
    echo -e "$RED Vault Agent did not render TLS material within 120 seconds.$NC" >&2
    journalctl -u vault-agent-tls.service -n 40 --no-pager || true
    return 1
}

detect_goarch() {
    local arch_raw
    arch_raw="$(uname -m)"
    case "$arch_raw" in
        x86_64|amd64)
            printf 'amd64\n'
            ;;
        aarch64|arm64)
            printf 'arm64\n'
            ;;
        *)
            echo -e "${RED}Unsupported architecture: ${arch_raw}${NC}" >&2
            return 1
            ;;
    esac
}

resolve_release_download_url() {
    local asset_name="$1"
    if [ "${AGENT_RELEASE_TAG}" = "latest" ]; then
        printf '%s/latest/download/%s\n' "${AGENT_RELEASE_BASE_URL}" "${asset_name}"
    else
        printf '%s/download/%s/%s\n' "${AGENT_RELEASE_BASE_URL}" "${AGENT_RELEASE_TAG}" "${asset_name}"
    fi
}

install_prebuilt_runtime_bundle() {
    local goarch="$1"
    local asset_name="artifact-${goarch}.tar.gz"
    local tmp_dir
    tmp_dir="$(mktemp -d /tmp/agent-runtime.XXXXXX)"

    local agent_bin=""
    local caddy_bin=""

    # 1. Try downloading bundle tarball
    local bundle_downloaded=false
    local bundle_url
    bundle_url="$(resolve_release_download_url "${asset_name}")"

    echo "Downloading runtime bundle: ${asset_name}"
    if curl -fL --retry 3 --connect-timeout 10 -o "${tmp_dir}/${asset_name}" "${bundle_url}" 2>/dev/null; then
        bundle_downloaded=true
    else
        # Try resolving hashed bundle asset (e.g. artifact-${goarch}-<commit>.tar.gz)
        local release_api_url
        if [ "${AGENT_RELEASE_TAG}" = "latest" ]; then
            release_api_url="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"
        else
            release_api_url="https://api.github.com/repos/${GITHUB_REPO}/releases/tags/${AGENT_RELEASE_TAG}"
        fi
        local matched_bundle_url
        matched_bundle_url="$(curl -fsSL --connect-timeout 5 "${release_api_url}" 2>/dev/null | \
            grep -oE "https://[^\" ]+/download/[^\" ]+/artifact-${goarch}[^\" ]*\.tar\.gz" | head -n 1 || true)"
        if [ -n "${matched_bundle_url}" ]; then
            echo "Downloading hashed runtime bundle: ${matched_bundle_url}"
            if curl -fL --retry 3 --connect-timeout 10 -o "${tmp_dir}/${asset_name}" "${matched_bundle_url}" 2>/dev/null; then
                bundle_downloaded=true
            fi
        fi
    fi

    if [ "$bundle_downloaded" = true ]; then
        tar -xzf "${tmp_dir}/${asset_name}" -C "${tmp_dir}" || true
    fi

    # Find agent binary in extracted bundle
    for candidate in \
        "${tmp_dir}/xconnect-edge-agent" \
        "${tmp_dir}/agent-svc-plus" \
        "${tmp_dir}/agent-proxy" \
        "$(find "${tmp_dir}" -maxdepth 2 -type f \( -name "xconnect-edge-agent" -o -name "agent-svc-plus" -o -name "agent-proxy" \) 2>/dev/null | head -n 1)"
    do
        if [ -n "$candidate" ] && [ -f "$candidate" ]; then
            agent_bin="$candidate"
            break
        fi
    done

    # Find caddy binary in extracted bundle
    for candidate in \
        "${tmp_dir}/caddy" \
        "$(find "${tmp_dir}" -maxdepth 2 -type f -name "caddy" 2>/dev/null | head -n 1)"
    do
        if [ -n "$candidate" ] && [ -f "$candidate" ]; then
            caddy_bin="$candidate"
            break
        fi
    done

    # 2. Fallback: Download individual prebuilt binaries directly if not found in bundle
    if [ -z "$agent_bin" ] || [ ! -f "$agent_bin" ]; then
        echo -e "${YELLOW}Agent binary not found in bundle, attempting direct binary download...${NC}"
        for bin_name in "xconnect-edge-agent-linux-${goarch}" "agent-svc-plus-linux-${goarch}"; do
            local direct_url
            direct_url="$(resolve_release_download_url "${bin_name}")"
            if curl -fL --retry 3 --connect-timeout 10 -o "${tmp_dir}/xconnect-edge-agent" "${direct_url}" 2>/dev/null; then
                chmod +x "${tmp_dir}/xconnect-edge-agent"
                agent_bin="${tmp_dir}/xconnect-edge-agent"
                echo -e "${GREEN}Downloaded ${bin_name} directly.${NC}"
                break
            fi
        done
    fi

    if [ -z "$caddy_bin" ] || [ ! -f "$caddy_bin" ]; then
        echo -e "${YELLOW}Caddy binary not found in bundle, attempting direct binary download...${NC}"
        local caddy_url
        caddy_url="$(resolve_release_download_url "caddy-linux-${goarch}")"
        if curl -fL --retry 3 --connect-timeout 10 -o "${tmp_dir}/caddy" "${caddy_url}" 2>/dev/null; then
            chmod +x "${tmp_dir}/caddy"
            caddy_bin="${tmp_dir}/caddy"
            echo -e "${GREEN}Downloaded caddy-linux-${goarch} directly.${NC}"
        fi
    fi

    if [ -z "$agent_bin" ] || [ ! -f "$agent_bin" ] || [ -z "$caddy_bin" ] || [ ! -f "$caddy_bin" ]; then
        echo -e "${RED}Runtime bundle is missing required binaries (xconnect-edge-agent/caddy).${NC}"
        exit 1
    fi

    echo -e "${GREEN}Installing binaries...${NC}"
    install -m 755 "${agent_bin}" /usr/local/bin/xconnect-edge-agent
    ln -sf /usr/local/bin/xconnect-edge-agent /usr/local/bin/agent-svc-plus
    ln -sf /usr/local/bin/xconnect-edge-agent /usr/local/bin/agent-proxy
    install -m 755 "${caddy_bin}" /usr/bin/caddy
    rm -rf "${tmp_dir}"
}

fetch_repo_archive() {
    local tmp_dir
    local extracted_dir
    tmp_dir="$(mktemp -d /tmp/agent-repo.XXXXXX)"

    curl -fL --retry 3 --connect-timeout 10 \
        "https://github.com/${GITHUB_REPO}/archive/refs/heads/main.tar.gz" \
        -o "${tmp_dir}/repo.tar.gz"
    tar -xzf "${tmp_dir}/repo.tar.gz" -C "${tmp_dir}"
    extracted_dir="$(find "${tmp_dir}" -maxdepth 1 -mindepth 1 -type d | head -n 1)"

    if [ -z "${extracted_dir}" ]; then
        echo -e "${RED}Failed to fetch repository archive for templates/config.${NC}"
        exit 1
    fi

    printf '%s\n' "${extracted_dir}"
}

apply_low_latency_tuning() {
    echo -e "${GREEN}[post] Applying low-latency kernel tuning (BBR/fq)...${NC}"

    cat > /etc/sysctl.d/99-agent-lowlatency.conf <<'EOF'
# Agent low-latency tuning (safe baseline)
net.ipv4.tcp_congestion_control = bbr
net.core.default_qdisc = fq
net.ipv4.tcp_fastopen = 3

# Queue/backlog
net.core.somaxconn = 8192
net.ipv4.tcp_max_syn_backlog = 8192
net.core.netdev_max_backlog = 16384

# Socket buffers
net.core.rmem_max = 67108864
net.core.wmem_max = 67108864
net.ipv4.tcp_rmem = 4096 87380 33554432
net.ipv4.tcp_wmem = 4096 65536 33554432

# Better handling for path MTU edge-cases
net.ipv4.tcp_mtu_probing = 1
EOF

    if ! sysctl --system >/tmp/agent-lowlatency-sysctl.log 2>&1; then
        echo -e "${YELLOW}sysctl --system returned non-zero; checking applied values...${NC}"
        tail -n 80 /tmp/agent-lowlatency-sysctl.log || true
    fi

    sysctl net.ipv4.tcp_congestion_control \
        net.core.default_qdisc \
        net.ipv4.tcp_fastopen \
        net.core.somaxconn \
        net.core.netdev_max_backlog \
        net.ipv4.tcp_max_syn_backlog \
        net.core.rmem_max \
        net.core.wmem_max \
        net.ipv4.tcp_mtu_probing || true
}

setup_fq_qdisc_service() {
    echo -e "${GREEN}[post] Ensuring fq qdisc persistence service...${NC}"

    cat > /usr/local/sbin/apply-fq-qdisc.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

while IFS= read -r dev; do
    [ "$dev" = "lo" ] && continue
    tc qdisc replace dev "$dev" root fq >/dev/null 2>&1 || true
done < <(ip -o link show | awk -F': ' '{print $2}' | cut -d'@' -f1)
EOF
    chmod +x /usr/local/sbin/apply-fq-qdisc.sh

    cat > /etc/systemd/system/apply-fq-qdisc.service <<'EOF'
[Unit]
Description=Apply fq qdisc for low-latency pacing
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/apply-fq-qdisc.sh

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable --now apply-fq-qdisc.service || true
}

ensure_ufw_ports() {
    local ufw_ports=(80 443 1443)

    if is_truthy "$OPEN_STUNNEL_5443"; then
        ufw_ports+=(5443)
    fi

    echo -e "${GREEN}[post] UFW check (${ufw_ports[*]})...${NC}"

    if ! command -v ufw >/dev/null 2>&1; then
        echo -e "${YELLOW}ufw not installed; skipping UFW checks.${NC}"
        return 0
    fi

    UFW_STATE="$(ufw status | head -n 1 | awk '{print $2}')"
    if [ "$UFW_STATE" != "active" ]; then
        echo -e "${YELLOW}ufw is installed but not active; skipping automatic rule changes.${NC}"
        return 0
    fi

    for port in "${ufw_ports[@]}"; do
        ufw allow "${port}/tcp" >/dev/null 2>&1 || true
    done
    ufw reload >/dev/null 2>&1 || true

    echo "UFW active rules:"
    ufw status | sed -n '1,40p' || true
}

post_install_network_optimization() {
    ensure_ufw_ports
    apply_low_latency_tuning
    if command -v tc >/dev/null 2>&1; then
        setup_fq_qdisc_service
    else
        echo -e "${YELLOW}tc command not found; skipping fq qdisc persistence setup.${NC}"
    fi
}

resolve_public_ipv4() {
    local ip=""

    for endpoint in \
        "https://ipv4.icanhazip.com" \
        "https://api.ipify.org" \
        "https://ifconfig.me/ip"
    do
        ip="$(curl -4fsSL --max-time 5 "$endpoint" 2>/dev/null | tr -d '[:space:]' || true)"
        if printf '%s\n' "$ip" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; then
            printf '%s\n' "$ip"
            return 0
        fi
    done

    return 1
}

update_cloudflare_dns_for_domain() {
    local domain_name="$1"
    local target_ip="$2"
    local token="${CLOUDFLARE_API_TOKEN:-}"
    local zone_lookup
    local zone_id
    local record_lookup
    local record_id
    local existing_type
    local existing_content
    local existing_proxied

    if [ -z "$token" ]; then
        echo -e "${YELLOW}CLOUDFLARE_API_TOKEN not set; skipping automatic DNS update for ${domain_name}.${NC}"
        return 0
    fi

    if [ -z "$CLOUDFLARE_ZONE_NAME" ]; then
        echo -e "${YELLOW}CLOUDFLARE_ZONE_NAME not set; skipping automatic DNS update for ${domain_name}.${NC}"
        return 0
    fi

    echo -e "${GREEN}[dns] Updating Cloudflare DNS: ${domain_name} -> ${target_ip}${NC}"

    zone_lookup="$(curl -fsSL \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json" \
        "${CLOUDFLARE_API_BASE}/zones?name=${CLOUDFLARE_ZONE_NAME}")" || {
        echo -e "${YELLOW}[dns] Failed to resolve Cloudflare zone ${CLOUDFLARE_ZONE_NAME}; skipping automatic DNS update.${NC}"
        return 0
    }

    zone_id="$(printf '%s' "$zone_lookup" | python3 -c 'import json,sys; data=json.load(sys.stdin); print((data.get("result") or [{}])[0].get("id",""))' 2>/dev/null || true)"
    if [ -z "$zone_id" ]; then
        echo -e "${YELLOW}[dns] Could not determine Cloudflare zone id for ${CLOUDFLARE_ZONE_NAME}; skipping DNS update.${NC}"
        return 0
    fi

    record_lookup="$(curl -fsSL \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json" \
        "${CLOUDFLARE_API_BASE}/zones/${zone_id}/dns_records?name=${domain_name}")" || {
        echo -e "${YELLOW}[dns] Failed to query existing DNS records for ${domain_name}; skipping DNS update.${NC}"
        return 0
    }

    record_id="$(printf '%s' "$record_lookup" | python3 -c 'import json,sys; data=json.load(sys.stdin); rows=data.get("result") or []; row=next((r for r in rows if r.get("type")=="A"), {}); print(row.get("id",""))' 2>/dev/null || true)"
    existing_type="$(printf '%s' "$record_lookup" | python3 -c 'import json,sys; data=json.load(sys.stdin); rows=data.get("result") or []; row=next((r for r in rows if r.get("type")=="A"), {}); print(row.get("type",""))' 2>/dev/null || true)"
    existing_content="$(printf '%s' "$record_lookup" | python3 -c 'import json,sys; data=json.load(sys.stdin); rows=data.get("result") or []; row=next((r for r in rows if r.get("type")=="A"), {}); print(row.get("content",""))' 2>/dev/null || true)"
    existing_proxied="$(printf '%s' "$record_lookup" | python3 -c 'import json,sys; data=json.load(sys.stdin); rows=data.get("result") or []; row=next((r for r in rows if r.get("type")=="A"), {}); print(str(row.get("proxied", False)).lower())' 2>/dev/null || true)"

    if [ -n "$record_id" ] && [ "$existing_type" = "A" ] && [ "$existing_content" = "$target_ip" ] && [ "$existing_proxied" = "false" ]; then
        echo -e "${GREEN}[dns] Cloudflare A record already up to date.${NC}"
        return 0
    fi

    if [ -n "$record_id" ]; then
        curl -fsSL -X PUT \
            -H "Authorization: Bearer ${token}" \
            -H "Content-Type: application/json" \
            --data "{\"type\":\"A\",\"name\":\"${domain_name}\",\"content\":\"${target_ip}\",\"ttl\":300,\"proxied\":false}" \
            "${CLOUDFLARE_API_BASE}/zones/${zone_id}/dns_records/${record_id}" >/dev/null || {
            echo -e "${YELLOW}[dns] Failed to update existing A record for ${domain_name}.${NC}"
            return 0
        }
        echo -e "${GREEN}[dns] Updated existing A record for ${domain_name}.${NC}"
    else
        curl -fsSL -X POST \
            -H "Authorization: Bearer ${token}" \
            -H "Content-Type: application/json" \
            --data "{\"type\":\"A\",\"name\":\"${domain_name}\",\"content\":\"${target_ip}\",\"ttl\":300,\"proxied\":false}" \
            "${CLOUDFLARE_API_BASE}/zones/${zone_id}/dns_records" >/dev/null || {
            echo -e "${YELLOW}[dns] Failed to create A record for ${domain_name}.${NC}"
            return 0
        }
        echo -e "${GREEN}[dns] Created A record for ${domain_name}.${NC}"
    fi
}

usage() {
    cat <<EOF
Usage:
  $0 [--upgrade-only|--upgrade] [--node <domain>] [--cloudflare-zone <zone>] [--auth-url <url>] [--internal-service-token <token>] [--open-stunnel-5443] [--standalone]
  $0 --with-observability --node <domain>
  $0 --print-arch

Env (optional):
  INSTALL_OBSERVABILITY=true  # run canonical monitoring playbook on this node
  VECTOR_AUTH_USER            # ingest credentials supplied by Vault at runtime
  VECTOR_AUTH_PASSWORD
  VAULT_OBSERVABILITY_SECRET_PATH # default kv/data/CICD/observability
  OBSERVABILITY_ENDPOINT     # defaults to https://observability.svc.plus
  OBSERVABILITY_PLAYBOOKS_REF # immutable playbooks commit (see helper default)
  AUTH_URL
  INTERNAL_SERVICE_TOKEN
  AGENT_PROXY_DOMAIN
  AGENT_REGION                # deployment region code, e.g. hk or jpn-tky
  AGENT_POOL                  # logical pool identifier within the region
  VAULT_ADDR                  # enables Vault Agent TLS sync when combined with token/path
  VAULT_TOKEN                 # read at runtime; never commit to this script
  VAULT_TLS_SECRET_PATH       # Vault KV v2 API path, e.g. kv/data/CICD/domains/<domain>
  VAULT_TLS_CERT_FIELD        # defaults to tls_fullchain_pem_b64
  VAULT_TLS_KEY_FIELD         # defaults to tls_key_pem_b64
  CLOUDFLARE_ZONE_NAME        # required with CLOUDFLARE_API_TOKEN
  OPEN_STUNNEL_5443=true   # when co-locating PostgreSQL on the same node

Examples:
  # Supports AMD64 and ARM64 (aarch64)
  curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \\
    bash -s -- --node "$AGENT_PROXY_DOMAIN"

  curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \\
    env AUTH_URL="$AUTH_URL" INTERNAL_SERVICE_TOKEN="$INTERNAL_SERVICE_TOKEN" \\
      bash -s -- --node "$AGENT_PROXY_DOMAIN"

  # Upgrade binaries only (no config overwrite)
  curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \\
    bash -s -- --upgrade-only

  # Open 5443/tcp together with 80/443/1443 for stunnel(PostgreSQL) co-location
  curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \\
    env OPEN_STUNNEL_5443=true CLOUDFLARE_ZONE_NAME="$CLOUDFLARE_ZONE_NAME" \\
      bash -s -- --node "$AGENT_PROXY_DOMAIN"

  # Standalone self-host mode: installs caddy + xray only, generates UUID and prints import links
  curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \\
    bash -s -- --node "$AGENT_PROXY_DOMAIN" --standalone

  # Print detected architecture and download artifacts (no install)
  curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \\
    bash -s -- --print-arch
EOF
}

DOMAIN="${AGENT_PROXY_DOMAIN:-}"
AUTH_URL="${AUTH_URL:-${ACCOUNTS_AUTH_URL:-${Accounts_AUTH_URL:-${ACCOUNTS_URL:-}}}}"
INTERNAL_SERVICE_TOKEN="${INTERNAL_SERVICE_TOKEN:-${NTERNAL_SERVICE_TOKEN:-}}"
BILLING_URL="${BILLING_URL:-${BILLING_BASE_URL:-${BILLING_SERVICE_URL:-${BILLING_AUTH_URL:-${BILLING_SERVICE_AUTH_URL:-${Billing_service_AUTH_URL:-${billing_service_url:-${billing_service_auth_url:-}}}}}}}}"
UPGRADE_ONLY=false
PRINT_ARCH=false

while [ "$#" -gt 0 ]; do
    case "$1" in
        --node)
            DOMAIN="${2:-}"
            shift 2
            ;;
        --node=*)
            DOMAIN="${1#*=}"
            shift
            ;;
        --cloudflare-zone)
            CLOUDFLARE_ZONE_NAME="${2:-}"
            shift 2
            ;;
        --cloudflare-zone=*)
            CLOUDFLARE_ZONE_NAME="${1#*=}"
            shift
            ;;
        --auth-url|--accounts-url|--accounts-auth-url)
            AUTH_URL="${2:-}"
            shift 2
            ;;
        --auth-url=*|--accounts-url=*|--accounts-auth-url=*)
            AUTH_URL="${1#*=}"
            shift
            ;;
        --internal-service-token)
            INTERNAL_SERVICE_TOKEN="${2:-}"
            shift 2
            ;;
        --internal-service-token=*)
            INTERNAL_SERVICE_TOKEN="${1#*=}"
            shift
            ;;
        --billing-url|--billing-service-url|--billing-base-url|--billing-auth-url|--billing-service-auth-url)
            BILLING_URL="${2:-}"
            shift 2
            ;;
        --billing-url=*|--billing-service-url=*|--billing-base-url=*|--billing-auth-url=*|--billing-service-auth-url=*)
            BILLING_URL="${1#*=}"
            shift
            ;;
        --upgrade-only|--upgrade)
            UPGRADE_ONLY=true
            shift
            ;;
        --print-arch)
            PRINT_ARCH=true
            shift
            ;;
        --open-stunnel-5443)
            OPEN_STUNNEL_5443=true
            shift
            ;;
        --open-stunnel-5443=*)
            OPEN_STUNNEL_5443="${1#*=}"
            shift
            ;;
        --with-observability)
            INSTALL_OBSERVABILITY=true
            shift
            ;;
        --standalone)
            STANDALONE_MODE=true
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        --)
            shift
            break
            ;;
        *)
            if [ -z "$DOMAIN" ]; then
                DOMAIN="$1"
                shift
            else
                echo -e "${RED}Unknown argument: $1${NC}"
                usage
                exit 1
            fi
            ;;
    esac
done

if [ "$PRINT_ARCH" = false ]; then
    if [ -z "$DOMAIN" ]; then
        HOSTNAME=$(hostname)
        echo -e "${YELLOW}No node provided. Using system hostname: ${HOSTNAME}${NC}"
        DOMAIN="$HOSTNAME"
    fi

    if [ -z "$DOMAIN" ]; then
        echo -e "${RED}Node domain is required.${NC}"
        exit 1
    fi

    echo -e "${GREEN}Using node domain: ${DOMAIN}${NC}"
fi

if [ "$UPGRADE_ONLY" = true ]; then
    echo -e "${YELLOW}Running in upgrade-only mode: configuration files will not be overwritten.${NC}"
fi
if [ "$STANDALONE_MODE" = true ]; then
    echo -e "${GREEN}Running in standalone self-host mode (caddy + xray only).${NC}"
fi
if [ -n "$AUTH_URL" ]; then
    echo -e "${GREEN}Using AUTH_URL: ${AUTH_URL}${NC}"
fi
if [ -n "$BILLING_URL" ]; then
    echo -e "${GREEN}Using BILLING_URL: ${BILLING_URL}${NC}"
fi
if is_truthy "$OPEN_STUNNEL_5443"; then
    OPEN_STUNNEL_5443=true
    echo -e "${GREEN}UFW will also allow 5443/tcp for stunnel co-location.${NC}"
else
    OPEN_STUNNEL_5443=false
fi

if [ "$PRINT_ARCH" = true ]; then
    ARCH_RAW="$(uname -m)"
    GOARCH="$(detect_goarch)"
    echo -e "${GREEN}Detected architecture: ${ARCH_RAW} (GOARCH=${GOARCH})${NC}"
    echo -e "${GREEN}Runtime bundle asset: artifact-${GOARCH}.tar.gz${NC}"
    exit 0
fi

# The combined installer can resolve monitoring credentials from the same
# runtime Vault session used for TLS. Never echo the resulting values.
if is_truthy "$INSTALL_OBSERVABILITY" &&
   { [ -z "${VECTOR_AUTH_USER:-}" ] || [ -z "${VECTOR_AUTH_PASSWORD:-}" ]; } &&
   [ -n "${VAULT_ADDR:-}" ] && [ -n "${VAULT_TOKEN:-}" ]; then
    monitoring_credentials="$(python3 - <<'PY_MONITORING'
import json, os, urllib.request, urllib.error
address = os.environ['VAULT_ADDR'].rstrip('/')
path = os.environ.get('VAULT_OBSERVABILITY_SECRET_PATH', 'kv/data/CICD/observability').strip('/')
if not address.startswith('https://') or '/data/' not in path:
    raise SystemExit('Monitoring credentials require HTTPS Vault and a KV v2 data path.')
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None
request = urllib.request.Request(address + '/v1/' + path,
    headers={'X-Vault-Token': os.environ['VAULT_TOKEN']})
try:
    with urllib.request.build_opener(NoRedirect()).open(request, timeout=20) as response:
        fields = json.load(response)['data']['data']
    user, password = fields['user'], fields['password']
    if not all(isinstance(v, str) and v and '\n' not in v and '\r' not in v for v in (user, password)) or ':' in user:
        raise ValueError('invalid credential fields')
except (urllib.error.URLError, OSError, KeyError, ValueError):
    raise SystemExit('Failed to read monitoring user/password from Vault; check runtime token permissions.')
print(user + ':' + password)
PY_MONITORING
)"
    export VECTOR_AUTH_USER="${monitoring_credentials%%:*}"
    export VECTOR_AUTH_PASSWORD="${monitoring_credentials#*:}"
    unset monitoring_credentials
fi

# Fail before changing the node when the requested combined deployment is incomplete.
if is_truthy "$INSTALL_OBSERVABILITY"; then
    if [ "$STANDALONE_MODE" = true ] || [ "$UPGRADE_ONLY" = true ]; then
        echo "--with-observability requires a normal managed-node installation." >&2
        exit 1
    fi
    if [ -z "$AUTH_URL" ] || [ -z "$INTERNAL_SERVICE_TOKEN" ] ||
       [ -z "${VECTOR_AUTH_USER:-}" ] || [ -z "${VECTOR_AUTH_PASSWORD:-}" ]; then
        echo "Combined deployment requires AUTH_URL, INTERNAL_SERVICE_TOKEN, VECTOR_AUTH_USER and VECTOR_AUTH_PASSWORD from Vault." >&2
        exit 1
    fi
fi

# 1. System Update & Dependencies
if [ "$UPGRADE_ONLY" = true ]; then
    echo -e "${YELLOW}[1/7] Upgrade mode: skipping apt dependency install.${NC}"
else
    echo -e "${GREEN}[1/7] Updating system and installing dependencies...${NC}"
    apt-get update && apt-get install -y python3 curl wget git socat build-essential debian-keyring debian-archive-keyring apt-transport-https dnsutils
fi

# 2. Xray Installation
echo -e "${GREEN}[2/7] Installing Xray...${NC}"
bash -c "$(curl -L https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install

# 3. Runtime Bundle Installation
echo -e "${GREEN}[3/7] Installing prebuilt runtime bundle (custom Caddy + xconnect-edge-agent)...${NC}"

ARCH_RAW="$(uname -m)"
GOARCH="$(detect_goarch)"
echo -e "${GREEN}Detected architecture: ${ARCH_RAW} (GOARCH=${GOARCH})${NC}"
install_prebuilt_runtime_bundle "${GOARCH}"

# Function to compare versions
version_lt() { test "$(echo "$@" | tr " " "\n" | sort -rV | head -n 1)" != "$1"; }

INSTALLED_CADDY_VER="0.0.0"
CADDY_HAS_L4=false
if command -v caddy &> /dev/null; then
    INSTALLED_CADDY_VER=$(caddy version | awk '{print $1}' | sed 's/v//')
    if caddy list-modules 2>/dev/null | grep -q '^layer4'; then
        CADDY_HAS_L4=true
    fi
fi

REQUIRED_VER="2.11.1"
echo "Installed Caddy Version: $INSTALLED_CADDY_VER"
echo "Caddy L4 Module Present: $CADDY_HAS_L4"

if version_lt "$INSTALLED_CADDY_VER" "$REQUIRED_VER" || [ "$CADDY_HAS_L4" != true ]; then
    echo -e "${RED}Installed prebuilt Caddy is missing required version/plugins.${NC}"
    exit 1
else
    echo -e "${GREEN}Caddy bundle verified (v$INSTALLED_CADDY_VER with layer4).${NC}"
fi

caddy version

# Ensure caddy runtime user exists (required by xray-tcp.service + file ownership)
ensure_caddy_user() {
    if id -u caddy >/dev/null 2>&1; then
        XRAY_TCP_USER="caddy"
        return 0
    fi

    echo -e "${YELLOW}caddy user not found, creating system user/group...${NC}"
    if ! getent group caddy >/dev/null 2>&1; then
        groupadd --system caddy || true
    fi

    USER_SHELL="/usr/sbin/nologin"
    if [ ! -x "$USER_SHELL" ]; then
        USER_SHELL="/usr/bin/false"
    fi

    useradd --system --gid caddy --create-home --home-dir /var/lib/caddy --shell "$USER_SHELL" caddy || true
    mkdir -p /var/lib/caddy

    if id -u caddy >/dev/null 2>&1; then
        chown -R caddy:caddy /var/lib/caddy || true
        XRAY_TCP_USER="caddy"
    else
        XRAY_TCP_USER="root"
        echo -e "${YELLOW}Failed to create caddy user. Falling back to root for xray-tcp service.${NC}"
    fi
}

ensure_caddy_user

generate_uuid() {
    if command -v uuidgen >/dev/null 2>&1; then
        uuidgen | tr '[:upper:]' '[:lower:]'
    else
        cat /proc/sys/kernel/random/uuid
    fi
}

ensure_standalone_uuid() {
    mkdir -p "$(dirname "$STANDALONE_UUID_FILE")"

    if [ -f "$STANDALONE_UUID_FILE" ]; then
        STANDALONE_UUID="$(tr -d '[:space:]' < "$STANDALONE_UUID_FILE")"
    fi

    if [ -z "${STANDALONE_UUID:-}" ]; then
        STANDALONE_UUID="$(generate_uuid)"
        printf '%s\n' "$STANDALONE_UUID" > "$STANDALONE_UUID_FILE"
        chmod 0644 "$STANDALONE_UUID_FILE"
    fi
}

render_xray_config_from_template() {
    local template_path="$1"
    local output_path="$2"
    local uuid_value="$3"

    sed "s|{{ UUID }}|${uuid_value}|g" "$template_path" > "$output_path"
}

print_standalone_links() {
    local node_name="${DOMAIN}"
    local xhttp_name="${node_name}-xhttp"
    local tcp_name="${node_name}-tcp"
    local xhttp_link
    local tcp_link

    xhttp_link="vless://${STANDALONE_UUID}@${DOMAIN}:443?encryption=none&security=tls&sni=${DOMAIN}&fp=chrome&type=xhttp&path=%2Fsplit#${xhttp_name}"
    tcp_link="vless://${STANDALONE_UUID}@${DOMAIN}:1443?encryption=none&flow=xtls-rprx-vision&security=tls&sni=${DOMAIN}&fp=chrome&type=tcp#${tcp_name}"

    echo ""
    echo -e "${GREEN}Standalone node import links:${NC}"
    echo "  XHTTP (recommended for OneXray/Xstream):"
    echo "  ${xhttp_link}"
    echo ""
    echo "  TCP Vision:"
    echo "  ${tcp_link}"
    echo ""
    echo "UUID:"
    echo "  ${STANDALONE_UUID}"
}

disable_agent_service_if_present() {
    for service_name in xconnect-edge-agent "$LEGACY_AGENT_SERVICE_NAME" agent-proxy; do
        if systemctl list-unit-files "${service_name}.service" >/dev/null 2>&1 || systemctl is-active "${service_name}.service" >/dev/null 2>&1; then
            systemctl stop "$service_name" >/dev/null 2>&1 || true
            systemctl disable "$service_name" >/dev/null 2>&1 || true
        fi
    done
}

# 4. Configuration Directories
echo -e "${GREEN}[4/7] Setting up configuration directories...${NC}"
mkdir -p /usr/local/etc/xray
mkdir -p /etc/caddy
mkdir -p /etc/caddy/conf.d
if [ "$STANDALONE_MODE" != true ]; then
    mkdir -p /etc/agent
fi

# Co-location hygiene: remove known duplicated PostgreSQL caddy fragments
# generated by older scripts (e.g. postgresql-postgresql-*.caddy).
for stale in /etc/caddy/conf.d/postgresql-postgresql-*.caddy; do
    [ -e "$stale" ] || continue
    rm -f "$stale" || true
done

REPO_SOURCE_DIR="$(fetch_repo_archive)"

# Stop both names during migration so an existing installation cannot report
# the same node from two agent processes after the service rename.
disable_agent_service_if_present

if [ "$STANDALONE_MODE" = true ]; then
    echo -e "${GREEN}[5/7] Preparing standalone Xray configuration...${NC}"
    mkdir -p /usr/local/etc/xray/templates
    cp "${REPO_SOURCE_DIR}"/config/*.template.json /usr/local/etc/xray/templates/
    ensure_standalone_uuid
    render_xray_config_from_template /usr/local/etc/xray/templates/xray.xhttp.template.json /usr/local/etc/xray/config.json "$STANDALONE_UUID"
    echo "Standalone UUID: ${STANDALONE_UUID}"
else
    # 5. Agent Installation
    echo -e "${GREEN}[5/7] Installing/Updating XConnect Edge Agent...${NC}"
    echo "xconnect-edge-agent already installed from runtime bundle."
fi

if [ "$UPGRADE_ONLY" = true ]; then
    post_install_network_optimization

    echo -e "${GREEN}[upgrade-only] Restarting services to apply new binaries...${NC}"
    systemctl restart xray || true
    systemctl restart xray-tcp || true
    systemctl restart caddy || true
    if [ "$STANDALONE_MODE" != true ]; then
        systemctl restart xconnect-edge-agent || true
    fi

    echo -e "${GREEN}Upgrade Complete!${NC}"
    echo -e "Service states:"
    echo -e "  - xray: $(systemctl is-active xray || echo unknown)"
    echo -e "  - xray-tcp: $(systemctl is-active xray-tcp || echo unknown)"
    echo -e "  - caddy: $(systemctl is-active caddy || echo unknown)"
    if [ "$STANDALONE_MODE" != true ]; then
        echo -e "  - xconnect-edge-agent: $(systemctl is-active xconnect-edge-agent || echo unknown)"
    fi
    if [ "$STANDALONE_MODE" = true ]; then
        print_standalone_links
    fi
    exit 0
fi

# Always update templates
mkdir -p /usr/local/etc/xray/templates
cp "${REPO_SOURCE_DIR}"/config/*.template.json /usr/local/etc/xray/templates/
echo "Templates updated at /usr/local/etc/xray/templates/"

if [ "$STANDALONE_MODE" != true ]; then
    # Copy default config if not exists, but don't overwrite user config
    mkdir -p /etc/agent
    if [ ! -f /etc/agent/account-agent.yaml ]; then
        echo "Initializing new configuration file..."
        install -m 0600 "${REPO_SOURCE_DIR}/account-agent.yaml" /etc/agent/account-agent.yaml
        # Initial path setup for templates in the new config
        sed -i 's|config/xray.xhttp.template.json|/usr/local/etc/xray/templates/xray.xhttp.template.json|g' /etc/agent/account-agent.yaml
        sed -i 's|config/xray.tcp.template.json|/usr/local/etc/xray/templates/xray.tcp.template.json|g' /etc/agent/account-agent.yaml
    else
        echo "Configuration file exists at /etc/agent/account-agent.yaml, skipping overwrite."
    fi

    chmod 0600 /etc/agent/account-agent.yaml
    ensure_agent_dynamic_users /etc/agent/account-agent.yaml

    # Apply runtime config from args/env (idempotent)
    if [ -n "${AGENT_REGION:-}" ]; then
        update_agent_metadata /etc/agent/account-agent.yaml region "$AGENT_REGION"
    fi
    if [ -n "${AGENT_POOL:-}" ]; then
        update_agent_metadata /etc/agent/account-agent.yaml pool "$AGENT_POOL"
    fi
    # JSON strings are valid YAML strings and safely preserve token punctuation.
    AGENT_PROXY_DOMAIN="$DOMAIN" AUTH_URL="$AUTH_URL" INTERNAL_SERVICE_TOKEN="$INTERNAL_SERVICE_TOKEN" python3 - <<'PY_CONFIG'
import json, os, pathlib, re
path = pathlib.Path('/etc/agent/account-agent.yaml')
text = path.read_text()
for key, variable in [('id', 'AGENT_PROXY_DOMAIN'), ('controllerUrl', 'AUTH_URL'), ('apiToken', 'INTERNAL_SERVICE_TOKEN')]:
    value = os.environ[variable]
    if value:
        text = re.sub(r'^(\s*' + key + r':\s*).*$',
                      lambda match: match.group(1) + json.dumps(value), text, flags=re.M)
path.write_text(text)
PY_CONFIG
    if [ -n "$BILLING_URL" ]; then
        if grep -q "billing:" /etc/agent/account-agent.yaml; then
            sed -i -E "s|^([[:space:]]*baseURL:[[:space:]]*).*$|\\1\"${BILLING_URL}\"|g" /etc/agent/account-agent.yaml
            sed -i -E "s|^([[:space:]]*enabled:[[:space:]]*).*$|\\1true|g" /etc/agent/account-agent.yaml
        else
            cat >> /etc/agent/account-agent.yaml <<EOF

billing:
  enabled: true
  baseURL: "${BILLING_URL}"
  httpTimeout: 15s
  collectInterval: 1m
  reconcileInterval: 5m
EOF
        fi
    fi
fi

# 6. Caddy Configuration
echo -e "${GREEN}[6/7] Configuration Caddyfile...${NC}"

# Check for existing certificates to reuse
LE_CERT="/etc/letsencrypt/live/${DOMAIN}/fullchain.pem"
LE_KEY="/etc/letsencrypt/live/${DOMAIN}/privkey.pem"
CADDY_CERT_DIR="/var/lib/caddy/.local/share/caddy/certificates/acme-v02.api.letsencrypt.org-directory/${DOMAIN}"
TLS_CONFIG=""
XRAY_CERT="${CADDY_CERT_DIR}/${DOMAIN}.crt"
XRAY_KEY="${CADDY_CERT_DIR}/${DOMAIN}.key"

if [ -n "$VAULT_ADDR" ] || [ -n "$VAULT_TOKEN" ] || [ -n "$VAULT_TLS_SECRET_PATH" ]; then
    TLS_CONFIG="tls /etc/caddy/tls/agent-proxy.crt /etc/caddy/tls/agent-proxy.key"
    XRAY_CERT="/etc/caddy/tls/agent-proxy.crt"
    XRAY_KEY="/etc/caddy/tls/agent-proxy.key"
    mkdir -p /etc/caddy/tls
elif [ -f "$LE_CERT" ] && [ -f "$LE_KEY" ]; then
    echo "Found existing Certbot certificates at $LE_CERT"
    TLS_CONFIG="tls $LE_CERT $LE_KEY"
    XRAY_CERT="$LE_CERT"
    XRAY_KEY="$LE_KEY"
    chmod 755 /etc/letsencrypt || true
    chmod 755 /etc/letsencrypt/live || true
    chmod 755 /etc/letsencrypt/archive || true
    chmod -R +r /etc/letsencrypt/archive/${DOMAIN} || true
    chmod -R +r /etc/letsencrypt/live/${DOMAIN} || true
elif [ -f "/etc/caddy/tls/agent-proxy.crt" ] && [ -f "/etc/caddy/tls/agent-proxy.key" ]; then
    echo "Found existing agent-proxy certificates at /etc/caddy/tls"
    TLS_CONFIG="tls /etc/caddy/tls/agent-proxy.crt /etc/caddy/tls/agent-proxy.key"
    XRAY_CERT="/etc/caddy/tls/agent-proxy.crt"
    XRAY_KEY="/etc/caddy/tls/agent-proxy.key"
    chmod 755 /etc/caddy/tls || true
    chmod 644 /etc/caddy/tls/agent-proxy.crt || true
    chmod 640 /etc/caddy/tls/agent-proxy.key || true
    chown caddy:caddy /etc/caddy/tls/agent-proxy.key || true
elif [ -n "${AGENT_DOMAIN_TLS_DIR:-}" ] && [ -f "${AGENT_DOMAIN_TLS_DIR}/current/fullchain.pem" ] && [ -f "${AGENT_DOMAIN_TLS_DIR}/current/key.pem" ]; then
    echo "Found existing domain TLS certificates under ${AGENT_DOMAIN_TLS_DIR}"
    TLS_CONFIG="tls ${AGENT_DOMAIN_TLS_DIR}/current/fullchain.pem ${AGENT_DOMAIN_TLS_DIR}/current/key.pem"
    XRAY_CERT="${AGENT_DOMAIN_TLS_DIR}/current/fullchain.pem"
    XRAY_KEY="${AGENT_DOMAIN_TLS_DIR}/current/key.pem"
else
    # Check if Caddy already obtained a cert in any directory
    existing_caddy_cert="$(find /var/lib/caddy/.local/share/caddy/certificates -type f -name "${DOMAIN}.crt" 2>/dev/null | head -n 1)"
    if [ -n "$existing_caddy_cert" ] && [ -f "${existing_caddy_cert%.crt}.key" ]; then
        echo "Found existing Caddy-managed certificate at $existing_caddy_cert"
        XRAY_CERT="$existing_caddy_cert"
        XRAY_KEY="${existing_caddy_cert%.crt}.key"
    else
        echo "No existing certificates found at $LE_CERT. Xray TCP will use Caddy-managed cert path: $XRAY_CERT"
    fi
fi

# Ensure Xray TCP template + config always match the chosen cert paths
sed -i -E "s|(\"certificateFile\"[[:space:]]*:[[:space:]]*\")[^\"]*(\")|\\1${XRAY_CERT}\\2|g" /usr/local/etc/xray/templates/xray.tcp.template.json
sed -i -E "s|(\"keyFile\"[[:space:]]*:[[:space:]]*\")[^\"]*(\")|\\1${XRAY_KEY}\\2|g" /usr/local/etc/xray/templates/xray.tcp.template.json
if [ "$STANDALONE_MODE" = true ]; then
    render_xray_config_from_template /usr/local/etc/xray/templates/xray.xhttp.template.json /usr/local/etc/xray/config.json "$STANDALONE_UUID"
    render_xray_config_from_template /usr/local/etc/xray/templates/xray.tcp.template.json /usr/local/etc/xray/tcp-config.json "$STANDALONE_UUID"
else
    cp /usr/local/etc/xray/templates/xray.tcp.template.json /usr/local/etc/xray/tcp-config.json
fi
chown "${XRAY_TCP_USER}:${XRAY_TCP_USER}" /usr/local/etc/xray/tcp-config.json || true
chmod 0644 /usr/local/etc/xray/tcp-config.json
echo "Updated Xray TCP template/config to use: ${XRAY_CERT}"

write_caddy_config
if [ -n "$VAULT_ADDR" ] || [ -n "$VAULT_TOKEN" ] || [ -n "$VAULT_TLS_SECRET_PATH" ]; then
    # The Caddyfile deliberately points at the Vault-synced certificate path.
    # Bootstrap Vault Agent before validating or starting Caddy; otherwise
    # `set -e` aborts on the missing file and never reaches this setup in step 7.
    configure_vault_agent_tls
fi
if command -v caddy >/dev/null 2>&1; then
    caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
fi

PUBLIC_IPV4="$(resolve_public_ipv4 || true)"
if [ -n "${PUBLIC_IPV4:-}" ]; then
    update_cloudflare_dns_for_domain "$DOMAIN" "$PUBLIC_IPV4"
else
    echo -e "${YELLOW}[dns] Could not determine public IPv4 address; skipping automatic Cloudflare DNS update.${NC}"
fi

# 7. Systemd Services
echo -e "${GREEN}[7/7] Installing Systemd Services...${NC}"

# Kill conflicting processes on 80/443
echo "Checking for port conflicts..."
if command -v fuser >/dev/null 2>&1; then
    fuser -k 80/tcp || true
    fuser -k 443/tcp || true
else
    echo -e "${YELLOW}fuser not found, skipping port pre-kill check.${NC}"
fi
# Stop legacy services if known
systemctl stop nginx || true
systemctl stop apache2 || true
systemctl stop caddy || true

# Permissions for config dir
mkdir -p /usr/local/etc/xray
chown -R root:root /usr/local/etc/xray
chmod -R a+rX /usr/local/etc/xray
if [ "$STANDALONE_MODE" != true ]; then
    mkdir -p "${AGENT_DATA_DIR}"
    chown root:root "${AGENT_DATA_DIR}"
    chmod 0755 "${AGENT_DATA_DIR}"
fi
# Legacy helper is no longer needed after switching to direct cert paths.
rm -f /usr/local/bin/sync-agent-certs

# Xray Service (XHTTP/Default)
cat > /etc/systemd/system/xray.service <<EOF
[Unit]
Description=Xray Service (XHTTP)
Documentation=https://github.com/xtls
After=network.target nss-lookup.target

[Service]
User=nobody
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ExecStart=/usr/local/bin/xray run -config /usr/local/etc/xray/config.json
Restart=on-failure
RestartPreventExitStatus=23
LimitNPROC=10000
LimitNOFILE=1000000
Environment=XRAY_LOCATION_ASSET=/usr/local/share/xray/

[Install]
WantedBy=multi-user.target
EOF

# Caddy Service (fallback when distro package service is absent)
if [ ! -f /etc/systemd/system/caddy.service ] && [ ! -f /lib/systemd/system/caddy.service ] && [ ! -f /usr/lib/systemd/system/caddy.service ]; then
cat > /etc/systemd/system/caddy.service <<EOF
[Unit]
Description=Caddy
Documentation=https://caddyserver.com/docs/
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${XRAY_TCP_USER}
Group=${XRAY_TCP_USER}
ExecStart=/usr/bin/caddy run --environ --config /etc/caddy/Caddyfile
ExecReload=/usr/bin/caddy reload --config /etc/caddy/Caddyfile
TimeoutStopSec=5s
LimitNOFILE=1048576
PrivateTmp=true
ProtectSystem=full
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
fi

# Xray TCP Service
cat > /etc/systemd/system/xray-tcp.service <<EOF
[Unit]
Description=Xray Service (TCP)
Documentation=https://github.com/xtls
After=network.target nss-lookup.target

[Service]
User=${XRAY_TCP_USER}
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ExecStart=/usr/local/bin/xray run -config /usr/local/etc/xray/tcp-config.json
Restart=on-failure
RestartPreventExitStatus=23
LimitNPROC=10000
LimitNOFILE=1000000
Environment=XRAY_LOCATION_ASSET=/usr/local/share/xray/

[Install]
WantedBy=multi-user.target
EOF

if [ "$STANDALONE_MODE" != true ]; then
# XConnect Edge Agent service
cat > /etc/systemd/system/xconnect-edge-agent.service <<EOF
[Unit]
Description=XConnect Edge Agent
After=network.target

[Service]
ExecStart=/usr/local/bin/xconnect-edge-agent -config /etc/agent/account-agent.yaml
Restart=always
User=root
WorkingDirectory=${AGENT_DATA_DIR}

[Install]
WantedBy=multi-user.target
EOF
fi

systemctl daemon-reload
systemctl enable xray
systemctl enable xray-tcp
systemctl enable caddy || true
if [ "$STANDALONE_MODE" != true ]; then
    systemctl enable xconnect-edge-agent
fi
systemctl restart xray || true
systemctl restart caddy
systemctl is-active --quiet caddy

if [ ! -f "$XRAY_CERT" ] || [ ! -f "$XRAY_KEY" ]; then
    echo "Waiting for Caddy to obtain certificate for ${DOMAIN}..."
    for i in $(seq 1 30); do
        discovered_cert="$(find /var/lib/caddy/.local/share/caddy/certificates -type f -name "${DOMAIN}.crt" 2>/dev/null | head -n 1)"
        if [ -n "$discovered_cert" ]; then
            discovered_key="${discovered_cert%.crt}.key"
            if [ -f "$discovered_key" ]; then
                XRAY_CERT="$discovered_cert"
                XRAY_KEY="$discovered_key"
                sed -i -E "s|(\"certificateFile\"[[:space:]]*:[[:space:]]*\")[^\"]*(\")|\\1${XRAY_CERT}\\2|g" /usr/local/etc/xray/tcp-config.json
                sed -i -E "s|(\"keyFile\"[[:space:]]*:[[:space:]]*\")[^\"]*(\")|\\1${XRAY_KEY}\\2|g" /usr/local/etc/xray/tcp-config.json
                echo "Discovered Caddy certificate at ${XRAY_CERT}"
                break
            fi
        fi
        sleep 2
    done
fi
systemctl restart xray-tcp || true

if [ "$STANDALONE_MODE" = true ]; then
    echo -e "${GREEN}Standalone mode: skipping xconnect-edge-agent service installation.${NC}"
elif [ -n "$AUTH_URL" ] && [ -n "$INTERNAL_SERVICE_TOKEN" ]; then
    systemctl restart xconnect-edge-agent
    sleep 2
    if systemctl is-active --quiet xconnect-edge-agent; then
        echo -e "${GREEN}xconnect-edge-agent service is active; controller heartbeat acceptance still requires verification.${NC}"
    else
        echo -e "${YELLOW}xconnect-edge-agent status: $(systemctl is-active xconnect-edge-agent)${NC}"
        exit 1
    fi
else
    echo -e "${YELLOW}Skipping xconnect-edge-agent start: AUTH_URL or INTERNAL_SERVICE_TOKEN is missing.${NC}"
fi

post_install_network_optimization

if is_truthy "$INSTALL_OBSERVABILITY"; then
    export AUTH_URL INTERNAL_SERVICE_TOKEN
    if [ -n "$BILLING_URL" ]; then
        export VECTOR_BILLING_INGEST_ENABLED="${VECTOR_BILLING_INGEST_ENABLED:-true}"
        export VECTOR_BILLING_INGEST_URL="${VECTOR_BILLING_INGEST_URL:-${BILLING_URL%/}/v1/ingest/snapshots}"
        export VECTOR_SNAPSHOT_URL="${VECTOR_SNAPSHOT_URL:-http://127.0.0.1:8686}"
    fi
    AGENT_PROXY_DOMAIN="$DOMAIN" bash "${REPO_SOURCE_DIR}/scripts/setup-observability.sh"
fi

echo -e "${GREEN}Installation Complete!${NC}"
if [ "$STANDALONE_MODE" = true ]; then
    echo -e "Standalone config:"
    echo -e "  - xray xhttp: /usr/local/etc/xray/config.json"
    echo -e "  - xray tcp: /usr/local/etc/xray/tcp-config.json"
    echo -e "  - uuid: ${STANDALONE_UUID}"
    print_standalone_links
else
    echo -e "Config file: /etc/agent/account-agent.yaml"
    echo -e "  - agent.id: ${DOMAIN}"
    echo -e "  - controllerUrl: ${AUTH_URL:-<not set>}"
    if [ -n "$INTERNAL_SERVICE_TOKEN" ]; then
        echo -e "  - apiToken: <provided>"
    else
        echo -e "  - apiToken: <not set>"
    fi
    if [ -n "$BILLING_URL" ]; then
        echo -e "  - billing.baseURL: ${BILLING_URL}"
    fi
    if [ -z "$AUTH_URL" ] || [ -z "$INTERNAL_SERVICE_TOKEN" ]; then
        echo -e "Set AUTH_URL and INTERNAL_SERVICE_TOKEN then run: systemctl restart xconnect-edge-agent"
    fi
fi
