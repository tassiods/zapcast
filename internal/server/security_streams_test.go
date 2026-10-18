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

func TestSecurityStreamsCloseOnExpiry(t *testing.T) {
	for _, qr := range []bool{false, true} {
		t.Run(map[bool]string{false: "events", true: "qr"}[qr], func(t *testing.T) {
			wa := &fakeWA{qrChan: make(chan whatsmeow.QRChannelItem, 1)}
			app := &App{authToken: "token", logger: testLogger, wa: wa, hub: NewSSEHub(testLogger)}
			value := app.issueSession()
			app.sessions.mu.Lock()
			app.sessions.active[sessionIdentity(value)] = time.Now().Unix() + 1
			app.sessions.mu.Unlock()
			handler := http.HandlerFunc(app.handleSSE)
			method, path := "GET", "/api/events"
			if qr {
				handler = app.handleAuthQR
				method, path = "POST", "/api/auth/qr"
			}
			server := httptest.NewServer(app.authMiddleware(handler))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			req.Header.Set("Origin", server.URL)
			start := time.Now()
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatal(resp.StatusCode)
			}
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > 1500*time.Millisecond {
				t.Fatal("expiry did not close existing stream")
			}
		})
	}
}

func TestSecurityQRClosesAfterLogout(t *testing.T) {
	wa := &fakeWA{qrChan: make(chan whatsmeow.QRChannelItem, 1)}
	app := &App{authToken: "token", logger: testLogger, wa: wa, hub: NewSSEHub(testLogger)}
	value := app.issueSession()
	server := httptest.NewServer(app.authMiddleware(http.HandlerFunc(app.handleAuthQR)))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/auth/qr", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	done := make(chan error, 1)
	go func() {
		resp, err := server.Client().Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		done <- err
	}()
	for deadline := time.Now().Add(time.Second); !wa.IsConnected() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if !wa.IsConnected() {
		t.Fatal("QR not connected")
	}
	if err := app.revokeSession(value); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("QR remained open after logout")
	}
}

func TestSecuritySSEClosesAfterLogout(t *testing.T) {
	app := &App{authToken: "token", logger: testLogger, hub: NewSSEHub(testLogger)}
	value := app.issueSession()
	server := httptest.NewServer(app.authMiddleware(http.HandlerFunc(app.handleSSE)))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/events", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	done := make(chan error, 1)
	go func() {
		resp, err := server.Client().Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		done <- err
	}()
	for deadline := time.Now().Add(time.Second); app.hub.Count() == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if app.hub.Count() != 1 {
		t.Fatal("SSE not subscribed")
	}
	r := httptest.NewRequest("POST", "/api/auth/session/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w := httptest.NewRecorder()
	app.handleSessionLogout(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE remained open after logout")
	}
}
