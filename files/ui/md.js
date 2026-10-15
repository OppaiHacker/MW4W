// Small Markdown (CommonMark-ish subset) and CSV readers for previews. Output is escaped HTML.
'use strict';

const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

function mdInline(s) {
  const codes = [];
  s = s.replace(/`([^`]+)`/g, (_, c) => `\u0000${codes.push(c) - 1}\u0000`);
  s = esc(s)
    .replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g, (_, alt, src) => `<img alt="${alt}" src="${/^(https?:|data:)/.test(src) ? src : ''}">`)
    .replace(/\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g, (_, t, href) => `<a data-href="${href}" title="${href}">${t}</a>`)
    .replace(/\*\*([^*]+)\*\*|__([^_]+)__/g, (_, a, b) => `<strong>${a || b}</strong>`)
    .replace(/(^|[^*\w])\*([^*\s][^*]*)\*/g, '$1<em>$2</em>')
    .replace(/(^|\W)_([^_\s][^_]*)_(?=\W|$)/g, '$1<em>$2</em>')
    .replace(/~~([^~]+)~~/g, '<del>$1</del>');
  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => `<code>${esc(codes[i])}</code>`);
}

function markdown(src) {
  const lines = src.replace(/\r\n?/g, '\n').split('\n');
  let out = '';
  let i = 0;
  const para = [];
  const flush = () => {
    if (para.length) out += `<p>${mdInline(para.join(' '))}</p>`;
    para.length = 0;
  };
  while (i < lines.length) {
    const l = lines[i];
    let m;
    if ((m = l.match(/^\s*(```|~~~)\s*(\S*)/))) {
      flush();
      const fence = m[1];
      const code = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(fence)) code.push(lines[i++]);
      i++;
      out += `<pre><code>${esc(code.join('\n'))}</code></pre>`;
      continue;
    }
    if ((m = l.match(/^(#{1,6})\s+(.*?)\s*#*\s*$/))) {
      flush();
      out += `<h${m[1].length}>${mdInline(m[2])}</h${m[1].length}>`;
    } else if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(l)) {
      flush();
      out += '<hr>';
    } else if (/^\s*>/.test(l)) {
      flush();
      const q = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) q.push(lines[i++].replace(/^\s*>\s?/, ''));
      out += `<blockquote>${markdown(q.join('\n'))}</blockquote>`;
      continue;
    } else if (/^\s*([-*+]|\d+[.)])\s+/.test(l)) {
      flush();
      const ordered = /^\s*\d/.test(l);
      out += ordered ? '<ol>' : '<ul>';
      while (i < lines.length && /^\s*([-*+]|\d+[.)])\s+/.test(lines[i])) {
        let item = lines[i].replace(/^\s*([-*+]|\d+[.)])\s+/, '');
        const box = item.match(/^\[([ xX])\]\s+/);
        if (box) item = `<input type="checkbox" disabled ${box[1] !== ' ' ? 'checked' : ''}> ` + mdInline(item.slice(box[0].length));
        else item = mdInline(item);
        out += `<li>${item}</li>`;
        i++;
      }
      out += ordered ? '</ol>' : '</ul>';
      continue;
    } else if (/^\s*\|.*\|\s*$/.test(l) && i + 1 < lines.length && /^\s*\|?\s*:?-+/.test(lines[i + 1])) {
      flush();
      const cells = (r) => r.trim().replace(/^\||\|$/g, '').split('|').map((c) => mdInline(c.trim()));
      out += '<div class="tbwrap"><table class="tb"><tr>' + cells(l).map((c) => `<th>${c}</th>`).join('') + '</tr>';
      i += 2;
      while (i < lines.length && /^\s*\|/.test(lines[i])) out += '<tr>' + cells(lines[i++]).map((c) => `<td>${c}</td>`).join('') + '</tr>';
      out += '</table></div>';
      continue;
    } else if (!l.trim()) {
      flush();
    } else {
      para.push(l.trim());
    }
    i++;
  }
  flush();
  return out;
}

// csv: rows of fields (quotes, escaped quotes, newlines in quotes); sep auto (, ; or tab)
function csv(text, max = 1000) {
  const first = text.slice(0, text.indexOf('\n') >>> 0);
  const sep = [',', ';', '\t'].reduce((a, b) => (first.split(b).length > first.split(a).length ? b : a));
  const rows = [];
  let row = [];
  let f = '';
  let q = false;
  for (let i = 0; i < text.length && rows.length < max; i++) {
    const c = text[i];
    if (q) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          f += '"';
          i++;
        } else q = false;
      } else f += c;
    } else if (c === '"') q = true;
    else if (c === sep) {
      row.push(f);
      f = '';
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i++;
      row.push(f);
      rows.push(row);
      row = [];
      f = '';
    } else f += c;
  }
  if (f || row.length) {
    row.push(f);
    rows.push(row);
  }
  return rows;
}
