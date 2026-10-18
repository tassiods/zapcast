"""Regression checks for fail-closed delivery configuration."""
import json
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]

class DeliveryTests(unittest.TestCase):
    def test_ci_cancels_only_superseded_pushes_and_pull_requests(self):
        ci = (ROOT / '.github/workflows/ci.yml').read_text()
        release = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('group: ci-${{ github.workflow }}-${{ github.ref }}', ci)
        self.assertIn("cancel-in-progress: ${{ github.event_name == 'push' || github.event_name == 'pull_request' }}", ci)
        self.assertIn('cancel-in-progress: false', release)

    def test_supported_exact_toolchain(self):
        self.assertIn('go 1.26.9', (ROOT / 'go.mod').read_text())
        package = json.loads((ROOT / 'package.json').read_text())
        self.assertTrue(all(re.fullmatch(r'\d+\.\d+\.\d+', v) for v in package['devDependencies'].values()))

    def test_lockfile_is_mandatory(self):
        for file in (ROOT / '.github/workflows').glob('*.yml'):
            self.assertNotIn('npm ci ||', file.read_text(), str(file))
            for action in re.findall(r'uses: ([^\s]+)', file.read_text()):
                if not action.startswith('./'):
                    self.assertRegex(action, r'@[a-f0-9]{40}$')

    def test_patched_go_pin_is_consistent_across_artifact_gates(self):
        for file in ['scripts/build.py', 'scripts/e2e_preflight.py', 'scripts/release.py',
                     '.github/workflows/ci.yml', 'Dockerfile']:
            source = (ROOT / file).read_text()
            self.assertIn('1.26.9', source, file)
            self.assertNotIn('1.26.8', source, file)
        pins = json.loads((ROOT / 'scripts/delivery-pins.json').read_text())
        self.assertEqual(pins['go'], '1.26.9')
        image = 'golang:1.26.9-alpine'
        self.assertIn(image + '@' + pins['containers'][image]['digest'], (ROOT / 'Dockerfile').read_text())

    def test_compose_security_is_explicit(self):
        compose = (ROOT / 'docker-compose.yml').read_text()
        for variable in ['AUTH_MODE', 'AUTH_TOKEN', 'FRONTEND_ORIGIN', 'TRUST_PROXY']:
            self.assertRegex(compose, rf'(?m)^      {variable}:.*\$\{{{variable}')
        self.assertIn('read_only: true', compose)
        self.assertIn('no-new-privileges:true', compose)

    def test_release_graph_consumes_same_sha_tested_artifact(self):
        ci = (ROOT / '.github/workflows/ci.yml').read_text()
        release = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('workflow_call:', ci)
        self.assertIn('needs: gates', release)
        self.assertIn('uses: ./.github/workflows/ci.yml', release)
        self.assertNotIn('go build', release)
        self.assertNotIn('npm run build', release)
        for workflow in [ci, release]:
            self.assertIn('ref: ${{ github.sha }}', workflow)
            self.assertIn('tested-release-${{ github.sha }}', workflow)

    def test_prod_docker(self):
        docker = (ROOT / 'Dockerfile').read_text()
        self.assertIn('npm ci', docker)
        self.assertIn('-tags prod', docker)
        self.assertIn('versionLdflag=', docker)
        for base in re.findall(r'(?m)^FROM (\S+)', docker):
            self.assertRegex(base, r'@sha256:[a-f0-9]{64}$')

if __name__ == '__main__':
    unittest.main()
