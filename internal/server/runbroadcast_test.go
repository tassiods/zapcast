// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

// runBroadcastForTest fabricates the StartHandle that runBroadcast expects
// without going through Broadcaster.Start (which would also enforce
// cooldown). Each helper subscribes to SSE events so callers can assert
// the broadcast_done envelope inline.
func runBroadcastForTest(t *testing.T, app *App, ctx context.Context, broadcastID int64, label, message string, contacts []Contact, vars map[string]string, mediaData []byte, mediaMime, mediaName, mediaKind string) chan Event {
	t.Helper()
	ch := app.subscribe()
	app.broadcaster.mu.Lock()
	app.broadcaster.wg.Add(1)
	derived, cancel := context.WithCancel(ctx)
	app.broadcaster.cancel.Store(&cancel)
	handle := &StartHandle{Ctx: derived, Cancel: cancel, b: app.broadcaster}
	media := mediaPayload{data: mediaData, mime: mediaMime, name: mediaName, kind: mediaKind}
	go app.runBroadcast(handle, broadcastID, label, message, contacts, vars, media)
	return ch
}

// drainUntilDone returns the broadcast_done event payload (or fails the
// test after timeout). Returned events of other types are discarded.
//
// The Data field is a typed struct in production; tests prefer index access
// (`done["ok"]`) so we JSON round-trip into a map. JSON keeps `int` as
// float64 — the helper converts the numeric fields back so the existing
// type assertions in callers (`.(int)`, `.(bool)`) keep working.
func drainUntilDone(t *testing.T, ch <-chan Event, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case evt := <-ch:
			if evt.Type == "broadcast_done" {
				return decodeDoneEvent(t, evt)
			}
		case <-deadline:
			t.Fatalf("did not receive broadcast_done within %s", timeout)
			return nil
		}
	}
}

func decodeDoneEvent(t *testing.T, evt Event) map[string]any {
	t.Helper()
	raw, err := json.Marshal(evt.Data)
	if err != nil {
		t.Fatalf("marshal broadcast_done data: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal broadcast_done data: %v", err)
	}
	// json.Number defaults to float64 — coerce integer-shaped fields back so
	// callers can `.(int)` without rewriting every assertion.
	for _, k := range []string{"total", "ok", "fail", "abortedRemaining"} {
		if v, ok := generic[k].(float64); ok {
			generic[k] = int(v)
		}
	}
	return generic
}

func TestRunBroadcastTextOnlyHappy(t *testing.T) {
	wa := &fakeWA{connected: true}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "Friends", "hi", 2, 0, 0, "", "")
	contacts := []Contact{
		{Name: "Ana", Phone: "5511111111111"},
		{Name: "Bob", Phone: "5511222222222"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := runBroadcastForTest(t, app, ctx, id, "Friends", "hi {nome}", contacts, nil, nil, "", "", "")
	defer app.unsubscribe(ch)

	done := drainUntilDone(t, ch, 2*time.Second)
	if done["ok"].(int) != 2 || done["fail"].(int) != 0 {
		t.Errorf("counts wrong: %+v", done)
	}
	if len(wa.sendCalls) != 2 {
		t.Errorf("expected 2 SendMessage calls, got %d", len(wa.sendCalls))
	}
	// Template substitution must have produced personalised text.
	first := wa.sendCalls[0].Msg.GetConversation()
	if !strings.Contains(first, "Ana") {
		t.Errorf("template not rendered for first recipient: %q", first)
	}
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 2 || msgs[0].Status != "sent" || msgs[1].Status != "sent" {
		t.Errorf("history wrong: %+v", msgs)
	}
	app.broadcaster.wg.Wait()
}

func TestRunBroadcastMediaUploadOK(t *testing.T) {
	wa := &fakeWA{
		connected:  true,
		uploadResp: whatsmeow.UploadResponse{URL: "u", FileLength: 42},
	}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "caption", 1, 0, 0, "image", "pic.jpg")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := runBroadcastForTest(t, app, ctx, id, "G", "hi", []Contact{{Name: "Ana", Phone: "5511111111111"}}, nil, []byte("fake-bytes"), "image/jpeg", "pic.jpg", "image")
	defer app.unsubscribe(ch)
	done := drainUntilDone(t, ch, 2*time.Second)
	if done["ok"].(int) != 1 {
		t.Errorf("ok count wrong: %+v", done)
	}
	if len(wa.sendCalls) != 1 || wa.sendCalls[0].Msg.GetImageMessage() == nil {
		t.Errorf("expected image message, got %+v", wa.sendCalls)
	}
	app.broadcaster.wg.Wait()
}

func TestRunBroadcastUploadFailureMarksAll(t *testing.T) {
	wa := &fakeWA{connected: true, uploadErr: errBoom}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 3, 0, 0, "image", "pic.jpg")
	contacts := []Contact{
		{Name: "A", Phone: "5511111111111"},
		{Name: "B", Phone: "5511222222222"},
		{Name: "C", Phone: "5511333333333"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := runBroadcastForTest(t, app, ctx, id, "G", "hi", contacts, nil, []byte("data"), "image/jpeg", "pic.jpg", "image")
	defer app.unsubscribe(ch)
	done := drainUntilDone(t, ch, 2*time.Second)
	if done["fail"].(int) != 3 || done["ok"].(int) != 0 {
		t.Errorf("expected all-fail counts, got %+v", done)
	}
	if done["error"].(string) != "upload_failed" {
		t.Errorf("expected upload_failed error, got %+v", done)
	}
	if len(wa.sendCalls) != 0 {
		t.Errorf("Upload failure must skip SendMessage; got %d calls", len(wa.sendCalls))
	}
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 3 {
		t.Errorf("expected 3 failed rows, got %d", len(msgs))
	}
	app.broadcaster.wg.Wait()
}

func TestRunBroadcastSendFailMidway(t *testing.T) {
	wa := &fakeWA{
		connected:      true,
		sendErrAtIndex: map[int]error{1: &RejectedDeliveryError{Err: errBoom}},
	}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 3, 0, 0, "", "")
	contacts := []Contact{
		{Name: "A", Phone: "5511111111111"},
		{Name: "B", Phone: "5511222222222"},
		{Name: "C", Phone: "5511333333333"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := runBroadcastForTest(t, app, ctx, id, "G", "hi", contacts, nil, nil, "", "", "")
	defer app.unsubscribe(ch)
	done := drainUntilDone(t, ch, 2*time.Second)
	if done["ok"].(int) != 2 || done["fail"].(int) != 1 {
		t.Errorf("expected 2 ok / 1 fail, got %+v", done)
	}
	app.broadcaster.wg.Wait()
}

func TestRunBroadcastAbortViaContext(t *testing.T) {
	// Block SendMessage forever via sendErrAll behaviour swap: use a slow
	// fake that blocks until released. Simpler approach — start with 2
	// contacts and cancel context before the loop reaches the second one.
	wa := &fakeWA{connected: true}
	app := newTestAppWithWA(t, wa)
	app.broadcaster.delay = 50 * time.Millisecond // give us a window to cancel
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 3, 0, 0, "", "")
	contacts := []Contact{
		{Name: "A", Phone: "5511111111111"},
		{Name: "B", Phone: "5511222222222"},
		{Name: "C", Phone: "5511333333333"},
	}
	ctx, cancel := context.WithCancel(context.Background())

	ch := runBroadcastForTest(t, app, ctx, id, "G", "hi", contacts, nil, nil, "", "", "")
	defer app.unsubscribe(ch)

	// Cancel after the first send completes — between iterations.
	go func() {
		// Wait long enough for at least one send to register.
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			wa.mu.Lock()
			n := len(wa.sendCalls)
			wa.mu.Unlock()
			if n >= 1 {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()

	done := drainUntilDone(t, ch, 5*time.Second)
	if !done["aborted"].(bool) {
		t.Errorf("expected aborted=true, got %+v", done)
	}
	if done["abortedRemaining"].(int) < 1 {
		t.Errorf("expected at least 1 aborted remaining, got %+v", done)
	}
	app.broadcaster.wg.Wait()
}

func TestRunBroadcastHourlyCapPauses(t *testing.T) {
	wa := &fakeWA{connected: true}
	app := newTestAppWithWA(t, wa)
	app.broadcaster.delay = 0
	app.broadcaster.jitter = 0
	app.broadcaster.hourlyCap = 1
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "hi", 2, 0, 0, "", "")
	contacts := []Contact{
		{Name: "A", Phone: "5511111111111"},
		{Name: "B", Phone: "5511222222222"},
	}
	ctx, cancel := context.WithCancel(context.Background())

	ch := runBroadcastForTest(t, app, ctx, id, "G", "hi", contacts, nil, nil, "", "", "")
	defer app.unsubscribe(ch)

	// First message sends, then the cap kicks in — wait for broadcast_paused,
	// then cancel so the second iteration short-circuits via ctx.Done.
	pausedCh := make(chan struct{}, 1)
	var once sync.Once
	go func() {
		for evt := range ch {
			if evt.Type == "broadcast_paused" {
				once.Do(func() { close(pausedCh) })
			}
			if evt.Type == "broadcast_done" {
				return
			}
		}
	}()
	select {
	case <-pausedCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("did not observe broadcast_paused")
	}
	cancel()
	app.broadcaster.wg.Wait()
}

// --- computeBroadcastDelay (extracted helper) ---

func TestComputeBroadcastDelayNoJitter(t *testing.T) {
	got := computeBroadcastDelay(500*time.Millisecond, 0, func() float64 { return 0.5 })
	if got != 500*time.Millisecond {
		t.Errorf("got %v, want 500ms", got)
	}
}

func TestComputeBroadcastDelayApplied(t *testing.T) {
	// rng=1.0 → n = jitter; +30% nudge.
	got := computeBroadcastDelay(1000*time.Millisecond, 0.3, func() float64 { return 1 })
	if got != 1300*time.Millisecond {
		t.Errorf("got %v, want 1.3s", got)
	}
}

func TestComputeBroadcastDelayFloor(t *testing.T) {
	// rng=0 → n = -jitter; -90% from 100ms → 10ms, must clamp to 50ms.
	got := computeBroadcastDelay(100*time.Millisecond, 0.9, func() float64 { return 0 })
	if got != 50*time.Millisecond {
		t.Errorf("got %v, want 50ms floor", got)
	}
}
