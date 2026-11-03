"""Guard the redacted operator entrypoint and its Vault lookup contract."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]


class InitScriptContractTests(unittest.TestCase):
    def test_init_uses_vault_paths_without_persisting_credentials(self):
        source = (ROOT / 'init.sh').read_text()
        self.assertIn('INTERNAL_SERVICE_TOKEN_VAULT_KV_PATH="kv/data/prod/accounts/runtime"', source)
        self.assertIn('AGENT_PROXY_VECTOR_VAULT_KV_PATH="kv/data/CICD/observability"', source)
        self.assertIn('read -rsp "Vault token: "', source)
        self.assertIn('mktemp /tmp/xconnect-setup-proxy.', source)
        self.assertNotIn('curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh |', source)
        self.assertNotIn('export INTERNAL_SERVICE_TOKEN=', source)
        self.assertNotIn('export VAULT_TOKEN="', source)


if __name__ == '__main__':
    unittest.main()
