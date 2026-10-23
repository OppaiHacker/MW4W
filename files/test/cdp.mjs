// Headless UI tests for MW4W Files: Edge headless over the DevTools protocol (Node 22+, no deps).
//
//   go build -o files-dev.exe . && MW4W_FILES_DATA=<tmp> files-dev.exe --serve   -> prints the URL
//   node test/ui.mjs <url> <playground folder>
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const EDGE = process.env.EDGE || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// open: a headless Edge at url
export async function open(url) {
  const prof = mkdtempSync(join(tmpdir(), 'mw4w-cdp-'));
  const port = 9300 + Math.floor(Math.random() * 500);
  const ed = spawn(EDGE, ['--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${prof}`, '--no-first-run', '--window-size=1200,800', url], { stdio: 'ignore' });
  const host = new URL(url).host;
  const p = await attach(port, (t) => t.type === 'page' && t.url.includes(host));
  const close = p.close;
  p.close = () => {
    close();
    ed.kill();
    setTimeout(() => rmSync(prof, { recursive: true, force: true }), 500);
  };
  return p;
}

// attach: the first page matching want on a DevTools port (the app's own WebView2:
// WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=<port>)
export async function attach(port, want, ms = 20000) {
  let target;
  for (const t0 = Date.now(); !target && Date.now() - t0 < ms; await sleep(100)) {
    try {
      target = (await (await fetch(`http://127.0.0.1:${port}/json`)).json()).find(want);
    } catch {}
  }
  if (!target) throw new Error('no page on DevTools port ' + port);
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((r) => (ws.onopen = r));
  let id = 0;
  const wait = new Map();
  const errors = [];
  ws.onmessage = (m) => {
    const j = JSON.parse(m.data);
    if (j.id && wait.has(j.id)) {
      wait.get(j.id)(j);
      wait.delete(j.id);
    } else if (j.method === 'Network.loadingFailed' && !j.params.canceled) errors.push('load failed: ' + j.params.errorText + ' ' + (j.params.blockedReason || ''));
    else if (j.method === 'Runtime.exceptionThrown') errors.push(j.params.exceptionDetails.exception?.description || j.params.exceptionDetails.text);
    else if (j.method === 'Runtime.consoleAPICalled' && j.params.type === 'error') errors.push(j.params.args.map((a) => a.value ?? a.description).join(' '));
  };
  const call = (method, params = {}) =>
    new Promise((r) => {
      const n = ++id;
      wait.set(n, r);
      ws.send(JSON.stringify({ id: n, method, params }));
    });
  await call('Runtime.enable');
  await call('Page.enable');
  await call('Network.enable');
  const page = {
    errors,
    call,
    // evaluate an expression (awaits promises), returns its value
    async eval(expr) {
      const r = await call('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
      if (r.result.exceptionDetails) throw new Error(r.result.exceptionDetails.exception?.description || r.result.exceptionDetails.text);
      return r.result.result.value;
    },
    async until(expr, ms = 5000) {
      const t = Date.now();
      while (Date.now() - t < ms) {
        if (await page.eval(expr).catch(() => false)) return true;
        await sleep(50);
      }
      throw new Error('timeout waiting for: ' + expr);
    },
    async key(key, mods = 0) {
      const code = key.length === 1 ? 'Key' + key.toUpperCase() : key;
      const vk = { Enter: 13, Escape: 27, F2: 113, Delete: 46 }[key] || key.toUpperCase().charCodeAt(0);
      for (const type of ['rawKeyDown', 'keyUp']) await call('Input.dispatchKeyEvent', { type, key, code, modifiers: mods, windowsVirtualKeyCode: vk });
    },
    async type(text) {
      await call('Input.insertText', { text });
    },
    close() {
      ws.close();
    },
  };
  return page;
}

// a tiny runner: test('name', async () => {...}); await run()
const tests = [];
export const test = (name, fn) => tests.push({ name, fn });
export async function run() {
  let fail = 0;
  for (const t of tests) {
    const t0 = Date.now();
    try {
      await t.fn();
      console.log(`ok   ${t.name} (${Date.now() - t0} ms)`);
    } catch (e) {
      fail++;
      console.log(`FAIL ${t.name}: ${e.message}`);
    }
  }
  console.log(fail ? `${fail} failed` : 'all passed');
  return fail;
}
export function assert(c, msg) {
  if (!c) throw new Error(msg || 'assertion failed');
}
