-- Ties each outbox row to the exact broadcast_messages row it retries.
-- MarkOutboxSent used to locate the history row by (broadcast_id, phone,
-- status='failed') ORDER BY id DESC LIMIT 1 — ambiguous if the same phone
-- ever appears twice in one broadcast. New rows carry the precise id;
-- NULL on rows enqueued before this migration keeps the legacy phone
-- matching as a fallback.

ALTER TABLE outbox ADD COLUMN broadcast_message_id INTEGER REFERENCES broadcast_messages(id) ON DELETE SET NULL;
