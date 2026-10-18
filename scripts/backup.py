#!/usr/bin/env python3
"""Online SQLite snapshots; restore only into a NEW, offline store directory.

Each DB is transactionally consistent, not a cross-database transaction. Quiesce
campaign admission/session changes for a coordinated recovery point. Backups
contain credentials and personal data; encrypt off-host and never commit them.
"""
import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import tempfile
import time


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def describe(path):
    with closing(sqlite3.connect(path.resolve().as_uri() + '?mode=ro', uri=True)) as db:
        if db.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
            raise ValueError('SQLite integrity failure: ' + path.name)
        if db.execute('PRAGMA foreign_key_check').fetchall():
            raise ValueError('SQLite foreign-key failure: ' + path.name)
        tables = db.execute("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name").fetchall()
        counts = {name: db.execute('SELECT count(*) FROM "' + name.replace('"', '""') + '"').fetchone()[0] for (name,) in tables}
        schema = db.execute('SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name').fetchall()
        return {'tables': counts, 'schema': schema}


def backup(store, destination, session_policy):
    store, destination = Path(store).resolve(), Path(destination).absolute()
    if session_policy not in ('preserve', 'invalidate'):
        raise ValueError('explicit session policy required')
    if not store.is_dir() or destination.exists() or store in destination.resolve().parents:
        raise ValueError('source must exist and backup must be new and outside store')
    destination.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix='.backup-', dir=destination.parent))
    os.chmod(stage, 0o700)
    manifest = {'format': 1, 'session_policy': session_policy, 'files': {}}
    try:
        for source in sorted(store.rglob('*')):
            if source.is_symlink():
                raise ValueError('symlinks are not accepted')
            if source.is_dir():
                continue
            relative = source.relative_to(store)
            if source.name.endswith(('-wal', '-shm', '-journal')):
                continue
            if relative.as_posix() in ('session.key', 'sessions.json') and session_policy == 'invalidate':
                continue
            target = stage / relative
            target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            with source.open('rb') as stream:
                sqlite = stream.read(16) == b'SQLite format 3\x00'
            if source.suffix in ('.db', '.sqlite', '.sqlite3') and not sqlite:
                raise ValueError('invalid SQLite store: ' + source.name)
            metadata = {}
            if sqlite:
                deadline = time.monotonic() + 120
                def progress(status, remaining, total):
                    if time.monotonic() > deadline:
                        raise TimeoutError('online backup exceeded 120 seconds')
                with closing(sqlite3.connect(source.resolve().as_uri() + '?mode=ro', uri=True)) as src, closing(sqlite3.connect(target)) as dst:
                    src.backup(dst, pages=256, progress=progress, sleep=0.1)
                metadata = describe(target)
                metadata['kind'] = 'sqlite'
            else:
                shutil.copyfile(source, target)
                metadata['kind'] = 'file'
            os.chmod(target, 0o600)
            metadata['sha256'] = digest(target)
            manifest['files'][relative.as_posix()] = metadata
        if not any(v['kind'] == 'sqlite' for v in manifest['files'].values()):
            raise ValueError('no SQLite databases found')
        (stage / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        os.chmod(stage / 'manifest.json', 0o600)
        os.rename(stage, destination)
    except BaseException:
        shutil.rmtree(stage)
        raise


def restore(source, destination, session_policy):
    source, destination = Path(source).resolve(), Path(destination).absolute()
    if destination.exists():
        raise ValueError('restore target must not exist; never overwrite live data')
    manifest = json.loads((source / 'manifest.json').read_text())
    if manifest['format'] != 1 or manifest['session_policy'] != session_policy:
        raise ValueError('restore session policy must match backup policy')
    if session_policy not in ('preserve', 'invalidate'):
        raise ValueError('explicit session policy required')
    for name, info in manifest['files'].items():
        relative = Path(name)
        file = source / relative
        if relative.is_absolute() or '..' in relative.parts or not file.is_file() or any(p.is_symlink() for p in [file, *file.parents]):
            raise ValueError('unsafe backup member')
        if digest(file) != info['sha256']:
            raise ValueError('backup checksum mismatch: ' + name)
        if info['kind'] == 'sqlite':
            actual = describe(file)
            if json.dumps(actual, sort_keys=True) != json.dumps({k: info[k] for k in ('tables', 'schema')}, sort_keys=True):
                raise ValueError('backup schema/row-count mismatch: ' + name)
    if session_policy == 'invalidate' and {'session.key', 'sessions.json'} & manifest['files'].keys():
        raise ValueError('invalidate snapshot must not contain native session secrets/registry')
    destination.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix='.restore-', dir=destination.parent))
    try:
        for name in manifest['files']:
            target = stage / name
            target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            shutil.copyfile(source / name, target)
            os.chmod(target, 0o600)
        os.rename(stage, destination)
    except BaseException:
        shutil.rmtree(stage)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=['backup', 'restore'])
    parser.add_argument('source', type=Path)
    parser.add_argument('destination', type=Path)
    parser.add_argument('--session-policy', required=True, choices=['preserve', 'invalidate'])
    args = parser.parse_args()
    os.umask(0o077)
    globals()[args.operation](args.source, args.destination, args.session_policy)
    print(args.operation + ' validated; protect the output as secret data')

if __name__ == '__main__':
    main()
