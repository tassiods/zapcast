// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Client-error sink. The SPA posts `window.error` / `unhandledrejection`
// payloads here so operators can see frontend bugs in the server log
// without setting up a separate telemetry pipeline.
//
// The endpoint deliberately does no business logic: decode → bucket
// metric → log. We never echo the payload back, and the response is
// always 204 so client-side retries can't leak status-based info.

import (
	"net/http"
)

// ClientErrorPayload mirrors the JSON shape produced by the global error
// handlers in web/app.js.
type ClientErrorPayload struct {
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Stack     string `json:"stack,omitempty"`
	Source    string `json:"source,omitempty"`
	Line      int    `json:"line,omitempty"`
	Col       int    `json:"col,omitempty"`
	UserAgent string `json:"userAgent,omitempty"`
	Href      string `json:"href,omitempty"`
}

func (app *App) handleClientError(w http.ResponseWriter, r *http.Request) {
	var body ClientErrorPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	// Only registered browser error categories become metric labels.
	switch body.Kind {
	case "window.error", "unhandledrejection":
	default:
		body.Kind = "unknown"
	}
	// Truncate again on the server side — the client already caps these,
	// but a malicious caller could bypass the limit.
	body.Message = truncate(app.sanitizeTelemetry(body.Message), 500)
	body.Stack = truncate(app.sanitizeTelemetry(body.Stack), 2000)
	body.Source = truncate(app.sanitizeTelemetry(sanitizeTelemetryURL(body.Source)), 200)
	body.UserAgent = truncate(app.sanitizeTelemetry(body.UserAgent), 200)
	body.Href = truncate(app.sanitizeTelemetry(sanitizeTelemetryURL(body.Href)), 500)

	app.logger.Warn("client error",
		"kind", body.Kind,
		"message", body.Message,
		"stack", body.Stack,
		"source", body.Source,
		"line", body.Line,
		"col", body.Col,
		"href", body.Href,
		"user_agent", body.UserAgent,
		"remote", clientIP(r),
	)
	metricClientErrors.Add(body.Kind, 1)

	w.WriteHeader(http.StatusNoContent)
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
