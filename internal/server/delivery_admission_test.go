// SPDX-FileCopyrightText: 2026 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"go.mau.fi/whatsmeow"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type unreadBody struct{ reads int }

func (b *unreadBody) Read(p []byte) (int, error) { b.reads++; return 0, io.EOF }
func (b *unreadBody) Close() error               { return nil }
func TestDeliveryAdmissionBeforeMultipartRead(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	h, err := app.broadcaster.Start(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Done()
	b := &unreadBody{}
	r := httptest.NewRequest(http.MethodPost, "/api/broadcast", b)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	w := httptest.NewRecorder()
	app.handleBroadcast(w, r)
	if w.Code != http.StatusTooManyRequests || b.reads != 0 {
		t.Fatalf("busy admission must reject without reading: code=%d reads=%d", w.Code, b.reads)
	}
}

func TestDeliveryRecoveryUploadSharesByteAdmissionWithRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	wa := &deliveryUploadWA{WAClient: &fakeWA{connected: true}, uploadHook: func(c context.Context, _ []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
		close(started)
		select {
		case <-release:
		case <-c.Done():
			return whatsmeow.UploadResponse{}, c.Err()
		}
		return whatsmeow.UploadResponse{URL: "recovered"}, nil
	}}
	app := newTestAppWithWA(t, wa)
	_, err := app.contacts.createDelivery(ctx, 0, "G", "hi", []Contact{{Name: "A", Phone: "5511111111111"}}, nil, mediaPayload{data: []byte("synthetic media"), kind: "image", mime: "image/jpeg", name: "synthetic.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.contacts.recoverDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	wkr := &OutboxWorker{app: app, logger: app.logger}
	go func() { defer close(finished); wkr.processOnce() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("no recovery upload")
	}
	b := &unreadBody{}
	r := httptest.NewRequest("POST", "/api/broadcast", b)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	w := httptest.NewRecorder()
	app.handleBroadcast(w, r)
	close(release)
	<-finished
	if w.Code != 429 || b.reads != 0 {
		t.Fatalf("recovery and request allocated concurrently: code=%d reads=%d", w.Code, b.reads)
	}
}
