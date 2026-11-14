const fs = require('node:fs'), vm = require('node:vm'), assert = require('node:assert/strict');
const source = fs.readFileSync('native/macos/UI/browser.js', 'utf8');

// Minimal DOM and xterm stand-ins: enough to run browser.js and drive a paste.
const nodes = {};
const node = id => (nodes[id] = nodes[id] || { id, hidden: true, textContent: '', disabled: false });
const listeners = {};
const textarea = { addEventListener: (type, fn) => { listeners[type] = fn; } };
let dataHandler = null;
class Terminal {
  constructor() { this.cols = 80; this.rows = 24; this.textarea = textarea; }
  loadAddon() {} open() {} reset() {} focus() {} write(_d, done) { done && done(); }
  onData(fn) { dataHandler = fn; } onBinary() {} onResize() {}
}
const sockets = [];
class WebSocket {
  constructor() { this.readyState = 1; this.sent = []; sockets.push(this); }
  send(frame) { this.sent.push(JSON.parse(frame)); }
  close() {}
}
WebSocket.OPEN = 1;
const timers = [];
const context = vm.createContext({
  location: { hash: '#token', host: '127.0.0.1:1' },
  document: { querySelector: s => node(s.replace('#', '')), fonts: { ready: Promise.resolve() }, head: { append() {} }, createElement: () => ({}) },
  window: { addEventListener() {} },
  Terminal, FitAddon: { FitAddon: class { fit() {} } }, WebSocket,
  ResizeObserver: class { observe() {} },
  requestAnimationFrame: fn => fn(),
  setTimeout: fn => { timers.push(fn); return timers.length; }, clearTimeout() {},
  TextEncoder, btoa: s => Buffer.from(s, 'binary').toString('base64'), atob: s => Buffer.from(s, 'base64').toString('binary'),
  Uint8Array, JSON, Math, String, Promise
});
vm.runInContext(source, context);
const socket = sockets[0];
const paste = text => listeners.paste({ clipboardData: { getData: () => text } });

// 1. Before the session is attached a paste is not confirmed (nothing is sent).
paste('abc');
assert.equal(nodes.pasted.hidden, true, 'no confirmation before the receipt');

// 2. Attached: a paste is confirmed with its length and the next step.
socket.onmessage({ data: JSON.stringify({ type: 'receipt' }) });
paste('x7Duh2HlFBehZXz7RoOObNsbhl3ZPsPqrxA72NVuUp2gMY0n#zvmnNs');
assert.equal(nodes.pasted.hidden, false);
assert.match(nodes.pasted.textContent, /^Pasted 55 characters\. Press Return to send\.$/);

// 3. The pasted text still goes to the session, and the notice hides again.
dataHandler('code');
assert.ok(socket.sent.some(frame => frame.type === 'input'), 'input forwarded');
timers.forEach(fn => fn());
assert.equal(nodes.pasted.hidden, true, 'notice hides after a moment');

console.log('PASS browser terminal confirms pastes that the program may not show');
