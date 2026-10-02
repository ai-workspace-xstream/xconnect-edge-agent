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
        result = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stderr)
        inventory = json.loads((self.work / 'inventory').read_text())
        hosts = inventory['all']['children']['agent_proxy']['hosts']
        self.assertEqual(list(hosts), ['edge.example.test'])
        self.assertEqual(hosts['edge.example.test']['ansible_connection'], 'local')
        values = json.loads((self.work / 'vars').read_text())
        self.assertTrue(values['vector_tls_verify'])
        self.assertEqual(values['xray_exporter_xray_api_addr'], '127.0.0.1:10086')
        self.assertEqual(values['blackbox_listen'], '127.0.0.1:9115')
        for secret in ('test-only-agent-secret', 'test-only-vector-secret'):
            self.assertNotIn(secret, (self.work / 'vars').read_text() + result.stdout + result.stderr)
        self.assertFalse(Path((self.work / 'inventory_path').read_text().strip()).exists())
        self.assertIn('--quiet blackbox', (self.work / 'services').read_text())

    def test_playbook_failure_propagates_and_cleans_up(self):
        result = self.run_helper(ANSIBLE_EXIT='7')
        self.assertEqual(result.returncode, 7)
        self.assertFalse((self.work / 'services').exists())
        self.assertFalse(Path((self.work / 'inventory_path').read_text().strip()).exists())

    def test_service_and_ingest_failures_propagate(self):
        self.assertNotEqual(self.run_helper(FAIL_SERVICE='vector').returncode, 0)
        self.assertNotEqual(self.run_helper(INGEST_EXIT='9').returncode, 0)

    def test_preflight_rejects_invalid_parameters(self):
        for values in ({'VECTOR_AUTH_PASSWORD': ''}, {'OBSERVABILITY_ENDPOINT': 'http://unsafe.test'},
                       {'OBSERVABILITY_PLAYBOOKS_REF': 'main'}):
            self.assertNotEqual(self.run_helper(**values).returncode, 0)
        self.assertFalse((self.work / 'inventory').exists())

    def test_combined_preflight_before_node_mutations(self):
        result = subprocess.run(['bash', str(ROOT / 'scripts/setup-proxy.sh'),
                                 '--node', 'edge.example.test', '--with-observability'],
                                env=dict(self.env, VECTOR_AUTH_PASSWORD=''), capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Combined deployment requires', result.stderr)
        self.assertFalse((self.work / 'services').exists())


if __name__ == '__main__':
    unittest.main()
