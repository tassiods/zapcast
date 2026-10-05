// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !prod

package server

import "embed"

// Dev build: serve the raw source tree. Changes to web/*.js are picked up
// by rebuilding the Go binary; no Vite step needed.

//go:embed all:web
var webFS embed.FS

const webSubdir = "web"
