// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Status + QR pairing + logout. The pairing flow streams QR events back to
// the browser over SSE so a single open request produces multiple refreshes
// and a final success / timeout.

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
)

// versionFallback is the literal used when neither ldflag nor module system
// provides a real version — i.e. local `go build` / `just build` only.
// Release binaries get the truth injected via
// `-X zapcast/internal/server.versionLdflag` (see .github/workflows/release.yml).
// Kept as a static "dev" marker rather than a version number so it can't
// drift out of sync with the real release version every bump.
const versionFallback = "dev"

var versionLdflag = ""

// Version returns the canonical version string.
func Version() string {
	if versionLdflag != "" {
		return versionLdflag
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return versionFallback
}

// AuthRequiredResponse is the body of /api/auth/required.
type AuthRequiredResponse struct {
	Required bool `json:"required"`
}

// StatusResponse is the body of /api/status. Phone is empty when no device is
// paired; encoding/json omits the field via `omitempty`.
type StatusResponse struct {
	Connected bool   `json:"connected"`
	LoggedIn  bool   `json:"loggedIn"`
	Version   string `json:"version"`
	Phone     string `json:"phone,omitempty"`
}

// AuthAlreadyConnectedResponse is the JSON body emitted by handleAuthQR
// when the device is already paired (the caller skipped the SSE loop).
type AuthAlreadyConnectedResponse struct {
	Status string `json:"status"` // always "already_connected"
	Phone  string `json:"phone"`
}

// LogoutResponse confirms that the device session was torn down.
type LogoutResponse struct {
	Status string `json:"status"` // always "logged_out"
}

func (app *App) handleAuthRequired(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, AuthRequiredResponse{Required: app.authToken != ""})
}

func (app *App) handleStatus(w http.ResponseWriter, _ *http.Request) {
	client := app.WA()
	resp := StatusResponse{
		Connected: client.IsConnected(),
		Version:   Version(),
	}
	if id := client.JID(); id != nil {
		resp.LoggedIn = true
		resp.Phone = id.User
	}
	jsonResponse(w, http.StatusOK, resp)
}

// QRCodeData carries one refresh of the QR PNG + raw pairing code.
type QRCodeData struct {
	Code string `json:"code"`
	PNG  string `json:"png"` // data: URL with base64-encoded PNG
}

// handleAuthQR pairs a fresh WhatsApp device. Returns JSON or SSE stream.
func (app *App) handleAuthQR(w http.ResponseWriter, r *http.Request) {
	// Fast path: already paired, skip the SSE loop.
	if probe := app.WA(); probe.IsConnected() && probe.JID() != nil {
		jsonResponse(w, http.StatusOK, AuthAlreadyConnectedResponse{
			Status: "already_connected",
			Phone:  probe.JID().User,
		})
		return
	}

	if !app.qrMu.TryLock() {
		jsonErrorLocalized(w, r, http.StatusTooManyRequests, "err_qr_pending")
		return
	}
	defer app.qrMu.Unlock()

	// Recapture under qrMu: Logout may have swapped the client since probe.
	client := app.WA()

	// Drop stale connection before pairing.
	client.Disconnect()

	// Bind QR loop to request context so closing the tab tears it down.
	qrChan, err := client.GetQRChannel(r.Context())
	if err != nil {
		jsonErrorLocalized(w, r, http.StatusInternalServerError, "err_qr_generate", err)
		return
	}
	if err := client.Connect(); err != nil {
		jsonErrorLocalized(w, r, http.StatusInternalServerError, "err_connect", err)
		return
	}

	flusher, ok := setupSSE(w, r)
	if !ok {
		return
	}

	const pairingDeadline = 120 * time.Second
	timeout := time.After(pairingDeadline)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			writeSSEFrame(w, flusher, Event{Type: "timeout"})
			return
		case evt, open := <-qrChan:
			if !open {
				return
			}
			res := processQREvent(evt)
			if res.Frame == nil {
				continue
			}
			writeSSEFrame(w, flusher, *res.Frame)
			if res.SideEvent != nil {
				app.broadcastEvent(*res.SideEvent)
			}
			if res.Terminal {
				return
			}
		}
	}
}

// qrFrameResult is what processQREvent hands back to handleAuthQR. Frame
// is what we write on the pairing SSE stream (nil = drop); Terminal flips
// to true once we should close the stream; SideEvent piggybacks onto the
// shared hub for global subscribers (status banner, dashboard counters).
type qrFrameResult struct {
	Frame     *Event
	Terminal  bool
	SideEvent *Event
}

// processQREvent classifies one whatsmeow QR channel item.
func processQREvent(evt whatsmeow.QRChannelItem) qrFrameResult {
	switch evt.Event {
	case "code":
		png, err := qrcode.Encode(evt.Code, qrcode.Medium, 280)
		if err != nil {
			slog.Default().Error("qrcode encode failed", "err", err)
			return qrFrameResult{}
		}
		dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		return qrFrameResult{
			Frame:     &Event{Type: "qr", Data: QRCodeData{Code: evt.Code, PNG: dataURL}},
			SideEvent: &Event{Type: "qr", Data: evt.Code},
		}
	case "success":
		return qrFrameResult{
			Frame:     &Event{Type: "success"},
			Terminal:  true,
			SideEvent: &Event{Type: "connected"},
		}
	case "timeout":
		return qrFrameResult{Frame: &Event{Type: "timeout"}, Terminal: true}
	}
	return qrFrameResult{}
}

// handleLogout tears down the current device session and rebuilds the
// in-memory whatsmeow client so the next QR pairing starts from clean
// Noise/Identity keys. Serialized against handleAuthQR via qrMu.
func (app *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !app.qrMu.TryLock() {
		jsonErrorLocalized(w, r, http.StatusTooManyRequests, "err_logout_pending")
		return
	}
	defer app.qrMu.Unlock()

	if err := app.WA().Logout(r.Context()); err != nil {
		jsonErrorLocalized(w, r, http.StatusInternalServerError, "err_logout", err)
		return
	}
	// whatsmeow Logout only deletes the row from the store; the in-memory
	// Device keeps stale Noise/Identity keys, which blocks GetQRChannel from
	// emitting on the next pairing attempt. Swap in a fresh Device.
	if err := app.resetClient(r.Context()); err != nil {
		app.logger.Error("failed to reset client after logout", "err", err)
	}
	jsonResponse(w, http.StatusOK, LogoutResponse{Status: "logged_out"})
}
