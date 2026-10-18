// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Server-Sent Events fan-out. SSEHub keeps subscriber channels and
// broadcasts every event without blocking. Slow consumers get dropped.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// defaultSSEBuffer is the per-subscriber channel depth used when SSE_BUFFER
// is unset. 20 covers the worst-case burst observed in practice (a 100-row
// broadcast progress fan-out while a sluggish browser tab catches up).
const defaultSSEBuffer = 20

// Event is the payload pushed to every SSE listener.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// SSEHub is a goroutine-safe pub/sub for Event values.
type SSEHub struct {
	logger      *slog.Logger
	bufferSize  int
	mu          sync.RWMutex
	subscribers map[chan Event]struct{}
}

// NewSSEHub returns an empty hub. Buffer size from SSE_BUFFER or default.
func NewSSEHub(lg *slog.Logger) *SSEHub {
	return &SSEHub{
		logger:      lg,
		bufferSize:  parseSSEBuffer(lg, os.Getenv("SSE_BUFFER")),
		subscribers: make(map[chan Event]struct{}),
	}
}

// parseSSEBuffer reads the SSE_BUFFER env knob, clamps it to a sane range
// and logs the chosen value so operators see the effective config at boot.
func parseSSEBuffer(lg *slog.Logger, raw string) int {
	if raw == "" {
		return defaultSSEBuffer
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		if lg != nil {
			lg.Warn("invalid SSE_BUFFER, using default", "raw", raw, "default", defaultSSEBuffer)
		}
		return defaultSSEBuffer
	}
	if n > 1000 {
		n = 1000
	}
	return n
}

// Subscribe registers a new listener and returns its channel.
func (h *SSEHub) Subscribe() chan Event {
	ch := make(chan Event, h.bufferSize)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes the channel from the subscriber set under the write
// lock. It deliberately does NOT close the channel: Broadcast snapshots the
// subscriber slice and then sends lock-free, so a close here could race with
// an in-flight send and panic ("send on closed channel" — which select's
// default case does NOT rescue, because a send to a closed channel is
// "ready" and gets selected). Once removed from the map no new sends target
// this channel; the reader goroutine exits on ctx.Done and the buffered
// channel is reclaimed by GC.
func (h *SSEHub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, ch)
}

// Broadcast fans out to every subscriber. Snapshot under RLock, release
// before sending, so a slow consumer can't block Subscribe/Unsubscribe.
// Safe lock-free because subscriber channels are never closed (see
// Unsubscribe) — a send either buffers, or hits default and is dropped.
func (h *SSEHub) Broadcast(evt Event) {
	h.mu.RLock()
	chans := make([]chan Event, 0, len(h.subscribers))
	for ch := range h.subscribers {
		chans = append(chans, ch)
	}
	h.mu.RUnlock()

	dropped := 0
	for _, ch := range chans {
		select {
		case ch <- evt:
		default:
			dropped++
		}
	}
	if dropped > 0 {
		if h.logger != nil {
			h.logger.Warn("sse drop", "type", evt.Type, "dropped", dropped, "subscribers", len(chans))
		}
		metricSSEDrops.Add(int64(dropped))
	}
}

// Count returns the number of live subscribers. Useful for metrics; the
// production handlers never look at it.
func (h *SSEHub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}

// --- Wire layer ---

// eventSeq is incremented per outbound frame purely to give each frame a
// monotonic id. Note: the server does not implement Last-Event-ID replay —
// there is no per-client buffer, so events during a reconnect gap are lost.
var eventSeq atomic.Uint64

// sseWriteTimeout bounds a single frame write. The server runs with
// WriteTimeout:0 (streaming), so without a per-write deadline a stalled
// client could block the handler goroutine indefinitely.
const sseWriteTimeout = 10 * time.Second

// setSSEWriteDeadline arms a per-write deadline via the ResponseController.
// Socket errors terminate the stream. Unsupported in-memory writers are
// explicitly logged; production net/http sockets support write deadlines.
func setSSEWriteDeadline(w http.ResponseWriter) error {
	rc := http.NewResponseController(w)
	err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	if errors.Is(err, http.ErrNotSupported) {
		// In-memory test writers lack deadlines; real net/http sockets support them.
		slog.Debug("SSE writer does not support write deadlines")
		return nil
	}
	return err
}

// handleSSE is the /api/events long poll.
func (app *App) handleSSE(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, authorized := app.authorizedStreamContext(r)
	defer cancel()
	if !authorized() {
		jsonError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}
	if !app.sseOriginAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	flusher, ok := setupSSE(w, r)
	if !ok {
		return
	}

	ch := app.hub.Subscribe()
	metricSSESubscribers.Set(int64(app.hub.Count()))
	defer func() {
		app.hub.Unsubscribe(ch)
		metricSSESubscribers.Set(int64(app.hub.Count()))
	}()

	// Keepalive: an idle stream sends a comment line periodically so a client
	// that has gone away (or a dead intermediary) surfaces as a write error
	// and tears the goroutine down, instead of a WriteTimeout:0 handler
	// wedging forever on a socket nobody is draining.
	const keepaliveEvery = 25 * time.Second
	ka := time.NewTicker(keepaliveEvery)
	defer ka.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ka.C:
			if !authorized() {
				return
			}
			if !writeSSEComment(w, flusher) {
				return
			}
		case evt := <-ch:
			if !authorized() {
				return
			}
			if !writeSSEFrame(w, flusher, evt) {
				return
			}
		}
	}
}

// writeSSEComment emits a keepalive comment frame with a bounded write
// deadline. Returns false when the write fails so the caller can stop.
func writeSSEComment(w http.ResponseWriter, flusher http.Flusher) bool {
	if setSSEWriteDeadline(w) != nil {
		return false
	}
	if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
		return false
	}
	return http.NewResponseController(w).Flush() == nil
}

// writeSSEFrame writes one event in id/event/data shape. Flushes after each
// event so EventSource doesn't sit on the TCP buffer. Returns false when the
// underlying write fails (dead/stalled client) so the caller can stop.
func writeSSEFrame(w http.ResponseWriter, flusher http.Flusher, evt Event) bool {
	if strings.ContainsAny(evt.Type, "\r\n") {
		panic(fmt.Sprintf("sse: event Type %q contains CR/LF; refusing to serialise", evt.Type))
	}
	if setSSEWriteDeadline(w) != nil {
		return false
	}
	id := eventSeq.Add(1)
	if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
		return false
	}
	if evt.Type != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", evt.Type); err != nil {
			return false
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", mustJSON(evt)); err != nil {
		return false
	}
	return http.NewResponseController(w).Flush() == nil
}

// setupSSE writes Server-Sent-Events headers and returns the underlying
// Flusher. On failure it writes a 500 and the caller bails out.
func setupSSE(w http.ResponseWriter, r *http.Request) (http.Flusher, bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, T(r, "err_streaming_unsupported"), http.StatusInternalServerError)
		return nil, false
	}
	return flusher, true
}

// mustJSON is the json.Marshal that handlers use to inline a value into an
// SSE frame. We swallow the error — every input goes through our own typed
// helpers, so Marshal failure here would be a programming error worth a
// panic, not a runtime check at every call site.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// sseOriginAllowed mirrors the corsMiddleware logic for the long-poll
// endpoint that browsers reach via EventSource (which never preflights).
func (app *App) sseOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser callers (curl, integration tests, the App itself
		// over loopback) skip Origin entirely. AUTH_TOKEN is the gate
		// for those clients.
		return true
	}
	if app.allowedOrigin != "" {
		return originsEqual(origin, app.allowedOrigin)
	}
	return isSameOriginRequest(r, origin)
}
