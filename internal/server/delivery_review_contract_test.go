package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDeliveryReviewDeclaresMissingCapturedPayloadAndLink(t *testing.T) {
	app := newTestApp(t)
	id, err := app.contacts.CreateBroadcast(ctx, "synthetic", "hello", 1, 0, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = app.contacts.EnqueueOutbox(ctx, OutboxRow{BroadcastID: id, ContactName: "Synthetic", Phone: "5511111111111", Rendered: "legacy"}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/delivery/review", nil)
	reply := httptest.NewRecorder()
	app.handleDeliveryReview(reply, request)
	var rows []map[string]any
	if err = json.Unmarshal(reply.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0]["noPayload"] != true || rows[0]["noMessageRowId"] != true {
		t.Fatalf("unsafe replay flags absent: %v", rows[0])
	}
	for _, private := range []string{"payload", "mediaKey", "rendered", "mediaData"} {
		if _, ok := rows[0][private]; ok {
			t.Fatalf("private field serialized: %s", private)
		}
	}
}
