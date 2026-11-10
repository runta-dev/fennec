const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const sent=[],terms=[],handlers={};let next=0;
class Terminal {constructor(){this.options={};this.cols=80;this.rows=24;this.writes=[];terms.push(this);}loadAddon(){}open(){}focus(){}dispose(){this.disposed=true;}onData(fn){this.input=fn;}onResize(fn){this.resize=fn;}write(data,done){this.writes.push(Buffer.from(data).toString());this.done=done;}}
const context=vm.createContext({window:{addEventListener:(name,fn)=>handlers[name]=fn,webkit:{messageHandlers:{fennec:{postMessage:v=>sent.push(JSON.parse(v))}}}},document:{querySelector(){return {textContent:''};}},Terminal,FitAddon:{FitAddon:class{fit(){}}},ResizeObserver:class{observe(){}disconnect(){}},crypto:{randomUUID:()=>String(++next)},Uint8Array,atob:v=>Buffer.from(v,'base64').toString('binary')});
vm.runInContext(fs.readFileSync('native/macos/UI/terminal.js','utf8'),context);
context.window.openSessionTerminal({}, {address:'t1',session_id:'one'});
terms[0].input('\u001b[A');assert.equal(sent.at(-1).data,'\u001b[A');
context.window.writeSessionTerminal('1',Buffer.from('\u001b[31m红色').toString('base64'));
assert.equal(terms[0].writes[0],'\u001b[31m红色');
context.window.openSessionTerminal({}, {address:'t2',session_id:'two'});
const count=sent.length;terms[0].input('stale');terms[0].done();
context.window.writeSessionTerminal('1','eA==');assert.equal(sent.length,count);assert.equal(terms[1].writes.length,0);
terms[1].resize({cols:100,rows:30});assert.equal(sent.at(-1).cols,100);
context.window.endSessionTerminal('2');
const afterEnd=sent.length;terms[1].input('ignored');assert.equal(sent.length,afterEnd);assert.equal(terms[1].options.disableStdin,true);
context.window.closeSessionTerminal();assert.equal(sent.at(-1).type,'terminal-close');assert.equal(terms[1].disposed,true);
console.log('PASS terminal bridge preserves ANSI/UTF-8 and fences stale input/output/credits');

context.window.openSessionTerminal({}, {address:'t3',session_id:'three'});
handlers.pagehide();
const afterUnload=sent.length;terms[2].input('late');assert.equal(sent.length,afterUnload);assert.equal(sent.at(-1).type,'terminal-close');

context.Terminal=class {constructor(){throw new TypeError('synthetic renderer error');}};
assert.equal(context.window.openSessionTerminal({}, {address:'t4',session_id:'four'}),false);
assert.ok(sent.some(event=>event.type==='terminal-error' && event.stage==='renderer-init' && event.kind==='TypeError'));
assert.ok(!sent.some(event=>JSON.stringify(event).includes('synthetic renderer error')));
