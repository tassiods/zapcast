package server

import (
	"context"
	"fmt"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIndependentKnownReceiptBeforeCrashMustFinishQueue(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	if ok, err := app.contacts.claimDelivery(ctx, rows[0]); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := app.contacts.UpdateMessageStatus(ctx, rows[0].ProviderID, "read"); err != nil {
		t.Fatal(err)
	}
	var dbPath string
	if err := app.contacts.db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	if err := app.contacts.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewContactStore(ctx, app.logger, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	app.contacts = reopened
	if err := app.contacts.recoverDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	hist, _ := app.contacts.ListBroadcasts(ctx, 10, 0)
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	t.Logf("after recovery history=%+v messages=%+v", hist, msgs)
	mux := http.NewServeMux()
	app.registerRoutes(mux)
	retryResp := httptest.NewRecorder()
	mux.ServeHTTP(retryResp, httptest.NewRequest("POST", fmt.Sprintf("/api/delivery/%d/resolve", rows[0].ID), strings.NewReader(`{"action":"retry","acknowledgeDuplicateRisk":true}`)))
	worker := &OutboxWorker{app: app, logger: app.logger}
	worker.processOnce()
	wa := app.WA().(*fakeWA)
	wa.mu.Lock()
	calls := len(wa.sendCalls)
	wa.mu.Unlock()
	t.Logf("operator retry response=%d RPCs for already-read intention=%d", retryResp.Code, calls)
	if hist[0].Uncertain != 0 || hist[0].Status != "completed" || calls != 0 {
		t.Fatalf("durable provider receipt became false uncertainty and allowed replay on startup: history=%+v retry=%d calls=%d", hist, retryResp.Code, calls)
	}
}

func TestDeliveryConfirmedIntentCannotBeReplayed(t *testing.T) {
	for _, status := range []string{"sent", "delivered", "read"} {
		for _, path := range []string{"retry", "orphan_retry", "claim", "orphan_claim", "recovery", "orphan_recovery"} {
			if status == "sent" && strings.HasPrefix(path, "orphan") {
				continue
			}
			t.Run(status+"/"+path, func(t *testing.T) {
				app := newTestAppWithWA(t, &fakeWA{connected: true})
				id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
				if err != nil {
					t.Fatal(err)
				}
				rows, err := app.contacts.deliveryRows(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				row := rows[0]
				if strings.HasPrefix(path, "orphan") {
					if err = app.contacts.UpdateMessageStatus(ctx, row.ProviderID, status); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err = app.contacts.db.ExecContext(ctx, `UPDATE broadcast_messages SET status=?,message_id=? WHERE id=?`, status, row.ProviderID, row.MessageRowID); err != nil {
						t.Fatal(err)
					}
				}
				if strings.Contains(path, "recovery") {
					if _, err = app.contacts.db.ExecContext(ctx, `UPDATE outbox SET status='inflight' WHERE id=?`, row.ID); err != nil {
						t.Fatal(err)
					}
					if err = app.contacts.recoverDeliveries(ctx); err != nil {
						t.Fatal(err)
					}
				} else if strings.HasSuffix(path, "retry") {
					if _, err = app.contacts.db.ExecContext(ctx, `UPDATE outbox SET status='uncertain' WHERE id=?`, row.ID); err != nil {
						t.Fatal(err)
					}
					mux := http.NewServeMux()
					app.registerRoutes(mux)
					resp := httptest.NewRecorder()
					mux.ServeHTTP(resp, httptest.NewRequest("POST", fmt.Sprintf("/api/delivery/%d/resolve", row.ID), strings.NewReader(`{"action":"retry","acknowledgeDuplicateRisk":true}`)))
					if resp.Code != 409 {
						t.Fatalf("confirmed retry accepted: %d %s", resp.Code, resp.Body.String())
					}
				} else {
					worker := &OutboxWorker{app: app, logger: app.logger}
					worker.attempt(ctx, app.WA(), row)
				}
				msgs, err := app.contacts.GetBroadcastMessages(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if msgs[0].Status != status || msgs[0].MessageID != row.ProviderID {
					t.Fatalf("confirmed history changed: %+v", msgs)
				}
				wa := app.WA().(*fakeWA)
				wa.mu.Lock()
				calls := len(wa.sendCalls)
				wa.mu.Unlock()
				if calls != 0 {
					t.Fatalf("confirmed intent emitted %d RPCs", calls)
				}
				var slots int
				if err = app.contacts.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_slots`).Scan(&slots); err != nil {
					t.Fatal(err)
				}
				if slots != 0 {
					t.Fatalf("confirmation consumed quota: %d", slots)
				}
				var queued int
				if err = app.contacts.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE id=?`, row.ID).Scan(&queued); err != nil {
					t.Fatal(err)
				}
				if queued != 0 {
					t.Fatalf("confirmed row remains queued: %d", queued)
				}
				hist, err := app.contacts.ListBroadcasts(ctx, 10, 0)
				if err != nil {
					t.Fatal(err)
				}
				if hist[0].Status != "completed" || hist[0].Uncertain != 0 || hist[0].OK != 1 || hist[0].Fail != 0 {
					t.Fatalf("not completed: %+v", hist)
				}
			})
		}
	}
}

func TestIndependentLateErrorMustNotEraseOperatorConfirmation(t *testing.T) {
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	wa.hook = func(c context.Context, j types.JID, m *waProto.Message) (whatsmeow.SendResponse, error) {
		close(entered)
		<-release
		return whatsmeow.SendResponse{}, context.Canceled
	}
	app := newTestAppWithWA(t, wa)
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	worker := &OutboxWorker{app: app, logger: app.logger}
	go func() { defer close(finished); worker.attempt(ctx, wa, rows[0]) }()
	<-entered
	mux := http.NewServeMux()
	app.registerRoutes(mux)
	cancelResp := httptest.NewRecorder()
	mux.ServeHTTP(cancelResp, httptest.NewRequest("POST", fmt.Sprintf("/api/broadcasts/%d/cancel", id), nil))
	confirmResp := httptest.NewRecorder()
	mux.ServeHTTP(confirmResp, httptest.NewRequest("POST", fmt.Sprintf("/api/delivery/%d/resolve", rows[0].ID), strings.NewReader(`{"action":"confirm_sent","providerConfirmed":true,"messageId":"provider-confirmed"}`)))
	close(release)
	<-finished
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	var n int
	if err := app.contacts.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	t.Logf("cancel=%d confirm=%d history=%+v outbox=%d", cancelResp.Code, confirmResp.Code, msgs, n)
	if cancelResp.Code != 200 || confirmResp.Code != 200 {
		t.Fatal("setup did not reach confirmation")
	}
	if msgs[0].Status != "sent" {
		t.Fatalf("late cancelled RPC erased confirmed success, no review row remains: %+v", msgs)
	}
}

func TestDeliveryLateAttemptPreservesResolvedOutcome(t *testing.T) {
	for _, resolution := range []string{"manual", "delivered", "read", "inflight_read"} {
		for _, outcome := range []string{"error", "success", "rejection"} {
			t.Run(resolution+"/"+outcome, func(t *testing.T) {
				entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
				wa.hook = func(context.Context, types.JID, *waProto.Message) (whatsmeow.SendResponse, error) {
					close(entered)
					<-release
					switch outcome {
					case "error":
						return whatsmeow.SendResponse{}, context.Canceled
					case "rejection":
						return whatsmeow.SendResponse{}, &RejectedDeliveryError{Err: errBoom}
					default:
						return whatsmeow.SendResponse{ID: "late-rpc-id"}, nil
					}
				}
				app := newTestAppWithWA(t, wa)
				id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
				if err != nil {
					t.Fatal(err)
				}
				rows, err := app.contacts.deliveryRows(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				row := rows[0]
				worker := &OutboxWorker{app: app, logger: app.logger}
				go func() { defer close(finished); worker.attempt(ctx, wa, row) }()
				<-entered
				// Always release the fake even if a setup assertion fails.
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
					<-finished
				}()
				mux := http.NewServeMux()
				app.registerRoutes(mux)
				if resolution != "inflight_read" {
					resp := httptest.NewRecorder()
					mux.ServeHTTP(resp, httptest.NewRequest("POST", fmt.Sprintf("/api/broadcasts/%d/cancel", id), nil))
					if resp.Code != 200 {
						t.Fatal(resp.Code, resp.Body.String())
					}
				}
				wantStatus, wantID := "sent", "provider-confirmed"
				if resolution == "manual" {
					resp := httptest.NewRecorder()
					mux.ServeHTTP(resp, httptest.NewRequest("POST", fmt.Sprintf("/api/delivery/%d/resolve", row.ID), strings.NewReader(`{"action":"confirm_sent","providerConfirmed":true,"messageId":"provider-confirmed"}`)))
					if resp.Code != 200 {
						t.Fatal(resp.Code, resp.Body.String())
					}
				} else {
					wantStatus = resolution
					if resolution == "inflight_read" {
						wantStatus = "read"
					}
					wantID = row.ProviderID
					if err = app.contacts.UpdateMessageStatus(ctx, wantID, wantStatus); err != nil {
						t.Fatal(err)
					}
				}
				before, err := app.contacts.GetBroadcastMessages(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				close(release)
				<-finished
				after, err := app.contacts.GetBroadcastMessages(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if after[0].Status != wantStatus || after[0].MessageID != wantID || after[0].SentAt != before[0].SentAt || after[0].DeliveredAt != before[0].DeliveredAt || after[0].ReadAt != before[0].ReadAt {
					t.Fatalf("late %s replaced resolution: before=%+v after=%+v", outcome, before, after)
				}
				var n int
				if err = app.contacts.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE id=?`, row.ID).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Fatalf("phantom review/attempt remains: %d", n)
				}
				hist, err := app.contacts.ListBroadcasts(ctx, 10, 0)
				if err != nil {
					t.Fatal(err)
				}
				if hist[0].OK != 1 || hist[0].Fail != 0 || hist[0].Uncertain != 0 {
					t.Fatalf("resolved counters regressed: %+v", hist)
				}
			})
		}
	}
}

func TestIndependentStaleWorkerSnapshotMustHonorRescheduledDueTime(t *testing.T) {
	wa := &fakeWA{connected: true, sendErrAll: &RejectedDeliveryError{Err: errBoom}}
	app := newTestAppWithWA(t, wa)
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, err = app.contacts.db.ExecContext(ctx, `UPDATE outbox SET next_attempt_at=? WHERE broadcast_id=?`, now.Format("2006-01-02 15:04:05"), id); err != nil {
		t.Fatal(err)
	}
	stale, err := app.contacts.FetchDueOutbox(ctx, now)
	if err != nil || len(stale) != 1 {
		t.Fatal(stale, err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	worker := &OutboxWorker{app: app, logger: app.logger, now: func() time.Time { return now }}
	worker.attempt(ctx, wa, rows[0])
	due, _ := app.contacts.FetchDueOutbox(ctx, now)
	if len(due) != 0 {
		t.Fatal("setup: rejection did not back off")
	}
	worker.attempt(ctx, wa, stale[0])
	wa.mu.Lock()
	n := len(wa.sendCalls)
	wa.mu.Unlock()
	t.Logf("RPCs before backoff expires=%d", n)
	if n != 1 {
		t.Fatalf("stale fetch bypassed committed backoff: calls=%d", n)
	}
}

func TestDeliveryBackoffAdmissionUsesPersistedClock(t *testing.T) {
	wa := &fakeWA{connected: true, sendErrAll: &RejectedDeliveryError{Err: errBoom}}
	app := newTestAppWithWA(t, wa)
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.FixedZone("offset", -3*60*60))
	if _, err = app.contacts.db.ExecContext(ctx, `UPDATE outbox SET next_attempt_at=? WHERE broadcast_id=?`, now.UTC().Format("2006-01-02 15:04:05"), id); err != nil {
		t.Fatal(err)
	}
	stale, err := app.contacts.FetchDueOutbox(ctx, now)
	if err != nil || len(stale) != 1 {
		t.Fatal(stale, err)
	}
	initial, err := app.contacts.deliveryRows(ctx, id)
	if err != nil || len(initial) != 1 {
		t.Fatal(initial, err)
	}
	worker := &OutboxWorker{app: app, logger: app.logger, now: func() time.Time { return now }}
	worker.attempt(ctx, wa, initial[0])
	base := now
	for attempt := 1; attempt <= 2; attempt++ {
		dueAt := base.Add(outboxBackoffBase * time.Duration(1<<(attempt-1)))
		var persisted string
		var tries int
		if err = app.contacts.db.QueryRowContext(ctx, `SELECT next_attempt_at,attempts FROM outbox WHERE id=?`, stale[0].ID).Scan(&persisted, &tries); err != nil {
			t.Fatal(err)
		}
		persistedTime, err := time.Parse(time.RFC3339Nano, persisted)
		if err != nil {
			t.Fatal(err)
		}
		if !persistedTime.Equal(dueAt) || tries != attempt {
			t.Fatalf("backoff incorrect: due=%s attempts=%d", persisted, tries)
		}
		for _, before := range []time.Time{base, dueAt.Add(-time.Second), dueAt.Add(-time.Nanosecond)} {
			now = before
			due, err := app.contacts.FetchDueOutbox(ctx, now)
			if err != nil || len(due) != 0 {
				t.Fatal("not due", due, err)
			}
			worker.attempt(ctx, wa, stale[0])
			var slots, attempts int
			var cadence int64
			if err = app.contacts.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_slots`).Scan(&slots); err != nil {
				t.Fatal(err)
			}
			if err = app.contacts.db.QueryRowContext(ctx, `SELECT attempts FROM outbox WHERE id=?`, stale[0].ID).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if err = app.contacts.db.QueryRowContext(ctx, `SELECT next_at FROM delivery_cadence WHERE id=1`).Scan(&cadence); err != nil {
				t.Fatal(err)
			}
			wa.mu.Lock()
			calls := len(wa.sendCalls)
			wa.mu.Unlock()
			if calls != attempt || slots != attempt || attempts != attempt || cadence != base.UnixNano() {
				t.Fatalf("early stale claim debited work: calls=%d slots=%d attempts=%d cadence=%d", calls, slots, attempts, cadence)
			}
		}
		now = dueAt
		worker.attempt(ctx, wa, stale[0])
		wa.mu.Lock()
		calls := len(wa.sendCalls)
		wa.mu.Unlock()
		if calls != attempt+1 {
			t.Fatalf("due boundary didn't admit: calls=%d", calls)
		}
		base = now
	}
}

func TestDeliveryLateBookkeepingRequiresCurrentQueueOwnership(t *testing.T) {
	for _, state := range []string{"removed", "pending", "relinked"} {
		for _, outcome := range []string{"error", "success", "rejection"} {
			t.Run(state+"/"+outcome, func(t *testing.T) {
				entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
				wa.hook = func(context.Context, types.JID, *waProto.Message) (whatsmeow.SendResponse, error) {
					close(entered)
					<-release
					switch outcome {
					case "error":
						return whatsmeow.SendResponse{}, errBoom
					case "rejection":
						return whatsmeow.SendResponse{}, &RejectedDeliveryError{Err: errBoom}
					default:
						return whatsmeow.SendResponse{ID: "stale-success"}, nil
					}
				}
				app := newTestAppWithWA(t, wa)
				id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, mediaPayload{})
				if err != nil {
					t.Fatal(err)
				}
				rows, err := app.contacts.deliveryRows(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				row := rows[0]
				worker := &OutboxWorker{app: app, logger: app.logger}
				go func() { defer close(finished); worker.attempt(ctx, wa, row) }()
				<-entered
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
					<-finished
				}()
				switch state {
				case "removed":
					_, err = app.contacts.db.ExecContext(ctx, `DELETE FROM outbox WHERE id=?`, row.ID)
				case "pending":
					_, err = app.contacts.db.ExecContext(ctx, `UPDATE outbox SET status='pending' WHERE id=?`, row.ID)
				case "relinked":
					_, err = app.contacts.db.ExecContext(ctx, `UPDATE outbox SET broadcast_message_id=? WHERE id=?`, rows[1].MessageRowID, row.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				before, err := app.contacts.GetBroadcastMessages(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				close(release)
				<-finished
				after, err := app.contacts.GetBroadcastMessages(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("unowned late result changed history: before=%+v after=%+v", before, after)
				}
				if state != "removed" {
					var status string
					var attempts int
					var mid int64
					if err = app.contacts.db.QueryRowContext(ctx, `SELECT status,attempts,broadcast_message_id FROM outbox WHERE id=?`, row.ID).Scan(&status, &attempts, &mid); err != nil {
						t.Fatal(err)
					}
					expected := "inflight"
					expectedMid := rows[1].MessageRowID
					if state == "pending" {
						expected = "pending"
						expectedMid = row.MessageRowID
					}
					if status != expected || attempts != 0 || mid != expectedMid {
						t.Fatalf("unowned late result changed queue: %s %d %d", status, attempts, mid)
					}
				}
			})
		}
	}
}

func TestDeliveryReceiptRacingManualRetryCannotReplay(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		t.Run(fmt.Sprintf("orphan=%v", orphan), func(t *testing.T) {
			for i := 0; i < 6; i++ {
				app := newTestAppWithWA(t, &fakeWA{connected: true})
				id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
				if err != nil {
					t.Fatal(err)
				}
				rows, err := app.contacts.deliveryRows(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				row := rows[0]
				if !orphan {
					if ok, err := app.contacts.claimDelivery(ctx, row); err != nil || !ok {
						t.Fatal(ok, err)
					}
				}
				if err = app.contacts.uncertainDelivery(ctx, row, "review"); err != nil {
					t.Fatal(err)
				}
				start, receiptDone, retryDone := make(chan struct{}), make(chan error, 1), make(chan *httptest.ResponseRecorder, 1)
				mux := http.NewServeMux()
				app.registerRoutes(mux)
				go func() { <-start; receiptDone <- app.contacts.UpdateMessageStatus(ctx, row.ProviderID, "read") }()
				go func() {
					<-start
					resp := httptest.NewRecorder()
					mux.ServeHTTP(resp, httptest.NewRequest("POST", fmt.Sprintf("/api/delivery/%d/resolve", row.ID), strings.NewReader(`{"action":"retry","acknowledgeDuplicateRisk":true}`)))
					retryDone <- resp
				}()
				close(start)
				if err = <-receiptDone; err != nil {
					t.Fatal(err)
				}
				resp := <-retryDone
				if resp.Code != 200 && resp.Code != 409 && resp.Code != 404 {
					t.Fatal(resp.Code, resp.Body.String())
				}
				worker := &OutboxWorker{app: app, logger: app.logger}
				worker.attempt(ctx, app.WA(), row)
				wa := app.WA().(*fakeWA)
				wa.mu.Lock()
				calls := len(wa.sendCalls)
				wa.mu.Unlock()
				if calls != 0 {
					t.Fatalf("receipt/retry race emitted RPC: %d", calls)
				}
				hist, err := app.contacts.ListBroadcasts(ctx, 10, 0)
				if err != nil {
					t.Fatal(err)
				}
				if hist[0].Status != "completed" || hist[0].Uncertain != 0 || hist[0].OK != 1 || hist[0].Fail != 0 {
					t.Fatalf("receipt/retry race unresolved: %+v", hist)
				}
			}
		})
	}
}

func TestDeliveryRecoveryConfirmedReceiptRollsBackAtomically(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := app.contacts.deliveryRows(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if ok, err := app.contacts.claimDelivery(ctx, row); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = app.contacts.UpdateMessageStatus(ctx, row.ProviderID, "read"); err != nil {
		t.Fatal(err)
	}
	if _, err = app.contacts.db.ExecContext(ctx, `CREATE TRIGGER abort_recovery BEFORE DELETE ON outbox BEGIN SELECT RAISE(ABORT,'injected recovery failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = app.contacts.recoverDeliveries(ctx); err == nil {
		t.Fatal("recovery ignored transaction failure")
	}
	var queue, campaign, history string
	if err = app.contacts.db.QueryRowContext(ctx, `SELECT o.status,b.delivery_state,m.status FROM outbox o JOIN broadcasts b ON b.id=o.broadcast_id JOIN broadcast_messages m ON m.id=o.broadcast_message_id WHERE o.id=?`, row.ID).Scan(&queue, &campaign, &history); err != nil {
		t.Fatal(err)
	}
	if queue != "inflight" || campaign != "active" || history != "read" {
		t.Fatalf("partial recovery committed: %s %s %s", queue, campaign, history)
	}
	if _, err = app.contacts.db.ExecContext(ctx, `DROP TRIGGER abort_recovery`); err != nil {
		t.Fatal(err)
	}
	if err = app.contacts.recoverDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	hist, err := app.contacts.ListBroadcasts(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hist[0].Status != "completed" || hist[0].Uncertain != 0 {
		t.Fatalf("retry recovery incomplete: %+v", hist)
	}
}
