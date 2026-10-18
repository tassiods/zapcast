// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// passHandler is a trivial 200 OK terminator used to detect middleware passthrough.
var passHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

// --- requestIDMiddleware ---

func TestRequestIDGeneratedWhenMissing(t *testing.T) {
	h := requestIDMiddleware(passHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got := rec.Header().Get("X-Request-Id"); got == "" {
		t.Errorf("expected generated request id, got empty")
	}
}

func TestRequestIDHonorsIncoming(t *testing.T) {
	h := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rid, _ := r.Context().Value(requestIDKey).(string); rid != "client-abc" {
			t.Errorf("context request id = %q, want client-abc", rid)
		}
		w.WriteHeader(200)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", "client-abc")
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-Id"); got != "client-abc" {
		t.Errorf("echoed X-Request-Id = %q", got)
	}
}

func TestNewRequestIDIsUnique(t *testing.T) {
	a := newRequestID()
	b := newRequestID()
	if a == b {
		t.Errorf("expected unique ids, both = %q", a)
	}
}

// --- corsMiddleware ---

func TestCorsWildcardWhenNoOriginConfigured(t *testing.T) {
	app := &App{}
	h := app.corsMiddleware(passHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected wildcard origin, got %q", got)
	}
}

func TestCorsStrictAllowedOrigin(t *testing.T) {
	app := &App{allowedOrigin: "https://app.example"}
	h := app.corsMiddleware(passHandler)

	// Matching origin passes.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://app.example")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("matched origin should pass, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Errorf("ACAO = %q", got)
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("credentials header missing in strict mode")
	}

	// Mismatched origin is blocked.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestCorsPreflightShortCircuits(t *testing.T) {
	app := &App{}
	called := false
	h := app.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/x", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
	if called {
		t.Errorf("inner handler should not run on preflight")
	}
}

// --- authMiddleware ---

func TestAuthNoTokenConfiguredPassesThrough(t *testing.T) {
	app := &App{} // empty authToken disables auth
	h := app.authMiddleware(passHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contacts", nil))
	if rec.Code != 200 {
		t.Errorf("expected passthrough, got %d", rec.Code)
	}
}

func TestAuthPublicEndpointsBypass(t *testing.T) {
	app := &App{authToken: "secret"}
	h := app.authMiddleware(passHandler)
	for _, path := range []string{"/api/translations", "/api/auth/required"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Errorf("%s should bypass, got %d", path, rec.Code)
		}
	}
}

func TestAuthRejectsMissingToken(t *testing.T) {
	app := &App{authToken: "secret"}
	h := app.authMiddleware(passHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/contacts", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Errorf("WWW-Authenticate missing")
	}
}

func TestAuthAcceptsBearer(t *testing.T) {
	app := &App{authToken: "secret"}
	h := app.authMiddleware(passHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/contacts", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAuthRejectsQueryToken(t *testing.T) {
	app := &App{authToken: "secret"}
	h := app.authMiddleware(passHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/events?token=secret", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for query token, got %d", rec.Code)
	}
}

func TestAuthRejectsWrongToken(t *testing.T) {
	app := &App{authToken: "secret"}
	h := app.authMiddleware(passHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/contacts", nil)
	req.Header.Set("Authorization", "Bearer nope")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthOPTIONSBypasses(t *testing.T) {
	app := &App{authToken: "secret"}
	h := app.authMiddleware(passHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/contacts", nil))
	if rec.Code != 200 {
		t.Errorf("OPTIONS should pass, got %d", rec.Code)
	}
}

func TestAuthNonAPIRoutesBypass(t *testing.T) {
	app := &App{authToken: "secret"}
	h := app.authMiddleware(passHandler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/index.html", nil))
	if rec.Code != 200 {
		t.Errorf("non-API path should bypass, got %d", rec.Code)
	}
}
