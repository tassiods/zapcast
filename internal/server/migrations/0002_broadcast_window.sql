-- broadcast_window persists the rolling-hour cap state per broadcast so a
-- restart mid-pause doesn't reset the window. One row per broadcast,
-- written by sendAll on every successful send and pruned alongside the
-- broadcast row via FK CASCADE.
--
-- window_start is stored in UTC ("YYYY-MM-DD HH:MM:SS"); writers must call
-- .UTC().Format(...) before INSERT/UPDATE so cross-TZ restarts compare
-- against the same wall clock.

CREATE TABLE broadcast_window (
    broadcast_id   INTEGER  PRIMARY KEY REFERENCES broadcasts(id) ON DELETE CASCADE,
    window_start   DATETIME NOT NULL,
    sent_in_window INTEGER  NOT NULL DEFAULT 0
);
