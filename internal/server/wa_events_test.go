// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// handleWAEvent dispatches WhatsApp client events. We can drive it directly
// with a synthetic *events.Receipt and observe the side effect on the store
// without needing a real WhatsApp connection.

func TestHandleWAEventReceiptDelivered(t *testing.T) {
	app := newTestApp(t)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "m", 1, 0, 0, "", "")
	_, _ = app.contacts.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "wid-x", "sent", "")

	// Subscribe to the SSE channel so we can verify the receipt fan-out.
	ch := app.subscribe()
	defer app.unsubscribe(ch)

	app.handleWAEvent(&events.Receipt{
		MessageIDs: []types.MessageID{"wid-x"},
		Type:       events.ReceiptTypeDelivered,
	})

	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 1 || msgs[0].Status != "delivered" {
		t.Errorf("status not promoted: %+v", msgs)
	}

	select {
	case evt := <-ch:
		if evt.Type != "receipt" {
			t.Errorf("expected receipt event, got %q", evt.Type)
		}
	case <-time.After(1 * time.Second):
		t.Errorf("no receipt event emitted")
	}
}

func TestHandleWAEventReceiptRead(t *testing.T) {
	app := newTestApp(t)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "m", 1, 0, 0, "", "")
	_, _ = app.contacts.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "wid-r", "sent", "")

	app.handleWAEvent(&events.Receipt{
		MessageIDs: []types.MessageID{"wid-r"},
		Type:       events.ReceiptTypeRead,
	})

	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if msgs[0].Status != "read" {
		t.Errorf("read not applied: %+v", msgs[0])
	}
}

func TestHandleWAEventReceiptShutdownCancelled(t *testing.T) {
	app := newTestApp(t)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "m", 1, 0, 0, "", "")
	_, _ = app.contacts.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "wid-shutdown", "sent", "")

	// Cancel the shutdown context before the receipt arrives: the inner
	// WithTimeout(app.shutdownCtx, 2s) inherits the cancellation, so
	// UpdateMessageStatus errors out before touching the row and the SSE
	// receipt fan-out is skipped. This is the path SIGTERM produces: a
	// late whatsmeow receipt must not race the DB close.
	app.shutdownStop()

	ch := app.subscribe()
	defer app.unsubscribe(ch)

	app.handleWAEvent(&events.Receipt{
		MessageIDs: []types.MessageID{"wid-shutdown"},
		Type:       events.ReceiptTypeDelivered,
	})

	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 1 || msgs[0].Status != "sent" {
		t.Errorf("status changed after shutdown: %+v", msgs)
	}

	select {
	case evt := <-ch:
		t.Errorf("no fan-out expected after shutdown, got %q", evt.Type)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestHandleWAEventReceiptUnknownTypeIgnored(t *testing.T) {
	app := newTestApp(t)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "m", 1, 0, 0, "", "")
	_, _ = app.contacts.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "wid-p", "sent", "")

	// "played" maps to nothing in handleWAEvent; status must stay "sent".
	app.handleWAEvent(&events.Receipt{
		MessageIDs: []types.MessageID{"wid-p"},
		Type:       events.ReceiptTypePlayed,
	})
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if msgs[0].Status != "sent" {
		t.Errorf("status changed unexpectedly: %+v", msgs[0])
	}
}

func TestHandleWAEventConnectedFansOut(t *testing.T) {
	app := newTestApp(t)
	ch := app.subscribe()
	defer app.unsubscribe(ch)
	app.handleWAEvent(&events.Connected{})
	select {
	case evt := <-ch:
		if evt.Type != "connected" {
			t.Errorf("expected connected, got %q", evt.Type)
		}
	case <-time.After(1 * time.Second):
		t.Errorf("no event emitted")
	}
}

func TestHandleWAEventDisconnectedFiresCancelAndEvent(t *testing.T) {
	app := newTestApp(t)
	ch := app.subscribe()
	defer app.unsubscribe(ch)
	// Set up a fake in-flight broadcast so cancelBroadcast has something to do.
	var cancelled bool
	var mu sync.Mutex
	var fakeCancel context.CancelFunc = func() {
		mu.Lock()
		cancelled = true
		mu.Unlock()
	}
	app.broadcaster.cancel.Store(&fakeCancel)

	app.handleWAEvent(&events.Disconnected{})
	mu.Lock()
	got := cancelled
	mu.Unlock()
	if !got {
		t.Errorf("expected cancelBroadcast to fire on Disconnected")
	}
	select {
	case evt := <-ch:
		if evt.Type != "disconnected" {
			t.Errorf("expected disconnected event, got %q", evt.Type)
		}
	case <-time.After(1 * time.Second):
		t.Errorf("no event")
	}
}

func TestHandleWAEventLoggedOutFiresCancelAndEvent(t *testing.T) {
	app := newTestApp(t)
	ch := app.subscribe()
	defer app.unsubscribe(ch)

	var cancelled bool
	var mu sync.Mutex
	var fakeCancel context.CancelFunc = func() {
		mu.Lock()
		cancelled = true
		mu.Unlock()
	}
	app.broadcaster.cancel.Store(&fakeCancel)

	app.handleWAEvent(&events.LoggedOut{OnConnect: false})
	// The handler spawns resetClient in a goroutine; we don't assert on that.
	mu.Lock()
	got := cancelled
	mu.Unlock()
	if !got {
		t.Errorf("cancelBroadcast not invoked on LoggedOut")
	}
	select {
	case evt := <-ch:
		if evt.Type != "logged_out" {
			t.Errorf("expected logged_out, got %q", evt.Type)
		}
	case <-time.After(1 * time.Second):
		t.Errorf("no event")
	}
}

// --- cancelBroadcast direct ---

func TestCancelBroadcastNoOpWhenIdle(t *testing.T) {
	app := newTestApp(t)
	app.cancelBroadcast("test") // must not panic when no broadcast running
}

func TestCancelBroadcastInvokesStoredCancel(t *testing.T) {
	app := newTestApp(t)
	called := false
	var fakeCancel context.CancelFunc = func() { called = true }
	app.broadcaster.cancel.Store(&fakeCancel)
	app.cancelBroadcast("test")
	if !called {
		t.Errorf("stored cancel was not invoked")
	}
}
