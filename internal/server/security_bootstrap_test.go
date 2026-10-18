package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSecuritySameOriginCookieLoginAndAPICompatibility(t *testing.T) {
	app := newTestApp(t)
	app.logoutLimiter = newRateLimiter(time.Second, 10, time.Minute)
	t.Cleanup(app.logoutLimiter.Close)
	app.authToken = "synthetic-master-token"
	app.sessionKeyDir = t.TempDir()
	mux := http.NewServeMux()
	app.registerRoutes(mux)
	server := httptest.NewServer(accessLogMiddleware(testLogger, requestIDMiddleware(app.corsMiddleware(app.authMiddleware(mux)))))
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	req, err := http.NewRequest("POST", server.URL+"/api/auth/login", strings.NewReader(`{"token":"synthetic-master-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", server.URL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
	}()
	resp.Body.Close()
	if resp.StatusCode != 200 || len(resp.Cookies()) != 1 {
		t.Fatal("login failed")
	}
	cookie := resp.Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie protections lost")
	}
	for _, path := range []string{"/api/contacts", "/api/auth/required"} {
		req, _ = http.NewRequest("GET", server.URL+path, nil)
		req.Header.Set("Origin", server.URL)
		resp, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("cookie-only %s returned %d", path, resp.StatusCode)
		}
		if path == "/api/auth/required" {
			var state AuthRequiredResponse
			if err = json.Unmarshal(body, &state); err != nil || !state.Authenticated || state.Mode != "native" {
				t.Fatalf("bootstrap incompatible: %s", body)
			}
		}
	}
	req, _ = http.NewRequest("POST", server.URL+"/api/auth/session/logout", nil)
	req.Header.Set("Origin", server.URL)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
	}()
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", server.URL+"/api/contacts", nil)
	req.AddCookie(cookie)
	// Use a jar-free caller to replay the copied cookie independently.
	resp, err = (&http.Client{Transport: client.Transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
	}()
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("logout replay still authenticated")
	}
}

func TestSecurityAuthRequiredReportsNativeSessionState(t *testing.T) {
	app := &App{authToken: "token", logger: testLogger}
	value := app.issueSession()
	for _, authenticated := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/api/auth/required", nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		}
		w := httptest.NewRecorder()
		app.handleAuthRequired(w, r)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["required"] != true || body["mode"] != "native" || body["authenticated"] != authenticated {
			t.Fatalf("wrong bootstrap state: %v", body)
		}
		if authenticated && (body["sessionId"] != sessionIdentity(value) || body["expiresAt"] == nil) {
			t.Fatal("stable session metadata missing")
		}
	}
}
