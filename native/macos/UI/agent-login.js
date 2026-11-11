// Sign-in delegates to the native agent in a browser session, not an account manager.
(() => {
 const pending=new Set();
 const controls=agent=>[...document.querySelectorAll('.native-account-login')].filter(button=>button.dataset.agent===agent);
 const sync=button=>{const busy=pending.has(button.dataset.agent);button.disabled=busy||button.dataset.loginReady!=='true';if(busy)button.setAttribute('aria-busy','true');else button.removeAttribute('aria-busy');};
 window.configureAgentLoginControl=(button,ready)=>{button.dataset.loginReady=String(ready);sync(button);};
 window.resetAgentLogin=()=>{pending.clear();document.querySelectorAll('.native-account-login').forEach(sync);};
 window.startAgentLogin=agent=>{
  if(pending.has(agent))return;
  pending.add(agent);controls(agent).forEach(sync);
  send('start-agent-login:'+JSON.stringify({agent}));
 };
 window.renderAgentLoginStarted=(_receipt,agent)=>{
  pending.delete(agent);controls(agent).forEach(sync);
 };
})();
