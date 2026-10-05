-- outbox holds messages that failed the live send. A background worker
-- (see outbox.go) wakes on an interval, picks rows whose next_attempt_at
-- has elapsed, retries them through whatsmeow, and either marks them
-- delivered (deleting the row) or bumps attempts + reschedules with
-- exponential backoff. Rows that exhaust max_attempts are kept with
-- status='dead' so operators can inspect them.
--
-- The rendered message body is captured at the moment the send failed —
-- template variables had already been substituted, so the retry doesn't
-- need access to the broadcast row's raw template. media_url + media_key
-- carry whatsmeow's UploadResponse fields so the worker can reuse the
-- single Meta-side upload instead of re-uploading per recipient.
--
-- next_attempt_at and created_at are stored in UTC; writers call
-- .UTC().Format(...) before INSERT/UPDATE and FetchDueOutbox formats the
-- comparison cutoff the same way. Keep that invariant for any future
-- column that holds a time.Time.

CREATE TABLE outbox (
    id               INTEGER  PRIMARY KEY AUTOINCREMENT,
    broadcast_id     INTEGER  NOT NULL REFERENCES broadcasts(id) ON DELETE CASCADE,
    contact_name     TEXT     NOT NULL,
    phone            TEXT     NOT NULL,
    rendered         TEXT     NOT NULL,
    media_kind       TEXT,
    media_mime       TEXT,
    media_name       TEXT,
    media_url        TEXT,
    media_direct_path TEXT,
    media_key        BLOB,
    media_file_sha   BLOB,
    media_enc_sha    BLOB,
    media_file_len   INTEGER  NOT NULL DEFAULT 0,
    attempts         INTEGER  NOT NULL DEFAULT 0,
    last_error       TEXT,
    next_attempt_at  DATETIME NOT NULL,
    status           TEXT     NOT NULL DEFAULT 'pending', -- pending | dead
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_outbox_due ON outbox(status, next_attempt_at);
CREATE INDEX idx_outbox_broadcast ON outbox(broadcast_id);
