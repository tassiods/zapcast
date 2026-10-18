// SPDX-FileCopyrightText: 2026 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// DeliveryReview intentionally excludes rendered bodies, media credentials and
// protobuf payloads. Stable IDs are correlation hints, not delivery guarantees.
type DeliveryReview struct {
	ID             int64  `json:"id"`
	BroadcastID    int64  `json:"broadcastId"`
	MessageRowID   int64  `json:"messageRowId"`
	CampaignStatus string `json:"campaignStatus"`
	ContactName    string `json:"contactName"`
	Phone          string `json:"phone"`
	MessageID      string `json:"messageId,omitempty"`
	Status         string `json:"status"`
	Attempts       int    `json:"attempts"`
	Error          string `json:"error,omitempty"`
	NoPayload      bool   `json:"noPayload,omitempty"`
	NoMessageRowID bool   `json:"noMessageRowId,omitempty"`
}

func (app *App) handleDeliveryReview(w http.ResponseWriter, r *http.Request) {
	rows, err := app.contacts.db.QueryContext(r.Context(), `SELECT o.id,o.broadcast_id,COALESCE(o.broadcast_message_id,0),b.delivery_state,o.contact_name,o.phone,COALESCE(o.provider_id,''),o.status,o.attempts,COALESCE(o.last_error,''),(o.payload IS NULL OR length(o.payload)=0),(COALESCE(o.broadcast_message_id,0)=0) FROM outbox o JOIN broadcasts b ON b.id=o.broadcast_id ORDER BY CASE o.status WHEN 'uncertain' THEN 0 WHEN 'dead' THEN 1 ELSE 2 END,o.id LIMIT 500`)
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []DeliveryReview{}
	for rows.Next() {
		var row DeliveryReview
		if err = rows.Scan(&row.ID, &row.BroadcastID, &row.MessageRowID, &row.CampaignStatus, &row.ContactName, &row.Phone, &row.MessageID, &row.Status, &row.Attempts, &row.Error, &row.NoPayload, &row.NoMessageRowID); err != nil {
			handleError(w, r, http.StatusInternalServerError, err)
			return
		}
		out = append(out, row)
	}
	if err = rows.Err(); err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	jsonResponse(w, http.StatusOK, out)
}

// DeliveryResolution requires an operator assertion; no auto-retry endpoint
// can reinterpret uncertain remote outcomes as definitive failures.
type DeliveryResolution struct {
	Action                   string `json:"action"`
	AcknowledgeDuplicateRisk bool   `json:"acknowledgeDuplicateRisk"`
	ProviderConfirmed        bool   `json:"providerConfirmed"`
	MessageID                string `json:"messageId"`
}

func (app *App) handleDeliveryResolve(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		jsonResponse(w, 400, map[string]string{"error": "invalid delivery id"})
		return
	}
	var body DeliveryResolution
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Action == "confirm_sent" {
		if !body.ProviderConfirmed || strings.TrimSpace(body.MessageID) == "" {
			jsonResponse(w, 400, map[string]string{"error": "confirmation requires providerConfirmed=true and messageId"})
			return
		}
		app.confirmDelivery(w, r, id, body.MessageID)
		return
	}
	if body.Action != "retry" || !body.AcknowledgeDuplicateRisk {
		jsonResponse(w, 400, map[string]string{"error": "retry requires acknowledgeDuplicateRisk=true"})
		return
	}
	tx, err := app.contacts.db.BeginTx(r.Context(), nil)
	if err != nil {
		handleError(w, r, 500, err)
		return
	}
	defer tx.Rollback()
	var bid, mid int64
	var status, campaign, history string
	var receipt int
	var payload []byte
	err = tx.QueryRowContext(r.Context(), `SELECT o.broadcast_id,COALESCE(o.broadcast_message_id,0),o.status,b.delivery_state,o.payload,COALESCE(m.status,''),EXISTS(SELECT 1 FROM delivery_receipts WHERE message_id=o.provider_id) FROM outbox o JOIN broadcasts b ON b.id=o.broadcast_id LEFT JOIN broadcast_messages m ON m.id=o.broadcast_message_id WHERE o.id=?`, id).Scan(&bid, &mid, &status, &campaign, &payload, &history, &receipt)
	if errors.Is(err, sql.ErrNoRows) {
		jsonResponse(w, 404, map[string]string{"error": "delivery not found"})
		return
	}
	if err != nil {
		handleError(w, r, 500, err)
		return
	}
	if mid > 0 && (receipt > 0 || history == "sent" || history == "delivered" || history == "read") {
		// The review snapshot can outlive confirmation. Complete that intent
		// atomically instead of resetting proven success to pending.
		if _, err = tx.ExecContext(r.Context(), linkDeliveryReceiptSQL+` AND id=?`, mid); err == nil {
			_, err = tx.ExecContext(r.Context(), `DELETE FROM outbox WHERE id=?`, id)
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), reconcileDeliverySQL, bid, bid, bid)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			handleError(w, r, 500, err)
			return
		}
		jsonResponse(w, 409, map[string]string{"error": "delivery already confirmed; retry refused"})
		return
	}
	if campaign == "cancelled" || status != "uncertain" && status != "dead" || mid == 0 || len(payload) == 0 {
		jsonResponse(w, 409, map[string]string{"error": "delivery not retryable: cancelled, unresolved legacy payload/link, or non-review state"})
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE outbox SET status='pending',next_attempt_at=CURRENT_TIMESTAMP,attempts=0,last_error='operator acknowledged duplicate risk' WHERE id=?`, id); err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE broadcast_messages SET status='pending',error='operator acknowledged duplicate risk' WHERE id=?`, mid)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE broadcasts SET delivery_state='active' WHERE id=?`, bid)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		handleError(w, r, 500, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"id": id, "status": "pending", "duplicateRisk": true})
}

func (app *App) confirmDelivery(w http.ResponseWriter, r *http.Request, id int64, messageID string) {
	var row OutboxRow
	var status string
	err := app.contacts.db.QueryRowContext(r.Context(), `SELECT id,broadcast_id,COALESCE(broadcast_message_id,0),status FROM outbox WHERE id=?`, id).Scan(&row.ID, &row.BroadcastID, &row.MessageRowID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		jsonResponse(w, 404, map[string]string{"error": "delivery not found"})
		return
	}
	if err != nil {
		handleError(w, r, 500, err)
		return
	}
	if row.MessageRowID == 0 || status != "uncertain" && status != "dead" && status != "cancelled" {
		jsonResponse(w, 409, map[string]string{"error": "delivery not confirmable or legacy link unresolved"})
		return
	}
	row.ExpectedStatus = status
	if err = app.contacts.MarkOutboxSent(r.Context(), row, messageID); err != nil {
		handleError(w, r, 500, err)
		return
	}
	if err = app.contacts.reconcileDelivery(r.Context(), row.BroadcastID); err != nil {
		handleError(w, r, 500, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"id": id, "status": "sent", "messageId": messageID})
}

func (app *App) handleDeliveryCancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		jsonResponse(w, 400, map[string]string{"error": "invalid campaign id"})
		return
	}
	var exists int
	if err = app.contacts.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM broadcasts WHERE id=?`, id).Scan(&exists); err != nil {
		handleError(w, r, 500, err)
		return
	}
	if exists == 0 {
		jsonResponse(w, 404, map[string]string{"error": "campaign not found"})
		return
	}
	app.broadcaster.attemptMu.Lock()
	err = app.contacts.cancelDelivery(r.Context(), id, "operator")
	app.broadcaster.cancelAttemptsLocked(id)
	app.broadcaster.attemptMu.Unlock()
	if err != nil {
		handleError(w, r, 500, err)
		return
	}
	if app.broadcaster.currentID.Load() == id {
		app.broadcaster.Cancel("operator")
	}
	if err = app.contacts.reconcileDelivery(r.Context(), id); err != nil {
		handleError(w, r, 500, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"broadcastId": id, "cancelled": true})
}
