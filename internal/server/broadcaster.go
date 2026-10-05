// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Broadcaster owns the broadcast lifecycle state — cadence, mutex, cancel
// hook, and WaitGroup for shutdown draining.

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Broadcaster keeps the bookkeeping for the at-most-one broadcast goroutine
// the service runs at a time.
type Broadcaster struct {
	logger    *slog.Logger
	delay     time.Duration
	jitter    float64
	hourlyCap int

	mu     sync.Mutex                         // held by the running goroutine
	last   time.Time                          // updated when a broadcast is admitted
	cancel atomic.Pointer[context.CancelFunc] // set while running, cleared at end
	wg     sync.WaitGroup                     // shutdown waits on this

	// lifeMu orders wg.Add against Close: either Close's critical section
	// runs first (Start then sees closing and never Adds) or Start's Add
	// completes before Close returns (so the Add happens-before the Wait that
	// follows Close). Without this, a broadcast request arriving during the
	// shutdown drain could Add(1) concurrently with wg.Wait when the counter
	// is zero — a WaitGroup misuse race.
	lifeMu  sync.Mutex
	closing bool
}

// NewBroadcaster wires the cadence knobs read from the environment.
func NewBroadcaster(lg *slog.Logger, delay time.Duration, jitter float64, hourlyCap int) *Broadcaster {
	return &Broadcaster{
		logger:    lg,
		delay:     delay,
		jitter:    jitter,
		hourlyCap: hourlyCap,
	}
}

// Cancel aborts the in-flight broadcast goroutine, if any. Safe to call from
// any goroutine; no-op when no broadcast is running.
func (b *Broadcaster) Cancel(reason string) {
	cancel := b.cancel.Load()
	if cancel != nil {
		if b.logger != nil {
			b.logger.Warn("cancelling in-flight broadcast", "reason", reason)
		}
		(*cancel)()
	}
}

// Running reports whether a broadcast goroutine currently holds the lock.
// Best-effort: the send may finish between this check and a Cancel call.
func (b *Broadcaster) Running() bool { return b.cancel.Load() != nil }

// SetLast updates the last-broadcast timestamp used for cooldown bookkeeping.
func (b *Broadcaster) SetLast(t time.Time) { b.last = t }

// ErrBroadcastRunning is returned by Start when another broadcast is in flight.
var ErrBroadcastRunning = errBroadcastRunning{}

type errBroadcastRunning struct{}

func (errBroadcastRunning) Error() string        { return "broadcast already running" }
func (errBroadcastRunning) LocalizedKey() string { return "err_broadcast_running" }
func (errBroadcastRunning) LocalizedArgs() []any { return nil }

// CooldownError is returned by Start when the per-broadcast cooldown window
// hasn't elapsed yet. Remaining tells the caller how long to wait.
type CooldownError struct {
	Remaining time.Duration
}

func (e *CooldownError) Error() string        { return "broadcast on cooldown" }
func (e *CooldownError) LocalizedKey() string { return "err_broadcast_cooldown" }
func (e *CooldownError) LocalizedArgs() []any { return []any{e.Remaining.Round(time.Second)} }

// StartHandle owns the cancellable context + Done/Abandon pair. Exactly
// one must run — never calling either leaks the mutex forever.
type StartHandle struct {
	Ctx    context.Context
	Cancel context.CancelFunc
	b      *Broadcaster
	once   sync.Once
}

// Done releases the lock after a successful goroutine run.
func (h *StartHandle) Done() {
	h.once.Do(func() {
		h.b.cancel.Store(nil)
		h.b.mu.Unlock()
		h.b.wg.Done()
	})
}

// Abandon releases the lock from an early return path.
func (h *StartHandle) Abandon() {
	h.once.Do(func() {
		h.Cancel()
		h.b.cancel.Store(nil)
		h.b.mu.Unlock()
		h.b.wg.Done()
	})
}

// Start reserves the broadcaster for one in-flight send: takes the per-send
// mutex, enforces the cooldown window, advances `last`, wires a cancellable
// context, and pre-increments the WaitGroup so shutdown waits even on the
// validation path. Returns ErrBroadcastRunning when another goroutine holds
// the lock, *CooldownError when the cooldown hasn't elapsed.
func (b *Broadcaster) Start(parent context.Context, cooldown time.Duration) (*StartHandle, error) {
	if !b.mu.TryLock() {
		return nil, ErrBroadcastRunning
	}
	if elapsed := time.Since(b.last); elapsed < cooldown {
		b.mu.Unlock()
		return nil, &CooldownError{Remaining: cooldown - elapsed}
	}
	// Refuse to admit a broadcast once shutdown has begun, and pair the
	// wg.Add with the closing check under lifeMu so it can't race wg.Wait.
	b.lifeMu.Lock()
	if b.closing {
		b.lifeMu.Unlock()
		b.mu.Unlock()
		return nil, ErrBroadcastRunning
	}
	b.wg.Add(1)
	b.lifeMu.Unlock()

	b.last = time.Now()

	ctx, cancel := context.WithCancel(parent)
	b.cancel.Store(&cancel)

	return &StartHandle{Ctx: ctx, Cancel: cancel, b: b}, nil
}

// Close marks the broadcaster as shutting down so no new broadcast is
// admitted. Ordered against Start via lifeMu so the subsequent wg.Wait is
// race-free. Idempotent.
func (b *Broadcaster) Close() {
	b.lifeMu.Lock()
	b.closing = true
	b.lifeMu.Unlock()
}
