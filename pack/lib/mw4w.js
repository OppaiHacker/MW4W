// Shared helpers for all MW4W widgets.
import * as zebar from './zebar.js';

export { zebar };
export const widget = zebar.currentWidget();

// Zebar 3.3 fills htmlPath (absolute) but not configPath.
const cfg = widget.htmlPath ?? widget.configPath;
export const packDir = cfg.slice(0, Math.max(cfg.lastIndexOf('\\'), cfg.lastIndexOf('/')));
const script = `${packDir}\\scripts\\mw4w.ps1`;

/** Runs scripts\mw4w.ps1 <cmd> [arg]. */
export function mw4w(cmd, arg) {
  const args = ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-WindowStyle', 'Hidden', '-File', script, cmd];
  if (arg !== undefined && arg !== '') args.push(String(arg));
  return zebar.shellExec('powershell', args);
}

export const b64 = s => btoa(String.fromCharCode(...new TextEncoder().encode(s)));

/** Opens a URI / ms-settings: page / folder. */
export const open = target => zebar.shellExec('explorer', [target]);

let glazeProvider;
/**
 * Runs a GlazeWM command over its IPC: `shell-exec x` starts x at once, outside Zebar's process tree.
 * Never quote in shell-exec: GlazeWM misparses quotes, and a program it can't start freezes it behind an error dialog.
 */
export async function glaze(cmd) {
  return (await glazeOut()).runCommand(cmd);
}

async function glazeOut() {
  glazeProvider ??= zebar.createProvider({ type: 'glazewm' });
  return glazeProvider.output ?? (await new Promise(r => glazeProvider.onOutput(r)));
}

/**
 * mw4w.ps1 <cmd> [arg] in winmask's warm PowerShell: ~50ms instead of ~1.5s, but
 * fire-and-forget. Commands that save state change its stamp when done (watchState).
 */
export const send = (cmd, arg = '') => glaze(`shell-exec ${packDir}\\bin\\winmask.exe --mw4w ${cmd} ${arg}`.trim());

// Popups are started hidden at Zebar startup (preset "preload") and only shown
// and hidden afterwards: a new WebView2 window takes ~0.7s, showing one ~15ms.
const zstate = window.__ZEBAR_STATE ?? JSON.parse(sessionStorage.getItem('ZEBAR_STATE') ?? '{}');
export const preloaded = zstate.openOptions?.preset === 'preload';

const popupWins = {};
/** Shows popup `name` (winmask focuses it), starting it if it isn't preloaded. */
export async function openPopup(name) {
  try {
    if (popupWins[name]) return await popupWins[name].show();
  } catch {} // closed since: look again
  for (const w of await widget.tauriWindow.constructor.getAll())
    if ((await w.title()) === `Zebar - mw4w / ${name}`) return (popupWins[name] = w).show();
  return zebar.startWidgetPreset(name, 'default');
}

export async function json(path, fallback = null) {
  try {
    const r = await fetch(`${path}?t=${Date.now()}`, { cache: 'no-store' });
    return r.ok ? await r.json() : fallback;
  } catch {
    return fallback;
  }
}

export async function settings() {
  const [d, u] = await Promise.all([json('defaults.json', {}), json('settings.json', {})]);
  return { ...d, ...u, apps: { ...d.apps, ...(u?.apps ?? {}) } };
}

/** Polls cache/state.json; calls cb(state, changed) on every stamp change. */
export function watchState(cb) {
  let last;
  const tick = async () => {
    const st = await json('cache/state.json', {});
    if (st.stamp !== last) {
      const changed = last !== undefined;
      last = st.stamp;
      cb(st, changed);
    }
  };
  tick();
  setInterval(tick, 1500);
}

/**
 * Popups hide on Escape and when they lose focus, like rofi. Returns close();
 * onHide resets the page for its next show. Focus comes from winmask.
 */
export function popup({ blur = true, onHide } = {}) {
  const win = widget.tauriWindow;
  // winmask shows popups natively, so tao may still think the window is hidden
  // and would skip hide(): show() syncs its state first (no-op on screen)
  const hide = async () => (await win.isVisible()) && (await win.show(), await win.hide(), true);
  // Zebar shows the window a while after it starts the page: wait for that, hide
  // it, then let the page paint (the head script keeps it invisible until then)
  if (preloaded)
    (async () => {
      for (let i = 0; i < 100; i++, await new Promise(r => setTimeout(r, 50))) if (await win.isVisible()) break;
      await win.hide();
      document.documentElement.classList.remove('preload');
    })();
  const close = async () => {
    if (await hide()) {
      // Windows hands the foreground to whatever is next (often the bar): give it back to GlazeWM's window
      const f = (await glazeOut()).focusedContainer;
      if (f?.type === 'window') await glaze(`focus --container-id ${f.id}`);
    }
    onHide?.();
  };
  addEventListener('keydown', e => e.key === 'Escape' && close());
  if (blur) setTimeout(() => addEventListener('blur', close), 400);
  // Hidden: reload for fresh colors/settings. Shown: just swap the colors.
  watchState(async (_, changed) => {
    if (!changed) return;
    if (!(await win.isVisible())) return location.reload();
    for (const l of document.querySelectorAll('link[href^="colors.css"]')) l.href = `colors.css?t=${Date.now()}`;
  });
  return close;
}

export const ic = (code, cls = '') => `<i class="ic ${cls}">&#x${code};</i>`;

export const esc = s =>
  String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

/** Fuzzy score: higher is better, -1 = no match. */
export function score(text, q) {
  if (!q) return 0;
  const t = text.toLowerCase();
  q = q.toLowerCase();
  if (t.startsWith(q)) return 1000 - t.length;
  const i = t.indexOf(q);
  if (i >= 0) return (/[\s\-_.(]/.test(t[i - 1]) ? 800 : 500) - i;
  let pos = 0;
  for (const ch of q) {
    pos = t.indexOf(ch, pos);
    if (pos < 0) return -1;
    pos++;
  }
  return 100 - pos;
}
