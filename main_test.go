package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthcheckCommandUsesExistingLoopbackServer(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "zapcast-probe")
	// #nosec G204,G702 -- fixed local Go build, output in this test-owned directory.
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{{"alive", "ok", 200, true}, {"wrong-body", "other", 200, false}, {"not-ready", "failed", 503, false}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					t.Error(r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			endpoint, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// #nosec G204,G702 -- execute the freshly built test-owned artifact with a fixed command.
			command := exec.CommandContext(ctx, binary, "healthcheck")
			command.Env = append(os.Environ(), "HOST=127.0.0.1", "PORT="+endpoint.Port(), "AUTH_TOKEN=", "AUTH_MODE=local", "STORE_DIR="+t.TempDir())
			output, err := command.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("healthcheck success=%v expected=%v: %s", err == nil, tc.ok, output)
			}
		})
	}
}
