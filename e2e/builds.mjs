import { build } from 'vite';
import { resolve, join } from 'node:path';
import { tmpdir } from 'node:os';
// Three real builds, different HTML and JS bytes, same deployment origin.
export const builds = ['one','two','three'].map(label => resolve(process.env.E2E_ARTIFACTS || join(tmpdir(),'zapcast-pwa-builds'),label));
export default async function() {
  await build(); // Fresh default prod artifact too; never test a stale dist/.
  for(let i=0;i<builds.length;i++) {
    await build({
      define: {'import.meta.env.VITE_ZAPCAST_BUILD_ID': JSON.stringify(`browser-release-${i+1}`)},
      build: {outDir: builds[i], rollupOptions:{output:{banner:`/* actual release ${i+1} */`}}},
      plugins:[{name:'browser-release-marker', transformIndexHtml(html) { return html.replace('<head>',`<head><meta name="e2e-release" content="${i+1}">`); }}],
    });
  }
}
