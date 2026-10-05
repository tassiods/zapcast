// SPDX-FileCopyrightText: 2025 ZapCast contributors
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
			attempts, last_error, next_attempt_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		row.BroadcastID, msgRowID, row.ContactName, row.Phone, row.Rendered,
		nullIfEmpty(row.MediaKind), nullIfEmpty(row.MediaMime), nullIfEmpty(row.MediaName),
		nullIfEmpty(row.MediaURL), nullIfEmpty(row.MediaDirectPath), nullIfBytes(row.MediaKey),
		nullIfBytes(row.MediaFileSHA), nullIfBytes(row.MediaEncSHA), row.MediaFileLen,
		nullIfEmpty(row.LastError),
		row.NextAttemptAt.UTC().Format("2006-01-02 15:04:05"),
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
			attempts, COALESCE(last_error, ''), next_attempt_at
		FROM outbox
		WHERE status = 'pending' AND next_attempt_at <= ?
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
			&r.Attempts, &r.LastError, &rawNext,
		); err != nil {
			return nil, err
		}
		if rawNext.Valid {
			r.NextAttemptAt, _ = time.Parse("2006-01-02 15:04:05", rawNext.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

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

	res, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", row.ID)
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
		SET status = 'sent', message_id = ?, error = NULL, sent_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'failed'`
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
			SET ok_count   = ok_count + 1,
			    fail_count = CASE WHEN fail_count > 0 THEN fail_count - 1 ELSE 0 END
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var attempts int
	if err := tx.QueryRowContext(ctx, "SELECT attempts FROM outbox WHERE id = ?", id).Scan(&attempts); err != nil {
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
	next := time.Now().Add(backoff)
	if _, err := tx.ExecContext(ctx, `
		UPDATE outbox
		SET attempts = ?, last_error = ?, next_attempt_at = ?
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
	app    *App
	logger *slog.Logger
	wg     sync.WaitGroup
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

func (w *OutboxWorker) processOnce() {
	client := w.app.WA()
	if client == nil || !client.IsConnected() {
		return
	}

	ctx, cancel := context.WithTimeout(w.app.shutdownCtx, 30*time.Second)
	defer cancel()

	due, err := w.app.contacts.FetchDueOutbox(ctx, time.Now())
	if err != nil {
		w.logger.Warn("fetch due outbox", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}
	w.logger.Info("outbox tick", "due", len(due))

	for _, row := range due {
		if w.app.shutdownCtx.Err() != nil {
			return
		}
		w.attempt(ctx, client, row)
	}
}

func (w *OutboxWorker) attempt(ctx context.Context, client WAClient, row OutboxRow) {
	jid := types.NewJID(row.Phone, types.DefaultUserServer)
	msg := composeOutboxMessage(row)

	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	resp, err := client.SendMessage(sendCtx, jid, msg)
	cancel()

	// Detached ctx for the post-send bookkeeping: ctx derives from
	// shutdownCtx, and a SIGTERM landing between a successful send and
	// MarkOutboxSent would leave the row 'pending' — the next boot would
	// resend it and the recipient would get a duplicate.
	pctx, pcancel := persistContext()
	defer pcancel()

	if err != nil {
		w.logger.Warn("outbox retry failed",
			"err", err, "outbox_id", row.ID,
			"phone", maskPhone(row.Phone), "attempts", row.Attempts+1,
		)
		if rerr := w.app.contacts.RescheduleOutbox(pctx, row.ID, err.Error()); rerr != nil {
			w.logger.Warn("reschedule failed", "err", rerr, "outbox_id", row.ID)
		}
		metricOutboxRetries.Add(1)
		return
	}

	if err := w.app.contacts.MarkOutboxSent(pctx, row, resp.ID); err != nil {
		w.logger.Warn("mark sent failed", "err", err, "outbox_id", row.ID)
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
