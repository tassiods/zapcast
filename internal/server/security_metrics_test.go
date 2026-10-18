package server

import (
	"expvar"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityRegisteredRouteLabelsSurviveRequestID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/contacts/{phone}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := accessLogMiddleware(testLogger, requestIDMiddleware(mux))
	r := httptest.NewRequest("GET", "/api/contacts/synthetic", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if label := routeLabel(r); label != "GET /api/contacts/{phone}" {
		t.Fatalf("registered label lost: %s", label)
	}
	if metricHTTPByRoute.Get("GET /api/contacts/{phone}") == nil {
		t.Fatal("registered route metric missing")
	}
}

func TestSecurityClientErrorKindsHaveBoundedCardinality(t *testing.T) {
	app := &App{logger: testLogger}
	before := 0
	metricClientErrors.Do(func(expvar.KeyValue) { before++ })
	for i := 0; i < 100; i++ {
		r := httptest.NewRequest("POST", "/api/client-error", strings.NewReader(fmt.Sprintf(`{"kind":"unique-%d"}`, i)))
		w := httptest.NewRecorder()
		app.handleClientError(w, r)
		if w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	after := 0
	metricClientErrors.Do(func(expvar.KeyValue) { after++ })
	if after-before > 1 {
		t.Fatalf("created %d client error kinds", after-before)
	}
}

func TestSecurityUnauthenticatedRoutesHaveBoundedCardinality(t *testing.T) {
	app := &App{authToken: "synthetic-token"}
	h := accessLogMiddleware(testLogger, app.authMiddleware(http.NotFoundHandler()))
	before := 0
	metricHTTPByRoute.Do(func(expvar.KeyValue) { before++ })
	for i := 0; i < 100; i++ {
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/security-unique-%d", i), nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	after := 0
	metricHTTPByRoute.Do(func(expvar.KeyValue) { after++ })
	if after-before > 1 {
		t.Fatalf("created %d labels for unmatched requests", after-before)
	}
}
