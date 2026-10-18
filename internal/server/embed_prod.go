// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

//go:build prod

package server

import "embed"

// Prod build: embed and serve only the Vite-bundled web/dist tree — no source
// or *.test.js files leak into the release binary. CI must run
// `npm run build` before `go build -tags prod`; otherwise the embed matches an
// empty (or missing) directory and the build fails loudly.

//go:embed all:web/dist
var webFS embed.FS

const webSubdir = "web/dist"
