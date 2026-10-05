// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Environment-driven configuration knobs.

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Boundaries for the broadcast cadence knobs.
const (
	defaultBroadcastDelay  = 500 * time.Millisecond
	minBroadcastDelay      = 100 * time.Millisecond
	maxBroadcastDelay      = 30 * time.Second
	defaultBroadcastJitter = 0.3
	maxBroadcastJitter     = 0.9
)

// parseBroadcastDelay reads the per-message broadcast delay from
// BROADCAST_DELAY_MS, clamping to [100ms, 30s]. Default 500ms.
func parseBroadcastDelay(lg *slog.Logger, s string) time.Duration {
	if s == "" {
		return defaultBroadcastDelay
	}
	ms, err := time.ParseDuration(s + "ms")
	if err != nil {
		lg.Warn("invalid BROADCAST_DELAY_MS, using default", "value", s)
		return defaultBroadcastDelay
	}
	if ms < minBroadcastDelay {
		lg.Warn("BROADCAST_DELAY_MS below minimum, clamping",
			"requested", ms, "clamped", minBroadcastDelay)
		return minBroadcastDelay
	}
	if ms > maxBroadcastDelay {
		lg.Warn("BROADCAST_DELAY_MS above maximum, clamping",
			"requested", ms, "clamped", maxBroadcastDelay)
		return maxBroadcastDelay
	}
	return ms
}

// parseBroadcastJitter reads BROADCAST_DELAY_JITTER as a fraction (0.0–0.9).
// Default ±30%. 0 = constant cadence.
func parseBroadcastJitter(lg *slog.Logger, s string) float64 {
	if s == "" {
		return defaultBroadcastJitter
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		lg.Warn("invalid BROADCAST_DELAY_JITTER, using default", "value", s)
		return defaultBroadcastJitter
	}
	if f < 0 {
		lg.Warn("BROADCAST_DELAY_JITTER negative, clamping to 0", "value", f)
		return 0
	}
	if f > maxBroadcastJitter {
		lg.Warn("BROADCAST_DELAY_JITTER above maximum, clamping",
			"requested", f, "clamped", maxBroadcastJitter)
		return maxBroadcastJitter
	}
	return f
}

// parseBroadcastHourlyCap reads BROADCAST_HOURLY_CAP. When > 0, the broadcast
// goroutine pauses once it has sent N messages within the current rolling
// hour and resumes when the window resets. Default 0 = unlimited (rely
// solely on per-message delay). Sensible values: 300–600 for conservative
// volume.
func parseBroadcastHourlyCap(lg *slog.Logger, s string) int {
	if s == "" {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		lg.Warn("invalid BROADCAST_HOURLY_CAP, using 0 (unlimited)", "value", s)
		return 0
	}
	if n < 0 {
		return 0
	}
	return n
}

// isLoopback reports whether host binds the listener to localhost only.
func isLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// commonWeakTokens enumerates strings an attacker would try first against a
// bearer-token check. Lower-cased so the caller normalises before lookup.
var commonWeakTokens = map[string]struct{}{
	"password": {}, "passw0rd": {}, "secret": {}, "token": {}, "admin": {},
	"changeme": {}, "letmein": {}, "test": {}, "qwerty": {}, "default": {},
	"zapcast": {}, "12345678": {}, "11111111": {}, "00000000": {},
}

// assessAuthTokenStrength returns a non-empty reason when the token is
// obviously weak. Empty return means "looks fine to us" — a heuristic, not a
// substitute for a real secrets manager.
//
// We flag three cheap classes of failure: short length, low character
// diversity, and well-known dictionary values. The 32-character floor aligns
// with SECURITY.md and gives roughly 128 bits of entropy when paired with a
// random source.
func assessAuthTokenStrength(token string) string {
	const minLen = 32
	if len(token) < minLen {
		return fmt.Sprintf("length %d < %d", len(token), minLen)
	}
	if _, hit := commonWeakTokens[strings.ToLower(token)]; hit {
		return "matches a well-known weak value"
	}
	var lower, upper, digit, other int
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z':
			lower++
		case r >= 'A' && r <= 'Z':
			upper++
		case r >= '0' && r <= '9':
			digit++
		default:
			other++
		}
	}
	classes := 0
	for _, c := range []int{lower, upper, digit, other} {
		if c > 0 {
			classes++
		}
	}
	if classes < 2 {
		return "uses only one character class"
	}
	return ""
}
