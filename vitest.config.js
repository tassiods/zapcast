// Vitest config — runs the frontend unit suite under jsdom so modules that
// touch document/localStorage work without a real browser.
//
// Tests live next to the modules they cover (web/*.test.js). Pure modules
// like state.js and api.js don't need jsdom; the env: 'jsdom' default just
// makes sure the few that touch the DOM (ui.js helpers) keep working.

import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'jsdom',
    include: ['internal/server/web/**/*.test.js'],
    globals: false,
    environmentOptions: {
      // jsdom needs an explicit URL to wire up Window.localStorage; without
      // it the storage objects come back as undefined.
      jsdom: {
        url: 'http://localhost/',
      },
    },
  },
});
