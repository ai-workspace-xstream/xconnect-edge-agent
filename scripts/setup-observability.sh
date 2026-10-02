#!/usr/bin/env bash
# Run the canonical collection playbook against this node only.
set -euo pipefail
umask 077

: "${AGENT_PROXY_DOMAIN:?AGENT_PROXY_DOMAIN is required}"
: "${VECTOR_AUTH_USER:?VECTOR_AUTH_USER must be supplied from Vault}"
: "${VECTOR_AUTH_PASSWORD:?VECTOR_AUTH_PASSWORD must be supplied from Vault}"
: "${AUTH_URL:?AUTH_URL is required}"
: "${INTERNAL_SERVICE_TOKEN:?INTERNAL_SERVICE_TOKEN must be supplied from Vault}"
export OBSERVABILITY_ENDPOINT="${OBSERVABILITY_ENDPOINT:-https://observability.svc.plus}"
# Tested canonical playbook revision; override with a reviewed immutable commit.
OBSERVABILITY_PLAYBOOKS_REF="${OBSERVABILITY_PLAYBOOKS_REF:-e166056ecf22800b2beef90a2a90685aae64e94e}"
if [[ ! "$OBSERVABILITY_PLAYBOOKS_REF" =~ ^[a-f0-9]{40}$ ]]; then
    echo 'OBSERVABILITY_PLAYBOOKS_REF must be a full commit SHA.' >&2
    exit 1
fi
if [[ "$OBSERVABILITY_ENDPOINT" != https://* ]]; then
    echo 'OBSERVABILITY_ENDPOINT must use HTTPS.' >&2
    exit 1
fi
if [ "$(id -u)" != 0 ]; then
    echo 'Run this helper as root on the Agent node.' >&2
    exit 1
fi

work_dir="$(mktemp -d /tmp/edge-observability.XXXXXX)"
trap 'rm -rf "$work_dir"' EXIT
curl -fsSL --retry 3 --connect-timeout 10 \
    "https://github.com/ai-workspace-infra/playbooks/archive/${OBSERVABILITY_PLAYBOOKS_REF}.tar.gz" \
    -o "$work_dir/playbooks.tar.gz"
mkdir "$work_dir/playbooks"
tar -xzf "$work_dir/playbooks.tar.gz" -C "$work_dir/playbooks" --strip-components=1
if ! command -v ansible-playbook >/dev/null 2>&1; then
    apt-get update
    apt-get install -y ansible-core
fi

# JSON inventory keeps the caller's node name out of INI/shell syntax. No fleet
# inventory or repository ansible.cfg is loaded. Secrets stay in the environment.
python3 - "$work_dir" <<'PY'
import json, os, pathlib, sys
root = pathlib.Path(sys.argv[1])
node = os.environ['AGENT_PROXY_DOMAIN']

def api_address(filename, fallback):
    config_path = pathlib.Path('/usr/local/etc/xray') / filename
    if not config_path.exists():
        return fallback
    config = json.loads(config_path.read_text())
    api_tag = config.get('api', {}).get('tag')
    for inbound in config.get('inbounds', []):
        if api_tag and inbound.get('tag') == api_tag:
            return '127.0.0.1:' + str(int(inbound['port']))
    raise SystemExit('Xray Stats API inbound missing in ' + filename)

billing_enabled = os.environ.get('VECTOR_BILLING_INGEST_ENABLED', 'false').lower() in ('true', '1', 'yes')
(root / 'inventory.json').write_text(json.dumps({'all': {'children': {
    'agent_proxy': {'hosts': {node: {'ansible_connection': 'local',
                                  'ansible_python_interpreter': '/usr/bin/python3'}}}
}}}))
(root / 'vars.json').write_text(json.dumps({
    'observability_agent_hosts': 'agent_proxy',
    'xray_exporter_hosts': 'agent_proxy',
    'vector_observability_endpoint': os.environ['OBSERVABILITY_ENDPOINT'].rstrip('/'),
    'vector_observability_service_domain': node,
    'vector_observability_environment': os.environ.get('DEPLOY_ENV', 'production'),
    'vector_tls_verify': True,
    'vector_local_observability_enabled': False,
    'vector_billing_ingest_enabled': billing_enabled,
    'xray_exporter_snapshot_features_enabled': billing_enabled,
    'xray_exporter_accounts_base_url': os.environ['AUTH_URL'],
    # Existing managed nodes may still expose StatsService on 28080/28081.
    'xray_exporter_xray_api_addr': api_address('config.json', '127.0.0.1:10086'),
    'xray_exporter_tcp_xray_api_addr': api_address('tcp-config.json', '127.0.0.1:10087'),
    'node_exporter_bind_addr': '127.0.0.1',
    'process_exporter_bind_addr': '127.0.0.1',
    'blackbox_listen': '127.0.0.1:9115',
}))
PY
cat > "$work_dir/ansible.cfg" <<EOF_CONFIG
[defaults]
inventory = $work_dir/inventory.json
roles_path = $work_dir/playbooks/roles
retry_files_enabled = False
stdout_callback = default
EOF_CONFIG
ANSIBLE_CONFIG="$work_dir/ansible.cfg" ansible-playbook \
    "$work_dir/playbooks/deploy_observability_agent.yml" \
    --inventory "$work_dir/inventory.json" --limit "$AGENT_PROXY_DOMAIN" \
    --extra-vars "@$work_dir/vars.json"

for service in xconnect-edge-agent xray xray-tcp caddy node-exporter process-exporter blackbox vector xray-exporter-xhttp xray-exporter-tcp; do
    systemctl is-active --quiet "$service" || {
        echo "Required service is not active: $service" >&2
        exit 1
    }
done
for port in 9100 9256; do
    curl -fsS --max-time 10 "http://127.0.0.1:$port/metrics" -o /dev/null
done

# A real authenticated log-ingest acknowledgement is stronger than an active
# process. It does not establish that all Vector sinks or dashboards are healthy.
python3 <<'PY'
import base64, datetime, json, os, urllib.request, urllib.error
url = os.environ['OBSERVABILITY_ENDPOINT'].rstrip('/') + '/ingest/logs/insert/jsonline?_msg_field=message&_stream_fields=instance'
auth = base64.b64encode((os.environ['VECTOR_AUTH_USER'] + ':' + os.environ['VECTOR_AUTH_PASSWORD']).encode()).decode()
record = {'message': 'xconnect edge observability deployment check',
          'instance': os.environ['AGENT_PROXY_DOMAIN'],
          'timestamp': datetime.datetime.now(datetime.timezone.utc).isoformat()}
request = urllib.request.Request(url, data=(json.dumps(record) + '\n').encode(),
    headers={'Authorization': 'Basic ' + auth, 'Content-Type': 'application/x-ndjson'}, method='POST')
try:
    with urllib.request.urlopen(request, timeout=20) as response:
        print('Observability log ingest accepted (HTTP %s).' % response.status)
except (urllib.error.URLError, OSError):
    raise SystemExit('Observability log ingest failed; check endpoint, Vault credentials and connectivity.')
PY
printf 'Probes active for %s. Verify fresh heartbeat in Accounts and metrics/logs in %s.\n' \
    "$AGENT_PROXY_DOMAIN" "$OBSERVABILITY_ENDPOINT"
