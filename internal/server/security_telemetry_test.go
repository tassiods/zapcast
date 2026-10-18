package server

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityTelemetryStackAndRelativeURLsAreRedacted(t *testing.T) {
	var log bytes.Buffer
	app := &App{authToken: "SYNTHETIC_MASTER_SECRET", logger: slog.New(slog.NewTextHandler(&log, nil))}
	r := httptest.NewRequest("POST", "/api/client-error", strings.NewReader(`{"kind":"window.error","message":"failure at /a.js?credential=SYNTHETIC_RELATIVE_SECRET#private","stack":"Error: SYNTHETIC_MASTER_SECRET\nat https://audit.invalid/a.js?credential=SYNTHETIC_STACK_SECRET#private"}`))
	w := httptest.NewRecorder()
	app.handleClientError(w, r)
	if w.Code != 204 {
		t.Fatalf("stack telemetry incompatible: %d", w.Code)
	}
	if strings.Contains(log.String(), "SYNTHETIC_") {
		t.Fatalf("sensitive marker retained: %s", log.String())
	}
}

func TestSecurityTelemetryRedactsURLsAndTokenText(t *testing.T) {
	var log bytes.Buffer
	app := &App{authToken: "SYNTHETIC_MASTER_SECRET", logger: slog.New(slog.NewTextHandler(&log, nil))}
	r := httptest.NewRequest("POST", "/api/client-error", strings.NewReader(`{"kind":"window.error","href":"https://audit.invalid/?token=SYNTHETIC_QUERY_SECRET#SYNTHETIC_FRAGMENT","source":"https://audit.invalid/a.js?credential=SYNTHETIC_SOURCE_SECRET","message":"failure https://audit.invalid/a.js?token=SYNTHETIC_MESSAGE_SECRET#fragment Bearer SYNTHETIC_BEARER_SECRET SYNTHETIC_MASTER_SECRET"}`))
	w := httptest.NewRecorder()
	app.handleClientError(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if strings.Contains(log.String(), "SYNTHETIC_") {
		t.Fatalf("sensitive marker retained: %s", log.String())
	}
	if !strings.Contains(log.String(), "https://audit.invalid/a.js") {
		t.Fatal("safe debugging URL lost")
	}
}
