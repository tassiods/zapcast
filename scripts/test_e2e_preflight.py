import importlib.util
import os
from pathlib import Path
import unittest
from unittest.mock import patch

class E2EPreflightTests(unittest.TestCase):
    def test_missing_explicit_binary_is_rejected(self):
        script = Path(__file__).with_name('e2e_preflight.py')
        self.assertTrue(script.exists(), 'production E2E preflight missing')
        spec = importlib.util.spec_from_file_location('e2e_preflight', script)
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(ValueError, 'ZAPCAST_E2E_BINARY'):
                module.check()

if __name__ == '__main__': unittest.main()
