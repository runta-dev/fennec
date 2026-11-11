const fs=require('node:fs');
const vm=require('node:vm');
const assert=require('node:assert/strict');
const source=fs.readFileSync('native/macos/UI/panel.js','utf8');
const start=source.indexOf('let lastSessionTerminals=');
assert.ok(start>=0);
const end=source.indexOf('\n};',source.indexOf('window.renderHerdrSessions =',start))+3;
let rendered;
const context=vm.createContext({window:{renderRuntimes(rows){rendered=rows;}}});
vm.runInContext(source.slice(start,end),context);
context.window.renderHerdrSessions([
 {address:'t1',state:'live',status:'working'},
 {address:'t2',state:'live',status:{state:'blocked'}},
 {address:'t3',state:'live',status:'done'},
 {address:'t4',state:'finished',status:'working'},
 {address:'t5',state:'live',status:'future-state'},
 {address:'t6',state:'live',status:null}
]);
assert.deepEqual(Array.from(rendered,x=>x.displayStatus),["[[i18n:ui.working]]","[[i18n:ui.needs_input]]","[[i18n:ui.idle]]","[[i18n:ui.idle]]","[[i18n:ui.idle]]"]);
assert.equal(rendered[2].status,'done');
assert.equal(rendered[3].status,'future-state');
console.log('PASS Herdr reported state, process exit, unknown and absent status remain distinct');
assert.deepEqual(Array.from(rendered,x=>x.group),['working','blocked','idle','idle','idle']);
context.window.renderHerdrSessions([
 {address:'t7',agent:'claude',name:'project-name',title:'  Fix login redirect  ',state:'live',status:'working',labels:{'fennec.session':'a'.repeat(32)}},
 {address:'t8',agent:'codex',title:'',state:'live',status:'idle'},
 {address:'t9',name:'Named session',title:'   ',state:'finished',status:'done',labels:{'fennec.session':'b'.repeat(32)}}
]);
assert.equal(rendered[0].name,'Fix login redirect');
assert.equal(rendered[1].name,"[[i18n:ui.untitled_session]]");
assert.equal(rendered.length,2);
assert.equal(rendered[0].canStop,true);
console.log('PASS session titles override project names; missing titles never expose agent or terminal IDs');
context.window.renderHerdrSessions([
 {address:'t20',state:'live',title:'查看本地仓库 | '+ 'a'.repeat(32),labels:{'fennec.session':'a'.repeat(32)}},
 {address:'t21',state:'live',title:'b'.repeat(32),name:'c'.repeat(32)},
 {address:'t22',state:'live',title:'Review | 01a1c322-3cdd-71f1-94e7-851e2468e6ec'},
 {address:'t23',state:'live',title:'Review | customer feedback'}
]);
assert.deepEqual(Array.from(rendered,row=>row.name),['查看本地仓库',"[[i18n:ui.untitled_session]]",'Review','Review | customer feedback']);
assert.equal(rendered[0].sessionID,'a'.repeat(32));
console.log('PASS display titles omit UUID suffixes while action identities and human title content remain intact');
context.window.renderHerdrSessions([
 {address:'t12',state:'live',title:'查看本地仓库 | 6629244c4e1d9427135cf...'},
 {address:'t15',state:'live',title:'说明当前运行环境 | 854d41450568901dee5fa...'},
 {address:'t16',state:'live',title:'6629244c4e1d9427135cf…'}
]);
assert.deepEqual(Array.from(rendered,row=>row.name),['查看本地仓库','说明当前运行环境',"[[i18n:ui.untitled_session]]"]);
const treeStart=source.indexOf('function orderSessionTree(');
const treeEnd=source.indexOf('\nwindow.renderRuntimes =',treeStart);
vm.runInContext(source.slice(treeStart,treeEnd),context);
const tree=context.orderSessionTree([{address:'child',parent:'parent'},{address:'parent'},{address:'orphan',parent:'missing'},{address:'cycle-a',parent:'cycle-b'},{address:'cycle-b',parent:'cycle-a'}]);
assert.deepEqual(Array.from(tree,x=>x.address),['parent','child','orphan','cycle-a','cycle-b']);
assert.equal(tree[1].depth,1);
assert.equal(tree[2].depth,0);
assert.equal(new Set(tree.map(x=>x.address)).size,5);
console.log('PASS parent/child ordering retains orphan and cyclic session metadata');

const collapsed=context.orderSessionTree([{address:'child',parent:'parent'},{address:'parent'},{address:'peer'}],new Set(['parent']));
assert.deepEqual(Array.from(collapsed,x=>x.address),['parent','peer']);
assert.equal(collapsed[0].hasChildren,true);

const refreshStart=source.indexOf('window.renderSessionRefreshError =');
const refreshEnd=source.indexOf('\n};',refreshStart)+3;
const note={replaceChildren(){},hidden:true,className:'',textContent:''};
const refreshContext=vm.createContext({window:{},document:{querySelector(selector){assert.equal(selector,'#runtime-message');return note;}}});
vm.runInContext(source.slice(refreshStart,refreshEnd),refreshContext);
refreshContext.window.renderSessionRefreshError();
assert.equal(note.hidden,false);
assert.equal(note.textContent,"[[i18n:ui.session_status_unavailable_refresh_to_retry]]");
console.log('PASS refresh errors preserve existing session rows');

context.window.renderHerdrSessions([{address:'t10',state:'finished',exit_code:7},{address:'t11',state:'finished',exit_code:0}]);
assert.equal(rendered.length,0);
context.window.renderHerdrSessions([{address:'t12',state:'live',status:'error',labels:{'fennec.session':'d'.repeat(32)}}]);
assert.equal(rendered[0].group,'error');assert.equal(rendered[0].displayStatus,"[[i18n:ui.errored]]");assert.equal(rendered[0].canStop,true);
console.log('PASS only live processes appear; exited processes are excluded and live errors remain actionable');
const stoppedID='a'.repeat(32);
const beforeStop=[{address:'tstop',state:'live',labels:{'fennec.session':stoppedID}},{address:'tkeep',state:'live'}];
context.window.renderHerdrSessions(beforeStop);
context.window.renderSessionStopped(stoppedID);
assert.deepEqual(Array.from(rendered,x=>x.address),['tkeep']);
context.window.renderHerdrSessions(beforeStop);
assert.deepEqual(Array.from(rendered,x=>x.address),['tkeep']);
console.log('PASS confirmed stop updates immediately and stale refresh cannot resurrect the row');
