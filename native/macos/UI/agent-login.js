(() => {
 let pending=false,login=null;
 const toolbar=document.querySelector('.session-detail-toolbar');
 const complete=document.createElement('button');complete.type='button';complete.className='agent-login-action';complete.textContent="[[i18n:ui.continue]]";complete.hidden=true;
 const cancel=document.createElement('button');cancel.type='button';cancel.className='agent-login-action';cancel.textContent="[[i18n:ui.cancel_sign_in]]";cancel.hidden=true;
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
  note().textContent=agent==='pi'?"[[i18n:ui.enter_login_to_choose_a_provider_exit_pi_after_signing_in]]":"[[i18n:ui.open_the_authorization_link_in_the_terminal_then_continue]]";
  sync();
 };
 complete.onclick=()=>{if(!login||pending)return;pending=true;sync();send('finish-agent-login:'+JSON.stringify(login));};
 cancel.onclick=()=>{if(!login||pending)return;pending=true;sync();send('cancel-agent-login:'+JSON.stringify(login));};
 window.renderAgentLoginFinished=state=>{
  pending=false;
  if(state?.signed_in){nativeModelStates.delete(login?.agent);nativeModelRequests.delete(login?.agent);window.resetAgentLogin();window.closeSessionTerminal?.();tab('agents');return;}
  if(state && !state.running){login=null;note().textContent="[[i18n:ui.sign_in_was_not_completed_start_again]]";}
  else note().textContent=state?"[[i18n:ui.finish_signing_in_then_continue]]":"[[i18n:ui.could_not_check_sign_in_try_again]]";
  sync();
 };
 window.renderAgentLoginCanceled=success=>{
  pending=false;
  if(success){window.resetAgentLogin();window.closeSessionTerminal?.();tab('agents');}
  else note().textContent="[[i18n:ui.could_not_cancel_sign_in_try_again]]";
  sync();
 };
 const back=document.querySelector('#session-back');back.onclick=()=>{if(login)cancel.click();else tab('sessions');};
 window.agentLoginTerminalEnded=()=>{if(login&&!pending)complete.click();};
})();
