"""Contract and CLI tests for --with-xconnect-gateway in scripts/setup-proxy.sh."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts/setup-proxy.sh'
DUMMY_SHA = '0123456789abcdef0123456789abcdef01234567'


class SetupProxyGatewayTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / 'bin'
        self.bin.mkdir()
        self.capture = self.work / 'capture.json'
        self.base_env = {
            'PATH': f'{self.bin}:/usr/bin:/bin',
            'AGENT_SETUP_PROXY_SELF_UPDATE': 'false',
            'AGENT_PLAYBOOKS_REF': DUMMY_SHA,
            'AGENT_GITOPS_REF': DUMMY_SHA,
            'BILLING_SERVICE_BASE_URL': 'https://billing.example.test',
            'TEST_CAPTURE': str(self.capture),
        }
        self._mock('systemctl', 'exit 0\n')
        self._mock('curl', '''while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then
    : > "$2"
    exit 0
  fi
  shift
done
exit 0
''')
        self._mock('tar', 'cat > /dev/null\nexit 0\n')
        self._mock('ansible-playbook', '''python3 - "$@" <<'PY'
import json, os, sys
payload = {
    'argv': sys.argv[1:],
    'env': {k: os.environ.get(k, '') for k in (
        'AGENT_PROXY_DOMAIN',
        'AGENT_PROXY_ENGINE',
        'AGENT_PROXY_OBSERVABILITY_ENABLED',
        'XCONNECT_GATEWAY_ENABLED',
        'XCONNECT_GATEWAY_DOMAIN',
        'XCONNECT_GATEWAY_ID',
        'XCONNECT_GATEWAY_LISTEN_SOCKET',
        'XCONNECT_GATEWAY_SOCKET',
    )},
}
with open(os.environ['TEST_CAPTURE'], 'w') as fh:
    json.dump(payload, fh)
PY
''')

    def _mock(self, name, body):
        path = self.bin / name
        path.write_text('#!/usr/bin/env bash\nset -e\n' + body)
        path.chmod(0o755)

    def _run(self, *args, **env_updates):
        env = dict(self.base_env, **env_updates)
        return subprocess.run(
            ['bash', str(SCRIPT), *args],
            env=env,
            capture_output=True,
            text=True,
        )

    def test_help_lists_gateway_options(self):
        result = self._run('--help')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('--with-xconnect-gateway', result.stdout)
        self.assertIn('--gateway-domain', result.stdout)
        self.assertIn('XCONNECT_GATEWAY_DOMAIN', result.stdout)

    def test_with_xconnect_gateway_requires_domain(self):
        result = self._run('--node', 'jp-xconnect-01.svc.plus', '--with-xconnect-gateway')
        self.assertEqual(result.returncode, 2)
        self.assertIn('XCONNECT_GATEWAY_DOMAIN', result.stderr)

    def test_env_domain_with_flag_and_haproxy(self):
        result = self._run(
            '--node', 'jp-xconnect-01.svc.plus',
            '--proxy-engine', 'haproxy',
            '--with-observability',
            '--with-xconnect-gateway',
            XCONNECT_GATEWAY_DOMAIN='jp-xconnect-gateway-01.svc.plus',
        )
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        captured = json.loads(self.capture.read_text())
        env = captured['env']
        self.assertEqual(env['AGENT_PROXY_DOMAIN'], 'jp-xconnect-01.svc.plus')
        self.assertEqual(env['AGENT_PROXY_ENGINE'], 'haproxy')
        self.assertEqual(env['AGENT_PROXY_OBSERVABILITY_ENABLED'], 'true')
        self.assertEqual(env['XCONNECT_GATEWAY_ENABLED'], 'true')
        self.assertEqual(env['XCONNECT_GATEWAY_DOMAIN'], 'jp-xconnect-gateway-01.svc.plus')
        self.assertEqual(env['XCONNECT_GATEWAY_ID'], 'jp-xconnect-gateway-01')
        self.assertEqual(env['XCONNECT_GATEWAY_LISTEN_SOCKET'], '/run/xconnect-gateway/xray.sock')
        extra_vars = captured['argv'][captured['argv'].index('-e') + 1]
        self.assertIn('agent_proxy_engine=haproxy', extra_vars)
        self.assertIn('xconnect_gateway_enabled=true', extra_vars)

    def test_gateway_domain_flag_enables_gateway_with_custom_socket(self):
        result = self._run(
            '--node', 'ph-xconnect.svc.plus',
            '--gateway-domain', 'ph-xconnect-gateway-01.svc.plus',
            '--gateway-socket', '/run/custom/gw.sock',
        )
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        captured = json.loads(self.capture.read_text())
        env = captured['env']
        self.assertEqual(env['XCONNECT_GATEWAY_ENABLED'], 'true')
        self.assertEqual(env['XCONNECT_GATEWAY_DOMAIN'], 'ph-xconnect-gateway-01.svc.plus')
        self.assertEqual(env['XCONNECT_GATEWAY_ID'], 'ph-xconnect-gateway-01')
        self.assertEqual(env['XCONNECT_GATEWAY_LISTEN_SOCKET'], '/run/custom/gw.sock')
        self.assertEqual(env['XCONNECT_GATEWAY_SOCKET'], '/run/custom/gw.sock')
        extra_vars = captured['argv'][captured['argv'].index('-e') + 1]
        self.assertIn('xconnect_gateway_enabled=true', extra_vars)


if __name__ == '__main__':
    unittest.main()
