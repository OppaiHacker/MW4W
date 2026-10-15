// MW4W Files UI. Talks to the resident mw4w-files process over its local API; the window host
// (WebView2) gets "drag", "max", "close", "size:<ht>", "title:<t>" messages.
'use strict';

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const host = window.chrome && window.chrome.webview;
const send = (m) => host && host.postMessage(m);
const enc = encodeURIComponent;
const coll = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

async function api(path, body) {
  const r = await fetch(path, body === undefined ? undefined : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}
const op = (o) => api('/api/op', o);

// ---------------------------------------------------------------- state
const S = {
  init: null,
  sep: '/',
  mode: 'dir', // dir | search | recent | starred | trash | computer
  path: '',
  stamp: 0,
  items: [], // everything in the location
  view: [], // shown, sorted
  sel: new Set(), // item.path
  focus: -1,
  anchor: -1,
  back: [],
  fwd: [],
  prefs: { view: 'list', sort: 'name', desc: false, hidden: false, preview: false, dirsFirst: true, zoom: 1, pane: 380 },
  q: { text: '', where: 'here', content: false, base: '' },
  counts: new Map(), // folder path -> item count
  cache: new Map(), // folder path -> {data, t}
  cut: new Set(),
  token: 0, // navigation generation
};

const icons = {
  desktop: 'i-pc', documents: 'i-doc', downloads: 'i-dl', music: 'i-music', pictures: 'i-img', videos: 'i-video', home: 'i-home',
};
const kindColor = {
  image: '#9a6cf0', video: '#e5534b', audio: '#f0883e', archive: '#2ea043', code: '#1f9e8f', text: '#7d8590', markdown: '#5b6a7c',
  pdf: '#e5484d', doc: '#3b82f6', font: '#d29922', exe: '#57606a', file: '#6e7681', csv: '#2ea043',
};
const kindGlyph = {
  image: 'i-img', video: 'i-play', audio: 'i-music', archive: 'i-zip', code: 'i-code', text: 'i-lines', markdown: 'i-lines',
  pdf: 'i-pdf', doc: 'i-lines', font: 'i-font', exe: 'i-gear', csv: 'i-grid',
};
const kindName = {
  dir: 'Folder', image: 'Image', video: 'Video', audio: 'Audio', archive: 'Archive', code: 'Source code', text: 'Text',
  markdown: 'Markdown', pdf: 'PDF document', doc: 'Document', font: 'Font', exe: 'Program', file: 'File',
};

// ---------------------------------------------------------------- formatting
function fmtSize(n) {
  if (n < 1000) return n === 1 ? '1 byte' : `${n} bytes`;
  const u = ['kB', 'MB', 'GB', 'TB'];
  let i = -1;
  do {
    n /= 1000;
    i++;
  } while (n >= 1000 && i < u.length - 1);
  return `${n.toFixed(1)} ${u[i]}`;
}
const fmtItems = (n) => (n < 0 ? '—' : n === 1 ? '1 item' : `${n.toLocaleString()} items`);

function fmtDate(t) {
  if (!t) return '';
  const d = new Date(t * 1000);
  const now = new Date();
  const day = 864e5;
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const days = Math.floor((start - new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()) / day);
  if (days <= 0) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  if (days === 1) return 'Yesterday';
  if (days < 7) return `${days} days ago`;
  if (days < 14) return 'Last week';
  if (days < 30) return `${Math.floor(days / 7)} weeks ago`;
  if (days < 60) return 'Last month';
  if (days < 365) return `${Math.floor(days / 30)} months ago`;
  if (days < 730) return 'Last year';
  return `${Math.floor(days / 365)} years ago`;
}
const fmtFull = (t) => (t ? new Date(t * 1000).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '');

const join = (dir, name) => (dir.endsWith(S.sep) ? dir + name : dir + S.sep + name);
const baseName = (p) => {
  const s = p.replace(/[\\/]+$/, '');
  const i = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'));
  return i < 0 ? s : s.slice(i + 1) || s;
};
const dirName = (p) => {
  const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'));
  if (i <= 0) return S.sep === '/' ? '/' : p;
  const d = p.slice(0, i);
  return /^[A-Za-z]:$/.test(d) ? d + '\\' : d;
};
const lower = (s) => (S.init && S.init.windows ? s.toLowerCase() : s);
const under = (p, root) => {
  p = lower(p);
  root = lower(root);
  return p === root || p.startsWith(root.endsWith(S.sep) ? root : root + S.sep);
};
function prettyPath(p) {
  const h = S.init.home;
  if (under(p, h)) return '~' + p.slice(h.length);
  return p;
}

// ---------------------------------------------------------------- icons
const svgUse = (id, cls = 'i') => `<svg class="${cls}"><use href="#${id}"/></svg>`;
const thumbable = (it) => it.size > 0 && ['image', 'video', 'pdf', 'doc', 'font'].includes(it.kind) || (S.init.windows && it.kind === 'exe');

function iconHTML(it, big) {
  let h;
  if (it.dir) {
    const sp = S.special.get(lower(it.path));
    h = svgUse('t-folder', 't') + (sp ? svgUse(sp, 'i g') : '');
  } else {
    const g = kindGlyph[it.kind];
    h = `<svg class="t" style="--k:${kindColor[it.kind] || kindColor.file}"><use href="#t-file"/></svg>` + (g ? svgUse(g, 'i g f') : '');
    if (thumbable(it) && (big || it.kind === 'image' || it.kind === 'video' || it.kind === 'exe')) {
      const px = big ? (S.prefs.zoom > 1.2 ? 512 : 256) : 64;
      h += `<img loading="lazy" decoding="async" draggable="false" class="${it.kind === 'exe' ? 'icon' : 'photo'}" src="/thumb?p=${enc(it.path)}&s=${px}&v=${it.mtime}-${it.size}">`;
    }
  }
  if (it.link) h += svgUse('i-link', 'i link');
  return `<span class="ico">${h}</span>`;
}
// thumbnails fade in when loaded; a missing one leaves the icon (no inline handlers: CSP)
document.addEventListener('load', (e) => e.target.tagName === 'IMG' && e.target.closest('.ico') && e.target.classList.add('ok'), true);
document.addEventListener('error', (e) => e.target.tagName === 'IMG' && e.target.closest('.ico') && e.target.remove(), true);

// ---------------------------------------------------------------- loading locations
function rowsToItems(d) {
  const out = new Array(d.entries.length);
  for (let i = 0; i < d.entries.length; i++) {
    const [name, fl, size, mtime, kind] = d.entries[i];
    out[i] = { name, path: join(d.path, name), dir: !!(fl & 1), hidden: !!(fl & 2), link: !!(fl & 4), starred: !!(fl & 8), size, mtime, kind };
  }
  return out;
}

async function fetchDir(path) {
  const d = await api('/api/list?p=' + enc(path));
  S.cache.set(d.path, { d, t: performance.now() });
  if (S.cache.size > 60) S.cache.delete(S.cache.keys().next().value);
  return d;
}

function hitsToItems(hits) {
  return (hits || []).map((h) => ({
    name: h.name, path: join(h.dir, h.name), loc: h.dir, dir: h.isDir, size: h.size, mtime: h.mtime, kind: h.kind,
    starred: h.starred, hidden: false, line: h.line, text: h.text,
  }));
}

function here() {
  return { mode: S.mode, path: S.path };
}

function pushHistory(to) {
  const cur = here();
  if (cur.mode === to.mode && cur.path === to.path) return;
  if (cur.mode !== 'search' && (cur.path || cur.mode !== 'dir')) S.back.push(cur);
  if (S.back.length > 200) S.back.shift();
  S.fwd = [];
}

async function go(path, opts = {}) {
  const gen = ++S.token;
  const cached = S.cache.get(path);
  let d = cached && cached.d;
  try {
    if (!d) d = await fetchDir(path);
  } catch (e) {
    toast(e.message, { err: true });
    return;
  }
  if (gen !== S.token) return;
  if (opts.push !== false) pushHistory({ mode: 'dir', path: d.path });
  leaveSearch(false);
  S.mode = 'dir';
  S.path = d.path;
  S.stamp = d.stamp;
  setItems(rowsToItems(d), { keepScroll: false, select: opts.select || d.select, scrollTop: opts.scrollTop });
  send('title:' + (baseName(d.path) || d.path));
  // a cached listing shows at once; then the real one
  if (cached && performance.now() - cached.t > 300) refresh(true);
}

async function refresh(quiet) {
  if (S.mode === 'dir') {
    const gen = S.token;
    try {
      const d = await fetchDir(S.path);
      if (gen !== S.token || S.mode !== 'dir') return;
      S.stamp = d.stamp;
      setItems(rowsToItems(d), { keepScroll: true, keepSel: true });
    } catch (e) {
      if (!quiet) toast(e.message, { err: true });
      // the folder is gone: up until one exists
      if (/Not found/.test(e.message)) go(dirName(S.path), { push: false });
    }
  } else if (S.mode === 'search') runSearch();
  else if (S.mode === 'recent' || S.mode === 'starred' || S.mode === 'trash') openSpecial(S.mode, { push: false, keep: true });
  else if (S.mode === 'computer') openSpecial('computer', { push: false });
}

async function openSpecial(mode, opts = {}) {
  const gen = ++S.token;
  if (opts.push !== false) pushHistory({ mode, path: '' });
  leaveSearch(false);
  S.mode = mode;
  S.path = '';
  send('title:' + { recent: 'Recent', starred: 'Starred', trash: 'Trash', computer: 'Computer' }[mode]);
  if (mode === 'computer') {
    S.items = [];
    S.view = [];
    renderAll();
    renderComputer();
    return;
  }
  let items;
  try {
    if (mode === 'trash') {
      const t = await api('/api/trash');
      items = t.map((x) => ({ name: x.name, path: x.path, dir: x.isDir, size: x.size, mtime: x.deleted, kind: x.kind, loc: dirName(x.orig), trash: x }));
    } else {
      items = hitsToItems((await api('/api/' + mode)).hits);
    }
  } catch (e) {
    toast(e.message, { err: true });
    items = [];
  }
  if (gen !== S.token) return;
  setItems(items, { keepScroll: opts.keep, keepSel: opts.keep });
}

function goBack() {
  const to = S.back.pop();
  if (!to) return;
  S.fwd.push(here());
  navTo(to);
}
function goFwd() {
  const to = S.fwd.pop();
  if (!to) return;
  S.back.push(here());
  navTo(to);
}
function navTo(to) {
  const prev = S.path;
  if (to.mode === 'dir') go(to.path, { push: false, select: prev && dirName(prev) === to.path ? baseName(prev) : undefined });
  else openSpecial(to.mode, { push: false });
}
function goUp() {
  if (S.mode !== 'dir') return;
  const p = dirName(S.path);
  if (p && p !== S.path) go(p, { select: baseName(S.path) });
}

// ---------------------------------------------------------------- sorting & items
const sig = (items) => items.map((it) => it.name + '|' + it.size + '|' + it.mtime + '|' + it.starred + '|' + it.hidden).join('/');

function setItems(items, o = {}) {
  // a refresh that changed nothing leaves the DOM alone (a click in progress survives it)
  if (o.keepSel && o.keepScroll && S.items.length === items.length && sig(S.items) === sig(items)) return;
  const prevSel = o.keepSel ? new Set(S.sel) : null;
  const focusPath = o.keepSel && S.view[S.focus] ? S.view[S.focus].path : null;
  S.items = items;
  sortView();
  S.sel = new Set();
  S.focus = -1;
  S.anchor = -1;
  if (prevSel) {
    S.view.forEach((it, i) => {
      if (prevSel.has(it.path)) S.sel.add(it.path);
      if (it.path === focusPath) S.focus = i;
    });
  }
  if (o.select) {
    const i = S.view.findIndex((it) => it.name === o.select);
    if (i >= 0) {
      S.sel.add(S.view[i].path);
      S.focus = S.anchor = i;
    }
  }
  if (!o.keepScroll) scroller.scrollTop = o.scrollTop || 0;
  renderAll();
  if (o.select && S.focus >= 0) reveal(S.focus);
  if (S.prefs.preview) updatePane();
}

function sortView() {
  const p = S.prefs;
  let v = p.hidden || S.mode !== 'dir' ? S.items.slice() : S.items.filter((it) => !it.hidden);
  if (S.mode === 'search' && !S.q.content) {
    // relevance order from the index; folders first is still nice
  } else if (S.mode === 'recent' || S.mode === 'trash') {
    if (p.sort === 'name') v.sort((a, b) => b.mtime - a.mtime);
    else v.sort(cmp);
  } else v.sort(cmp);
  if (p.dirsFirst && S.mode !== 'search' && S.mode !== 'recent') v.sort((a, b) => b.dir - a.dir);
  S.view = v;
}

function cmp(a, b) {
  const p = S.prefs;
  let r = 0;
  if (p.sort === 'size') {
    const sa = a.dir ? S.counts.get(a.path) ?? -1 : a.size;
    const sb = b.dir ? S.counts.get(b.path) ?? -1 : b.size;
    r = a.dir !== b.dir ? 0 : sa - sb;
  } else if (p.sort === 'mtime') r = a.mtime - b.mtime;
  else if (p.sort === 'kind') r = coll.compare(a.kind, b.kind) || coll.compare(extOf(a.name), extOf(b.name));
  if (!r) r = coll.compare(a.name, b.name);
  return p.desc ? -r : r;
}
const extOf = (n) => (n.lastIndexOf('.') > 0 ? n.slice(n.lastIndexOf('.') + 1).toLowerCase() : '');

// ---------------------------------------------------------------- rendering (virtual)
const scroller = $('#scroller');
const rowsEl = $('#rows');
const sizer = $('#sizer');
let pool = new Map(); // index -> element
let geom = { rh: 52, cw: 124, ch: 152, cols: 1, pad: 8 };
let rafPending = false;

function measure() {
  const z = S.prefs.zoom;
  const twoLine = S.mode === 'search' || S.mode === 'recent' || S.mode === 'starred' || S.mode === 'trash';
  geom.rh = Math.round((twoLine ? 58 : 52) * z);
  geom.cw = Math.round(124 * z);
  geom.ch = Math.round(152 * z);
  const w = scroller.clientWidth - 16;
  geom.cols = Math.max(1, Math.floor(w / geom.cw));
  geom.gap = Math.floor((w - geom.cols * geom.cw) / geom.cols);
}

function renderAll() {
  const app = document.documentElement;
  app.classList.toggle('grid', S.prefs.view === 'grid');
  $('#app').classList.toggle('searching', S.mode === 'search');
  $('#main').classList.toggle('searching', S.mode === 'search');
  $('#main').classList.toggle('trash', S.mode === 'trash');
  $('#main').classList.toggle('computer', S.mode === 'computer');
  $('#app').classList.toggle('preview', !!S.prefs.preview && S.mode !== 'computer');
  $('#b-view use').setAttribute('href', S.prefs.view === 'grid' ? '#i-list' : '#i-grid');
  for (const el of pool.values()) el.remove();
  pool.clear();
  measure();
  renderCols();
  renderCrumbs();
  renderPlacesActive();
  renderEmpty();
  renderTrashBar();
  $('#b-back').disabled = !S.back.length;
  $('#b-fwd').disabled = !S.fwd.length;
  paint();
  updateBubble();
}

function renderEmpty() {
  const e = $('#empty');
  const on = S.view.length === 0 && S.mode !== 'computer';
  e.classList.toggle('on', on);
  if (!on) return;
  const [ic, t, s] = {
    dir: ['i-folder', 'Folder is Empty', ''],
    search: ['i-search', S.q.text ? 'No Results Found' : 'Search Everywhere', S.q.text ? 'Try a different search' : 'Names from the index · ext:pdf · kind:image'],
    recent: ['i-clock', 'No Recent Files', ''],
    starred: ['i-star', 'No Starred Files', 'Star files and folders to find them here'],
    trash: ['i-trash', 'Trash is Empty', ''],
  }[S.mode] || ['i-folder', '', ''];
  e.innerHTML = `${svgUse(ic)}<b>${t}</b><span>${s}</span>`;
}

function paint() {
  rafPending = false;
  const n = S.view.length;
  const grid = S.prefs.view === 'grid';
  const top = scroller.scrollTop;
  const h = scroller.clientHeight;
  let first, last;
  if (grid) {
    const rows = Math.ceil(n / geom.cols);
    sizer.style.height = rows * geom.ch + 16 + 'px';
    const r0 = Math.max(0, Math.floor(top / geom.ch) - 2);
    const r1 = Math.min(rows, Math.ceil((top + h) / geom.ch) + 2);
    first = r0 * geom.cols;
    last = Math.min(n, r1 * geom.cols);
  } else {
    sizer.style.height = n * geom.rh + 16 + 'px';
    first = Math.max(0, Math.floor(top / geom.rh) - 6);
    last = Math.min(n, Math.ceil((top + h) / geom.rh) + 6);
  }
  for (const [i, el] of pool) {
    if (i < first || i >= last) {
      el.remove();
      pool.delete(i);
    }
  }
  const frag = document.createDocumentFragment();
  const need = [];
  for (let i = first; i < last; i++) {
    let el = pool.get(i);
    if (!el) {
      el = grid ? makeCell(i) : makeRow(i);
      pool.set(i, el);
      frag.appendChild(el);
    }
    const it = S.view[i];
    if (it.dir && !S.counts.has(it.path) && S.mode !== 'trash') need.push(it.path);
  }
  rowsEl.appendChild(frag);
  if (need.length) wantCounts(need);
}
scroller.addEventListener('scroll', () => {
  if (!rafPending) {
    rafPending = true;
    requestAnimationFrame(paint);
  }
}, { passive: true });
new ResizeObserver(() => {
  const cols = geom.cols;
  measure();
  if (cols !== geom.cols && S.prefs.view === 'grid') renderAll();
  else paint();
}).observe(scroller);

function sizeText(it) {
  if (it.dir) return S.mode === 'trash' ? '' : S.counts.has(it.path) ? fmtItems(S.counts.get(it.path)) : '';
  return fmtSize(it.size);
}

function rowClasses(it, i) {
  return (S.sel.has(it.path) ? ' sel' : '') + (i === S.focus ? ' focus' : '') + (it.hidden ? ' hid' : '') + (S.cut.has(it.path) ? ' cut' : '');
}

function makeRow(i) {
  const it = S.view[i];
  const el = document.createElement('div');
  el.className = 'row' + rowClasses(it, i);
  el.dataset.i = i;
  el.draggable = true;
  el.style.transform = `translateY(${i * geom.rh + 4}px)`;
  let sub = '';
  if (it.text) sub = `<small class="line">${it.line ? it.line + ': ' : ''}${hl(it.text)}</small>`;
  else if (it.loc !== undefined) sub = `<small><span dir="ltr">${esc(prettyPath(it.loc))}</span></small>`;
  const date = S.mode === 'trash' ? fmtDate(it.mtime) : fmtDate(it.mtime);
  el.innerHTML =
    iconHTML(it, false) +
    `<span class="nm"><b>${esc(it.name)}</b>${sub}</span><span class="sz">${sizeText(it)}</span><span class="dt" title="${fmtFull(it.mtime)}">${date}</span>` +
    (S.mode === 'trash' ? '<span></span>' : `<button class="star${it.starred ? ' on' : ''}" title="Star">${svgUse('i-star')}</button>`);
  return el;
}

function makeCell(i) {
  const it = S.view[i];
  const el = document.createElement('div');
  el.className = 'cell' + rowClasses(it, i);
  el.dataset.i = i;
  el.draggable = true;
  const c = i % geom.cols;
  const r = Math.floor(i / geom.cols);
  el.style.transform = `translate(${8 + c * (geom.cw + geom.gap) + geom.gap / 2}px, ${r * geom.ch + 6}px)`;
  el.innerHTML = iconHTML(it, true) + `<span class="nm" title="${esc(it.name)}">${esc(it.name)}</span>` +
    (S.mode === 'trash' ? '' : `<button class="star${it.starred ? ' on' : ''}" title="Star">${svgUse('i-star')}</button>`);
  return el;
}

function hl(text) {
  let s = esc(text);
  for (const w of S.q.text.toLowerCase().split(/\s+/).filter((w) => w && !w.includes(':'))) {
    s = s.replace(new RegExp(w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi'), (m) => `<mark>${m}</mark>`);
  }
  return s;
}

function updateClasses() {
  for (const [i, el] of pool) {
    const it = S.view[i];
    if (!it) continue;
    const grid = el.classList.contains('cell');
    el.className = (grid ? 'cell' : 'row') + rowClasses(it, i);
  }
  updateBubble();
  if (S.prefs.preview) updatePane();
}

function repaintRow(i) {
  const el = pool.get(i);
  if (!el) return;
  const n = S.prefs.view === 'grid' ? makeCell(i) : makeRow(i);
  el.replaceWith(n);
  pool.set(i, n);
}

function reveal(i) {
  if (i < 0) return;
  const grid = S.prefs.view === 'grid';
  const top = grid ? Math.floor(i / geom.cols) * geom.ch : i * geom.rh;
  const h = grid ? geom.ch : geom.rh;
  if (top < scroller.scrollTop) scroller.scrollTop = top;
  else if (top + h + 12 > scroller.scrollTop + scroller.clientHeight) scroller.scrollTop = top + h + 12 - scroller.clientHeight;
  paint();
}

// folder item counts, read on demand for the rows on screen
let countQ = new Set();
let countT = 0;
function wantCounts(paths) {
  paths.forEach((p) => countQ.add(p));
  clearTimeout(countT);
  countT = setTimeout(async () => {
    const batch = [...countQ].slice(0, 200);
    batch.forEach((p) => countQ.delete(p));
    try {
      const r = await api('/api/counts', { paths: batch });
      for (const p in r) S.counts.set(p, r[p]);
    } catch {}
    for (const [i, el] of pool) {
      const it = S.view[i];
      if (it && it.dir && el.classList.contains('row')) {
        const sz = $('.sz', el);
        if (sz) sz.textContent = sizeText(it);
      }
    }
    if (countQ.size) wantCounts([]);
  }, 30);
}

function renderCols() {
  const k = { name: 'Name', size: 'Size', mtime: S.mode === 'trash' ? 'Deleted' : 'Modified' };
  $$('#cols button').forEach((b) => {
    const key = b.dataset.k;
    const on = S.prefs.sort === key;
    b.innerHTML = (key === 'name' && S.mode === 'trash' ? 'Name' : k[key]) + (on ? svgUse(S.prefs.desc ? 'i-down' : 'i-up') : '');
  });
  $('#cols').classList.toggle('loc', S.mode !== 'dir');
}

// ---------------------------------------------------------------- path bar
function crumbsFor(p) {
  const out = [];
  const home = S.init.home;
  let rest;
  if (under(p, home)) {
    out.push({ label: 'Home', path: home, icon: 'i-home' });
    rest = p.slice(home.length);
  } else {
    const m = p.match(/^([A-Za-z]:\\|\\\\[^\\]+\\[^\\]+\\?|\/)/);
    const root = m ? m[1] : p;
    out.push({ label: root === '/' ? '/' : root.replace(/\\$/, ''), path: root, icon: 'i-drive' });
    rest = p.slice(root.length);
  }
  let acc = out[0].path;
  for (const seg of rest.split(/[\\/]/).filter(Boolean)) {
    acc = join(acc, seg);
    out.push({ label: seg, path: acc });
  }
  return out;
}

function renderCrumbs() {
  const el = $('#crumbs');
  const pb = $('#pathbar');
  if (pb.classList.contains('editing') || S.mode === 'search') return;
  let cs;
  if (S.mode === 'dir') cs = crumbsFor(S.path);
  else cs = [{ label: { recent: 'Recent', starred: 'Starred', trash: 'Trash', computer: 'Computer' }[S.mode], icon: { recent: 'i-clock', starred: 'i-star', trash: 'i-trash', computer: 'i-pc' }[S.mode] }];
  el.innerHTML = cs
    .map((c, i) => (i ? `<svg class="i crumb-sep" style="width:12px"><use href="#i-right"/></svg>` : '') +
      `<button class="crumb${i === cs.length - 1 ? ' cur' : ''}" data-p="${c.path ? esc(c.path) : ''}">${c.icon ? svgUse(c.icon) : ''}${esc(c.label)}</button>`)
    .join('');
  el.classList.toggle('overflow', el.scrollWidth > el.clientWidth);
  $('#b-fmenu').style.display = S.mode === 'dir' ? '' : 'none';
}

$('#crumbs').addEventListener('click', (e) => {
  const b = e.target.closest('.crumb');
  if (b && b.dataset.p && !b.classList.contains('cur')) go(b.dataset.p);
  else if (S.mode === 'dir' && (!b || b.classList.contains('cur'))) editPath();
});

function editPath(text) {
  const pb = $('#pathbar');
  if (pb.classList.contains('editing')) return;
  pb.classList.add('editing');
  const el = $('#crumbs');
  el.innerHTML = `<input id="pathedit" spellcheck="false" autocomplete="off">`;
  const inp = $('#pathedit');
  inp.value = text ?? (S.mode === 'dir' ? S.path : '');
  inp.focus();
  if (text === undefined) inp.select();
  let sugg = [];
  let si = -1;
  const box = document.createElement('div');
  box.id = 'suggest';
  const done = () => {
    pb.classList.remove('editing');
    box.remove();
    renderCrumbs();
  };
  const showSugg = async () => {
    const v = inp.value;
    const cut = Math.max(v.lastIndexOf('\\'), v.lastIndexOf('/'));
    if (cut < 0) return box.remove();
    const dir = v.slice(0, cut + 1);
    const part = v.slice(cut + 1).toLowerCase();
    let d = S.cache.get(dir.length > 1 && !/^[A-Za-z]:\\$/.test(dir) ? dir.slice(0, -1) : dir)?.d;
    try {
      d = d || (await fetchDir(dir));
    } catch {
      return box.remove();
    }
    if (inp.value !== v) return;
    sugg = rowsToItems(d).filter((it) => it.dir && it.name.toLowerCase().startsWith(part) && (S.prefs.hidden || !it.hidden)).sort((a, b) => coll.compare(a.name, b.name)).slice(0, 12);
    si = -1;
    if (!sugg.length) return box.remove();
    box.innerHTML = sugg.map((s) => `<div data-p="${esc(s.path)}">${svgUse('i-folder')}${esc(s.name)}</div>`).join('');
    pb.appendChild(box);
  };
  inp.addEventListener('input', showSugg);
  box.addEventListener('mousedown', (e) => {
    const d = e.target.closest('[data-p]');
    if (d) {
      e.preventDefault();
      inp.value = d.dataset.p + S.sep;
      showSugg();
    }
  });
  inp.addEventListener('keydown', async (e) => {
    e.stopPropagation();
    if (e.key === 'Escape') return done();
    if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && sugg.length) {
      e.preventDefault();
      si = (si + (e.key === 'ArrowDown' ? 1 : -1) + sugg.length) % sugg.length;
      $$('#suggest div').forEach((d, i) => d.classList.toggle('on', i === si));
      return;
    }
    if (e.key === 'Tab' && sugg.length) {
      e.preventDefault();
      inp.value = sugg[Math.max(0, si)].path + S.sep;
      showSugg();
      return;
    }
    if (e.key === 'Enter') {
      let v = si >= 0 ? sugg[si].path : inp.value.trim();
      if (v.startsWith('~')) v = S.init.home + v.slice(1);
      if (/^shell:|^[a-z-]+:\/\//i.test(v)) {
        done();
        op({ op: 'open', paths: [v] }).catch((err) => toast(err.message, { err: true }));
        return;
      }
      done();
      if (v) go(v);
    }
  });
  inp.addEventListener('blur', () => setTimeout(done, 120));
}

// ---------------------------------------------------------------- sidebar
S.special = new Map();
function renderPlaces() {
  const I = S.init;
  S.special = new Map(I.known.map((k) => [lower(k.path), icons[k.icon] || 'i-folder']));
  S.special.set(lower(I.home), 'i-home');
  const pl = (id, ic, name, extra = '') => `<button class="pl" ${id} title="${esc(name)}">${svgUse(ic)}<span>${esc(name)}</span>${extra}</button>`;
  let h = pl(`data-p="${esc(I.home)}"`, 'i-home', 'Home') + pl('data-m="recent"', 'i-clock', 'Recent') + pl('data-m="starred"', 'i-star', 'Starred') +
    pl('data-m="computer"', 'i-pc', I.windows ? 'This PC' : 'Computer') + pl('data-m="trash"', 'i-trash', 'Trash');
  if (I.bookmarks && I.bookmarks.length) {
    h += '<div class="sep"></div>';
    for (const b of I.bookmarks) h += pl(`data-p="${esc(b.path)}" data-bm="1"`, icons[b.icon] || 'i-folder', b.name);
  }
  if (I.drives && I.drives.length) {
    h += '<div class="sep"></div>';
    for (const d of I.drives) {
      const used = d.total ? 1 - d.free / d.total : 0;
      h += pl(`data-p="${esc(d.path)}"`, d.icon === 'usb' ? 'i-usb' : d.icon === 'net' ? 'i-net' : 'i-drive', d.name,
        d.total ? `<span class="bar" title="${fmtSize(d.free)} free of ${fmtSize(d.total)}"><i class="${used > 0.9 ? 'full' : ''}" style="width:${(used * 100).toFixed(1)}%"></i></span>` : '');
    }
  }
  $('#places').innerHTML = h;
  renderPlacesActive();
}
function renderPlacesActive() {
  $$('#places .pl').forEach((b) => b.classList.toggle('on', S.mode === 'dir' ? b.dataset.p === S.path : b.dataset.m === S.mode));
}
$('#places').addEventListener('click', (e) => {
  const b = e.target.closest('.pl');
  if (!b) return;
  if (b.dataset.m) openSpecial(b.dataset.m);
  else go(b.dataset.p);
});
$('#places').addEventListener('auxclick', (e) => {
  const b = e.target.closest('.pl');
  if (b && b.dataset.p && e.button === 1) newWindow(b.dataset.p);
});
$('#places').addEventListener('contextmenu', (e) => {
  const b = e.target.closest('.pl');
  e.preventDefault();
  if (!b || !b.dataset.p) return;
  const p = b.dataset.p;
  menu(e.clientX, e.clientY, [
    ['i-ext', 'Open', () => go(p)],
    ['i-window', 'Open in New Window', () => newWindow(p)],
    ['i-term', 'Open in Terminal', () => op({ op: 'terminal', dest: p })],
    '-',
    b.dataset.bm ? ['i-x', 'Remove from Sidebar', () => setBookmarks(S.init.bookmarks.map((x) => x.path).filter((x) => x !== p))] : null,
    ['i-info', 'Properties', () => op({ op: 'props', paths: [p] }).catch((err) => toast(err.message, { err: true }))],
  ]);
});

async function setBookmarks(paths) {
  await op({ op: 'bookmarks', paths });
  S.init = { ...S.init, ...(await api('/api/init')) };
  renderPlaces();
}

// index status (bottom of the sidebar)
async function pollIndex() {
  try {
    const s = await api('/api/index');
    const el = $('#ixstat');
    el.classList.toggle('scan', s.scanning);
    $('.t', el).textContent = (s.scanning ? 'Indexing… ' : 'Indexed ') + s.files.toLocaleString() + ' items';
    el.title = `${s.dirs.toLocaleString()} folders in ${s.roots.join(', ')}${s.watching ? ' · live' : ''}\nClick to rescan`;
    setTimeout(pollIndex, s.scanning ? 1000 : 8000);
  } catch {
    setTimeout(pollIndex, 8000);
  }
}
$('#ixstat').addEventListener('click', () => api('/api/index', {}).then(() => setTimeout(pollIndex, 300)));

// ---------------------------------------------------------------- selection & mouse
function itemAt(e) {
  const el = e.target.closest('.row, .cell');
  return el ? +el.dataset.i : -1;
}
const selected = () => S.view.filter((it) => S.sel.has(it.path));

function select(i, e = {}) {
  if (i < 0) {
    if (!e.ctrlKey && !e.shiftKey) S.sel.clear();
    S.focus = -1;
  } else if (e.shiftKey && S.anchor >= 0) {
    if (!e.ctrlKey) S.sel.clear();
    const [a, b] = S.anchor < i ? [S.anchor, i] : [i, S.anchor];
    for (let k = a; k <= b; k++) S.sel.add(S.view[k].path);
    S.focus = i;
  } else if (e.ctrlKey) {
    const p = S.view[i].path;
    S.sel.has(p) ? S.sel.delete(p) : S.sel.add(p);
    S.focus = S.anchor = i;
  } else {
    S.sel.clear();
    S.sel.add(S.view[i].path);
    S.focus = S.anchor = i;
  }
  updateClasses();
}

rowsEl.addEventListener('mousedown', (e) => {
  if (e.target.closest('.star, input')) return;
  const i = itemAt(e);
  if (e.button === 0) {
    if (i >= 0 && S.sel.has(S.view[i].path) && !e.ctrlKey && !e.shiftKey) {
      S.pendingSelect = i; // keep a multi-selection for dragging; select on click
      return;
    }
    select(i, e);
  } else if (e.button === 2 && i >= 0 && !S.sel.has(S.view[i].path)) select(i);
});
scroller.addEventListener('mousedown', (e) => {
  if (e.target === scroller || e.target === sizer || e.target === rowsEl || e.target.id === 'empty') {
    if (e.button === 0) select(-1, e);
    scroller.focus();
  }
});
rowsEl.addEventListener('click', (e) => {
  const star = e.target.closest('.star');
  if (star) {
    const it = S.view[itemAt(e)];
    if (it) toggleStar([it], !it.starred);
    return;
  }
  if (S.pendingSelect !== undefined) {
    select(S.pendingSelect);
    S.pendingSelect = undefined;
  }
});
rowsEl.addEventListener('dblclick', (e) => {
  if (e.target.closest('.star, input')) return;
  const i = itemAt(e);
  if (i >= 0) openItems([S.view[i]]);
});
rowsEl.addEventListener('auxclick', (e) => {
  const it = S.view[itemAt(e)];
  if (e.button === 1 && it && it.dir) newWindow(it.path);
});
// hovering a folder fetches it, so opening it is instant
let hoverT = 0;
rowsEl.addEventListener('mouseover', (e) => {
  const it = S.view[itemAt(e)];
  clearTimeout(hoverT);
  if (!it || !it.dir || S.mode === 'trash') return;
  hoverT = setTimeout(() => {
    const c = S.cache.get(it.path);
    if (!c || performance.now() - c.t > 4000) fetchDir(it.path).catch(() => {});
  }, 70);
});
scroller.addEventListener('contextmenu', (e) => {
  e.preventDefault();
  const i = itemAt(e);
  if (i >= 0) itemMenu(e.clientX, e.clientY);
  else {
    select(-1);
    bgMenu(e.clientX, e.clientY);
  }
});

// ---------------------------------------------------------------- drag & drop (inside the app)
let dragPaths = null;
rowsEl.addEventListener('dragstart', (e) => {
  const i = itemAt(e);
  if (i < 0 || S.mode === 'trash') return e.preventDefault();
  if (!S.sel.has(S.view[i].path)) select(i);
  S.pendingSelect = undefined;
  dragPaths = selected().map((it) => it.path);
  e.dataTransfer.effectAllowed = 'copyMove';
  e.dataTransfer.setData('text/plain', dragPaths.join('\n'));
});
document.addEventListener('dragend', () => {
  dragPaths = null;
  $$('.drop').forEach((x) => x.classList.remove('drop'));
});
function dropTarget(e) {
  const row = e.target.closest('.row, .cell');
  if (row) {
    const it = S.view[+row.dataset.i];
    if (it && it.dir && !dragPaths.includes(it.path)) return { el: row, path: it.path };
    return null;
  }
  const pl = e.target.closest('.pl[data-p], .crumb[data-p]');
  if (pl && pl.dataset.p) return { el: pl, path: pl.dataset.p };
  if (e.target.closest('#scroller') && S.mode === 'dir') return { el: null, path: S.path };
  return null;
}
document.addEventListener('dragover', (e) => {
  if (!dragPaths) return;
  const t = dropTarget(e);
  $$('.drop').forEach((x) => x !== (t && t.el) && x.classList.remove('drop'));
  if (!t || (!t.el && dragPaths.every((p) => dirName(p) === t.path))) return;
  e.preventDefault();
  e.dataTransfer.dropEffect = e.ctrlKey ? 'copy' : 'move';
  if (t.el) t.el.classList.add('drop');
});
document.addEventListener('drop', (e) => {
  if (!dragPaths) return;
  e.preventDefault();
  const t = dropTarget(e);
  const paths = dragPaths;
  dragPaths = null;
  $$('.drop').forEach((x) => x.classList.remove('drop'));
  if (t) transfer(paths, t.path, !e.ctrlKey);
});
// a folder dragged onto the sidebar's empty part becomes a bookmark
$('#places').addEventListener('dragover', (e) => {
  if (dragPaths && !e.target.closest('.pl')) e.preventDefault();
});
$('#places').addEventListener('drop', (e) => {
  if (!dragPaths || e.target.closest('.pl')) return;
  e.preventDefault();
  e.stopPropagation();
  const dirs = S.view.filter((it) => it.dir && dragPaths.includes(it.path)).map((it) => it.path);
  dragPaths = null;
  if (dirs.length) setBookmarks([...S.init.bookmarks.map((b) => b.path), ...dirs]);
});

// ---------------------------------------------------------------- actions
async function openItems(items) {
  if (!items.length) return;
  if (S.mode === 'trash') return quickLook(true);
  const dirs = items.filter((it) => it.dir);
  if (items.length === 1 && dirs.length === 1) return go(dirs[0].path);
  for (const d of dirs.slice(1)) newWindow(d.path);
  if (dirs.length) go(dirs[0].path);
  for (const f of items.filter((it) => !it.dir)) {
    op({ op: 'open', paths: [f.path] }).catch((e) => toast(e.message, { err: true }));
  }
}

function newWindow(path) {
  api('/api/window', { path: path || '' }).catch(() => window.open('/?p=' + enc(path || ''), '_blank'));
}

async function run(label, fn, after) {
  let t = null;
  const timer = setTimeout(() => (t = toast(label, { spin: true, sticky: true })), 250);
  try {
    const r = await fn();
    if (after) after(r);
    return r;
  } catch (e) {
    toast(e.message, { err: true });
  } finally {
    clearTimeout(timer);
    if (t) t.remove();
    refresh(true);
  }
}

function transfer(paths, dest, move) {
  const n = paths.length;
  return run(`${move ? 'Moving' : 'Copying'} ${n === 1 ? '“' + baseName(paths[0]) + '”' : n + ' items'}…`, () => op({ op: move ? 'move' : 'copy', paths, dest }));
}

async function trashItems(items) {
  if (!items.length) return;
  const paths = items.map((it) => it.path);
  const r = await run('Moving to Trash…', () => op({ op: 'trash', paths }));
  if (r === undefined) return;
  toast(items.length === 1 ? `“${items[0].name}” moved to Trash` : `${items.length} items moved to Trash`, {
    action: 'Undo',
    onAction: () => run('Restoring…', () => op({ op: 'untrash', paths })),
  });
}

async function deleteItems(items) {
  if (!items.length) return;
  const ok = await confirmDlg(items.length === 1 ? `Permanently Delete “${items[0].name}”?` : `Permanently Delete ${items.length} Items?`,
    'Permanently deleted items can not be restored.', 'Delete', true);
  if (!ok) return;
  if (S.mode === 'trash') run('Deleting…', () => op({ op: 'purge', paths: items.map((it) => it.trash.id) }));
  else run('Deleting…', () => op({ op: 'delete', paths: items.map((it) => it.path) }));
}

async function toggleStar(items, on) {
  await op({ op: 'star', paths: items.map((it) => it.path), on });
  items.forEach((it) => (it.starred = on));
  for (const [i] of pool) if (items.includes(S.view[i])) repaintRow(i);
  if (S.mode === 'starred') refresh(true);
}

async function clip(cut) {
  const items = selected();
  if (!items.length) return;
  const paths = items.map((it) => it.path);
  await op({ op: 'clip', paths, cut });
  S.cut = new Set(cut ? paths : []);
  updateClasses();
  toast(`${items.length === 1 ? '“' + items[0].name + '”' : items.length + ' items'} ${cut ? 'will be moved' : 'copied'}`);
}

async function paste(dest = S.path) {
  if (S.mode !== 'dir') return;
  const r = await run('Pasting…', () => op({ op: 'paste', dest }));
  if (r) S.cut.clear();
}

function rename(i = S.focus) {
  const it = S.view[i];
  if (!it || S.mode === 'trash') return;
  reveal(i);
  const el = pool.get(i);
  if (!el) return;
  const nm = $('.nm', el);
  const inp = document.createElement('input');
  inp.className = 'rn';
  inp.value = it.name;
  inp.spellcheck = false;
  nm.replaceChildren(inp);
  inp.focus();
  const dot = it.dir ? -1 : it.name.lastIndexOf('.');
  inp.setSelectionRange(0, dot > 0 ? dot : it.name.length);
  let done = false;
  const finish = async (save) => {
    if (done) return;
    done = true;
    const v = inp.value.trim();
    if (save && v && v !== it.name) {
      try {
        const r = await op({ op: 'rename', paths: [it.path], name: v });
        await refresh(true);
        const j = S.view.findIndex((x) => x.path === r.path);
        if (j >= 0) {
          S.sel = new Set([r.path]);
          S.focus = S.anchor = j;
          reveal(j);
          updateClasses();
        }
        return;
      } catch (e) {
        toast(e.message, { err: true });
      }
    }
    repaintRow(i);
    scroller.focus();
  };
  inp.addEventListener('keydown', (e) => {
    e.stopPropagation();
    if (e.key === 'Enter') finish(true);
    if (e.key === 'Escape') finish(false);
  });
  inp.addEventListener('blur', () => finish(true));
  inp.addEventListener('mousedown', (e) => e.stopPropagation());
}

async function create(kind) {
  if (S.mode !== 'dir') return;
  const name = await promptDlg(kind === 'mkdir' ? 'New Folder' : 'New File', kind === 'mkdir' ? 'Folder name' : 'File name', kind === 'mkdir' ? 'Untitled Folder' : 'Untitled.txt', kind === 'mkdir' ? 'Create' : 'Create');
  if (!name) return;
  try {
    const r = await op({ op: kind, dest: S.path, name });
    await refresh(true);
    const j = S.view.findIndex((x) => x.path === r.path);
    if (j >= 0) {
      S.sel = new Set([r.path]);
      S.focus = S.anchor = j;
      reveal(j);
      updateClasses();
    }
  } catch (e) {
    toast(e.message, { err: true });
  }
}

async function compress() {
  const items = selected();
  if (!items.length) return;
  const name = await promptDlg('Compress', 'Archive name', (items.length === 1 ? items[0].name.replace(/\.[^.]+$/, '') : 'Archive') + '.zip', 'Create');
  if (name) run('Compressing…', () => op({ op: 'compress', paths: items.map((it) => it.path), dest: S.path, name }));
}

function copyText(t) {
  navigator.clipboard.writeText(t).then(() => toast('Copied to clipboard'), () => toast('Clipboard is unavailable', { err: true }));
}

// ---------------------------------------------------------------- menus
let menuEl = null;
function closeMenu() {
  if (menuEl) menuEl.remove();
  menuEl = null;
}
function menu(x, y, items) {
  closeMenu();
  const m = document.createElement('div');
  m.className = 'menu';
  const acts = [];
  m.innerHTML = items
    .filter(Boolean)
    .map((it) => {
      if (it === '-') return '<hr>';
      if (typeof it === 'string') return `<div class="lbl">${esc(it)}</div>`;
      if (it.html) return it.html;
      const [ic, label, fn, o = {}] = it;
      acts.push(fn);
      return `<button data-a="${acts.length - 1}" class="${o.danger ? 'danger' : ''}" ${o.disabled ? 'disabled style="opacity:.4"' : ''}>${ic ? svgUse(ic) : '<span style="width:16px"></span>'}${esc(label)}${o.check ? svgUse('i-check', 'i chk') : ''}${o.kbd ? `<kbd>${o.kbd}</kbd>` : ''}</button>`;
    })
    .join('')
    .replace(/^(<hr>)+|(<hr>)+$/g, '')
    .replace(/(<hr>){2,}/g, '<hr>');
  m.addEventListener('click', (e) => {
    const b = e.target.closest('[data-a]');
    if (!b || b.disabled) return;
    closeMenu();
    acts[+b.dataset.a]();
  });
  m.addEventListener('mousedown', (e) => e.stopPropagation());
  document.body.appendChild(m);
  menuEl = m;
  const r = m.getBoundingClientRect();
  m.style.left = Math.min(x, innerWidth - r.width - 6) + 'px';
  m.style.top = (y + r.height > innerHeight - 6 ? Math.max(6, y - r.height) : y) + 'px';
}
document.addEventListener('mousedown', (e) => {
  if (menuEl && !menuEl.contains(e.target)) closeMenu();
});
addEventListener('blur', closeMenu);

function itemMenu(x, y) {
  const items = selected();
  if (!items.length) return;
  const one = items.length === 1 ? items[0] : null;
  if (S.mode === 'trash') {
    return menu(x, y, [
      ['i-undo', 'Restore from Trash', () => run('Restoring…', () => op({ op: 'restore', paths: items.map((it) => it.trash.id) }))],
      ['i-eye', 'Preview', () => quickLook(true), { kbd: 'Space' }],
      '-',
      ['i-trash', 'Delete Permanently…', () => deleteItems(items), { danger: true, kbd: 'Del' }],
    ]);
  }
  const isZip = one && !one.dir && /\.(zip|jar|apk|nupkg|vsix|docx|xlsx|pptx|epub)$/i.test(one.name);
  menu(x, y, [
    ['i-ext', one && one.dir ? 'Open' : 'Open', () => openItems(items), { kbd: 'Enter' }],
    one && one.dir ? ['i-window', 'Open in New Window', () => newWindow(one.path), { kbd: 'Middle click' }] : null,
    one && !one.dir ? ['i-apps', 'Open With…', () => op({ op: 'openwith', paths: [one.path] }).catch((e) => toast(e.message, { err: true }))] : null,
    one && one.dir ? ['i-term', 'Open in Terminal', () => op({ op: 'terminal', dest: one.path })] : null,
    S.mode !== 'dir' ? ['i-folder', 'Open Item Location', () => go(items[0].path)] : null,
    '-',
    ['i-cut', 'Cut', () => clip(true), { kbd: 'Ctrl+X' }],
    ['i-copy', 'Copy', () => clip(false), { kbd: 'Ctrl+C' }],
    one && one.dir && S.mode === 'dir' ? ['i-paste', 'Paste Into Folder', () => paste(one.path)] : null,
    '-',
    one ? ['i-pen', 'Rename…', () => rename(S.view.indexOf(one)), { kbd: 'F2' }] : null,
    ['i-star', items.every((it) => it.starred) ? 'Unstar' : 'Star', () => toggleStar(items, !items.every((it) => it.starred))],
    one && one.dir ? ['i-bm', 'Add to Sidebar', () => setBookmarks([...S.init.bookmarks.map((b) => b.path), one.path])] : null,
    ['i-zip', 'Compress…', compress],
    isZip ? ['i-unzip', 'Extract Here', () => run('Extracting…', () => op({ op: 'extract', paths: [one.path], dest: dirName(one.path) }))] : null,
    ['i-link', items.length > 1 ? 'Copy Paths' : 'Copy Path', () => copyText(items.map((it) => it.path).join('\n'))],
    '-',
    ['i-trash', 'Move to Trash', () => trashItems(items), { kbd: 'Del' }],
    ['i-x', 'Delete Permanently…', () => deleteItems(items), { danger: true, kbd: 'Shift+Del' }],
    '-',
    ['i-eye', 'Preview', () => quickLook(true), { kbd: 'Space' }],
    one ? ['i-info', 'Properties', () => op({ op: 'props', paths: [one.path] }).catch((e) => toast(e.message, { err: true })), { kbd: 'Alt+Enter' }] : null,
  ]);
}

function bgMenu(x, y) {
  if (S.mode === 'trash') return menu(x, y, [['i-trash', 'Empty Trash…', emptyTrash, { danger: true }]]);
  if (S.mode !== 'dir') return;
  menu(x, y, [
    ['i-fplus', 'New Folder…', () => create('mkdir'), { kbd: 'Ctrl+Shift+N' }],
    ['i-fileplus', 'New File…', () => create('newfile')],
    ['i-paste', 'Paste', () => paste(), { kbd: 'Ctrl+V' }],
    '-',
    ['i-select', 'Select All', selectAll, { kbd: 'Ctrl+A' }],
    ['i-term', 'Open in Terminal', () => op({ op: 'terminal', dest: S.path }), { kbd: 'Ctrl+Alt+T' }],
    ['i-refresh', 'Reload', () => refresh(), { kbd: 'F5' }],
    '-',
    ['i-info', 'Properties', () => op({ op: 'props', paths: [S.path] }).catch((e) => toast(e.message, { err: true }))],
  ]);
}

function folderMenu() {
  const r = $('#b-fmenu').getBoundingClientRect();
  const bm = S.init.bookmarks.some((b) => b.path === S.path);
  menu(r.left - 180, r.bottom + 6, [
    ['i-fplus', 'New Folder…', () => create('mkdir'), { kbd: 'Ctrl+Shift+N' }],
    ['i-fileplus', 'New File…', () => create('newfile')],
    '-',
    ['i-bm', bm ? 'Remove from Sidebar' : 'Add to Sidebar', () => setBookmarks(bm ? S.init.bookmarks.map((b) => b.path).filter((p) => p !== S.path) : [...S.init.bookmarks.map((b) => b.path), S.path]), { kbd: 'Ctrl+D' }],
    ['i-term', 'Open in Terminal', () => op({ op: 'terminal', dest: S.path }), { kbd: 'Ctrl+Alt+T' }],
    ['i-window', 'New Window', () => newWindow(S.path), { kbd: 'Ctrl+N' }],
    ['i-link', 'Copy Location', () => copyText(S.path)],
    ['i-paste', 'Paste', () => paste(), { kbd: 'Ctrl+V' }],
    ['i-select', 'Select All', selectAll, { kbd: 'Ctrl+A' }],
    '-',
    ['i-info', 'Properties', () => op({ op: 'props', paths: [S.path] }).catch((e) => toast(e.message, { err: true }))],
  ]);
}

function viewMenu() {
  const r = $('#b-vmenu').getBoundingClientRect();
  const p = S.prefs;
  const sort = (k, label) => [null, label, () => setPrefs({ sort: k }), { check: p.sort === k }];
  menu(r.right - 240, r.bottom + 6, [
    { html: `<div class="zoom"><button data-z="-1" title="Zoom out (Ctrl+-)">${svgUse('i-minus')}</button><button data-z="0" title="Reset (Ctrl+0)">${Math.round(p.zoom * 100)}%</button><button data-z="1" title="Zoom in (Ctrl++)">${svgUse('i-plus')}</button></div>` },
    '-',
    'Sort',
    sort('name', 'A–Z'),
    sort('mtime', 'Last Modified'),
    sort('size', 'Size'),
    sort('kind', 'Type'),
    [null, 'Reversed Order', () => setPrefs({ desc: !p.desc }), { check: p.desc }],
    '-',
    ['i-eye', 'Show Hidden Files', () => setPrefs({ hidden: !p.hidden }), { check: p.hidden, kbd: 'Ctrl+H' }],
    ['i-folder', 'Folders Before Files', () => setPrefs({ dirsFirst: !p.dirsFirst }), { check: p.dirsFirst }],
    ['i-pane', 'Preview Pane', () => setPrefs({ preview: !p.preview }), { check: p.preview, kbd: 'Ctrl+I' }],
  ]);
  $$('.menu [data-z]').forEach((b) =>
    b.addEventListener('click', (e) => {
      e.stopPropagation();
      zoom(+b.dataset.z);
      $('.menu [data-z="0"]').textContent = Math.round(S.prefs.zoom * 100) + '%';
    }),
  );
}

function appMenu() {
  const r = $('#b-appmenu').getBoundingClientRect();
  menu(r.left, r.bottom + 6, [
    ['i-window', 'New Window', () => newWindow(S.path), { kbd: 'Ctrl+N' }],
    ['i-refresh', 'Rebuild Index', () => api('/api/index', {}).then(() => (toast('Rescanning…'), pollIndex()))],
    ['i-eye', 'Show Hidden Files', () => setPrefs({ hidden: !S.prefs.hidden }), { check: S.prefs.hidden, kbd: 'Ctrl+H' }],
    '-',
    ['i-info', 'Keyboard Shortcuts', shortcuts, { kbd: 'Ctrl+?' }],
    ['i-info', `About MW4W Files ${S.init.version}`, () => toast(`MW4W Files ${S.init.version} · ${$('#ixstat .t').textContent}`)],
  ]);
}

function shortcuts() {
  const rows = [
    ['Ctrl+L', 'Type a location'], ['Ctrl+F or just type', 'Search'], ['Space', 'Quick Look'], ['Ctrl+I', 'Preview pane'],
    ['Alt+← / Alt+→ / Backspace', 'Back / Forward'], ['Alt+↑', 'Parent folder'], ['Alt+Home', 'Home'], ['Enter', 'Open'],
    ['F2', 'Rename'], ['Del / Shift+Del', 'Trash / Delete'], ['Ctrl+C / X / V', 'Copy / Cut / Paste'], ['Ctrl+Shift+N', 'New folder'],
    ['Ctrl+H', 'Hidden files'], ['Ctrl+1 / Ctrl+2', 'List / Grid'], ['Ctrl+ + / -', 'Zoom'], ['Ctrl+N', 'New window'], ['Ctrl+Alt+T', 'Terminal here'],
  ];
  dialog(`<h2>Keyboard Shortcuts</h2><div class="tbwrap" style="margin:10px 0 16px;max-height:55vh"><table class="tb">${rows.map(([k, d]) => `<tr><td><b>${k}</b></td><td>${d}</td></tr>`).join('')}</table></div><div class="row2"><button class="btn accent" data-r="1">Close</button></div>`);
}

// ---------------------------------------------------------------- prefs
let prefT = 0;
function setPrefs(p) {
  const relist = 'sort' in p || 'desc' in p || 'hidden' in p || 'dirsFirst' in p;
  Object.assign(S.prefs, p);
  if ('sort' in p && !('desc' in p)) S.prefs.desc = p.sort === 'mtime' || p.sort === 'size';
  applyPrefs();
  if (relist) {
    const sel = new Set(S.sel);
    sortView();
    S.sel = sel;
    S.focus = S.view.findIndex((it) => S.sel.has(it.path));
  }
  renderAll();
  if (S.prefs.preview) updatePane();
  clearTimeout(prefT);
  prefT = setTimeout(() => api('/api/prefs', S.prefs).catch(() => {}), 400);
}
function applyPrefs() {
  const r = document.documentElement.style;
  r.setProperty('--zoom', S.prefs.zoom);
  r.setProperty('--pane', S.prefs.pane + 'px');
}
function zoom(d) {
  const steps = [0.75, 0.875, 1, 1.25, 1.5, 2];
  let i = steps.indexOf(S.prefs.zoom);
  if (i < 0) i = 2;
  i = d === 0 ? 2 : Math.max(0, Math.min(steps.length - 1, i + d));
  setPrefs({ zoom: steps[i] });
}

// ---------------------------------------------------------------- search
function enterSearch(initial = '') {
  if (S.mode !== 'search') {
    S.q.base = S.mode === 'dir' ? S.path : '';
    S.q.where = S.q.base && indexed(S.q.base) ? 'here' : 'all';
    S.q.return = here();
    S.mode = 'search';
    S.items = [];
    S.view = [];
    renderAll();
  }
  const pb = $('#pathbar');
  pb.classList.add('editing');
  $('#crumbs').innerHTML = `<svg class="i lead"><use href="#i-search"/></svg><input id="searchin" spellcheck="false" autocomplete="off" placeholder="Search ${S.q.where === 'here' ? 'in ' + esc(baseName(S.q.base) || S.q.base) : 'everywhere'}">`;
  $('#b-fmenu').style.display = 'none';
  const inp = $('#searchin');
  inp.value = initial || S.q.text;
  inp.focus();
  inp.addEventListener('input', () => {
    S.q.text = inp.value;
    clearTimeout(S.q.t);
    S.q.t = setTimeout(runSearch, 40);
  });
  inp.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      e.stopPropagation();
      leaveSearch(true);
    } else if (e.key === 'ArrowDown' || e.key === 'Enter') {
      e.preventDefault();
      e.stopPropagation();
      if (!S.view.length) return;
      scroller.focus();
      if (S.focus < 0) select(0);
      if (e.key === 'Enter') openItems(selected());
    }
  });
  renderChips();
  if (initial || S.q.text) runSearch();
}

function indexed(p) {
  return (S.ixRoots || []).some((r) => under(p, r));
}

function renderChips() {
  $$('#sbar [data-w]').forEach((b) => b.classList.toggle('on', b.dataset.w === S.q.where));
  $$('#sbar [data-c]').forEach((b) => b.classList.toggle('on', (b.dataset.c === '1') === S.q.content));
  $('#sbar [data-w="here"]').style.display = S.q.base ? '' : 'none';
}
$('#sbar').addEventListener('click', (e) => {
  const b = e.target.closest('.chip');
  if (!b) return;
  if (b.dataset.w) S.q.where = b.dataset.w;
  if (b.dataset.c) S.q.content = b.dataset.c === '1';
  renderChips();
  const inp = $('#searchin');
  if (inp) {
    inp.placeholder = S.q.where === 'here' ? 'Search in ' + (baseName(S.q.base) || S.q.base) : 'Search everywhere';
    inp.focus();
  }
  runSearch();
});

async function runSearch() {
  const q = S.q.text.trim();
  const stat = $('#sbar .stat');
  const gen = ++S.token;
  if (!q) {
    setItems([]);
    stat.textContent = '';
    return;
  }
  const inDir = S.q.where === 'here' ? S.q.base : '';
  // a folder outside the index: filter what is listed (names only)
  if (inDir && !indexed(inDir)) {
    const d = S.cache.get(inDir)?.d || (await fetchDir(inDir).catch(() => null));
    if (gen !== S.token) return;
    const words = q.toLowerCase().split(/\s+/);
    const items = d ? rowsToItems(d).filter((it) => words.every((w) => it.name.toLowerCase().includes(w))) : [];
    items.forEach((it) => (it.loc = inDir));
    setItems(items);
    stat.textContent = `${items.length} in this folder (not indexed)`;
    return;
  }
  try {
    const t0 = performance.now();
    const r = await api(`/api/search?q=${enc(q)}&in=${enc(inDir)}&c=${S.q.content ? 1 : 0}`);
    if (gen !== S.token) return;
    setItems(hitsToItems(r.hits));
    const shown = r.hits ? r.hits.length : 0;
    stat.textContent = `${r.total.toLocaleString()} result${r.total === 1 ? '' : 's'}${r.total > shown ? ` (best ${shown})` : ''} · ${r.ms.toFixed(1)} ms in index, ${(performance.now() - t0).toFixed(0)} ms total${r.indexing ? ' · still indexing' : ''}`;
  } catch (e) {
    if (gen === S.token) stat.textContent = e.message;
  }
}

function leaveSearch(back) {
  if (S.mode !== 'search' && !$('#searchin')) return;
  $('#pathbar').classList.remove('editing');
  const ret = S.q.return;
  S.q.text = '';
  $('#sbar .stat').textContent = '';
  if (back && ret) {
    S.mode = ret.mode;
    if (ret.mode === 'dir') go(ret.path, { push: false });
    else openSpecial(ret.mode, { push: false });
  } else renderCrumbs();
}

// ---------------------------------------------------------------- previews
const pvCache = new Map();
let pvT = 0;
let pvFor = '';
function updatePane() {
  clearTimeout(pvT);
  pvT = setTimeout(() => {
    const items = selected();
    const it = items.length === 1 ? items[0] : null;
    const box = $('#pv');
    $('#pane .hdr b').textContent = it ? it.name : items.length ? `${items.length} items` : S.mode === 'dir' ? baseName(S.path) : '';
    if (!it) {
      pvFor = '';
      if (items.length) {
        const size = items.reduce((a, x) => a + (x.dir ? 0 : x.size), 0);
        box.innerHTML = `<div class="pv-big"><span class="ico">${svgUse('t-folder', 't')}</span></div><dl class="pv-meta"><dt>Selected</dt><dd>${items.length} items</dd><dt>Files</dt><dd>${fmtSize(size)}</dd></dl>`;
      } else box.innerHTML = `<p class="pv-note" style="text-align:center;margin-top:40%">Select a file to preview it</p>`;
      return;
    }
    const key = it.path + '|' + it.mtime + '|' + it.size;
    if (pvFor === key) return;
    pvFor = key;
    renderPreview(box, it);
  }, 40);
}

async function renderPreview(box, it, big) {
  const key = it.path + '|' + it.mtime + '|' + it.size;
  let p = pvCache.get(key);
  if (!p) {
    box.innerHTML = `<div class="pv-big">${iconHTML(it, true)}</div>`;
    try {
      p = await api('/api/preview?p=' + enc(it.path));
    } catch (e) {
      box.innerHTML = `<div class="pv-big">${iconHTML(it, true)}</div><p class="pv-note">${esc(e.message)}</p>`;
      return;
    }
    pvCache.set(key, p);
    if (pvCache.size > 80) pvCache.delete(pvCache.keys().next().value);
  }
  if (!big && pvFor !== key) return;
  const raw = `/raw?p=${enc(it.path)}&v=${it.mtime}-${it.size}`;
  let body = '';
  const meta = [['Type', kindName[p.kind] || (p.lang ? p.lang : 'File')]];
  switch (p.kind) {
    case 'image':
      body = `<div class="pv-media"><img src="${raw}" alt=""></div>`;
      if (p.width) meta.push(['Dimensions', `${p.width} × ${p.height}`]);
      break;
    case 'video':
      body = `<div class="pv-media"><video src="${raw}" controls ${big ? 'autoplay' : ''} preload="metadata"></video></div>`;
      break;
    case 'audio':
      body = `<div class="pv-big">${iconHTML(it, true)}</div><div class="pv-media" style="min-height:0;background:none"><audio src="${raw}" controls ${big ? 'autoplay' : ''}></audio></div>`;
      break;
    case 'pdf':
      body = `<div class="pv-media"><iframe src="${raw}#toolbar=0&view=FitH"></iframe></div>`;
      break;
    case 'font': {
      const fam = 'pv' + Math.abs(hash(key));
      if (!document.fonts.check(`12px ${fam}`)) {
        const ff = new FontFace(fam, `url(${raw})`);
        document.fonts.add(ff);
        ff.load().catch(() => {});
      }
      body = `<div class="font-spec" style="font-family:${fam}"><div class="aa">Aa</div><p style="font-size:28px">The quick brown fox jumps over the lazy dog</p><p style="font-size:18px">ABCDEFGHIJKLMNOPQRSTUVWXYZ<br>abcdefghijklmnopqrstuvwxyz<br>0123456789 !@#$%&*()</p><p style="font-size:14px">Zażółć gęślą jaźń · Съешь же ещё</p></div>`;
      break;
    }
    case 'code':
      body = p.html;
      meta[0] = ['Type', `${kindName.code} · ${p.lang}`];
      meta.push(['Lines', p.lines.toLocaleString()]);
      break;
    case 'markdown':
      body = `<div class="seg"><button class="on" data-md="r">Rendered</button><button data-md="s">Source</button></div><div class="md">${markdown(p.text)}</div><div class="md-src" style="display:none">${p.html || `<pre class="txt">${esc(p.text)}</pre>`}</div>`;
      meta.push(['Lines', p.lines.toLocaleString()]);
      break;
    case 'csv': {
      const rows = csv(p.text, 500);
      body = `<div class="tbwrap"><table class="tb"><tr>${(rows[0] || []).map((c) => `<th>${esc(c)}</th>`).join('')}</tr>${rows.slice(1).map((r) => `<tr>${r.map((c) => `<td class="${/^-?[\d.,]+$/.test(c) ? 'n' : ''}">${esc(c)}</td>`).join('')}</tr>`).join('')}</table></div>`;
      meta[0] = ['Type', 'Table (CSV)'];
      meta.push(['Rows', (p.lines - 1).toLocaleString()]);
      break;
    }
    case 'text':
      body = p.html || `<pre class="txt">${esc(p.text || '')}</pre>`;
      if (p.lines) meta.push(['Lines', p.lines.toLocaleString()]);
      break;
    case 'archive':
      body = `<div class="tbwrap"><table class="tb"><tr><th>Name</th><th style="text-align:right">Size</th></tr>${(p.entries || []).map((e) => `<tr><td>${e.isDir ? '📁 ' : ''}${esc(e.name)}</td><td class="n">${e.isDir ? '' : fmtSize(e.size)}</td></tr>`).join('')}</table></div>`;
      meta.push(['Contents', `${p.total.toLocaleString()} items`]);
      break;
    case 'dir':
      body = `<div class="pv-big">${iconHTML(it, true)}</div>`;
      meta.push(['Contents', fmtItems(p.items)]);
      if (p.indexed) meta.push(['Total size', `${fmtSize(p.dirSize)} in ${p.dirFiles.toLocaleString()} files`]);
      break;
    default:
      body = `<div class="pv-big">${iconHTML(it, true)}</div>` + (p.hex ? `<pre class="hex">${esc(p.hex)}</pre>` : '');
  }
  if (p.cut) body = `<p class="pv-note">Showing the first ${fmtSize(1 << 20)}</p>` + body;
  if (!p.isDir) meta.push(['Size', `${fmtSize(p.size)}${p.size >= 1000 ? ` (${p.size.toLocaleString()} bytes)` : ''}`]);
  meta.push(['Modified', fmtFull(p.mtime)]);
  if (it.trash) meta.push(['Deleted from', esc(it.trash.orig)]);
  else meta.push(['Location', `<a data-go="${esc(dirName(p.path))}">${esc(prettyPath(dirName(p.path)))}</a>`]);
  if (p.readOnly) meta.push(['Attributes', 'Read-only']);
  box.innerHTML = body + `<dl class="pv-meta">${meta.map(([k, v]) => `<dt>${k}</dt><dd>${k === 'Location' || k === 'Deleted from' ? v : esc(v)}</dd>`).join('')}</dl>` +
    `<p class="pv-note">Preview built in ${p.ms.toFixed(1)} ms</p>`;
}
function hash(s) {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0;
  return h;
}
for (const id of ['#pv', '#qlb']) {
  $(id).addEventListener('click', (e) => {
    const a = e.target.closest('[data-go]');
    if (a) {
      closeQL();
      return go(a.dataset.go);
    }
    const md = e.target.closest('[data-md]');
    if (md) {
      const box = md.closest('#pv, #qlb');
      $$('[data-md]', box).forEach((b) => b.classList.toggle('on', b === md));
      $('.md', box).style.display = md.dataset.md === 'r' ? '' : 'none';
      $('.md-src', box).style.display = md.dataset.md === 's' ? '' : 'none';
    }
    const link = e.target.closest('a[data-href]');
    if (link && /^https?:/.test(link.dataset.href)) op({ op: 'open', paths: [link.dataset.href] });
  });
}
$('#pv-close').addEventListener('click', () => setPrefs({ preview: false }));
$('#pv-open').addEventListener('click', () => openItems(selected()));

// pane width
$('#pane .grip').addEventListener('mousedown', (e) => {
  e.preventDefault();
  const x0 = e.clientX;
  const w0 = S.prefs.pane;
  const mv = (ev) => {
    S.prefs.pane = Math.max(260, Math.min(innerWidth * 0.6, w0 + x0 - ev.clientX));
    applyPrefs();
  };
  const up = () => {
    removeEventListener('mousemove', mv);
    removeEventListener('mouseup', up);
    setPrefs({});
  };
  addEventListener('mousemove', mv);
  addEventListener('mouseup', up);
});

// quick look (Space)
function quickLook(force) {
  const ql = $('#ql');
  if (ql.classList.contains('on') && !force) return closeQL();
  const it = S.view[S.focus] || selected()[0];
  if (!it) return;
  ql.classList.add('on');
  $('#ql .hdr b').textContent = it.name;
  renderPreview($('#qlb'), it, true);
}
function closeQL() {
  const ql = $('#ql');
  if (!ql.classList.contains('on')) return;
  ql.classList.remove('on');
  $('#qlb').innerHTML = '';
}
$('#ql').addEventListener('mousedown', (e) => e.target.id === 'ql' && closeQL());
$('#ql-close').addEventListener('click', closeQL);
$('#ql-open').addEventListener('click', () => {
  closeQL();
  openItems(selected());
});

// ---------------------------------------------------------------- computer & trash
function renderComputer() {
  const I = S.init;
  const card = (p, ic, name, sub, bar) =>
    `<button class="card" data-p="${esc(p)}"><span class="ico">${ic}</span><div><b>${esc(name)}</b>${bar !== undefined ? `<div class="bar"><i style="width:${(bar * 100).toFixed(1)}%"></i></div>` : ''}<small>${esc(sub)}</small></div></button>`;
  let h = '<h3>Devices</h3><div class="cards">';
  for (const d of I.drives) {
    h += card(d.path, svgUse('t-drive', 't'), d.name, d.total ? `${fmtSize(d.free)} free of ${fmtSize(d.total)}` : d.path, d.total ? 1 - d.free / d.total : undefined);
  }
  h += '</div><h3>Folders</h3><div class="cards">';
  for (const k of [{ name: 'Home', path: I.home, icon: 'home' }, ...I.known]) {
    h += card(k.path, svgUse('t-folder', 't') + svgUse(icons[k.icon] || 'i-folder', 'i g'), k.name, prettyPath(k.path));
  }
  $('#computer').innerHTML = h + '</div>';
}
$('#computer').addEventListener('click', (e) => {
  const c = e.target.closest('.card');
  if (c) go(c.dataset.p);
});

function renderTrashBar() {
  if (S.mode !== 'trash') return;
  const total = S.items.reduce((a, it) => a + (it.size || 0), 0);
  $('#tbar .grow').textContent = S.items.length ? `${S.items.length} items · ${fmtSize(total)}` : '';
  $('#b-empty').disabled = !S.items.length;
}
$('#b-restore').addEventListener('click', () => {
  const items = selected();
  if (items.length) run('Restoring…', () => op({ op: 'restore', paths: items.map((it) => it.trash.id) }));
  else toast('Select items to restore');
});
$('#b-purge').addEventListener('click', () => (selected().length ? deleteItems(selected()) : toast('Select items to delete')));
$('#b-empty').addEventListener('click', emptyTrash);
async function emptyTrash() {
  if (await confirmDlg('Empty Trash?', 'All items in the Trash will be permanently deleted.', 'Empty Trash', true)) run('Emptying Trash…', () => op({ op: 'empty' }));
}

// ---------------------------------------------------------------- bubble, toasts, dialogs
function updateBubble() {
  const b = $('#bubble');
  const items = selected();
  if (!items.length || S.mode === 'computer') return b.classList.remove('on');
  const files = items.filter((it) => !it.dir);
  const size = files.reduce((a, it) => a + it.size, 0);
  if (items.length === 1) {
    const it = items[0];
    b.textContent = `“${it.name}” selected` + (it.dir ? (S.counts.has(it.path) ? ` (containing ${fmtItems(S.counts.get(it.path))})` : '') : ` (${fmtSize(size)})`);
  } else {
    const dirs = items.length - files.length;
    b.textContent = [dirs ? `${dirs} folder${dirs > 1 ? 's' : ''}` : '', files.length ? `${files.length} file${files.length > 1 ? 's' : ''} (${fmtSize(size)})` : ''].filter(Boolean).join(', ') + ' selected';
  }
  b.classList.add('on');
}

function toast(msg, o = {}) {
  const t = document.createElement('div');
  t.className = 'toast' + (o.err ? ' err' : '');
  t.innerHTML = (o.spin ? '<span class="spin"></span>' : '') + `<span>${esc(msg)}</span>` + (o.action ? `<button class="btn">${esc(o.action)}</button>` : '') +
    (o.sticky ? '' : `<button class="ib sm">${svgUse('i-x')}</button>`);
  if (o.action) $('.btn', t).addEventListener('click', () => (t.remove(), o.onAction()));
  const x = $('.ib', t);
  if (x) x.addEventListener('click', () => t.remove());
  $('#toasts').appendChild(t);
  while ($('#toasts').children.length > 3) $('#toasts').firstChild.remove();
  if (!o.sticky) setTimeout(() => t.remove(), o.action ? 7000 : o.err ? 6000 : 3000);
  return t;
}

function dialog(html, setup) {
  return new Promise((res) => {
    const d = $('#dlg');
    const box = $('.box', d);
    box.innerHTML = html;
    d.classList.add('on');
    const close = (v) => {
      d.classList.remove('on');
      box.innerHTML = '';
      d.onkeydown = null;
      scroller.focus();
      res(v);
    };
    box.addEventListener('click', (e) => {
      const b = e.target.closest('[data-r]');
      if (b) close(b.dataset.r === '1' ? (setup ? setup.value() : true) : null);
    });
    d.onkeydown = (e) => {
      e.stopPropagation();
      if (e.key === 'Escape') close(null);
      if (e.key === 'Enter') {
        e.preventDefault();
        const ok = $('[data-r="1"]', box);
        if (ok && !ok.disabled) ok.click();
      }
    };
    if (setup) setup.init(box);
    else $('[data-r="1"]', box)?.focus();
  });
}
function confirmDlg(title, text, ok, danger) {
  return dialog(`<h2>${esc(title)}</h2><p>${esc(text)}</p><div class="row2"><button class="btn" data-r="0">Cancel</button><button class="btn ${danger ? 'danger' : 'accent'}" data-r="1">${esc(ok)}</button></div>`);
}
function promptDlg(title, label, value, ok) {
  let inp;
  return dialog(`<h2>${esc(title)}</h2><p>${esc(label)}</p><input spellcheck="false"><div class="err"></div><div class="row2"><button class="btn" data-r="0">Cancel</button><button class="btn accent" data-r="1">${esc(ok)}</button></div>`, {
    init(box) {
      inp = $('input', box);
      inp.value = value;
      inp.focus();
      const dot = value.lastIndexOf('.');
      inp.setSelectionRange(0, dot > 0 ? dot : value.length);
      const check = () => {
        const v = inp.value.trim();
        const bad = !v ? '' : /[\\/:*?"<>|]/.test(v) ? 'A name can not contain \\ / : * ? " < > |' : S.items.some((it) => it.name.toLowerCase() === v.toLowerCase()) ? `“${v}” already exists` : '';
        $('.err', box).textContent = bad;
        $('[data-r="1"]', box).disabled = !v || !!bad;
      };
      inp.addEventListener('input', check);
      check();
    },
    value: () => inp.value.trim(),
  });
}

// ---------------------------------------------------------------- keyboard
function selectAll() {
  S.sel = new Set(S.view.map((it) => it.path));
  updateClasses();
}

function moveFocus(d, e) {
  const n = S.view.length;
  if (!n) return;
  let i = S.focus < 0 ? (d > 0 ? -1 : n) : S.focus;
  i = Math.max(0, Math.min(n - 1, i + d));
  if (e.shiftKey) {
    if (S.anchor < 0) S.anchor = Math.max(0, S.focus);
    select(i, { shiftKey: true, ctrlKey: e.ctrlKey });
  } else if (e.ctrlKey) {
    S.focus = i;
    updateClasses();
  } else select(i);
  reveal(i);
  if ($('#ql').classList.contains('on')) quickLook(true);
}

document.addEventListener('keydown', (e) => {
  if (e.target.matches('input, textarea')) return;
  if ($('#dlg').classList.contains('on')) return;
  const k = e.key;
  const c = e.ctrlKey || e.metaKey;
  const grid = S.prefs.view === 'grid';
  const page = Math.max(1, Math.floor(scroller.clientHeight / (grid ? geom.ch : geom.rh)) * (grid ? geom.cols : 1));
  const keys = {
    ArrowDown: () => moveFocus(grid ? geom.cols : 1, e),
    ArrowUp: () => (e.altKey ? goUp() : moveFocus(grid ? -geom.cols : -1, e)),
    ArrowRight: () => (e.altKey ? goFwd() : grid ? moveFocus(1, e) : null),
    ArrowLeft: () => (e.altKey ? goBack() : grid ? moveFocus(-1, e) : null),
    PageDown: () => moveFocus(page, e),
    PageUp: () => moveFocus(-page, e),
    Home: () => (e.altKey ? go(S.init.home) : moveFocus(-1e9, e)),
    End: () => moveFocus(1e9, e),
    Enter: () => (e.altKey ? selected()[0] && op({ op: 'props', paths: [selected()[0].path] }) : openItems(selected())),
    Backspace: () => goBack(),
    Delete: () => (e.shiftKey || S.mode === 'trash' ? deleteItems(selected()) : trashItems(selected())),
    F2: () => rename(),
    F5: () => refresh(),
    ' ': () => quickLook(),
    Escape: () => {
      if ($('#ql').classList.contains('on')) return closeQL();
      if (menuEl) return closeMenu();
      if (S.mode === 'search') return leaveSearch(true);
      S.cut.clear();
      select(-1);
    },
  };
  const ctrlKeys = {
    a: selectAll, c: () => clip(false), x: () => clip(true), v: () => paste(), l: () => editPath(), f: () => enterSearch(),
    h: () => setPrefs({ hidden: !S.prefs.hidden }), i: () => setPrefs({ preview: !S.prefs.preview }), r: () => refresh(),
    1: () => setPrefs({ view: 'list' }), 2: () => setPrefs({ view: 'grid' }), '=': () => zoom(1), '+': () => zoom(1), '-': () => zoom(-1), 0: () => zoom(0),
    n: () => (e.shiftKey ? create('mkdir') : newWindow(S.path)), w: () => send('close') || (host ? 0 : window.close()),
    d: () => S.mode === 'dir' && setBookmarks([...S.init.bookmarks.map((b) => b.path), S.path]), '?': shortcuts,
    t: () => e.altKey && S.mode === 'dir' && op({ op: 'terminal', dest: S.path }),
  };
  let fn = null;
  if (c) fn = ctrlKeys[e.shiftKey && k === 'N' ? 'n' : k.toLowerCase()] || (k === '/' && e.shiftKey ? shortcuts : null);
  else if (!e.altKey || k.startsWith('Arrow') || k === 'Home' || k === 'Enter') fn = keys[k];
  if (fn) {
    e.preventDefault();
    fn();
    return;
  }
  // typing starts a search (Nautilus)
  if (!c && !e.altKey && k.length === 1 && k !== ' ' && S.mode !== 'computer' && S.mode !== 'trash') {
    e.preventDefault();
    if (k === '/' || k === '~') return editPath(k === '~' ? S.init.home + S.sep : '/');
    enterSearch(S.mode === 'search' ? S.q.text + k : k);
  }
});
// mouse back/forward buttons
addEventListener('mouseup', (e) => {
  if (e.button === 3) goBack();
  if (e.button === 4) goFwd();
});
addEventListener('wheel', (e) => {
  if (e.ctrlKey) {
    e.preventDefault();
    zoom(e.deltaY < 0 ? 1 : -1);
  }
}, { passive: false });

// ---------------------------------------------------------------- header buttons & window
$('#b-back').addEventListener('click', goBack);
$('#b-fwd').addEventListener('click', goFwd);
$('#b-search').addEventListener('click', () => (S.mode === 'search' ? leaveSearch(true) : enterSearch()));
$('#b-search2').addEventListener('click', () => (S.mode === 'search' ? leaveSearch(true) : enterSearch()));
$('#b-view').addEventListener('click', () => setPrefs({ view: S.prefs.view === 'grid' ? 'list' : 'grid' }));
$('#b-vmenu').addEventListener('click', viewMenu);
$('#b-fmenu').addEventListener('click', folderMenu);
$('#b-appmenu').addEventListener('click', appMenu);
$('#b-close').addEventListener('click', () => (host ? send('close') : window.close()));
$('#cols').addEventListener('click', (e) => {
  const b = e.target.closest('button');
  if (!b) return;
  const k = b.dataset.k;
  if (S.prefs.sort === k) setPrefs({ desc: !S.prefs.desc });
  else setPrefs({ sort: k });
});

// frameless window: empty header space drags, double click maximizes; edges resize
$$('.drag').forEach((h) => {
  h.addEventListener('mousedown', (e) => {
    if (e.button === 0 && !e.target.closest('button, input, #pathbar') && e.detail === 1) send('drag');
  });
  h.addEventListener('dblclick', (e) => !e.target.closest('button, input, #pathbar') && send('max'));
});
if (host) {
  for (const [cls, ht] of [['n', 12], ['s', 15], ['e', 11], ['w', 10], ['nw', 13], ['ne', 14], ['sw', 16], ['se', 17]]) {
    const d = document.createElement('div');
    d.className = 'edge ' + cls;
    d.addEventListener('mousedown', (e) => e.button === 0 && send('size:' + ht));
    document.body.appendChild(d);
  }
  host.addEventListener('message', (e) => {
    // the host reuses a warm window: {open: path}
    const m = e.data;
    if (m && m.open !== undefined) openPath(m.open);
    if (m && m.maximized !== undefined) document.documentElement.classList.toggle('max', m.maximized);
  });
}

// live: the folder on screen follows changes made elsewhere
setInterval(async () => {
  if (document.hidden || S.mode !== 'dir' || !S.path) return;
  try {
    const r = await api('/api/stamp?p=' + enc(S.path));
    if (r.stamp && r.stamp !== S.stamp) {
      S.stamp = r.stamp;
      refresh(true);
    }
  } catch {}
}, 1500);
document.addEventListener('visibilitychange', () => !document.hidden && S.mode === 'dir' && S.path && refresh(true));

// ---------------------------------------------------------------- start
function openPath(p) {
  closeQL();
  closeMenu();
  S.back = [];
  S.fwd = [];
  S.path = '';
  if (p === 'shell:recent' || p === 'recent') return openSpecial('recent', { push: false });
  if (p === 'shell:trash' || p === 'trash') return openSpecial('trash', { push: false });
  go(p || S.init.home, { push: false });
}
window.mw4wOpen = openPath;

(async function start() {
  const url = new URL(location.href);
  const t0 = performance.now();
  document.documentElement.classList.add(host ? 'embedded' : 'browser');
  if (!host) document.documentElement.style.setProperty('--wall', 'url(/wallpaper)');
  history.replaceState(null, '', '/' + (url.searchParams.get('p') ? '?p=' + enc(url.searchParams.get('p')) : '')); // the token stays out of the URL
  S.init = await api('/api/init');
  S.sep = S.init.sep;
  Object.assign(S.prefs, S.init.prefs || {});
  applyPrefs();
  renderPlaces();
  api('/api/index').then((s) => (S.ixRoots = s.roots));
  pollIndex();
  const p = url.searchParams.get('p') || '';
  if (url.searchParams.get('warm') !== '1' || p) openPath(p);
  else go(S.init.home, { push: false }); // warm window: home is ready, the host shows it on demand
  scroller.focus();
  console.debug('ready in', (performance.now() - t0).toFixed(1), 'ms');
})();
