// Playwright config — minimal smoke suite that boots the Go binary,
// hits the SPA, asserts the page renders and the console stays clean.

import { defineConfig, devices } from '@playwright/test';

const PORT = process.env.E2E_PORT || '3132';

export default defineConfig({
  testDir: 'e2e',
  fullyParallel: false,
  retries: 0,
  reporter: process.env.CI ? 'github' : 'list',
  timeout: 30_000,
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  webServer: {
    // `go run .` works with whatever Go toolchain the developer has
    // installed; CI should `go build` first and point at the binary.
    command: `CGO_ENABLED=1 PORT=${PORT} LOG_LEVEL=error STORE_DIR=$(mktemp -d) go run .`,
    url: `http://127.0.0.1:${PORT}/healthz`,
    reuseExistingServer: true,
    timeout: 60_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
