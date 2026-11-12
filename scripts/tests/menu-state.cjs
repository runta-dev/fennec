// Run the real self-drawn menu handler with a minimal DOM boundary.
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
function run(optimistic) {
 const buttons=[];
 function element(tag){return {tag,dataset:{},style:{},children:[],setAttribute(){},append(...nodes){this.children.push(...nodes);if(tag==='div')for(const node of nodes)if(node.tag==='button')buttons.push(node)},remove(){},focus(){},contains(){return false},querySelector(){return {focus(){}}},getBoundingClientRect(){return {width:180,height:32}}};}
 const original={textContent:'old/model'};
 const trigger={setAttribute(){},querySelector(){return original},getBoundingClientRect(){return {right:200,bottom:40,top:10,width:180}},focus(){}};
 let sent;
 const option={value:'new/model',textContent:'new/model',selected:true};
 const select={options:[option],selectedOptions:[option],labels:[],dispatchEvent(){sent=this.value}};
 const document={querySelector(){return {hidden:false}},querySelectorAll(){return []},createElement:element,body:{append(){}},addEventListener(){}};
 const context=vm.createContext({document,Event:class{},innerWidth:440,innerHeight:390});
 vm.runInContext(fs.readFileSync('native/macos/UI/menus.js','utf8'),context);
 context.select=select;context.trigger=trigger;context.optimistic=optimistic;
 vm.runInContext('openChoiceMenu(select,trigger,{optimistic})',context);
 buttons[0].onclick();
 assert.equal(sent,'new/model');
 assert.equal(original.textContent,optimistic?'new/model':'old/model');
}
run(true);run(false);

function permissionMenu(value) {
  const options = [
    {value:'ask',textContent:'Ask for approval',dataset:{iconSvg:'<svg></svg>',description:'Always ask'}},
    {value:'auto',textContent:'Approve for me',dataset:{iconSvg:'<svg></svg>',description:'Only ask when unsafe'}},
    {value:'full',textContent:'Full access',dataset:{iconSvg:'<svg></svg>',description:'Unrestricted access'}}
  ];
  const selected = options.find(option => option.value === value);
  selected.selected = true;
  const created=[];
 const document={querySelector(){return {hidden:false}},querySelectorAll(){return [select]},createElement(tag){const node={tag,dataset:{},full:false,style:{},children:[],setAttribute(){},append(...nodes){node.children.push(...nodes)},remove(){},focus(){},contains(){return false},querySelector(){return {focus(){},textContent:''}},getBoundingClientRect(){return {width:272,height:150}},prepend(node){node.parent=node},classList:{toggle(name,force){if(name==='is-full')node.full=Boolean(force)},contains(){return node.full}}};created.push(node);return node},body:{append(){}},addEventListener(){}};
 const select={id:'permission-mode',hidden:false,value,labels:[],options,selectedOptions:[selected],dispatchEvent(){},after(){},addEventListener(name,fn){this.change=fn}};
  const context=vm.createContext({document,Event:class{},innerWidth:440,innerHeight:500});
  vm.runInContext(fs.readFileSync('native/macos/UI/menus.js','utf8'),context);
  context.select=select;context.trigger=created[0];
  vm.runInContext('openChoiceMenu(select,trigger,{optimistic:false})',context);
  return {trigger:created[0],created,select};
}

const auto=permissionMenu('auto');
assert.equal(Boolean(auto.trigger.full),false);
const full=permissionMenu('auto');
assert.equal(Boolean(full.trigger.full),false);
full.select.value='full';
full.select.change();
assert.equal(Boolean(full.trigger.full),true);
const menu=full.created.find(node=>node.tag==='div' && node.children[2]);
const fullItem=menu.children[2];
fullItem.onclick();
assert.equal(fullItem.dataset.value,'full');
assert.equal(fullItem.children[0].children[1].children[1].textContent,'Unrestricted access');
console.log('PASS model menu sends selection without claiming persistence; local settings update immediately');
