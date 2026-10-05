// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// testLogger is the discard logger every test that needs a *slog.Logger
// can pass through to helpers that take one. Parallel tests can all share
// it because slog.Logger is goroutine-safe and we never reconfigure it.
var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// TestMain runs the package suite. We no longer set a global logger — each
// test that needs one uses testLogger.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"localhost", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"192.168.1.10", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopback(c.host); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestAssessAuthTokenStrength(t *testing.T) {
	cases := []struct {
		name     string
		token    string
		wantWeak bool
	}{
		{"empty stays empty (caller handles)", "", true},
		{"too short", "abc123", true},
		{"well-known weak", "password", true},
		{"only lowercase, 32 chars", strings.Repeat("a", 32), true},
		{"only digits, 32 chars", strings.Repeat("9", 32), true},
		{"32 mixed lower + digits", "abcdefghij1234567890klmnopqrstuv", false},
		{"32 mixed plus symbols", "Aa1!Bb2@Cc3#Dd4$Ee5%Ff6^Gg7&Hh8*", false},
		{"64 random-looking", "x9K2pQ7tR4mZ8aB3yL6jE1uF5sN0wH7cD2eG4iV9oU8nT5rY1pL6jK3hM0xZ9aQ7", false},
	}
	for _, c := range cases {
		got := assessAuthTokenStrength(c.token)
		if (got != "") != c.wantWeak {
			t.Errorf("%s: assessAuthTokenStrength(%q) = %q, wantWeak=%v", c.name, c.token, got, c.wantWeak)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"5511999998888", "5511*****8888"},
		{"15551234567", "1555***4567"},
		{"123456789", "1234*6789"}, // 9 chars → 1 masked
		{"12345678", "12345678"},   // ≤8 chars → unchanged
		{"1234567", "1234567"},
		{"", ""},
	}
	for _, c := range cases {
		if got := maskPhone(c.in); got != c.want {
			t.Errorf("maskPhone(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseBroadcastDelay(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 500 * time.Millisecond},    // default
		{"250", 250 * time.Millisecond}, // valid value
		{"50", 100 * time.Millisecond},  // clamped to lower bound
		{"60000", 30 * time.Second},     // clamped to upper bound
		{"abc", 500 * time.Millisecond}, // parse error → default
	}
	for _, c := range cases {
		if got := parseBroadcastDelay(testLogger, c.in); got != c.want {
			t.Errorf("parseBroadcastDelay(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseBroadcastJitter(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 0.3},     // default
		{"0", 0},      // disabled
		{"0.5", 0.5},  // valid
		{"1.5", 0.9},  // clamped upper
		{"-0.1", 0},   // clamped lower
		{"junk", 0.3}, // parse error → default
	}
	for _, c := range cases {
		if got := parseBroadcastJitter(testLogger, c.in); got != c.want {
			t.Errorf("parseBroadcastJitter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseBroadcastHourlyCap(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},      // default unlimited
		{"500", 500}, // valid
		{"-1", 0},    // negative → unlimited
		{"junk", 0},  // parse error → unlimited
	}
	for _, c := range cases {
		if got := parseBroadcastHourlyCap(testLogger, c.in); got != c.want {
			t.Errorf("parseBroadcastHourlyCap(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestClassifyMedia(t *testing.T) {
	cases := []struct {
		mime, wantKind string
		wantOK         bool
	}{
		{"image/png", "image", true},
		{"image/jpeg", "image", true},
		{"image/webp", "image", true},
		{"video/mp4", "video", true},
		{"audio/ogg", "audio", true},
		{"audio/mpeg", "audio", true},
		{"application/pdf", "document", true},
		{"text/plain", "document", true},
		{"application/octet-stream", "document", true},
		{"", "", false},
	}
	for _, c := range cases {
		_, kind, ok := classifyMedia(c.mime)
		if ok != c.wantOK || kind != c.wantKind {
			t.Errorf("classifyMedia(%q) = (%v, %q), want (%v, %q)",
				c.mime, ok, kind, c.wantOK, c.wantKind)
		}
	}
}

func TestRenderTemplateShortCircuit(t *testing.T) {
	c := Contact{Name: "Alice", Phone: "5511999998888"}
	// No `{` markers — should return identical string without touching the Replacer path.
	in := "no markers here"
	if got := renderTemplate(in, c, nil); got != in {
		t.Errorf("renderTemplate short-circuit failed: got %q, want %q", got, in)
	}

	// Builtins only.
	out := renderTemplate("Hi {name} ({phone})", c, nil)
	if out != "Hi Alice (5511999998888)" {
		t.Errorf("renderTemplate builtins: got %q", out)
	}

	// Custom var that shadows nothing.
	out = renderTemplate("Order {id}", c, map[string]string{"id": "42"})
	if out != "Order 42" {
		t.Errorf("renderTemplate custom: got %q", out)
	}

	// Unknown placeholder stays literal — matches backend behavior.
	out = renderTemplate("{unknown}", c, nil)
	if out != "{unknown}" {
		t.Errorf("renderTemplate unknown: got %q", out)
	}
}
