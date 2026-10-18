import importlib.util
from contextlib import closing
from pathlib import Path
import tempfile
import sqlite3
import unittest

SCRIPT = Path(__file__).with_name('backup.py')

class BackupTests(unittest.TestCase):
    def test_backup_and_validation_close_every_sqlite_handle(self):
        from unittest.mock import patch
        spec = importlib.util.spec_from_file_location('backup', SCRIPT)
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        real_connect = sqlite3.connect
        connections = []
        class TrackedConnection(sqlite3.Connection):
            def close(self):
                self.did_close = True
                return super().close()
        def connect(*args, **kwargs):
            connection = real_connect(*args, **kwargs, factory=TrackedConnection)
            connections.append(connection)
            return connection
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); store = root / 'store'; store.mkdir()
            db = real_connect(store / 'campaigns.db')
            db.execute('CREATE TABLE campaigns(id INTEGER)'); db.commit(); db.close()
            with patch.object(module.sqlite3, 'connect', side_effect=connect):
                module.backup(store, root / 'backup', 'invalidate')
                module.restore(root / 'backup', root / 'restore', 'invalidate')
            unclosed = [db for db in connections if not getattr(db, 'did_close', False)]
            for db in unclosed: db.close()
            self.assertEqual(len(unclosed), 0, 'backup/validation leaked SQLite handles')

    def test_restore_rejects_corruption_existing_target_and_policy_mismatch(self):
        spec = importlib.util.spec_from_file_location('backup', SCRIPT)
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); store = root / 'store'; store.mkdir()
            with closing(sqlite3.connect(store / 'campaigns.db')) as db, db:
                db.execute('CREATE TABLE recipients(id INTEGER)')
                db.execute('INSERT INTO recipients VALUES(1)')
            module.backup(store, root / 'backup', 'invalidate')
            target = root / 'existing'; target.mkdir()
            with self.assertRaisesRegex(ValueError, 'must not exist'):
                module.restore(root / 'backup', target, 'invalidate')
            with self.assertRaisesRegex(ValueError, 'policy'):
                module.restore(root / 'backup', root / 'mismatch', 'preserve')
            (root / 'backup/campaigns.db').write_bytes(b'corrupt synthetic snapshot')
            with self.assertRaisesRegex(ValueError, 'checksum'):
                module.restore(root / 'backup', root / 'bad-restore', 'invalidate')
            self.assertFalse((root / 'bad-restore').exists())

    def test_backup_rejects_symlink_and_does_not_publish_partial_snapshot(self):
        spec = importlib.util.spec_from_file_location('backup', SCRIPT)
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); store = root / 'store'; store.mkdir()
            (store / 'unsafe').symlink_to(root / 'elsewhere')
            with self.assertRaisesRegex(ValueError, 'symlinks'):
                module.backup(store, root / 'backup', 'invalidate')
            self.assertFalse((root / 'backup').exists())

    def test_online_backup_preserves_wal_and_unknown_future_schema(self):
        self.assertTrue(SCRIPT.exists(), 'online backup implementation missing')
        spec = importlib.util.spec_from_file_location('backup', SCRIPT)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); store = root / 'store'; store.mkdir()
            db = sqlite3.connect(store / 'contacts.sqlite')
            db.execute('PRAGMA journal_mode=WAL')
            db.execute('CREATE TABLE campaigns(id TEXT, state TEXT)')
            db.execute("INSERT INTO campaigns VALUES ('synthetic', 'uncertain')")
            db.execute('CREATE TABLE future_schema(id INTEGER)')
            db.execute('INSERT INTO future_schema VALUES (42)')
            db.commit()
            other = sqlite3.connect(store / 'whatsmeow.db')
            other.execute('CREATE TABLE synthetic_device(id INTEGER)'); other.commit(); other.close()
            (store / 'session.key').write_bytes(b'synthetic-session-key')
            module.backup(store, root / 'backup', 'invalidate')
            self.assertFalse((root / 'backup/session.key').exists())
            module.restore(root / 'backup', root / 'restore', 'invalidate')
            with closing(sqlite3.connect(root / 'restore/contacts.sqlite')) as restored:
                self.assertEqual(restored.execute('SELECT state FROM campaigns').fetchone()[0], 'uncertain')
                self.assertEqual(restored.execute('SELECT id FROM future_schema').fetchone()[0], 42)
            db.close()
            module.backup(store, root / 'preserved', 'preserve')
            module.restore(root / 'preserved', root / 'restored-preserved', 'preserve')
            self.assertEqual((root / 'restored-preserved/session.key').read_bytes(), b'synthetic-session-key')
            self.assertEqual((root / 'preserved').stat().st_mode & 0o777, 0o700)

if __name__ == '__main__': unittest.main()
