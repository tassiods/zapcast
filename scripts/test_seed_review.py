import importlib.util
from pathlib import Path
import sqlite3
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('seed_review', ROOT / 'scripts/seed_e2e_review.py')
assert spec is not None and spec.loader is not None
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class NativeReviewSeedSafety(unittest.TestCase):
    def test_rejects_unowned_store_before_any_write(self):
        with tempfile.TemporaryDirectory(prefix='zapcast-native-e2e-') as directory:
            with self.assertRaisesRegex(ValueError, 'runner store'):
                module.seed(directory)
            self.assertFalse((Path(directory) / 'zapcast.db').exists())

    def test_rejects_database_symlink_before_connecting(self):
        with tempfile.TemporaryDirectory(prefix='zapcast-native-e2e-') as directory:
            store = Path(directory)
            (store / '.e2e-owned').write_text('synthetic runner input')
            outside = store / 'outside.db'
            outside.write_text('untouched synthetic marker')
            (store / 'zapcast.db').symlink_to(outside)
            with self.assertRaisesRegex(ValueError, 'redirected'):
                module.seed(store)
            self.assertEqual(outside.read_text(), 'untouched synthetic marker')

    def test_real_schema_seed_is_uncertain_unreplayable_and_one_shot(self):
        with tempfile.TemporaryDirectory(prefix='zapcast-native-e2e-') as directory:
            store = Path(directory)
            (store / '.e2e-owned').write_text('synthetic runner input')
            connection = sqlite3.connect(store / 'zapcast.db')
            try:
                for path in sorted((ROOT / 'internal/server/migrations').glob('*.sql')):
                    connection.executescript(path.read_text())
                connection.commit()
            finally:
                connection.close()
            module.seed(store)
            connection = sqlite3.connect(store / 'zapcast.db')
            try:
                self.assertEqual(connection.execute('SELECT status,payload FROM outbox').fetchall(), [('uncertain', None)])
                self.assertEqual(connection.execute('SELECT COUNT(*) FROM contacts').fetchone()[0], 0)
                with self.assertRaisesRegex(ValueError, 'nonempty'):
                    module.seed(store)
                self.assertEqual(connection.execute('SELECT COUNT(*) FROM broadcasts').fetchone()[0], 2)
            finally:
                connection.close()

if __name__ == '__main__':
    unittest.main()
