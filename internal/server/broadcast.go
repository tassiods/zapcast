// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Broadcast lifecycle: validate, return 202 immediately, fan-out in a
// goroutine. Progress streams over SSE.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
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

// windowPersistEvery batches the rolling-window UPDATEs. One write per
// message would serialise every API read behind the broadcast on the
// single-connection pool; a crash loses at most the last few increments,
// which only under-counts the resumed window.
const windowPersistEvery = 25

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

	broadcastID, hErr := app.contacts.CreateBroadcast(r.Context(), label, body.Message, len(contacts), 0, 0, media.kind, media.name)
	if hErr != nil {
		handle.Abandon()
		handleError(w, r, http.StatusInternalServerError, hErr)
		return
	}

	metricBroadcastsTotal.Add(1)
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
	app.cancelBroadcast("operator")
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

	ctx := handle.Ctx
	client := app.WA()

	uploadResp, ok := app.uploadIfMedia(ctx, client, broadcastID, contacts, label, media)
	if !ok {
		return
	}

	okCount, failCount, abortedCount, aborted := app.sendAll(ctx, client, broadcastID, label, message, contacts, templateVars, media, uploadResp)

	// Detached ctx: on the abort path the broadcast ctx is already
	// cancelled, but the final counters still describe what happened.
	pctx, pcancel := persistContext()
	defer pcancel()
	if err := app.contacts.UpdateBroadcastCounts(pctx, broadcastID, okCount, failCount); err != nil {
		app.logger.Warn("failed to update broadcast counts", "err", err, "broadcast_id", broadcastID)
	}
	app.broadcastEvent(buildBroadcastDoneEvent(broadcastID, label, len(contacts), okCount, failCount, abortedCount, aborted, ""))
}

// uploadIfMedia uploads the attachment once. Failure aborts the broadcast.
func (app *App) uploadIfMedia(ctx context.Context, client WAClient, broadcastID int64, contacts []Contact, label string, media mediaPayload) (whatsmeow.UploadResponse, bool) {
	if !media.present() {
		return whatsmeow.UploadResponse{}, true
	}

	mediaType, _, _ := classifyMedia(media.mime)
	upCtx, upCancel := context.WithTimeout(ctx, uploadTimeout)
	defer upCancel()

	resp, err := client.Upload(upCtx, media.data, mediaType)
	if err != nil {
		app.logger.Error("media upload failed", "broadcast_id", broadcastID, "err", err)
		errMsg := "upload_failed: " + err.Error()
		// Detached ctx — the upload may have failed precisely because ctx
		// was cancelled, and these rows must still record the outcome.
		pctx, pcancel := persistContext()
		defer pcancel()
		for _, c := range contacts {
			if _, perr := app.contacts.InsertBroadcastMessage(pctx, broadcastID, c.Name, c.Phone, "", "failed", errMsg); perr != nil {
				app.logger.Warn("failed to persist upload-failed message", "err", perr, "broadcast_id", broadcastID)
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
	// Rolling-hour cap state. The row is persisted per batch so that (a) the
	// running goroutine's own cap accounting survives across pause/resume
	// within this process, and (b) its lingering presence at the next boot
	// flags this broadcast as interrupted (see TakeInterruptedBroadcasts).
	// Note: broadcasts are NOT resumed across a restart — a killed goroutine
	// leaves the unsent remainder lost. LoadBroadcastWindow returns a
	// zero-value window (windowStart = now, sentInWindow = 0) when no row
	// exists yet.
	win, werr := app.contacts.LoadBroadcastWindow(ctx, broadcastID)
	if werr != nil {
		app.logger.Warn("failed to load broadcast window — starting fresh", "err", werr, "broadcast_id", broadcastID)
		win = BroadcastWindow{BroadcastID: broadcastID, WindowStart: time.Now().UTC()}
	}
	windowStart := win.WindowStart
	sentInWindow := win.SentInWindow

	saveWindow := func() {
		pctx, pcancel := persistContext()
		defer pcancel()
		err := app.contacts.SaveBroadcastWindow(pctx, BroadcastWindow{
			BroadcastID:  broadcastID,
			WindowStart:  windowStart,
			SentInWindow: sentInWindow,
		})
		if err != nil {
			app.logger.Warn("failed to persist broadcast window", "err", err, "broadcast_id", broadcastID)
		}
	}

	defer func() {
		// Drop the window row once the broadcast is finished — there's no
		// point keeping per-broadcast state for a row that won't be
		// resumed. Best-effort; an orphan row gets cleaned via FK CASCADE
		// when the parent broadcast is eventually deleted. Detached ctx —
		// this defer also runs on the abort path where ctx is cancelled.
		pctx, pcancel := persistContext()
		defer pcancel()
		if err := app.contacts.ClearBroadcastWindow(pctx, broadcastID); err != nil {
			app.logger.Debug("failed to clear broadcast window", "err", err, "broadcast_id", broadcastID)
		}
	}()

	// recordAborted books contacts[from:] as aborted on a detached ctx (the
	// broadcast ctx is cancelled by definition here, so its inserts would all
	// fail immediately and the history would lose the aborted remainder).
	recordAborted := func(from int) {
		aborted = true
		abortedCount = len(contacts) - from
		metricBroadcastAborts.Add(1)
		pctx, pcancel := persistContext()
		defer pcancel()
		for _, rest := range contacts[from:] {
			if _, err := app.contacts.InsertBroadcastMessage(pctx, broadcastID, rest.Name, rest.Phone, "", "failed", "aborted"); err != nil {
				app.logger.Warn("failed to persist aborted broadcast message", "err", err, "broadcast_id", broadcastID)
			}
			failCount++
		}
	}

	for i, c := range contacts {
		if ctx.Err() != nil {
			recordAborted(i)
			break
		}

		if app.broadcaster.hourlyCap > 0 && sentInWindow >= app.broadcaster.hourlyCap {
			windowStart, sentInWindow = app.pauseForHourlyCap(ctx, broadcastID, windowStart)
			saveWindow()
			// A cancel during the (possibly hour-long) pause must abort here
			// rather than fall through into a doomed SendMessage that would be
			// recorded as "failed" and needlessly enqueued for retry.
			if ctx.Err() != nil {
				recordAborted(i)
				break
			}
		}

		rendered := renderTemplate(message, c, templateVars)
		jid := types.NewJID(c.Phone, types.DefaultUserServer)
		msg := composeMessage(rendered, media, uploadResp)

		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		sendResp, err := client.SendMessage(sendCtx, jid, msg)
		cancel()

		// The send already happened (or definitively failed) — its history
		// row and retry enqueue must not be dropped by a cancellation that
		// races in between.
		pctx, pcancel := persistContext()

		var status, errMsg, messageID string
		if err != nil {
			failCount++
			status = "failed"
			errMsg = err.Error()
			metricBroadcastMessagesErr.Add(1)
			app.logger.Warn("broadcast message failed", "broadcast_id", broadcastID, "contact", maskPhone(c.Phone), "err", err)
		} else {
			okCount++
			status = "sent"
			messageID = sendResp.ID
			metricBroadcastMessagesOK.Add(1)
			app.logger.Info("broadcast message sent", "broadcast_id", broadcastID, "contact", maskPhone(c.Phone), "message_id", sendResp.ID)
		}

		// History row first — the outbox enqueue below references its id so
		// the retry promotes exactly this row, not a phone-matched guess.
		msgRowID, perr := app.contacts.InsertBroadcastMessage(pctx, broadcastID, c.Name, c.Phone, messageID, status, errMsg)
		if perr != nil {
			app.logger.Warn("failed to persist broadcast message", "err", perr, "broadcast_id", broadcastID)
		}

		if err != nil {
			// Enqueue for retry. We capture the rendered body (template
			// already substituted) + the shared UploadResponse fields so
			// the outbox worker can resend without rebuilding either.
			row := OutboxRow{
				BroadcastID:  broadcastID,
				MessageRowID: msgRowID,
				ContactName:  c.Name,
				Phone:        c.Phone,
				Rendered:     rendered,
				LastError:    errMsg,
			}
			if media.present() {
				row.MediaKind = media.kind
				row.MediaMime = media.mime
				row.MediaName = media.name
				row.MediaURL = uploadResp.URL
				row.MediaDirectPath = uploadResp.DirectPath
				row.MediaKey = uploadResp.MediaKey
				row.MediaFileSHA = uploadResp.FileSHA256
				row.MediaEncSHA = uploadResp.FileEncSHA256
				row.MediaFileLen = uploadResp.FileLength
			}
			if qerr := app.contacts.EnqueueOutbox(pctx, row); qerr != nil {
				app.logger.Warn("enqueue outbox failed", "err", qerr, "broadcast_id", broadcastID)
			}
		}
		pcancel()

		app.broadcastEvent(Event{
			Type: "broadcast_progress",
			Data: BroadcastProgressData{
				BroadcastID: broadcastID,
				Current:     i + 1,
				Total:       len(contacts),
				OK:          okCount,
				Fail:        failCount,
				Contact:     c.Name,
				Success:     err == nil,
			},
		})

		sentInWindow++
		if sentInWindow%windowPersistEvery == 0 {
			saveWindow()
		}

		// Per-message pacing. Timer (not time.After) so cancellation frees
		// it immediately instead of leaking it until the delay elapses.
		delay := computeBroadcastDelay(app.broadcaster.delay, app.broadcaster.jitter, rand.Float64)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			// loop's ctx check on the next iteration finishes out cleanly.
			timer.Stop()
		}
	}
	return okCount, failCount, abortedCount, aborted
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

// pauseForHourlyCap sleeps until the rolling-hour window resets.
func (app *App) pauseForHourlyCap(ctx context.Context, broadcastID int64, windowStart time.Time) (time.Time, int) {
	wait := time.Hour - time.Since(windowStart)
	if wait > 0 {
		resumeAt := time.Now().Add(wait)
		app.logger.Warn("broadcast hit hourly cap, pausing",
			"broadcast_id", broadcastID, "cap", app.broadcaster.hourlyCap,
			"resume_at", resumeAt.Format(time.RFC3339))
		app.broadcastEvent(Event{
			Type: "broadcast_paused",
			Data: BroadcastPausedData{
				BroadcastID: broadcastID,
				Reason:      "hourly_cap",
				ResumeAt:    resumeAt.Format(time.RFC3339),
				WaitSeconds: int(wait.Seconds()),
			},
		})
		// Timer instead of time.After — this wait can be most of an hour,
		// and an abort mid-pause must not leave the timer pinned in memory.
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}
	return time.Now().UTC(), 0
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
