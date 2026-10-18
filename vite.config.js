// Production PWA contract: emitted assets, content-derived generation, no
// source module precache. Source/Go dev embeds remain deliberately uncached.
import { defineConfig } from 'vite';
import { resolve } from 'node:path';
import { readFile, writeFile, copyFile, readdir } from 'node:fs/promises';
import { createHash } from 'node:crypto';
const webRoot = resolve(import.meta.dirname, 'internal/server/web');
let output;
async function files(dir, prefix = '') {
  const result = [];
  for (const entry of await readdir(dir, {withFileTypes:true})) {
    const name = prefix + entry.name;
    if(entry.isDirectory()) result.push(...await files(resolve(dir,entry.name),name+'/'));
    else result.push(name);
  }
  return result.sort();
}
export default defineConfig({
  root: webRoot, base: '/', publicDir: false,
  plugins: [{
    name: 'zapcast-immutable-pwa',
    configResolved(config) { output = resolve(config.root, config.build.outDir); },
    transformIndexHtml: {
      order: 'post',
      handler(html, context) {
        const theme = Object.values(context.bundle || {}).find(entry => entry.type === 'chunk' && entry.name === 'themeBoot');
        return theme ? html.replace('src="/theme-boot.js"', `src="/${theme.fileName}"`) : html;
      },
    },
    async closeBundle() {
      for(const name of ['icon.svg','icon-maskable.svg','favicon.svg','favicon.ico','apple-touch-icon.png','favicon-96x96.png','web-app-manifest-192x192.png','web-app-manifest-512x512.png','manifest.webmanifest']) {
        await copyFile(resolve(webRoot,name),resolve(output,name));
      }
      const emitted = (await files(output)).filter(name => name !== 'sw.js' && !name.endsWith('.map'));
      const template = await readFile(resolve(webRoot,'sw.js'),'utf8');
      const hash = createHash('sha256').update(template);
      const integrity = {};
      for(const name of emitted) {
        const bytes = await readFile(resolve(output,name));
        hash.update(name); hash.update(bytes);
        integrity[name === 'index.html' ? '/' : '/'+name] = 'sha256-'+createHash('sha256').update(bytes).digest('base64');
      }
      const id = hash.digest('hex').slice(0,24);
      const assets = emitted.map(name => name === 'index.html' ? '/' : '/'+name);
      await writeFile(resolve(output,'sw.js'),`self.__ZAPCAST_BUILD__=${JSON.stringify({id,assets,integrity})};\n${template}`);
    },
  }],
  build: {
    outDir: resolve(webRoot,'dist'), emptyOutDir:true, sourcemap:false, target:'es2022',
    rollupOptions: {
      input: {index:resolve(webRoot,'index.html'), themeBoot:resolve(webRoot,'theme-boot.js')},
      output: {
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames:'assets/[name]-[hash].js', assetFileNames:'assets/[name]-[hash][extname]',
      },
    },
  },
});
