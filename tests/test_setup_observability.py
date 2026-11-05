"""Exercise the installer orchestration without changing a node or using secrets."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / 'bin'
        self.bin.mkdir()
        self.xray_config_dir = self.work / 'xray'
        self.xray_config_dir.mkdir()
        source = self.work / 'repo'
        source.mkdir()
        (source / 'deploy_observability_agent.yml').write_text('---\n[]\n')
        self.archive = self.work / 'repo.tar.gz'
        with tarfile.open(self.archive, 'w:gz') as archive:
            archive.add(source, arcname='playbooks-test')
        self.env = dict(os.environ, PATH=f'{self.bin}:/usr/bin:/bin',
                        AGENT_PROXY_DOMAIN='edge.example.test',
                        AUTH_URL='https://accounts.example.test',
                        INTERNAL_SERVICE_TOKEN='test-only-agent-secret',
                        VECTOR_AUTH_USER='test-user',
                        VECTOR_AUTH_PASSWORD='test-only-vector-secret',
                        XRAY_CONFIG_DIR=str(self.xray_config_dir),
                        TEST_ARCHIVE=str(self.archive), TEST_CAPTURE=str(self.work))
        self.mock('id', 'echo 0\n')
        self.mock('curl', '''while [ "$#" -gt 0 ]; do
if [ "$1" = -o ]; then cp "$TEST_ARCHIVE" "$2"; exit; fi
shift
done
''')
        self.mock('systemctl', '''echo "$*" >> "$TEST_CAPTURE/services"
[ "${FAIL_SERVICE:-}" != "${3:-}" ]
''')
        self.mock('ansible-playbook', '''cp "$ANSIBLE_CONFIG" "$TEST_CAPTURE/config"
while [ "$#" -gt 0 ]; do
case "$1" in
--inventory) cp "$2" "$TEST_CAPTURE/inventory"; echo "$2" > "$TEST_CAPTURE/inventory_path"; shift;;
--extra-vars) cp "${2#@}" "$TEST_CAPTURE/vars"; shift;;
esac
shift
done
exit "${ANSIBLE_EXIT:-0}"
''')
        # Execute inventory authoring with real Python; simulate the network-only
        # ingest probe so no outbound credential-bearing request can be sent.
        self.mock('python3', f'''if [ "$#" -gt 0 ]; then exec '{sys.executable}' "$@"; fi
cat > /dev/null
exit "${{INGEST_EXIT:-0}}"
''')

    def mock(self, name, body):
        path = self.bin / name
        path.write_text('#!/bin/bash\nset -e\n' + body)
        path.chmod(0o755)

    def run_helper(self, **updates):
        return subprocess.run(['bash', str(ROOT / 'scripts/setup-observability.sh')],
                              env=dict(self.env, **updates), capture_output=True, text=True)

    def test_single_node_inventory_and_cleanup(self):
        (self.xray_config_dir / 'config.json').write_text(
            json.dumps({'api': {'listen': '127.0.0.1:10086'}}))
        (self.xray_config_dir / 'tcp-config.json').write_text(
            json.dumps({'api': {'listen': '127.0.0.1:10087'}}))
        result = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stderr)
        inventory = json.loads((self.work / 'inventory').read_text())
        hosts = inventory['all']['children']['agent_proxy']['hosts']
        self.assertEqual(list(hosts), ['edge.example.test'])
        self.assertEqual(hosts['edge.example.test']['ansible_connection'], 'local')
        values = json.loads((self.work / 'vars').read_text())
        self.assertTrue(values['vector_tls_verify'])
        self.assertTrue(values['vector_system_journald_enabled'])
        self.assertEqual(values['xray_exporter_xray_api_addr'], '127.0.0.1:10086')
        self.assertEqual(values['xray_exporter_tcp_xray_api_addr'], '127.0.0.1:10087')
        self.assertFalse(values['xray_exporter_repair_xray_services_enabled'])
        self.assertEqual(values['xray_exporter_tcp_certificate_domain'], 'edge.example.test')
        self.assertEqual(values['xray_exporter_tcp_certificate_file'], '/etc/caddy/tls/agent-proxy.crt')
        self.assertEqual(values['blackbox_listen'], '127.0.0.1:9115')
        for secret in ('test-only-agent-secret', 'test-only-vector-secret'):
            self.assertNotIn(secret, (self.work / 'vars').read_text() + result.stdout + result.stderr)
        self.assertFalse(Path((self.work / 'inventory_path').read_text().strip()).exists())
        self.assertIn('--quiet blackbox', (self.work / 'services').read_text())

    def test_legacy_tagged_api_inbound_is_supported(self):
        (self.xray_config_dir / 'config.json').write_text(json.dumps({
            'api': {'tag': 'legacy-api'},
            'inbounds': [{'tag': 'legacy-api', 'port': 28080}],
        }))
        result = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stderr)
        values = json.loads((self.work / 'vars').read_text())
        self.assertEqual(values['xray_exporter_xray_api_addr'], '127.0.0.1:28080')

    def test_billing_snapshot_fanout_remains_enabled_when_requested(self):
        result = self.run_helper(VECTOR_BILLING_INGEST_ENABLED='true',
                                 BILLING_SERVICE_URL='https://billing.example.test')
        self.assertEqual(result.returncode, 0, result.stderr)
        values = json.loads((self.work / 'vars').read_text())
        self.assertTrue(values['vector_billing_ingest_enabled'])
        self.assertTrue(values['xray_exporter_snapshot_features_enabled'])
        self.assertEqual(values['xray_exporter_vector_snapshot_url'], 'http://127.0.0.1:8686')

    def test_snapshot_handoff_is_empty_when_billing_is_disabled(self):
        result = self.run_helper(VECTOR_BILLING_INGEST_ENABLED='false')
        self.assertEqual(result.returncode, 0, result.stderr)
        values = json.loads((self.work / 'vars').read_text())
        self.assertFalse(values['xray_exporter_snapshot_features_enabled'])
        self.assertEqual(values['xray_exporter_vector_snapshot_url'], '')

    def test_playbook_failure_propagates_and_cleans_up(self):
        result = self.run_helper(ANSIBLE_EXIT='7')
        self.assertEqual(result.returncode, 7)
        self.assertFalse((self.work / 'services').exists())
        self.assertFalse(Path((self.work / 'inventory_path').read_text().strip()).exists())

    def test_service_and_ingest_failures_propagate(self):
        self.assertNotEqual(self.run_helper(FAIL_SERVICE='vector').returncode, 0)
        self.assertNotEqual(self.run_helper(INGEST_EXIT='9').returncode, 0)

    def test_service_self_check_accepts_combined_or_legacy_host_metrics(self):
        combined = self.run_helper(FAIL_SERVICE='node-exporter')
        self.assertEqual(combined.returncode, 0, combined.stderr)
        self.assertIn('PASS service xconnect-host-metrics', combined.stdout)
        legacy = self.run_helper(FAIL_SERVICE='xconnect-host-metrics')
        self.assertEqual(legacy.returncode, 0, legacy.stderr)
        self.assertIn('PASS services node-exporter and process-exporter', legacy.stdout)

    def test_preflight_rejects_invalid_parameters(self):
        for values in ({'VECTOR_AUTH_PASSWORD': ''}, {'OBSERVABILITY_ENDPOINT': 'http://unsafe.test'},
                       {'OBSERVABILITY_PLAYBOOKS_REF': 'main'}):
            self.assertNotEqual(self.run_helper(**values).returncode, 0)
        self.assertFalse((self.work / 'inventory').exists())

    def test_default_playbooks_revision_contains_vault_environment_handoff(self):
        playbooks_revision = '1ae97b6f31e4cd739c7603eaec5966d5ea498fd9'
        helper = (ROOT / 'scripts/setup-observability.sh').read_text()
        installer = (ROOT / 'scripts/setup-proxy.sh').read_text()
        self.assertIn('OBSERVABILITY_PLAYBOOKS_REF', helper)
        self.assertIn(f'AGENT_PLAYBOOKS_REF:-{playbooks_revision}', installer)

    def test_xray_metrics_repair_is_opt_in(self):
        result = self.run_helper(XRAY_EXPORTER_REPAIR_XRAY_SERVICES_ENABLED='true',
                                 XRAY_EXPORTER_XRAY_API_PORT='28080',
                                 XRAY_EXPORTER_TCP_XRAY_API_PORT='28081')
        self.assertEqual(result.returncode, 0, result.stderr)
        values = json.loads((self.work / 'vars').read_text())
        self.assertTrue(values['xray_exporter_repair_xray_services_enabled'])
        self.assertEqual(values['xray_exporter_xray_api_port'], 28080)
        self.assertEqual(values['xray_exporter_tcp_xray_api_port'], 28081)
        self.assertEqual(values['xray_exporter_xray_api_addr'], '127.0.0.1:28080')
        self.assertEqual(values['xray_exporter_tcp_xray_api_addr'], '127.0.0.1:28081')

    def test_billing_ingest_url_is_handed_to_playbooks(self):
        result = self.run_helper(VECTOR_BILLING_INGEST_ENABLED='true',
                                 BILLING_SERVICE_URL='https://billing.example.test')
        self.assertEqual(result.returncode, 0, result.stderr)
        values = json.loads((self.work / 'vars').read_text())
        self.assertTrue(values['vector_billing_ingest_enabled'])
        self.assertEqual(values['vector_billing_ingest_url'],
                         'https://billing.example.test/v1/ingest/snapshots')

    def test_billing_ingest_rejects_missing_endpoint(self):
        result = self.run_helper(VECTOR_BILLING_INGEST_ENABLED='true', BILLING_SERVICE_URL='')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('BILLING_SERVICE_URL is required', result.stderr)


if __name__ == '__main__':
    unittest.main()
