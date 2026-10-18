// SPDX-FileCopyrightText: 2026 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeliveryReviewAPIExposesStatesNotPayloadSecrets(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	id, err := app.contacts.createDelivery(ctx, 0, "G", "PRIVATE_PAYLOAD", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := app.contacts.deliveryRows(ctx, id)
	if err = app.contacts.uncertainDelivery(ctx, rows[0], "unknown"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	app.registerRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/delivery/review", nil))
	var body []map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body) != 1 || body[0]["status"] != "uncertain" {
		t.Fatalf("review API missing: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	if strings.Contains(w.Body.String(), "PRIVATE_PAYLOAD") || strings.Contains(w.Body.String(), "mediaKey") || strings.Contains(w.Body.String(), "payload") {
		t.Fatalf("internal payload leaked: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/broadcasts", nil))
	var envelope struct {
		Items []map[string]any `json:"items"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || len(envelope.Items) != 1 || envelope.Items[0]["uncertain"] != float64(1) || envelope.Items[0]["status"] != "uncertain" {
		t.Fatalf("campaign must expose uncertainty: %s err=%v", w.Body.String(), err)
	}
}

func TestDeliveryManualRetryRequiresDuplicateRiskAcknowledgement(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	id, _ := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	rows, _ := app.contacts.deliveryRows(ctx, id)
	if err := app.contacts.uncertainDelivery(ctx, rows[0], "unknown"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	app.registerRoutes(mux)
	path := fmt.Sprintf("/api/delivery/%d/resolve", rows[0].ID)
	for _, tc := range []struct {
		body string
		code int
	}{{`{"action":"retry"}`, 400}, {`{"action":"retry","acknowledgeDuplicateRisk":true}`, 200}} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(tc.body)))
		if w.Code != tc.code {
			t.Fatalf("resolution code=%d want=%d body=%s", w.Code, tc.code, w.Body.String())
		}
	}
	due, _ := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	if len(due) != 1 || due[0].ProviderID != rows[0].ProviderID {
		t.Fatalf("manual retry lost identity: %+v", due)
	}
}

func TestDeliveryManualConfirmationDoesNotCallProvider(t *testing.T) {
	wa := &fakeWA{connected: true}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	rows, _ := app.contacts.deliveryRows(ctx, id)
	if err := app.contacts.uncertainDelivery(ctx, rows[0], "unknown"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	app.registerRoutes(mux)
	path := fmt.Sprintf("/api/delivery/%d/resolve", rows[0].ID)
	for _, tc := range []struct {
		body string
		code int
	}{{`{"action":"confirm_sent","messageId":"confirmed"}`, 400}, {`{"action":"confirm_sent","messageId":"confirmed","providerConfirmed":true}`, 200}} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(tc.body)))
		if w.Code != tc.code {
			t.Fatalf("confirmation code=%d want=%d body=%s", w.Code, tc.code, w.Body.String())
		}
	}
	msgs, _ := app.contacts.GetBroadcastMessages(ctx, id)
	if len(msgs) != 1 || msgs[0].Status != "sent" || msgs[0].MessageID != "confirmed" {
		t.Fatalf("not confirmed: %+v", msgs)
	}
	wa.mu.Lock()
	n := len(wa.sendCalls)
	wa.mu.Unlock()
	if n != 0 {
		t.Fatal("resolution must never call provider")
	}
}

func TestDeliveryTargetedCancelStopsPendingCampaignAfterRestart(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	var ids []int64
	for _, phone := range []string{"5511111111111", "5511222222222"} {
		id, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: phone}}, nil, mediaPayload{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := app.contacts.recoverDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	app.registerRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/api/broadcasts/%d/cancel", ids[0]), nil))
	if w.Code != 200 {
		t.Fatalf("cancel API missing: %d %s", w.Code, w.Body.String())
	}
	due, _ := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	if len(due) != 1 || due[0].BroadcastID != ids[1] {
		t.Fatalf("targeted cancel did not preserve other campaign: %+v", due)
	}
}

type inspectDeliveryResponse struct {
	*httptest.ResponseRecorder
	beforeAccepted func()
}

func (w *inspectDeliveryResponse) WriteHeader(code int) {
	if code == 202 {
		w.beforeAccepted()
	}
	w.ResponseRecorder.WriteHeader(code)
}
func TestDeliveryHTTP202HasDurableRenderedIntentAndMediaSnapshot(t *testing.T) {
	for _, media := range []bool{false, true} {
		t.Run(fmt.Sprint(media), func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
			wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
				select {
				case <-started:
				default:
					close(started)
				}
				select {
				case <-release:
				case <-c.Done():
					return whatsmeow.SendResponse{}, c.Err()
				}
				return whatsmeow.SendResponse{ID: "accepted"}, nil
			}
			client := WAClient(wa)
			if media {
				client = &deliveryUploadWA{WAClient: wa, uploadHook: func(c context.Context, _ []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
					close(started)
					select {
					case <-release:
					case <-c.Done():
						return whatsmeow.UploadResponse{}, c.Err()
					}
					return whatsmeow.UploadResponse{URL: "uploaded"}, nil
				}}
			}
			app := newTestAppWithWA(t, client)
			if _, err := app.contacts.AddContact(ctx, "Ana", "5511111111111", nil); err != nil {
				t.Fatal(err)
			}
			var r *http.Request
			if media {
				var b bytes.Buffer
				mp := multipart.NewWriter(&b)
				_ = mp.WriteField("message", "hi {nome}")
				_ = mp.WriteField("phones", "5511111111111")
				part, err := mp.CreateFormFile("file", "synthetic.jpg")
				if err != nil {
					t.Fatal(err)
				}
				_, _ = part.Write([]byte("synthetic image"))
				_ = mp.Close()
				r = httptest.NewRequest("POST", "/api/broadcast", &b)
				r.Header.Set("Content-Type", mp.FormDataContentType())
			} else {
				r = httptest.NewRequest("POST", "/api/broadcast", strings.NewReader(`{"message":"hi {nome}","phones":["5511111111111"]}`))
			}
			recorder := httptest.NewRecorder()
			w := &inspectDeliveryResponse{ResponseRecorder: recorder, beforeAccepted: func() {
				var rendered, state string
				var payload, raw []byte
				var n int
				err := app.contacts.db.QueryRowContext(ctx, `SELECT m.rendered,m.payload,b.media_data,o.status FROM broadcasts b JOIN broadcast_messages m ON m.broadcast_id=b.id JOIN outbox o ON o.broadcast_message_id=m.id`).Scan(&rendered, &payload, &raw, &state)
				if err != nil || rendered != "hi Ana" {
					t.Errorf("202 preceded durable rendered intent: %s %v", rendered, err)
				}
				_ = app.contacts.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&n)
				if n != 1 {
					t.Error("202 has no durable queue")
				}
				if media && len(raw) == 0 {
					t.Error("202 preceded media snapshot")
				}
				if !media && len(payload) == 0 {
					t.Error("202 preceded RPC payload")
				}
			}}
			app.handleBroadcast(w, r)
			close(release)
			app.broadcaster.wg.Wait()
			if w.Code != 202 {
				t.Fatalf("not accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDeliveryCancelInterruptsWorkerRPCWithoutEnablingRetry(t *testing.T) {
	entered := make(chan struct{})
	finished := make(chan struct{})
	runctx, stop := context.WithCancel(ctx)
	defer stop()
	wa := &deliveryHookWA{WAClient: &fakeWA{connected: true}}
	wa.hook = func(c context.Context, _ types.JID, _ *waProto.Message) (whatsmeow.SendResponse, error) {
		close(entered)
		<-c.Done()
		return whatsmeow.SendResponse{}, c.Err()
	}
	app := newTestAppWithWA(t, wa)
	id, _ := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{})
	rows, _ := app.contacts.deliveryRows(ctx, id)
	worker := &OutboxWorker{app: app, logger: app.logger}
	go func() { defer close(finished); worker.attempt(runctx, wa, rows[0]) }()
	<-entered
	mux := http.NewServeMux()
	app.registerRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/api/broadcasts/%d/cancel", id), nil))
	select {
	case <-finished:
	case <-time.After(500 * time.Millisecond):
		stop()
		<-finished
		t.Fatal("durable cancellation did not cancel the worker RPC context")
	}
	due, _ := app.contacts.FetchDueOutbox(ctx, time.Now().Add(time.Hour))
	if w.Code != 200 || len(due) != 0 {
		t.Fatalf("cancelled worker became eligible: code=%d rows=%+v", w.Code, due)
	}
}
