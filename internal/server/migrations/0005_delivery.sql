-- Durable delivery state. Legacy rows are deliberately not auto-replayed:
-- the old application cannot prove whether a pending retry reached the provider.
ALTER TABLE broadcasts ADD COLUMN delivery_state TEXT NOT NULL DEFAULT 'legacy';
ALTER TABLE broadcasts ADD COLUMN media_data BLOB;
ALTER TABLE broadcasts ADD COLUMN media_mime TEXT;
ALTER TABLE broadcast_messages ADD COLUMN rendered TEXT;
ALTER TABLE broadcast_messages ADD COLUMN payload BLOB;
ALTER TABLE outbox ADD COLUMN payload BLOB;
UPDATE outbox SET status = 'uncertain', last_error = 'legacy ambiguous attempt; manual review required' WHERE status IN ('pending','dead');
UPDATE broadcasts SET delivery_state = 'interrupted' WHERE id IN (SELECT broadcast_id FROM broadcast_window)
 OR total > (SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id = broadcasts.id);
UPDATE broadcasts SET delivery_state = 'uncertain' WHERE id IN (SELECT broadcast_id FROM outbox WHERE status = 'uncertain');

ALTER TABLE outbox ADD COLUMN provider_id TEXT;

-- Preserve orphan/early confirmations until the durable provider ID is linked.
CREATE TABLE delivery_receipts (
 message_id TEXT PRIMARY KEY,
 status TEXT NOT NULL CHECK(status IN ('delivered','read')),
 delivered_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 read_at TEXT
);
CREATE TRIGGER delivery_receipt_on_insert AFTER INSERT ON broadcast_messages
WHEN NEW.message_id IS NOT NULL
BEGIN
 UPDATE broadcast_messages SET
 status=COALESCE((SELECT status FROM delivery_receipts WHERE message_id=NEW.message_id),status),
 delivered_at=COALESCE(delivered_at,(SELECT delivered_at FROM delivery_receipts WHERE message_id=NEW.message_id)),
 read_at=COALESCE(read_at,(SELECT read_at FROM delivery_receipts WHERE message_id=NEW.message_id))
 WHERE id=NEW.id;
END;
CREATE TRIGGER delivery_receipt_on_link AFTER UPDATE OF message_id ON broadcast_messages
WHEN NEW.message_id IS NOT NULL
BEGIN
 UPDATE broadcast_messages SET
 status=COALESCE((SELECT status FROM delivery_receipts WHERE message_id=NEW.message_id),status),
 delivered_at=COALESCE(delivered_at,(SELECT delivered_at FROM delivery_receipts WHERE message_id=NEW.message_id)),
 read_at=COALESCE(read_at,(SELECT read_at FROM delivery_receipts WHERE message_id=NEW.message_id))
 WHERE id=NEW.id;
END;

-- One provider account is active per application. Keep cadence across campaigns,
-- retries, process restarts and even account re-pairing (conservative).
CREATE TABLE delivery_cadence(id INTEGER PRIMARY KEY CHECK(id=1),next_at INTEGER NOT NULL);
INSERT INTO delivery_cadence VALUES(1,0);
CREATE TABLE delivery_slots(id INTEGER PRIMARY KEY, started_at INTEGER NOT NULL);
CREATE INDEX delivery_slots_time ON delivery_slots(started_at);

UPDATE broadcast_messages SET status='uncertain',error='legacy ambiguous attempt; manual review required' WHERE id IN (SELECT broadcast_message_id FROM outbox WHERE status='uncertain') AND status NOT IN ('delivered','read');

-- Even a legacy failure without an outbox link may be an accepted message or
-- an enqueue gap. Do not recreate its lost personalization/media from templates.
UPDATE broadcast_messages SET status='uncertain',error='legacy failure; rejection unproven and payload/link may be unresolved' WHERE status='failed';
UPDATE broadcasts SET delivery_state='uncertain' WHERE id IN (SELECT broadcast_id FROM broadcast_messages WHERE status='uncertain');
