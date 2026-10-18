// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStaticETagMiddlewareAttachesETag confirms the middleware sets the ETag
// and Cache-Control headers on every passthrough response.
func TestStaticETagMiddlewareAttachesETag(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})
	h := staticETagMiddleware(ok, `"deadbeef00000000"`)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/index.html", nil))
	if got := rec.Header().Get("ETag"); got != `"deadbeef00000000"` {
		t.Errorf("ETag header = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache, must-revalidate" {
		t.Errorf("Cache-Control = %q", got)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("body lost: %q", rec.Body.String())
	}
}

// TestStaticETagMiddleware304OnMatch confirms a matching If-None-Match short-
// circuits to 304 without invoking the inner handler.
func TestStaticETagMiddleware304OnMatch(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte("body"))
	})
	h := staticETagMiddleware(inner, `"abc"`)

	r := httptest.NewRequest("GET", "/index.html", nil)
	r.Header.Set("If-None-Match", `"abc"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotModified {
		t.Errorf("expected 304, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 must not have body, got %q", rec.Body.String())
	}
	if called {
		t.Error("inner handler must not run on If-None-Match hit")
	}
}

// TestComputeAssetETagStable confirms two calls over the same embed FS return
// the identical tag, so the value is safe to compute once at startup.
func TestComputeAssetETagStable(t *testing.T) {
	a := computeAssetETag(webFS, webSubdir)
	b := computeAssetETag(webFS, webSubdir)
	if a != b {
		t.Errorf("etag not stable: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, `"`) || !strings.HasSuffix(a, `"`) {
		t.Errorf("etag must be quoted per RFC 7232: %q", a)
	}
	if len(a) < 4 {
		t.Errorf("etag too short: %q", a)
	}
}
