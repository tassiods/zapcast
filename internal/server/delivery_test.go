// SPDX-FileCopyrightText: 2026 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"sync/atomic"
	"testing"
	"time"
)

type deliveryHookWA struct {
	WAClient
	hook func(context.Context, types.JID, *waProto.Message) (whatsmeow.SendResponse, error)
}

func (w *deliveryHookWA) SendMessage(c context.Context, j types.JID, m *waProto.Message) (whatsmeow.SendResponse, error) {
	return w.hook(c, j, m)
}
func TestDeliveryAllIntentsExistBeforeFirstRPC(t *testing.T) {
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 2, 0, 0, "", "")
	var observed atomic.Int32
	wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
		msgs, err := app.contacts.GetBroadcastMessages(c, id)
		if err != nil {
			t.Error(err)
		}
		if len(msgs) != 2 {
			t.Errorf("RPC must see all durable recipients, got %d", len(msgs))
		} else {
			observed.Add(1)
		}
		return whatsmeow.SendResponse{ID: "test"}, nil
	}
	ch := runBroadcastForTest(t, app, ctx, id, "G", "hi {nome}", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, nil, "", "", "")
	defer app.unsubscribe(ch)
	drainUntilDone(t, ch, 2*time.Second)
	app.broadcaster.wg.Wait()
	if observed.Load() != 2 {
		t.Fatal("recipient intents were not durable before RPC")
	}
}

func TestDeliveryCommitFailureNeverResends(t *testing.T) {
	wa := &fakeWA{connected: true}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 1, 0, 1, "", "")
	mid, _ := app.contacts.InsertBroadcastMessage(ctx, id, "A", "5511111111111", "", "failed", "boom")
	if err := app.contacts.EnqueueOutbox(ctx, OutboxRow{BroadcastID: id, MessageRowID: mid, ContactName: "A", Phone: "5511111111111", Rendered: "hi"}); err != nil {
		t.Fatal(err)
	}
	_, err := app.contacts.db.ExecContext(ctx, `CREATE TRIGGER fail_commit BEFORE DELETE ON outbox BEGIN SELECT RAISE(ABORT,'injected commit failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	w := &OutboxWorker{app: app, logger: app.logger, now: func() time.Time { return rows[0].NextAttemptAt }}
	w.attempt(ctx, wa, rows[0])
	w.attempt(ctx, wa, rows[0])
	wa.mu.Lock()
	n := len(wa.sendCalls)
	wa.mu.Unlock()
	if n != 1 {
		t.Fatalf("successful remote call repeated after commit failure: %d", n)
	}
	rows, _ = app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	if len(rows) != 0 {
		t.Fatalf("ambiguous attempt still eligible: %v", rows)
	}
	var state string
	_ = app.contacts.db.QueryRowContext(ctx, `SELECT status FROM outbox`).Scan(&state)
	if state != "uncertain" {
		t.Fatalf("need explicit uncertain status: %s", state)
	}
}

func TestDeliveryInFlightCancellationSuppressesAllRetries(t *testing.T) {
	runctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 2, 0, 0, "", "")
	wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
		calls.Add(1)
		func() {
			if err := app.cancelBroadcast("operator"); err != nil {
				t.Fatal(err)
			}
		}()
		cancel()
		return whatsmeow.SendResponse{}, c.Err()
	}
	ch := runBroadcastForTest(t, app, runctx, id, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, nil, "", "", "")
	defer app.unsubscribe(ch)
	drainUntilDone(t, ch, 2*time.Second)
	app.broadcaster.wg.Wait()
	due, _ := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	w := &OutboxWorker{app: app, logger: app.logger}
	for _, row := range due {
		w.attempt(ctx, wa, row)
	}
	if calls.Load() != 1 || len(due) != 0 {
		t.Fatalf("cancelled campaign retried: calls=%d eligible=%d", calls.Load(), len(due))
	}
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 2 || msgs[0].Status != "uncertain" || msgs[1].Status != "cancelled" {
		t.Fatalf("cancel outcomes must distinguish inflight/pending: %+v", msgs)
	}
}

func TestDeliveryUnknownProviderErrorIsUncertain(t *testing.T) {
	for _, sendErr := range []error{context.DeadlineExceeded, errBoom} {
		t.Run(sendErr.Error(), func(t *testing.T) {
			wa := &fakeWA{connected: true, sendErrAll: sendErr}
			app := newTestAppWithWA(t, wa)
			id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
			if err != nil {
				t.Fatal(err)
			}
			rows, _ := app.contacts.deliveryRows(ctx, id)
			w := &OutboxWorker{app: app, logger: app.logger}
			w.attempt(ctx, wa, rows[0])
			w.attempt(ctx, wa, rows[0])
			wa.mu.Lock()
			n := len(wa.sendCalls)
			wa.mu.Unlock()
			msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
			if n != 1 || len(msgs) != 1 || msgs[0].Status != "uncertain" {
				t.Fatalf("unknown result auto-retried: calls=%d msgs=%+v", n, msgs)
			}
		})
	}
}

type stableDeliveryWA struct {
	*fakeWA
	ids []string
}

func (w *stableDeliveryWA) SendMessageWithID(c context.Context, j types.JID, m *waProto.Message, id string) (whatsmeow.SendResponse, error) {
	w.ids = append(w.ids, id)
	return w.SendMessage(c, j, m)
}
func TestDeliveryStableProviderIdentityExistsBeforeRPC(t *testing.T) {
	wa := &stableDeliveryWA{fakeWA: &fakeWA{connected: true, sendErrAll: &RejectedDeliveryError{Err: errBoom}}}
	app := newTestAppWithWA(t, wa)
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	w := &OutboxWorker{app: app, logger: app.logger}
	w.attempt(ctx, wa, rows[0])
	// Exercise the retry at its persisted due time, not before backoff.
	due, err := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	if err != nil || len(due) != 1 {
		t.Fatal(due, err)
	}
	w.now = func() time.Time { return due[0].NextAttemptAt }
	w.attempt(ctx, wa, rows[0])
	if len(wa.ids) != 2 || wa.ids[0] == "" || wa.ids[0] != wa.ids[1] {
		t.Fatalf("missing stable provider identity: %v", wa.ids)
	}
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if msgs[0].MessageID != wa.ids[0] {
		t.Fatalf("identity not durable: %+v", msgs)
	}
}

func TestDeliveryBatchDeadlinePreservesUnstartedAttempts(t *testing.T) {
	var calls atomic.Int32
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
		calls.Add(1)
		deadline, ok := c.Deadline()
		if !ok || time.Until(deadline) > 200*time.Millisecond {
			t.Error("batch timeout seam ignored")
			return whatsmeow.SendResponse{}, context.DeadlineExceeded
		}
		<-c.Done()
		return whatsmeow.SendResponse{}, c.Err()
	}
	app := newTestAppWithWA(t, wa)
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	w := &OutboxWorker{app: app, logger: app.logger, batchTimeout: 100 * time.Millisecond, attemptTimeout: time.Second}
	w.processOnce()
	rows, _ := app.contacts.deliveryRows(ctx, id)
	if calls.Load() != 1 || len(rows) != 1 || rows[0].Attempts != 0 {
		t.Fatalf("expired batch consumed unstarted attempt: calls=%d rows=%+v", calls.Load(), rows)
	}
}

func TestDeliveryConcurrentRetryCountsReconcileFromRows(t *testing.T) {
	second := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
		n := calls.Add(1)
		if n == 1 {
			return whatsmeow.SendResponse{}, &RejectedDeliveryError{Err: errBoom}
		}
		if n == 2 {
			close(second)
			select {
			case <-release:
			case <-c.Done():
				return whatsmeow.SendResponse{}, c.Err()
			}
		}
		return whatsmeow.SendResponse{ID: "sent"}, nil
	}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 2, 0, 0, "", "")
	ch := runBroadcastForTest(t, app, ctx, id, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, nil, "", "", "")
	defer app.unsubscribe(ch)
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("no second RPC")
	}
	due, _ := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	if len(due) != 1 {
		t.Fatalf("due=%v", due)
	}
	w := &OutboxWorker{app: app, logger: app.logger, now: func() time.Time { return due[0].NextAttemptAt }}
	w.attempt(ctx, wa, due[0])
	close(release)
	done := drainUntilDone(t, ch, 2*time.Second)
	if done["ok"] != 2 || done["fail"] != 0 {
		t.Fatalf("SSE completion counts stale: %+v", done)
	}
	app.broadcaster.wg.Wait()
	hist, err := app.contacts.ListBroadcasts(ctx, 10, 0)
	if err != nil || len(hist) != 1 || hist[0].OK != 2 || hist[0].Fail != 0 {
		t.Fatalf("stale aggregate overwrote retry: hist=%v err=%v", hist, err)
	}
}

func TestDeliveryReceiptBeforeSendReturnIsRetained(t *testing.T) {
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	app := newTestAppWithWA(t, wa)
	wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
		if err := app.contacts.UpdateMessageStatus(c, "early", "read"); err != nil {
			return whatsmeow.SendResponse{}, err
		}
		if err := app.contacts.UpdateMessageStatus(c, "early", "delivered"); err != nil {
			return whatsmeow.SendResponse{}, err
		}
		return whatsmeow.SendResponse{ID: "early"}, nil
	}
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 1, 0, 0, "", "")
	ch := runBroadcastForTest(t, app, ctx, id, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, nil, "", "", "")
	defer app.unsubscribe(ch)
	drainUntilDone(t, ch, 2*time.Second)
	app.broadcaster.wg.Wait()
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 1 || msgs[0].Status != "read" || msgs[0].ReadAt == "" || msgs[0].DeliveredAt == "" {
		t.Fatalf("early receipt lost/downgraded: %+v", msgs)
	}
}

func TestDeliveryRestartRecoversOnlyNeverStartedIntents(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	ok, err := app.contacts.claimDelivery(ctx, rows[0])
	if err != nil || !ok {
		t.Fatal("claim", err)
	}
	if err = app.contacts.recoverDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	w := &OutboxWorker{app: app, logger: app.logger}
	w.processOnce()
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 2 || msgs[0].Status != "uncertain" || msgs[1].Status != "sent" {
		t.Fatalf("restart must preserve uncertain and resume pending: %+v", msgs)
	}
}

func TestDeliveryGlobalAccountCapSharedAcrossCampaignsAndRetries(t *testing.T) {
	wa := &fakeWA{connected: true}
	app := newTestAppWithWA(t, wa)
	app.broadcaster.hourlyCap = 1
	var intents []OutboxRow
	for _, phone := range []string{"5511111111111", "5511222222222"} {
		id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: phone}}, nil, mediaPayload{})
		if err != nil {
			t.Fatal(err)
		}
		rows, _ := app.contacts.deliveryRows(ctx, id)
		intents = append(intents, rows[0])
	}
	w := &OutboxWorker{app: app, logger: app.logger}
	w.attempt(ctx, wa, intents[0])
	budget, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	w.attempt(budget, wa, intents[1])
	wa.mu.Lock()
	n := len(wa.sendCalls)
	wa.mu.Unlock()
	if n != 1 {
		t.Fatalf("account cap reset across campaigns/retries: calls=%d", n)
	}
	due, _ := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	if len(due) != 1 || due[0].Attempts != 0 {
		t.Fatalf("limiter wait consumed attempt: %+v", due)
	}
}

func TestDeliveryRestartDuringUploadRecoversMediaPayload(t *testing.T) {
	wa := &fakeWA{connected: true, uploadResp: whatsmeow.UploadResponse{URL: "stored", FileLength: 4}}
	app := newTestAppWithWA(t, wa)
	id, err := app.contacts.createDelivery(ctx, 0, "G", "caption", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{data: []byte("data"), kind: "image", mime: "image/jpeg", name: "image.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.contacts.recoverDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	w := &OutboxWorker{app: app, logger: app.logger}
	w.processOnce()
	wa.mu.Lock()
	n := len(wa.sendCalls)
	image := n == 1 && wa.sendCalls[0].Msg.GetImageMessage().GetURL() == "stored"
	wa.mu.Unlock()
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if !image || len(msgs) != 1 || msgs[0].Status != "sent" {
		t.Fatalf("media snapshot not recovered: calls=%d msgs=%+v", n, msgs)
	}
}

func TestDeliveryCampaignStatusTracksDurableCompletion(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	w := &OutboxWorker{app: app, logger: app.logger}
	w.attempt(ctx, app.WA(), rows[0])
	hist, _ := app.contacts.ListBroadcasts(ctx, 10, 0)
	if len(hist) != 1 || hist[0].Status != "completed" {
		t.Fatalf("completed campaign remains active: %+v", hist)
	}
}

func TestDeliveryCompletedSnapshotCannotBeCreatedAgain(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	contacts := []Contact{{Name: "A", Phone: "5511111111111"}}
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", contacts, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	w := &OutboxWorker{app: app, logger: app.logger}
	w.attempt(ctx, app.WA(), rows[0])
	if _, err = app.contacts.createDelivery(ctx, id, "G", "hi", contacts, nil, mediaPayload{}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	due, _ := app.contacts.deliveryRows(ctx, id)
	if len(msgs) != 1 || len(due) != 0 {
		t.Fatalf("worker/initial race recreated completed intentions: msgs=%+v due=%+v", msgs, due)
	}
}

func TestDeliveryLateReceiptReconcilesUncertainOutcome(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	ok, err := app.contacts.claimDelivery(ctx, rows[0])
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = app.contacts.uncertainDelivery(ctx, rows[0], "timeout"); err != nil {
		t.Fatal(err)
	}
	if err = app.contacts.UpdateMessageStatus(ctx, rows[0].ProviderID, "read"); err != nil {
		t.Fatal(err)
	}
	hist, _ := app.contacts.ListBroadcasts(ctx, 10, 0)
	if len(hist) != 1 || hist[0].Uncertain != 0 || hist[0].OK != 1 || hist[0].Status != "completed" {
		t.Fatalf("confirmed receipt left uncertain backlog: %+v", hist)
	}
}

func TestDeliveryCancelledRejectedAttemptCannotBecomePendingAgain(t *testing.T) {
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
		if err := app.contacts.cancelDelivery(ctx, id, "operator"); err != nil {
			t.Error(err)
		}
		return whatsmeow.SendResponse{}, &RejectedDeliveryError{Err: errBoom}
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	w := &OutboxWorker{app: app, logger: app.logger}
	w.attempt(ctx, wa, rows[0])
	var state string
	_ = app.contacts.db.QueryRowContext(ctx, `SELECT status FROM outbox WHERE id=?`, rows[0].ID).Scan(&state)
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if state != "cancelled" || msgs[0].Status != "cancelled" {
		t.Fatalf("cancellation undone by rejection bookkeeping: outbox=%s msgs=%+v", state, msgs)
	}
}

func TestDeliveryShutdownPreservesUnstartedRecipientsForRecovery(t *testing.T) {
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 2, 0, 0, "", "")
	wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
		app.shutdownStop()
		func() {
			if err := app.cancelBroadcast("shutdown"); err != nil {
				t.Fatal(err)
			}
		}()
		return whatsmeow.SendResponse{}, c.Err()
	}
	ch := runBroadcastForTest(t, app, app.shutdownCtx, id, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, nil, "", "", "")
	defer app.unsubscribe(ch)
	drainUntilDone(t, ch, 2*time.Second)
	app.broadcaster.wg.Wait()
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 2 || msgs[0].Status != "uncertain" || msgs[1].Status != "pending" {
		t.Fatalf("shutdown silently cancelled resumable intent: %+v", msgs)
	}
}

type deliveryUploadWA struct {
	WAClient
	uploadHook func(context.Context, []byte, whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
}

func (w *deliveryUploadWA) Upload(c context.Context, b []byte, mt whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return w.uploadHook(c, b, mt)
}
func TestDeliveryCancelledUploadRemainsCancelled(t *testing.T) {
	wa := &deliveryUploadWA{WAClient: &fakeWA{connected: true}}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 1, 0, 0, "image", "synthetic.jpg")
	wa.uploadHook = func(c context.Context, _ []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
		func() {
			if err := app.cancelBroadcast("operator"); err != nil {
				t.Fatal(err)
			}
		}()
		return whatsmeow.UploadResponse{}, c.Err()
	}
	ch := runBroadcastForTest(t, app, ctx, id, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, []byte("data"), "image/jpeg", "synthetic.jpg", "image")
	defer app.unsubscribe(ch)
	drainUntilDone(t, ch, 2*time.Second)
	app.broadcaster.wg.Wait()
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 1 || msgs[0].Status != "cancelled" {
		t.Fatalf("cancelled upload changed to failed: %+v", msgs)
	}
}

func TestDeliveryCorruptedPayloadCannotFallBackToDifferentMessage(t *testing.T) {
	wa := &fakeWA{connected: true}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	_, err := app.contacts.db.ExecContext(ctx, `UPDATE outbox SET payload=x'ff' WHERE broadcast_id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	w := &OutboxWorker{app: app, logger: app.logger}
	w.attempt(ctx, wa, rows[0])
	wa.mu.Lock()
	n := len(wa.sendCalls)
	wa.mu.Unlock()
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if n != 0 || msgs[0].Status != "uncertain" {
		t.Fatalf("corrupted media/text fell back to fabricated message: calls=%d msgs=%+v", n, msgs)
	}
}

func TestDeliveryStaleConfirmationCannotEraseNewlyAdmittedAttempt(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	id, _ := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	rows, _ := app.contacts.deliveryRows(ctx, id)
	rows[0].ExpectedStatus = "uncertain"
	// Model a stale manual confirmation read racing an acknowledged retry.
	if ok, err := app.contacts.claimDelivery(ctx, rows[0]); err != nil || !ok {
		t.Fatal(err)
	}
	err := app.contacts.MarkOutboxSent(ctx, rows[0], "stale-confirmation")
	if !errors.Is(err, ErrOutboxMissing) {
		t.Fatalf("stale review overwrote a new admission: %v", err)
	}
	var state string
	_ = app.contacts.db.QueryRowContext(ctx, `SELECT status FROM outbox`).Scan(&state)
	if state != "inflight" {
		t.Fatal("admitted attempt dropped", state)
	}
}

func TestDeliveryAttemptTimeoutDoesNotExpireFollowingRecipient(t *testing.T) {
	var calls atomic.Int32
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
		if calls.Add(1) == 1 {
			<-c.Done()
			return whatsmeow.SendResponse{}, c.Err()
		}
		if c.Err() != nil {
			t.Error("second recipient inherited first attempt expiry")
		}
		return whatsmeow.SendResponse{ID: "second"}, nil
	}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, mediaPayload{})
	w := &OutboxWorker{app: app, logger: app.logger, batchTimeout: 5 * time.Second, attemptTimeout: 10 * time.Millisecond}
	w.processOnce()
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if calls.Load() != 2 || msgs[0].Status != "uncertain" || msgs[1].Status != "sent" {
		t.Fatalf("per-attempt budgets were not independent: calls=%d msgs=%+v", calls.Load(), msgs)
	}
}
