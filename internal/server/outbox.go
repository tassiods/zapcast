// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Outbox + retry worker. Failed sends are enqueued and retried with
// exponential backoff.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const (
	outboxBackoffBase  = 30 * time.Second
	outboxMaxAttempts  = 5
	outboxPollInterval = 15 * time.Second
)

// OutboxRow is the persisted shape of one pending retry. media_* fields
// are zero when the original send was text-only.
type OutboxRow struct {
	ExpectedStatus  string // compare-and-swap: review status, or attemptOwnership for RPCs
	ProviderID      string
	Payload         []byte
	ID              int64
	BroadcastID     int64
	MessageRowID    int64 // broadcast_messages.id this retry promotes; 0 on legacy rows
	ContactName     string
	Phone           string
	Rendered        string
	MediaKind       string
	MediaMime       string
	MediaName       string
	MediaURL        string
	MediaDirectPath string
	MediaKey        []byte
	MediaFileSHA    []byte
	MediaEncSHA     []byte
	MediaFileLen    uint64
	Attempts        int
	LastError       string
	NextAttemptAt   time.Time
}

// EnqueueOutbox writes one failed send into the outbox so the worker
// can pick it up. The first retry runs after outboxBackoffBase.
func (s *ContactStore) EnqueueOutbox(ctx context.Context, row OutboxRow) error {
	row.NextAttemptAt = time.Now().Add(outboxBackoffBase)
	var msgRowID any
	if row.MessageRowID > 0 {
		msgRowID = row.MessageRowID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO outbox (
			broadcast_id, broadcast_message_id, contact_name, phone, rendered,
			media_kind, media_mime, media_name,
			media_url, media_direct_path, media_key,
			media_file_sha, media_enc_sha, media_file_len,
			attempts, last_error, next_attempt_at, provider_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		row.BroadcastID, msgRowID, row.ContactName, row.Phone, row.Rendered,
		nullIfEmpty(row.MediaKind), nullIfEmpty(row.MediaMime), nullIfEmpty(row.MediaName),
		nullIfEmpty(row.MediaURL), nullIfEmpty(row.MediaDirectPath), nullIfBytes(row.MediaKey),
		nullIfBytes(row.MediaFileSHA), nullIfBytes(row.MediaEncSHA), row.MediaFileLen,
		nullIfEmpty(row.LastError),
		row.NextAttemptAt.UTC().Format("2006-01-02 15:04:05"), newDeliveryID(),
	)
	return err
}

// FetchDueOutbox pulls every pending row whose next_attempt_at has
// elapsed. Caller is expected to either MarkOutboxSent (delete) or
// RescheduleOutbox (bump attempts) on each row returned.
func (s *ContactStore) FetchDueOutbox(ctx context.Context, now time.Time) ([]OutboxRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, broadcast_id, COALESCE(broadcast_message_id, 0), contact_name, phone, rendered,
			COALESCE(media_kind, ''), COALESCE(media_mime, ''), COALESCE(media_name, ''),
			COALESCE(media_url, ''), COALESCE(media_direct_path, ''),
			media_key, media_file_sha, media_enc_sha, media_file_len,
			attempts, COALESCE(last_error, ''), next_attempt_at, payload, COALESCE(provider_id,'')
		FROM outbox
		WHERE status = 'pending' AND next_attempt_at <= ? AND broadcast_id IN (SELECT id FROM broadcasts WHERE delivery_state<>'cancelled')
		ORDER BY next_attempt_at ASC
		LIMIT 50`,
		now.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		var rawNext sql.NullString
		if err := rows.Scan(
			&r.ID, &r.BroadcastID, &r.MessageRowID, &r.ContactName, &r.Phone, &r.Rendered,
			&r.MediaKind, &r.MediaMime, &r.MediaName,
			&r.MediaURL, &r.MediaDirectPath,
			&r.MediaKey, &r.MediaFileSHA, &r.MediaEncSHA, &r.MediaFileLen,
			&r.Attempts, &r.LastError, &rawNext, &r.Payload, &r.ProviderID,
		); err != nil {
			return nil, err
		}
		if rawNext.Valid {
			// SQLite's DATETIME driver may return RFC3339 rather than the
			// storage format. Keep the persisted due time for clocked admission.
			r.NextAttemptAt, err = time.Parse("2006-01-02 15:04:05", rawNext.String)
			if err != nil {
				r.NextAttemptAt, err = time.Parse(time.RFC3339Nano, rawNext.String)
				if err != nil {
					return nil, err
				}
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// attemptOwnership accepts cancellation-created uncertainty while excluding
// pending/requeued rows that are no longer owned by this RPC.
const attemptOwnership = "attempt"

// MarkOutboxSent removes the row + flips the broadcast_messages row for
// this attempt to status='sent' with the live message_id. Returns
// ErrOutboxMissing if the row was deleted out from under us (rare; eg.
// the broadcast got purged mid-retry).
func (s *ContactStore) MarkOutboxSent(ctx context.Context, row OutboxRow, messageID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE id=? AND broadcast_id=? AND COALESCE(broadcast_message_id,0)=? AND (?='' OR provider_id=?) AND (?='' OR status=? OR (?=? AND status IN ('inflight','uncertain')))`, row.ID, row.BroadcastID, row.MessageRowID, row.ProviderID, row.ProviderID, row.ExpectedStatus, row.ExpectedStatus, row.ExpectedStatus, attemptOwnership)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrOutboxMissing
	}
	// Promote the original failed broadcast_messages row to sent so the
	// history reflects the retry. Rows enqueued after migration 0004 carry
	// the exact broadcast_messages id; legacy rows fall back to matching
	// by (broadcast_id, phone) + status='failed' — still guarded so a
	// stale "delivered" receipt for an unrelated row isn't overwritten.
	promote := `
		UPDATE broadcast_messages
		SET status = CASE WHEN status IN ('delivered','read') THEN status ELSE 'sent' END, message_id = CASE WHEN status IN ('delivered','read') THEN message_id ELSE ? END, error = NULL, sent_at = CASE WHEN status IN ('delivered','read') THEN sent_at ELSE CURRENT_TIMESTAMP END
		WHERE id = ?`
	args := []any{messageID, row.MessageRowID}
	if row.MessageRowID == 0 {
		promote = `
			UPDATE broadcast_messages
			SET status = 'sent', message_id = ?, error = NULL, sent_at = CURRENT_TIMESTAMP
			WHERE id = (
				SELECT id FROM broadcast_messages
				WHERE broadcast_id = ? AND phone = ? AND status = 'failed'
				ORDER BY id DESC LIMIT 1
			)`
		args = []any{messageID, row.BroadcastID, row.Phone}
	}
	pres, err := tx.ExecContext(ctx, promote, args...)
	if err != nil {
		return err
	}
	// When the retry actually flipped a row failed→sent, move the aggregate
	// broadcasts counters too: the initial UpdateBroadcastCounts booked this
	// recipient as a failure, and /api/stats + the history list read those
	// columns. Without this the successful retry stays counted as a failure
	// forever. Guard fail_count so it can't underflow past zero.
	if n, _ := pres.RowsAffected(); n > 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE broadcasts
			SET ok_count = (SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=broadcasts.id AND status IN ('sent','delivered','read')),
			    fail_count = (SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=broadcasts.id AND status IN ('failed','cancelled','uncertain'))
			WHERE id = ?`, row.BroadcastID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RescheduleOutbox bumps attempts on the row and recomputes
// next_attempt_at. When attempts would exceed outboxMaxAttempts the
// row is marked 'dead' instead so the worker stops touching it.
func (s *ContactStore) RescheduleOutbox(ctx context.Context, id int64, sendErr string) error {
	return s.rescheduleOutboxAt(ctx, id, sendErr, time.Now())
}

func (s *ContactStore) rescheduleOutboxAt(ctx context.Context, id int64, sendErr string, now time.Time) error {
	return s.rescheduleDeliveryAt(ctx, OutboxRow{ID: id}, sendErr, now)
}

func (s *ContactStore) rescheduleDeliveryAt(ctx context.Context, row OutboxRow, sendErr string, now time.Time) error {
	id := row.ID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var attempts int
	var mid int64
	var campaign, state string
	if err := tx.QueryRowContext(ctx, `SELECT o.attempts,COALESCE(o.broadcast_message_id,0),b.delivery_state,o.status FROM outbox o JOIN broadcasts b ON b.id=o.broadcast_id WHERE o.id=? AND (?<>? OR (o.broadcast_id=? AND COALESCE(o.broadcast_message_id,0)=? AND (?='' OR o.provider_id=?) AND o.status IN ('inflight','uncertain')))`, id, row.ExpectedStatus, attemptOwnership, row.BroadcastID, row.MessageRowID, row.ProviderID, row.ProviderID).Scan(&attempts, &mid, &campaign, &state); err != nil {
		return err
	}
	var confirmed int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM broadcast_messages WHERE id=? AND status IN ('sent','delivered','read')`, mid).Scan(&confirmed); err != nil {
		return err
	}
	if confirmed > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM outbox WHERE id=?`, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE broadcasts SET ok_count=(SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=broadcasts.id AND status IN ('sent','delivered','read')),fail_count=(SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=broadcasts.id AND status IN ('failed','cancelled','uncertain')) WHERE id=(SELECT broadcast_id FROM broadcast_messages WHERE id=?)`, mid); err != nil {
			return err
		}
		return tx.Commit()
	}
	if campaign == "cancelled" {
		if _, err = tx.ExecContext(ctx, `UPDATE outbox SET status='cancelled',last_error=? WHERE id=?`, sendErr, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE broadcast_messages SET status='cancelled',error=? WHERE id=? AND status NOT IN ('sent','delivered','read')`, sendErr, mid); err != nil {
			return err
		}
		return tx.Commit()
	}
	if state != "pending" && state != "inflight" {
		return ErrOutboxMissing
	}
	if _, err = tx.ExecContext(ctx, `UPDATE broadcast_messages SET status='failed',error=? WHERE id=? AND status NOT IN ('delivered','read')`, sendErr, mid); err != nil {
		return err
	}
	attempts++
	if attempts >= outboxMaxAttempts {
		_, err := tx.ExecContext(ctx,
			"UPDATE outbox SET status = 'dead', attempts = ?, last_error = ? WHERE id = ?",
			attempts, sendErr, id)
		if err != nil {
			return err
		}
		return tx.Commit()
	}

	// Backoff: base * 2^(attempts-1).
	backoff := outboxBackoffBase * (1 << (attempts - 1))
	if backoff > time.Hour {
		backoff = time.Hour
	}
	next := now.Add(backoff)
	if _, err := tx.ExecContext(ctx, `
		UPDATE outbox
		SET status = 'pending', attempts = ?, last_error = ?, next_attempt_at = ?
		WHERE id = ?`,
		attempts, sendErr, next.UTC().Format("2006-01-02 15:04:05"), id,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrOutboxMissing is returned by MarkOutboxSent when the row vanished
// before the update landed.
var ErrOutboxMissing = errors.New("outbox row missing")

// nullIfBytes is the []byte sibling of nullIfEmpty.
func nullIfBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// composeOutboxMessage rebuilds the waProto.Message for one retry. Text
// rows return a plain Conversation; media rows reuse the original
// UploadResponse fields the original send already captured.
func composeOutboxMessage(r OutboxRow) *waProto.Message {
	if len(r.Payload) > 0 {
		var msg waProto.Message
		if proto.Unmarshal(r.Payload, &msg) == nil {
			return &msg
		}
	}
	if r.MediaKind == "" {
		return &waProto.Message{Conversation: proto.String(r.Rendered)}
	}
	resp := whatsmeow.UploadResponse{
		URL:           r.MediaURL,
		DirectPath:    r.MediaDirectPath,
		MediaKey:      r.MediaKey,
		FileSHA256:    r.MediaFileSHA,
		FileEncSHA256: r.MediaEncSHA,
		FileLength:    r.MediaFileLen,
	}
	return buildMediaMessage(r.Rendered, r.MediaKind, resp, r.MediaMime, r.MediaName)
}

// OutboxWorker runs the retry loop. Started by Run() and torn down via
// the App shutdown ctx.
type OutboxWorker struct {
	now            func() time.Time // deterministic admission/backoff clock; nil uses wall time
	batchTimeout   time.Duration
	attemptTimeout time.Duration
	app            *App
	logger         *slog.Logger
	wg             sync.WaitGroup
}

// StartOutboxWorker spawns the worker goroutine and returns the handle.
func StartOutboxWorker(app *App) *OutboxWorker {
	w := &OutboxWorker{app: app, logger: app.logger.With("component", "outbox")}
	w.wg.Add(1)
	go w.loop()
	return w
}

// Wait blocks until the worker goroutine exits.
func (w *OutboxWorker) Wait() { w.wg.Wait() }

func (w *OutboxWorker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(outboxPollInterval)
	defer ticker.Stop()

	// Kick once on startup so a backlog accumulated during downtime is
	// processed without waiting for the first tick.
	w.processOnce()

	for {
		select {
		case <-w.app.shutdownCtx.Done():
			return
		case <-ticker.C:
			w.processOnce()
		}
	}
}

func (w *OutboxWorker) currentTime() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *OutboxWorker) processOnce() {
	client := w.app.WA()
	if client == nil || !client.IsConnected() {
		return
	}

	preparing, perr := w.app.contacts.db.QueryContext(w.app.shutdownCtx, `SELECT id FROM broadcasts WHERE delivery_state='interrupted' AND media_data IS NOT NULL ORDER BY id LIMIT 50`)
	if perr != nil {
		w.logger.Warn("fetch media recovery", "err", perr)
		return
	}
	var ids []int64
	for preparing.Next() {
		var id int64
		if err := preparing.Scan(&id); err != nil {
			preparing.Close()
			return
		}
		ids = append(ids, id)
	}
	perr = preparing.Err()
	preparing.Close()
	if perr != nil {
		return
	}
	for _, id := range ids {
		// A recovery upload owns the same byte admission as HTTP parsing. Never
		// load a persisted media buffer concurrently with an admitted request.
		if !w.app.broadcaster.mu.TryLock() {
			break
		}
		func() {
			defer w.app.broadcaster.mu.Unlock()
			if err := w.app.prepareDelivery(w.app.shutdownCtx, id, client); err != nil {
				w.logger.Warn("recover media upload", "err", err, "broadcast_id", id)
			}
		}()
	}

	fetchCtx, fetchCancel := context.WithTimeout(w.app.shutdownCtx, 5*time.Second)
	due, err := w.app.contacts.FetchDueOutbox(fetchCtx, w.currentTime())
	fetchCancel()
	if err != nil {
		w.logger.Warn("fetch due outbox", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}
	w.logger.Info("outbox tick", "due", len(due))
	budget := w.batchTimeout
	if budget <= 0 {
		budget = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(w.app.shutdownCtx, budget)
	defer cancel()

	for _, row := range due {
		if w.app.shutdownCtx.Err() != nil || ctx.Err() != nil {
			return
		}
		w.attempt(ctx, client, row)
	}
}

func (w *OutboxWorker) attempt(ctx context.Context, client WAClient, row OutboxRow) {
	if ctx.Err() != nil {
		return
	}
	attemptCtx, attemptCancel := context.WithCancel(ctx)
	defer attemptCancel()
	ctx = attemptCtx
	for {
		delay := computeBroadcastDelay(w.app.broadcaster.delay, w.app.broadcaster.jitter, rand.Float64)
		b := w.app.broadcaster
		b.attemptMu.Lock()
		claimed, wait, err := w.app.contacts.claimDeliveryAt(ctx, row, w.currentTime(), delay, b.hourlyCap)
		if claimed {
			if b.attempts == nil {
				b.attempts = make(map[int64]deliveryAttempt)
			}
			b.attempts[row.ID] = deliveryAttempt{broadcastID: row.BroadcastID, cancel: attemptCancel}
		}
		b.attemptMu.Unlock()
		if err != nil {
			w.logger.Warn("delivery admission failed", "err", err)
			return
		}
		if claimed {
			break
		}
		if wait <= 0 {
			return
		}
		reason := "cadence"
		if wait > time.Minute {
			reason = "hourly_cap"
		}
		w.app.broadcastEvent(Event{Type: "broadcast_paused", Data: BroadcastPausedData{BroadcastID: row.BroadcastID, Reason: reason, ResumeAt: time.Now().Add(wait).Format(time.RFC3339), WaitSeconds: int(wait.Seconds())}})
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
	defer func() {
		b := w.app.broadcaster
		b.attemptMu.Lock()
		delete(b.attempts, row.ID)
		b.attemptMu.Unlock()
		pctx, cancel := persistContext()
		defer cancel()
		_ = w.app.contacts.reconcileDelivery(pctx, row.BroadcastID)
	}()
	row.ExpectedStatus = attemptOwnership
	jid := types.NewJID(row.Phone, types.DefaultUserServer)
	if len(row.Payload) > 0 {
		var stored waProto.Message
		if err := proto.Unmarshal(row.Payload, &stored); err != nil {
			pctx, cancel := persistContext()
			defer cancel()
			if e := w.app.contacts.uncertainDelivery(pctx, row, "stored payload corrupt: "+err.Error()); e != nil {
				w.logger.Error("record corrupt intent", "err", e)
			}
			return
		}
	}
	msg := composeOutboxMessage(row)

	timeout := w.attemptTimeout
	if timeout <= 0 {
		timeout = sendTimeout
	}
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	var resp whatsmeow.SendResponse
	var err error
	if sendCtx.Err() != nil {
		cancel()
		pctx, pcancel := persistContext()
		defer pcancel()
		_ = w.app.contacts.uncertainDelivery(pctx, row, sendCtx.Err().Error())
		return
	}
	if stable, ok := client.(stableWAClient); ok {
		if row.ProviderID == "" {
			_ = w.app.contacts.db.QueryRowContext(ctx, `SELECT provider_id FROM outbox WHERE id=?`, row.ID).Scan(&row.ProviderID)
		}
		resp, err = stable.SendMessageWithID(sendCtx, jid, msg, row.ProviderID)
	} else {
		resp, err = client.SendMessage(sendCtx, jid, msg)
	}
	cancel()
	// Count provider outcomes, independently of the durable commit/review state.
	if err != nil {
		metricBroadcastMessagesErr.Add(1)
	} else {
		metricBroadcastMessagesOK.Add(1)
	}

	// Detached ctx for the post-send bookkeeping: ctx derives from
	// shutdownCtx, and a SIGTERM landing between a successful send and
	// MarkOutboxSent would leave the row 'pending' — the next boot would
	// resend it and the recipient would get a duplicate.
	pctx, pcancel := persistContext()
	defer pcancel()

	if err != nil {
		var rejected *RejectedDeliveryError
		if !errors.As(err, &rejected) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if e := w.app.contacts.uncertainDelivery(pctx, row, err.Error()); e != nil {
				w.logger.Error("retain inflight for recovery; uncertain write failed", "err", e, "outbox_id", row.ID)
			}
			return
		}
		w.logger.Warn("outbox retry failed",
			"err", err, "outbox_id", row.ID,
			"phone", maskPhone(row.Phone), "attempts", row.Attempts+1,
		)
		if rerr := w.app.contacts.rescheduleDeliveryAt(pctx, row, err.Error(), w.currentTime()); rerr != nil {
			w.logger.Warn("reschedule failed", "err", rerr, "outbox_id", row.ID)
		}
		metricOutboxRetries.Add(1)
		return
	}

	if err := w.app.contacts.MarkOutboxSent(pctx, row, resp.ID); err != nil {
		w.logger.Warn("mark sent failed", "err", err, "outbox_id", row.ID)
		if e := w.app.contacts.uncertainDelivery(pctx, row, "remote success; local commit failed: "+err.Error()); e != nil {
			w.logger.Error("retain inflight for startup reconciliation", "err", e)
		}
		return
	}
	w.logger.Info("outbox retry succeeded",
		"outbox_id", row.ID,
		"phone", maskPhone(row.Phone),
		"message_id", resp.ID,
		"attempts", row.Attempts+1,
	)
	metricOutboxDelivered.Add(1)
	w.app.broadcastEvent(Event{
		Type: "outbox_retry",
		Data: OutboxRetryData{
			BroadcastID: row.BroadcastID,
			Phone:       row.Phone,
			MessageID:   resp.ID,
			Attempts:    row.Attempts + 1,
		},
	})
}

// OutboxRetryData is the SSE payload for a successful outbox retry. Typed to
// match the "every payload is a typed struct" convention the hub follows.
type OutboxRetryData struct {
	BroadcastID int64  `json:"broadcastId"`
	Phone       string `json:"phone"`
	MessageID   string `json:"messageId"`
	Attempts    int    `json:"attempts"`
}

// OutboxDeadRow is the JSON shape returned by the /api/outbox/dead
// endpoint so operators can inspect rows that exhausted retries.
type OutboxDeadRow struct {
	ID          int64     `json:"id"`
	BroadcastID int64     `json:"broadcastId"`
	ContactName string    `json:"contactName"`
	Phone       string    `json:"phone"`
	Attempts    int       `json:"attempts"`
	LastError   string    `json:"lastError"`
	CreatedAt   time.Time `json:"createdAt"`
}

// handleOutboxDead returns the list of dead rows for the dashboard.
func (app *App) handleOutboxDead(w http.ResponseWriter, r *http.Request) {
	rows, err := app.contacts.ListDeadOutbox(r.Context())
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	jsonResponse(w, http.StatusOK, rows)
}

// ListDeadOutbox returns every row currently in the dead state. Bounded
// at 500 rows — operators with more than that are well past needing a
// nudge to investigate.
func (s *ContactStore) ListDeadOutbox(ctx context.Context) ([]OutboxDeadRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, broadcast_id, contact_name, phone, attempts,
		       COALESCE(last_error, ''), created_at
		FROM outbox
		WHERE status = 'dead'
		ORDER BY created_at DESC
		LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []OutboxDeadRow{}
	for rows.Next() {
		var row OutboxDeadRow
		var raw sql.NullString
		if err := rows.Scan(&row.ID, &row.BroadcastID, &row.ContactName, &row.Phone,
			&row.Attempts, &row.LastError, &raw); err != nil {
			return nil, err
		}
		if raw.Valid {
			row.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", raw.String)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Keep json import alive for OutboxDeadRow marshalling via jsonResponse.
var _ = json.Marshal
