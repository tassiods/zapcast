package server

import (
	"net/http/httptest"
	"testing"
)

func TestSecurityTrustedProxyRejectsMalformedForwarding(t *testing.T) {
	old := trustedProxies
	defer func() { trustedProxies = old }()
	trustedProxies = parseTrustedProxies(testLogger, "192.0.2.5")
	r := httptest.NewRequest("GET", "http://internal.invalid/api/status", nil)
	r.RemoteAddr = "192.0.2.5:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	for _, host := range []string{"audit.invalid?", "audit.invalid#", "audit.invalid,evil.invalid", "user@audit.invalid", "audit.invalid/path"} {
		r.Header.Set("X-Forwarded-Host", host)
		if isSameOriginRequest(r, "https://audit.invalid") {
			t.Errorf("malformed forwarded host accepted: %s", host)
		}
	}
	r.Header.Set("X-Forwarded-Host", "audit.invalid")
	r.Header.Set("X-Forwarded-Proto", "https,http")
	if isSameOriginRequest(r, "https://audit.invalid") || isSecureRequest(r) {
		t.Fatal("ambiguous proxy scheme accepted")
	}
}

func TestSecurityProxyModeRejectsCrossOriginReads(t *testing.T) {
	app := &App{authMode: "proxy"}
	r := httptest.NewRequest("GET", "https://audit.invalid/api/contacts", nil)
	r.Header.Set("Origin", "https://other.invalid")
	w := httptest.NewRecorder()
	app.corsMiddleware(passHandler).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("proxy-mode API readable cross-origin")
	}
	r.Header.Set("Origin", "https://audit.invalid")
	w = httptest.NewRecorder()
	app.corsMiddleware(passHandler).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("same-origin proxy client rejected")
	}
}

func TestSecurityConfiguredOriginComparesEffectivePorts(t *testing.T) {
	app := &App{authToken: "token", allowedOrigin: "https://audit.invalid"}
	r := httptest.NewRequest("GET", "http://internal.invalid/api/status", nil)
	r.Header.Set("Origin", "https://audit.invalid:443")
	w := httptest.NewRecorder()
	app.corsMiddleware(passHandler).ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != r.Header.Get("Origin") || !app.sseOriginAllowed(r) {
		t.Fatal("equivalent configured origin rejected")
	}
	r.Header.Set("Origin", "http://audit.invalid:443")
	w = httptest.NewRecorder()
	app.corsMiddleware(passHandler).ServeHTTP(w, r)
	if w.Code != 403 || app.sseOriginAllowed(r) {
		t.Fatal("different configured scheme accepted")
	}
}

func TestSecurityOriginUsesCanonicalStrictTuple(t *testing.T) {
	r := httptest.NewRequest("POST", "https://audit.invalid/api/import", nil)
	for _, origin := range []string{"https://audit.invalid:443", "https://AUDIT.INVALID"} {
		if !isSameOriginRequest(r, origin) {
			t.Errorf("equivalent origin rejected: %s", origin)
		}
	}
	for _, origin := range []string{"https://audit.invalid/path", "https://user@audit.invalid", "https://audit.invalid?token=x", "https://audit.invalid#x", "https://audit.invalid:444", "ftp://audit.invalid:443", "null", "https://audit.invalid:"} {
		if isSameOriginRequest(r, origin) {
			t.Errorf("invalid/different origin accepted: %s", origin)
		}
	}
	v6 := httptest.NewRequest("GET", "http://[::1]/api/status", nil)
	if !isSameOriginRequest(v6, "http://[::1]:80") {
		t.Error("IPv6 effective port rejected")
	}
}

func TestSecurityForwardedHeadersRequireTrustedPeer(t *testing.T) {
	old := trustedProxies
	defer func() { trustedProxies = old }()
	trustedProxies = nil
	r := httptest.NewRequest("POST", "http://internal.invalid/api/auth/login", nil)
	r.RemoteAddr = "192.0.2.5:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "public.invalid")
	if isSecureRequest(r) {
		t.Fatal("untrusted X-Forwarded-Proto enabled Secure")
	}
	if isSameOriginRequest(r, "https://public.invalid") {
		t.Fatal("untrusted headers changed origin")
	}
	trustedProxies = parseTrustedProxies(testLogger, "192.0.2.5")
	if !isSecureRequest(r) || !isSameOriginRequest(r, "https://public.invalid:443") {
		t.Fatal("configured proxy origin rejected")
	}
}

func TestSecurityOriginRejectsExplicitPortSchemeMismatch(t *testing.T) {
	r := httptest.NewRequest("POST", "https://audit.invalid:443/api/import", nil)
	if isSameOriginRequest(r, "http://audit.invalid:443") {
		t.Fatal("scheme mismatch accepted with explicit port")
	}
}
