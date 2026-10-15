// Liquid glass: a port of MediaControll's liquidglass.frag to an SVG filter.
// The backdrop (the blurred wallpaper behind the bar/dock, or the cover in the island) is
// lensed at the rounded edge, split a little into R/G/B (chromatic aberration), tinted,
// brightened in the bezel and lit by a specular rim from the top-left.
// WebView2 widgets can't see the desktop, but the bar and dock only ever sit on the
// wallpaper (GlazeWM's gaps keep windows away), so cache/glass.jpg stands in for it.

const NS = 'http://www.w3.org/2000/svg';
let count = 0;

const css = `
.lg, .lg-light { position: absolute; inset: 0; border-radius: inherit; pointer-events: none; }
.lg { z-index: -2; overflow: hidden; }
.lg img { position: absolute; max-width: none; object-fit: cover; opacity: 0; transition: opacity .5s; }
.lg img.ok { opacity: 1; }
.lg-light { z-index: -1; }
`;

function sdRoundRect(x, y, bx, by, r) {
  const qx = Math.abs(x) - bx + r;
  const qy = Math.abs(y) - by + r;
  return Math.min(Math.max(qx, qy), 0) + Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) - r;
}

/**
 * Two images for a w x h rounded rect: the displacement map (R/G = where to sample, 0.5 = here)
 * and the light (bezel glow + specular rim) at device resolution.
 */
function bake(w, h, { radius, bezel, specular }) {
  const hx = w / 2;
  const hy = h / 2;
  const r = Math.min(radius, hx, hy);
  const sd = (x, y) => sdRoundRect(x, y, hx, hy, r);
  const lx = -0.55 / Math.hypot(0.55, 0.85);
  const ly = -0.85 / Math.hypot(0.55, 0.85);

  const pixels = (cw, ch, scale, fn) => {
    const c = document.createElement('canvas');
    c.width = cw;
    c.height = ch;
    const ctx = c.getContext('2d');
    const img = ctx.createImageData(cw, ch);
    for (let j = 0; j < ch; j++)
      for (let i = 0; i < cw; i++) {
        const x = (i + 0.5) / scale - hx;
        const y = (j + 0.5) / scale - hy;
        const d = sd(x, y);
        let nx = sd(x + 1, y) - sd(x - 1, y);
        let ny = sd(x, y + 1) - sd(x, y - 1);
        const nl = Math.hypot(nx, ny) || 1;
        nx /= nl;
        ny /= nl;
        fn(img.data, (j * cw + i) * 4, d, nx, ny);
      }
    ctx.putImageData(img, 0, 0);
    return c.toDataURL();
  };

  // 0 in the flat middle, 1 at the edge; cubic so the lens curls at the rim
  const map = pixels(Math.ceil(w), Math.ceil(h), 1, (px, o, d, nx, ny) => {
    const t = d > 0 ? 0 : Math.min(1, Math.max(0, 1 + d / bezel));
    const bend = t * t * t; // sample inwards: base - n * bend
    px[o] = Math.round(255 * (0.5 - (nx * bend) / 2));
    px[o + 1] = Math.round(255 * (0.5 - (ny * bend) / 2));
    px[o + 2] = 128;
    px[o + 3] = 255;
  });

  const dpr = Math.min(2, devicePixelRatio || 1);
  const light = pixels(Math.ceil(w * dpr), Math.ceil(h * dpr), dpr, (px, o, d, nx, ny) => {
    if (d > 0.5) return;
    const t = Math.min(1, Math.max(0, 1 + d / bezel));
    const rim = 1 - Math.min(1, Math.max(0, -d / 2.5));
    const up = Math.max(nx * lx + ny * ly, 0);
    const down = Math.max(-(nx * lx + ny * ly), 0);
    const a = t * t * 0.09 + rim * (up * up + 0.45 * down * down) * specular;
    px[o] = px[o + 1] = px[o + 2] = 255;
    px[o + 3] = Math.round(255 * Math.min(1, a));
  });

  return { map, light };
}

/**
 * Turns `host` (positioned, with a border-radius) into liquid glass.
 * place(rect) -> {left, top, width, height} of that image relative to the host.
 * size: fixed {w, h} to bake for (the island morphs; everything else follows the host's size).
 */
export function liquidGlass(host, { place, radius = 20, bezel = 12, strength = 9, aberration = 0.35, blur = 16, tint = 'rgba(0,0,0,0.3)', specular = 0.55, size } = {}) {
  if (!document.getElementById('lg-css')) {
    const st = document.createElement('style');
    st.id = 'lg-css';
    st.textContent = css;
    document.head.append(st);
  }
  const id = `lg-${++count}`;
  const svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('width', '0');
  svg.setAttribute('height', '0');
  svg.style.position = 'absolute';
  const S = 2 * strength;
  const chan = (scale, row, res) =>
    `<feDisplacementMap in="SourceGraphic" in2="map" scale="${scale}" xChannelSelector="R" yChannelSelector="G" result="d${res}"/>` +
    `<feColorMatrix in="d${res}" type="matrix" values="${row} 0 0 0 1 0" result="${res}"/>`;
  svg.innerHTML =
    `<filter id="${id}" x="0" y="0" width="100%" height="100%" primitiveUnits="userSpaceOnUse" color-interpolation-filters="sRGB">` +
    `<feImage result="map" x="0" y="0" preserveAspectRatio="none"/>` +
    chan(S * (1 + aberration), '1 0 0 0 0  0 0 0 0 0  0 0 0 0 0 ', 'r') +
    chan(S, '0 0 0 0 0  0 1 0 0 0  0 0 0 0 0 ', 'g') +
    chan(S * (1 - aberration), '0 0 0 0 0  0 0 0 0 0  0 0 1 0 0 ', 'b') +
    `<feComposite in="r" in2="g" operator="arithmetic" k2="1" k3="1" result="rg"/>` +
    `<feComposite in="rg" in2="b" operator="arithmetic" k2="1" k3="1"/>` +
    `</filter>`;
  document.body.append(svg);
  const feImage = svg.querySelector('feImage');

  const back = document.createElement('div');
  back.className = 'lg';
  back.style.filter = `url(#${id})`;
  const img = document.createElement('img');
  img.alt = '';
  img.style.filter = `blur(${blur}px) saturate(1.6)`;
  img.onload = () => img.classList.add('ok');
  img.onerror = () => img.classList.remove('ok');
  back.append(img);
  const light = document.createElement('div');
  light.className = 'lg-light';
  host.prepend(back, light);

  let baked = '';
  function layout() {
    const r = host.getBoundingClientRect();
    const w = size?.w ?? r.width;
    const h = size?.h ?? r.height;
    if (w < 4 || h < 4) return;
    const key = `${w}x${h}`;
    if (key !== baked) {
      baked = key;
      const { map, light: l } = bake(w, h, { radius, bezel, specular });
      feImage.setAttribute('href', map);
      feImage.setAttribute('width', w);
      feImage.setAttribute('height', h);
      light.style.background = `url(${l}) 0 0 / ${w}px ${h}px no-repeat, ${tint}`;
    }
    if (place) {
      const p = place(r);
      Object.assign(img.style, { left: `${p.left}px`, top: `${p.top}px`, width: `${p.width}px`, height: `${p.height}px` });
    }
  }
  let raf;
  new ResizeObserver(() => (cancelAnimationFrame(raf), (raf = requestAnimationFrame(layout)))).observe(host);
  layout();

  return {
    /** new backdrop (e.g. the next cover) */
    setSource(s) {
      if (!s) return img.classList.remove('ok'), img.removeAttribute('src');
      if (img.getAttribute('src') !== s) img.src = s;
    },
    layout,
  };
}

/**
 * Where the wallpaper is behind `host`: the screen-sized image shifted by the host's position
 * on the monitor. The widget window is anchored to the monitor's top or bottom edge.
 * A little bigger than the screen, so the blur doesn't fade at the screen edges.
 */
export const wallpaperPlace = edge => r => {
  const pad = 60;
  const top = edge === 'bottom' ? screen.height - innerHeight + r.top : r.top;
  return { left: -r.left - pad, top: -top - pad, width: screen.width + 2 * pad, height: screen.height + 2 * pad };
};

