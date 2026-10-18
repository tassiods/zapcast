// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// --- processQREvent (pure helper extracted from handleAuthQR) ---

func TestProcessQREventCode(t *testing.T) {
	res := processQREvent(whatsmeow.QRChannelItem{Event: "code", Code: "abc"})
	if res.Frame == nil {
		t.Fatal("expected frame for code event")
	}
	if res.Terminal {
		t.Errorf("code event must not be terminal")
	}
	if res.Frame.Type != "qr" {
		t.Errorf("frame type wrong: %+v", res.Frame)
	}
	qr, ok := res.Frame.Data.(QRCodeData)
	if !ok {
		t.Fatalf("expected QRCodeData, got %T", res.Frame.Data)
	}
	if qr.Code != "abc" {
		t.Errorf("code wrong: %q", qr.Code)
	}
	if !strings.HasPrefix(qr.PNG, "data:image/png;base64,") {
		t.Errorf("png prefix wrong: %q", qr.PNG)
	}
	if res.SideEvent == nil || res.SideEvent.Type != "qr" {
		t.Errorf("side event wrong: %+v", res.SideEvent)
	}
}

func TestProcessQREventSuccess(t *testing.T) {
	res := processQREvent(whatsmeow.QRChannelItem{Event: "success"})
	if !res.Terminal || res.Frame == nil || res.Frame.Type != "success" {
		t.Errorf("success wrong: terminal=%v frame=%+v", res.Terminal, res.Frame)
	}
	if res.SideEvent == nil || res.SideEvent.Type != "connected" {
		t.Errorf("expected connected side event, got %+v", res.SideEvent)
	}
}

func TestProcessQREventTimeout(t *testing.T) {
	res := processQREvent(whatsmeow.QRChannelItem{Event: "timeout"})
	if !res.Terminal || res.Frame == nil || res.Frame.Type != "timeout" {
		t.Errorf("timeout wrong: terminal=%v frame=%+v", res.Terminal, res.Frame)
	}
	if res.SideEvent != nil {
		t.Errorf("timeout must not produce side event, got %+v", res.SideEvent)
	}
}

func TestProcessQREventUnknownDropped(t *testing.T) {
	res := processQREvent(whatsmeow.QRChannelItem{Event: "weird"})
	if res.Frame != nil || res.Terminal || res.SideEvent != nil {
		t.Errorf("unknown event must produce no output, got %+v", res)
	}
}

// --- handleAuthQR end-to-end with fake WAClient ---

func TestHandleAuthQRAlreadyConnected(t *testing.T) {
	jid := types.NewJID("5511999998888", types.DefaultUserServer)
	wa := &fakeWA{connected: true, jid: &jid}
	app := newTestAppWithWA(t, wa)
	rec := httptest.NewRecorder()
	app.handleAuthQR(rec, httptest.NewRequest("POST", "/api/auth/qr", nil))
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already_connected") {
		t.Errorf("expected already_connected payload, got %s", rec.Body.String())
	}
}

func TestHandleAuthQRBusyLockReturns429(t *testing.T) {
	wa := &fakeWA{}
	app := newTestAppWithWA(t, wa)
	app.qrMu.Lock()
	defer app.qrMu.Unlock()
	rec := httptest.NewRecorder()
	app.handleAuthQR(rec, httptest.NewRequest("POST", "/api/auth/qr", nil))
	if rec.Code != 429 {
		t.Errorf("expected 429 (qr pending), got %d", rec.Code)
	}
}

func TestHandleAuthQRGetQRChannelError(t *testing.T) {
	wa := &fakeWA{qrChanErr: errBoom}
	app := newTestAppWithWA(t, wa)
	rec := httptest.NewRecorder()
	app.handleAuthQR(rec, httptest.NewRequest("POST", "/api/auth/qr", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500 on GetQRChannel error, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAuthQRConnectError(t *testing.T) {
	wa := &fakeWA{connectErr: errBoom}
	app := newTestAppWithWA(t, wa)
	rec := httptest.NewRecorder()
	app.handleAuthQR(rec, httptest.NewRequest("POST", "/api/auth/qr", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500 on Connect error, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAuthQRStreamsCodeAndSuccess(t *testing.T) {
	wa := &fakeWA{qrChan: make(chan whatsmeow.QRChannelItem, 4)}
	// Pre-fill so the handler emits the first frame immediately — Go's HTTP
	// server only flushes response headers when the handler writes, so a
	// purely-blocked select would leave the client waiting on headers.
	wa.qrChan <- whatsmeow.QRChannelItem{Event: "code", Code: "QRDATA"}
	wa.qrChan <- whatsmeow.QRChannelItem{Event: "success"}

	app := newTestAppWithWA(t, wa)
	srv := httptest.NewServer(http.HandlerFunc(app.handleAuthQR))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected SSE, got %q (status %d)", got, resp.StatusCode)
	}

	br := bufio.NewReader(resp.Body)
	var sawQR, sawSuccess bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, `"type":"qr"`) {
			sawQR = true
		}
		if strings.Contains(line, `"type":"success"`) {
			sawSuccess = true
			break
		}
	}
	if !sawQR || !sawSuccess {
		t.Errorf("missed event(s): sawQR=%v sawSuccess=%v", sawQR, sawSuccess)
	}
}

func TestHandleAuthQRTimeoutBranch(t *testing.T) {
	wa := &fakeWA{qrChan: make(chan whatsmeow.QRChannelItem, 1)}
	wa.qrChan <- whatsmeow.QRChannelItem{Event: "timeout"}

	app := newTestAppWithWA(t, wa)
	srv := httptest.NewServer(http.HandlerFunc(app.handleAuthQR))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, `"type":"timeout"`) {
			return // expected
		}
	}
	t.Errorf("did not observe timeout event")
}

// --- handleLogout end-to-end with fake WAClient ---

func TestHandleLogoutHappy(t *testing.T) {
	// resetClient still constructs a real whatsmeow.Client via the container,
	// so we need a container — pick the real path via newTestApp + swap in a
	// fake whose Logout returns nil.
	app := newTestApp(t)
	wa := &fakeWA{}
	app.clientMu.Lock()
	app.wa = wa
	app.clientMu.Unlock()

	rec := httptest.NewRecorder()
	app.handleLogout(rec, httptest.NewRequest("POST", "/api/auth/logout", nil))
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "logged_out") {
		t.Errorf("expected logged_out payload, got %s", rec.Body.String())
	}
	// resetClient should have rotated app.wa to a fresh wrapper.
	if app.WA() == WAClient(wa) {
		t.Errorf("app.wa was not rotated after Logout")
	}
}

func TestHandleLogoutBusyLock(t *testing.T) {
	wa := &fakeWA{}
	app := newTestAppWithWA(t, wa)
	app.qrMu.Lock()
	defer app.qrMu.Unlock()
	rec := httptest.NewRecorder()
	app.handleLogout(rec, httptest.NewRequest("POST", "/api/auth/logout", nil))
	if rec.Code != 429 {
		t.Errorf("expected 429, got %d", rec.Code)
	}
}

func TestHandleLogoutSurfacesError(t *testing.T) {
	wa := &fakeWA{logoutErr: errBoom}
	app := newTestAppWithWA(t, wa)
	rec := httptest.NewRecorder()
	app.handleLogout(rec, httptest.NewRequest("POST", "/api/auth/logout", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}
