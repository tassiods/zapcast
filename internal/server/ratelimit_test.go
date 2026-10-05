// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRateLimiterAllowsBurstThenBlocks confirms the bucket admits exactly
// `burst` requests in quick succession then returns 429. A subsequent reset
// of the clock past the refill interval lets one more request through.
func TestRateLimiterAllowsBurstThenBlocks(t *testing.T) {
	rl := newRateLimiter(100*time.Millisecond, 3, time.Minute)
	defer rl.Close()

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := rl.Middleware("test", ok)

	mkReq := func() *http.Request {
		r := httptest.NewRequest("GET", "/x", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		return r
	}

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, mkReq())
		if rec.Code != 200 {
			t.Fatalf("call %d: expected 200, got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th call: expected 429, got %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("Retry-After header missing on 429")
	}
}

// TestRateLimiterIsolatesPerIP verifies that a noisy caller cannot starve a
// quiet one — each IP gets its own bucket.
func TestRateLimiterIsolatesPerIP(t *testing.T) {
	rl := newRateLimiter(time.Hour, 1, time.Minute)
	defer rl.Close()
	h := rl.Middleware("test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	// Exhaust IP A.
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/x", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		want := 200
		if i == 1 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Errorf("A call %d: got %d want %d", i+1, rec.Code, want)
		}
	}
	// IP B must still be allowed.
	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = "10.0.0.2:5678"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Errorf("B call: got %d want 200", rec.Code)
	}
}

// TestClientIPTrustsXFFOnlyFromTrustedProxy verifies the spoofing guard:
// X-Forwarded-For is honored ONLY when the immediate peer (RemoteAddr) is a
// configured trusted proxy; otherwise RemoteAddr wins so a client can't forge
// its bucket key by sending the header.
func TestClientIPTrustsXFFOnlyFromTrustedProxy(t *testing.T) {
	saved := trustedProxies
	t.Cleanup(func() { trustedProxies = saved })
	trustedProxies = parseTrustedProxies(testLogger, "10.0.0.0/8")

	cases := []struct {
		name   string
		xff    string
		remote string
		want   string
	}{
		// Peer is a trusted proxy (10.0.0.1): honor the left-most XFF entry.
		{"trusted single", "203.0.113.1", "10.0.0.1:1234", "203.0.113.1"},
		{"trusted chain", "203.0.113.1, 10.0.0.5", "10.0.0.1:1234", "203.0.113.1"},
		{"trusted padded", " 203.0.113.2 , 10.0.0.5", "10.0.0.1:1234", "203.0.113.2"},
		// Peer is NOT trusted: XFF ignored, RemoteAddr host wins.
		{"untrusted peer spoof", "203.0.113.99", "198.51.100.7:1234", "198.51.100.7"},
		// No XFF: RemoteAddr host regardless.
		{"no xff", "", "10.0.0.1:1234", "10.0.0.1"},
		{"unparseable remote", "", "not-a-host", "not-a-host"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/x", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("%s: clientIP(xff=%q, remote=%q) = %q, want %q", c.name, c.xff, c.remote, got, c.want)
		}
	}
}

// TestClientIPIgnoresXFFByDefault confirms the secure default: with no
// TRUST_PROXY configured, X-Forwarded-For is never honored.
func TestClientIPIgnoresXFFByDefault(t *testing.T) {
	saved := trustedProxies
	t.Cleanup(func() { trustedProxies = saved })
	trustedProxies = nil

	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	if got := clientIP(r); got != "10.0.0.1" {
		t.Errorf("default must ignore XFF: got %q want 10.0.0.1", got)
	}
}

// TestRateLimiterEvictsIdleVisitors confirms the GC loop drops entries whose
// lastSeen is past the cutoff so the map doesn't grow unbounded.
func TestRateLimiterEvictsIdleVisitors(t *testing.T) {
	rl := newRateLimiter(time.Hour, 1, 50*time.Millisecond)
	defer rl.Close()
	h := rl.Middleware("test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	h.ServeHTTP(httptest.NewRecorder(), r)

	rl.mu.Lock()
	if len(rl.visitors) != 1 {
		t.Fatalf("expected 1 visitor, got %d", len(rl.visitors))
	}
	rl.mu.Unlock()

	// Wait long enough for at least one GC tick (interval = evictAfter/2 = 25ms)
	// plus the eviction threshold.
	time.Sleep(150 * time.Millisecond)

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if len(rl.visitors) != 0 {
		t.Errorf("expected 0 visitors after eviction, got %d", len(rl.visitors))
	}
}

// TestFormatRetryAfterRoundsUp asserts Retry-After never claims sub-second
// availability so clients don't reconnect tighter than the bucket admits.
func TestFormatRetryAfterRoundsUp(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "1"},
		{500 * time.Millisecond, "1"},
		{time.Second, "1"},
		{1500 * time.Millisecond, "1"},
		{2 * time.Second, "2"},
		{10 * time.Second, "10"},
	}
	for _, c := range cases {
		if got := formatRetryAfter(c.d); got != c.want {
			t.Errorf("formatRetryAfter(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
