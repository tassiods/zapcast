// Canonical E2E runs BOTH synthetic browser contracts and this exact prod binary.
// A fixture is never presented as proof of Go authentication/delivery behavior.
import { spawn } from 'node:child_process';
import { mkdtemp, rm, realpath, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

async function unusedPort() {
  const server = createServer();
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  const port = server.address().port;
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  return port;
}
async function runBrowser(env) {
  const child = spawn(process.execPath, ['node_modules/@playwright/test/cli.js', 'test', '--project=chromium', '--workers=1'], {env, stdio:'inherit'});
  return await new Promise((resolve, reject) => { child.once('error', reject); child.once('exit', (code, signal) => resolve(code ?? (signal ? 1 : 0))); });
}
async function stop(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = new Promise(resolve => child.once('exit', resolve));
  child.kill('SIGTERM');
  const forced = setTimeout(() => child.kill('SIGKILL'), 20_000);
  await exited;
  clearTimeout(forced);
}
const supplied = process.env.ZAPCAST_E2E_BINARY;
if (!supplied) throw Error('ZAPCAST_E2E_BINARY must name the identified production binary');
const binary = await realpath(supplied);
const store = await mkdtemp(join(tmpdir(), 'zapcast-native-e2e-'));
const nativePort = await unusedPort();
let fixturePort = await unusedPort();
while (fixturePort === nativePort) fixturePort = await unusedPort();
const url = `http://127.0.0.1:${nativePort}`;
const env = {...process.env, HOST:'127.0.0.1', PORT:String(nativePort), STORE_DIR:store, AUTH_MODE:'native', AUTH_TOKEN:'synthetic-test-token', FRONTEND_ORIGIN:url, TRUST_PROXY:'', LOG_LEVEL:'error'};
// Reject server reuse: only the child spawned from the supplied immutable artifact.
const child = spawn(binary, [], {env, stdio:'inherit'});
try {
  let ready = false;
  for (let attempt=0; attempt<100; attempt++) {
    if (child.exitCode !== null || child.signalCode !== null) throw Error('Production binary exited before readiness');
    try {
      const response = await fetch(`${url}/readyz`, {signal:AbortSignal.timeout(500)});
      if (response.ok && (await response.json()).ready) {ready=true;break;}
    } catch { /* bounded fresh-server readiness check */ }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  if (!ready) throw Error('Production binary did not become ready');
  await writeFile(join(store, '.e2e-owned'), 'synthetic canonical E2E store', {mode:0o600});
  const seed = spawn('python3', ['scripts/seed_e2e_review.py', store], {env:process.env, stdio:'inherit'});
  const seeded = await new Promise((resolve, reject) => { seed.once('error', reject); seed.once('exit', code => resolve(code)); });
  if (seeded !== 0) throw Error('Synthetic review inputs were not prepared safely');
  const fixtureEnv = {...process.env, E2E_PORT:String(fixturePort)};
  delete fixtureEnv.E2E_NATIVE_URL;
  let result = await runBrowser(fixtureEnv);
  if (result === 0) result = await runBrowser({...process.env, E2E_NATIVE_URL:url});
  process.exitCode = Number(result);
} finally {
  await stop(child);
  await rm(store, {recursive:true, force:true});
}
