// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"strings"
	"testing"
	"time"
)

// --- CreateBroadcast / ListBroadcasts cursor ---

func TestListBroadcastsCursor(t *testing.T) {
	store := newTestStore(t)
	ids := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		id, err := store.CreateBroadcast(ctx, "Group", "msg", 1, 0, 0, "", "")
		if err != nil {
			t.Fatalf("CreateBroadcast: %v", err)
		}
		ids = append(ids, id)
	}

	// First page: newest first.
	page, err := store.ListBroadcasts(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListBroadcasts: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("expected 2 items, got %d", len(page))
	}
	if page[0].ID != ids[4] || page[1].ID != ids[3] {
		t.Errorf("page order wrong: %+v", page)
	}

	// Cursor: items older than ids[3].
	next, err := store.ListBroadcasts(ctx, 10, ids[3])
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if len(next) != 3 {
		t.Fatalf("expected 3 older items, got %d", len(next))
	}
	if next[0].ID != ids[2] {
		t.Errorf("cursor first = %d, want %d", next[0].ID, ids[2])
	}
}

func TestListBroadcastsLimitClamp(t *testing.T) {
	store := newTestStore(t)
	got, err := store.ListBroadcasts(ctx, -1, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil {
		t.Error("expected empty slice, not nil")
	}
	if _, err := store.ListBroadcasts(ctx, 99999, 0); err != nil {
		t.Errorf("high limit should clamp silently: %v", err)
	}
}

// --- InsertBroadcastMessage + GetBroadcastMessages ---

func TestBroadcastMessagesRoundtrip(t *testing.T) {
	store := newTestStore(t)
	id, err := store.CreateBroadcast(ctx, "Group", "msg", 2, 0, 0, "", "")
	if err != nil {
		t.Fatalf("CreateBroadcast: %v", err)
	}
	if _, err := store.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "wid-1", "sent", ""); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := store.InsertBroadcastMessage(ctx, id, "Bruno", "5511222222222", "", "failed", "network"); err != nil {
		t.Fatalf("Insert fail: %v", err)
	}

	msgs, err := store.GetBroadcastMessages(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2, got %d", len(msgs))
	}
	if msgs[0].ContactName != "Ana" || msgs[0].Status != "sent" || msgs[0].MessageID != "wid-1" {
		t.Errorf("msg[0] wrong: %+v", msgs[0])
	}
	if msgs[1].Status != "failed" || msgs[1].Error != "network" {
		t.Errorf("msg[1] wrong: %+v", msgs[1])
	}
}

// --- UpdateBroadcastCounts ---

func TestUpdateBroadcastCounts(t *testing.T) {
	store := newTestStore(t)
	id, _ := store.CreateBroadcast(ctx, "Group", "msg", 3, 0, 0, "", "")
	if err := store.UpdateBroadcastCounts(ctx, id, 2, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	items, _ := store.ListBroadcasts(ctx, 1, 0)
	if len(items) == 0 || items[0].OK != 2 || items[0].Fail != 1 {
		t.Errorf("counts not persisted: %+v", items)
	}
}

// --- UpdateMessageStatus ---

func TestUpdateMessageStatusDelivered(t *testing.T) {
	store := newTestStore(t)
	id, _ := store.CreateBroadcast(ctx, "Group", "msg", 1, 0, 0, "", "")
	_, _ = store.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "wid-1", "sent", "")

	if err := store.UpdateMessageStatus(ctx, "wid-1", "delivered"); err != nil {
		t.Fatalf("delivered: %v", err)
	}
	msgs, _ := store.GetBroadcastMessages(ctx, id)
	if msgs[0].Status != "delivered" {
		t.Errorf("status = %q, want delivered", msgs[0].Status)
	}
	if msgs[0].DeliveredAt == "" {
		t.Errorf("DeliveredAt missing after delivered receipt")
	}
}

func TestUpdateMessageStatusReadPromotes(t *testing.T) {
	store := newTestStore(t)
	id, _ := store.CreateBroadcast(ctx, "Group", "msg", 1, 0, 0, "", "")
	_, _ = store.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "wid-1", "sent", "")

	if err := store.UpdateMessageStatus(ctx, "wid-1", "read"); err != nil {
		t.Fatalf("read: %v", err)
	}
	msgs, _ := store.GetBroadcastMessages(ctx, id)
	if msgs[0].Status != "read" || msgs[0].ReadAt == "" {
		t.Errorf("read not applied: %+v", msgs[0])
	}
	// Read implicitly back-fills delivered_at when receipt arrived out of order.
	if msgs[0].DeliveredAt == "" {
		t.Errorf("DeliveredAt should be back-filled when read arrives first")
	}
}

func TestUpdateMessageStatusDoesNotDowngrade(t *testing.T) {
	store := newTestStore(t)
	id, _ := store.CreateBroadcast(ctx, "Group", "msg", 1, 0, 0, "", "")
	_, _ = store.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "wid-1", "sent", "")
	_ = store.UpdateMessageStatus(ctx, "wid-1", "read")
	// A late "delivered" receipt must not regress the row to "delivered".
	if err := store.UpdateMessageStatus(ctx, "wid-1", "delivered"); err != nil {
		t.Fatalf("late delivered: %v", err)
	}
	msgs, _ := store.GetBroadcastMessages(ctx, id)
	if msgs[0].Status != "read" {
		t.Errorf("status downgraded to %q after read", msgs[0].Status)
	}
}

func TestUpdateMessageStatusInvalid(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateMessageStatus(ctx, "wid-1", "garbage")
	if err == nil {
		t.Fatal("expected error for invalid status")
	}
	ek, ok := err.(*errKey)
	if !ok || ek.key != "err_invalid_status" {
		t.Errorf("expected err_invalid_status, got %v", err)
	}
}

// --- GetBroadcastStats ---

func TestGetBroadcastStatsAggregates(t *testing.T) {
	store := newTestStore(t)
	a, _ := store.CreateBroadcast(ctx, "G", "m", 3, 0, 0, "", "")
	_ = store.UpdateBroadcastCounts(ctx, a, 2, 1)
	_, _ = store.InsertBroadcastMessage(ctx, a, "x", "5511111111111", "wa-a", "sent", "")
	_, _ = store.InsertBroadcastMessage(ctx, a, "y", "5511222222222", "wa-b", "sent", "")
	_, _ = store.InsertBroadcastMessage(ctx, a, "z", "5511333333333", "wa-c", "failed", "boom")
	_ = store.UpdateMessageStatus(ctx, "wa-a", "delivered")
	_ = store.UpdateMessageStatus(ctx, "wa-b", "read")

	stats, err := store.GetBroadcastStats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats["broadcasts"] != 1 {
		t.Errorf("broadcasts = %d", stats["broadcasts"])
	}
	if stats["total"] != 3 {
		t.Errorf("total = %d", stats["total"])
	}
	if stats["ok"] != 2 || stats["fail"] != 1 {
		t.Errorf("ok/fail = %d/%d", stats["ok"], stats["fail"])
	}
	if stats["delivered"] != 2 {
		t.Errorf("delivered = %d (want 2 — one delivered + one read)", stats["delivered"])
	}
	if stats["read"] != 1 {
		t.Errorf("read = %d", stats["read"])
	}
}

func TestGetBroadcastStatsZero(t *testing.T) {
	store := newTestStore(t)
	stats, err := store.GetBroadcastStats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	for _, k := range []string{"broadcasts", "total", "ok", "fail", "delivered", "read"} {
		if stats[k] != 0 {
			t.Errorf("%s should be 0 on empty store, got %d", k, stats[k])
		}
	}
}

// --- LastBroadcastTime ---

func TestLastBroadcastTimeEmpty(t *testing.T) {
	store := newTestStore(t)
	got, err := store.LastBroadcastTime(ctx)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("expected zero time on empty store, got %v", got)
	}
}

func TestLastBroadcastTimeReturnsLatest(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateBroadcast(ctx, "Group", "msg", 1, 0, 0, "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.LastBroadcastTime(ctx)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.IsZero() {
		t.Errorf("expected non-zero timestamp")
	}
	// Should be close to "now" (SQLite stores CURRENT_TIMESTAMP in UTC).
	if delta := time.Since(got); delta > time.Minute || delta < -time.Minute {
		t.Errorf("timestamp far from now: %v", got)
	}
}

// --- Media metadata roundtrip ---

func TestBroadcastMediaFieldsRoundtrip(t *testing.T) {
	store := newTestStore(t)
	id, err := store.CreateBroadcast(ctx, "Group", "msg", 1, 0, 0, "video", "trailer.mp4")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	items, _ := store.ListBroadcasts(ctx, 10, 0)
	var got *BroadcastSummary
	for i := range items {
		if items[i].ID == id {
			got = &items[i]
			break
		}
	}
	if got == nil {
		t.Fatal("broadcast not in list")
	}
	if got.MediaType != "video" || got.MediaName != "trailer.mp4" {
		t.Errorf("media fields wrong: %q/%q", got.MediaType, got.MediaName)
	}
}

// --- nullIfEmpty edge ---

func TestNullIfEmpty(t *testing.T) {
	if got := nullIfEmpty(""); got != nil {
		t.Errorf("empty should be nil, got %v", got)
	}
	if got := nullIfEmpty("x"); got != "x" {
		t.Errorf("non-empty wrong: %v", got)
	}
}

// --- Defensive check that ContactStore.UpdateMessageStatus tolerates unknown
// message_id (no row updated). Should not error since SQL UPDATE with 0
// affected rows is a normal result. ---

func TestUpdateMessageStatusUnknownIDIsNoOp(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpdateMessageStatus(ctx, "nonexistent", "delivered"); err != nil {
		t.Errorf("unknown message id should be silent, got %v", err)
	}
}

// Sanity: store.LastBroadcastTime should parse SQLite "YYYY-MM-DD HH:MM:SS"
// format reliably across timezones (it stores in UTC).
func TestLastBroadcastTimeFormat(t *testing.T) {
	store := newTestStore(t)
	_, _ = store.CreateBroadcast(ctx, "Group", "msg", 1, 0, 0, "", "")
	got, err := store.LastBroadcastTime(ctx)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// String form must contain the year, ruling out a malformed parse.
	if !strings.Contains(got.Format("2006-01-02"), "20") {
		t.Errorf("year missing from parsed timestamp: %v", got)
	}
}
