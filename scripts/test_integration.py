"""Cross-workstream contracts; no services or production data are touched."""
import json
from pathlib import Path
import re
import tempfile
import sqlite3
import importlib.util
import unittest

ROOT = Path(__file__).resolve().parents[1]

class IntegrationContracts(unittest.TestCase):
    def test_known_security_module_patches_are_selected(self):
        go_mod = (ROOT / 'go.mod').read_text()
        for path, version in [('golang.org/x/crypto', 'v0.57.0'), ('golang.org/x/net', 'v0.60.0'), ('golang.org/x/text', 'v0.42.0')]:
            self.assertIn(path + ' ' + version, go_mod)

    def test_compose_auth_mode_is_understood_by_backend(self):
        source = (ROOT / 'internal/server/auth_config.go').read_text()
        accepted = set(re.findall(r'case "(\w+)":', source))
        compose = (ROOT / 'docker-compose.yml').read_text()
        match = re.search(r'AUTH_MODE: "\$\{AUTH_MODE:-(\w+)\}"', compose)
        assert match is not None
        selected = match.group(1)
        self.assertIn(selected, accepted, 'Compose default must be a validated backend mode')
        self.assertEqual(selected, 'native')
        for file in ['Dockerfile', 'Dockerfile.runtime']:
            self.assertNotIn('AUTH_MODE=token', (ROOT / file).read_text())

    def test_forwarded_proxy_default_is_empty_not_invalid_boolean(self):
        self.assertIn('TRUST_PROXY: "${TRUST_PROXY:-}"', (ROOT / 'docker-compose.yml').read_text())

    def test_invalidate_backup_discards_both_native_session_files(self):
        spec = importlib.util.spec_from_file_location('backup_integration', ROOT / 'scripts/backup.py')
        assert spec is not None and spec.loader is not None
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            store = root / 'store'
            store.mkdir()
            connection = sqlite3.connect(store / 'zapcast.db')
            try:
                connection.execute('CREATE TABLE synthetic(id INTEGER)')
                connection.commit()
            finally:
                connection.close()
            (store / 'session.key').write_text('synthetic-key')
            (store / 'sessions.json').write_text('{"generation":"synthetic","active":{}}')
            module.backup(store, root / 'backup', 'invalidate')
            module.restore(root / 'backup', root / 'restored', 'invalidate')
            self.assertFalse((root / 'restored/session.key').exists())
            self.assertFalse((root / 'restored/sessions.json').exists())

    def test_canonical_browser_command_starts_exact_production_binary(self):
        package = json.loads((ROOT / 'package.json').read_text())
        self.assertEqual(package['scripts']['test:e2e'], 'node e2e/run.mjs')
        runner = (ROOT / 'e2e/run.mjs').read_text()
        self.assertIn('ZAPCAST_E2E_BINARY', runner)
        self.assertIn('spawn(binary', runner)
        self.assertIn('E2E_NATIVE_URL', runner)
        self.assertIn('finally', runner)
        self.assertNotIn('go run', runner)

if __name__ == '__main__':
    unittest.main()
