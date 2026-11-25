// Fennec trailer scenes. Every frame is drawn from code as a pure function of
// time, so the Go renderer can ask for any frame in any order. Scene lengths,
// voice-over lines and cue times come from the resolved timeline served by
// `go run ./cmd/trailer`, the same data that places music, sound and voice.
'use strict';

const W = 1920, H = 1080;
const C = {
  bg: '#F3EEE4', bg2: '#E9E1D3', dusk: '#EED9C2',
  ink: '#221E1A', muted: '#776D62', faint: '#A69B8E', hair: 'rgba(34,30,26,0.14)',
  orange: '#F38D25', orangeDeep: '#D7701A', cream: '#FFD8A1', card: '#FBF8F2',
  term: '#1E1B18', termBar: '#2A2622', termText: '#ECE5D9', termDim: '#8E8478', path: '#A9C7DF',
  green: '#6FA35B', greenSoft: '#DDEBD3', alu: '#DED7CB', alu2: '#CBC3B6', bezel: '#2A2521',
};
const SERIF = '"Newsreader"', SANS = '"Inter"', MONO = '"JetBrains Mono"';

// ---------- math ----------
const clamp = (x, a = 0, b = 1) => Math.min(b, Math.max(a, x));
const lerp = (a, b, p) => a + (b - a) * p;
const seg = (t, a, d) => clamp((t - a) / d);
const E = {
  out: x => 1 - Math.pow(1 - x, 3),
  out5: x => 1 - Math.pow(1 - x, 5),
  in: x => x * x * x,
  inOut: x => (x < 0.5 ? 4 * x * x * x : 1 - Math.pow(-2 * x + 2, 3) / 2),
  back: x => { const c1 = 1.45, c3 = c1 + 1; return 1 + c3 * Math.pow(x - 1, 3) + c1 * Math.pow(x - 1, 2); },
};
function mixColor(a, b, p) {
  const pa = parseInt(a.slice(1), 16), pb = parseInt(b.slice(1), 16);
  const ch = s => Math.round(lerp((pa >> s) & 255, (pb >> s) & 255, clamp(p)));
  return `rgb(${ch(16)},${ch(8)},${ch(0)})`;
}

// ---------- drawing helpers ----------
function font(g, size, fam, weight = 400, style = '') {
  g.font = `${style} ${weight} ${size}px ${fam}`.trim();
}
function text(g, s, x, y, o = {}) {
  g.save();
  font(g, o.size || 32, o.fam || SANS, o.weight || 400, o.style || '');
  g.fillStyle = o.color || C.ink;
  g.textAlign = o.align || 'left';
  g.textBaseline = o.base || 'alphabetic';
  if (o.alpha != null) g.globalAlpha *= o.alpha;
  if (o.ls) g.letterSpacing = o.ls + 'px';
  g.fillText(s, x, y);
  g.restore();
}
function measure(g, s, size, fam, weight = 400, style = '') {
  g.save(); font(g, size, fam, weight, style);
  const w = g.measureText(s).width; g.restore(); return w;
}
function rrect(g, x, y, w, h, r) { g.beginPath(); g.roundRect(x, y, w, h, r); }
// reveal fades and lifts whatever is drawn next; call inside save/restore.
function reveal(g, p, dy = 26) {
  const e = E.out(clamp(p));
  g.globalAlpha *= e;
  g.translate(0, (1 - e) * dy);
}
function withAlpha(g, a, fn) { if (a <= 0.001) return; g.save(); g.globalAlpha *= a; fn(); g.restore(); }

// Staggered serif headline lines.
function headline(g, lines, x, y, size, t, t0, o = {}) {
  const lh = o.lh || size * 1.12;
  lines.forEach((ln, i) => {
    const p = seg(t, t0 + i * (o.stagger ?? 0.14), o.fade || 0.8);
    if (p <= 0) return;
    g.save(); reveal(g, p, 22);
    const spec = typeof ln === 'string' ? { t: ln } : ln;
    text(g, spec.t, x, y + i * lh, {
      size, fam: SERIF, weight: spec.weight || o.weight || 400, style: spec.italic ? 'italic' : '',
      color: spec.color || o.color || C.ink, align: o.align || 'left',
    });
    g.restore();
  });
}

// Icons drawn as shapes so every machine renders them identically.
function icon(g, kind, x, y, s, color, t = 0) {
  g.save(); g.translate(x, y); g.strokeStyle = color; g.fillStyle = color;
  g.lineWidth = Math.max(1.5, s * 0.16); g.lineCap = 'round'; g.lineJoin = 'round';
  if (kind === 'dot') { g.beginPath(); g.arc(0, 0, s * 0.28, 0, Math.PI * 2); g.fill(); }
  if (kind === 'check') {
    g.beginPath(); g.moveTo(-s * 0.32, 0); g.lineTo(-s * 0.08, s * 0.24); g.lineTo(s * 0.36, -s * 0.26); g.stroke();
  }
  if (kind === 'spin') {
    const a = t * 6;
    g.beginPath(); g.arc(0, 0, s * 0.3, a, a + Math.PI * 1.4); g.stroke();
  }
  if (kind === 'cloud') cloudGlyph(g, 0, 0, s, color);
  g.restore();
}
function cloudGlyph(g, cx, cy, s, color, fill = false) {
  g.save(); g.translate(cx, cy); g.scale(s / 40, s / 40);
  g.beginPath();
  g.moveTo(-14, 10);
  g.arc(-12, 2, 8, Math.PI * 0.5, Math.PI * 1.35);
  g.arc(-2, -5, 10, Math.PI * 1.1, Math.PI * 1.85);
  g.arc(10, 1, 9, Math.PI * 1.45, Math.PI * 0.5);
  g.closePath();
  g.lineWidth = 3.2; g.strokeStyle = color; g.lineJoin = 'round';
  if (fill) { g.fillStyle = color; g.fill(); } else g.stroke();
  g.restore();
}

// Segments: [{t, c, w}, {icon, c}] drawn left to right in one line.
function spans(g, x, y, segs, size, fam = MONO) {
  let cx = x;
  for (const s of segs) {
    if (s.icon) {
      icon(g, s.icon, cx + size * 0.45, y - size * 0.33, size * 0.9, s.c || C.termText, s.time || 0);
      cx += size * 1.2;
      continue;
    }
    font(g, size, fam, s.w || 400);
    g.fillStyle = s.c || C.termText;
    g.textBaseline = 'alphabetic';
    g.fillText(s.t, cx, y);
    cx += g.measureText(s.t).width;
  }
  return cx;
}

// ---------- Fennec logo (native/macos/UI/icons/fennec.svg) ----------
const LOGO = {
  face: new Path2D('M5 39V9c0-3 2-4 4-2l23 15L55 7c2-2 4-1 4 2v30L34 57c-1.2.9-2.8.9-4 0Z'),
  earL: new Path2D('m8 6 24 16-12 9Z'),
  earR: new Path2D('m56 6-24 16 12 9Z'),
  muzzle: new Path2D('M5 39 30 22c1.2-.8 2.8-.8 4 0l25 17-25 18c-1.2.9-2.8.9-4 0Z'),
  muzzleL: new Path2D('m5 39 27-17v36L30 57Z'),
};
function logo(g, cx, cy, size, o = {}) {
  const s = size / 64;
  g.save(); g.translate(cx - 32 * s, cy - 32 * s); g.scale(s, s);
  g.fillStyle = '#FFD8A1'; g.fill(LOGO.face);
  g.save(); g.translate(24, 24); g.rotate(o.earL || 0); g.translate(-24, -24); g.fillStyle = '#FFB953'; g.fill(LOGO.earL); g.restore();
  g.save(); g.translate(40, 24); g.rotate(-(o.earR ?? o.earL ?? 0)); g.translate(-40, -24); g.fillStyle = '#F7A136'; g.fill(LOGO.earR); g.restore();
  g.fillStyle = '#F38D25'; g.fill(LOGO.muzzle);
  g.fillStyle = '#FFA83F'; g.fill(LOGO.muzzleL);
  const blink = o.blink || 0;
  g.fillStyle = '#3B332E';
  for (const ex of [24, 40]) { g.beginPath(); g.ellipse(ex, 40, 2.6, 2.6 * (1 - 0.9 * blink), 0, 0, Math.PI * 2); g.fill(); }
  g.beginPath(); g.ellipse(32, 52, 3.2, 3.3, 0, 0, Math.PI * 2); g.fill();
  g.restore();
}

// ---------- laptop ----------
const screenCanvas = document.createElement('canvas');
screenCanvas.width = 1520; screenCanvas.height = 960;
const SW = 760, SH = 480;

// open: 1 = open, 0 = closed. drawScreen paints a 760x480 screen.
function laptop(g, cx, baseY, s, open, drawScreen) {
  const lw = 820 * s, lh = 520 * s;
  g.save();
  g.fillStyle = 'rgba(90,60,30,0.10)';
  g.beginPath(); g.ellipse(cx, baseY + 36 * s, lw / 2 + 130 * s, 20 * s, 0, 0, Math.PI * 2); g.fill();

  // Lid: a trapezoid whose projected height follows the hinge angle.
  const h = Math.max(lh * open, 7 * s);
  const topW = lw * (1 + 0.05 * (1 - open));
  const lid = new Path2D();
  const r = 18 * s * clamp(open * 3);
  lid.moveTo(cx - lw / 2, baseY);
  lid.lineTo(cx - topW / 2, baseY - h + r);
  lid.quadraticCurveTo(cx - topW / 2, baseY - h, cx - topW / 2 + r, baseY - h);
  lid.lineTo(cx + topW / 2 - r, baseY - h);
  lid.quadraticCurveTo(cx + topW / 2, baseY - h, cx + topW / 2, baseY - h + r);
  lid.lineTo(cx + lw / 2, baseY);
  lid.closePath();
  g.fillStyle = C.bezel; g.fill(lid);
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
    g.fillStyle = `rgba(0,0,0,${0.75 * (1 - open)})`;
    g.fillRect(cx - sw / 2, baseY - h, sw, h);
    g.restore();
  }
  // The lid's back shows as it folds flat.
  withAlpha(g, clamp(1 - open / 0.22), () => { g.fillStyle = C.alu; g.fill(lid); });
  g.lineWidth = 2.5 * s; g.strokeStyle = C.ink; g.lineJoin = 'round'; g.stroke(lid);

  // Base deck.
  const tw = lw + 30 * s, bw = lw + 160 * s, bh = 28 * s;
  g.beginPath();
  g.moveTo(cx - tw / 2, baseY);
  g.lineTo(cx + tw / 2, baseY);
  g.lineTo(cx + bw / 2, baseY + bh - 9 * s);
  g.quadraticCurveTo(cx + bw / 2, baseY + bh, cx + bw / 2 - 12 * s, baseY + bh);
  g.lineTo(cx - bw / 2 + 12 * s, baseY + bh);
  g.quadraticCurveTo(cx - bw / 2, baseY + bh, cx - bw / 2, baseY + bh - 9 * s);
  g.closePath();
  g.fillStyle = C.alu; g.fill(); g.stroke();
  g.beginPath(); g.moveTo(cx - 80 * s, baseY + 1.5 * s);
  g.quadraticCurveTo(cx, baseY + 14 * s, cx + 80 * s, baseY + 1.5 * s);
  g.lineWidth = 2 * s; g.strokeStyle = C.alu2; g.stroke();
  g.restore();
}

// ---------- windows, cards, chips ----------
function terminal(g, x, y, w, h, o = {}) {
  g.save();
  g.fillStyle = 'rgba(60,40,20,0.12)';
  rrect(g, x + 10, y + 22, w, h, 22); g.fill();
  rrect(g, x, y, w, h, 20); g.fillStyle = C.term; g.fill();
  g.save(); g.clip();
  g.fillStyle = C.termBar; g.fillRect(x, y, w, 52);
  ['#E9806B', '#E8B85C', '#8FBF73'].forEach((c, i) => {
    g.beginPath(); g.arc(x + 30 + i * 26, y + 26, 8, 0, Math.PI * 2); g.fillStyle = c; g.fill();
  });
  text(g, o.title || 'storefront — zsh', x + w / 2, y + 33, { size: 18, color: C.termDim, align: 'center', fam: SANS, weight: 500 });
  if (o.badge > 0) {
    withAlpha(g, o.badge, () => {
      const bw = 214, bx = x + w - bw - 18, by = y + 11;
      rrect(g, bx, by, bw, 30, 15); g.fillStyle = 'rgba(243,141,37,0.16)'; g.fill();
      cloudGlyph(g, bx + 24, by + 16, 22, C.orange);
      text(g, 'Cloud computer', bx + 42, by + 21, { size: 16, color: C.orange, weight: 600 });
    });
  }
  if (o.body) { g.save(); g.translate(x, y + 52); o.body(g, w, h - 52); g.restore(); }
  g.restore();
  g.lineWidth = 2.5; g.strokeStyle = C.ink; rrect(g, x, y, w, h, 20); g.stroke();
  g.restore();
}

function card(g, x, y, w, h, r = 22) {
  g.fillStyle = 'rgba(60,40,20,0.08)'; rrect(g, x + 8, y + 16, w, h, r); g.fill();
  g.fillStyle = C.card; rrect(g, x, y, w, h, r); g.fill();
  g.lineWidth = 2.5; g.strokeStyle = C.ink; g.stroke();
}

// The cloud computer: a card listing what the agent is doing.
function cloudCard(g, x, y, w, h, t, rows, o = {}) {
  card(g, x, y, w, h);
  cloudGlyph(g, x + 40, y + 42, 34, C.orange);
  text(g, o.title || 'Your cloud computer', x + 70, y + 51, { size: 26, weight: 600 });
  const pulse = 0.55 + 0.45 * Math.sin(t * 4);
  g.beginPath(); g.arc(x + w - 120, y + 42, 7, 0, Math.PI * 2);
  g.save(); g.fillStyle = C.green; g.globalAlpha *= pulse; g.fill(); g.restore();
  text(g, 'running', x + w - 104, y + 50, { size: 20, color: C.muted, weight: 500 });
  g.fillStyle = C.hair; g.fillRect(x + 24, y + 80, w - 48, 1.5);
  rows.forEach((r, i) => {
    if (r.alpha <= 0) return;
    g.save(); reveal(g, r.alpha, 10);
    const ry = y + 128 + i * (o.rowH || 54);
    const ic = r.state === 'done' ? 'check' : r.state === 'run' ? 'spin' : 'dot';
    const col = r.state === 'done' ? C.green : C.orange;
    icon(g, ic, x + 44, ry - 9, 26, col, t);
    text(g, r.text, x + 76, ry, { size: o.size || 25, color: r.state === 'done' ? C.muted : C.ink, weight: 500 });
    if (r.progress != null) {
      const bx = x + 76, bw = w - 76 - 40, by = ry + 18;
      rrect(g, bx, by, bw, 8, 4); g.fillStyle = C.bg2; g.fill();
      rrect(g, bx, by, Math.max(8, bw * r.progress), 8, 4); g.fillStyle = C.orange; g.fill();
    }
    g.restore();
  });
}

function chip(g, label, cx, cy, p, style) {
  if (p <= 0) return;
  const size = 26;
  const w = measure(g, label, size, SANS, 500) + 52, h = 58;
  g.save(); g.translate(cx, cy);
  const s = E.back(clamp(p)); g.scale(s, s); g.globalAlpha *= clamp(p * 2);
  rrect(g, -w / 2, -h / 2, w, h, h / 2);
  if (style === 'tool') { g.fillStyle = C.bg2; g.fill(); }
  else { g.fillStyle = C.card; g.fill(); g.lineWidth = 2.2; g.strokeStyle = C.ink; g.stroke(); }
  text(g, label, 0, 9, { size, weight: 500, align: 'center', color: style === 'tool' ? C.muted : C.ink });
  g.restore();
  return w;
}

function cursorArrow(g, x, y, pressed) {
  g.save(); g.translate(x, y); const s = pressed ? 0.9 : 1; g.scale(s * 1.4, s * 1.4);
  g.beginPath(); g.moveTo(0, 0); g.lineTo(0, 22); g.lineTo(6, 17); g.lineTo(10, 26); g.lineTo(14, 24); g.lineTo(10, 15.5); g.lineTo(17, 15.5); g.closePath();
  g.fillStyle = C.ink; g.fill(); g.lineWidth = 1.6; g.strokeStyle = '#fff'; g.stroke();
  g.restore();
}

function phone(g, cx, cy, w, h, draw) {
  const x = cx - w / 2, y = cy - h / 2;
  g.save();
  g.fillStyle = 'rgba(60,40,20,0.12)'; rrect(g, x + 12, y + 24, w, h, 56); g.fill();
  rrect(g, x, y, w, h, 56); g.fillStyle = C.ink; g.fill();
  const ix = x + 13, iy = y + 13, iw = w - 26, ih = h - 26;
  rrect(g, ix, iy, iw, ih, 44); g.fillStyle = C.card; g.fill();
  g.save(); g.clip(); draw(g, ix, iy, iw, ih); g.restore();
  rrect(g, cx - 52, y + 28, 104, 30, 15); g.fillStyle = C.ink; g.fill();
  g.restore();
}

// Typing helper: text typed from `at` at `cps` characters per second.
function typed(str, at, cps, t) {
  const n = clamp(Math.floor((t - at) * cps + 1e-6), 0, str.length);
  return { s: str.slice(0, n), done: n >= str.length, enter: at + str.length / cps + 0.35 };
}
const caretOn = t => Math.floor(t * 2.2) % 2 === 0;

// ---------- screens ----------
function agentLog(t, t0, every) {
  const all = [
    [{ t: '> ', c: C.termDim }, { t: 'Fix the flaky checkout tests', w: 500 }],
    [],
    [{ icon: 'dot', c: C.orange }, { t: 'Read  ', w: 600 }, { t: 'src/checkout/cart.ts', c: C.termDim }],
    [{ icon: 'dot', c: C.orange }, { t: 'Read  ', w: 600 }, { t: 'src/checkout/cart.test.ts', c: C.termDim }],
    [{ icon: 'dot', c: C.orange }, { t: 'Edit  ', w: 600 }, { t: 'src/checkout/cart.ts ', c: C.termDim }, { t: '+12 -4', c: C.green }],
    [{ icon: 'dot', c: C.orange }, { t: 'Run   ', w: 600 }, { t: 'npm test -- checkout', c: C.termDim }],
  ];
  const n = clamp(Math.floor((t - t0) / every) + 1, 0, all.length);
  return all.slice(0, n);
}

function workingScreen(sg, w, h, t, frozen) {
  sg.fillStyle = C.term; sg.fillRect(0, 0, w, h);
  sg.fillStyle = C.termBar; sg.fillRect(0, 0, w, 34);
  text(sg, 'claude — storefront', w / 2, 23, { size: 14, color: C.termDim, align: 'center', weight: 500 });
  const lines = agentLog(frozen, 0.2, 0.55);
  lines.forEach((ln, i) => spans(sg, 30, 78 + i * 34, ln.map(s => ({ ...s, time: frozen })), 19));
  if (lines.length >= 6) {
    const p = clamp((frozen - 3.5) / 14) * 0.9 + 0.02;
    const passed = Math.floor(342 * p);
    spans(sg, 30, 78 + 6 * 34, [{ t: '  ' }, { icon: 'check', c: C.green }, { t: `${passed} passed   `, c: C.termText }, { icon: 'spin', c: C.orange, time: frozen }, { t: `${342 - passed} running`, c: C.termDim }], 19);
    sg.fillStyle = '#34302B'; rrect(sg, 30, h - 54, w - 60, 10, 5); sg.fill();
    sg.fillStyle = C.orange; rrect(sg, 30, h - 54, (w - 60) * p, 10, 5); sg.fill();
  }
}

function macScreen(sg, w, h, t) {
  const gr = sg.createLinearGradient(0, 0, w, h);
  gr.addColorStop(0, '#F6E3C8'); gr.addColorStop(1, '#E9C9A6');
  sg.fillStyle = gr; sg.fillRect(0, 0, w, h);
  sg.fillStyle = 'rgba(255,255,255,0.55)'; sg.fillRect(0, 0, w, 26);
  logo(sg, w - 70, 13, 18);
  text(sg, '9:41', w - 22, 18, { size: 13, weight: 600, align: 'right', color: C.ink });
  // A folder and a terminal on the desktop.
  sg.fillStyle = '#8FB6D8'; rrect(sg, 60, 70, 76, 56, 8); sg.fill();
  sg.fillStyle = '#A7C7E3'; rrect(sg, 60, 64, 34, 14, 4); sg.fill();
  text(sg, 'storefront', 98, 150, { size: 14, align: 'center', weight: 500 });
  sg.fillStyle = C.term; rrect(sg, 60, 186, 76, 60, 10); sg.fill();
  text(sg, '>_', 98, 226, { size: 22, fam: MONO, align: 'center', color: C.termText, weight: 600 });
  text(sg, 'Terminal', 98, 270, { size: 14, align: 'center', weight: 500 });
}

const DETACH_LINES = [
  [{ t: 'Session detached.', w: 500 }],
  [{ t: 'Run ', c: C.termDim }, { t: 'fen attach cone-leaf', c: C.orange, w: 600 }, { t: ' to reattach, or ', c: C.termDim }, { t: 'fen', c: C.orange, w: 600 }, { t: ' to manage sessions.', c: C.termDim }],
];

// ---------- scenes ----------
const SCENES = {};

SCENES.lid = (g, t, sc) => {
  const close = sc.cue('lidClose').at;
  const pc = E.in(seg(t, close, 0.55));
  const settle = pc >= 1 ? Math.exp(-(t - close - 0.55) * 9) * Math.sin((t - close - 0.55) * 30) * 0.012 : 0;
  const open = clamp(1 - pc + Math.abs(settle));
  const z = lerp(1.07, 1.0, E.inOut(seg(t, 0, close)));
  const drop = 60 * E.inOut(seg(t, close + 0.7, 1.1));
  g.save();
  g.translate(W / 2, H / 2); g.scale(z, z); g.translate(-W / 2, -H / 2);
  laptop(g, W / 2, 730 + drop, 1, open, (sg, w, h) => workingScreen(sg, w, h, t, Math.min(t, close)));

  // Status pill under the laptop.
  const stopped = seg(t, close + 0.45, 0.4);
  const py = 840 + drop;
  const label = stopped > 0.5 ? 'Agent stopped' : 'Agent working';
  const pw = measure(g, label, 24, SANS, 500) + 70;
  rrect(g, W / 2 - pw / 2, py - 24, pw, 48, 24);
  g.fillStyle = C.card; g.fill(); g.lineWidth = 2; g.strokeStyle = C.hair; g.stroke();
  g.beginPath(); g.arc(W / 2 - pw / 2 + 30, py, 7, 0, Math.PI * 2);
  g.fillStyle = mixColor(C.orange, '#B7AEA3', stopped);
  g.save(); g.globalAlpha *= stopped > 0.5 ? 1 : 0.6 + 0.4 * Math.sin(t * 5); g.fill(); g.restore();
  text(g, label, W / 2 - pw / 2 + 48, py + 8, { size: 24, weight: 500, color: stopped > 0.5 ? C.muted : C.ink });
  g.restore();

  g.save(); reveal(g, seg(t, close + 0.9, 1.0));
  text(g, 'Everything stops.', W / 2, 500, { size: 112, fam: SERIF, align: 'center', weight: 400 });
  g.restore();
};

SCENES.meet = (g, t, sc) => {
  const at = sc.cue('logo').at;
  const p = seg(t, at, 1.0);
  const twitch = Math.sin(clamp((t - at - 0.9) / 0.35) * Math.PI) * 0.1;
  const blink = Math.sin(clamp((t - at - 1.7) / 0.18) * Math.PI);
  g.save();
  g.globalAlpha = E.out(seg(t, at, 0.45));
  g.translate(W / 2, 420); const s = lerp(0.72, 1, E.back(p)); g.scale(s, s);
  logo(g, 0, 0, 250, { earL: twitch, blink });
  g.restore();
  g.save(); reveal(g, seg(t, at + 0.45, 0.9), 20);
  text(g, 'Fennec', W / 2, 740, { size: 150, fam: SERIF, weight: 500, align: 'center' });
  g.restore();
};

SCENES.cloud = (g, t, sc) => {
  const L = sc.lines[0], connect = sc.cue('connect').at;
  headline(g, ['Its own computer,', { t: 'in the cloud.', italic: true, color: C.orangeDeep }], 150, 420, 92, t, L.start + 0.5);
  g.save(); reveal(g, seg(t, connect, 0.8));
  text(g, 'Connected to your Mac.', 152, 640, { size: 38, color: C.muted, weight: 500 });
  g.restore();

  const cx = 1345, cardY = 150, cardH = 330, cardW = 600;
  const rows = [
    { text: 'Planning the change', at: L.start + 0.9, done: L.start + 2.6 },
    { text: 'Editing 4 files', at: L.start + 1.8, done: L.start + 4.0 },
    { text: 'Running tests', at: L.start + 3.0, done: 1e9 },
  ].map(r => ({ text: r.text, alpha: seg(t, r.at, 0.5), state: t > r.done ? 'done' : t > r.at ? 'run' : 'todo' }));
  g.save(); reveal(g, seg(t, L.start - 0.2, 0.8));
  cloudCard(g, cx - cardW / 2, cardY, cardW, cardH, t, rows);
  g.restore();

  const lapTop = 930 - 520 * 0.44;
  g.save(); reveal(g, seg(t, L.start + 0.6, 0.8));
  laptop(g, cx, 930, 0.44, 1, (sg, w, h) => macScreen(sg, w, h, t));
  g.restore();

  // Link between the two machines.
  const y0 = cardY + cardH + 14, y1 = lapTop - 14;
  const lp = E.inOut(seg(t, connect - 0.3, 0.7));
  if (lp > 0) {
    g.save();
    g.setLineDash([2, 14]); g.lineCap = 'round'; g.lineWidth = 5; g.strokeStyle = C.faint;
    g.beginPath(); g.moveTo(cx, y0); g.lineTo(cx, lerp(y0, y1, lp)); g.stroke();
    g.setLineDash([]);
    if (lp >= 1) {
      for (let k = 0; k < 3; k++) {
        const f = ((t - connect) * 0.55 + k / 3) % 1;
        g.beginPath(); g.arc(cx - 0, lerp(y0, y1, f), 7, 0, Math.PI * 2); g.fillStyle = C.orange;
        g.globalAlpha = Math.sin(f * Math.PI); g.fill();
        const f2 = ((t - connect) * 0.55 + k / 3 + 0.5) % 1;
        g.beginPath(); g.arc(cx, lerp(y1, y0, f2), 5, 0, Math.PI * 2); g.fillStyle = C.ink;
        g.globalAlpha = 0.5 * Math.sin(f2 * Math.PI); g.fill();
      }
    }
    g.restore();
  }
};

SCENES.brain = (g, t, sc) => {
  const L0 = sc.lines[0], ask = sc.cue('ask').at, ok = sc.cue('approve').at;
  const handsAt = lerp(L0.start, L0.end, 0.5);
  const lx = 520, rx = 1400;

  headline(g, [{ t: 'Brain in the cloud.' }], lx, 200, 64, t, L0.start, { align: 'center' });
  const rows = [
    { text: 'Reading the codebase', alpha: 1, state: 'done' },
    { text: 'Planning the fix', alpha: 1, state: 'done' },
    { text: 'Writing the change', alpha: 1, state: 'run' },
  ];
  g.save(); reveal(g, seg(t, L0.start + 0.15, 0.8));
  cloudCard(g, lx - 300, 290, 600, 330, t, rows);
  g.restore();

  headline(g, [{ t: 'Hands on your Mac.' }], rx, 200, 64, t, handsAt, { align: 'center' });
  g.save(); reveal(g, seg(t, handsAt + 0.15, 0.8));
  laptop(g, rx, 600, 0.5, 1, (sg, w, h) => macScreen(sg, w, h, t));
  g.restore();
  const tiles = [['Files', 'folder'], ['Terminal', 'term'], ['Browser', 'web']];
  tiles.forEach(([label, kind], i) => {
    const p = seg(t, handsAt + 0.5 + i * 0.12, 0.6);
    if (p <= 0) return;
    const tx = rx - 190 + i * 190, ty = 760;
    const lit = kind === 'folder' ? seg(t, ok + 0.3, 0.4) : 0;
    g.save(); reveal(g, p, 16);
    rrect(g, tx - 52, ty - 52, 104, 104, 24);
    g.fillStyle = mixColor(C.card, '#FFE9CF', lit); g.fill();
    g.lineWidth = 2.2; g.strokeStyle = lit > 0 ? mixColor(C.ink, C.orange, lit) : C.ink; g.stroke();
    g.lineWidth = 3; g.strokeStyle = C.ink; g.fillStyle = C.ink;
    if (kind === 'folder') {
      g.beginPath(); g.moveTo(tx - 26, ty - 14); g.lineTo(tx - 26, ty + 20); g.lineTo(tx + 26, ty + 20); g.lineTo(tx + 26, ty - 8);
      g.lineTo(tx - 2, ty - 8); g.lineTo(tx - 8, ty - 16); g.lineTo(tx - 24, ty - 16); g.closePath(); g.stroke();
    } else if (kind === 'term') {
      text(g, '>_', tx, ty + 12, { size: 34, fam: MONO, weight: 700, align: 'center' });
    } else {
      g.beginPath(); g.arc(tx, ty, 24, 0, Math.PI * 2); g.stroke();
      g.beginPath(); g.ellipse(tx, ty, 10, 24, 0, 0, Math.PI * 2); g.stroke();
      g.beginPath(); g.moveTo(tx - 24, ty); g.lineTo(tx + 24, ty); g.stroke();
    }
    text(g, label, tx, ty + 92, { size: 24, weight: 500, align: 'center', color: C.muted });
    g.restore();
  });

  // Cloud reaches the approved folder once allowed.
  const reach = E.inOut(seg(t, ok + 0.35, 0.8));
  if (reach > 0) {
    g.save(); g.strokeStyle = C.orange; g.lineWidth = 4; g.setLineDash([2, 12]); g.lineCap = 'round';
    g.beginPath();
    const x0 = lx + 300, y0 = 455, x1 = rx - 190, y1 = 700;
    const steps = 40;
    for (let i = 0; i <= steps * reach; i++) {
      const u = i / steps;
      const x = lerp(x0, x1, u), y = lerp(y0, y1, u) - Math.sin(u * Math.PI) * 120;
      i ? g.lineTo(x, y) : g.moveTo(x, y);
    }
    g.stroke(); g.restore();
  }

  // Approval dialog.
  const show = seg(t, ask, 0.45) * (1 - seg(t, ok + 0.85, 0.4));
  if (show > 0) {
    g.save();
    g.fillStyle = `rgba(243,238,228,${0.55 * show})`; g.fillRect(0, 0, W, H);
    const dw = 780, dh = 330, dx = W / 2 - dw / 2, dy = 360;
    g.globalAlpha = E.out(show);
    g.translate(W / 2, dy + dh / 2); const s = lerp(0.94, 1, E.back(seg(t, ask, 0.5))); g.scale(s, s); g.translate(-W / 2, -(dy + dh / 2));
    card(g, dx, dy, dw, dh, 26);
    const allowed = seg(t, ok + 0.2, 0.3);
    rrect(g, dx + 40, dy + 36, 222, 40, 20); g.fillStyle = C.bg2; g.fill();
    text(g, 'Ask for approval', dx + 151, dy + 63, { size: 20, weight: 600, align: 'center', color: C.muted });
    withAlpha(g, 1 - allowed, () => {
      text(g, 'Let your agent open this project?', dx + 40, dy + 140, { size: 42, fam: SERIF, weight: 500 });
      text(g, '~/code/storefront', dx + 40, dy + 192, { size: 26, fam: MONO, color: C.muted });
      const pressed = t > ok && t < ok + 0.16;
      rrect(g, dx + dw - 360, dy + dh - 96, 150, 60, 30); g.lineWidth = 2; g.strokeStyle = C.hair; g.stroke();
      text(g, 'Deny', dx + dw - 285, dy + dh - 57, { size: 24, weight: 600, align: 'center', color: C.muted });
      rrect(g, dx + dw - 190, dy + dh - 96, 150, 60, 30); g.fillStyle = pressed ? C.orangeDeep : C.orange; g.fill();
      text(g, 'Allow', dx + dw - 115, dy + dh - 57, { size: 24, weight: 600, align: 'center', color: '#fff' });
    });
    withAlpha(g, allowed, () => {
      g.beginPath(); g.arc(dx + 80, dy + 168, 30, 0, Math.PI * 2); g.fillStyle = C.greenSoft; g.fill();
      icon(g, 'check', dx + 80, dy + 168, 40, C.green);
      text(g, 'Allowed for this project', dx + 130, dy + 182, { size: 42, fam: SERIF, weight: 500 });
    });
    g.restore();
    // Cursor glides to Allow and clicks.
    const m = E.inOut(seg(t, ask + 0.5, Math.max(0.3, ok - 0.15 - ask - 0.5)));
    const tx = W / 2 + dw / 2 - 115, ty = dy + dh - 66;
    withAlpha(g, show * (1 - allowed * 0.0), () => cursorArrow(g, lerp(1560, tx, m), lerp(980, ty, m), t > ok && t < ok + 0.16));
  }
};

const AGENTS = ['Claude Code', 'Codex', 'Pi', 'Kimi Code', 'Cursor CLI', 'Grok Build', 'OpenCode', 'Antigravity CLI'];
const TOOLS = ['tmux', 'cmux', 'Herdr'];

function promptLine(cmd, caret) {
  return [{ t: '~/code/storefront ', c: C.path }, { t: '$ ', c: C.termDim }, { t: cmd, w: 500 }, ...(caret ? [{ t: '▍', c: C.orange }] : [])];
}
function launchedLines(cmd) {
  return [
    promptLine(cmd, false),
    [{ icon: 'cloud', c: C.orange }, { t: 'Fennec ', c: C.orange, w: 600 }, { t: 'running on your cloud computer', c: C.termDim }],
    [{ t: '   session ', c: C.termDim }, { t: 'cone-leaf', w: 600 }, { t: ' · connected to your Mac', c: C.termDim }],
    [],
  ];
}

SCENES.terminal = (g, t, sc) => {
  const L0 = sc.lines[0], ty = sc.cue('typeAgent'), chips = sc.cue('chips');
  headline(g, ['Nothing about how you work has to change.'], W / 2, 150, 66, t, L0.start, { align: 'center' });
  const ty0 = typed(ty.text, ty.at, ty.cps, t);
  const launched = t > ty0.enter + 0.25;
  g.save(); reveal(g, seg(t, 0, 0.7), 30);
  terminal(g, 410, 220, 1100, 540, {
    title: 'storefront — zsh', badge: seg(t, ty0.enter + 0.3, 0.5),
    body: (g2) => {
      const lines = launched ? launchedLines(ty.text) : [promptLine(ty0.s, t < ty.at || !ty0.done || caretOn(t))];
      lines.forEach((ln, i) => spans(g2, 40, 64 + i * 46, ln, 26));
      if (launched) spans(g2, 40, 64 + lines.length * 46, [{ t: '> ', c: C.orange, w: 700 }, ...(caretOn(t) ? [{ t: '▍', c: C.termText }] : [])], 26);
    },
  });
  g.restore();

  const all = [...AGENTS.map(a => [a, 'agent']), ...TOOLS.map(a => [a, 'tool'])];
  const rowY = [855, 945];
  const rows = [AGENTS, TOOLS];
  let idx = 0;
  rows.forEach((row, ri) => {
    const widths = row.map(l => measure(g, l, 26, SANS, 500) + 52);
    const gap = 18, total = widths.reduce((a, b) => a + b, 0) + gap * (row.length - 1);
    let x = W / 2 - total / 2;
    row.forEach((label, i) => {
      const p = seg(t, chips.at + idx * chips.interval, 0.45);
      chip(g, label, x + widths[i] / 2, rowY[ri], p, ri === 1 ? 'tool' : 'agent');
      x += widths[i] + gap; idx++;
    });
  });
  void all;
};

function detachBody(t, sc) {
  const L = sc.lines[0], ctrl = sc.cue('ctrlC').at;
  return (g2) => {
    const base = launchedLines('claude').slice(1, 3);
    const work = [
      [{ icon: 'dot', c: C.orange }, { t: 'Refactor ', w: 600 }, { t: 'checkout service', c: C.termDim }],
      [{ icon: 'dot', c: C.orange }, { t: 'Update   ', w: 600 }, { t: '14 tests', c: C.termDim }],
      [{ icon: 'spin', c: C.orange, time: t }, { t: 'Running  ', w: 600 }, { t: 'full test suite', c: C.termDim }],
    ];
    const n = clamp(Math.floor((Math.min(t, ctrl) + 0.6) / 0.35), 0, work.length);
    let lines = [...base, [], ...work.slice(0, n)];
    if (t > ctrl) lines.push([{ t: '^C', c: C.termDim }]);
    if (t > ctrl + 0.35) lines = [...lines, [], ...DETACH_LINES];
    if (lines.length > 9) lines = lines.slice(lines.length - 9);
    lines.forEach((ln, i) => spans(g2, 40, 64 + i * 46, ln, 26));
    void L;
  };
}

SCENES.detach = (g, t, sc) => {
  const hand = sc.cue('handoff').at;
  const m = E.inOut(seg(t, hand, 1.0));
  g.save();
  const cx0 = 960, cy0 = 490, cx1 = 520, cy1 = 560;
  g.translate(lerp(cx0, cx1, m), lerp(cy0, cy1, m));
  const s = lerp(1, 0.68, m); g.scale(s, s);
  g.globalAlpha = lerp(1, 0.88, m);
  terminal(g, -550, -270, 1100, 540, { badge: 1 - m, body: detachBody(t, sc) });
  g.restore();

  const rows = [
    { text: 'Refactor checkout service', at: hand + 0.6, done: hand + 1.6 },
    { text: 'Update 14 tests', at: hand + 0.8, done: hand + 2.6 },
    { text: 'Running full test suite', at: hand + 1.0, done: 1e9, progress: true },
  ].map(r => ({
    text: r.text, alpha: seg(t, r.at, 0.4), state: t > r.done ? 'done' : 'run',
    progress: r.progress ? clamp((t - hand - 1.0) / 9) * 0.7 + 0.05 : null,
  }));
  g.save();
  const p = seg(t, hand + 0.3, 0.9);
  g.globalAlpha = E.out(p); g.translate((1 - E.out(p)) * 80, 0);
  cloudCard(g, 1080, 370, 680, 380, t, rows, { rowH: 66 });
  g.restore();
  headline(g, [{ t: 'It keeps working.', italic: true }], 1080, 290, 76, t, hand + 0.9);
};

SCENES.away = (g, t, sc) => {
  const L = sc.lines[0], shut = sc.cue('lidClose').at, notify = sc.cue('notify').at;
  const day = clamp(t / sc.dur);
  g.fillStyle = mixColor(C.bg, C.dusk, E.inOut(day)); g.fillRect(0, 0, W, H);

  // Time-lapse sun arc.
  const acx = W / 2, acy = 1250, ar = 1050;
  g.save(); g.setLineDash([2, 16]); g.lineCap = 'round'; g.lineWidth = 3; g.strokeStyle = C.hair;
  g.beginPath(); g.arc(acx, acy, ar, Math.PI * 1.12, Math.PI * 1.88); g.stroke(); g.restore();
  const a = lerp(Math.PI * 1.2, Math.PI * 1.82, E.inOut(day));
  const sx = acx + Math.cos(a) * ar, sy = acy + Math.sin(a) * ar;
  const glow = g.createRadialGradient(sx, sy, 0, sx, sy, 90);
  glow.addColorStop(0, 'rgba(243,141,37,0.35)'); glow.addColorStop(1, 'rgba(243,141,37,0)');
  g.fillStyle = glow; g.fillRect(sx - 90, sy - 90, 180, 180);
  g.beginPath(); g.arc(sx, sy, 20, 0, Math.PI * 2); g.fillStyle = C.orange; g.fill();

  const minutes = 18 * 60 + 40 + Math.floor(95 * E.inOut(day));
  const clock = `${((Math.floor(minutes / 60) + 11) % 12) + 1}:${String(minutes % 60).padStart(2, '0')} PM`;
  text(g, clock, 120, 130, { size: 34, fam: MONO, weight: 500, color: C.muted });

  const span = L.end - L.start;
  headline(g, ['Close the lid.'], 150, 330, 72, t, L.start + 0.05);
  headline(g, ['Go for a walk.'], 150, 420, 72, t, L.start + span * 0.3);
  headline(g, [{ t: 'Check in from your phone.', italic: true, color: C.orangeDeep }], 150, 510, 72, t, L.start + span * 0.58);
  const open = 1 - E.in(seg(t, shut, 0.5));
  laptop(g, 520, 860, 0.62, open, (sg, w, h) => {
    sg.fillStyle = C.term; sg.fillRect(0, 0, w, h);
    DETACH_LINES.forEach((ln, i) => spans(sg, 28, 60 + i * 34, ln, 17));
  });

  const ph = seg(t, lerp(L.start, L.end, 0.55) - 0.4, 0.9);
  if (ph > 0) {
    g.save(); g.globalAlpha = E.out(ph); g.translate(0, (1 - E.out(ph)) * 120);
    phone(g, 1330, 560, 350, 700, (g2, x, y, w, h) => {
      const done = t > notify;
      text(g2, clock.replace(' PM', ''), x + 44, y + 50, { size: 18, weight: 600 });
      text(g2, 'Sessions', x + 28, y + 128, { size: 40, fam: SERIF, weight: 500 });
      const rows = [
        { name: 'cone-leaf', agent: 'Claude Code', state: done ? 'Done' : 'Working', p: done ? 1 : 0.55 + 0.35 * seg(t, 0, notify) },
        { name: 'quiet-river', agent: 'Codex', state: 'Idle', p: null },
      ];
      rows.forEach((r, i) => {
        const ry = y + 160 + i * 132;
        rrect(g2, x + 18, ry, w - 36, 116, 20); g2.fillStyle = '#fff'; g2.fill();
        g2.lineWidth = 1.5; g2.strokeStyle = C.hair; g2.stroke();
        logo(g2, x + 52, ry + 38, 30);
        text(g2, r.name, x + 80, ry + 38, { size: 21, weight: 600 });
        text(g2, r.agent + ' · cloud', x + 80, ry + 64, { size: 16, color: C.muted, weight: 500 });
        const col = r.state === 'Done' ? C.green : r.state === 'Working' ? C.orange : C.faint;
        g2.beginPath(); g2.arc(x + w - 110, ry + 32, 5, 0, Math.PI * 2); g2.fillStyle = col; g2.fill();
        text(g2, r.state, x + w - 98, ry + 38, { size: 16, weight: 600, color: col });
        if (r.p != null) {
          rrect(g2, x + 38, ry + 88, w - 76, 7, 3.5); g2.fillStyle = C.bg2; g2.fill();
          rrect(g2, x + 38, ry + 88, (w - 76) * r.p, 7, 3.5); g2.fillStyle = col; g2.fill();
        }
      });
      // Notification banner.
      const nb = E.out(seg(t, notify - 0.1, 0.5));
      if (nb > 0) {
        const by = lerp(y - 120, y + 64, nb);
        g2.save();
        g2.fillStyle = 'rgba(60,40,20,0.15)'; rrect(g2, x + 14, by + 8, w - 28, 96, 22); g2.fill();
        rrect(g2, x + 12, by, w - 24, 96, 22); g2.fillStyle = '#fff'; g2.fill(); g2.lineWidth = 1.5; g2.strokeStyle = C.hair; g2.stroke();
        logo(g2, x + 50, by + 48, 34);
        text(g2, 'cone-leaf is done', x + 82, by + 42, { size: 19, weight: 700 });
        text(g2, '342 tests passing', x + 82, by + 70, { size: 17, color: C.muted, weight: 500 });
        g2.restore();
      }
    });
    g.restore();
  }
};

SCENES.return = (g, t, sc) => {
  const L = sc.lines[0], op = sc.cue('lidOpen').at, ty = sc.cue('typeAttach'), restore = sc.cue('restore').at;
  const open = E.out(seg(t, op, 0.9));
  const tt = typed(ty.text, ty.at, ty.cps, t);
  laptop(g, W / 2, 850, 1.08, open, (sg, w, h) => {
    sg.fillStyle = C.term; sg.fillRect(0, 0, w, h);
    if (t < restore) {
      const lines = [...DETACH_LINES, [], [{ t: '$ ', c: C.termDim }, { t: tt.s, w: 500 }, ...(t < ty.at || !tt.done || caretOn(t) ? [{ t: '▍', c: C.orange }] : [])]];
      lines.forEach((ln, i) => spans(sg, 28, 54 + i * 32, ln, 17));
      return;
    }
    sg.fillStyle = C.termBar; sg.fillRect(0, 0, w, 36);
    spans(sg, 22, 24, [{ icon: 'cloud', c: C.orange }, { t: 'cone-leaf', w: 600 }, { t: '  ·  Claude Code  ·  cloud computer', c: C.termDim }], 15, SANS);
    const out = [
      [{ icon: 'check', c: C.green }, { t: 'Refactored the checkout service' }],
      [{ icon: 'check', c: C.green }, { t: 'Updated 14 tests' }],
      [{ icon: 'check', c: C.green }, { t: '342 tests passing' }],
      [],
      [{ t: 'Ready for your review.', w: 600 }],
    ];
    out.forEach((ln, i) => {
      const a = seg(t, restore + 0.15 + i * 0.22, 0.3);
      if (a <= 0) return;
      sg.save(); sg.globalAlpha = a; spans(sg, 28, 86 + i * 36, ln, 19); sg.restore();
    });
    if (t > restore + 1.4) spans(sg, 28, 86 + out.length * 36, [{ t: '> ', c: C.orange, w: 700 }, ...(caretOn(t) ? [{ t: '▍' }] : [])], 19);
  });
  g.save(); reveal(g, seg(t, restore + 0.5, 0.9));
  text(g, 'Right where you left off.', W / 2, 150, { size: 72, fam: SERIF, style: 'italic', align: 'center' });
  g.restore();
  void L;
};

SCENES.end = (g, t, sc) => {
  const L = sc.lines[0], at = sc.cue('logo').at;
  const span = L.end - L.start;
  const p = seg(t, at, 1.0);
  g.save(); g.globalAlpha = E.out(seg(t, at, 0.5));
  g.translate(W / 2, 360); const s = lerp(0.8, 1, E.back(p)); g.scale(s, s);
  logo(g, 0, 0, 190, { earL: Math.sin(clamp((t - at - 1.2) / 0.35) * Math.PI) * 0.08 });
  g.restore();
  g.save(); reveal(g, seg(t, L.start - 0.1, 0.8), 16);
  text(g, 'Fennec', W / 2, 610, { size: 128, fam: SERIF, weight: 500, align: 'center' });
  g.restore();
  const a1 = seg(t, L.start + span * 0.28, 0.7), a2 = seg(t, L.start + span * 0.62, 0.7);
  const s1 = 'Keep your agent. ', s2 = 'Leave the laptop.';
  const w1 = measure(g, s1, 54, SERIF, 400, 'italic'), w2 = measure(g, s2, 54, SERIF, 400, 'italic');
  const x0 = W / 2 - (w1 + w2) / 2;
  g.save(); reveal(g, a1, 12); text(g, s1, x0, 720, { size: 54, fam: SERIF, style: 'italic', color: C.muted }); g.restore();
  g.save(); reveal(g, a2, 12); text(g, s2, x0 + w1, 720, { size: 54, fam: SERIF, style: 'italic', color: C.orangeDeep }); g.restore();
  g.save(); reveal(g, seg(t, L.end + 0.3, 0.8), 8);
  text(g, 'runfennec.com', W / 2, 900, { size: 30, weight: 500, align: 'center', color: C.muted, ls: 1 });
  g.restore();
};

// ---------- compositor ----------
let TL = null;
const canvas = document.getElementById('c');
const ctx = canvas.getContext('2d');
const layer = document.createElement('canvas');
layer.width = W; layer.height = H;
const lctx = layer.getContext('2d');

function sceneView(sc) {
  const cues = Object.fromEntries((sc.cues || []).map(c => [c.name, c]));
  return { ...sc, cue: n => { if (!cues[n]) throw new Error(`scene ${sc.id}: no cue ${n}`); return cues[n]; } };
}

function paintScene(g, sc, t) {
  g.save();
  g.fillStyle = C.bg; g.fillRect(0, 0, W, H);
  const fn = SCENES[sc.id];
  if (fn) fn(g, t, sc);
  g.restore();
}

function drawAt(T) {
  const scenes = TL.views;
  let i = scenes.findIndex(s => T < s.start + s.dur);
  if (i < 0) i = scenes.length - 1;
  const sc = scenes[i];
  const half = TL.crossfade / 2;
  paintScene(ctx, sc, Math.min(T - sc.start, sc.dur));
  // Crossfade around each boundary.
  const prev = scenes[i - 1], next = scenes[i + 1];
  if (prev && T - sc.start < half) {
    const a = 0.5 - (T - sc.start) / (2 * half);
    paintScene(lctx, prev, prev.dur);
    ctx.save(); ctx.globalAlpha = E.inOut(clamp(a)); ctx.drawImage(layer, 0, 0); ctx.restore();
  } else if (next && sc.start + sc.dur - T < half) {
    const a = 0.5 - (sc.start + sc.dur - T) / (2 * half);
    paintScene(lctx, next, 0);
    ctx.save(); ctx.globalAlpha = E.inOut(clamp(a)); ctx.drawImage(layer, 0, 0); ctx.restore();
  }
  // Fade in from and out to the background.
  const edge = Math.min(clamp(T / 0.6), clamp((TL.duration - T) / 0.8));
  if (edge < 1) { ctx.fillStyle = C.bg; ctx.globalAlpha = 1 - E.inOut(edge); ctx.fillRect(0, 0, W, H); ctx.globalAlpha = 1; }
  if (TL.captions) drawCaption(T);
}

function drawCaption(T) {
  for (const sc of TL.views) for (const ln of sc.lines) {
    const a = sc.start + ln.start, b = sc.start + ln.end;
    if (T < a - 0.05 || T > b + 0.3) continue;
    const w = measure(ctx, ln.text, 30, SANS, 500) + 48;
    ctx.save(); ctx.globalAlpha = 0.9;
    rrect(ctx, W / 2 - w / 2, 990, w, 54, 12); ctx.fillStyle = 'rgba(34,30,26,0.82)'; ctx.fill();
    text(ctx, ln.text, W / 2, 1027, { size: 30, weight: 500, align: 'center', color: '#F6F0E6' });
    ctx.restore();
  }
}

window.renderFrame = i => { drawAt(i / TL.fps); return canvas.toDataURL('image/png'); };

async function init() {
  try {
    const params = new URLSearchParams(location.search);
    TL = await (await fetch('timeline.json')).json();
    TL.views = TL.scenes.map(sceneView);
    await Promise.all([
      document.fonts.load(`400 80px ${SERIF}`), document.fonts.load(`italic 400 80px ${SERIF}`),
      document.fonts.load(`500 30px ${SANS}`), document.fonts.load(`600 30px ${SANS}`),
      document.fonts.load(`400 24px ${MONO}`), document.fonts.load(`600 24px ${MONO}`),
    ]);
    if (params.has('render')) {
      document.body.classList.add('render');
    } else {
      // Live preview in a browser: plays in real time, click to restart.
      if (params.has('captions')) TL.captions = true;
      let t0 = performance.now();
      canvas.addEventListener('click', () => { t0 = performance.now(); });
      const tick = () => { drawAt(((performance.now() - t0) / 1000) % TL.duration); requestAnimationFrame(tick); };
      tick();
    }
    window.trailerReady = true;
  } catch (e) {
    window.trailerError = String(e && e.stack || e);
    throw e;
  }
}
init();
