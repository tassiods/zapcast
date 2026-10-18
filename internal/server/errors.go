// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Error envelope + LocalizedError contract.

import (
	"errors"
	"log/slog"
	"net/http"
)

// genericErrorBodyLimit caps the message for the non-localised error path so
// driver stacks don't leak through the API.
const genericErrorBodyLimit = 200

// APIError is the structured body emitted by every failure response.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// apiErrorEnvelope is what handlers JSON-encode.
type apiErrorEnvelope struct {
	Error APIError `json:"error"`
}

// jsonError writes the envelope at the given status.
func jsonError(w http.ResponseWriter, status int, code, msg string) {
	jsonResponse(w, status, apiErrorEnvelope{Error: APIError{Code: code, Message: msg}})
}

// handleError sends a localized error response. Falls back to "internal"
// for non-localized errors so driver stacks don't leak through the API.
func handleError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if err == nil {
		return
	}
	if le, ok := asLocalized(err); ok {
		jsonError(w, status, le.LocalizedKey(),
			TForLang(getLang(r), le.LocalizedKey(), le.LocalizedArgs()...))
		return
	}
	rid, _ := r.Context().Value(requestIDKey).(string)
	slog.Error("unlocalised handler error", "err", err, "status", status, "request_id", rid, "path", r.URL.Path)
	jsonError(w, status, "internal", truncateForWire(err.Error()))
}

// truncateForWire shortens s to genericErrorBodyLimit characters.
func truncateForWire(s string) string {
	if len(s) <= genericErrorBodyLimit {
		return s
	}
	return s[:genericErrorBodyLimit] + "…"
}

// LocalizedError is the interface handlers use to detect translatable errors.
// Using an interface (not a type assertion) survives fmt.Errorf wrapping.
type LocalizedError interface {
	error
	LocalizedKey() string
	LocalizedArgs() []any
}

// asLocalized unwraps err via errors.As to find any LocalizedError.
func asLocalized(err error) (LocalizedError, bool) {
	var le LocalizedError
	if errors.As(err, &le) {
		return le, true
	}
	return nil, false
}

// Compile-time assertions: every type below implements LocalizedError.
var (
	_ LocalizedError = (*errKey)(nil)
	_ LocalizedError = errBroadcastRunning{}
	_ LocalizedError = (*CooldownError)(nil)
)
