const fs = require('node:fs'), vm = require('node:vm'), assert = require('node:assert/strict');
const source = fs.readFileSync('native/macos/UI/browser.js', 'utf8');

// Minimal DOM and xterm stand-ins: enough to run browser.js and drive a paste.
const nodes = {};
const node = id => (nodes[id] = nodes[id] || { id, hidden: true, textContent: '', disabled: false });
const listeners = {};
const textarea = { addEventListener: (type, fn, capture) => { listeners[type+(capture ? ':capture' : '')] = fn; } };
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

(async()=>{
  socket.onmessage({data:JSON.stringify({type:'receipt',capabilities:['image_paste_v1']})});
  socket.sent=[];
  let complete;
  const payload=new Uint8Array(100000).fill(42);
  const file={size:payload.length,arrayBuffer:()=>new Promise(resolve=>{complete=resolve;})};
  let prevented=false;
  const uploading=listeners['paste:capture']({clipboardData:{items:[{type:'image/png',getAsFile:()=>file}]},preventDefault(){prevented=true;},stopImmediatePropagation(){}});
  dataHandler('after\r');
  assert.equal(socket.sent.length,0,'typing must wait behind the asynchronous clipboard read');
  complete(payload.buffer);await uploading;
  assert.equal(prevented,true);
  assert.equal(socket.sent.reduce((n,f)=>n+Buffer.from(f.data||'','base64').length,0),65536,'image respects credit window');
  socket.onmessage({data:JSON.stringify({type:'input_credit',bytes:65536})});
  const images=socket.sent.filter(f=>f.type==='image');
  assert.equal(images[0].bytes,payload.length);
  assert.ok(images.slice(1).every(f=>f.bytes===0));
  assert.deepEqual(Buffer.concat(images.map(f=>Buffer.from(f.data,'base64'))),Buffer.from(payload));
  assert.equal(socket.sent.at(-1).type,'input');
  assert.equal(Buffer.from(socket.sent.at(-1).data,'base64').toString(),'after\r');

  // A clipboard read that resolves after reconnect must not enter the new session.
  const stale=listeners['paste:capture']({clipboardData:{items:[{type:'image/png',getAsFile:()=>file}]},preventDefault(){},stopImmediatePropagation(){}});
  nodes.reconnect.onclick();const replacement=sockets.at(-1);
  replacement.onmessage({data:JSON.stringify({type:'receipt',capabilities:['image_paste_v1']})});
  complete(payload.buffer);await stale;
  assert.equal(replacement.sent.length,0,'stale clipboard image crossed sessions');
  console.log('PASS browser terminal text/image paste, ordering, credits, and reconnect isolation');
})().catch(error=>{console.error(error);process.exitCode=1;});
