// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Reliability-path coverage: recipient resolution, abort-row persistence
// and the failed-send → outbox → retry → history-promotion pipeline.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- resolveRecipients ---

func TestResolveRecipientsAdhoc(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.contacts.AddContact(ctx, "Ana", "5511111111111", nil); err != nil {
		t.Fatalf("AddContact: %v", err)
	}
	if _, err := app.contacts.AddContact(ctx, "Bob", "5511222222222", nil); err != nil {
		t.Fatalf("AddContact: %v", err)
	}

	contacts, label, err := app.resolveRecipients(ctx, true, BroadcastRequest{
		Phones: []string{"5511111111111", "5511222222222"},
	})
	if err != nil {
		t.Fatalf("resolveRecipients: %v", err)
	}
	if len(contacts) != 2 {
		t.Errorf("expected 2 contacts, got %d", len(contacts))
	}
	if label != "Seleção manual (2)" {
		t.Errorf("unexpected label: %q", label)
	}
}

func TestResolveRecipientsAdhocNoMatch(t *testing.T) {
	app := newTestApp(t)
	_, _, err := app.resolveRecipients(ctx, true, BroadcastRequest{
		Phones: []string{"5599999999999"},
	})
	le, ok := asLocalized(err)
	if !ok || le.LocalizedKey() != "err_no_contacts" {
		t.Errorf("expected err_no_contacts, got %v", err)
	}
}

func TestResolveRecipientsGroup(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.contacts.AddContact(ctx, "Ana", "5511111111111", nil); err != nil {
		t.Fatalf("AddContact: %v", err)
	}
	if _, err := app.contacts.AddGroup(ctx, "Friends", []string{"5511111111111"}); err != nil {
		t.Fatalf("AddGroup: %v", err)
	}

	contacts, label, err := app.resolveRecipients(ctx, false, BroadcastRequest{GroupID: "Friends"})
	if err != nil {
		t.Fatalf("resolveRecipients: %v", err)
	}
	if len(contacts) != 1 || contacts[0].Phone != "5511111111111" {
		t.Errorf("unexpected contacts: %+v", contacts)
	}
	if label != "Friends" {
		t.Errorf("expected group name as label, got %q", label)
	}
}

func TestResolveRecipientsGroupEmpty(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.contacts.AddGroup(ctx, "Empty", nil); err != nil {
		t.Fatalf("AddGroup: %v", err)
	}
	_, _, err := app.resolveRecipients(ctx, false, BroadcastRequest{GroupID: "Empty"})
	le, ok := asLocalized(err)
	if !ok || le.LocalizedKey() != "err_group_empty" {
		t.Errorf("expected err_group_empty, got %v", err)
	}
}

func TestResolveRecipientsGroupMissing(t *testing.T) {
	app := newTestApp(t)
	_, _, err := app.resolveRecipients(ctx, false, BroadcastRequest{GroupID: "nope"})
	if err == nil {
		t.Errorf("expected error for missing group, got nil")
	}
}

// --- abort persistence (regression for the cancelled-ctx abort bug) ---

func TestRunBroadcastAbortPersistsAbortedRows(t *testing.T) {
	wa := &fakeWA{connected: true}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 3, 0, 0, "", "")
	contacts := []Contact{
		{Name: "A", Phone: "5511111111111"},
		{Name: "B", Phone: "5511222222222"},
		{Name: "C", Phone: "5511333333333"},
	}

	// Cancel before launch — the first loop iteration must take the abort
	// path with the ctx already dead, exactly the shutdown/cancel shape.
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := runBroadcastForTest(t, app, runCtx, id, "G", "hi", contacts, nil, nil, "", "", "")
	defer app.unsubscribe(ch)

	done := drainUntilDone(t, ch, 2*time.Second)
	if !done["aborted"].(bool) || done["abortedRemaining"].(int) != 3 {
		t.Fatalf("expected fully aborted broadcast, got %+v", done)
	}

	// The history must record the aborted remainder even though the
	// broadcast ctx was cancelled when the rows were written.
	msgs, err := app.contacts.GetBroadcastMessages(ctx, id)
	if err != nil {
		t.Fatalf("GetBroadcastMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 aborted rows persisted, got %d", len(msgs))
	}
	for _, m := range msgs {
		if m.Status != "failed" || m.Error != "aborted" {
			t.Errorf("expected failed/aborted row, got %+v", m)
		}
	}

	// Final counters must land despite the cancelled ctx.
	app.broadcaster.wg.Wait()
	hist, err := app.contacts.ListBroadcasts(ctx, 10, 0)
	if err != nil || len(hist) == 0 {
		t.Fatalf("ListBroadcasts: %v (%d rows)", err, len(hist))
	}
	if hist[0].Fail != 3 {
		t.Errorf("expected fail_count=3 persisted, got %+v", hist[0])
	}
}

// --- outbox pipeline: failed send → enqueue → retry → promotion ---

func TestOutboxRetrySucceedsEndToEnd(t *testing.T) {
	wa := &fakeWA{
		connected:      true,
		sendErrAtIndex: map[int]error{0: errBoom},
	}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 1, 0, 0, "", "")
	contacts := []Contact{{Name: "Ana", Phone: "5511111111111"}}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := runBroadcastForTest(t, app, runCtx, id, "G", "hi {nome}", contacts, nil, nil, "", "", "")
	defer app.unsubscribe(ch)
	done := drainUntilDone(t, ch, 2*time.Second)
	if done["fail"].(int) != 1 {
		t.Fatalf("expected 1 failed send, got %+v", done)
	}
	app.broadcaster.wg.Wait()

	// The failed send must be sitting in the outbox with the rendered body.
	due, err := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("FetchDueOutbox: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 outbox row, got %d", len(due))
	}
	row := due[0]
	if row.Phone != "5511111111111" || row.Rendered != "hi Ana" {
		t.Errorf("unexpected outbox row: %+v", row)
	}
	if row.MessageRowID == 0 {
		t.Errorf("expected outbox row to reference its broadcast_messages id, got 0")
	}

	// Retry through the worker path; the fake now succeeds (index 1).
	w := &OutboxWorker{app: app, logger: app.logger}
	w.attempt(ctx, app.WA(), row)

	// Row gone from the outbox…
	due, err = app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("FetchDueOutbox after retry: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected empty outbox after retry, got %d rows", len(due))
	}

	// …and the history row promoted from failed to sent with the live id.
	msgs, err := app.contacts.GetBroadcastMessages(ctx, id)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("GetBroadcastMessages: %v (%d rows)", err, len(msgs))
	}
	if msgs[0].Status != "sent" || msgs[0].MessageID != "fake-msg-id" || msgs[0].Error != "" {
		t.Errorf("expected promoted sent row, got %+v", msgs[0])
	}
}

func TestOutboxRetryFailureReschedulesThenDies(t *testing.T) {
	wa := &fakeWA{connected: true, sendErrAll: errBoom}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 1, 0, 0, "", "")

	if err := app.contacts.EnqueueOutbox(ctx, OutboxRow{
		BroadcastID: id, ContactName: "Ana", Phone: "5511111111111", Rendered: "hi",
	}); err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}
	due, err := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Minute))
	if err != nil || len(due) != 1 {
		t.Fatalf("FetchDueOutbox: %v (%d rows)", err, len(due))
	}
	row := due[0]

	// Every attempt fails; outboxMaxAttempts-1 reschedules keep it pending…
	w := &OutboxWorker{app: app, logger: app.logger}
	for i := 0; i < outboxMaxAttempts-1; i++ {
		w.attempt(ctx, app.WA(), row)
	}
	due, err = app.contacts.FetchDueOutbox(ctx, time.Now().Add(48*time.Hour))
	if err != nil || len(due) != 1 {
		t.Fatalf("expected row still pending before max attempts, got %v (%d rows)", err, len(due))
	}
	if due[0].Attempts != outboxMaxAttempts-1 {
		t.Errorf("expected %d attempts recorded, got %d", outboxMaxAttempts-1, due[0].Attempts)
	}

	// …and the final attempt flips it to dead.
	w.attempt(ctx, app.WA(), due[0])
	due, err = app.contacts.FetchDueOutbox(ctx, time.Now().Add(48*time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatalf("expected no pending rows after death, got %v (%d rows)", err, len(due))
	}
	dead, err := app.contacts.ListDeadOutbox(ctx)
	if err != nil || len(dead) != 1 {
		t.Fatalf("ListDeadOutbox: %v (%d rows)", err, len(dead))
	}
	if dead[0].Attempts != outboxMaxAttempts || dead[0].LastError == "" {
		t.Errorf("unexpected dead row: %+v", dead[0])
	}
}

// TestMarkOutboxSentLegacyFallback covers rows enqueued before migration
// 0004 (MessageRowID == 0): promotion must fall back to the
// (broadcast_id, phone, status='failed') match.
func TestMarkOutboxSentLegacyFallback(t *testing.T) {
	app := newTestApp(t)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 1, 0, 0, "", "")
	if _, err := app.contacts.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "", "failed", "boom"); err != nil {
		t.Fatalf("InsertBroadcastMessage: %v", err)
	}
	// MessageRowID deliberately zero — a pre-migration row.
	if err := app.contacts.EnqueueOutbox(ctx, OutboxRow{
		BroadcastID: id, ContactName: "Ana", Phone: "5511111111111", Rendered: "hi",
	}); err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}
	due, err := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Minute))
	if err != nil || len(due) != 1 {
		t.Fatalf("FetchDueOutbox: %v (%d rows)", err, len(due))
	}
	if due[0].MessageRowID != 0 {
		t.Fatalf("expected legacy row without message id, got %d", due[0].MessageRowID)
	}

	if err := app.contacts.MarkOutboxSent(ctx, due[0], "legacy-mid"); err != nil {
		t.Fatalf("MarkOutboxSent: %v", err)
	}
	msgs, err := app.contacts.GetBroadcastMessages(ctx, id)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("GetBroadcastMessages: %v (%d rows)", err, len(msgs))
	}
	if msgs[0].Status != "sent" || msgs[0].MessageID != "legacy-mid" {
		t.Errorf("expected legacy row promoted via phone match, got %+v", msgs[0])
	}
}

func TestMarkOutboxSentMissingRow(t *testing.T) {
	app := newTestApp(t)
	err := app.contacts.MarkOutboxSent(ctx, OutboxRow{ID: 99999, BroadcastID: 1, Phone: "5511111111111"}, "mid")
	if !errors.Is(err, ErrOutboxMissing) {
		t.Errorf("expected ErrOutboxMissing, got %v", err)
	}
}

// TestMarkOutboxSentPromotesExactRow seeds two failed rows for the same
// phone in one broadcast — the ambiguity the broadcast_message_id FK
// exists to resolve — and asserts the retry promotes the referenced row,
// not the newest phone match.
func TestMarkOutboxSentPromotesExactRow(t *testing.T) {
	app := newTestApp(t)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 2, 0, 0, "", "")

	first, err := app.contacts.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "", "failed", "boom-1")
	if err != nil {
		t.Fatalf("InsertBroadcastMessage: %v", err)
	}
	if _, err := app.contacts.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "", "failed", "boom-2"); err != nil {
		t.Fatalf("InsertBroadcastMessage: %v", err)
	}

	if err := app.contacts.EnqueueOutbox(ctx, OutboxRow{
		BroadcastID: id, MessageRowID: first, ContactName: "Ana",
		Phone: "5511111111111", Rendered: "hi",
	}); err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}
	due, err := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Minute))
	if err != nil || len(due) != 1 {
		t.Fatalf("FetchDueOutbox: %v (%d rows)", err, len(due))
	}

	if err := app.contacts.MarkOutboxSent(ctx, due[0], "retry-mid"); err != nil {
		t.Fatalf("MarkOutboxSent: %v", err)
	}

	msgs, err := app.contacts.GetBroadcastMessages(ctx, id)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("GetBroadcastMessages: %v (%d rows)", err, len(msgs))
	}
	byID := map[int64]BroadcastMessage{}
	for _, m := range msgs {
		byID[m.ID] = m
	}
	if got := byID[first]; got.Status != "sent" || got.MessageID != "retry-mid" {
		t.Errorf("expected first row promoted, got %+v", got)
	}
	for rid, m := range byID {
		if rid != first && m.Status != "failed" {
			t.Errorf("unrelated row must stay failed, got %+v", m)
		}
	}
}
