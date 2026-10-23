// The real app (its own hidden WebView2 window), driven over DevTools: drops from other apps.
//   node test/app.mjs <mw4w-files.exe>
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, existsSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { attach, test, run, assert } from './cdp.mjs';

const exe = process.argv[2];
const root = mkdtempSync(join(tmpdir(), 'mw4w-app-'));
const data = join(root, 'data');
const src = join(root, 'src');
const dest = join(root, 'dest');
for (const d of [data, src, dest, join(src, 'folder')]) mkdirSync(d, { recursive: true });
writeFileSync(join(src, 'a.txt'), 'a');
writeFileSync(join(src, 'b c.txt'), 'b');
writeFileSync(join(src, 'moved.txt'), 'm');
writeFileSync(join(src, 'folder', 'in.txt'), 'f');

const port = 9800 + Math.floor(Math.random() * 100);
const app = spawn(exe, ['--background'], {
  env: { ...process.env, MW4W_FILES_DATA: data, WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS: `--remote-debugging-port=${port}` },
  stdio: 'ignore',
});
const p = await attach(port, (t) => t.type === 'page' && t.url.includes('127.0.0.1') && t.url.includes('warm=1'));
await p.until('S.init && S.path', 15000);
await p.eval(`go(${JSON.stringify(dest)})`);
await p.until(`S.path === ${JSON.stringify(dest)}`);

// a drag from Explorer, as Chromium sees it: real files, dropped on the folder view
async function drop(files, modifiers = 0) {
  const data = { items: [], files, dragOperationsMask: 1 | 2 | 16 };
  const at = await p.eval(`(() => { const r = scroller.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height - 20]; })()`);
  for (const type of ['dragEnter', 'dragOver', 'drop']) {
    await p.call('Input.dispatchDragEvent', { type, x: at[0], y: at[1], data, modifiers });
  }
}

test('files dropped from another app are copied in (real paths, not uploads)', async () => {
  await drop([join(src, 'a.txt'), join(src, 'b c.txt')]);
  await p.until(`S.view.some((it) => it.name === 'a.txt') && S.view.some((it) => it.name === 'b c.txt')`, 8000);
  assert(existsSync(join(src, 'a.txt')), 'a copy must keep the original');
});

test('a dropped folder comes with its contents', async () => {
  await drop([join(src, 'folder')]);
  await p.until(`S.view.some((it) => it.name === 'folder' && it.dir)`, 8000);
  assert(readFileSync(join(dest, 'folder', 'in.txt'), 'utf8') === 'f');
});

test('Shift moves', async () => {
  await drop([join(src, 'moved.txt')], 8);
  await p.until(`S.view.some((it) => it.name === 'moved.txt')`, 8000);
  assert(!existsSync(join(src, 'moved.txt')), 'still in the source');
});

test('a file with no path (from a browser) is saved', async () => {
  await p.eval(`dropped({ dest: S.path, move: false, files: [new File(['hello'], 'from-web.txt')] }, [''])`);
  await p.until(`S.view.some((it) => it.name === 'from-web.txt')`, 5000);
  assert(readFileSync(join(dest, 'from-web.txt'), 'utf8') === 'hello');
});

test('upload refuses bad names', async () => {
  const r = await p.eval(`fetch('/api/upload?dest=' + enc(S.path) + '&name=' + enc('..\\\\evil.txt'), { method: 'POST', body: 'x' }).then((r) => r.status)`);
  assert(r === 400, 'status ' + r);
});

test('thumbnails and previews load from the media port (picture, PDF)', async () => {
  const png = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';
  writeFileSync(join(dest, 'pic.png'), Buffer.from(png, 'base64'));
  writeFileSync(join(dest, 'doc.pdf'), '%PDF-1.1\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj 2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj 3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 100 100]>>endobj trailer<</Root 1 0 R>>\n%%EOF');
  await p.eval('refresh(true)'); // the warm window is hidden: it doesn't poll
  await p.until(`S.view.some((it) => it.name === 'pic.png') && S.view.some((it) => it.name === 'doc.pdf')`, 8000);
  const n = await p.eval(`new Promise((r) => { const i = new Image(); i.onload = () => r(i.naturalWidth); i.onerror = () => r(-1); i.src = media() + '/thumb?p=' + enc(S.path + '\\\\pic.png') + '&s=64'; })`);
  assert(n > 0, 'thumbnail blocked');
  const show = (name) => p.eval(`(() => { S.sel = new Set([S.view.find((x) => x.name === '${name}').path]); updatePane(); })()`);
  await show('pic.png');
  await p.until(`$('#pv img') && $('#pv img').src.startsWith(media()) && $('#pv img').complete`, 5000);
  assert((await p.eval(`$('#pv img').naturalWidth`)) === 1, 'preview picture blocked');
  await show('doc.pdf');
  await p.until(`$('#pv iframe')`, 5000);
  await new Promise((r) => setTimeout(r, 1000));
  assert(!p.errors.length, p.errors.join('\n'));
});

test('no page errors', async () => {
  assert(p.errors.length === 0, p.errors.join('\n'));
});

const fail = await run();
p.close();
const si = JSON.parse(readFileSync(join(data, 'server.json'), 'utf8'));
await fetch(`http://127.0.0.1:${si.port}/api/quit`, { method: 'POST', headers: { 'X-Token': si.token } }).catch(() => {});
setTimeout(() => {
  app.kill();
  rmSync(root, { recursive: true, force: true });
  process.exit(fail ? 1 : 0);
}, 1500);
