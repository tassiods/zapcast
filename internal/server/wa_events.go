// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Bridges whatsmeow's event stream into the app: receipts update
// broadcast_messages, connect/disconnect ripple to SSE, remote logouts
// trigger a client reset.

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// handleWAEvent is wired to client.AddEventHandler.
func (app *App) handleWAEvent(rawEvt any) {
	switch evt := rawEvt.(type) {
	case *events.Receipt:
		app.handleReceiptEvent(evt)
	case *events.Connected:
		app.logger.Info("whatsmeow connected")
		app.broadcastEvent(Event{Type: "connected"})
	case *events.Disconnected:
		app.logger.Warn("whatsmeow disconnected")
		_ = app.cancelBroadcast("whatsapp disconnected")
		app.broadcastEvent(Event{Type: "disconnected"})
	case *events.LoggedOut:
		app.logger.Warn("whatsmeow logged out", "reason", evt.Reason.String())
		_ = app.cancelBroadcast("whatsapp logged out")
		// WhatsApp invalidated this device. Swap in a fresh one so the next
		// QR pairing doesn't use stale Noise/Identity keys.
		go app.resetClientAsync()
		app.broadcastEvent(Event{Type: "logged_out"})
	}
}

// handleReceiptEvent maps whatsmeow receipt types onto our status values
// and updates the per-message rows. Unknown types (played/server/...) are
// ignored — we only care about delivered / read for now.
func (app *App) handleReceiptEvent(evt *events.Receipt) {
	var status string
	switch evt.Type {
	case events.ReceiptTypeDelivered:
		status = "delivered"
	case events.ReceiptTypeRead, events.ReceiptTypeReadSelf:
		status = "read"
	default:
		return
	}
	// Bound each row update with a small timeout derived from the app
	// shutdown ctx. SIGTERM cancels the parent, so receipt writes stop
	// instead of holding the DB connection open during teardown.
	for _, mid := range evt.MessageIDs {
		ctx, cancel := context.WithTimeout(app.shutdownCtx, 2*time.Second)
		err := app.contacts.UpdateMessageStatus(ctx, mid, status)
		cancel()
		if err != nil {
			app.logger.Debug("failed to update message status", "err", err, "message_id", mid, "status", status)
			continue
		}
		app.broadcastEvent(Event{
			Type: "receipt",
			Data: ReceiptData{
				MessageID: mid,
				Status:    status,
				From:      evt.SourceString(),
			},
		})
	}
}

// resetClientAsync runs resetClient with a bounded deadline so a slow reset
// can't pin the whatsmeow event-handler goroutine. Rooted at shutdownCtx so
// SIGTERM during a remote-logout reset cancels the in-flight work before the
// DB closes.
func (app *App) resetClientAsync() {
	ctx, cancel := context.WithTimeout(app.shutdownCtx, 10*time.Second)
	defer cancel()
	if err := app.resetClient(ctx); err != nil {
		app.logger.Error("failed to reset client after remote logout", "err", err)
	}
}
