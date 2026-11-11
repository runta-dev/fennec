const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const source=fs.readFileSync('native/macos/UI/panel.js','utf8');
const html=fs.readFileSync('native/macos/UI/index.html','utf8');
assert.equal((html.match(/class="[^"]*\blogin\b[^"]*"/g)||[]).length,1,'one login entry only');
const elements=new Map();
function element(key){if(!elements.has(key))elements.set(key,{hidden:false,disabled:false,textContent:'',classList:{add(){},remove(){},toggle(){}},removeAttribute(){},replaceChildren(){}});return elements.get(key);}
const actions=[];
const context=vm.createContext({window:{renderAgents(){},renderBilling(){}},authorized:false,sessionListConnecting:true,visibleSessionCount:0,detectedAgents:[],routeResults:new Map(),takeoverAgents:new Set(),activeTakeoverAgents:new Set(),takeoverChanging:false,gatewayModels:[],nativeModelStates:new Map(),nativeModelRequests:new Map(),modelSaving:false,userMenu:{hidden:false},tab:x=>actions.push('tab:'+x),send:x=>actions.push(x),document:{createElement:()=>({textContent:"",classList:{toggle(){}}}),querySelector:element,querySelectorAll:()=>[element('.header-login')]}});
for(const name of ['renderLogin','renderAuthorized','renderSignedOut']){
 const start=source.indexOf('window.'+name+' =');const end=source.indexOf('\n};',start)+3;
 vm.runInContext(source.slice(start,end),context);
}
context.window.renderLogin('',false);
assert.equal(element('#notice').hidden,true);
context.window.renderLogin('Connecting',true);
assert.equal(element('#notice').hidden,false);
assert.equal(element('.header-login').disabled,true);
assert.deepEqual(actions,[],'login feedback must not switch tabs');
context.window.renderAuthorized();
assert.equal(element('#session-signin').hidden,true);
assert.equal(element('.header-login').disabled,false);
context.window.renderSignedOut();
assert.equal(element('#session-signin').hidden,false);
assert.equal(element('#runtime-panel').hidden,true);
assert.equal(element('.header-login').disabled,false);
console.log('PASS single login entry, normal first-launch state and logout transitions');
