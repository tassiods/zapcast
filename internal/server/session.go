// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Cookie-based session.
//
// Bearer tokens in ?token= leak into proxy logs. POSTing the token once and
// pinning it to an httpOnly cookie removes those vectors.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookieName = "zapcast_session"
	sessionTTL        = 12 * time.Hour
)

// LoginRequest is the JSON body of /api/auth/login.
type LoginRequest struct {
	Token string `json:"token"`
}

// LoginResponse confirms the session was created.
type LoginResponse struct {
	Status string `json:"status"` // always "ok"
}

// handleLogin trades a bearer token for an httpOnly cookie. The cookie
// is bound to the listener's origin (Secure + SameSite=Strict) so a
// stolen cookie can't be reused cross-site or over plain HTTP.
//
// We reject anything but the configured AUTH_TOKEN (or anything when
// AUTH_TOKEN is empty — there's nothing to authenticate without one).
func (app *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if app.authToken == "" {
		// No auth configured — sessions don't make sense.
		jsonError(w, http.StatusBadRequest, "auth_disabled", "AUTH_TOKEN is not configured")
		return
	}
	var body LoginRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Token), []byte(app.authToken)) != 1 {
		jsonError(w, http.StatusUnauthorized, "unauthorized", "invalid token")
		return
	}
	value := app.issueSession()
	// #nosec G124 -- HttpOnly/SameSite=Strict are set; Secure is gated by
	// isSecureRequest so local-HTTP dev still works. Production runs
	// behind TLS (req.TLS != nil) or a proxy setting X-Forwarded-Proto.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	jsonResponse(w, http.StatusOK, LoginResponse{Status: "ok"})
}

// handleSessionLogout clears the session cookie. Idempotent; succeeds
// even when no cookie is present.
func (app *App) handleSessionLogout(w http.ResponseWriter, r *http.Request) {
	// #nosec G124 -- mirrors handleLogin; clearing a cookie that was set
	// with the same attributes the client originally received.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
	jsonResponse(w, http.StatusOK, LogoutResponse{Status: "logged_out"})
}

// issueSession mints a fresh signed session cookie value of the form
//
//	<nonceHex>.<issuedUnix>.<macHex>
//
// The random nonce makes every login produce a distinct cookie (so a leaked
// value can't be recognised as "the" session), and the embedded issue time is
// signed and checked server-side (validSessionCookie), giving the cookie a
// real, enforced TTL instead of the old constant HMAC(token) that never
// expired and never changed.
func (app *App) issueSession() string {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	payload := hex.EncodeToString(nonce) + "." + strconv.FormatInt(time.Now().Unix(), 10)
	return payload + "." + app.sessionMACFor(payload)
}

// sessionMACFor returns the hex HMAC of payload under the session key.
func (app *App) sessionMACFor(payload string) string {
	mac := hmac.New(sha256.New, app.sessionKey())
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// sessionKey returns the HMAC key, loaded lazily so binaries that never
// enable AUTH_TOKEN never touch it. The key is persisted under STORE_DIR
// so browser sessions survive process restarts; when no store dir is
// configured (tests) it falls back to an in-memory key.
func (app *App) sessionKey() []byte {
	app.sessionKeyOnce.Do(func() {
		app.sessionKeyValue = loadOrCreateSessionKey(app.logger, app.sessionKeyDir)
	})
	return app.sessionKeyValue
}

// loadOrCreateSessionKey reads dir/session.key, generating + persisting a
// fresh 32-byte key when missing. Persist failures degrade gracefully to
// the per-process behaviour (sessions die on restart) with a warning.
func loadOrCreateSessionKey(lg *slog.Logger, dir string) []byte {
	fresh := make([]byte, 32)
	_, _ = rand.Read(fresh)
	if dir == "" {
		return fresh
	}
	path := filepath.Join(dir, "session.key")
	// #nosec G304 -- path derives from STORE_DIR, set by the operator.
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b
	}
	if err := os.WriteFile(path, fresh, 0o600); err != nil && lg != nil {
		lg.Warn("failed to persist session key — sessions won't survive restarts",
			"err", err, "path", path)
	}
	return fresh
}

// validSessionCookie reports whether value is a well-formed, correctly-signed
// and unexpired session cookie. Signature check is constant-time; expiry is
// enforced server-side against sessionTTL from the embedded issue time.
func (app *App) validSessionCookie(value string) bool {
	if value == "" || app.authToken == "" {
		return false
	}
	// Split off the trailing MAC segment; the rest is the signed payload.
	i := strings.LastIndexByte(value, '.')
	if i < 0 {
		return false
	}
	payload, gotMAC := value[:i], value[i+1:]
	expMAC := app.sessionMACFor(payload)
	if subtle.ConstantTimeCompare([]byte(gotMAC), []byte(expMAC)) != 1 {
		return false
	}
	// payload = <nonceHex>.<issuedUnix>. Enforce the TTL server-side.
	j := strings.LastIndexByte(payload, '.')
	if j < 0 {
		return false
	}
	issued, err := strconv.ParseInt(payload[j+1:], 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(issued, 0)) < sessionTTL
}

// isSecureRequest mirrors net/http's heuristic. TLS terminated inside
// our process counts; behind a reverse proxy the proxy must set
// X-Forwarded-Proto: https.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}
