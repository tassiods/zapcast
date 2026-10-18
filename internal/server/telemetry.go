package server

import (
	"net/url"
	"regexp"
	"strings"
)

var telemetryURLs = regexp.MustCompile(`(?i)(?:https?://|/)[^\s"'<>]+`)
var telemetryCredentials = regexp.MustCompile(`(?i)\b(?:bearer\s+|(?:auth[_-]?token|token|password|secret|session|api[_-]?key)\s*[:=]\s*)[^\s"'<>]+`)

func sanitizeTelemetryURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") {
		return "[redacted]"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

func (app *App) sanitizeTelemetry(text string) string {
	if app.authToken != "" {
		for _, secret := range []string{app.authToken, url.QueryEscape(app.authToken), url.PathEscape(app.authToken)} {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	text = telemetryURLs.ReplaceAllStringFunc(text, sanitizeTelemetryURL)
	return telemetryCredentials.ReplaceAllString(text, "[redacted]")
}
