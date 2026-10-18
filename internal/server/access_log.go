// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// HTTP access log middleware. Skips high-frequency probes (/healthz, /readyz,
// /api/events) to keep noise low.

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// noisyPaths are excluded from the access log to keep production logs
// readable. They're still observable elsewhere (probes via process metrics,
// SSE via the SSE drop counter).
var noisyPaths = map[string]bool{
	"/healthz":    true,
	"/readyz":     true,
	"/api/events": true,
}

// loggingResponseWriter captures status + bytes written. Defaults to 200.
type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *loggingResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush exposes the underlying flusher so SSE handlers keep working.
func (w *loggingResponseWriter) Flush() {
	_ = w.FlushError()
}

func (w *loggingResponseWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// accessLogMiddleware logs method/path/status/duration after each request.
func accessLogMiddleware(lg *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noisyPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		lw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lw, r)
		dur := time.Since(start)

		rid, _ := r.Context().Value(requestIDKey).(string)
		lg.Info("http",
			"method", r.Method,
			"path", trimQuery(r.URL.RequestURI()),
			"status", lw.status,
			"bytes", lw.bytes,
			"dur_ms", dur.Milliseconds(),
			"remote", clientIP(r),
			"request_id", rid,
		)
		metricHTTPRequests.Add(httpStatusClass(lw.status), 1)
		// Per-route counter by registered pattern.
		metricHTTPByRoute.Add(routeLabel(r), 1)
	})
}

// trimQuery drops the query string so bearer tokens (?token=) don't leak
// into log files.
func trimQuery(uri string) string {
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		return uri[:i]
	}
	return uri
}

// httpStatusClass buckets responses into 2xx/3xx/4xx/5xx so the per-status
// expvar map stays small even under arbitrary status codes.
func httpStatusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

// routeLabel returns the registered mux pattern for the current request
// (Go 1.22+ ServeMux exposes it via r.Pattern). Requests rejected before
// routing share one unknown bucket; never retain client-controlled paths.
func routeLabel(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unknown"
}
