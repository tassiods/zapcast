// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

// Package server is the body of the ZapCast HTTP service.
//
// Architecture: a Go HTTP server embeds a static SPA, owns a single
// whatsmeow client for WhatsApp Multi-Device, persists contacts + groups +
// broadcast history in SQLite, and streams progress over SSE.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	_ "github.com/mattn/go-sqlite3"
)

// initLogger builds a structured slog logger. LOG_LEVEL picks the minimum
// level (debug|info|warn|error); LOG_FORMAT picks the handler (text|json).
func initLogger() *slog.Logger {
	lvl := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if strings.ToLower(os.Getenv("LOG_FORMAT")) == "json" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h).With("app", "zapcast")
}

// webFS and webSubdir are declared per build tag in embed_dev.go /
// embed_prod.go so each build embeds only the subtree it actually serves
// (dev: web/, prod: web/dist/). This keeps test files out of release
// binaries and keeps the asset ETag computed over the served tree.

// App holds the application state. Most fields are written once at boot;
// mutexes guard the parts that mutate at runtime (whatsmeow client swap,
// QR pairing serialisation).
type App struct {
	logger    *slog.Logger
	container *sqlstore.Container
	wa        WAClient
	clientMu  sync.RWMutex // protects swapping wa after logout
	contacts  *ContactStore

	qrMu sync.Mutex // serialises /api/auth/qr requests

	hub         *SSEHub
	broadcaster *Broadcaster
	outbox      *OutboxWorker

	authToken     string
	allowedOrigin string

	// sessionKeyDir is where the persisted session HMAC key lives
	// (STORE_DIR). Empty in tests — sessions then use an in-memory key.
	sessionKeyDir   string
	sessionKeyOnce  sync.Once
	sessionKeyValue []byte

	qrLimiter        *rateLimiter
	logoutLimiter    *rateLimiter
	broadcastLimiter *rateLimiter

	// shutdownCtx is cancelled at the start of runShutdown so background
	// work (whatsmeow event handlers, client reset goroutines) winds down
	// before the DB closes.
	shutdownCtx  context.Context
	shutdownStop context.CancelFunc
}

// Thin wrappers so handlers don't need to spell out app.hub / app.broadcaster.
func (app *App) subscribe() chan Event     { return app.hub.Subscribe() }
func (app *App) unsubscribe(ch chan Event) { app.hub.Unsubscribe(ch) }
func (app *App) broadcastEvent(evt Event)  { app.hub.Broadcast(evt) }
func (app *App) cancelBroadcast(reason string) {
	if app.broadcaster != nil {
		app.broadcaster.Cancel(reason)
	}
}

// WA returns the current WAClient under a read lock. Capture once per
// request — stable for the duration even if a background swap happens.
func (app *App) WA() WAClient {
	app.clientMu.RLock()
	defer app.clientMu.RUnlock()
	return app.wa
}

// resetClient discards the current client and builds a new one from a fresh
// device. Called after Logout so the next QR pairing starts clean.
func (app *App) resetClient(ctx context.Context) error {
	app.clientMu.Lock()
	defer app.clientMu.Unlock()

	if app.wa != nil {
		app.wa.Disconnect()
	}
	deviceStore, err := app.container.GetFirstDevice(ctx)
	if err != nil {
		return err
	}
	newClient := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "WARN", true))
	newClient.AddEventHandler(app.handleWAEvent)
	app.wa = NewWAClientFromWhatsmeow(newClient)
	return nil
}

// jsonResponse writes v as JSON with the given status.
func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("jsonResponse encode failed", "err", err, "status", status)
	}
}

func Run() {
	lg := initLogger()
	slog.SetDefault(lg)

	// Register .webmanifest MIME — the stdlib doesn't know it.
	if err := mime.AddExtensionType(".webmanifest", "application/manifest+json"); err != nil {
		lg.Warn("failed to register .webmanifest MIME type", "err", err)
	}

	// Identify ourselves so WhatsApp shows "ZapCast" instead of "Outro dispositivo".
	configureDeviceProps()

	host, port, storeDir := readBootEnv()
	if err := prepareStoreDir(lg, storeDir); err != nil {
		lg.Error("failed to prepare store directory", "err", err, "dir", storeDir)
		os.Exit(1)
	}

	container, contactStore, client, err := openStores(lg, storeDir)
	if err != nil {
		lg.Error("failed to open stores", "err", err)
		os.Exit(1)
	}

	app := newApp(lg, container, contactStore, client)
	app.sessionKeyDir = storeDir
	warnAuthConfig(lg, app, host)

	bootCtx, bootCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if t, err := contactStore.LastBroadcastTime(bootCtx); err == nil && !t.IsZero() {
		app.broadcaster.SetLast(t)
	}
	// Broadcasts are not restart-safe: a send goroutine killed mid-flight
	// leaves its remaining recipients unsent (their rendered bodies were never
	// persisted). Surface any such interruption from the last run instead of
	// letting it pass silently.
	if ids, err := contactStore.TakeInterruptedBroadcasts(bootCtx); err != nil {
		lg.Warn("failed to check for interrupted broadcasts", "err", err)
	} else if len(ids) > 0 {
		lg.Warn("broadcasts interrupted by a previous restart — remaining recipients were NOT sent",
			"broadcast_ids", ids, "count", len(ids))
	}
	bootCancel()

	mux := http.NewServeMux()
	app.registerRoutes(mux)

	// Middleware chain: access log → request_id → CORS → securityHeaders → auth → mux.
	handler := accessLogMiddleware(lg,
		requestIDMiddleware(
			app.corsMiddleware(
				securityHeadersMiddleware(
					app.authMiddleware(mux)))))

	addr := fmt.Sprintf("%s:%s", host, port)
	server := &http.Server{
		Addr:    addr,
		Handler: handler,
		// Slowloris protection.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	client.AddEventHandler(app.handleWAEvent)
	connectExistingDevice(lg, client)

	// Spawn the outbox retry worker.
	app.outbox = StartOutboxWorker(app)

	go app.runShutdown(server)

	lg.Info("server listening",
		"addr", addr,
		"store_dir", storeDir,
		"version", Version(),
		"broadcast_delay_ms", app.broadcaster.delay.Milliseconds(),
		"broadcast_jitter", app.broadcaster.jitter,
		"broadcast_hourly_cap", app.broadcaster.hourlyCap,
	)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		lg.Error("server error", "err", err)
		os.Exit(1)
	}
}

// warnAuthConfig emits auth/origin warnings at boot.
func warnAuthConfig(lg *slog.Logger, app *App, host string) {
	if !isLoopback(host) && app.authToken == "" {
		lg.Warn("listener bound beyond loopback without AUTH_TOKEN — API is open to the network",
			"host", host)
	}
	if app.authToken == "" {
		return
	}
	if reason := assessAuthTokenStrength(app.authToken); reason != "" {
		lg.Warn("AUTH_TOKEN is weak — rotate it before exposing the listener publicly",
			"reason", reason)
	}
	if app.allowedOrigin == "" {
		lg.Warn("AUTH_TOKEN set without FRONTEND_ORIGIN — CORS will only accept same-origin browser callers",
			"hint", "set FRONTEND_ORIGIN if a separate origin needs to call /api")
	}
}

// readBootEnv pulls host/port/store_dir from the environment.
func readBootEnv() (host, port, storeDir string) {
	host = os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port = os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	storeDir = os.Getenv("STORE_DIR")
	if storeDir == "" {
		storeDir = "store"
	}
	return
}

// prepareStoreDir creates the store directory if missing and pins 0700 perms.
func prepareStoreDir(lg *slog.Logger, dir string) error {
	// #nosec G302,G703 -- session secrets, owner-only.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll won't tighten perms on a pre-existing directory; do it explicitly.
	// #nosec G302,G703 -- same rationale as above.
	if err := os.Chmod(dir, 0o700); err != nil {
		lg.Warn("failed to chmod store directory", "err", err, "dir", dir)
	}
	return nil
}

// openStores brings up both SQLite databases + the whatsmeow client.
func openStores(lg *slog.Logger, storeDir string) (*sqlstore.Container, *ContactStore, *whatsmeow.Client, error) {
	dbLog := waLog.Stdout("Database", "WARN", true)
	deviceDSN := fmt.Sprintf("file:%s/device.db?_foreign_keys=on", storeDir)
	container, err := sqlstore.New(context.Background(), "sqlite3", deviceDSN, dbLog)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open whatsmeow store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get device: %w", err)
	}

	clientLog := waLog.Stdout("Client", "WARN", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)

	contactStore, err := NewContactStore(context.Background(), lg, fmt.Sprintf("%s/zapcast.db", storeDir))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open contacts DB: %w", err)
	}
	return container, contactStore, client, nil
}

// newApp wires the App struct from already-open dependencies.
func newApp(lg *slog.Logger, container *sqlstore.Container, contactStore *ContactStore, client *whatsmeow.Client) *App {
	shutdownCtx, shutdownStop := context.WithCancel(context.Background())
	// Trusted-proxy set for clientIP's X-Forwarded-For handling. Unset =
	// XFF ignored (no spoofable rate-limit bucket / log IP).
	trustedProxies = parseTrustedProxies(lg, os.Getenv("TRUST_PROXY"))
	return &App{
		logger:    lg,
		container: container,
		wa:        NewWAClientFromWhatsmeow(client),
		contacts:  contactStore,
		hub:       NewSSEHub(lg),
		broadcaster: NewBroadcaster(lg,
			parseBroadcastDelay(lg, os.Getenv("BROADCAST_DELAY_MS")),
			parseBroadcastJitter(lg, os.Getenv("BROADCAST_DELAY_JITTER")),
			parseBroadcastHourlyCap(lg, os.Getenv("BROADCAST_HOURLY_CAP")),
		),
		authToken:     strings.TrimSpace(os.Getenv("AUTH_TOKEN")),
		allowedOrigin: strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN")),
		// QR pairing: 5 attempts/min per IP, burst 5.
		qrLimiter: newRateLimiter(12*time.Second, 5, 10*time.Minute),
		// Logout: 10/min per IP, burst 10.
		logoutLimiter: newRateLimiter(6*time.Second, 10, 10*time.Minute),
		// Broadcast: 1 per 10s per IP, burst 2.
		broadcastLimiter: newRateLimiter(10*time.Second, 2, 10*time.Minute),
		shutdownCtx:      shutdownCtx,
		shutdownStop:     shutdownStop,
	}
}

// registerRoutes attaches every HTTP route to mux.
func (app *App) registerRoutes(mux *http.ServeMux) {
	// Embedded frontend. Immutable per binary — derive a single ETag at
	// startup so browsers revalidate cheaply (If-None-Match → 304).
	webContent, err := fs.Sub(webFS, webSubdir)
	if err != nil {
		app.logger.Error("failed to root embedded web fs", "err", err, "subdir", webSubdir)
		os.Exit(1)
	}
	assetETag := computeAssetETag(webFS, webSubdir)
	mux.Handle("/", staticETagMiddleware(http.FileServer(http.FS(webContent)), assetETag))

	// Operational probes — no auth.
	mux.HandleFunc("GET /healthz", app.handleHealthz)
	mux.HandleFunc("GET /readyz", app.handleReadyz)

	// API.
	mux.HandleFunc("GET /api/auth/required", app.handleAuthRequired)
	mux.HandleFunc("GET /api/status", app.handleStatus)
	mux.Handle("POST /api/auth/qr", app.qrLimiter.Middleware("/api/auth/qr", http.HandlerFunc(app.handleAuthQR)))
	mux.Handle("POST /api/auth/logout", app.logoutLimiter.Middleware("/api/auth/logout", http.HandlerFunc(app.handleLogout)))
	mux.Handle("POST /api/auth/login", app.logoutLimiter.Middleware("/api/auth/login", http.HandlerFunc(app.handleLogin)))
	mux.HandleFunc("POST /api/auth/session/logout", app.handleSessionLogout)

	mux.HandleFunc("GET /api/contacts", app.listContacts)
	mux.HandleFunc("GET /api/contacts/export", app.exportContacts)
	mux.HandleFunc("POST /api/contacts", app.createContact)
	mux.HandleFunc("PUT /api/contacts/{phone}", app.updateContact)
	mux.HandleFunc("DELETE /api/contacts/{phone}", app.deleteContact)

	mux.HandleFunc("GET /api/groups", app.listGroups)
	mux.HandleFunc("POST /api/groups", app.createGroup)
	mux.HandleFunc("PUT /api/groups/{name}", app.updateGroup)
	mux.HandleFunc("DELETE /api/groups/{name}", app.deleteGroup)

	mux.HandleFunc("GET /api/templates", app.listTemplates)
	mux.HandleFunc("POST /api/templates", app.upsertTemplate)
	mux.HandleFunc("DELETE /api/templates/{key}", app.deleteTemplate)

	mux.HandleFunc("POST /api/import", app.handleImport)
	mux.Handle("POST /api/broadcast", app.broadcastLimiter.Middleware("/api/broadcast", http.HandlerFunc(app.handleBroadcast)))
	mux.HandleFunc("POST /api/broadcast/cancel", app.handleBroadcastCancel)
	mux.HandleFunc("GET /api/broadcasts", app.handleBroadcastList)
	mux.HandleFunc("GET /api/broadcasts/{id}", app.handleBroadcastByID)
	mux.HandleFunc("GET /api/stats", app.handleStats)
	mux.HandleFunc("GET /api/events", app.handleSSE)
	mux.HandleFunc("GET /api/translations", app.handleTranslations)

	// Client-side error sink — SPA posts uncaught exceptions here.
	mux.HandleFunc("POST /api/client-error", app.handleClientError)

	// Outbox: rows that exhausted retries.
	mux.HandleFunc("GET /api/outbox/dead", app.handleOutboxDead)

	// Runtime metrics. Gated by auth when AUTH_TOKEN is set.
	mux.HandleFunc("GET /debug/vars", handleMetrics)
}

// connectExistingDevice tries to connect a previously-paired session.
// Failure is informational — user can still pair via QR.
func connectExistingDevice(lg *slog.Logger, client *whatsmeow.Client) {
	if client.Store.ID == nil {
		return
	}
	if err := client.Connect(); err != nil {
		lg.Warn("failed to connect WhatsApp on startup", "err", err, "recoverable", true)
		return
	}
	lg.Info("WhatsApp connected")
}

// runShutdown handles SIGINT/SIGTERM: cancel broadcast, close HTTP,
// disconnect WhatsApp, close DB. Bounded by a 15s deadline.
func (app *App) runShutdown(server *http.Server) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	app.logger.Info("shutting down")

	// Cancel shutdown context so detached goroutines bail before DB closes.
	app.shutdownStop()
	// Stop admitting new broadcasts before waiting on the WaitGroup, so a
	// late request can't Add(1) concurrently with wg.Wait.
	app.broadcaster.Close()
	app.cancelBroadcast("shutdown")
	// Wait up to 10s for the broadcast goroutine to finish writing aborted rows.
	done := make(chan struct{})
	go func() { app.broadcaster.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		app.logger.Warn("broadcast goroutine did not finish in time")
	}
	// Drain the outbox worker only after the broadcast goroutine is done —
	// an aborting broadcast may still be enqueueing retry rows.
	if app.outbox != nil {
		app.outbox.Wait()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		app.logger.Warn("HTTP shutdown error", "err", err)
	}
	app.WA().Disconnect()
	_ = app.contacts.Close()
	if app.container != nil {
		_ = app.container.Close()
	}
	app.qrLimiter.Close()
	app.logoutLimiter.Close()
	app.broadcastLimiter.Close()
}
