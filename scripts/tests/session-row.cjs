const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const source=fs.readFileSync('native/macos/UI/panel.js','utf8');
const start=source.indexOf('    if(row.sessionID){');
const end=source.indexOf('    section.append(item);',start);
function element(){return {children:[],dataset:{},attributes:{},classList:{toggle(){}},querySelector(selector){return this.children.find(node=>node.className===selector.slice(1));},setAttribute(key,value){this.attributes[key]=value;},append(...nodes){this.children.push(...nodes);}};}
for(const canStop of [true,false]){
 const events=[],menus=[],item=element();
 const row={sessionID:'a'.repeat(32),address:'t1',name:'Fix login',canStop};
 const context=vm.createContext({item,row,document:{createElement:element},send:value=>events.push(value),openSessionActions:(trigger,session)=>menus.push({trigger,session})});
 vm.runInContext(source.slice(source.indexOf("const sessionCopyFeedback ="),source.indexOf("window.renderRuntimes =")),context);
 vm.runInContext(source.slice(start,end),context);
 assert.equal(item.onclick,undefined);
 assert.equal(item.onkeydown,undefined);
 assert.equal(item.attributes.role,undefined);
 const [copy,browser,more]=item.children[0].children;
 assert.equal(item.children[0].children.length,canStop?3:2);
 copy.onclick();
 assert.equal(events[0],'copy-attach:'+JSON.stringify({address:'t1',session_id:row.sessionID}));
 browser.onclick();
 assert.equal(events[1],'browser-attach:'+JSON.stringify({address:'t1',session_id:row.sessionID}));
 if(canStop){more.onclick();assert.equal(menus.length,1);assert.equal(menus[0].session,row);assert.equal(more.attributes['aria-haspopup'],'menu');}
 else assert.equal(more,undefined);
 assert.equal(events.length,2);
 assert.ok(events.every(value=>!value.startsWith('read-session:')));
}
console.log('PASS session rows expose copy and kill without detail navigation; finished sessions cannot be killed');
