// Fennec trailer scenes. Every frame is drawn from code as a pure function of
// time, so the Go renderer can ask for any frame in any order. Scene cuts,
// word timings (from the single voice-over take), cue times and the beat grid
// all come from the resolved timeline served by `go run ./cmd/trailer`, the
// same data that places music, sound and voice.
'use strict';

const W = 1920, H = 1080;
const C = {
  bg: '#0B0A09', panel: '#141210', panel2: '#1C1916', bar: '#1F1C19',
  ink: '#F4EFE7', dim: '#8C8379', faint: '#38332D', line: 'rgba(244,239,231,0.14)',
  orange: '#FF8A1F', green: '#7CC46A', red: '#EF5A45', path: '#8DB8DA',
};
const SANS = '"Inter"', MONO = '"JetBrains Mono"';

// ---------- math ----------
const clamp = (x, a = 0, b = 1) => Math.min(b, Math.max(a, x));
const lerp = (a, b, p) => a + (b - a) * p;
const seg = (t, a, d) => clamp((t - a) / d);
const E = {
  out: x => 1 - Math.pow(1 - x, 3),
  in: x => x * x * x,
  inOut: x => (x < 0.5 ? 4 * x * x * x : 1 - Math.pow(-2 * x + 2, 3) / 2),
  back: x => { const c1 = 1.6, c3 = c1 + 1; return 1 + c3 * Math.pow(x - 1, 3) + c1 * Math.pow(x - 1, 2); },
};
const norm = s => s.toLowerCase().replace(/[^a-z0-9]/g, '');

// ---------- drawing helpers ----------
function font(g, size, fam, weight = 400) { g.font = `${weight} ${size}px ${fam}`; }
function text(g, s, x, y, o = {}) {
  g.save();
  font(g, o.size || 32, o.fam || SANS, o.weight || 500);
  g.letterSpacing = (o.ls || 0) + 'px';
  g.fillStyle = o.color || C.ink;
  g.textAlign = o.align || 'left';
  g.textBaseline = 'alphabetic';
  if (o.alpha != null) g.globalAlpha *= o.alpha;
  g.fillText(s, x, y);
  g.restore();
}
function measure(g, s, size, fam, weight = 500, ls = 0) {
  g.save(); font(g, size, fam, weight); g.letterSpacing = ls + 'px';
  const w = g.measureText(s).width; g.restore(); return w;
}
function rrect(g, x, y, w, h, r) { g.beginPath(); g.roundRect(x, y, w, h, r); }

function icon(g, kind, x, y, s, color, t = 0) {
  g.save(); g.translate(x, y); g.strokeStyle = color; g.fillStyle = color;
  g.lineWidth = Math.max(2, s * 0.15); g.lineCap = 'round'; g.lineJoin = 'round';
  if (kind === 'dot') { g.beginPath(); g.arc(0, 0, s * 0.26, 0, Math.PI * 2); g.fill(); }
  if (kind === 'check') {
    g.beginPath(); g.moveTo(-s * 0.32, 0); g.lineTo(-s * 0.08, s * 0.24); g.lineTo(s * 0.36, -s * 0.26); g.stroke();
  }
  if (kind === 'spin') { const a = t * 7; g.beginPath(); g.arc(0, 0, s * 0.3, a, a + Math.PI * 1.4); g.stroke(); }
  if (kind === 'cloud') cloudGlyph(g, 0, 0, s, color);
  g.restore();
}
function cloudGlyph(g, cx, cy, s, color) {
  g.save(); g.translate(cx, cy); g.scale(s / 40, s / 40);
  g.beginPath();
  g.moveTo(-14, 10);
  g.arc(-12, 2, 8, Math.PI * 0.5, Math.PI * 1.35);
  g.arc(-2, -5, 10, Math.PI * 1.1, Math.PI * 1.85);
  g.arc(10, 1, 9, Math.PI * 1.45, Math.PI * 0.5);
  g.closePath();
  g.lineWidth = 3.4; g.strokeStyle = color; g.lineJoin = 'round'; g.stroke();
  g.restore();
}

// Segments [{t, c, w}] or [{icon, c}] drawn left to right on one line.
function spans(g, x, y, segs, size, fam = MONO) {
  let cx = x;
  for (const s of segs) {
    if (s.icon) {
      icon(g, s.icon, cx + size * 0.45, y - size * 0.33, size * 0.9, s.c || C.ink, s.time || 0);
      cx += size * 1.2;
      continue;
    }
    font(g, size, fam, s.w || 400);
    g.letterSpacing = '0px';
    g.fillStyle = s.c || C.ink;
    g.textBaseline = 'alphabetic'; g.textAlign = 'left';
    g.fillText(s.t, cx, y);
    cx += g.measureText(s.t).width;
  }
  return cx;
}
function spansWidth(g, segs, size, fam = MONO) {
  let w = 0;
  for (const s of segs) w += s.icon ? size * 1.2 : measure(g, s.t, size, fam, s.w || 400);
  return w;
}

// Kinetic type: each word pops in exactly when it is spoken.
function kinetic(g, words, t, x, y, size, o = {}) {
  const weight = o.weight || 800, ls = o.ls ?? -size * 0.035, lh = o.lh || size * 1.1;
  const maxW = o.maxW || 1640, align = o.align || 'center', pop = o.pop ?? 0.22;
  const hi = new Set((o.hi || []).map(norm));
  g.save();
  font(g, size, SANS, weight); g.letterSpacing = ls + 'px';
  const space = g.measureText(' ').width;
  const items = words.map(w => ({ ...w, width: g.measureText(w.w).width }));
  g.restore();
  const lines = []; let cur = [], cw = 0;
  for (const it of items) {
    const add = (cur.length ? space : 0) + it.width;
    if (cur.length && cw + add > maxW) { lines.push({ items: cur, w: cw }); cur = []; cw = 0; }
    cw += (cur.length ? space : 0) + it.width; cur.push(it);
  }
  if (cur.length) lines.push({ items: cur, w: cw });
  lines.forEach((ln, li) => {
    let cx = align === 'center' ? x - ln.w / 2 : x;
    const cy = y + li * lh;
    for (const it of ln.items) {
      const p = clamp((t - it.start + 0.04) / 0.13);
      if (p > 0) {
        const e = E.out(p), s = 1 + pop * (1 - e);
        g.save();
        g.translate(cx + it.width / 2, cy); g.scale(s, s);
        g.globalAlpha *= e;
        font(g, size, SANS, weight); g.letterSpacing = ls + 'px';
        g.textAlign = 'center'; g.textBaseline = 'alphabetic';
        g.fillStyle = hi.has(norm(it.w)) ? (o.hiColor || C.orange) : (o.color || C.ink);
        g.fillText(it.w, 0, 0);
        g.restore();
      }
      cx += it.width + space;
    }
  });
}

// ---------- Fennec logo (native/macos/UI/icons/fennec.svg) ----------
const LOGO = {
  face: new Path2D('M5 39V9c0-3 2-4 4-2l23 15L55 7c2-2 4-1 4 2v30L34 57c-1.2.9-2.8.9-4 0Z'),
  earL: new Path2D('m8 6 24 16-12 9Z'),
  earR: new Path2D('m56 6-24 16 12 9Z'),
  muzzle: new Path2D('M5 39 30 22c1.2-.8 2.8-.8 4 0l25 17-25 18c-1.2.9-2.8.9-4 0Z'),
  muzzleL: new Path2D('m5 39 27-17v36L30 57Z'),
};
function logo(g, cx, cy, size) {
  const s = size / 64;
  g.save(); g.translate(cx - 32 * s, cy - 32 * s); g.scale(s, s);
  g.fillStyle = '#FFD8A1'; g.fill(LOGO.face);
  g.fillStyle = '#FFB953'; g.fill(LOGO.earL);
  g.fillStyle = '#F7A136'; g.fill(LOGO.earR);
  g.fillStyle = '#F38D25'; g.fill(LOGO.muzzle);
  g.fillStyle = '#FFA83F'; g.fill(LOGO.muzzleL);
  g.fillStyle = '#3B332E';
  for (const ex of [24, 40]) { g.beginPath(); g.arc(ex, 40, 2.6, 0, Math.PI * 2); g.fill(); }
  g.beginPath(); g.ellipse(32, 52, 3.2, 3.3, 0, 0, Math.PI * 2); g.fill();
  g.restore();
}

// ---------- backdrop and beat ----------
let TL = null;
const gridCanvas = document.createElement('canvas');
gridCanvas.width = W; gridCanvas.height = H;
{
  const gg = gridCanvas.getContext('2d');
  gg.fillStyle = '#F4EFE7';
  for (let y = 24; y < H; y += 48) for (let x = 24; x < W; x += 48) { gg.beginPath(); gg.arc(x, y, 1.4, 0, Math.PI * 2); gg.fill(); }
}
// Same as Timeline.gridAfter in cmd/trailer/timeline.go.
function gridAfter(T, q, n) { const k = Math.ceil((T - TL.grid0) / q - 1e-9); return TL.grid0 + (k + n) * q; }
function pulse(T) {
  if (T < TL.grid0 || T > TL.endHit) return 0;
  return Math.exp(-((T - TL.grid0) % TL.beat) * 10);
}
function backdrop(g, T) {
  g.fillStyle = C.bg; g.fillRect(0, 0, W, H);
  const p = pulse(T);
  g.save(); g.globalAlpha = 0.05 + 0.07 * p; g.drawImage(gridCanvas, 0, 0); g.restore();
  if (T >= TL.grid0) {
    const gr = g.createRadialGradient(W / 2, H + 260, 0, W / 2, H + 260, 1100);
    gr.addColorStop(0, `rgba(255,138,31,${0.10 + 0.08 * p})`); gr.addColorStop(1, 'rgba(255,138,31,0)');
    g.fillStyle = gr; g.fillRect(0, 0, W, H);
  }
  const v = g.createRadialGradient(W / 2, H / 2, 380, W / 2, H / 2, 1150);
  v.addColorStop(0, 'rgba(0,0,0,0)'); v.addColorStop(1, 'rgba(0,0,0,0.55)');
  g.fillStyle = v; g.fillRect(0, 0, W, H);
}

// ---------- terminal, laptop, keys ----------
function termWin(g, x, y, w, h, o = {}) {
  g.save();
  g.shadowColor = 'rgba(0,0,0,0.65)'; g.shadowBlur = 70; g.shadowOffsetY = 24;
  rrect(g, x, y, w, h, 22); g.fillStyle = C.panel; g.fill();
  g.shadowColor = 'transparent';
  g.save(); rrect(g, x, y, w, h, 22); g.clip();
  g.fillStyle = C.bar; g.fillRect(x, y, w, 58);
  ['#E9806B', '#E8B85C', '#8FBF73'].forEach((c, i) => { g.beginPath(); g.arc(x + 32 + i * 28, y + 29, 8.5, 0, Math.PI * 2); g.fillStyle = c; g.fill(); });
  text(g, o.title || 'storefront — zsh', x + w / 2, y + 37, { size: 20, color: C.dim, align: 'center', fam: MONO });
  if (o.badge > 0) {
    g.save(); g.globalAlpha *= o.badge;
    const bw = 236, bx = x + w - bw - 18, by = y + 12;
    rrect(g, bx, by, bw, 34, 17); g.fillStyle = 'rgba(255,138,31,0.16)'; g.fill();
    cloudGlyph(g, bx + 26, by + 18, 24, C.orange);
    text(g, 'Cloud computer', bx + 46, by + 24, { size: 18, color: C.orange, weight: 600 });
    g.restore();
  }
  if (o.body) { g.save(); g.translate(x, y + 58); o.body(g, w, h - 58); g.restore(); }
  g.restore();
  g.lineWidth = 2; g.strokeStyle = C.line; rrect(g, x, y, w, h, 22); g.stroke();
  if (o.glow > 0) {
    g.save(); g.shadowColor = C.orange; g.shadowBlur = 50 * o.glow; g.lineWidth = 3; g.strokeStyle = `rgba(255,138,31,${o.glow})`;
    rrect(g, x, y, w, h, 22); g.stroke(); g.restore();
  }
  g.restore();
}

const screenCanvas = document.createElement('canvas');
screenCanvas.width = 1520; screenCanvas.height = 960;
const SW = 760, SH = 480;
// open: 1 = open, 0 = closed. drawScreen paints a 760x480 screen.
function laptop(g, cx, baseY, s, open, drawScreen) {
  const lw = 820 * s, lh = 520 * s;
  g.save();
  const h = Math.max(lh * open, 7 * s);
  const topW = lw * (1 + 0.05 * (1 - open));
  const r = 18 * s * clamp(open * 3);
  const lid = new Path2D();
  lid.moveTo(cx - lw / 2, baseY);
  lid.lineTo(cx - topW / 2, baseY - h + r);
  lid.quadraticCurveTo(cx - topW / 2, baseY - h, cx - topW / 2 + r, baseY - h);
  lid.lineTo(cx + topW / 2 - r, baseY - h);
  lid.quadraticCurveTo(cx + topW / 2, baseY - h, cx + topW / 2, baseY - h + r);
  lid.lineTo(cx + lw / 2, baseY);
  lid.closePath();
  g.fillStyle = '#1A1815'; g.fill(lid);
  if (open > 0.03 && drawScreen) {
    const sg = screenCanvas.getContext('2d');
    sg.setTransform(2, 0, 0, 2, 0, 0);
    sg.clearRect(0, 0, SW, SH);
    drawScreen(sg, SW, SH);
    const inset = 18 * s;
    const sw = lw - inset * 2, sh = (lh - inset * 2) * open;
    g.save();
    rrect(g, cx - sw / 2, baseY - h + inset * open, sw, sh, 6 * s); g.clip();
    g.drawImage(screenCanvas, cx - sw / 2, baseY - h + inset * open, sw, sh);
    g.fillStyle = `rgba(0,0,0,${0.8 * (1 - open)})`;
    g.fillRect(cx - sw / 2, baseY - h, sw, h);
    g.restore();
  }
  g.lineWidth = 2.5 * s; g.strokeStyle = 'rgba(244,239,231,0.75)'; g.lineJoin = 'round'; g.stroke(lid);
  const tw = lw + 30 * s, bw = lw + 160 * s, bh = 28 * s;
  g.beginPath();
  g.moveTo(cx - tw / 2, baseY);
  g.lineTo(cx + tw / 2, baseY);
  g.lineTo(cx + bw / 2, baseY + bh - 9 * s);
  g.quadraticCurveTo(cx + bw / 2, baseY + bh, cx + bw / 2 - 12 * s, baseY + bh);
  g.lineTo(cx - bw / 2 + 12 * s, baseY + bh);
  g.quadraticCurveTo(cx - bw / 2, baseY + bh, cx - bw / 2, baseY + bh - 9 * s);
  g.closePath();
  g.fillStyle = '#1A1815'; g.fill(); g.stroke();
  g.restore();
}

function keycap(g, x, y, w, h, down, draw) {
  const depth = 18, off = down * 12;
  g.save();
  rrect(g, x, y + depth, w, h, 28); g.fillStyle = '#050404'; g.fill();
  g.lineWidth = 2; g.strokeStyle = C.line; g.stroke();
  rrect(g, x, y + off, w, h, 28);
  g.fillStyle = down > 0.5 ? '#2B1B0C' : C.panel2; g.fill();
  g.lineWidth = 3; g.strokeStyle = down > 0.5 ? C.orange : 'rgba(244,239,231,0.22)'; g.stroke();
  if (down > 0.5) { g.shadowColor = C.orange; g.shadowBlur = 40; g.stroke(); g.shadowColor = 'transparent'; }
  g.translate(x, y + off); draw(g, down > 0.5 ? C.orange : C.ink);
  g.restore();
}

function typed(str, at, cps, t) {
  const n = clamp(Math.floor((t - at) * cps + 1e-6), 0, str.length);
  return { s: str.slice(0, n), done: n >= str.length, enter: at + str.length / cps + 0.18 };
}
const caretOn = t => Math.floor(t * 2.4) % 2 === 0;
const caret = t => (caretOn(t) ? [{ t: '▍', c: C.orange }] : []);

// ---------- screens and shared lines ----------
const LOG = [
  [{ t: '> ', c: C.dim }, { t: 'Fix the flaky checkout tests', w: 600 }],
  [{ icon: 'dot', c: C.orange }, { t: 'Read  ', w: 600 }, { t: 'src/checkout/cart.ts', c: C.dim }],
  [{ icon: 'dot', c: C.orange }, { t: 'Read  ', w: 600 }, { t: 'src/checkout/cart.test.ts', c: C.dim }],
  [{ icon: 'dot', c: C.orange }, { t: 'Edit  ', w: 600 }, { t: 'src/checkout/cart.ts ', c: C.dim }, { t: '+12 -4', c: C.green }],
  [{ icon: 'dot', c: C.orange }, { t: 'Edit  ', w: 600 }, { t: 'src/checkout/session.ts ', c: C.dim }, { t: '+31 -9', c: C.green }],
  [{ icon: 'dot', c: C.orange }, { t: 'Run   ', w: 600 }, { t: 'npm test -- checkout', c: C.dim }],
  [{ t: '   ' }, { icon: 'check', c: C.green }, { t: '118 passed', c: C.ink }],
  [{ icon: 'dot', c: C.orange }, { t: 'Edit  ', w: 600 }, { t: 'src/checkout/retry.ts ', c: C.dim }, { t: '+8 -2', c: C.green }],
  [{ icon: 'dot', c: C.orange }, { t: 'Run   ', w: 600 }, { t: 'npm test', c: C.dim }],
  [{ t: '   ' }, { icon: 'check', c: C.green }, { t: '204 passed', c: C.ink }],
];
function workingScreen(sg, w, h, t) {
  sg.fillStyle = '#0F0E0C'; sg.fillRect(0, 0, w, h);
  sg.fillStyle = C.bar; sg.fillRect(0, 0, w, 34);
  text(sg, 'claude — storefront', w / 2, 23, { size: 14, color: C.dim, align: 'center', fam: MONO });
  const n = clamp(Math.floor(t * 7) + 2, 1, LOG.length);
  const start = Math.max(0, n - 10);
  LOG.slice(start, n).forEach((ln, i) => spans(sg, 30, 76 + i * 36, ln.map(s => ({ ...s, time: t })), 19));
  spans(sg, 30, 76 + Math.min(n, 10) * 36, [{ icon: 'spin', c: C.orange, time: t }, { t: 'working', c: C.dim }], 19);
}

const DETACH_LINES = [
  [{ t: 'Session detached.', w: 600 }],
  [{ t: 'Run ', c: C.dim }, { t: 'fen attach cone-leaf', c: C.orange, w: 600 }, { t: ' to reattach.', c: C.dim }],
];
function promptLine(cmd, withCaret, t) {
  return [{ t: '~/code/storefront ', c: C.path }, { t: '$ ', c: C.dim }, { t: cmd, w: 600 }, ...(withCaret ? caret(t) : [])];
}
const TERM = { x: 310, y: 290, w: 1300, h: 640, size: 32, lh: 54, px: 52, py: 82 };
function termLines(g2, lines) { lines.forEach((ln, i) => spans(g2, TERM.px, TERM.py + i * TERM.lh, ln, TERM.size)); }

// ---------- scenes ----------
const SCENES = {};

SCENES.close = (g, t, s) => {
  const shut = s.cue('shut').at;
  const open = 1 - E.in(seg(t, shut, 0.18));
  if (open > 0) {
    const gr = g.createRadialGradient(W / 2, 920, 10, W / 2, 920, 760);
    gr.addColorStop(0, `rgba(255,138,31,${0.2 * open})`); gr.addColorStop(1, 'rgba(255,138,31,0)');
    g.fillStyle = gr; g.fillRect(0, 480, W, 600);
  }
  const z = lerp(1.0, 1.05, seg(t, 0, s.dur));
  g.save(); g.translate(W / 2, H / 2); g.scale(z, z); g.translate(-W / 2, -H / 2);
  laptop(g, W / 2, 920, 0.9, open, (sg, w, h) => workingScreen(sg, w, h, Math.min(t, shut)));
  g.restore();
  kinetic(g, s.words, t, W / 2, 250, 124, { hi: ['laptop'] });
};

SCENES.stops = (g, t, s) => {
  const stopW = s.word('stops'), stop = s.cue('stop').at;
  if (t >= stop + 0.03) { g.fillStyle = '#000'; g.fillRect(0, 0, W, H); return; }
  const dead = t >= stopW.start;
  const glitch = seg(t, stopW.start, 0.05);
  g.save();
  if (glitch > 0) g.translate(Math.sin(t * 170) * 12 * glitch, Math.cos(t * 130) * 5 * glitch);
  kinetic(g, s.words, t, W / 2, 400, 150, { hi: ['stops'], hiColor: C.red });
  const cw = 980, ch = 210, cx = W / 2 - cw / 2, cy = 540;
  rrect(g, cx, cy, cw, ch, 24); g.fillStyle = C.panel; g.fill(); g.lineWidth = 2; g.strokeStyle = C.line; g.stroke();
  icon(g, dead ? 'dot' : 'spin', cx + 56, cy + 68, 34, dead ? C.red : C.orange, t);
  text(g, 'Running tests', cx + 96, cy + 80, { size: 36, weight: 600 });
  const p = 0.42 + Math.min(t, stopW.start) * 0.08;
  text(g, dead ? 'stopped' : Math.floor(p * 100) + '%', cx + cw - 44, cy + 80, { size: 32, fam: MONO, align: 'right', color: dead ? C.red : C.dim });
  rrect(g, cx + 44, cy + 132, cw - 88, 16, 8); g.fillStyle = C.faint; g.fill();
  rrect(g, cx + 44, cy + 132, (cw - 88) * p, 16, 8); g.fillStyle = dead ? '#5E5650' : C.orange; g.fill();
  g.restore();
  if (glitch > 0) { g.fillStyle = `rgba(0,0,0,${0.4 * glitch})`; for (let y = 0; y < H; y += 6) g.fillRect(0, y, W, 2); }
};

SCENES.drop = (g, t, s, T) => {
  const fl = 1 - seg(t, 0, 0.35);
  if (fl > 0) { g.fillStyle = `rgba(255,138,31,${0.9 * E.in(fl)})`; g.fillRect(0, 0, W, H); }
  const pb = 1 + 0.03 * pulse(T);
  g.save(); g.translate(W / 2, 560); g.scale(pb, pb); g.translate(-W / 2, -560);
  kinetic(g, s.words, t, W / 2, 620, 250, { pop: 0.6, hi: ['anymore'] });
  g.restore();
};

SCENES.fennec = (g, t, s, T) => {
  const w = s.words, w1 = w[1].start;
  const m = E.inOut(seg(t, w1 - 0.12, 0.4));
  const lp = E.back(seg(t, w[0].start - 0.06, 0.3));
  g.save(); g.globalAlpha *= clamp(lp * 2);
  g.translate(lerp(W / 2, 150, m), lerp(380, 128, m)); g.scale(lp, lp);
  logo(g, 0, 0, lerp(240, 76, m));
  g.restore();
  const wm = lerp(180, 62, m), ww = measure(g, 'Fennec', wm, SANS, 800, -wm * 0.035);
  text(g, 'Fennec', lerp(W / 2 - ww / 2, 206, m), lerp(700, 150, m), { size: wm, weight: 800, ls: -wm * 0.035, alpha: seg(t, w[0].start - 0.02, 0.12) });

  if (t > w1 - 0.15) kinetic(g, w.slice(1), t, 140, 420, 82, { align: 'left', maxW: 880, lh: 96, hi: ['cloud', 'computer', 'mac'] });

  // Cloud computer.
  const bx = 1140, bw = 620;
  const ca = E.back(seg(t, s.word('cloud').start - 0.06, 0.3));
  if (ca > 0) {
    g.save(); g.globalAlpha *= clamp(ca * 2); g.translate(bx + bw / 2, 340); g.scale(ca, ca); g.translate(-(bx + bw / 2), -340);
    rrect(g, bx, 200, bw, 280, 24); g.fillStyle = C.panel; g.fill(); g.lineWidth = 2.5; g.strokeStyle = 'rgba(255,138,31,0.6)'; g.stroke();
    cloudGlyph(g, bx + 56, 258, 44, C.orange);
    text(g, 'cloud computer', bx + 96, 270, { size: 34, weight: 700 });
    g.beginPath(); g.arc(bx + bw - 130, 260, 7, 0, Math.PI * 2); g.fillStyle = C.green; g.fill();
    text(g, 'running', bx + bw - 112, 268, { size: 22, fam: MONO, color: C.dim });
    ['agent', 'tests', 'build'].forEach((lab, i) => {
      const yy = 330 + i * 46;
      text(g, lab, bx + 40, yy + 10, { size: 22, fam: MONO, color: C.dim });
      const fw = (bw - 190) * (0.25 + 0.7 * Math.abs(Math.sin(T * (2.2 + i) + i * 1.3)));
      rrect(g, bx + 140, yy - 6, bw - 190, 14, 7); g.fillStyle = C.faint; g.fill();
      rrect(g, bx + 140, yy - 6, fw, 14, 7); g.fillStyle = C.orange; g.fill();
    });
    g.restore();
  }
  // Your Mac.
  const ma = E.back(seg(t, s.word('mac').start - 0.08, 0.3));
  if (ma > 0) {
    g.save(); g.globalAlpha *= clamp(ma * 2); g.translate(bx + bw / 2, 790); g.scale(ma, ma); g.translate(-(bx + bw / 2), -790);
    rrect(g, bx, 640, bw, 300, 24); g.fillStyle = C.panel; g.fill(); g.lineWidth = 2.5; g.strokeStyle = C.line; g.stroke();
    text(g, 'your Mac', bx + 36, 694, { size: 30, weight: 700 });
    laptop(g, bx + bw / 2 + 60, 900, 0.36, 1, (sg, sw, sh) => {
      sg.fillStyle = '#221E1A'; sg.fillRect(0, 0, sw, sh);
      sg.fillStyle = '#8FB6D8'; rrect(sg, 60, 70, 90, 66, 10); sg.fill();
      sg.fillStyle = '#0F0E0C'; rrect(sg, 200, 70, 90, 66, 10); sg.fill();
      text(sg, '>_', 245, 114, { size: 26, fam: MONO, align: 'center', weight: 700 });
    });
    g.restore();
  }
  // The link between them.
  const conn = s.cue('connect').at;
  const lp2 = E.inOut(seg(t, conn - 0.1, 0.3));
  if (lp2 > 0) {
    const x = bx + bw / 2, y0 = 490, y1 = 630;
    g.save(); g.setLineDash([3, 14]); g.lineCap = 'round'; g.lineWidth = 5; g.strokeStyle = C.orange;
    g.beginPath(); g.moveTo(x, y0); g.lineTo(x, lerp(y0, y1, lp2)); g.stroke(); g.setLineDash([]);
    if (t >= conn) {
      for (let k = 0; k < 3; k++) {
        const f = ((t - conn) * 1.4 + k / 3) % 1;
        g.beginPath(); g.arc(x, lerp(y0, y1, f), 7, 0, Math.PI * 2); g.fillStyle = C.ink; g.globalAlpha = Math.sin(f * Math.PI); g.fill();
      }
    }
    g.restore();
  }
};

SCENES.same = (g, t, s) => {
  kinetic(g, s.words, t, W / 2, 200, 104, { hi: ['terminal', 'agent'] });
  const c = s.cue('type'), tt = typed(c.text, c.at, c.cps, t);
  const launched = t > tt.enter + 0.05;
  g.save(); g.translate(0, (1 - E.out(seg(t, 0, 0.3))) * 40);
  termWin(g, TERM.x, TERM.y, TERM.w, TERM.h, {
    badge: seg(t, tt.enter + 0.1, 0.25),
    body: g2 => termLines(g2, launched ? [
      promptLine(c.text, false, t),
      [{ icon: 'cloud', c: C.orange }, { t: 'Fennec ', c: C.orange, w: 700 }, { t: 'running on your cloud computer', c: C.dim }],
      [{ t: '   session ', c: C.dim }, { t: 'cone-leaf', w: 600 }, { t: ' · connected to your Mac', c: C.dim }],
      [],
      [{ t: '> ', c: C.orange, w: 700 }, ...caret(t)],
    ] : [promptLine(tt.s, true, t)]),
  });
  g.restore();
};

// One name per eighth note; the ticks in cmd/trailer/score.go use the same
// grid and the same count (agentNames).
const AGENT_NAMES = [
  ['Claude Code', 'claude'], ['Codex', 'codex'], ['Pi', 'pi'], ['Kimi Code', 'kimi'],
  ['Cursor CLI', 'cursor-agent'], ['Grok Build', 'grok'], ['OpenCode', 'opencode'], ['Antigravity CLI', 'agy'],
];
SCENES.agents = (g, t, s, T) => {
  const iw = s.words.findIndex(w => norm(w.w) === 'whatever');
  const wv = s.words[iw].start;
  if (t < wv - 0.02) {
    const q = TL.beat / 2, first = gridAfter(s.start + 0.001, q, 0);
    const k = T < first ? 0 : Math.floor((T - first) / q + 1e-6) + 1;
    const idx = Math.min(k, AGENT_NAMES.length - 1);
    const since = idx === 0 ? t : T - (first + (idx - 1) * q);
    const sc = 1 + 0.14 * Math.exp(-since * 22);
    const [name, cmd] = AGENT_NAMES[idx];
    g.save(); g.translate(W / 2, 560); g.scale(sc, sc);
    text(g, name, 0, 0, { size: 180, weight: 800, align: 'center', ls: -6, alpha: clamp(since / 0.05) });
    g.restore();
    text(g, '$ ' + cmd, W / 2, 690, { size: 46, fam: MONO, align: 'center', color: C.orange });
    AGENT_NAMES.forEach((_, i) => {
      g.beginPath(); g.arc(W / 2 - 7 * 26 / 2 + i * 26, 790, 6, 0, Math.PI * 2);
      g.fillStyle = i <= idx ? C.orange : C.faint; g.fill();
    });
  } else {
    kinetic(g, s.words.slice(iw), t, W / 2, 600, 180, { hi: ['use'] });
  }
};

SCENES.ctrlc = (g, t, s) => {
  kinetic(g, s.words, t, W / 2, 220, 112, { hi: ['controlc'] });
  const c = s.cue('press');
  const ctrlDown = seg(t, c.at - 0.06, 0.06) * (1 - seg(t, c.at + 0.5, 0.08));
  const cDown = seg(t, c.at + c.interval - 0.04, 0.05) * (1 - seg(t, c.at + c.interval + 0.22, 0.06));
  const kx = W / 2 - 320, ky = 400;
  keycap(g, kx, ky, 360, 240, ctrlDown, (g2, col) => {
    g2.save(); g2.strokeStyle = col; g2.lineWidth = 8; g2.lineCap = 'round'; g2.lineJoin = 'round';
    g2.beginPath(); g2.moveTo(40, 92); g2.lineTo(72, 56); g2.lineTo(104, 92); g2.stroke(); g2.restore();
    text(g2, 'control', 40, 200, { size: 40, weight: 600, color: col });
  });
  keycap(g, kx + 400, ky, 240, 240, cDown, (g2, col) => {
    text(g2, 'C', 120, 160, { size: 130, weight: 800, align: 'center', color: col });
  });
  const a = seg(t, c.at + 0.4, 0.15);
  if (a > 0) {
    g.save(); g.globalAlpha *= a;
    DETACH_LINES.forEach((ln, i) => {
      const w = spansWidth(g, ln, 36);
      spans(g, W / 2 - w / 2, 800 + i * 58, ln, 36);
    });
    g.restore();
  }
};

SCENES.walk = (g, t, s) => {
  const z = lerp(1.12, 1, E.out(seg(t, 0, 0.6)));
  g.save(); g.translate(W / 2, H / 2); g.scale(z, z); g.translate(-W / 2, -H / 2);
  const cx = W / 2, cy = 540, r = 390;
  g.lineWidth = 4; g.strokeStyle = C.faint; g.beginPath(); g.arc(cx, cy, r, 0, Math.PI * 2); g.stroke();
  for (let i = 0; i < 12; i++) {
    const a = i / 12 * Math.PI * 2;
    g.beginPath(); g.moveTo(cx + Math.cos(a) * (r - 24), cy + Math.sin(a) * (r - 24)); g.lineTo(cx + Math.cos(a) * (r - 6), cy + Math.sin(a) * (r - 6));
    g.lineWidth = 4; g.strokeStyle = C.faint; g.stroke();
  }
  const ang = -Math.PI / 2 + t * 8;
  g.lineWidth = 6; g.strokeStyle = C.orange; g.lineCap = 'round';
  g.beginPath(); g.arc(cx, cy, r, -Math.PI / 2, ang); g.stroke();
  g.restore();
  kinetic(g, s.words, t, W / 2, 610, 200, { hi: ['away'] });
  const mins = 18 * 60 + 40 + Math.floor(t * 110);
  const clock = `${((Math.floor(mins / 60) + 11) % 12) + 1}:${String(mins % 60).padStart(2, '0')} PM`;
  text(g, clock, W / 2, 760, { size: 44, fam: MONO, align: 'center', color: C.dim });
};

SCENES.working = (g, t, s, T) => {
  kinetic(g, s.words, t, W / 2, 200, 112, { hi: ['working'] });
  const cw = 980, cx = W / 2 - cw / 2, cy = 290, ch = 600;
  g.save(); g.translate(0, (1 - E.out(seg(t, 0, 0.25))) * 40);
  rrect(g, cx, cy, cw, ch, 26); g.fillStyle = C.panel; g.fill(); g.lineWidth = 2.5; g.strokeStyle = 'rgba(255,138,31,0.55)'; g.stroke();
  cloudGlyph(g, cx + 60, cy + 66, 46, C.orange);
  text(g, 'cone-leaf', cx + 104, cy + 78, { size: 38, weight: 700 });
  text(g, 'Claude Code · cloud computer', cx + 300, cy + 76, { size: 24, fam: MONO, color: C.dim });
  g.save(); g.globalAlpha *= 0.6 + 0.4 * Math.sin(T * 8); g.beginPath(); g.arc(cx + cw - 150, cy + 66, 8, 0, Math.PI * 2); g.fillStyle = C.green; g.fill(); g.restore();
  text(g, 'running', cx + cw - 130, cy + 75, { size: 24, fam: MONO, color: C.green });
  g.fillStyle = C.line; g.fillRect(cx + 36, cy + 118, cw - 72, 2);
  ['Refactor checkout service', 'Update 14 tests', 'Run full test suite'].forEach((lab, i) => {
    // Checked on the beat; the chimes in cmd/trailer/score.go use the same times.
    const at = gridAfter(s.start + 0.25, TL.beat, i);
    const done = T >= at;
    const yy = cy + 196 + i * 96;
    const pop = done ? 1 + 0.35 * Math.exp(-(T - at) * 14) : 1;
    g.save(); g.translate(cx + 70, yy - 12); g.scale(pop, pop);
    icon(g, done ? 'check' : 'spin', 0, 0, 38, done ? C.green : C.orange, T);
    g.restore();
    text(g, lab, cx + 120, yy, { size: 38, weight: 600, color: done ? C.ink : C.dim });
  });
  const p = clamp(t / s.dur);
  rrect(g, cx + 48, cy + 500, cw - 96, 16, 8); g.fillStyle = C.faint; g.fill();
  rrect(g, cx + 48, cy + 500, (cw - 96) * p, 16, 8); g.fillStyle = C.orange; g.fill();
  text(g, `${Math.floor(342 * p)} / 342 tests passed`, cx + cw - 48, cy + 476, { size: 24, fam: MONO, align: 'right', color: C.dim });
  g.restore();
};

SCENES.attach = (g, t, s) => {
  kinetic(g, s.words, t, W / 2, 200, 104, { hi: ['fen', 'attach'] });
  const c = s.cue('type'), tt = typed(c.text, c.at, c.cps, t);
  termWin(g, TERM.x, TERM.y, TERM.w, TERM.h, {
    body: g2 => {
      const lines = [...DETACH_LINES, [], [{ t: '$ ', c: C.dim }, { t: tt.s, w: 600 }, ...(t < tt.enter ? caret(t) : [])]];
      if (t > tt.enter) lines.push([{ icon: 'spin', c: C.orange, time: t }, { t: 'reattaching to cone-leaf', c: C.dim }]);
      termLines(g2, lines);
    },
  });
};

SCENES.resume = (g, t, s) => {
  kinetic(g, s.words, t, W / 2, 200, 78, { hi: ['left', 'off'], maxW: 1800 });
  const at = s.cue('restore').at;
  const rows = [
    [{ icon: 'check', c: C.green }, { t: 'Refactored the checkout service' }],
    [{ icon: 'check', c: C.green }, { t: 'Updated 14 tests' }],
    [{ icon: 'check', c: C.green }, { t: '342 tests passing' }],
    [],
    [{ t: 'Ready for your review.', w: 700, c: C.orange }],
  ];
  termWin(g, TERM.x, TERM.y, TERM.w, TERM.h, {
    title: 'cone-leaf — Claude Code', badge: 1, glow: 1 - seg(t, at, 0.7),
    body: g2 => {
      spans(g2, TERM.px, TERM.py, [{ icon: 'cloud', c: C.orange }, { t: 'cone-leaf', w: 700 }, { t: ' reattached · still on your cloud computer', c: C.dim }], TERM.size);
      rows.forEach((ln, i) => {
        const a = seg(t, at + 0.1 + i * 0.13, 0.12);
        if (a <= 0) return;
        g2.save(); g2.globalAlpha *= a; g2.translate((1 - E.out(a)) * 40, 0);
        spans(g2, TERM.px, TERM.py + (i + 1.4) * TERM.lh, ln, TERM.size);
        g2.restore();
      });
    },
  });
};

SCENES.end = (g, t, s) => {
  const at = s.cue('logo').at;
  if (t >= at) {
    const fl = 1 - seg(t, at, 0.3);
    if (fl > 0) { g.fillStyle = `rgba(255,138,31,${0.55 * E.in(fl)})`; g.fillRect(0, 0, W, H); }
  }
  const p = seg(t, at, 0.3);
  if (p > 0) {
    g.save(); g.globalAlpha *= clamp(p * 3); g.translate(W / 2, 360); const sc = lerp(1.6, 1, E.out(p)); g.scale(sc, sc);
    logo(g, 0, 0, 230); g.restore();
  }
  const w0 = s.words[0];
  kinetic(g, [{ ...w0, w: w0.w.replace(/\.$/, '') }], t, W / 2, 660, 180, { pop: 0.3 });
  kinetic(g, s.words.slice(1), t, W / 2, 790, 68, { weight: 600, color: C.dim, ls: -1, hi: ['leave', 'the', 'laptop'] });
  text(g, 'runfennec.com', W / 2, 930, { size: 32, fam: MONO, align: 'center', color: C.dim, alpha: seg(t, s.segEnd + 0.3, 0.4) });
};

// ---------- compositor ----------
const canvas = document.getElementById('c');
const ctx = canvas.getContext('2d');

function sceneView(sc) {
  const cues = Object.fromEntries((sc.cues || []).map(c => [c.name, c]));
  return {
    ...sc,
    cue: n => { if (!cues[n]) throw new Error(`scene ${sc.id}: no cue ${n}`); return cues[n]; },
    word: w => { const x = sc.words.find(v => norm(v.w) === norm(w)); if (!x) throw new Error(`scene ${sc.id}: no word ${w}`); return x; },
  };
}

function drawAt(T) {
  const scenes = TL.views;
  let i = scenes.findIndex(s => T < s.start + s.dur);
  if (i < 0) i = scenes.length - 1;
  const sc = scenes[i];
  const t = Math.min(T - sc.start, sc.dur);
  ctx.save();
  backdrop(ctx, T);
  const fn = SCENES[sc.id];
  if (fn) fn(ctx, t, sc, T);
  ctx.restore();
  const edge = Math.min(clamp(T / 0.35), clamp((TL.duration - T) / 0.8));
  if (edge < 1) { ctx.fillStyle = '#000'; ctx.globalAlpha = 1 - E.inOut(edge); ctx.fillRect(0, 0, W, H); ctx.globalAlpha = 1; }
}

window.renderFrame = i => { drawAt(i / TL.fps); return canvas.toDataURL('image/png'); };

async function init() {
  try {
    const params = new URLSearchParams(location.search);
    TL = await (await fetch('timeline.json')).json();
    TL.views = TL.scenes.map(sceneView);
    const end = TL.views.find(s => s.id === 'end');
    TL.endHit = end.start + end.cue('logo').at;
    await Promise.all([
      document.fonts.load(`800 80px ${SANS}`), document.fonts.load(`700 30px ${SANS}`),
      document.fonts.load(`600 30px ${SANS}`), document.fonts.load(`500 30px ${SANS}`),
      document.fonts.load(`400 24px ${MONO}`), document.fonts.load(`600 24px ${MONO}`), document.fonts.load(`700 24px ${MONO}`),
    ]);
    if (params.has('render')) {
      document.body.classList.add('render');
    } else {
      let t0 = performance.now();
      canvas.addEventListener('click', () => { t0 = performance.now(); });
      const tick = () => { drawAt(((performance.now() - t0) / 1000) % TL.duration); requestAnimationFrame(tick); };
      tick();
    }
    window.trailerReady = true;
  } catch (e) {
    window.trailerError = String((e && e.stack) || e);
    throw e;
  }
}
init();
