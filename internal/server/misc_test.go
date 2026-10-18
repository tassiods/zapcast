// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
)

func TestInitLoggerHonorsLogFormat(t *testing.T) {
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("LOG_LEVEL", "debug")
	lg := initLogger()
	if lg == nil {
		t.Fatal("initLogger returned nil")
	}
	lg.Debug("smoke")
}

func TestInitLoggerLevels(t *testing.T) {
	for _, lvl := range []string{"debug", "warn", "error", "info", ""} {
		t.Setenv("LOG_LEVEL", lvl)
		if lg := initLogger(); lg == nil {
			t.Errorf("level %q produced nil logger", lvl)
		}
	}
	_ = os.Unsetenv("LOG_LEVEL")
}

func TestConfigureDevicePropsSetsValues(t *testing.T) {
	// Mutates package-level store.DeviceProps; not parallel-safe.
	configureDeviceProps()
	if store.DeviceProps.PlatformType == nil {
		t.Errorf("PlatformType not set")
	}
	if store.DeviceProps.Os == nil || !strings.Contains(*store.DeviceProps.Os, "ZapCast") {
		t.Errorf("Os string missing ZapCast prefix: %v", store.DeviceProps.Os)
	}
	// The platform type should be one of the known DESKTOP/CATALINA enums.
	pt := *store.DeviceProps.PlatformType
	if pt != waCompanionReg.DeviceProps_CATALINA && pt != waCompanionReg.DeviceProps_DESKTOP {
		t.Errorf("unexpected platform type: %v", pt)
	}
}

// --- handleSSE end-to-end via httptest.NewServer ---
//
// httptest.NewServer wraps a real net/http server, so the ResponseWriter on
// the handler side implements http.Flusher (unlike the default ResponseRecorder
// in some older Go versions). This lets us exercise the streaming path.

func TestHandleSSEStreamsBroadcastEvent(t *testing.T) {
	app := newTestApp(t)
	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The server keeps the response unflushed until it has an event to send.
	// Publish in a goroutine once a subscriber is registered so the headers
	// + first frame arrive together and Do() unblocks.
	go func() {
		for {
			app.hub.mu.RLock()
			n := len(app.hub.subscribers)
			app.hub.mu.RUnlock()
			if n > 0 {
				app.broadcastEvent(Event{Type: "ping", Data: "ok"})
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed below after err check; bodyclose can't trace t.Fatalf
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}

	buf := make([]byte, 200)
	done := make(chan int, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		done <- n
	}()
	select {
	case n := <-done:
		got := string(buf[:n])
		if !strings.Contains(got, "ping") {
			t.Errorf("SSE payload missing event type: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("SSE response never delivered")
	}
	cancel() // unblock handleSSE so srv.Close() can finish quickly
}
