package server

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSecurityAuthModeConfigurationMatrix(t *testing.T) {
	cases := []struct {
		host, token, mode, origin, want string
		reject                          bool
	}{
		{"127.0.0.1", "", "", "", "local", false}, {"::1", "", "local", "", "local", false},
		{"0.0.0.0", "token", "", "", "native", false}, {"0.0.0.0", "", "proxy", "", "proxy", false},
		{"0.0.0.0", "", "", "", "", true}, {"0.0.0.0", "", "local", "", "", true}, {"127.0.0.1", "", "native", "", "", true},
		{"0.0.0.0", "token", "proxy", "", "", true}, {"127.0.0.1", "", "typo", "", "", true},
		{"127.0.0.1", "token", "native", "https://audit.invalid:999999", "", true},
		{"127.0.0.1", "token", "native", "https://audit.invalid?", "", true},
	}
	for _, c := range cases {
		got, err := validateAuthConfig(c.host, c.token, c.mode, c.origin)
		if (err != nil) != c.reject || (!c.reject && got != c.want) {
			t.Errorf("host=%s mode=%s origin=%s: got mode=%s err=%v", c.host, c.mode, c.origin, got, err)
		}
	}
}

func TestSecurityStartupRejectsUnprotectedNetworkListener(t *testing.T) {
	if os.Getenv("ZAPCAST_STARTUP_PROBE") == "1" {
		Run()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// #nosec G204,G702 -- execute this test binary itself with fixed test arguments.
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSecurityStartupRejectsUnprotectedNetworkListener$")
	cmd.Env = append(os.Environ(), "ZAPCAST_STARTUP_PROBE=1", "HOST=0.0.0.0", "PORT=0", "AUTH_TOKEN=", "AUTH_MODE=", "STORE_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("unprotected listener started instead of rejecting configuration")
	}
	if err == nil || !strings.Contains(string(out), "AUTH_MODE") {
		t.Fatalf("expected explicit auth configuration failure, err=%v output=%s", err, out)
	}
}
