#!/bin/bash
# xconnect-edge-agent 部署脚本
# 使用方法: ./deploy.sh [--dns-only] [--deploy-only] [--uat-shared|--uat-team|--prod|--inventory FILE]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
source "$SCRIPT_DIR/common.sh"

# 加载环境变量
if [ -f "$PROJECT_ROOT/.env" ]; then
    echo "加载环境变量..."
    set -a
    source "$PROJECT_ROOT/.env" 2>/dev/null || true
    set +a
fi

# 参数解析
DNS_ONLY=false
DEPLOY_ONLY=false
INVENTORY_FILE="$SCRIPT_DIR/inventory.ini"
AGENT_VARS_FILE="$SCRIPT_DIR/vars/xconnect_edge_agent.yml"
TARGET_ENVIRONMENT="inventory.ini"
SKIP_DNS=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --dns-only)
            DNS_ONLY=true
            shift
            ;;
        --deploy-only)
            DEPLOY_ONLY=true
            shift
            ;;
        --prod)
            INVENTORY_FILE="$SCRIPT_DIR/inventory.prod.ini"
            AGENT_VARS_FILE="$SCRIPT_DIR/vars/xconnect_edge_agent.prod.yml"
            TARGET_ENVIRONMENT="production"
            shift
            ;;
        --uat-shared)
            INVENTORY_FILE="$SCRIPT_DIR/inventory.uat-shared.ini"
            AGENT_VARS_FILE="$SCRIPT_DIR/vars/xconnect_edge_agent.uat-shared.yml"
            TARGET_ENVIRONMENT="uat-shared"
            SKIP_DNS=true
            shift
            ;;
        --uat-team)
            INVENTORY_FILE="$SCRIPT_DIR/inventory.uat-team.ini"
            AGENT_VARS_FILE="$SCRIPT_DIR/vars/xconnect_edge_agent.uat-team.yml"
            TARGET_ENVIRONMENT="uat-team"
            SKIP_DNS=true
            shift
            ;;
        --inventory)
            if [ $# -lt 2 ]; then
                echo "错误: --inventory 需要一个文件路径"
                exit 1
            fi
            INVENTORY_FILE="$(resolve_inventory_path "$2")"
            TARGET_ENVIRONMENT="custom"
            shift 2
            ;;
        *)
            echo "未知参数: $1"
            echo "使用方法: $0 [--dns-only] [--deploy-only] [--uat-shared|--uat-team|--prod|--inventory FILE]"
            exit 1
            ;;
    esac
done

if [ ! -f "$INVENTORY_FILE" ]; then
    echo "错误: inventory 文件不存在: $INVENTORY_FILE"
    exit 1
fi

if [ ! -f "$AGENT_VARS_FILE" ]; then
    echo "错误: vars 文件不存在: $AGENT_VARS_FILE"
    exit 1
fi

HOST_ALIAS="$(inventory_first_host_alias "$INVENTORY_FILE" || true)"
HOST="$(inventory_target_host "$INVENTORY_FILE" || true)"
SSH_USER="$(inventory_target_user "$INVENTORY_FILE" || true)"

if [ -z "$HOST_ALIAS" ] || [ -z "$HOST" ]; then
    echo "错误: inventory 未配置 xconnect_edge_agent 主机: $INVENTORY_FILE"
    echo "请先填写 inventory 主机，或显式使用 --prod / --inventory。"
    exit 1
fi

echo ""
echo "=== xconnect-edge-agent 部署 ==="
echo "目标环境: $TARGET_ENVIRONMENT"
echo "Inventory: $INVENTORY_FILE"
echo "Vars: $AGENT_VARS_FILE"
echo "目标主机: $HOST_ALIAS ($HOST)"
echo "前提: 目标主机必须已通过 scripts/setup-proxy.sh 完成 xray/xray-tcp/caddy 预配置"
if [ "$SKIP_DNS" = true ]; then
    echo "DNS: UAT target 不声明或修改 Cloudflare DNS；仅执行 agent 部署"
fi
echo ""

# 进入 ansible 目录
cd "$SCRIPT_DIR"
export ANSIBLE_CONFIG="$SCRIPT_DIR/ansible.cfg"
export AGENT_VARS_FILE

# 步骤 1: 更新 DNS
if [ "$DEPLOY_ONLY" = false ] && [ "$SKIP_DNS" = false ]; then
    echo "=== 步骤 1/2: 更新 DNS 记录 ==="
    if [ -z "${CLOUDFLARE_API_TOKEN:-}" ]; then
        echo "错误: CLOUDFLARE_API_TOKEN 未设置"
        echo "请导出环境变量: export CLOUDFLARE_API_TOKEN=xxx"
        exit 1
    fi

    ansible-playbook -i "$INVENTORY_FILE" playbooks/update_cloudflare_dns.yml -v

    if [ "$DNS_ONLY" = true ]; then
        echo ""
        echo "✓ DNS 更新完成"
        exit 0
    fi
    echo ""
fi

# 步骤 2: 部署 agent
echo "=== 步骤 2/2: 部署 xconnect-edge-agent ==="
if [ -z "${INTERNAL_SERVICE_TOKEN:-}" ]; then
    echo "错误: INTERNAL_SERVICE_TOKEN 未设置"
    echo "请导出环境变量: export INTERNAL_SERVICE_TOKEN=xxx"
    exit 1
fi

ansible-playbook -i "$INVENTORY_FILE" playbooks/deploy_xconnect_edge_agent.yml -v

echo ""
echo "=== 部署完成 ==="
echo "验证命令:"
echo "  ssh ${SSH_USER}@${HOST} 'systemctl status xconnect-edge-agent'"
echo "  ssh ${SSH_USER}@${HOST} 'journalctl -u xconnect-edge-agent -f'"
