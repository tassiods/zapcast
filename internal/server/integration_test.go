// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// End-to-end tests that exercise the production middleware chain
// (access log → request_id → CORS → auth → mux). Each test boots a
// real httptest.Server so the wire shape — headers, status codes, body
// — matches what a browser would see.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// newIntegrationApp wires an App with the full middleware chain and
// returns the httptest.Server the test should hit. The chain mirrors
// what Run() builds in production.
func newIntegrationApp(t *testing.T, opts ...func(*App)) (*App, *httptest.Server) {
	t.Helper()

	container, err := sqlstore.New(context.Background(), "sqlite3",
		"file:integration_"+t.Name()+"?mode=memory&cache=shared&_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("sqlstore.New: %v", err)
	}
	t.Cleanup(func() { _ = container.Close() })

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		t.Fatalf("GetFirstDevice: %v", err)
	}
	client := whatsmeow.NewClient(deviceStore, waLog.Noop)

	shutdownCtx, shutdownStop := context.WithCancel(context.Background())
	t.Cleanup(shutdownStop)

	app := &App{
		logger:       testLogger,
		container:    container,
		wa:           NewWAClientFromWhatsmeow(client),
		contacts:     newTestStore(t),
		hub:          NewSSEHub(testLogger),
		broadcaster:  NewBroadcaster(testLogger, 10*time.Millisecond, 0, 0),
		shutdownCtx:  shutdownCtx,
		shutdownStop: shutdownStop,
		// Rate limiters with a huge burst so integration tests can fire
		// requests freely. Production values are wired in newApp().
		qrLimiter:        newRateLimiter(time.Millisecond, 1000, time.Minute),
		logoutLimiter:    newRateLimiter(time.Millisecond, 1000, time.Minute),
		broadcastLimiter: newRateLimiter(time.Millisecond, 1000, time.Minute),
	}
	for _, opt := range opts {
		opt(app)
	}

	mux := http.NewServeMux()
	app.registerRoutes(mux)
	handler := accessLogMiddleware(testLogger,
		requestIDMiddleware(
			app.corsMiddleware(
				app.authMiddleware(mux))))

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		app.qrLimiter.Close()
		app.logoutLimiter.Close()
		app.broadcastLimiter.Close()
	})
	return app, srv
}

func TestIntegrationRequestIDEchoed(t *testing.T) {
	_, srv := newIntegrationApp(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Request-Id") == "" {
		t.Errorf("X-Request-Id header missing")
	}
}

func TestIntegrationStaticETag304(t *testing.T) {
	_, srv := newIntegrationApp(t)

	first, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	first.Body.Close()
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatalf("expected ETag on first response")
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("If-None-Match", etag)
	second, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusNotModified {
		t.Errorf("expected 304, got %d", second.StatusCode)
	}
}

func TestIntegrationAuthMiddlewareGatesAPI(t *testing.T) {
	_, srv := newIntegrationApp(t, func(a *App) { a.authToken = "topsecret" })

	resp, err := http.Get(srv.URL + "/api/contacts")
	if err != nil {
		t.Fatalf("GET /api/contacts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// Structured error envelope.
	var body apiErrorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "unauthorized" {
		t.Errorf("expected code unauthorized, got %q", body.Error.Code)
	}

	// /api/translations is intentionally public.
	pub, err := http.Get(srv.URL + "/api/translations")
	if err != nil {
		t.Fatalf("GET /api/translations: %v", err)
	}
	pub.Body.Close()
	if pub.StatusCode != http.StatusOK {
		t.Errorf("public endpoint blocked: %d", pub.StatusCode)
	}
}

func TestIntegrationStructuredErrorEnvelope(t *testing.T) {
	_, srv := newIntegrationApp(t)

	// POST without name/phone trips the 400 + err_name_phone_required path.
	resp, err := http.Post(srv.URL+"/api/contacts", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)

	var env apiErrorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode (%s): %v", raw, err)
	}
	if env.Error.Code != "err_name_phone_required" {
		t.Errorf("code = %q, want err_name_phone_required", env.Error.Code)
	}
	if env.Error.Message == "" {
		t.Errorf("message empty")
	}
}

func TestIntegrationDebugVarsGatedByAuth(t *testing.T) {
	_, srv := newIntegrationApp(t, func(a *App) { a.authToken = "topsecret" })

	// Without token → 401.
	resp, err := http.Get(srv.URL + "/debug/vars")
	if err != nil {
		t.Fatalf("GET /debug/vars: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// With token → 200.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/debug/vars", nil)
	req.Header.Set("Authorization", "Bearer topsecret")
	ok, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed GET: %v", err)
	}
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with bearer, got %d", ok.StatusCode)
	}
}

func TestIntegrationTranslationsETag304(t *testing.T) {
	_, srv := newIntegrationApp(t)

	first, err := http.Get(srv.URL + "/api/translations")
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	first.Body.Close()
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatalf("expected ETag on translations response")
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/translations", nil)
	req.Header.Set("If-None-Match", etag)
	second, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusNotModified {
		t.Errorf("expected 304, got %d", second.StatusCode)
	}
}
