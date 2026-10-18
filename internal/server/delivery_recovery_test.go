// SPDX-FileCopyrightText: 2026 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

// TestDeliveryProcessCrashRecovery uses a disposable SQLite WAL and a real
// child process killed without closing the DB. No provider or network is used.
func TestDeliveryProcessCrashRecovery(t *testing.T) {
	if path := os.Getenv("ZAPCAST_DELIVERY_CRASH_DB"); path != "" {
		store, err := NewContactStore(ctx, testLogger, path)
		if err != nil {
			t.Fatal(err)
		}
		mode := os.Getenv("ZAPCAST_DELIVERY_CRASH_PHASE")
		media := mediaPayload{}
		if mode == "upload" {
			media = mediaPayload{data: []byte("image"), kind: "image", mime: "image/jpeg", name: "synthetic.jpg"}
		}
		id, err := store.createDelivery(ctx, 0, "crash", "hi {nome}", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, media)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "upload" {
			client := &deliveryUploadWA{WAClient: &fakeWA{connected: true}, uploadHook: func(context.Context, []byte, whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
				if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				return whatsmeow.UploadResponse{}, errors.New("child unexpectedly survived")
			}}
			app := &App{logger: testLogger, contacts: store, broadcaster: NewBroadcaster(testLogger, 0, 0, 0)}
			_ = app.prepareDelivery(ctx, id, client)
			t.Fatal("upload RPC did not terminate the child")
		}
		if mode == "inflight" || mode == "afterfirst" || mode == "pause" {
			rows, err := store.deliveryRows(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			claimed, _, err := store.claimDeliveryAt(ctx, rows[0], time.Now(), 0, 1)
			if err != nil || !claimed {
				t.Fatal("claim", err)
			}
			if mode != "inflight" {
				if err = store.MarkOutboxSent(ctx, rows[0], "before-crash"); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err = syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		t.Fatal("child survived crash")
	}
	for _, phase := range []string{"accepted", "upload", "inflight", "afterfirst", "pause"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crash.db")
			// #nosec G204,G702 -- execute this test binary itself with fixed test arguments.
			child := exec.Command(os.Args[0], "-test.run=^TestDeliveryProcessCrashRecovery$", "-test.count=1")
			child.Env = append(os.Environ(), "ZAPCAST_DELIVERY_CRASH_DB="+path, "ZAPCAST_DELIVERY_CRASH_PHASE="+phase)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || !exit.Sys().(syscall.WaitStatus).Signaled() {
				t.Fatalf("child was not killed: %v %s", err, output)
			}
			store, err := NewContactStore(ctx, testLogger, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			if err = store.recoverDeliveries(ctx); err != nil {
				t.Fatal(err)
			}
			wa := &fakeWA{connected: true, uploadResp: whatsmeow.UploadResponse{URL: "recovered", FileLength: 5}}
			app := &App{logger: testLogger, contacts: store, wa: wa, hub: NewSSEHub(testLogger), broadcaster: NewBroadcaster(testLogger, 0, 0, 0), shutdownCtx: context.Background()}
			if phase == "pause" {
				app.broadcaster.hourlyCap = 1
			}
			budget := 5 * time.Second
			if phase == "pause" {
				budget = 10 * time.Millisecond
			}
			w := &OutboxWorker{app: app, logger: testLogger, batchTimeout: budget}
			w.processOnce()
			msgs, err := store.GetBroadcastMessages(ctx, 1)
			if err != nil || len(msgs) != 2 {
				t.Fatalf("lost durable snapshot: %+v %v", msgs, err)
			}
			want := []string{"sent", "sent"}
			if phase == "inflight" {
				want[0] = "uncertain"
			}
			if phase == "pause" {
				want[1] = "pending"
			}
			for i, m := range msgs {
				if m.Status != want[i] {
					t.Fatalf("phase=%s recipient=%d status=%s want=%s", phase, i, m.Status, want[i])
				}
			}
			t.Logf("real SIGKILL recovery phase=%s statuses=%s/%s calls=%d", phase, msgs[0].Status, msgs[1].Status, len(wa.sendCalls))
		})
	}
}

func TestDeliveryIntentInsertFailureRollsBackEntireCampaign(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	_, err := app.contacts.db.ExecContext(ctx, `CREATE TRIGGER fail_intent BEFORE INSERT ON outbox BEGIN SELECT RAISE(ABORT,'injected enqueue fault'); END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err == nil {
		t.Fatal("expected injected insertion fault")
	}
	for _, table := range []string{"broadcasts", "broadcast_messages", "outbox"} {
		var n int
		if err = app.contacts.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("non-atomic %s n=%d err=%v", table, n, err)
		}
	}
}

func TestDeliveryLegacyMigrationRequiresExplicitReviewWithoutInventingPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_initial.sql", "0002_broadcast_window.sql", "0003_outbox.sql", "0004_outbox_message_fk.sql"} {
		if err = applyMigration(ctx, db, name); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.ExecContext(ctx, `INSERT INTO broadcasts(id,group_name,message,total,ok_count,fail_count) VALUES(1,'legacy','hi',3,0,1);
 INSERT INTO broadcast_messages(id,broadcast_id,contact_name,phone,status,error) VALUES(1,1,'A','5511111111111','failed','unknown');
 INSERT INTO outbox(broadcast_id,broadcast_message_id,contact_name,phone,rendered,next_attempt_at) VALUES(1,1,'A','5511111111111','known captured payload',CURRENT_TIMESTAMP);
 INSERT INTO outbox(broadcast_id,contact_name,phone,rendered,next_attempt_at,status) VALUES(1,'B','5511222222222','legacy dead payload',CURRENT_TIMESTAMP,'dead')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = runMigrations(ctx, testLogger, db); err != nil {
		t.Fatal(err)
	}
	if err = runMigrations(ctx, testLogger, db); err != nil {
		t.Fatal("non-idempotent migration", err)
	}
	store := &ContactStore{db: db, logger: testLogger}
	rows, err := store.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy ambiguity auto-replayed: %+v %v", rows, err)
	}
	hist, err := store.ListBroadcasts(ctx, 10, 0)
	if err != nil || len(hist) != 1 || hist[0].Status != "uncertain" || hist[0].Unresolved != 2 {
		t.Fatalf("missing legacy recipient uncertainty: %+v %v", hist, err)
	}
	var ambiguous int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE status='uncertain'`).Scan(&ambiguous); err != nil || ambiguous != 2 {
		t.Fatalf("legacy dead/pending outcomes not marked uncertain: %d %v", ambiguous, err)
	}
	var payload []byte
	var provider sql.NullString
	if err = db.QueryRowContext(ctx, `SELECT payload,provider_id FROM outbox`).Scan(&payload, &provider); err != nil || len(payload) != 0 || provider.Valid {
		t.Fatalf("migration invented missing payload/provider identity: %x %+v %v", payload, provider, err)
	}
	var n int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM broadcast_messages`).Scan(&n)
	if n != 1 {
		t.Fatal("migration invented legacy recipient rows", n)
	}
}

func TestDeliveryCadenceJitterAndSlidingCapPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cadence.db")
	store, err := NewContactStore(ctx, testLogger, path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}, {Name: "B", Phone: "5511222222222"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := store.deliveryRows(ctx, id)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	// Pin creation eligibility to the same deterministic cadence clock.
	if _, err = store.db.ExecContext(ctx, `UPDATE outbox SET next_attempt_at=? WHERE broadcast_id=?`, now.UTC().Format("2006-01-02 15:04:05"), id); err != nil {
		t.Fatal(err)
	}
	delay := computeBroadcastDelay(time.Second, 0.3, func() float64 { return 1 })
	ok, wait, err := store.claimDeliveryAt(ctx, rows[0], now, delay, 2)
	if err != nil || !ok || wait != 0 {
		t.Fatal("first admission", ok, wait, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewContactStore(ctx, testLogger, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	ok, wait, err = store.claimDeliveryAt(ctx, rows[1], now.Add(time.Second), delay, 2)
	if err != nil || ok || wait != 300*time.Millisecond {
		t.Fatalf("persisted jitter/cadence ignored: %v %v %v", ok, wait, err)
	}
	ok, wait, err = store.claimDeliveryAt(ctx, rows[1], now.Add(2*time.Second), delay, 1)
	if err != nil || ok || wait != time.Hour-2*time.Second {
		t.Fatalf("persisted account cap reset: %v %v %v", ok, wait, err)
	}
	ok, wait, err = store.claimDeliveryAt(ctx, rows[1], now.Add(time.Hour), delay, 1)
	if err != nil || !ok || wait != 0 {
		t.Fatalf("sliding window never reopened: %v %v %v", ok, wait, err)
	}
}

func TestDeliveryStoreUsesFullySynchronousCommits(t *testing.T) {
	store := newTestStore(t)
	var mode int
	if err := store.db.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Fatalf("durable intent commits require FULL synchronous mode, got %d", mode)
	}
}

func TestDeliveryLegacyUnqueuedFailuresRequireReview(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy-gap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_initial.sql", "0002_broadcast_window.sql", "0003_outbox.sql", "0004_outbox_message_fk.sql"} {
		if err = applyMigration(ctx, db, name); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.ExecContext(ctx, `INSERT INTO broadcasts(id,group_name,message,total,ok_count,fail_count) VALUES(1,'legacy gap','hi {nome}',1,0,1);
 INSERT INTO broadcast_messages(broadcast_id,contact_name,phone,status,error) VALUES(1,'A','5511111111111','failed','old timeout or enqueue gap')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = runMigrations(ctx, testLogger, db); err != nil {
		t.Fatal(err)
	}
	store := &ContactStore{db: db, logger: testLogger}
	msgs, _ := store.GetBroadcastMessages(ctx, 1)
	hist, _ := store.ListBroadcasts(ctx, 10, 0)
	if len(msgs) != 1 || msgs[0].Status != "uncertain" || len(hist) != 1 || hist[0].Status != "uncertain" || hist[0].Uncertain != 1 {
		t.Fatalf("legacy enqueue gap mislabeled definitive failure: msgs=%+v hist=%+v", msgs, hist)
	}
	var n int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&n)
	if n != 0 {
		t.Fatal("migration invented missing rendered recipient intent")
	}
}
