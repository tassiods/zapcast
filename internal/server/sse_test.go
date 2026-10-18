// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// nonFlushingWriter is a minimal http.ResponseWriter that deliberately does
// NOT implement http.Flusher (unlike httptest.ResponseRecorder, which does).
// Used to exercise the failure branch of setupSSE.
type nonFlushingWriter struct {
	status int
	hdr    http.Header
}

func (w *nonFlushingWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}
func (w *nonFlushingWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *nonFlushingWriter) WriteHeader(code int)        { w.status = code }

func TestSubscribeRegistersChannel(t *testing.T) {
	app := &App{hub: NewSSEHub(testLogger)}
	ch := app.subscribe()
	if ch == nil {
		t.Fatal("subscribe returned nil channel")
	}
	app.hub.mu.RLock()
	_, ok := app.hub.subscribers[ch]
	app.hub.mu.RUnlock()
	if !ok {
		t.Errorf("channel not registered")
	}
}

func TestUnsubscribeRemovesButDoesNotClose(t *testing.T) {
	app := &App{hub: NewSSEHub(testLogger)}
	ch := app.subscribe()
	app.unsubscribe(ch)
	app.hub.mu.RLock()
	_, ok := app.hub.subscribers[ch]
	app.hub.mu.RUnlock()
	if ok {
		t.Errorf("channel still registered after unsubscribe")
	}
	// The channel must NOT be closed: Broadcast snapshots subscriber channels
	// and then sends lock-free, so closing here could race an in-flight send
	// and panic. A non-blocking receive on the still-open, empty channel takes
	// the default branch.
	select {
	case _, open := <-ch:
		if !open {
			t.Errorf("channel must not be closed by unsubscribe")
		} else {
			t.Errorf("unexpected value on drained channel")
		}
	default:
		// expected: open and empty
	}
}

// TestBroadcastAfterUnsubscribeNoPanic exercises the race the no-close design
// prevents: a Broadcast whose snapshot still holds a just-unsubscribed channel
// must not panic (a send-on-closed-channel would).
func TestBroadcastAfterUnsubscribeNoPanic(t *testing.T) {
	app := &App{hub: NewSSEHub(testLogger)}
	ch := app.subscribe()
	app.unsubscribe(ch)
	// Would panic if Unsubscribe had closed ch and Broadcast still sent to it.
	app.broadcastEvent(Event{Type: "test", Data: "x"})
}

func TestUnsubscribeIdempotent(t *testing.T) {
	app := &App{hub: NewSSEHub(testLogger)}
	ch := app.subscribe()
	app.unsubscribe(ch)
	// Second call must be a no-op (delete on an absent key), never a panic.
	app.unsubscribe(ch)
}

func TestBroadcastEventFanout(t *testing.T) {
	app := &App{hub: NewSSEHub(testLogger)}
	a := app.subscribe()
	b := app.subscribe()
	defer app.unsubscribe(a)
	defer app.unsubscribe(b)

	evt := Event{Type: "test", Data: "payload"}
	app.broadcastEvent(evt)

	var wg sync.WaitGroup
	wg.Add(2)
	check := func(ch chan Event, label string) {
		defer wg.Done()
		select {
		case got := <-ch:
			if got.Type != "test" || got.Data.(string) != "payload" {
				t.Errorf("%s received wrong event: %+v", label, got)
			}
		case <-time.After(1 * time.Second):
			t.Errorf("%s did not receive event", label)
		}
	}
	go check(a, "a")
	go check(b, "b")
	wg.Wait()
}

func TestBroadcastEventDoesNotBlockOnSlowSubscriber(t *testing.T) {
	app := &App{hub: NewSSEHub(testLogger)}
	ch := app.subscribe()
	defer app.unsubscribe(ch)

	// Channel buffer is 20. Fill it without draining, then send one more.
	for i := 0; i < 21; i++ {
		app.broadcastEvent(Event{Type: "x"})
	}
	// If broadcastEvent blocked instead of dropping, this test would hang and
	// the deadlock detector would fire. Reaching this line is the pass.
}

func TestSetupSSEFailsWithoutFlusher(t *testing.T) {
	w := &nonFlushingWriter{}
	req := httptest.NewRequest("GET", "/api/events", nil)
	_, ok := setupSSE(w, req)
	if ok {
		t.Errorf("expected setupSSE to fail when ResponseWriter is not a Flusher")
	}
	if w.status != 500 {
		t.Errorf("expected 500, got %d", w.status)
	}
}

// flushableRecorder wraps httptest.ResponseRecorder with a no-op Flush so
// setupSSE accepts it.
type flushableRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushableRecorder) Flush() {}

func TestSetupSSESetsHeaders(t *testing.T) {
	rec := &flushableRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest("GET", "/api/events", nil)
	flusher, ok := setupSSE(rec, req)
	if !ok || flusher == nil {
		t.Fatalf("expected ok with Flusher")
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestMustJSONMarshalsValue(t *testing.T) {
	got := mustJSON(map[string]int{"n": 7})
	if got != `{"n":7}` {
		t.Errorf("mustJSON wrong: %q", got)
	}
}
