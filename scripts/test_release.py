import importlib.util
from pathlib import Path
import json
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('release.py')

def load():
    spec = importlib.util.spec_from_file_location('release', SCRIPT)
    module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    return module

class ReleaseTests(unittest.TestCase):
    def test_publication_does_not_finalize_digest_mismatched_draft(self):
        # Synthetic HTTP boundary only: no credentials and no network writes.
        import io
        import os
        import urllib.error
        from unittest.mock import patch
        module = load()
        calls = []
        sha = 'a' * 40
        def respond(request, timeout):
            calls.append(request.method)
            url = request.full_url
            if ('/git/ref/tags/' in url or '/releases/tags/' in url) and 'POST' not in calls:
                raise urllib.error.HTTPError(url, 404, 'synthetic missing', {}, None)
            if request.method == 'POST' and url.endswith('/releases'):
                result = {'id': 1, 'upload_url': 'https://uploads.github.com/synthetic{?name}'}
            elif request.method == 'POST':
                result = {}
            elif '/git/ref/' in url:
                result = {'object': {'type': 'commit', 'sha': sha}}
            else:
                result = {'draft': False, 'assets': [{'name': 'test.bin', 'size': 4, 'digest': 'sha256:wrong'}]}
            return io.BytesIO(json.dumps(result).encode())
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp); (path / 'test.bin').write_bytes(b'test')
            with patch.dict(os.environ, {'GITHUB_ACTIONS': 'true', 'GITHUB_REF': 'refs/heads/master', 'GITHUB_SHA': sha, 'GITHUB_TOKEN': 'synthetic-test-token'}, clear=True), patch.object(module.urllib.request, 'urlopen', side_effect=respond):
                with self.assertRaisesRegex(ValueError, 'digest'):
                    module.publish({'version': 'v1.2.3', 'sha': sha, 'assets': ['test.bin']}, path, 'synthetic/repo')
            self.assertNotIn('PATCH', calls)

    def test_dry_run_rejects_dirty_release_source(self):
        module = load()
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            import hashlib
            (path / 'zapcast-linux-amd64').write_bytes(b'synthetic binary')
            checksum = hashlib.sha256(b'synthetic binary').hexdigest()
            (path / 'SHA256SUMS').write_text(checksum + '  zapcast-linux-amd64\n')
            (path / 'provenance.json').write_text(json.dumps({'version': 'v1.2.3', 'sha': 'a' * 40, 'go': 'go1.26.9', 'tags': ['prod'], 'dirty': True, 'binary_sha256': checksum}))
            with self.assertRaisesRegex(ValueError, 'dirty'):
                module.plan('v1.2.3', 'a' * 40, path)

    def test_dry_run_rejects_invalid_version_and_wrong_sha(self):
        self.assertTrue(SCRIPT.exists(), 'release validation missing')
        module = load()
        with self.assertRaises(ValueError): module.plan('v1.2.3;bad', 'a' * 40, None)
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            (path / 'provenance.json').write_text(json.dumps({'version': 'v1.2.3', 'sha': 'b' * 40}))
            with self.assertRaises(ValueError): module.plan('v1.2.3', 'a' * 40, path)

if __name__ == '__main__': unittest.main()
