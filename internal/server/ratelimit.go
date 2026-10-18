// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// rateLimitVisitor tracks a per-IP token bucket and the last time it was used,
// so the cleanup loop can evict idle entries without growing the map forever.
type rateLimitVisitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// rateLimiter implements per-IP token-bucket throttling with GC of idle
// visitors.
type rateLimiter struct {
	r          rate.Limit
	burst      int
	mu         sync.Mutex
	visitors   map[string]*rateLimitVisitor
	evictAfter time.Duration
	stop       chan struct{}
	stopOnce   sync.Once
	timeNow    func() time.Time // injection point for tests
	keyForReq  func(r *http.Request) string
}

// newRateLimiter returns a limiter that allows `every` per request with a
// burst of `burst`. Idle visitors are evicted after evictAfter.
func newRateLimiter(every time.Duration, burst int, evictAfter time.Duration) *rateLimiter {
	if every <= 0 {
		every = time.Second
	}
	if burst < 1 {
		burst = 1
	}
	if evictAfter <= 0 {
		evictAfter = 10 * time.Minute
	}
	rl := &rateLimiter{
		r:          rate.Every(every),
		burst:      burst,
		visitors:   make(map[string]*rateLimitVisitor),
		evictAfter: evictAfter,
		stop:       make(chan struct{}),
		timeNow:    time.Now,
		keyForReq:  clientIP,
	}
	go rl.gcLoop()
	return rl
}

// gcLoop periodically discards idle visitor entries.
func (rl *rateLimiter) gcLoop() {
	interval := rl.evictAfter / 2
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case <-t.C:
			rl.evictIdle()
		}
	}
}

// evictIdle drops visitors whose lastSeen is past the threshold. Held under
// the same mutex as allow() so a concurrent admit can't observe a half-deleted
// entry.
func (rl *rateLimiter) evictIdle() {
	cutoff := rl.timeNow().Add(-rl.evictAfter)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k, v := range rl.visitors {
		if v.lastSeen.Before(cutoff) {
			delete(rl.visitors, k)
		}
	}
}

// Close stops the GC goroutine. Safe to call multiple times.
func (rl *rateLimiter) Close() {
	rl.stopOnce.Do(func() { close(rl.stop) })
}

// allow reports whether the visitor identified by key may proceed. Updates
// lastSeen on every call so live visitors are never evicted mid-session.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	v, ok := rl.visitors[key]
	if !ok {
		v = &rateLimitVisitor{limiter: rate.NewLimiter(rl.r, rl.burst)}
		rl.visitors[key] = v
	}
	v.lastSeen = rl.timeNow()
	return v.limiter.Allow()
}

// Middleware wraps next with rate-limit enforcement. Returns 429 with
// Retry-After when the bucket is empty.
func (rl *rateLimiter) Middleware(label string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := rl.keyForReq(r)
		if !rl.allow(key) {
			retry := time.Duration(float64(time.Second) / float64(rl.r))
			w.Header().Set("Retry-After", formatRetryAfter(retry))
			w.Header().Set("X-RateLimit-Remaining", "0")
			metricRateLimitBlocks.Add(label, 1)
			jsonErrorLocalized(w, r, http.StatusTooManyRequests, "err_rate_limited")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// formatRetryAfter renders the integer-seconds form of Retry-After, rounding
// up so the client never retries too early.
func formatRetryAfter(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}

// trustedProxies is the set of immediate-peer CIDRs whose X-Forwarded-For
// header we honor. Set once at boot from TRUST_PROXY (see parseTrustedProxies).
// Empty by default — an unset TRUST_PROXY means XFF is NOT trusted, so a
// client cannot forge its rate-limit bucket / log IP by sending the header.
var trustedProxies []*net.IPNet

// clientIP extracts the caller IP used as the rate-limit bucket key and the
// logged remote address. It only honors X-Forwarded-For when the immediate
// peer (RemoteAddr) is a configured trusted proxy; otherwise it returns
// RemoteAddr. This closes the header-spoofing bypass: without TRUST_PROXY an
// attacker sending `X-Forwarded-For: <random>` gets no new bucket.
func clientIP(r *http.Request) string {
	host := remoteHost(r)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && peerIsTrusted(host) {
		// Left-most entry is the original client as seen by the first proxy
		// (assuming the whole forwarding chain is trusted, the standard
		// single-proxy deployment).
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return host
}

// remoteHost strips the port from RemoteAddr, falling back to the raw value.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// peerIsTrusted reports whether host falls inside a configured trusted-proxy
// CIDR.
func peerIsTrusted(host string) bool {
	if len(trustedProxies) == 0 {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseTrustedProxies reads TRUST_PROXY: a comma-separated list of IPs or
// CIDRs whose X-Forwarded-For header clientIP should honor. Bare IPs are
// treated as /32 (or /128). Invalid entries are logged and skipped.
func parseTrustedProxies(lg *slog.Logger, raw string) []*net.IPNet {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			if strings.Contains(part, ":") {
				part += "/128"
			} else {
				part += "/32"
			}
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			if lg != nil {
				lg.Warn("invalid TRUST_PROXY entry, skipping", "entry", part, "err", err)
			}
			continue
		}
		out = append(out, n)
	}
	return out
}
