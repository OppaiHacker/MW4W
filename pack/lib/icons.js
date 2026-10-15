// Line icons for the bar, dock and media island (Lucide-style, 24x24, stroke = currentColor).
const P = {
  terminal: '<path d="m7 11 2-2-2-2"/><path d="M11 13h4"/><rect width="18" height="18" x="3" y="3" rx="2"/>',
  grid: '<rect width="7" height="7" x="3" y="3" rx="2"/><rect width="7" height="7" x="14" y="3" rx="2"/><rect width="7" height="7" x="14" y="14" rx="2"/><rect width="7" height="7" x="3" y="14" rx="2"/>',
  bell: '<path fill="currentColor" d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>',
  timer: '<line x1="10" x2="14" y1="2" y2="2"/><line x1="12" x2="15" y1="14" y2="11"/><circle cx="12" cy="14" r="8"/>',
  box: '<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>',
  rocket: '<path d="M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91-.09z"/><path d="m12 15-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2z"/><path d="M9 12H4s.55-3.03 2-4c1.62-1.08 5 0 5 0"/><path d="M12 15v5s3.03-.55 4-2c1.08-1.62 0-5 0-5"/>',
  vol0: '<path d="M11 5 6 9H2v6h4l5 4z"/><line x1="22" x2="16" y1="9" y2="15"/><line x1="16" x2="22" y1="9" y2="15"/>',
  vol1: '<path d="M11 5 6 9H2v6h4l5 4z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/>',
  vol2: '<path d="M11 5 6 9H2v6h4l5 4z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M19 5a10 10 0 0 1 0 14"/>',
  lan: '<rect fill="currentColor" x="16" y="16" width="6" height="6" rx="1"/><rect fill="currentColor" x="2" y="16" width="6" height="6" rx="1"/><rect fill="currentColor" x="9" y="2" width="6" height="6" rx="1"/><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3"/><path d="M12 12V8"/>',
  wifi: '<path d="M12 20h.01"/><path d="M2 8.82a15 15 0 0 1 20 0"/><path d="M5 12.86a10 10 0 0 1 14 0"/><path d="M8.5 16.43a5 5 0 0 1 7 0"/>',
  offline: '<path d="M12 20h.01"/><path d="M8.5 16.43a5 5 0 0 1 7 0"/><path d="M2 8.82a15 15 0 0 1 4.18-2.93"/><path d="M22 8.82a15 15 0 0 0-11.29-3.76"/><path d="m2 2 20 20"/>',
  battery: '<rect width="16" height="10" x="2" y="7" rx="2"/><line x1="22" x2="22" y1="11" y2="13"/>',
  charging: '<path d="M15 7h1a2 2 0 0 1 2 2v6a2 2 0 0 1-2 2h-2"/><path d="M6 7H4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h1"/><path d="m11 7-3 5h4l-3 5"/><line x1="22" x2="22" y1="11" y2="13"/>',
  power: '<path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/>',
  chevron: '<path d="m15 18-6-6 6-6"/>',
  music: '<path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/>',
  prev: '<path fill="currentColor" d="M19 20 9 12l10-8z"/><line x1="5" x2="5" y1="19" y2="5"/>',
  next: '<path fill="currentColor" d="m5 4 10 8-10 8z"/><line x1="19" x2="19" y1="5" y2="19"/>',
  play: '<path fill="currentColor" stroke="none" d="M7 4.5v15a1 1 0 0 0 1.5.86l12.5-7.5a1 1 0 0 0 0-1.72L8.5 3.64A1 1 0 0 0 7 4.5z"/>',
  pause: '<rect fill="currentColor" stroke="none" x="6" y="4" width="4.5" height="16" rx="1.2"/><rect fill="currentColor" stroke="none" x="13.5" y="4" width="4.5" height="16" rx="1.2"/>',
};

/** <svg> for icon `name`; size in px. */
export const svg = (name, size = 18, cls = '') =>
  `<svg class="svg ${cls}" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${P[name] ?? ''}</svg>`;

export const volIcon = (v, muted) => (muted || v <= 0 ? 'vol0' : v < 50 ? 'vol1' : 'vol2');

/** 83 -> "1:23", 3723 -> "1:02:03" */
export function fmt(sec) {
  sec = Math.max(0, Math.floor(sec || 0));
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = String(sec % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`;
}

/**
 * The playing track: Zebar's media provider (instant title/state, controls) merged with
 * cache/media.json from `mw4w.ps1 media-loop` (cover, app, seekable position, per-app volume).
 * The extras only count while they describe the same track.
 */
export function track(session, extra) {
  if (!session?.title) return null;
  const same = extra?.title === session.title;
  const x = same ? extra : {};
  const playing = !!session.isPlaying;
  const len = x.len || session.endTime || 0;
  let pos = x.pos ?? session.position ?? 0;
  if (same && playing && x.at) pos += (Date.now() - x.at) / 1000;
  return {
    title: session.title,
    artist: session.artist || x.artist || '',
    playing,
    len,
    pos: len ? Math.min(pos, len) : pos,
    cover: x.cover ?? null,
    app: x.app ?? '',
    icon: x.icon ?? null,
    vol: x.vol ?? null,
    canSeek: !!x.canSeek && len > 0,
  };
}
