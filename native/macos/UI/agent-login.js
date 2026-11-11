(() => {
 let pending=false,login=null;
 const toolbar=document.querySelector('.session-detail-toolbar');
 const complete=document.createElement('button');complete.type='button';complete.className='agent-login-action';complete.textContent='完成登录';complete.hidden=true;
 const cancel=document.createElement('button');cancel.type='button';cancel.className='agent-login-action';cancel.textContent='取消登录';cancel.hidden=true;
 toolbar.append(complete,cancel);
 const note=()=>document.querySelector('#session-detail-status');
 const sync=()=>{complete.hidden=!login;cancel.hidden=!login;complete.disabled=pending;cancel.disabled=pending;};
 window.resetAgentLogin=()=>{pending=false;login=null;sync();};
 window.startAgentLogin=agent=>{
  if(pending)return;pending=true;sync();
  send('start-agent-login:'+JSON.stringify({agent}));
 };
 window.renderAgentLoginStarted=(receipt,agent)=>{
  pending=false;
  if(!receipt?.session_id){sync();return;}
  login={...receipt,agent};activeSession=login;sessionDetailOpen=true;tab('session-detail');
  document.querySelector('#session-detail-title').textContent=detectedAgents.find(a=>a.id===agent)?.name||agent;
  window.renderSessionDetail({terminal:{state:'live'}});
  note().textContent=agent==='pi'?'输入 /login 选择服务，登录后退出 Pi。':'在终端中的链接打开授权页面，完成后继续。';
  sync();
 };
 complete.onclick=()=>{if(!login||pending)return;pending=true;sync();send('finish-agent-login:'+JSON.stringify(login));};
 cancel.onclick=()=>{if(!login||pending)return;pending=true;sync();send('cancel-agent-login:'+JSON.stringify(login));};
 window.renderAgentLoginFinished=state=>{
  pending=false;
  if(state?.signed_in){nativeModelStates.delete(login?.agent);nativeModelRequests.delete(login?.agent);window.resetAgentLogin();window.closeSessionTerminal?.();tab('agents');return;}
  if(state && !state.running){login=null;note().textContent='登录未完成，请重新开始。';}
  else note().textContent=state?'请完成登录后继续。':'暂时无法检查登录，请重试。';
  sync();
 };
 window.renderAgentLoginCanceled=success=>{
  pending=false;
  if(success){window.resetAgentLogin();window.closeSessionTerminal?.();tab('agents');}
  else note().textContent='暂时无法取消登录，请重试。';
  sync();
 };
 const back=document.querySelector('#session-back');back.onclick=()=>{if(login)cancel.click();else tab('sessions');};
 window.agentLoginTerminalEnded=()=>{if(login&&!pending)complete.click();};
})();
