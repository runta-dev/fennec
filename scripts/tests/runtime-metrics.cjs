const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const uiRoot = path.join(__dirname, '..', '..', 'native', 'macos', 'UI');
const panelJS = fs.readFileSync(path.join(uiRoot, 'panel.js'), 'utf8');
const panelCSS = fs.readFileSync(path.join(uiRoot, 'panel.css'), 'utf8');

// Extract sparklinePaths and verify auto-zoom on small fluctuations + panorama scale on large swings.
const match = panelJS.match(/function sparklinePaths\([\s\S]*?\n\}/);
assert.ok(match, 'sparklinePaths should exist in panel.js');

const ctx = vm.createContext({ METRIC_HISTORY_LEN: 24, Math, Number });
vm.runInContext(match[0], ctx);

function parseYs(linePath) {
  return linePath
    .replace(/^M/, '')
    .split(' L')
    .map(pair => Number(pair.split(',')[1]));
}

// 1. Idle micro-fluctuation (0.0% .. 0.8% CPU) should auto-zoom to a clearly visible waveform (>= 8px height in 24px chart).
const idleCPU = ctx.sparklinePaths([0.2, 0.0, 0.4, 0.1, 0.8, 0.3], { fullMax: 100, minSpan: 0.35 });
const idleCPUYs = parseYs(idleCPU.line);
const idleCPUSpanPx = Math.max(...idleCPUYs) - Math.min(...idleCPUYs);
assert.ok(idleCPUSpanPx >= 8, `expected idle CPU micro-fluctuation to span >= 8px vertically, got ${idleCPUSpanPx.toFixed(1)}px`);

// 2. Sub-MiB memory breathing around 379 MiB should also auto-zoom cleanly instead of flattening near y=22.
const idleMem = ctx.sparklinePaths([379.08, 379.15, 379.11, 379.29, 379.19, 379.34], { fullMax: 4096, minSpan: 0.25 });
const idleMemYs = parseYs(idleMem.line);
const idleMemSpanPx = Math.max(...idleMemYs) - Math.min(...idleMemYs);
assert.ok(idleMemSpanPx >= 8, `expected sub-MiB memory fluctuation to span >= 8px vertically, got ${idleMemSpanPx.toFixed(1)}px`);

// 3. Large workload swing (2% .. 85% CPU) should scale out to full panorama [0, 100].
const burstCPU = ctx.sparklinePaths([2, 15, 48, 85, 62, 78], { fullMax: 100, minSpan: 0.35 });
assert.equal(burstCPU.lo, 0, 'large workload swing should anchor bottom at 0');
assert.equal(burstCPU.hi, 100, 'large workload swing should expand top to full 100 panorama');

// 4. UI copy, icons, and neutral spec badge invariants.
assert.ok(panelJS.includes("vcpus + ' vCPU · '"), 'runtime spec should separate vCPU and memory with middle dot');
assert.ok(!panelJS.includes("vcpus + ' vCPU / '"), 'runtime spec should not use slash separator');
assert.ok(panelJS.includes('formatMemoryMiB(exactUsedMiB)'), 'memory value should format sub-GiB in MB and >=1 GiB in GB');
assert.ok(!panelJS.includes("M7.5 17.5h5M10 14.5v3"), 'cloud icon should not have server stem tail underneath');
assert.ok(!panelJS.includes("M6 16V4m0 0L3 7m3-3 3 3m5-3v12"), 'network icon should not duplicate up/down arrows');
assert.ok(!panelCSS.includes('.runtime-metrics-spec{display:inline-flex;align-items:center;height:20px;padding:0 8px;border:1px solid #f38d2540'), 'runtime spec should not use accent yellow badge');
