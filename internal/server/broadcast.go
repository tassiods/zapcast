// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Broadcast lifecycle: validate, return 202 immediately, fan-out in a
// goroutine. Progress streams over SSE.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// BroadcastRequest is the JSON body for /api/broadcast. Either GroupID or
// Phones must be present.
type BroadcastRequest struct {
	GroupID string   `json:"groupId,omitempty"`
	Phones  []string `json:"phones,omitempty"`
	Message string   `json:"message"`
}

// BroadcastStartedResponse is the 202 reply with the just-created broadcast
// id so the client can correlate progress events.
type BroadcastStartedResponse struct {
	BroadcastID int64  `json:"broadcastId"`
	Total       int    `json:"total"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Media       string `json:"media,omitempty"`
}

// broadcastCooldown is the minimum interval between broadcasts.
const broadcastCooldown = 10 * time.Second

// maxBroadcastFileBytes caps a single media attachment. Above this the
// server returns 413. Chosen for WhatsApp's image/audio/doc envelope:
// 16 MiB covers virtually all images and most documents without buffering
// huge videos in memory.
const maxBroadcastFileBytes = 16 << 20

// uploadTimeout bounds the encrypted-upload to Meta. 60s is comfortable
// for the maximum file size on typical residential bandwidth.
const uploadTimeout = 60 * time.Second

// sendTimeout bounds a single SendMessage RPC. WhatsApp's typical reply is
// well under a second; 30s is enough that legitimate retries succeed.
const sendTimeout = 30 * time.Second

// mediaPayload captures the attachment fields a broadcast carries through
// validation into the goroutine.
type mediaPayload struct {
	data []byte
	mime string
	name string
	kind string
}

func (m mediaPayload) present() bool { return len(m.data) > 0 }

// handleBroadcast validates the request synchronously and launches the
// runBroadcast goroutine. Lock ownership transfers to the goroutine on
// successful launch; every early-return path here releases it explicitly.
func (app *App) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	client := app.WA()
	if !client.IsConnected() {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_whatsapp_not_conn")
		return
	}

	// Detached parent context — the send must survive HTTP request close
	// but must be cancellable from WhatsApp disconnect and SIGTERM.
	handle, err := app.broadcaster.Start(context.Background(), broadcastCooldown)
	if err != nil {
		if errors.Is(err, ErrBroadcastRunning) {
			jsonErrorLocalized(w, r, http.StatusTooManyRequests, "err_broadcast_running")
			return
		}
		var ce *CooldownError
		if errors.As(err, &ce) {
			jsonErrorLocalized(w, r, http.StatusTooManyRequests, "err_broadcast_cooldown", ce.Remaining)
			return
		}
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}

	launched := false
	defer func() {
		if !launched {
			handle.Abandon()
		}
	}()

	body, media, ok := app.parseBroadcastRequest(w, r)
	if !ok {
		return
	}
	if body.Message == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_message_required")
		return
	}

	adhoc := len(body.Phones) > 0
	if !adhoc && body.GroupID == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_group_or_phones")
		return
	}

	contacts, label, err := app.resolveRecipients(r.Context(), adhoc, body)
	if err != nil {
		handle.Abandon()
		if _, ok := asLocalized(err); ok {
			handleError(w, r, http.StatusBadRequest, err)
		} else {
			handleError(w, r, http.StatusInternalServerError, err)
		}
		return
	}

	templateVars, err := app.contacts.GetTemplateVars(r.Context())
	if err != nil {
		handle.Abandon()
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	if templateVars == nil {
		templateVars = make(map[string]string)
	}

	broadcastID, hErr := app.contacts.createDelivery(r.Context(), 0, label, body.Message, contacts, templateVars, media)
	if hErr != nil {
		handle.Abandon()
		handleError(w, r, http.StatusInternalServerError, hErr)
		return
	}

	app.broadcaster.currentID.Store(broadcastID)
	metricBroadcastsTotal.Add(1)
	launched = true
	go app.runBroadcast(handle, broadcastID, label, body.Message, contacts, templateVars, media)

	jsonResponse(w, http.StatusAccepted, BroadcastStartedResponse{
		BroadcastID: broadcastID,
		Total:       len(contacts),
		Label:       label,
		Status:      "started",
		Media:       media.kind,
	})
}

// handleBroadcastCancel aborts the in-flight broadcast, if any. Always 200:
// cancelling nothing is a no-op reported as {"cancelled": false}, not an
// error, so the button stays safe to press on a stale progress panel.
func (app *App) handleBroadcastCancel(w http.ResponseWriter, r *http.Request) {
	running := app.broadcaster.Running()
	if err := app.cancelBroadcast("operator"); err != nil {
		handleError(w, r, 500, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"cancelled": running})
}

// parseBroadcastRequest decodes either a multipart upload or JSON body.
func (app *App) parseBroadcastRequest(w http.ResponseWriter, r *http.Request) (BroadcastRequest, mediaPayload, bool) {
	var body BroadcastRequest
	var media mediaPayload

	if !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		if !decodeJSON(w, r, &body) {
			return body, media, false
		}
		return body, media, true
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBroadcastFileBytes+(1<<20))
	// #nosec G120 -- MaxBytesReader applied on the previous line.
	if err := r.ParseMultipartForm(maxBroadcastFileBytes); err != nil {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_upload_processing", err.Error())
		return body, media, false
	}

	defer func() {
		if err := r.MultipartForm.RemoveAll(); err != nil {
			app.logger.Warn("multipart cleanup failed", "err", err)
		}
	}()
	body.Message = strings.TrimSpace(r.FormValue("message"))
	body.GroupID = strings.TrimSpace(r.FormValue("groupId"))
	if raw := r.FormValue("phones"); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				body.Phones = append(body.Phones, p)
			}
		}
	}

	file, header, ferr := r.FormFile("file")
	if ferr != nil || file == nil {
		return body, media, true
	}
	defer file.Close()

	data, rerr := io.ReadAll(io.LimitReader(file, maxBroadcastFileBytes+1))
	if rerr != nil {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_upload_processing", rerr.Error())
		return body, media, false
	}
	if len(data) > maxBroadcastFileBytes {
		jsonErrorLocalized(w, r, http.StatusRequestEntityTooLarge, "err_file_too_large")
		return body, media, false
	}

	// Don't trust the multipart-supplied MIME — browsers occasionally send
	// "application/octet-stream" for known types.
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	media.mime = http.DetectContentType(sniff)
	media.name = header.Filename
	_, media.kind, _ = classifyMedia(media.mime)
	media.data = data
	return body, media, true
}

// resolveRecipients turns the request body into a contact slice and label.
func (app *App) resolveRecipients(ctx context.Context, adhoc bool, body BroadcastRequest) ([]Contact, string, error) {
	if adhoc {
		contacts, err := app.contacts.GetContactsByPhones(ctx, body.Phones)
		if err != nil {
			return nil, "", err
		}
		if len(contacts) == 0 {
			return nil, "", newErrKey("err_no_contacts")
		}
		return contacts, fmt.Sprintf("Seleção manual (%d)", len(contacts)), nil
	}
	contacts, err := app.contacts.GetGroupContacts(ctx, body.GroupID)
	if err != nil {
		return nil, "", err
	}
	if len(contacts) == 0 {
		return nil, "", newErrKey("err_group_empty")
	}
	return contacts, body.GroupID, nil
}

// computeBroadcastDelay returns base delay nudged by ±(jitter * base).
func computeBroadcastDelay(base time.Duration, jitter float64, rngFloat func() float64) time.Duration {
	if jitter <= 0 {
		return base
	}
	n := (rngFloat()*2 - 1) * jitter
	delay := time.Duration(float64(base) * (1 + n))
	if delay < 50*time.Millisecond {
		delay = 50 * time.Millisecond
	}
	return delay
}

// buildBroadcastDoneEvent kept as a thin alias for the test suite. New code
// should call newBroadcastDoneEvent directly.
func buildBroadcastDoneEvent(broadcastID int64, label string, total, ok, fail, abortedRemaining int, aborted bool, errMsg string) Event {
	return newBroadcastDoneEvent(broadcastID, label, total, ok, fail, abortedRemaining, aborted, errMsg)
}

// runBroadcast is the goroutine spawned by handleBroadcast.
func (app *App) runBroadcast(handle *StartHandle, broadcastID int64, label, message string, contacts []Contact, templateVars map[string]string, media mediaPayload) {
	defer func() {
		if rec := recover(); rec != nil {
			app.logger.Error("broadcast goroutine panic", "broadcast_id", broadcastID, "err", rec)
		}
		handle.Done()
	}()

	app.broadcaster.currentID.Store(broadcastID)
	ctx := handle.Ctx
	client := app.WA()

	pctx0, cancel0 := persistContext()
	_, intentErr := app.contacts.createDelivery(pctx0, broadcastID, label, message, contacts, templateVars, media)
	cancel0()
	if intentErr != nil {
		app.logger.Error("persist delivery", "err", intentErr)
		return
	}
	uploadResp, ok := app.uploadIfMedia(ctx, client, broadcastID, contacts, label, media)
	if !ok {
		return
	}

	okCount, failCount, abortedCount, aborted := app.sendAll(ctx, client, broadcastID, label, message, contacts, templateVars, media, uploadResp)
	if aborted {
		metricBroadcastAborts.Add(1)
	}

	// Detached ctx: on the abort path the broadcast ctx is already
	// cancelled, but the final counters still describe what happened.
	pctx, pcancel := persistContext()
	defer pcancel()
	if err := app.contacts.UpdateBroadcastCounts(pctx, broadcastID, okCount, failCount); err != nil {
		app.logger.Warn("failed to update broadcast counts", "err", err, "broadcast_id", broadcastID)
	}
	if err := app.contacts.db.QueryRowContext(pctx, `SELECT ok_count,fail_count FROM broadcasts WHERE id=?`, broadcastID).Scan(&okCount, &failCount); err != nil {
		app.logger.Warn("read reconciled completion counts", "err", err)
	}
	app.broadcastEvent(buildBroadcastDoneEvent(broadcastID, label, len(contacts), okCount, failCount, abortedCount, aborted, ""))
}

// uploadIfMedia uploads the attachment once. Failure aborts the broadcast.
func (app *App) uploadIfMedia(ctx context.Context, client WAClient, broadcastID int64, contacts []Contact, label string, media mediaPayload) (whatsmeow.UploadResponse, bool) {
	if !media.present() {
		return whatsmeow.UploadResponse{}, true
	}

	err := app.prepareDelivery(ctx, broadcastID, client)
	resp := whatsmeow.UploadResponse{}
	if err != nil {
		app.logger.Error("media upload failed", "broadcast_id", broadcastID, "err", err)
		errMsg := "upload_failed: " + err.Error()
		// Detached ctx — the upload may have failed precisely because ctx
		// was cancelled, and these rows must still record the outcome.
		pctx, pcancel := persistContext()
		defer pcancel()
		if app.shutdownCtx != nil && app.shutdownCtx.Err() != nil {
			_, _ = app.contacts.db.ExecContext(pctx, `UPDATE broadcasts SET delivery_state='interrupted' WHERE id=? AND delivery_state<>'cancelled'`, broadcastID)
		} else if ctx.Err() != nil {
			if e := app.contacts.cancelDelivery(pctx, broadcastID, "upload cancelled"); e != nil {
				app.logger.Error("cancel upload delivery", "err", e)
			}
		} else {
			tx, e := app.contacts.db.BeginTx(pctx, nil)
			if e == nil {
				_, e = tx.ExecContext(pctx, `UPDATE broadcast_messages SET status='failed',error=? WHERE broadcast_id=? AND status='pending'`, errMsg, broadcastID)
				if e == nil {
					_, e = tx.ExecContext(pctx, `UPDATE outbox SET status='dead',last_error=? WHERE broadcast_id=? AND status='preparing'`, errMsg, broadcastID)
				}
				if e == nil {
					e = tx.Commit()
				}
				_ = tx.Rollback()
			}
			if e != nil {
				app.logger.Error("record upload failure", "err", e)
			}
		}
		_ = app.contacts.UpdateBroadcastCounts(pctx, broadcastID, 0, len(contacts))
		app.broadcastEvent(buildBroadcastDoneEvent(broadcastID, label, len(contacts), 0, len(contacts), 0, false, "upload_failed"))
		return resp, false
	}
	app.logger.Info("media uploaded", "broadcast_id", broadcastID, "kind", media.kind, "bytes", resp.FileLength)
	return resp, true
}

// sendAll loops through recipients, enforcing the hourly cap and aborting
// on cancellation.
func (app *App) sendAll(ctx context.Context, client WAClient, broadcastID int64, label, message string, contacts []Contact, templateVars map[string]string, media mediaPayload, uploadResp whatsmeow.UploadResponse) (okCount, failCount, abortedCount int, aborted bool) {
	rows, err := app.contacts.deliveryRows(context.Background(), broadcastID)
	if err != nil {
		return 0, len(contacts), 0, false
	}
	w := &OutboxWorker{app: app, logger: app.logger}

	for i, row := range rows {
		if ctx.Err() != nil {
			aborted = true
			abortedCount = len(rows) - i
			pctx, cancel := persistContext()
			if app.shutdownCtx != nil && app.shutdownCtx.Err() != nil {
				_, _ = app.contacts.db.ExecContext(pctx, `UPDATE broadcasts SET delivery_state='interrupted' WHERE id=? AND delivery_state<>'cancelled'`, broadcastID)
				cancel()
				break
			}
			_ = app.contacts.cancelDelivery(pctx, broadcastID, "aborted")
			_, _ = app.contacts.db.ExecContext(pctx, `UPDATE broadcast_messages SET status='cancelled',error='aborted' WHERE broadcast_id=? AND status='pending'`, broadcastID)
			_, _ = app.contacts.db.ExecContext(pctx, `UPDATE outbox SET status='cancelled' WHERE broadcast_id=? AND status='pending'`, broadcastID)
			cancel()
			failCount += abortedCount
			break
		}

		w.attempt(ctx, client, row)
		var status string
		_ = app.contacts.db.QueryRowContext(context.Background(), `SELECT status FROM broadcast_messages WHERE id=?`, row.MessageRowID).Scan(&status)
		success := status == "sent" || status == "delivered" || status == "read"
		if success {
			okCount++
		} else {
			failCount++
		}
		app.broadcastEvent(Event{Type: "broadcast_progress", Data: BroadcastProgressData{BroadcastID: broadcastID, Current: i + 1, Total: len(rows), OK: okCount, Fail: failCount, Contact: row.ContactName, Success: success}})

	}
	return
}

// persistContext returns a short-lived context detached from the broadcast's
// cancellable ctx. History writes record what already happened — a sent
// message, a failed attempt, an aborted remainder — so they must land even
// when the broadcast itself was cancelled; the cancelled ctx would make
// database/sql refuse them outright. Bounded well under runShutdown's 10s
// drain window.
func persistContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// composeMessage returns the right waProto.Message variant for a recipient.
func composeMessage(rendered string, media mediaPayload, uploadResp whatsmeow.UploadResponse) *waProto.Message {
	if !media.present() {
		return &waProto.Message{Conversation: proto.String(rendered)}
	}
	return buildMediaMessage(rendered, media.kind, uploadResp, media.mime, media.name)
}

// maskPhone hides the middle digits of a phone number for log output,
// keeping the country code + last 4 visible (e.g. 5511***8888) so
// operators can correlate failures without leaking PII into log aggregators.
func maskPhone(p string) string {
	if len(p) <= 8 {
		return p
	}
	return p[:4] + strings.Repeat("*", len(p)-8) + p[len(p)-4:]
}
