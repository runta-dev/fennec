// Run the real self-drawn menu handler with a minimal DOM boundary.
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
function run(optimistic) {
 const buttons=[];
 function element(tag){return {tag,style:{},children:[],setAttribute(){},append(...nodes){this.children.push(...nodes);if(tag==='div')for(const node of nodes)if(node.tag==='button')buttons.push(node)},remove(){},focus(){},contains(){return false},querySelector(){return {focus(){}}},getBoundingClientRect(){return {width:180,height:32}}};}
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
console.log('PASS model menu sends selection without claiming persistence; local settings update immediately');
