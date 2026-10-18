// Synthetic backend is explicitly a browser contract fixture, not Go auth proof.
// Prod bundles are real Vite builds, served without transforming their contents.
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import { resolve, extname } from 'node:path';
const port = Number(process.env.E2E_PORT || 3132);
let build = process.env.E2E_DIST || 'internal/server/web/dist';
let session = false;
let shellPolicy = '';
let mode = 'native';
let expired = false;
let holdHistory = false;
let redirectLogin = false;
const types = {'.html':'text/html','.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml','.png':'image/png','.ico':'image/x-icon','.webmanifest':'application/manifest+json'};
http.createServer(async (req,res) => {
  const path = new URL(req.url,'http://localhost').pathname;
  const json = (data,status=200) => {res.writeHead(status,{'Content-Type':'application/json','Cache-Control':'no-store'});res.end(JSON.stringify(data));};
  if (path === '/__fixture/build') {build = new URL(req.url,'http://localhost').searchParams.get('dir');return json({ok:true});}
  if (path === '/__fixture/expire') {session=false;expired=true;return json({ok:true});}
  if (path === '/__fixture/policy') {shellPolicy = new URL(req.url,'http://localhost').searchParams.get('value');return json({ok:true});}
  if (path === '/' && shellPolicy === 'redirect') {res.writeHead(302,{Location:'/sso/login'});return res.end();}
  if (path === '/' && shellPolicy === 'tamper') {res.writeHead(200,{'Content-Type':'text/html','Cache-Control':'no-cache'});return res.end('PRIVATE SSO masquerading as shell');}
  if (path === '/' && shellPolicy === 'mime') return json({private:'PRIVATE SHELL'});
  if (path === '/__fixture/mode') {mode = new URL(req.url,'http://localhost').searchParams.get('value');expired=false;return json({ok:true});}
  if (path === '/__fixture/hold-history') {holdHistory = new URL(req.url,'http://localhost').searchParams.get('value') === '1';return json({ok:true});}
  if (path === '/__fixture/login-redirect') {redirectLogin = new URL(req.url,'http://localhost').searchParams.get('value') === '1';return json({ok:true});}
  if (path === '/healthz') return json({ok:true});
  if (path === '/api/auth/required') return json({required:mode === 'native'});
  if (path === '/api/auth/login') {
    if(redirectLogin) {res.writeHead(307,{Location:'/sso/login'});return res.end();}
    let data='';for await(const chunk of req)data+=chunk;
    if(JSON.parse(data).token !== 'synthetic-test-token')return json({},401);
    session=true;expired=false;res.setHeader('Set-Cookie','zapcast_session=synthetic-session; HttpOnly; SameSite=Strict; Path=/');return json({status:'ok'});
  }
  if (path === '/api/auth/session/logout') {session=false;res.setHeader('Set-Cookie','zapcast_session=; HttpOnly; SameSite=Strict; Path=/; Max-Age=0');return json({status:'logged_out'});}
  if(path === '/api/translations') {if(req.headers['if-none-match']){res.writeHead(304);return res.end();}res.setHeader('ETag','"fixture"');return json({});}
  if(path.startsWith('/api/')) {
    if(mode === 'native' && (!session || !req.headers.cookie?.includes('zapcast_session=synthetic-session')))return json({},401);
    if(mode === 'proxy' && expired) {res.writeHead(302,{Location:'/sso/login'});return res.end();}
    if(path === '/api/broadcasts' && holdHistory) await new Promise(resolve=>setTimeout(resolve,2000));
    if(path === '/api/contacts/zzz' && req.method === 'DELETE')return json({error:{code:'err_contact_not_found',message:'contact not found'}},404);
    if(path === '/api/events'){res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-store'});res.write('data: {"type":"status"}\n\n');return;}
    if(path === '/api/status')return json({connected:false,loggedIn:false,version:'fixture'});
    if(path === '/api/stats')return json({});
    if(path === '/api/templates')return json({});
    return json([]);
  }
  if(path === '/debug/vars')return json({private:'PRIVATE DEBUG'});
  if(path === '/sso/login'){res.writeHead(200,{'Content-Type':'text/html','Cache-Control':'no-store'});return res.end('PRIVATE SSO');}
  try {
    const dir=resolve(build);const file=resolve(dir,'.'+(path === '/' ? '/index.html':path));
    if(!file.startsWith(dir+'/'))throw Error('path');
    const body=await readFile(file);res.writeHead(200,{'Content-Type':types[extname(file)]||'application/octet-stream','Cache-Control':path === '/' && shellPolicy === 'no-store' ? 'no-store' : 'no-cache'});res.end(body);
  } catch {res.writeHead(404);res.end('not found');}
}).listen(port,'127.0.0.1');
