package server

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// Pin the credential that opened a stream. A revoked cookie must not fall
// through to another credential, and idle streams must expire as well.
func (app *App) streamAuthorization(r *http.Request) func() bool {
	if app.authToken == "" {
		return func() bool { return true }
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && app.validSessionCookie(c.Value) {
		value := c.Value
		return func() bool { return app.validSessionCookie(value) }
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return func() bool {
		return ok && app.authToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(app.authToken)) == 1
	}
}

func (app *App) authorizedStreamContext(r *http.Request) (context.Context, context.CancelFunc, func() bool) {
	ctx, cancel := context.WithCancel(r.Context())
	valid := app.streamAuthorization(r)
	if !valid() {
		cancel()
		return ctx, cancel, valid
	}
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		var shutdown <-chan struct{}
		if app.shutdownCtx != nil {
			shutdown = app.shutdownCtx.Done()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-shutdown:
				cancel()
				return
			case <-ticker.C:
				if !valid() {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel, valid
}
