// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
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
	"errors"
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
// uses HttpOnly, SameSite=Strict and Secure on trusted HTTPS requests.
// Copies are bearer credentials; the durable allowlist makes logout revocable.
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
	if value == "" {
		jsonError(w, http.StatusServiceUnavailable, "session_unavailable", "session store unavailable")
		return
	}
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

// handleSessionLogout durably revokes the cookie identity, then clears it.
// Idempotent when absent; persistence errors return 503 instead of false success.
func (app *App) handleSessionLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if err := app.revokeSession(c.Value); err != nil {
			jsonError(w, http.StatusServiceUnavailable, "session_unavailable", "session revocation unavailable")
			return
		}
	}
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
	if app.authToken == "" || len(app.sessionKey()) != 32 {
		return ""
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	payload := hex.EncodeToString(nonce) + "." + strconv.FormatInt(time.Now().Unix(), 10)
	value := payload + "." + app.sessionMACFor(payload)
	if !app.registerSession(value) {
		return ""
	}
	return value
}

// sessionMACFor returns the hex HMAC of payload under the session key.
func (app *App) sessionMACFor(payload string) string {
	mac := hmac.New(sha256.New, app.sessionKey())
	mac.Write([]byte(app.authToken))
	mac.Write([]byte{0})
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
// fresh 32-byte key when missing. Read, entropy, or persistence errors return
// nil: native login/startup fail closed instead of silently replacing keys.
func loadOrCreateSessionKey(lg *slog.Logger, dir string) []byte {
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil
	}
	if dir == "" {
		return fresh
	}
	path := filepath.Join(dir, "session.key")
	// #nosec G304 -- path derives from STORE_DIR, set by the operator.
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != 32 {
			return nil
		}
		return b
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// #nosec G304 -- operator-selected STORE_DIR, never a request-supplied path.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil
	}
	if _, err = f.Write(fresh); err != nil {
		_ = f.Close()
		return nil
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return nil
	}
	if err = f.Close(); err != nil {
		return nil
	}
	if lg != nil {
		lg.Debug("created session key")
	}
	return fresh
}

// validSessionCookie reports whether value is a well-formed, correctly-signed
// and unexpired session cookie. Signature check is constant-time; expiry is
// enforced server-side against sessionTTL from the embedded issue time.
func (app *App) validSessionCookie(value string) bool {
	if value == "" || app.authToken == "" || len(app.sessionKey()) != 32 || !app.sessionActive(value) {
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
	age := time.Since(time.Unix(issued, 0))
	return age >= 0 && age < sessionTTL && app.sessionActive(value)
}

// isSecureRequest mirrors net/http's heuristic. TLS terminated inside
// our process counts; behind a reverse proxy the proxy must set
// X-Forwarded-Proto: https and be an immediate peer configured in TRUST_PROXY.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return peerIsTrusted(remoteHost(r)) && r.Header.Get("X-Forwarded-Proto") == "https"
}
