// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Liveness and readiness probes. /healthz is always 200; /readyz returns
// 503 only when the DB is unreachable. WhatsApp connectivity is reported
// informationally but never gates readiness — a disconnected device must
// not take the server out of rotation, since the operator needs the web
// UI up to re-pair via QR.

import (
	"context"
	"net/http"
	"time"
)

// ReadyzResponse aggregates per-dependency status plus the rolled-up boolean.
type ReadyzResponse struct {
	Ready  bool              `json:"ready"`
	Checks map[string]string `json:"checks"`
}

// handleHealthz is the trivial liveness probe — plain text "ok".
func (app *App) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (app *App) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	checks := map[string]string{"db": "ok", "wa": "ok"}
	ready := true

	if err := app.contacts.db.PingContext(ctx); err != nil {
		// Generic status only — /readyz is unauthenticated, so the raw driver
		// error (which can disclose file paths / internals) stays in the log.
		checks["db"] = "fail"
		app.logger.Warn("readyz db ping failed", "err", err)
		ready = false
	}
	// Informational only — never flips ready (see file comment).
	if wa := app.WA(); wa == nil || !wa.IsConnected() {
		checks["wa"] = "disconnected"
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	jsonResponse(w, status, ReadyzResponse{Ready: ready, Checks: checks})
}
