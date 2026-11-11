(() => {
 const modal=document.querySelector('#uninstall-dialog');
 const cloud=document.querySelector('#uninstall-cloud');
 const confirmation=document.querySelector('#uninstall-confirmation');
 const confirm=document.querySelector('#uninstall-confirm');
 const cancel=document.querySelector('#uninstall-cancel');
 const retry=document.querySelector('#uninstall-retry');
 const login=document.querySelector('#uninstall-login');
 const status=document.querySelector('#uninstall-status');
 let plan=null,busy=false,requestID=0,pending=false,lastRecord='';
 const say=text=>{status.textContent=text;};
 const sync=()=>{
  document.querySelector('#uninstall-cloud-warning').hidden=!cloud.checked;
  confirm.textContent=cloud.checked?'删除云电脑并卸载':'卸载';
  confirm.disabled=busy||!plan||(cloud.checked&&confirmation.value.trim()!==plan.cloud?.id);
  cloud.disabled=busy||pending;confirmation.disabled=busy;cancel.disabled=busy;
 };
 const preview=()=>{plan=null;retry.hidden=true;login.hidden=true;say('正在准备…');sync();send('uninstall-plan:'+JSON.stringify({delete_cloud:cloud.checked,request_id:++requestID}));};
 document.querySelector('#uninstall-open').onclick=()=>{
  plan=null;busy=false;confirmation.value='';if(!pending)cloud.checked=false;
  if(!modal.open)modal.showModal();preview();
 };
 cloud.onchange=()=>{confirmation.value='';preview();};confirmation.oninput=sync;
 cancel.onclick=()=>{if(!busy)modal.close();};
 modal.addEventListener('cancel',event=>{if(busy)event.preventDefault();});
 retry.onclick=preview;
 login.onclick=()=>{send('reauthenticate');say('请在浏览器完成登录，然后重试');};
 confirm.onclick=()=>{
  if(confirm.disabled||!plan)return;
  busy=true;retry.hidden=true;sync();say('正在卸载…');
  send('uninstall-execute:'+JSON.stringify({plan_id:plan.id,confirm_local:true,delete_cloud:plan.delete_cloud,confirm_runtime:plan.delete_cloud?confirmation.value.trim():''}));
 };
 window.renderUninstallPlan=(value,id)=>{
  if(id!==requestID||!modal.open)return;
  if(!value?.id){say(value?.login_required?'请先登录，再删除云电脑':'暂时无法准备卸载，请重试');login.hidden=!value?.login_required;retry.hidden=false;sync();return;}
  plan=value;cloud.checked=!!plan.delete_cloud;
  const apps=document.querySelector('#uninstall-apps');apps.replaceChildren();
  for(const path of plan.local?.apps||[]){const item=document.createElement('li');item.textContent=path;apps.append(item);}
  document.querySelector('#uninstall-runtime-id').textContent=plan.cloud?.id||'';
  document.querySelector('#uninstall-cloud-name').textContent=plan.cloud?.name||'';
  say('');sync();
 };
 window.renderUninstallStarted=success=>{
  if(success){pending=true;say('正在卸载…');}
  else{busy=false;plan=null;say('暂时无法卸载，请重试');retry.hidden=false;}
  sync();
 };
 window.renderUninstallStatus=record=>{
  if(!record?.plan?.id)return;
  const signature=JSON.stringify([record.plan.id,record.stage,record.error]);if(signature===lastRecord)return;lastRecord=signature;
  pending=record.stage!=='complete';cloud.checked=!!record.plan.delete_cloud;
  if(!modal.open)modal.showModal();
  if(record.stage==='failed'){
   busy=false;plan=null;say('卸载未完成，请重试');retry.hidden=false;
   login.hidden=record.error_code!=='login_required';
   cancel.textContent='关闭';
  }else{
   busy=record.stage!=='complete';retry.hidden=true;
   login.hidden=true;
   say(record.stage==='deleting_cloud'?'正在删除云电脑…':record.stage==='complete'?'卸载完成':'正在卸载…');
  }
  document.querySelector('#uninstall-runtime-id').textContent=record.plan.cloud?.id||'';
  document.querySelector('#uninstall-cloud-name').textContent=record.plan.cloud?.name||'';
  sync();
 };
})();
