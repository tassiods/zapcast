// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"net/http/httptest"
	"testing"
)

// --- session HMAC key persistence ---

func TestLoadOrCreateSessionKeyPersists(t *testing.T) {
	dir := t.TempDir()
	first := loadOrCreateSessionKey(testLogger, dir)
	if len(first) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(first))
	}
	second := loadOrCreateSessionKey(testLogger, dir)
	if !bytes.Equal(first, second) {
		t.Errorf("key must survive reloads from the same store dir")
	}
}

func TestLoadOrCreateSessionKeyNoDir(t *testing.T) {
	a := loadOrCreateSessionKey(testLogger, "")
	b := loadOrCreateSessionKey(testLogger, "")
	if len(a) != 32 || len(b) != 32 {
		t.Fatalf("expected 32-byte keys, got %d/%d", len(a), len(b))
	}
	if bytes.Equal(a, b) {
		t.Errorf("dirless keys must be independent random values")
	}
}

func TestSessionCookieSurvivesAppRestart(t *testing.T) {
	dir := t.TempDir()
	mkApp := func() *App {
		app := newTestApp(t)
		app.authToken = "super-secret-token-0123456789abcdef"
		app.sessionKeyDir = dir
		return app
	}
	cookie := mkApp().issueSession()
	if !mkApp().validSessionCookie(cookie) {
		t.Errorf("cookie minted before 'restart' must still validate")
	}
	// A tampered payload (same shape, different nonce) must not validate
	// against the persisted key — the MAC covers the whole payload.
	if mkApp().validSessionCookie("deadbeef.9999999999.0000") {
		t.Errorf("forged cookie must not validate")
	}
}

// --- isSameOriginRequest scheme normalisation ---

func TestIsSameOriginRequestMatchesPlainHTTP(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.com/api/status", nil)
	r.Host = "example.com"
	if !isSameOriginRequest(r, "http://example.com") {
		t.Errorf("same host + same scheme must match")
	}
}

func TestIsSameOriginRequestSchemeMismatch(t *testing.T) {
	// https Origin against a plain-HTTP listener: default ports differ
	// (443 vs 80), so this is NOT the same origin. The old code borrowed
	// the Origin's scheme for both sides and wrongly matched.
	r := httptest.NewRequest("GET", "http://example.com/api/status", nil)
	r.Host = "example.com"
	if isSameOriginRequest(r, "https://example.com") {
		t.Errorf("https origin must not match a plain-http listener on default ports")
	}
}

func TestIsSameOriginRequestBehindTLSProxy(t *testing.T) {
	old := trustedProxies
	defer func() { trustedProxies = old }()
	trustedProxies = parseTrustedProxies(testLogger, "192.0.2.1")
	r := httptest.NewRequest("GET", "http://example.com/api/status", nil)
	r.Host = "example.com"
	r.Header.Set("X-Forwarded-Proto", "https")
	if !isSameOriginRequest(r, "https://example.com") {
		t.Errorf("https origin must match when the proxy says the request is https")
	}
}
