// Sign-in delegates to the native agent in a browser session, not an account manager.
(() => {
 const pending=new Set();
 const controls=agent=>[...document.querySelectorAll('.native-account-login')].filter(button=>button.dataset.agent===agent);
 window.resetAgentLogin=()=>pending.clear();
 window.startAgentLogin=agent=>{
  if(pending.has(agent))return;
  pending.add(agent);controls(agent).forEach(button=>{button.disabled=true;button.setAttribute('aria-busy','true');});
  send('start-agent-login:'+JSON.stringify({agent}));
 };
 window.renderAgentLoginStarted=(_receipt,agent)=>{
  pending.delete(agent);controls(agent).forEach(button=>{button.disabled=false;button.removeAttribute('aria-busy');});
 };
})();
