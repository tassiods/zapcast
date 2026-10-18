package server

import (
	"fmt"
)

// Explicit proxy mode delegates the authentication boundary to a gateway.
// Local mode is intentionally limited to loopback, including container boots.
func validateAuthConfig(host, token, mode, origin string) (string, error) {
	if mode == "" {
		if token != "" {
			mode = "native"
		} else {
			mode = "local"
		}
	}
	switch mode {
	case "native":
		if token == "" {
			return "", fmt.Errorf("AUTH_MODE=native requires AUTH_TOKEN")
		}
	case "proxy":
		if token != "" {
			return "", fmt.Errorf("AUTH_MODE=proxy conflicts with AUTH_TOKEN")
		}
	case "local":
		if !isLoopback(host) || token != "" {
			return "", fmt.Errorf("non-loopback listener requires AUTH_TOKEN or explicit AUTH_MODE=proxy")
		}
	default:
		return "", fmt.Errorf("invalid AUTH_MODE: use native, proxy, or local")
	}
	if origin != "" {
		if _, host := originTuple(origin); host == "" {
			return "", fmt.Errorf("FRONTEND_ORIGIN must be an HTTP(S) origin")
		}
	}
	return mode, nil
}
