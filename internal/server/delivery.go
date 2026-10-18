// SPDX-FileCopyrightText: 2026 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"google.golang.org/protobuf/proto"
)

// createDelivery persists the complete recipient snapshot and retry/history
// linkage in one transaction. No provider operation may precede this commit.
func (s *ContactStore) createDelivery(ctx context.Context, id int64, label, message string, contacts []Contact, vars map[string]string, media mediaPayload) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if id == 0 {
		res, e := tx.ExecContext(ctx, `INSERT INTO broadcasts(group_name,message,total,ok_count,fail_count,media_type,media_name,delivery_state,media_data,media_mime) VALUES(?,?,?,0,0,?,?,'active',?,?)`, label, message, len(contacts), media.kind, media.name, media.data, media.mime)
		if e != nil {
			return 0, e
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	} else {
		var n int
		var state string
		if err = tx.QueryRowContext(ctx, `SELECT delivery_state FROM broadcasts WHERE id=?`, id).Scan(&state); err != nil {
			return 0, err
		}
		if state != "legacy" {
			return id, tx.Commit()
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=?`, id).Scan(&n); err != nil {
			return 0, err
		}
		if n > 0 {
			return id, tx.Commit()
		}
		if _, err = tx.ExecContext(ctx, `UPDATE broadcasts SET delivery_state='active',media_data=?,media_mime=? WHERE id=?`, media.data, media.mime, id); err != nil {
			return 0, err
		}
	}
	for _, c := range contacts {
		rendered := renderTemplate(message, c, vars)
		payload, err := proto.Marshal(composeMessage(rendered, mediaPayload{}, whatsmeow.UploadResponse{}))
		if err != nil {
			return 0, err
		}
		state := "pending"
		if media.present() {
			state = "preparing"
			payload = nil
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO broadcast_messages(broadcast_id,contact_name,phone,status,rendered,payload) VALUES(?,?,?,'pending',?,?)`, id, c.Name, c.Phone, rendered, payload)
		if err != nil {
			return 0, err
		}
		mid, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO outbox(broadcast_id,broadcast_message_id,contact_name,phone,rendered,payload,media_kind,media_mime,media_name,next_attempt_at,status,provider_id) VALUES(?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,?,?)`, id, mid, c.Name, c.Phone, rendered, payload, media.kind, media.mime, media.name, state, newDeliveryID()); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func (app *App) prepareDelivery(ctx context.Context, id int64, client WAClient) error {
	app.broadcaster.prepareMu.Lock()
	defer app.broadcaster.prepareMu.Unlock()
	var data []byte
	var mime, name, kind string
	err := app.contacts.db.QueryRowContext(ctx, `SELECT media_data,COALESCE(media_mime,''),COALESCE(media_name,''),COALESCE(media_type,'') FROM broadcasts WHERE id=?`, id).Scan(&data, &mime, &name, &kind)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	mt, _, _ := classifyMedia(mime)
	upctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	resp, err := client.Upload(upctx, data, mt)
	cancel()
	if err != nil {
		return err
	}
	pctx, pcancel := persistContext()
	defer pcancel()
	tx, err := app.contacts.db.BeginTx(pctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(pctx, `SELECT id,broadcast_message_id,rendered FROM outbox WHERE broadcast_id=? AND status='preparing'`, id)
	if err != nil {
		return err
	}
	type item struct {
		id, mid  int64
		rendered string
	}
	var items []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.mid, &i.rendered); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		payload, err := proto.Marshal(buildMediaMessage(i.rendered, kind, resp, mime, name))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(pctx, `UPDATE outbox SET payload=?,status='pending' WHERE id=?`, payload, i.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(pctx, `UPDATE broadcast_messages SET payload=? WHERE id=?`, payload, i.mid); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(pctx, `UPDATE broadcasts SET media_data=NULL WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ContactStore) deliveryRows(ctx context.Context, id int64) ([]OutboxRow, error) {
	// FetchDueOutbox remains bounded for the worker. Initial fan-out is scoped
	// to the durable recipient snapshot, not a volatile contact list.
	rows, err := s.db.QueryContext(ctx, `SELECT id,broadcast_id,broadcast_message_id,contact_name,phone,rendered,payload,COALESCE(provider_id,'') FROM outbox WHERE broadcast_id=? AND status='pending' ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		if err = rows.Scan(&r.ID, &r.BroadcastID, &r.MessageRowID, &r.ContactName, &r.Phone, &r.Rendered, &r.Payload, &r.ProviderID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *ContactStore) reconcileDelivery(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, reconcileDeliverySQL, id, id, id)
	return err
}

// claimDelivery is the durable no-return boundary. After it commits, a crash
// or any bookkeeping failure must be reviewed, never automatically replayed.
func (s *ContactStore) claimDelivery(ctx context.Context, row OutboxRow) (bool, error) {
	claimed, _, err := s.claimDeliveryAt(ctx, row, time.Now(), 0, 0)
	return claimed, err
}

// The cadence reservation and in-flight boundary share a transaction. Waiting
// never consumes an attempt or a slot; every RPC path uses this one admission.
func (s *ContactStore) claimDeliveryAt(ctx context.Context, row OutboxRow, now time.Time, delay time.Duration, cap int) (bool, time.Duration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	var eligible int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE id=? AND status='pending' AND next_attempt_at<=? AND broadcast_id IN (SELECT id FROM broadcasts WHERE delivery_state<>'cancelled')`, row.ID, now.UTC().Format("2006-01-02 15:04:05")).Scan(&eligible)
	if err != nil || eligible == 0 {
		return false, 0, err
	}
	// Replay orphan receipts before admitting the provider call. The link
	// trigger applies their monotonic status inside this same transaction.
	if _, err = tx.ExecContext(ctx, linkDeliveryReceiptSQL+` AND id=(SELECT broadcast_message_id FROM outbox WHERE id=?)`, row.ID); err != nil {
		return false, 0, err
	}
	var confirmed int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox o JOIN broadcast_messages m ON m.id=o.broadcast_message_id WHERE o.id=? AND m.status IN ('sent','delivered','read')`, row.ID).Scan(&confirmed); err != nil {
		return false, 0, err
	}
	if confirmed > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM outbox WHERE id=?`, row.ID); err != nil {
			return false, 0, err
		}
		if _, err = tx.ExecContext(ctx, reconcileDeliverySQL, row.BroadcastID, row.BroadcastID, row.BroadcastID); err != nil {
			return false, 0, err
		}
		return false, 0, tx.Commit()
	}
	var next int64
	if err = tx.QueryRowContext(ctx, `SELECT next_at FROM delivery_cadence WHERE id=1`).Scan(&next); err != nil {
		return false, 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM delivery_slots WHERE started_at<=?`, now.Add(-time.Hour).UnixNano()); err != nil {
		return false, 0, err
	}
	if cap > 0 {
		var count int
		var earliest sql.NullInt64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),MIN(started_at) FROM delivery_slots`).Scan(&count, &earliest); err != nil {
			return false, 0, err
		}
		if count >= cap && earliest.Valid && earliest.Int64+int64(time.Hour) > next {
			next = earliest.Int64 + int64(time.Hour)
		}
	}
	if next > now.UnixNano() {
		return false, time.Duration(next - now.UnixNano()), nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE outbox SET status='inflight' WHERE id=? AND status='pending'`, row.ID)
	if err != nil {
		return false, 0, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE broadcast_messages SET status='inflight',message_id=(SELECT provider_id FROM outbox WHERE id=?) WHERE id=? AND status IN ('pending','failed')`, row.ID, row.MessageRowID); err != nil {
		return false, 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE delivery_cadence SET next_at=? WHERE id=1`, now.Add(delay).UnixNano()); err != nil {
		return false, 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_slots(started_at) VALUES(?)`, now.UnixNano()); err != nil {
		return false, 0, err
	}
	return true, 0, tx.Commit()
}

func (s *ContactStore) uncertainDelivery(ctx context.Context, row OutboxRow, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Resolution owns the outcome once it removes this attempt's queue row.
	// A cancelled provider call can return long after that transaction commits.
	var owned int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE id=? AND broadcast_id=? AND COALESCE(broadcast_message_id,0)=? AND (?='' OR provider_id=?) AND status IN ('pending','inflight','uncertain') AND (?<>? OR status IN ('inflight','uncertain'))`, row.ID, row.BroadcastID, row.MessageRowID, row.ProviderID, row.ProviderID, row.ExpectedStatus, attemptOwnership).Scan(&owned); err != nil {
		return err
	}
	if owned == 0 {
		return nil
	}
	var confirmed int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM broadcast_messages WHERE id=? AND status IN ('sent','delivered','read')`, row.MessageRowID).Scan(&confirmed); err != nil {
		return err
	}
	if confirmed > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM outbox WHERE id=?`, row.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, reconcileDeliverySQL, row.BroadcastID, row.BroadcastID, row.BroadcastID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE outbox SET status='uncertain' ,last_error=? WHERE id=?`, reason, row.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE broadcast_messages SET status='uncertain',error=? WHERE id=? AND status NOT IN ('delivered','read')`, reason, row.MessageRowID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE broadcasts SET delivery_state=CASE WHEN delivery_state='cancelled' THEN delivery_state ELSE 'uncertain' END WHERE id=?`, row.BroadcastID); err != nil {
		return err
	}
	// Other untouched recipients remain safe to resume. Uncertainty is per intent.
	return tx.Commit()
}

func (s *ContactStore) cancelDelivery(ctx context.Context, id int64, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	where := `delivery_state IN ('active','interrupted','uncertain')`
	args := []any{}
	if id > 0 {
		where = `id=?`
		args = append(args, id)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE broadcasts SET delivery_state='cancelled' WHERE `+where, args...); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE broadcast_messages SET status=CASE WHEN status='inflight' THEN 'uncertain' ELSE 'cancelled' END,error=? WHERE broadcast_id IN (SELECT id FROM broadcasts WHERE delivery_state='cancelled') AND status IN ('pending','inflight','failed')`, reason); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE outbox SET status=CASE WHEN status='inflight' THEN 'uncertain' ELSE 'cancelled' END,last_error=? WHERE broadcast_id IN (SELECT id FROM broadcasts WHERE delivery_state='cancelled') AND status IN ('pending','preparing','inflight')`, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// RejectedDeliveryError may only wrap a provider result that proves no message
// was accepted. Network/ACK/context errors do not establish that fact.
type RejectedDeliveryError struct{ Err error }

func (e *RejectedDeliveryError) Error() string { return e.Err.Error() }
func (e *RejectedDeliveryError) Unwrap() error { return e.Err }

func newDeliveryID() string {
	// The provider supports application-supplied IDs. Random identity is created
	// once at intent commit, never regenerated for retry or recovery.
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return whatsmeow.WebMessageIDPrefix + strings.ToUpper(hex.EncodeToString(b))
}

// recoverDeliveries runs once before any worker admits provider calls. Only
// never-started, fully captured new intents are safe to resume automatically.
func (s *ContactStore) recoverDeliveries(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		linkDeliveryReceiptSQL,
		`UPDATE broadcasts SET delivery_state='interrupted' WHERE delivery_state='active'`,
		`DELETE FROM outbox WHERE broadcast_message_id IN (SELECT id FROM broadcast_messages WHERE status IN ('sent','delivered','read'))`,
		`UPDATE outbox SET status='uncertain',last_error='interrupted in flight; remote outcome unknown' WHERE status='inflight'`,
		`UPDATE broadcast_messages SET status='uncertain',error='interrupted in flight; remote outcome unknown' WHERE status='inflight'`,
		`UPDATE outbox SET status='cancelled' WHERE status IN ('pending','preparing') AND broadcast_id IN (SELECT id FROM broadcasts WHERE delivery_state='cancelled')`,
		`UPDATE broadcasts SET delivery_state='uncertain' WHERE delivery_state NOT IN ('cancelled','legacy') AND (EXISTS(SELECT 1 FROM outbox WHERE broadcast_id=broadcasts.id AND status='uncertain') OR EXISTS(SELECT 1 FROM broadcast_messages WHERE broadcast_id=broadcasts.id AND status='uncertain'))`,
		strings.ReplaceAll(strings.ReplaceAll(reconcileDeliverySQL, "broadcast_id=?", "broadcast_id=broadcasts.id"), "WHERE id=?", "WHERE delivery_state<>'legacy'"),
	}
	for _, q := range statements {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Only establish a missing/unconfirmed identity when a durable receipt
// already proves its outcome. Never replace an operator-confirmed identity.
const linkDeliveryReceiptSQL = `UPDATE broadcast_messages SET message_id=(SELECT provider_id FROM outbox WHERE broadcast_message_id=broadcast_messages.id)
 WHERE status NOT IN ('sent','delivered','read') AND EXISTS(SELECT 1 FROM outbox o JOIN delivery_receipts r ON r.message_id=o.provider_id WHERE o.broadcast_message_id=broadcast_messages.id)`

const reconcileDeliverySQL = `UPDATE broadcasts SET
 ok_count=(SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=? AND status IN ('sent','delivered','read')),
 fail_count=(SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=? AND status IN ('failed','cancelled','uncertain')),
 delivery_state=CASE
 WHEN delivery_state IN ('cancelled','legacy') THEN delivery_state
 WHEN EXISTS(SELECT 1 FROM outbox WHERE broadcast_id=broadcasts.id AND status='uncertain') OR EXISTS(SELECT 1 FROM broadcast_messages WHERE broadcast_id=broadcasts.id AND status='uncertain') THEN 'uncertain'
 WHEN EXISTS(SELECT 1 FROM outbox WHERE broadcast_id=broadcasts.id AND status IN ('pending','preparing','inflight')) THEN delivery_state
 WHEN total>(SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=broadcasts.id) THEN 'interrupted'
 ELSE 'completed' END WHERE id=?`
