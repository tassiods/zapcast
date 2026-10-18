import importlib.util
from pathlib import Path
import unittest

class BuildCoverageTests(unittest.TestCase):
    def test_provenance_detects_untracked_source_but_ignores_build_outputs(self):
        import subprocess
        import tempfile
        script = Path(__file__).with_name('build.py')
        spec = importlib.util.spec_from_file_location('builder', script)
        assert spec is not None and spec.loader is not None
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(['git', 'init', '-q', str(root)], check=True)
            (root / '.git/info/exclude').write_text('release-artifacts/\n')
            (root / 'release-artifacts').mkdir()
            (root / 'release-artifacts/synthetic.bin').write_bytes(b'synthetic build output')
            self.assertFalse(module.source_dirty(root))
            (root / 'untracked-source.go').write_text('package synthetic\n')
            self.assertTrue(module.source_dirty(root), 'untracked source must make artifact dirty')

    def test_coverage_rejects_missing_total_and_low_coverage(self):
        script = Path(__file__).with_name('coverage.py')
        self.assertTrue(script.exists(), 'coverage gate missing')
        spec = importlib.util.spec_from_file_location('coverage', script)
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        self.assertTrue(module.check('total: (statements) 72.4%', 65))
        self.assertFalse(module.check('total: (statements) 64.9%', 65))
        with self.assertRaises(ValueError): module.check('no total', 65)

    def test_production_builder_requires_bundle(self):
        script = Path(__file__).with_name('build.py')
        self.assertTrue(script.exists(), 'production builder missing')
        spec = importlib.util.spec_from_file_location('builder', script)
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        with self.assertRaises(ValueError): module.validate_bundle(Path('/nonexistent-zapcast-bundle'))

if __name__ == '__main__': unittest.main()
