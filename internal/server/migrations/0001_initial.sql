-- Initial schema. One bundle: contacts + groups + memberships, template
-- vars, broadcast history and per-message receipts. Media columns ship
-- baked in (the v1.2 split-out migration is folded into this one since the
-- product hasn't shipped a release yet).

CREATE TABLE contacts (
    phone TEXT PRIMARY KEY,
    name  TEXT NOT NULL
);

CREATE TABLE contact_groups (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE group_members (
    group_id INTEGER NOT NULL REFERENCES contact_groups(id) ON DELETE CASCADE,
    phone    TEXT    NOT NULL REFERENCES contacts(phone)       ON DELETE CASCADE ON UPDATE CASCADE,
    PRIMARY KEY (group_id, phone)
);
CREATE INDEX idx_group_members_phone ON group_members(phone);

CREATE TABLE template_vars (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE broadcasts (
    id         INTEGER  PRIMARY KEY AUTOINCREMENT,
    group_name TEXT     NOT NULL,
    message    TEXT     NOT NULL,
    total      INTEGER  NOT NULL,
    ok_count   INTEGER  NOT NULL,
    fail_count INTEGER  NOT NULL,
    sent_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    media_type TEXT,
    media_name TEXT
);
CREATE INDEX idx_broadcasts_sent_at ON broadcasts(sent_at DESC);

CREATE TABLE broadcast_messages (
    id           INTEGER  PRIMARY KEY AUTOINCREMENT,
    broadcast_id INTEGER  NOT NULL REFERENCES broadcasts(id) ON DELETE CASCADE,
    contact_name TEXT     NOT NULL,
    phone        TEXT     NOT NULL,
    message_id   TEXT,
    status       TEXT     NOT NULL, -- sent | failed | delivered | read
    error        TEXT,
    sent_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    delivered_at DATETIME,
    read_at      DATETIME
);
CREATE INDEX idx_bmsg_broadcast  ON broadcast_messages(broadcast_id);
CREATE INDEX idx_bmsg_message_id ON broadcast_messages(message_id);
