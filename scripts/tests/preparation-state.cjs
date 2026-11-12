const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const source=fs.readFileSync('native/macos/UI/panel.js','utf8');
const start=source.indexOf('window.renderPreparation =');
const end=source.indexOf('\n};',start)+3;
function element(tag){return {tag,children:[],attrs:{},classes:new Set(),classList:{add(name){this.owner.classes.add(name);}},append(...nodes){this.children.push(...nodes);},replaceChildren(){this.children=[];},setAttribute(k,v){this.attrs[k]=v;}};}
function node(tag){const el=element(tag);el.classList.owner=el;return el;}
const panel=node('panel'),messages=[];
const runtimePanel=node('runtime-panel');
const runtimeMessage=node('runtime-message');
const context=vm.createContext({window:{},authorized:true,visibleSessionCount:0,sessionListConnecting:true,routeResults:new Map(),document:{querySelector:selector=>selector==='#runtime-panel'?runtimePanel:selector==='#runtime-message'?runtimeMessage:panel,createElement:node},send:x=>messages.push(x)});
vm.runInContext(source.slice(start,end),context);
for(const stage of ['installing_codex','connecting','configuring_routes']){
 context.window.renderPreparation({stage});
 assert.equal(panel.attrs['aria-busy'],'true');
 assert.ok(panel.children[0].children[1].classes.has('working'));
 assert.equal(panel.children.filter(x=>x.tag==='button').length,0);
}
for(const stage of ['failed']){
 runtimeMessage.className='runtime-note';
 runtimeMessage.hidden=false;
 runtimeMessage.children.push(node('span'));
 context.window.renderPreparation({stage});
 assert.equal(panel.attrs['aria-busy'],'false');
 assert.equal(runtimePanel.hidden,true,'failed setup with zero sessions must not stack an empty session card');
 assert.equal(runtimeMessage.hidden,true,'preparation banner suppresses duplicate session refresh error notes');
 assert.ok(!panel.children[0].children[1].classes.has('working'));
 const button=panel.children.find(x=>x.tag==='button');assert.ok(button);button.onclick();
 assert.equal(messages.pop(),stage==='credentials_required'?'login':'retry-runtime');
}
context.window.renderPreparation({stage:'offline',status:'network_offline'});
assert.equal(panel.hidden,false);
assert.equal(panel.attrs['aria-busy'],'false');
assert.equal(runtimePanel.hidden,true,'offline state with zero sessions must not show duplicate session card');
assert.equal(panel.children.filter(x=>x.tag==='button').length,0,'offline state recovers automatically without manual retry');
assert.equal(panel.children.filter(x=>x.tag==='p').length,1,'offline state explains automatic reconnection');
console.log('PASS preparation waits expose action; active work retains spinner');

for (const stage of ['waiting_credentials','credentials_required']) { context.window.renderPreparation({stage}); assert.equal(panel.hidden,true); assert.equal(panel.children.length,0); }

for(const status of ['native_credentials_need_attention','model_routes_need_attention']){
 context.routeResults.clear();
 context.window.renderPreparation({stage:'agents_installed',status});
 assert.equal(panel.hidden,true,'agent results arriving later must not leave a global credential error');
 assert.equal(panel.children.length,0,'no ineffective global retry for an agent sign-in');
}

context.routeResults.set('claude',{status:'configured'});
context.routeResults.set('opencode',{status:'installation_failed'});
context.window.renderPreparation({stage:'agents_installed',status:'model_routes_need_attention'});
assert.equal(panel.hidden,true,'one failed agent must not show a global setup blocker');
context.window.renderPreparation({stage:'connecting'});
assert.equal(runtimePanel.hidden,true,'connecting must not show an empty session card');
context.visibleSessionCount=2;
context.window.renderPreparation({stage:'connecting'});
assert.equal(runtimePanel.hidden,false,'existing sessions remain visible while connecting');
context.visibleSessionCount=0;
context.window.renderPreparation({stage:'agents_installed'});
assert.equal(runtimePanel.hidden,false,'completed preparation may show the empty state');

context.window.renderPreparation({stage:'connecting'});
context.window.renderPreparation({stage:'disabled'});
assert.equal(panel.hidden,false);
assert.equal(runtimePanel.hidden,true,'intentional cloud deletion must not expose stale sessions');
assert.equal(panel.attrs['aria-busy'],'false','deleted cloud is not still preparing');
assert.equal(panel.children.length,1);
assert.equal(panel.children.filter(x=>x.tag==='button').length,0,'disabled cloud does not expose deletion/management UI');
console.log('PASS disabled cloud state renders cleanly without failure, loading, or deletion UI');
