// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Shared HTTP middleware. Chain: requestID → CORS → securityHeaders → auth → mux.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// contextKey scopes our context values so they don't collide with anything a
// downstream library might stash on the same Context.
type contextKey string

const requestIDKey contextKey = "request_id"

// requestIDMiddleware honours X-Request-Id or generates a short one and
// echoes it back to the client.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set("X-Request-Id", rid)
		ctx := context.WithValue(r.Context(), requestIDKey, rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newRequestID returns a short correlation id: timestamp + monotonic counter.
var ridCounter uint64

func newRequestID() string {
	n := atomic.AddUint64(&ridCounter, 1)
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), n)
}

// corsMiddleware emits CORS headers. When FRONTEND_ORIGIN is set, only that
// origin is allowed; otherwise falls back to * for local dev.
func (app *App) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		switch {
		case app.allowedOrigin != "":
			// Strict mode: only the configured origin is allowed.
			if origin != "" && origin != app.allowedOrigin {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", app.allowedOrigin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
		case app.authToken != "":
			// AUTH_TOKEN is set but FRONTEND_ORIGIN isn't. Reflect the
			// caller's origin when it matches the listener (same-origin
			// SPA), otherwise reject. We deliberately *don't* fall back to
			// `*` here — a wildcard with a bearer-token API surface widens
			// the blast radius of a stolen token across every site.
			if origin != "" && !isSameOriginRequest(r, origin) {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
			}
		default:
			// No auth, no allowlist — local-dev fallback. Still guard
			// state-changing requests against cross-site drive-by (CSRF): a
			// cross-origin multipart/form POST is a CORS "simple request" and
			// executes the side effect before the browser blocks the response,
			// so reject a mismatched Origin on unsafe methods.
			if origin != "" && isUnsafeMethod(r.Method) && !isSameOriginRequest(r, origin) {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isUnsafeMethod reports whether the HTTP method mutates state and therefore
// warrants a CSRF Origin check.
func isUnsafeMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// isSameOriginRequest checks whether the Origin points back at our own
// listener. Used in AUTH_TOKEN-without-allowlist mode.
func isSameOriginRequest(r *http.Request, origin string) bool {
	if r.Host == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	originHost := normalisedHost(u.Scheme, u.Host)
	if originHost == "" {
		return false
	}
	// Normalise r.Host with the scheme the request actually arrived on —
	// borrowing the Origin's scheme would substitute the wrong default
	// port whenever the two differ (e.g. an http:// Origin against a
	// TLS-terminated listener).
	reqScheme := "http"
	if isSecureRequest(r) {
		reqScheme = "https"
	}
	reqHost := normalisedHost(reqScheme, r.Host)
	return originHost == reqHost
}

// normalisedHost returns host:port, substituting the scheme's default port
// when omitted. Returns "" for unknown schemes.
func normalisedHost(scheme, host string) string {
	if !strings.Contains(host, ":") {
		switch scheme {
		case "http":
			return host + ":80"
		case "https":
			return host + ":443"
		default:
			return ""
		}
	}
	return host
}

// authMiddleware enforces a bearer token on /api/* when AUTH_TOKEN is set.
// Probes and auth endpoints bypass the check.
func (app *App) authMiddleware(next http.Handler) http.Handler {
	publicAPI := map[string]bool{
		"/api/translations":        true,
		"/api/auth/required":       true,
		"/api/auth/login":          true, // needed to bootstrap the session
		"/api/auth/session/logout": true, // idempotent; clears the cookie
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gated := strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/debug/")
		if app.authToken == "" || !gated {
			next.ServeHTTP(w, r)
			return
		}
		if publicAPI[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		// CORS preflight passes; the real request re-auths.
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		// Three accepted credentials, in order of preference:
		//   1. The httpOnly session cookie (preferred for browser SPAs).
		//   2. `Authorization: Bearer <token>` (CLI / integration callers).
		//   3. `?token=` query string (EventSource fallback; deprecated).
		if c, err := r.Cookie(sessionCookieName); err == nil && app.validSessionCookie(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(app.authToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="zapcast"`)
			jsonError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeadersMiddleware applies security headers to every route.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		applySecurityHeaders(w)
		next.ServeHTTP(w, r)
	})
}

// staticETagMiddleware attaches ETag and short-circuits matching
// If-None-Match into a 304.
func staticETagMiddleware(next http.Handler, etag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		next.ServeHTTP(w, r)
	})
}

// cspPolicy mirrors the <meta http-equiv> tag in index.html and adds
// frame-ancestors (which meta CSP cannot express). Served as a header so
// the policy also covers non-HTML responses and error pages. Keep the two
// in sync — browsers enforce the intersection.
const cspPolicy = "default-src 'self'; base-uri 'self'; object-src 'none'; " +
	"script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; " +
	"manifest-src 'self'; worker-src 'self'; form-action 'self'; " +
	"frame-ancestors 'none'"

// applySecurityHeaders writes response-only security headers.
func applySecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", cspPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=(), payment=(), usb=()")
	h.Set("X-Frame-Options", "DENY")
}

// computeAssetETag walks the embedded web FS under root and returns a strong
// ETag. root must match the subtree actually served (webSubdir) so the tag
// reflects the bytes on the wire — in -tags prod that's web/dist, not web.
func computeAssetETag(efs embed.FS, root string) string {
	h := sha256.New()
	_ = fs.WalkDir(efs, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, rerr := efs.ReadFile(path)
		if rerr != nil {
			return nil
		}
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write(b)
		return nil
	})
	sum := h.Sum(nil)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}
