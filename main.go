// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

// Command zapcast boots the HTTP server.

package main

import (
	"context"
	"fmt"
	"os"
	"zapcast/internal/server"
)

func main() {
	if len(os.Args) > 1 {
		if len(os.Args) != 2 || os.Args[1] != "healthcheck" {
			fmt.Fprintln(os.Stderr, "usage: zapcast [healthcheck]")
			os.Exit(2)
		}
		if err := server.ProbeHealth(context.Background(), os.Getenv("PORT")); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck failed")
			os.Exit(1)
		}
		return
	}
	server.Run()
}
