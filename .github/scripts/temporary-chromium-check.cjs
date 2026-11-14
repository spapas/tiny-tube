const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { randomBytes } = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { tmpdir } = require('node:os');
const net = require('node:net');
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const executable = path.resolve(process.env.RUNNER_TEMP, 'tiny-tube');
const fixture = fs.readFileSync('testdata/sample.mp4');
const chrome = 'google-chrome';
async function poll(fn, ms, title) {
  const until = Date.now() + ms; let last;
  while (Date.now() < until) {
    try { const value = await fn(); if (value) return value; } catch (err) { last = err; }
    await delay(150);
  }
  throw Error(title + ': ' + (last?.message || 'timeout'));
}
function port() {
  return new Promise((res, rej) => {
    const s = net.createServer();
    s.on('error', rej).listen(0, '127.0.0.1', () => { const num = s.address().port; s.close(() => res(num)); });
  });
}
async function run() {
  const root = fs.mkdtempSync(path.join(tmpdir(), 'tiny-tube-browser-'));
  const servicePort = await port(), debugPort = await port();
  const base = 'http://127.0.0.1:' + servicePort, debug = 'http://127.0.0.1:' + debugPort;
  const password = randomBytes(24).toString('hex');
  const server = spawn(executable, [], { cwd: root, windowsHide: true, stdio: 'ignore', env: {
    ...process.env, ADDR: '127.0.0.1:' + servicePort, VIDEO_DIR: path.join(root, 'videos'),
    ADMIN_USER: 'smoke', ADMIN_PASS: password, AUTH_MODE: 'local', STORE: 'sqlite',
    DB_PATH: '', COOKIE_SECURE: 'false', FFMPEG_BIN: ''
  }});
  const children = [server]; let socket, browserLogs = '';
  try {
    await poll(async () => (await fetch(base+'/readyz')).ok, 12000, 'server readiness');
    async function check(url, expected, options) {
      const response = await fetch(base+url, options);
      assert.equal(response.status, expected, url.split('?')[0]);
      return response;
    }
    const login = await check('/api/login', 200, {method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({username:'smoke',password})});
    const cookie = login.headers.getSetCookie()[0].split(';')[0];
    const headers = { Cookie: cookie, Accept: 'application/json' };
    const list = await check('/api/admin/lists/create', 200, {method:'POST',headers:{Cookie:cookie},
      body:new URLSearchParams({name:'Browser smoke list'})});
    const markup = await list.text();
    const key = markup.match(/\/view_list\?key=([\w-]+)/)?.[1];
    const listId = markup.match(/name="id" value="([\w-]+)"/)?.[1];
    assert.ok(key && listId);
    const multipart = new FormData();
    multipart.set('title', 'Browser smoke clip');
    multipart.set('access_list', listId);
    multipart.set('file', new Blob([fixture], {type:'video/mp4'}), 'sample.mp4');
    const {id} = await (await check('/api/upload', 201, {method:'POST',headers,body:multipart})).json();
    const watch = '/watch?id='+id;
    await check(watch, 404);
    const sub = new FormData();
    sub.set('id', id); sub.set('language', 'en');
    sub.set('file', new Blob(['WEBVTT\n\n00:00:00.000 --> 00:00:04.000\nChromium caption\n']), 'test.vtt');
    await check('/api/subtitles/upload', 200, {method:'POST',headers,body:sub});
    await poll(async () => (await (await check('/api/video?id='+id,200,{headers})).json()).hls, 20000, 'HLS');
    console.log('Temporary Windows server provided locked HLS and WebVTT.');
    const child = spawn(chrome,['--headless=new','--disable-gpu','--disable-dev-shm-usage','--disable-background-networking',
      '--no-first-run','--no-default-browser-check','--autoplay-policy=no-user-gesture-required',
      '--remote-debugging-address=127.0.0.1','--remote-debugging-port='+debugPort,
      '--user-data-dir='+path.join(root,'chrome'),'about:blank'], {windowsHide:true,stdio:['ignore','ignore','pipe']});
    children.push(child);
    child.stderr.on('data', data => { browserLogs += data.toString(); });
    const tab = await poll(async () => (await (await fetch(debug+'/json/list')).json()).find(t => t.type==='page'), 12000, 'Chromium debugging');
    socket = new WebSocket(tab.webSocketDebuggerUrl);
    const callbacks = new Map(); let requestId = 0; const requests = [], failures = [];
    socket.addEventListener('message', ({data}) => {
      const event = JSON.parse(data);
      if(event.id) { const cb = callbacks.get(event.id); callbacks.delete(event.id); cb?.(event); }
      if(event.method === 'Network.responseReceived') requests.push(event.params.response);
      if(event.method === 'Runtime.exceptionThrown') failures.push(event.params.exceptionDetails.text);
    });
    await new Promise((resolve,reject) => {socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
    function cdp(method,params={}) {
      return new Promise((resolve,reject) => {
        const id=++requestId;
        const timer=setTimeout(() => { callbacks.delete(id); reject(Error(method+' timed out')); },6000);
        callbacks.set(id, msg => {clearTimeout(timer); if(msg.error)reject(Error(msg.error.message));else resolve(msg.result);});
        socket.send(JSON.stringify({id,method,params}));
      });
    }
    async function evalPage(expression) {
      const outcome=await cdp('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});
      if(outcome.exceptionDetails)throw Error(outcome.exceptionDetails.exception?.description || outcome.exceptionDetails.text);
      return outcome.result.value;
    }
    await cdp('Network.enable'); await cdp('Runtime.enable');
    await cdp('Page.navigate',{url:base+watch});
    await poll(() => evalPage('document.readyState==="complete" && location.pathname==="/watch" && !document.getElementById("v")'), 6000, 'locked page denied');
    await cdp('Page.navigate',{url:base+'/view_list?key='+key});
    await poll(() => evalPage('document.readyState==="complete" && location.pathname==="/view_list"'), 6000, 'locked list opened');
    await cdp('Page.navigate',{url:base+watch+'#t=1'});
    await poll(() => evalPage('document.querySelector("video")?.readyState >= 3'), 12000, 'Chromium HLS buffering');
    const media = await evalPage(String.raw`(async () => {
      const v = document.querySelector('video');
      v.pause();
      const track = document.querySelector('track');
      track.track.mode = 'showing';
      if (track.readyState === 0 || track.readyState === 1) await new Promise((ok,bad) => { track.onload=ok; track.onerror=bad; });
      return { src:v.currentSrc, time:v.currentTime, duration:v.duration, caption:track.track.cues[0]?.text,
        supported:window.Hls.isSupported() };
    })()`);
    assert.ok(media.supported && media.src.startsWith('blob:') && media.time >= 0.9 && media.duration >= 4);
    assert.equal(media.caption, 'Chromium caption');
    assert.ok(requests.some(({url,status}) => url.endsWith('.m3u8') && status===200));
    assert.ok(requests.some(({url,status}) => url.endsWith('.ts') && status===200));
    assert.deepEqual(failures,[]);
    console.log('Chromium passed: locked watch page, list unlock, hls.js manifest and segment, timestamp seek and rendered WebVTT cue.');
  } catch(err) {
    if (browserLogs) console.error(browserLogs.split(/\r?\n/).slice(-9).join('\n'));
    throw err;
  } finally {
    socket?.close();
    for (const child of children.reverse()) child.kill();
    await delay(1000);
    try { fs.rmSync(root,{recursive:true,force:true,maxRetries:3}); } catch {}
  }
}
run().catch(err=>{console.error(err.stack);process.exitCode=1;});