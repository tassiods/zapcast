import { defineConfig, devices } from '@playwright/test';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
const PORT = process.env.E2E_PORT || '3132';
const external = process.env.E2E_NATIVE_URL;
if(external && !['127.0.0.1','localhost','[::1]'].includes(new URL(external).hostname)) {
  throw new Error('Native-session E2E is restricted to a dedicated loopback server.');
}
export default defineConfig({
  globalSetup: external ? undefined : './e2e/builds.mjs',
  testMatch: external ? '**/native-session.spec.js' : '**/!(native-session).spec.js',
  testDir: 'e2e', fullyParallel: false, workers: 1, retries: 0,
  reporter: 'list', timeout: 120_000, expect: {timeout: 20_000},
  outputDir: process.env.E2E_RESULTS || join(tmpdir(),'zapcast-browser-results'),
  use: { baseURL: external || `http://127.0.0.1:${PORT}`, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: [{name:'chromium', use:{...devices['Desktop Chrome']}}],
  webServer: external ? undefined : {
    command: 'node e2e/fixture-server.mjs', url: `http://127.0.0.1:${PORT}/healthz`,
    reuseExistingServer: false, timeout: 30_000,
  },
});
