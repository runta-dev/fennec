// UI-only presentation; the daemon owns confirmation, deletion, and recovery.
(() => {
 const account=document.querySelector('#account');
 if(!account)return;
 let state=null,plan=null,busy=false,timer=null;
 const card=document.createElement('div');card.className='card cloud-computer-card';card.hidden=true;
 card.innerHTML='<div class="cloud-computer-row"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="M5 15a4 4 0 1 1 .5-8 5 5 0 0 1 9.5 1 3.5 3.5 0 0 1 0 7Z"/></svg><span>[[i18n:ui.cloud_computer]]</span><button class="cloud-computer-action" type="button"></button></div>';
 account.append(card);
 const action=card.querySelector('button');
 const dialog=document.createElement('dialog');dialog.className='cloud-delete-dialog';dialog.setAttribute('aria-labelledby','cloud-delete-title');
 dialog.innerHTML='<h2 id="cloud-delete-title">[[i18n:ui.cloud_delete_title]]</h2><p>[[i18n:ui.cloud_delete_scope]]</p><p>[[i18n:ui.cloud_keep_local]]</p><code class="cloud-delete-target"></code><label for="cloud-delete-confirm">[[i18n:ui.cloud_type_id]]</label><input id="cloud-delete-confirm" autocomplete="off" spellcheck="false"><p class="cloud-delete-error" role="status"></p><div class="cloud-delete-actions"><button type="button" class="cloud-delete-cancel">[[i18n:ui.cloud_cancel]]</button><button type="button" class="cloud-delete-confirm" disabled>[[i18n:ui.cloud_delete_title]]</button></div>';
 document.body.append(dialog);
 const input=dialog.querySelector('input'),confirm=dialog.querySelector('.cloud-delete-confirm'),cancel=dialog.querySelector('.cloud-delete-cancel'),error=dialog.querySelector('.cloud-delete-error');
 const text=value=>typeof translate==='function'?translate(value):value;
 const draw=()=>{
  card.hidden=!state?.runtime_id;
  action.disabled=busy||state?.stage==='deleting';
  action.setAttribute('aria-busy',String(action.disabled));
  action.textContent=text(state?.stage==='deleted'?'[[i18n:ui.cloud_create]]':state?.stage==='deleting'?'[[i18n:ui.deleting_cloud_computer]]':['unconfirmed','review_required'].includes(state?.stage)?'[[i18n:ui.cloud_review_delete]]':'[[i18n:ui.cloud_delete_title]]');
  action.classList.toggle('is-danger',state?.stage!=='deleted');
 };
 action.onclick=()=>{
  if(busy||!state?.runtime_id)return;
  busy=true;action.removeAttribute('title');draw();
  if(state.stage==='deleted'&&state.can_create)send('cloud-create:'+JSON.stringify({confirm_runtime:state.runtime_id,request_id:state.request_id}));
  else send('cloud-preview:'+JSON.stringify({runtime_id:state.runtime_id}));
 };
 input.oninput=()=>{confirm.disabled=busy||!plan||input.value!==plan.target.runtime;};
 cancel.onclick=()=>{if(!busy)dialog.close();};
 dialog.addEventListener('cancel',event=>{if(busy)event.preventDefault();});
 dialog.addEventListener('close',()=>{plan=null;input.value='';action.focus();});
 confirm.onclick=()=>{
  if(busy||!plan||input.value!==plan.target.runtime)return;
  busy=true;confirm.disabled=true;cancel.disabled=true;input.disabled=true;error.textContent='';
  send('cloud-delete:'+JSON.stringify({plan_id:plan.id,confirm_runtime:plan.target.runtime}));
 };
 window.renderCloudState=value=>{
  clearTimeout(timer);timer=null;
  if(!value)return;
  state=value;draw();
  if(value.stage==='deleting'&&!account.hidden)timer=setTimeout(()=>send('load-cloud'),500);
 };
 window.renderCloudPreview=value=>{
  busy=false;draw();
  if(!value?.id||value.target?.runtime!==state?.runtime_id){action.title=text('[[i18n:ui.cloud_review_unavailable]]');return;}
  plan=value;input.value='';input.disabled=false;confirm.disabled=true;cancel.disabled=false;error.textContent='';
  dialog.querySelector('.cloud-delete-target').textContent=value.target.runtime;
  if(!dialog.open)dialog.showModal();input.focus();
 };
 window.renderCloudDeleteResult=success=>{
  busy=false;cancel.disabled=false;input.disabled=false;
  if(success){dialog.close();state={...state,stage:'deleting',can_create:false};window.renderCloudState(state);send('load-cloud');}
  else{error.textContent=text('[[i18n:ui.cloud_confirm_unavailable]]');confirm.disabled=true;plan=null;}
  draw();
 };
 window.renderCloudCreateResult=success=>{busy=false;draw();if(!success)action.title=text('[[i18n:ui.cloud_create_unavailable]]');send('load-cloud');};
 window.renderCloudReadFailure=()=>{clearTimeout(timer);timer=null;busy=false;draw();action.title=text('[[i18n:ui.cloud_status_unavailable]]');};
 window.resetCloudComputer=()=>{clearTimeout(timer);timer=null;state=null;plan=null;busy=false;if(dialog.open)dialog.close();draw();};
})();
