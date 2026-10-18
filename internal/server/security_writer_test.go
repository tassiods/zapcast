package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline    time.Time
	deadlineErr error
	flushErr    error
}

func (w *deadlineRecorder) SetWriteDeadline(d time.Time) error { w.deadline = d; return w.deadlineErr }
func (w *deadlineRecorder) FlushError() error                  { return w.flushErr }

func TestSecurityPermissionsPolicyOmitsInvalidBluetooth(t *testing.T) {
	w := httptest.NewRecorder()
	applySecurityHeaders(w)
	if strings.Contains(w.Header().Get("Permissions-Policy"), "bluetooth") {
		t.Fatal("invalid bluetooth policy still emitted")
	}
}

func TestSecurityLoggingWriterForwardsQRDeadline(t *testing.T) {
	base := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	wrapped := &loggingResponseWriter{ResponseWriter: base}
	if err := http.NewResponseController(wrapped).SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal("QR deadline cannot reach socket:", err)
	}
	if base.deadline.IsZero() {
		t.Fatal("deadline not forwarded")
	}
}

func TestSecuritySSEWritePropagatesDeadlineAndFlushErrors(t *testing.T) {
	boom := errors.New("socket write failure")
	for _, base := range []*deadlineRecorder{{ResponseRecorder: httptest.NewRecorder(), deadlineErr: boom}, {ResponseRecorder: httptest.NewRecorder(), flushErr: boom}} {
		wrapped := &loggingResponseWriter{ResponseWriter: base}
		if writeSSEFrame(wrapped, wrapped, Event{Type: "qr", Data: "synthetic"}) {
			t.Fatal("stream silently ignored socket error")
		}
	}
}
