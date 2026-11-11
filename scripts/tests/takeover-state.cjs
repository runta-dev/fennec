const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');

const source = fs.readFileSync('native/macos/UI/panel.js', 'utf8');
const expression = source.match(/detail\.querySelector\('\.takeover-status'\)\.textContent = (.*);/)[1];
const status = {};
const context = vm.createContext({
  detail: {querySelector: () => status},
  activeTakeoverAgents: new Set(),
  entryCoverage: new Map(),
  route: {status: 'model_unavailable'},
  labels: {model_unavailable: '所选模型暂不可用'},
  enabled: true,
  agent: {cloudSupported: true},
});
vm.runInContext(`detail.querySelector('.takeover-status').textContent = ${expression}`, context);
assert.equal(status.textContent, '所选模型暂不可用');
console.log('PASS unavailable model takes precedence over stale takeover preference');
context.agent.id='codex';context.activeTakeoverAgents.add('codex');
for(const coverage of [undefined,'new_shells','failed','entry_and_new_shells']){
 context.entryCoverage.set('codex',coverage);
 vm.runInContext(`detail.querySelector('.takeover-status').textContent = ${expression}`,context);
 assert.equal(status.textContent,'','internal CLI integration state leaked into product UI');
}
console.log('PASS internal entry diagnostics never appear as product errors');
const disabledExpression=source.match(/button\.disabled=(.*);/)[1];
for(const route of [undefined,{status:'configuration_failed'},{status:'configured'}]){
 const button={};const state=vm.createContext({button,takeoverChanging:false,takeoverSelectionKnown:true,enabled:false,cloud:true,agent:{cloudSupported:true},route});
 vm.runInContext('button.disabled='+disabledExpression,state);
 assert.equal(button.disabled,false,'cloud placement must remain selectable while preparation recovers');
}
console.log('PASS cloud placement is independent of model/preparation readiness');
