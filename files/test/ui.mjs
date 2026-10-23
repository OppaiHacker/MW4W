// node test/ui.mjs <url from --serve> <empty playground folder>
import { mkdirSync, writeFileSync, existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { open, test, run, assert } from './cdp.mjs';

const [url, play] = process.argv.slice(2);
const p = await open(url);
await p.until('S.init && S.path', 15000);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const goto = async (dir) => {
  await p.eval(`go(${JSON.stringify(dir)})`);
  await p.until(`S.path === ${JSON.stringify(dir)}`);
};
const names = () => p.eval('S.view.map((it) => it.name)');

test('new folder shows at once (Ctrl+Shift+N)', async () => {
  await goto(play);
  for (const n of ['Untitled Folder', 'second']) {
    await p.eval('scroller.focus()');
    await p.key('N', 2 | 8); // Ctrl+Shift
    await p.until(`$('#dlg').classList.contains('on')`);
    await p.eval(`$('#dlg input').select()`);
    await p.type(n);
    await p.key('Enter');
    await p.until(`S.view.some((it) => it.name === ${JSON.stringify(n)})`, 3000);
    assert(existsSync(join(play, n)), 'not on disk');
    const sel = await p.eval('[...S.sel]');
    assert(sel.length === 1 && sel[0].endsWith(n), 'new folder not selected: ' + sel);
  }
});

test('new file shows at once', async () => {
  await p.eval(`void create('newfile')`);
  await p.until(`$('#dlg').classList.contains('on')`);
  await p.eval(`$('#dlg input').select()`);
  await p.type('notes.txt');
  await p.key('Enter');
  await p.until(`S.view.some((it) => it.name === 'notes.txt')`, 3000);
});

test('rename shows at once', async () => {
  const i = await p.eval(`S.view.findIndex((it) => it.name === 'notes.txt')`);
  await p.eval(`select(${i}); rename(${i})`);
  await p.until(`document.querySelector('input.rn')`);
  await p.eval(`document.querySelector('input.rn').select()`);
  await p.type('renamed.txt');
  await p.key('Enter');
  await p.until(`S.view.some((it) => it.name === 'renamed.txt') && !S.view.some((it) => it.name === 'notes.txt')`, 3000);
});

test('changes made elsewhere show up', async () => {
  mkdirSync(join(play, 'from-outside'));
  writeFileSync(join(play, 'outside.txt'), 'x');
  await p.until(`S.view.some((it) => it.name === 'from-outside') && S.view.some((it) => it.name === 'outside.txt')`, 4000);
});

test('a file written in a subfolder updates its parent row only when needed', async () => {
  // writing into a subfolder doesn't change the folder's own stamp: nothing to do, no error
  writeFileSync(join(play, 'second', 'inner.txt'), 'x');
  await sleep(300);
  assert((await names()).includes('second'));
});

test('dialogs do not pile up listeners', async () => {
  for (let i = 0; i < 3; i++) {
    const r = p.eval(`confirmDlg('t', 'x', 'OK')`);
    await p.until(`$('#dlg').classList.contains('on')`);
    await p.eval(`$('#dlg [data-r="1"]').click()`);
    assert((await r) === true);
  }
  // a stale listener would resolve with an old value or throw
  const r = p.eval(`promptDlg('t', 'x', 'abc', 'OK')`);
  await p.until(`$('#dlg').classList.contains('on')`);
  await p.eval(`$('#dlg [data-r="1"]').click()`);
  assert((await r) === 'abc');
});

test('convert from the menu: JSON -> YAML lands next to it', async () => {
  writeFileSync(join(play, 'people.json'), '[{"name":"Ala","age":30}]');
  await p.until(`S.view.some((it) => it.name === 'people.json')`, 4000);
  const i = await p.eval(`S.view.findIndex((it) => it.name === 'people.json')`);
  await p.eval(`select(${i}); void convertMenu(10, 10)`);
  await p.until(`menuEl && [...menuEl.querySelectorAll('button')].some((b) => b.textContent.startsWith('YAML'))`);
  await p.eval(`[...menuEl.querySelectorAll('button')].find((b) => b.textContent.startsWith('YAML')).click()`);
  await p.until(`S.view.some((it) => it.name === 'people.yaml')`, 8000);
  assert(readFileSync(join(play, 'people.yaml'), 'utf8').includes('name: Ala'));
  // mixed files: only an archive of them all
  const ts = await p.eval(`api('/api/convert?p=' + enc(S.path + S.sep + 'people.json') + '&p=' + enc(S.path + S.sep + 'renamed.txt')).then((r) => r.targets.map((t) => t.id))`);
  assert(ts.join() === 'zip-all', 'mixed: ' + ts);
});

test('no page errors', async () => {
  assert(p.errors.length === 0, p.errors.join('\n'));
});

const fail = await run();
p.close();
process.exit(fail ? 1 : 0);
