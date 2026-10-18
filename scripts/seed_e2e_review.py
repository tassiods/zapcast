#!/usr/bin/env python3
"""Synthetic review INPUTS for a fresh runner-owned store, never provider evidence."""
from contextlib import closing
from pathlib import Path
import sqlite3
import sys


def seed(directory):
    store = Path(directory)
    if not store.name.startswith('zapcast-native-e2e-') or store.is_symlink() or not (store / '.e2e-owned').is_file():
        raise ValueError('only a fresh canonical E2E runner store may be seeded')
    database = store / 'zapcast.db'
    if database.is_symlink() or (store / '.e2e-owned').is_symlink():
        raise ValueError('E2E seed refuses redirected database/ownership marker')
    with closing(sqlite3.connect(database.resolve().as_uri() + '?mode=rw', uri=True, timeout=5)) as connection:
        connection.execute('PRAGMA foreign_keys=ON')
        if connection.execute('SELECT COUNT(*) FROM broadcasts').fetchone()[0]:
            raise ValueError('E2E seed refuses a nonempty application database')
        with connection:
            connection.execute("INSERT INTO broadcasts(id,group_name,message,total,ok_count,fail_count,delivery_state) VALUES(94171,'Synthetic review fixture','synthetic missing payload',1,0,1,'uncertain')")
            connection.execute("INSERT INTO broadcast_messages(id,broadcast_id,contact_name,phone,message_id,status,error) VALUES(94172,94171,'Synthetic review recipient','5511111111111','synthetic-correlation','uncertain','legacy payload not captured')")
            connection.execute("INSERT INTO outbox(id,broadcast_id,broadcast_message_id,contact_name,phone,rendered,attempts,last_error,next_attempt_at,provider_id,status) VALUES(94173,94171,94172,'Synthetic review recipient','5511111111111','synthetic original',0,'legacy payload not captured',CURRENT_TIMESTAMP,'synthetic-correlation','uncertain')")
            connection.execute("INSERT INTO broadcasts(id,group_name,message,total,ok_count,fail_count,delivery_state) VALUES(94174,'Synthetic unresolved fixture','synthetic missing recipients',3,0,0,'interrupted')")


if __name__ == '__main__':
    seed(sys.argv[1])
