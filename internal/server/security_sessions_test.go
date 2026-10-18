package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSecurityLogoutRevokesCopiedCookie(t *testing.T) {
	dir := t.TempDir()
	app := &App{authToken: "synthetic-token", sessionKeyDir: dir, logger: testLogger}
	cookie := app.issueSession()
	other := app.issueSession()
	if !app.validSessionCookie(cookie) {
		t.Fatal("fresh cookie rejected")
	}
	r := httptest.NewRequest("POST", "https://audit.invalid/api/auth/session/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w := httptest.NewRecorder()
	app.handleSessionLogout(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if app.validSessionCookie(cookie) {
		t.Fatal("copied cookie remains valid after logout")
	}
	if !app.validSessionCookie(other) {
		t.Fatal("logout revoked unrelated session")
	}
	restarted := &App{authToken: app.authToken, sessionKeyDir: dir, logger: testLogger}
	if restarted.validSessionCookie(cookie) {
		t.Fatal("revoked cookie resurrected after restart")
	}
	if !restarted.validSessionCookie(other) {
		t.Fatal("unrevoked cookie lost after same-token restart")
	}
}

func TestSecurityUnknownLogoutDoesNotRewriteRegistry(t *testing.T) {
	dir := t.TempDir()
	app := &App{authToken: "token", sessionKeyDir: dir, logger: testLogger}
	value := app.issueSession()
	if value == "" {
		t.Fatal("issue")
	}
	// A malformed public logout should not perform a durable write at all.
	if err := os.Remove(filepath.Join(dir, "sessions.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sessions.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := app.revokeSession("not-a-credential"); err != nil {
		t.Fatal("unknown cookie caused persistence write", err)
	}
	if !app.validSessionCookie(value) {
		t.Fatal("unknown logout disturbed another session")
	}
}

func TestSecuritySessionsExpireAndCleanupIsBounded(t *testing.T) {
	app := &App{authToken: "token", logger: testLogger}
	cookie := app.issueSession()
	app.sessions.mu.Lock()
	app.sessions.active[sessionIdentity(cookie)] = time.Now().Add(-time.Second).Unix()
	app.sessions.mu.Unlock()
	if app.validSessionCookie(cookie) {
		t.Fatal("expired registry entry accepted")
	}
	app.sessions.mu.Lock()
	for i := 0; i < maxActiveSessions; i++ {
		app.sessions.active[strconv.Itoa(i)] = time.Now().Add(time.Hour).Unix()
	}
	app.sessions.mu.Unlock()
	if app.issueSession() != "" {
		t.Fatal("session cap exceeded")
	}
	app.sessions.mu.Lock()
	for id := range app.sessions.active {
		app.sessions.active[id] = 0
	}
	app.sessions.mu.Unlock()
	if app.issueSession() == "" {
		t.Fatal("expired identities not cleaned")
	}
	if len(app.sessions.active) != 1 {
		t.Fatal("cleanup retained expired identities")
	}
}

func TestSecurityPersistenceFailureFailsClosed(t *testing.T) {
	dir := t.TempDir()
	app := &App{authToken: "token", sessionKeyDir: dir, logger: testLogger}
	value := app.issueSession()
	if value == "" {
		t.Fatal("issue")
	}
	if err := os.Remove(filepath.Join(dir, "sessions.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sessions.json"), 0700); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/auth/session/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w := httptest.NewRecorder()
	app.handleSessionLogout(w, r)
	if w.Code != 503 || app.validSessionCookie(value) {
		t.Fatal("revocation write failure did not fail closed")
	}
	restarted := &App{authToken: "token", sessionKeyDir: dir, logger: testLogger}
	if restarted.validSessionCookie(value) || restarted.issueSession() != "" {
		t.Fatal("unreadable registry accepted login/session")
	}
}

func TestSecurityRejectsExpiredSignedSession(t *testing.T) {
	app := &App{authToken: "token", logger: testLogger}
	payload := "0123456789abcdef0123456789abcdef." + strconv.FormatInt(time.Now().Add(-sessionTTL-time.Second).Unix(), 10)
	value := payload + "." + app.sessionMACFor(payload)
	if !app.registerSession(value) {
		t.Fatal("register")
	}
	if app.validSessionCookie(value) {
		t.Fatal("expired signed cookie accepted")
	}
}

func TestSecurityKeyFailureFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "session.key"), 0700); err != nil {
		t.Fatal(err)
	}
	app := &App{authToken: "token", sessionKeyDir: dir, logger: testLogger}
	if cookie := app.issueSession(); cookie != "" {
		t.Fatal("issued session after key read/write failure")
	}
}

func TestSecurityRejectsFutureSignedSession(t *testing.T) {
	app := &App{authToken: "token", logger: testLogger}
	payload := "0123456789abcdef0123456789abcdef." + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	value := payload + "." + app.sessionMACFor(payload)
	if !app.registerSession(value) {
		t.Fatal("register")
	}
	if app.validSessionCookie(value) {
		t.Fatal("future-issued cookie accepted")
	}
}

func TestSecurityRotationInvalidatesUnrevokedSessions(t *testing.T) {
	dir := t.TempDir()
	app := &App{authToken: "old-token", sessionKeyDir: dir, logger: testLogger}
	cookie := app.issueSession()
	rotated := &App{authToken: "new-token", sessionKeyDir: dir, logger: testLogger}
	if rotated.validSessionCookie(cookie) {
		t.Fatal("old session accepted after token rotation")
	}
	fresh := rotated.issueSession()
	if fresh == "" || !rotated.validSessionCookie(fresh) {
		t.Fatal("new login rejected")
	}
	reverted := &App{authToken: "old-token", sessionKeyDir: dir, logger: testLogger}
	if reverted.validSessionCookie(cookie) {
		t.Fatal("old session resurrected after rotating back")
	}
}
