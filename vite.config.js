// Vite build config — produces a minified, code-split bundle of the SPA
// under internal/server/web/dist/. Optional in dev: the Go server still
// embeds and serves the source tree (web/*.js) for instant feedback. CI
// runs `npm run build` so the release binary, compiled with `-tags prod`,
// embeds the bundled dist/ instead.

import { defineConfig } from 'vite';
import { resolve } from 'node:path';

const webRoot = resolve(import.meta.dirname, 'internal/server/web');

export default defineConfig({
  root: webRoot,
  base: '/',
  publicDir: false,
  build: {
    outDir: resolve(webRoot, 'dist'),
    emptyOutDir: true,
    sourcemap: true,
    target: 'es2022',
    rollupOptions: {
      input: {
        index: resolve(webRoot, 'index.html'),
        themeBoot: resolve(webRoot, 'theme-boot.js'),
        sw: resolve(webRoot, 'sw.js'),
      },
      output: {
        entryFileNames: (chunk) => {
          if (chunk.name === 'themeBoot') return 'theme-boot.js';
          if (chunk.name === 'sw') return 'sw.js';
          return 'assets/[name]-[hash].js';
        },
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
});
