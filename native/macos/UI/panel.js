const send = action => window.webkit.messageHandlers.fennec.postMessage(action);
const chevron = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="m6 4 4 4-4 4"/></svg>';
let expandedAgent = null;
let authorized = false;
let takeoverAgents = new Set();
let takeoverChanging = false;
let takeoverSelectionKnown = false;
let activeTakeoverAgents = new Set();
let entryCoverage = new Map();
let detectedAgents = [];
let routeResults = new Map();
let gatewayModels = [];
let localGatewayModels = new Map();
let localGatewayPending = new Set();
function loadLocalGatewayModel(agent){if(localGatewayPending.has(agent))return;localGatewayPending.add(agent);send('get-local-model:'+JSON.stringify({agent}));}
let nativeModelStates = new Map();
let nativeAccountStates = new Map();
let nativeAccountPending = new Set();
function loadNativeAccount(agent,location,refresh=false){const key=agent+':'+location;if(nativeAccountPending.has(key))return;nativeAccountPending.add(key);send('get-agent-account:'+JSON.stringify({agent,location,refresh}));}

let nativeModelRequests = new Map();
function loadNativeModel(agent){nativeModelRequests.set(agent,'pending');send('get-model:'+JSON.stringify({agent}));}
let modelSaving = false;
let sessionDetailOpen = false;
let activeSession = null;
let sessionSending = false;

function syncTabIndicator(){
 const navigation=document.querySelector('nav');
 const selected=navigation?.querySelector('[data-tab].selected');
 const indicator=navigation?.querySelector('.tab-indicator');
 if(!indicator)return;
 indicator.hidden=!selected;
 if(!selected)return;
 indicator.style.width=selected.offsetWidth+'px';
 indicator.style.height=selected.offsetHeight+'px';
 indicator.style.transform=`translate(${selected.offsetLeft}px, ${selected.offsetTop}px)`;
}

function tab(name) {
  send("visible-tab:" + name);
  if (name !== 'session-detail' && sessionDetailOpen) {
    window.closeSessionTerminal?.();
    sessionDetailOpen=false;
    send('cancel-session-read');
    document.querySelector('#session-output').textContent='';
  }
  if (name === "account") send("load-billing");
  document.querySelector('#settings').classList.toggle('active', name === 'preferences');
  document.querySelector('#settings').setAttribute('aria-pressed', String(name === 'preferences'));
  document.querySelectorAll('main>section').forEach(section => section.hidden = section.id !== name);
  document.querySelectorAll('[data-tab]').forEach(button => {
    button.classList.toggle('selected', button.dataset.tab === name);
    button.setAttribute('aria-selected', String(button.dataset.tab === name));
  });
  syncTabIndicator();
}
const tabNavigation=document.querySelector('nav');
const tabIndicator=document.createElement('span');tabIndicator.className='tab-indicator';tabIndicator.setAttribute('aria-hidden','true');
tabIndicator.style.transition='none';tabNavigation.prepend(tabIndicator);
syncTabIndicator();requestAnimationFrame(()=>{tabIndicator.style.transition='';});
const tabResizeObserver=new ResizeObserver(syncTabIndicator);
tabResizeObserver.observe(tabNavigation);
document.querySelectorAll('[data-tab]').forEach(button=>tabResizeObserver.observe(button));
document.querySelectorAll('[data-tab]').forEach(button => button.onclick = () => tab(button.dataset.tab));
document.querySelector('#settings').onclick = () => tab('preferences');
window.renderRefreshing = () => {};
document.querySelector('#refresh')?.remove();
document.addEventListener('dragstart',event=>{
 if(event.target.closest?.('img,svg'))event.preventDefault();
});
const userMenu = document.querySelector('#user-menu');
document.querySelector('#logout').onclick = () => {
  userMenu.hidden = true;
  send('logout');
};
document.addEventListener('keydown', event => {
  if (event.key === 'Escape') userMenu.hidden = true;
});
document.addEventListener('click', event => {
  if (event.target.closest('.login')) send('login');
  if (event.target.closest('.header-login.account-link')) {
    userMenu.hidden = !userMenu.hidden;
    return;
  }
  if (!event.target.closest('#user-menu')) userMenu.hidden = true;
});

let agentFoldTimer = null;
let agentLocationTimer = null;
const renderedLocations = new Map();
let pendingAgentRender = false;
let agentEffortEditing = false;
const finishEffortEditing=()=>{if(!agentEffortEditing)return;agentEffortEditing=false;requestAnimationFrame(()=>{if(pendingAgentRender&&!modelSaving&&!agentEffortEditing){pendingAgentRender=false;window.renderAgents(detectedAgents);}});};
window.addEventListener('pointerup',finishEffortEditing);window.addEventListener('pointercancel',finishEffortEditing);window.addEventListener('keyup',finishEffortEditing);
window.renderAgents = agents => {
  detectedAgents = agents;
  if (agentFoldTimer !== null || agentLocationTimer !== null || agentEffortEditing || modelSaving) { pendingAgentRender = true; return; }
  const list = document.querySelector('#agent-list');
  list.replaceChildren();
  document.querySelector('#count').textContent = agents.length;
  for (const agent of agents) {
    const article = document.createElement('article');
    article.className = 'agent';
    const head = document.createElement('button');
    head.className = 'agent-head';
    head.setAttribute('aria-expanded', String(expandedAgent === agent.id));
    head.setAttribute('aria-controls', `detail-${agent.id}`);
    const icon = document.createElement('img');
    icon.className = 'agent-icon';
    icon.draggable = false;
    icon.alt = '';
    icon.src = 'icons/' + agent.icon;
    const name = document.createElement('span');
    name.className = 'agent-name';
    name.textContent = agent.name;
    const state = document.createElement('span');
    state.className = 'detected';
    state.textContent = "[[i18n:ui.this_mac]]";
    const arrow = document.createElement('span');
    arrow.className = 'chevron';
    arrow.innerHTML = chevron;
    head.append(icon, name, state, arrow);
    if(!agent.cloudSupported || !['claude','codex','pi'].includes(agent.id)){
      head.disabled=true;
      head.removeAttribute('aria-expanded');head.removeAttribute('aria-controls');
      head.classList.add('agent-coming-soon');
      state.textContent="[[i18n:ui.coming_soon]]";arrow.remove();
      if(expandedAgent===agent.id)expandedAgent=null;
      article.append(head);list.append(article);continue;
    }
    const detail = document.createElement('div');
    detail.className = 'detail';
    detail.id = `detail-${agent.id}`;
    const fold = document.createElement('div');
    fold.className = 'agent-fold' + (expandedAgent === agent.id ? ' open' : '');
    fold.inert = expandedAgent !== agent.id;
    const clip = document.createElement('div');
    clip.className = 'agent-fold-clip';
    detail.innerHTML = `
      <div class="takeover"><span class="takeover-name">[[i18n:ui.run_location]]</span><div class="location-segments" role="radiogroup" aria-label="[[i18n:ui.run_location]]"><button type="button" role="radio" data-location="local" aria-checked="true" disabled>[[i18n:ui.this_mac]]</button><button type="button" role="radio" data-location="cloud" aria-checked="false" disabled>[[i18n:ui.cloud_computer]]</button></div></div>
      <div class="takeover-status">[[i18n:ui.sign_in_for_cloud_takeover]]</div>
`;
    if (!agent.installed) state.textContent = "[[i18n:ui.not_installed]]";
    head.onclick = () => {
      expandedAgent = expandedAgent === agent.id ? null : agent.id;
      if(expandedAgent && magpieEnabled && !takeoverAgents.has(agent.id))loadLocalGatewayModel(agent.id);
      if(expandedAgent && !magpieEnabled){loadNativeAccount(agent.id,takeoverAgents.has(agent.id)?'cloud':'local');if(takeoverAgents.has(agent.id))loadNativeModel(agent.id);}
      list.querySelectorAll('.agent-head').forEach(button => {
        const body = document.getElementById(button.getAttribute('aria-controls'));
        if (!body) return;
        const opening = body.id === `detail-${expandedAgent}`;
        button.setAttribute('aria-expanded', String(opening));
        const container = body.closest('.agent-fold');
        container.classList.toggle('open', opening);
        container.inert = !opening;
      });
      clearTimeout(agentFoldTimer);
      // Keep the live DOM stable while the fold moves; consume the newest update once.
      agentFoldTimer = setTimeout(() => {
        agentFoldTimer = null;
        if (pendingAgentRender) { pendingAgentRender = false; window.renderAgents(detectedAgents); }
      }, window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 360);
    };
    if (authorized) {
      const route = routeResults.get(agent.id);
      const toggle=detail.querySelector('.location-segments');
      const enabled=takeoverAgents.has(agent.id);
      if(takeoverSelectionKnown){
       const previous=renderedLocations.get(agent.id);
       if(previous!==undefined&&previous!==enabled && expandedAgent===agent.id){
        toggle.classList.add(enabled?'slide-to-cloud':'slide-to-local');
        if(!matchMedia('(prefers-reduced-motion: reduce)').matches){
         clearTimeout(agentLocationTimer);
         agentLocationTimer=setTimeout(()=>{
          agentLocationTimer=null;
          if(pendingAgentRender){pendingAgentRender=false;window.renderAgents(detectedAgents);}
         },280);
        }
       }
       renderedLocations.set(agent.id,enabled);
      }
      const choices=[...toggle.querySelectorAll('button')];
      choices.forEach((button,index)=>{
       const cloud=button.dataset.location==='cloud';
       const selected=cloud===enabled;
       button.setAttribute('aria-checked',String(selected));button.tabIndex=selected?0:-1;
       button.disabled=takeoverChanging || (!takeoverSelectionKnown && !enabled) || (cloud&&!agent.cloudSupported);
       button.onclick=()=>{if(cloud!==enabled)send('toggle-takeover:'+agent.id);};
       button.onkeydown=event=>{if(!['ArrowLeft','ArrowRight'].includes(event.key))return;event.preventDefault();const other=choices[1-index];if(!other.disabled){other.click();other.focus();}};
      });
      if (enabled) {
        state.textContent = "[[i18n:ui.cloud_computer]]";
      }
      const labels = {distribution_unsupported:"[[i18n:ui.cloud_setup_for_this_version_is_not_yet_implemented]]", version_detection_failed:"[[i18n:ui.cannot_read_agent_version]]", installation_failed:"[[i18n:ui.agent_version_installation_failed]]", catalog_refresh_failed:"[[i18n:ui.model_catalog_refresh_failed]]", configured:"[[i18n:ui.ready_to_enable]]", model_unavailable:"[[i18n:ui.selected_model_unavailable]]", model_selection_required:"[[i18n:ui.select_a_local_model_first]]", configuration_failed:"[[i18n:ui.model_configuration_failed]]", verification_failed:"[[i18n:ui.model_verification_failed]]", model_query_failed:"[[i18n:ui.cannot_read_local_model]]", local_gateway_not_found:"[[i18n:ui.local_model_gateway_not_found]]"};
      detail.querySelector('.takeover-status').textContent = activeTakeoverAgents.has(agent.id) ? '' : route && route.status !== 'configured' ? (labels[route.status] || "[[i18n:ui.unable_to_connect_please_try_again]]") : enabled ? "[[i18n:ui.connecting]]" : '';

      const statusNode = detail.querySelector('.takeover-status');
      if (!activeTakeoverAgents.has(agent.id)) {
        const recoveryLabels = {model_rate_limited:"[[i18n:ui.model_rate_limited_retrying]]", model_temporarily_unavailable:"[[i18n:ui.model_temporarily_unavailable_retrying]]", model_authentication_required:"[[i18n:ui.model_credentials_expired_sign_in_again]]", model_access_denied:"[[i18n:ui.model_access_denied_check_your_provider_s_limits]]"};
        if (recoveryLabels[route?.status]) statusNode.textContent = recoveryLabels[route.status];
      }
      const unknown = !takeoverSelectionKnown && agent.cloudSupported && !enabled;
      if(unknown){
        state.textContent='';state.classList.add('agent-location-skeleton','setting-skeleton');state.setAttribute('aria-label',"[[i18n:ui.loading_settings]]");toggle.hidden=true;statusNode.textContent='';
        const placeholder=document.createElement('span');placeholder.className='takeover-loading setting-skeleton';placeholder.setAttribute('aria-label',"[[i18n:ui.loading_settings]]");
        detail.querySelector('.takeover').append(placeholder);
      }
      statusNode.hidden = !statusNode.textContent;
      if(statusNode.textContent==="[[i18n:ui.connecting]]"){statusNode.textContent='';statusNode.hidden=true;if(window.fennecPreparationStage!=='offline')state.classList.add('loading-shimmer');}
      if (magpieEnabled && route?.status === 'model_selection_required') {statusNode.textContent='';statusNode.hidden=true;}
      {
        const row=document.createElement('div');row.className='agent-integration-row';
        const icon=document.querySelector('#magpie-label')?.previousElementSibling?.cloneNode(true);
        const label=document.createElement('span');label.textContent='Magpie';
        const info=document.querySelector('#magpie-label')?.parentElement?.querySelector('.setting-info')?.cloneNode(true);
        if(info){const tooltip=info.querySelector('[role="tooltip"]');const button=info.querySelector('.setting-info-button');if(tooltip&&button){tooltip.id='magpie-info-'+agent.id;button.setAttribute('aria-describedby',tooltip.id);}}
        const toggle=document.createElement('button');toggle.className='switch';toggle.type='button';toggle.setAttribute('role','switch');toggle.setAttribute('aria-label','Magpie');toggle.setAttribute('aria-checked',String(magpieEnabled));
        toggle.onclick=()=>send('settings:'+JSON.stringify({magpie:!magpieEnabled}));
        if(icon)row.append(icon);row.append(label);if(info)row.append(info);row.append(toggle);detail.append(row);
      }
      if(!magpieEnabled){
        const location=enabled?'cloud':'local';const key=agent.id+':'+location;const account=nativeAccountStates.get(key);
        if(expandedAgent===agent.id&&!account&&!nativeAccountPending.has(key))loadNativeAccount(agent.id,location);
        statusNode.hidden=true;statusNode.textContent='';
        const card=document.createElement('div');card.className='native-account-card';card.setAttribute('aria-label',"[[i18n:ui.account]]");
        const entries=account?.status==='signed_in'&&account.accounts?.length?account.accounts:[null];
        for(const identity of entries){
          const accountRow=document.createElement('div');accountRow.className='native-account-row';
          const badge=document.createElement('span');badge.className='native-account-check'+(identity?'':' is-signed-out');badge.setAttribute('aria-hidden','true');
          badge.innerHTML=identity?'<svg viewBox="0 0 20 20"><circle cx="10" cy="10" r="9" fill="currentColor"/><path d="m5.8 10 2.7 2.8 5.7-6" fill="none" stroke="var(--card)" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>':'<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="10" cy="10" r="8"/><circle cx="10" cy="7" r="2.4"/><path d="M5.4 16v-1.2c0-2 1.9-3.1 4.6-3.1s4.6 1.1 4.6 3.1V16"/></svg>';
          const name=document.createElement('span');name.className='native-account-name';
          if(identity)name.dataset.identity='true';
          name.textContent=identity?(identity.email||({openai:'OpenAI',anthropic:'Anthropic','openai-codex':'ChatGPT',google:'Google'}[identity.provider]||identity.provider)):(account?.status==='signed_out'?"[[i18n:ui.not_signed_in]]":account?"[[i18n:ui.account_unavailable]]":'');
          if(!account)name.classList.add('setting-skeleton');
          accountRow.append(badge,name);
          if(identity){const plan=document.createElement('span');plan.className='native-subscription';plan.textContent=identity.subscription||(identity.auth_type==='api_key'?'API key':'OAuth');accountRow.append(plan);}
          const refresh=document.createElement('button');refresh.type='button';refresh.className='native-account-refresh';refresh.setAttribute('aria-label',"[[i18n:ui.refresh_account]]");refresh.title="[[i18n:ui.refresh_account]]";refresh.disabled=nativeAccountPending.has(key);
          refresh.innerHTML='<svg viewBox="0 0 20 20" aria-hidden="true"><path d="M16 8a6 6 0 1 0 0 4M16 3v5h-5"/></svg>';
          refresh.onclick=()=>{loadNativeAccount(agent.id,location,true);refresh.disabled=true;};accountRow.append(refresh);
          if(enabled&&account?.status==='signed_out'){const login=document.createElement('button');login.type='button';login.className='native-account-login';login.dataset.agent=agent.id;login.textContent="[[i18n:ui.sign_in]]";login.disabled=!route;window.configureAgentLoginControl?.(login,!!route);login.onclick=()=>window.startAgentLogin(agent.id);accountRow.append(login);}
          card.append(accountRow);
        }
        detail.append(card);
      }
      if (magpieEnabled || (enabled && ['codex','claude','pi'].includes(agent.id))) {
        const native=nativeModelStates.get(agent.id);
        const local=magpieEnabled&&!enabled;const localModel=localGatewayModels.get(agent.id);
        if(local && expandedAgent===agent.id && !localModel && !localGatewayPending.has(agent.id))loadLocalGatewayModel(agent.id);
        if(!magpieEnabled && !native && expandedAgent===agent.id && !nativeModelRequests.has(agent.id))loadNativeModel(agent.id);
        const models=magpieEnabled ? (localModel?.models?.length&&local?localModel.models:gatewayModels).map(id=>({id,name:id.split('/').slice(1).join('/')})) : native?.models || [{id:'',name:"[[i18n:ui.default]]"}];
        const current=magpieEnabled ? (local?localModel?.model:route?.model) || '' : native?.model || '';
        const row=document.createElement('div');row.className='agent-model-row';
        const select=document.createElement('select');select.hidden=true;select.setAttribute('aria-label',"[[i18n:ui.model]]");
        for(const model of models){const option=document.createElement('option');option.value=model.id;option.textContent=model.name;select.append(option);}
        select.value=current;
        const trigger=document.createElement('button');trigger.type='button';trigger.className='choice-trigger agent-model-trigger';trigger.setAttribute('aria-haspopup','listbox');trigger.setAttribute('aria-expanded','false');
        trigger.disabled=modelSaving || (local && !localModel) || (!magpieEnabled && !native && nativeModelRequests.get(agent.id)!=='failed') || models.length===0;
        const label=document.createElement('span');label.className='field-label';label.textContent="[[i18n:ui.model]]";
        const value=document.createElement('span');value.className='field-value';value.textContent= !magpieEnabled && !native ? (nativeModelRequests.get(agent.id)==='failed'?"[[i18n:ui.retry]]":'') : models.find(m=>m.id===current)?.name || current || (magpieEnabled?"[[i18n:ui.choose_a_model]]":"[[i18n:ui.default]]");
        if(local&&localModel?.error)value.textContent="[[i18n:ui.retry]]";
        if(!magpieEnabled && !native && nativeModelRequests.get(agent.id)==='pending')value.classList.add('setting-skeleton');
        const modelIcon=icon.cloneNode(true);modelIcon.className='model-provider-icon';modelIcon.alt='';
        const provider=magpieEnabled ? current.split('/')[0] : models.find(m=>m.id===current)?.provider;
        const providerIcon={codex:'codex-color.svg',openai:'codex-color.svg',anthropic:'claude-color.svg',claude:'claude-color.svg',google:'geminicli-color.svg',xai:'xai.svg','kimi-code':'kimi.svg'}[provider];if(providerIcon)modelIcon.src='icons/'+providerIcon;
        const chevron=document.createElementNS('http://www.w3.org/2000/svg','svg');chevron.setAttribute('viewBox','0 0 16 16');chevron.innerHTML='<path d="m5 6 3 3 3-3"/>';
        trigger.append(label,value,modelIcon,chevron);
        trigger.onclick=()=>{if(local&&localModel?.error){loadLocalGatewayModel(agent.id);trigger.disabled=true;}else if(!magpieEnabled && !native){loadNativeModel(agent.id);window.renderAgents(detectedAgents);}else openChoiceMenu(select,trigger,{optimistic:false});};
        trigger.onkeydown=event=>{if(['ArrowDown','ArrowUp'].includes(event.key)){event.preventDefault();trigger.click();}};
        select.onchange=()=>{window.renderModelSaving(true);send('select-model:'+JSON.stringify({agent:agent.id,model:select.value,location:local?'local':'cloud'}));};
        row.append(select,trigger);if(magpieEnabled)detail.append(row);
        const selected=models.find(m=>m.id===current);
        if(!magpieEnabled && selected?.efforts?.length){
          const levels=['',...selected.efforts];
          const effort=document.createElement('div');effort.className='agent-effort';
          const caption=document.createElement('label');caption.textContent="[[i18n:ui.reasoning_effort]]";
          const result=document.createElement('span');result.textContent=native.effort || "[[i18n:ui.default]]";caption.append(result);
          const slider=document.createElement('input');slider.type='range';slider.min='0';slider.max=String(levels.length-1);slider.step='1';slider.value=String(Math.max(0,levels.indexOf(native.effort||'')));slider.disabled=modelSaving;slider.setAttribute('aria-label','Reasoning effort');slider.setAttribute('aria-valuetext',result.textContent);
          slider.oninput=()=>{result.textContent=levels[Number(slider.value)]||"[[i18n:ui.default]]";slider.setAttribute('aria-valuetext',result.textContent);};
          slider.onpointerdown=()=>{agentEffortEditing=true;};slider.onkeydown=event=>{if(['ArrowLeft','ArrowRight','ArrowUp','ArrowDown','Home','End','PageUp','PageDown'].includes(event.key))agentEffortEditing=true;};
          slider.onchange=()=>{window.renderModelSaving(true);send('select-model:'+JSON.stringify({agent:agent.id,model:current,effort:levels[Number(slider.value)]}));};
          const track=document.createElement('div');track.className='effort-track';
          const ticks=document.createElement('div');ticks.className='effort-ticks';ticks.setAttribute('aria-hidden','true');
          levels.forEach((_,index)=>{const tick=document.createElement('span');tick.style.left=(index/(levels.length-1)*100)+'%';ticks.append(tick);});
          track.append(ticks,slider);effort.append(caption,track);detail.append(effort);
        }
      }

    }
    clip.append(detail);
    fold.append(clip);
    article.append(head, fold);
    list.append(article);
  }
};
window.renderLogin = (message, busy) => {
  const notice = document.querySelector('#notice');
  const text = (message || '').trim();
  notice.hidden = !text;
  notice.classList.remove('loading-shimmer');
  const label = document.createElement('span');
  label.classList.toggle('loading-shimmer', !!busy && !!text);
  label.textContent = busy ? text.replace(/(?:\.{3}|…)$/, '') : text;
  notice.replaceChildren(label);
  document.querySelectorAll('.login').forEach(button => button.disabled = busy);
};
let sessionListConnecting = true;
let visibleSessionCount = 0;
window.renderAuthorized = () => {
  authorized = true;
  document.querySelector('#notice').hidden = true;
  const header = document.querySelector('.header-login');
  header.textContent = "[[i18n:ui.account]]";
  header.setAttribute('data-i18n','ui.account');
  header.disabled = false;
  header.classList.remove('login');
  header.classList.add('account-link');
  window.renderAgents(detectedAgents);
  document.querySelector('#session-signin').hidden = true;
  document.querySelector('#runtime-panel').hidden = sessionListConnecting && visibleSessionCount === 0;
};
const collapsedSessionParents = new Set();
function orderSessionTree(rows, collapsed = new Set()) {
 const ids=new Set(rows.map(row=>row.address));
 const visited=new Set(),ordered=[];
 const children=new Map();
 for(const row of rows){
  if(row.parent && row.parent!==row.address && ids.has(row.parent)){
   if(!children.has(row.parent))children.set(row.parent,[]);
   children.get(row.parent).push(row);
  }
 }
 function visit(row,depth){
  if(visited.has(row.address))return;
  visited.add(row.address);ordered.push({...row,depth:Math.min(depth,4),hasChildren:!!children.get(row.address)?.length});
  if (!collapsed.has(row.address)) for(const child of children.get(row.address)||[])visit(child,depth+1);
  else {
   const hide = parent => { for(const child of children.get(parent)||[]) { if(visited.has(child.address))continue; visited.add(child.address); hide(child.address); } };
   hide(row.address);
  }
 }
 for(const row of rows)if(!row.parent || row.parent===row.address || !ids.has(row.parent))visit(row,0);
 for(const row of rows)visit(row,0); // Broken/cyclic parent metadata must not hide sessions.
 return ordered;
}

let sessionActionMenu=null;
function closeSessionActions(restoreFocus=false){
 if(!sessionActionMenu)return;
 const {menu,trigger,cleanup}=sessionActionMenu;
 sessionActionMenu=null;cleanup();menu.remove();trigger.setAttribute('aria-expanded','false');
 if(restoreFocus&&trigger.isConnected)trigger.focus();
}
function openSessionActions(trigger,row){
 if(sessionActionMenu?.trigger===trigger){closeSessionActions(true);return;}
 closeSessionActions();
 const menu=document.createElement('div');menu.className='session-action-menu';menu.setAttribute('role','menu');
 const stop=document.createElement('button');stop.type='button';stop.className='session-stop';stop.setAttribute('role','menuitem');
 const stopIcon=document.createElement('span');stopIcon.className='session-kill-icon';
 stopIcon.innerHTML='<svg viewBox="0 0 20 20" aria-hidden="true"><rect x="5" y="5" width="10" height="10" rx="1.5" fill="currentColor"/></svg>';
 const stopLabel=document.createElement('span');stopLabel.textContent=typeof translate==='function'?translate("[[i18n:ui.stop_session]]"):"[[i18n:ui.stop_session]]";
 stop.append(stopIcon,stopLabel);
 stop.onclick=()=>{
  if(!row.canStop||stop.disabled)return;
  stop.disabled=true;send('stop-session:'+JSON.stringify({address:row.address,session_id:row.sessionID}));closeSessionActions(true);
 };
 menu.append(stop);document.body.append(menu);
 const anchor=trigger.getBoundingClientRect(),box=menu.getBoundingClientRect();
 menu.style.left=Math.max(8,Math.min(anchor.right-box.width,innerWidth-box.width-8))+'px';
 menu.style.top=(anchor.bottom+box.height+8<=innerHeight?anchor.bottom+4:Math.max(8,anchor.top-box.height-4))+'px';
 const outside=event=>{if(!menu.contains(event.target)&&!trigger.contains(event.target))closeSessionActions();};
 const key=event=>{
  if(event.key==='Escape'){event.preventDefault();closeSessionActions(true);}
  else if(event.key==='Tab')closeSessionActions();
  else if(event.key==='ArrowDown'||event.key==='ArrowUp'){event.preventDefault();stop.focus();}
 };
 const dismiss=()=>closeSessionActions();
 document.addEventListener('pointerdown',outside,true);document.addEventListener('keydown',key);
 document.addEventListener('scroll',dismiss,true);window.addEventListener('resize',dismiss);
 sessionActionMenu={menu,trigger,cleanup:()=>{
  document.removeEventListener('pointerdown',outside,true);document.removeEventListener('keydown',key);
  document.removeEventListener('scroll',dismiss,true);window.removeEventListener('resize',dismiss);
 }};
 trigger.setAttribute('aria-expanded','true');stop.focus();
}

const sessionCopyFeedback = new Map();
function updateSessionCopyButton(button) {
  const feedback = sessionCopyFeedback.get(button.dataset.sessionId);
  const active = feedback && feedback.until > Date.now();
  button.classList.toggle('has-copy-feedback', !!active);
  const result=button.querySelector('.session-copy-result');
  if(active){
    result.textContent=feedback.text;
    if(button.isConnected && document.createRange){
      const range=document.createRange();range.selectNodeContents(result);
      button.style.setProperty('--copy-feedback-width',Math.ceil(range.getBoundingClientRect().width+16)+'px');
    }
  }
}
window.renderRuntimes = (rows, message) => {
  visibleSessionCount = rows.length;
  document.querySelector('#runtime-panel').hidden = !authorized || (sessionListConnecting && rows.length === 0);
  closeSessionActions();
  const list = document.querySelector('#runtime-list');
  list.replaceChildren();
  const feedback = document.querySelector('#runtime-message');
  feedback.replaceChildren();
  feedback.hidden = rows.length > 0 && !message;
  feedback.className = rows.length ? 'runtime-note' : 'runtime-empty';
  if (!rows.length) {
    const icon = document.createElement('span');
    icon.className = 'runtime-empty-icon';
    icon.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="4" width="16" height="6" rx="2"/><rect x="4" y="14" width="16" height="6" rx="2"/><path d="M7 7h.01M7 17h.01M11 7h6M11 17h6"/></svg>';
    feedback.append(icon);
  }
  const text = document.createElement('span');
  text.textContent = message || "[[i18n:ui.no_active_sessions]]";
  feedback.append(text);
  list.hidden = false;
  const groups = [
    ['idle',"[[i18n:ui.idle]]"],['working',"[[i18n:ui.working]]"],['blocked',"[[i18n:ui.needs_input]]"],['error',"[[i18n:ui.errored]]"]
  ];
  for (const [group,label] of groups) {
   const members=rows.filter(row=>(row.group || 'unknown')===group);
   if (!members.length) continue;
   const section=document.createElement('section');section.className='session-group';
   const heading=document.createElement('div');heading.className='session-group-heading';
   const caption=document.createElement('span');caption.className='runtime-status';caption.textContent=label;
   const count=document.createElement('span');count.className='session-group-count';count.textContent=String(members.length);
   heading.append(caption,count);section.append(heading);
   for (const row of orderSessionTree(members, collapsedSessionParents)) {
    const item = document.createElement('div'); item.className = 'runtime-row';
    item.style.setProperty('--session-depth',String(row.depth));
    if(row.depth) item.classList.add('session-child');
    const info = document.createElement('div'); info.className = 'runtime-info';
    const name = document.createElement('span'); name.className='session-title'; name.textContent = row.name; name.title=row.name;
    if(row.untitled)name.setAttribute('data-i18n','ui.untitled_session');
    const status = document.createElement('span'); status.className = 'runtime-status';
    status.classList.add('session-state-dot');status.dataset.state=group;
    status.setAttribute('aria-label',row.displayStatus || row.status);
    if(row.hasChildren){
      const disclosure=document.createElement('button');disclosure.className='session-disclosure';
      disclosure.innerHTML=chevron;
      disclosure.setAttribute('aria-label',row.name);
      disclosure.setAttribute('aria-expanded',String(!collapsedSessionParents.has(row.address)));
      disclosure.onclick=()=>{
        if(collapsedSessionParents.has(row.address))collapsedSessionParents.delete(row.address);
        else collapsedSessionParents.add(row.address);
        window.renderRuntimes(rows,message);
      };
      item.append(disclosure);
    }
    info.append(name);
    const agentIcons={claude:"claudecode-color.svg",codex:"codex-color.svg",pi:"pi.svg",opencode:"opencode.svg",kimi:"kimi.svg",devin:"devin.svg",grok:"xai.svg",cursor:"cursor.svg"};
    const identity=document.createElement('span');identity.className='session-agent-identity';
    if(agentIcons[row.agent]){const icon=document.createElement("img");icon.className="agent-icon session-agent-icon";icon.src="icons/"+agentIcons[row.agent];icon.alt=row.agent;icon.title=row.agent;icon.draggable=false;identity.append(icon);}
    else{identity.innerHTML='<svg class="session-agent-fallback" viewBox="0 0 20 20" aria-hidden="true"><rect x="2" y="3" width="16" height="14" rx="3"/><path d="m5 7 3 3-3 3m6 0h4"/></svg>';}
    identity.append(status);item.append(identity);
    item.append(info);
    if(row.sessionID){
      const actions=document.createElement('div');actions.className='session-actions';
      const payload=JSON.stringify({address:row.address,session_id:row.sessionID});
      const copy=document.createElement('button');copy.className='session-copy';copy.type='button';
      copy.dataset.sessionId=row.sessionID;
      copy.setAttribute('aria-label',"[[i18n:ui.copy_attach_command_2]]");
      const terminalIcon=document.createElement('span');terminalIcon.className='session-terminal-icon';
      terminalIcon.innerHTML='<svg viewBox="0 0 20 20" aria-hidden="true"><rect x="7" y="7" width="10" height="10" rx="2"/><path d="M13 7V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2"/></svg>';
      const copyLabel=document.createElement('span');copyLabel.className='session-copy-label';copyLabel.textContent="[[i18n:ui.copy_attach_command_2]]";
      copy.append(terminalIcon,copyLabel);
      const copyResult=document.createElement('span');copyResult.className='session-copy-result';
      copyResult.setAttribute('role','status');copyResult.setAttribute('aria-live','polite');
      copy.append(copyResult);
      updateSessionCopyButton(copy);
      copy.onclick=()=>send('copy-attach:'+payload);
      actions.append(copy);
      const browser=document.createElement('button');browser.className='session-more session-browser';browser.type='button';
      browser.setAttribute('aria-label',"[[i18n:ui.open_in_browser]]");browser.title="[[i18n:ui.open_in_browser]]";
      browser.innerHTML='<svg viewBox="0 0 20 20" aria-hidden="true"><path d="M11 3h6v6m0-6-9 9M8 5H5a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2v-3"/></svg>';
      browser.onclick=()=>send('browser-attach:'+payload);actions.append(browser);
      if(row.canStop){
       const more=document.createElement('button');more.className='session-more';more.type='button';
       more.setAttribute('aria-label',"[[i18n:ui.session_actions]]");more.setAttribute('aria-haspopup','menu');more.setAttribute('aria-expanded','false');
       more.innerHTML='<svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="4" cy="10" r="1"/><circle cx="10" cy="10" r="1"/><circle cx="16" cy="10" r="1"/></svg>';
       more.onclick=()=>openSessionActions(more,row);actions.append(more);
      }
      item.append(actions);
    }
    section.append(item);
   }
   list.append(section);
  }
};
window.renderProfile = (name, fallback = false) => {
  const button = document.querySelector('.header-login');
  if(fallback){button.setAttribute('data-i18n','ui.account');button.textContent='[[i18n:ui.account]]';button.removeAttribute('title');}
  else{button.removeAttribute('data-i18n');button.textContent=name;button.title=name;}
};
window.renderSignedOut = () => {
  window.resetAgentLogin?.();
  authorized = false;
  sessionListConnecting = true;
  visibleSessionCount = 0;
  routeResults.clear();
  takeoverAgents.clear();activeTakeoverAgents.clear();takeoverChanging=false;takeoverSelectionKnown=false;
  gatewayModels = [];localGatewayModels.clear();localGatewayPending.clear();
  nativeModelStates.clear();nativeModelRequests.clear();nativeAccountStates.clear();nativeAccountPending.clear();
  modelSaving = false;
  window.renderBilling({error:"[[i18n:ui.sign_in_first]]"});
  userMenu.hidden = true;
  document.querySelector('#runtime-panel').hidden = true;
  document.querySelector('#runtime-list').replaceChildren();
  document.querySelector('#session-signin').hidden = false;
  const header = document.querySelector('.header-login');
  header.classList.remove('account-link');
  header.classList.add('login');
  header.textContent = "[[i18n:ui.sign_in_to_runta]]";
  header.setAttribute('data-i18n','ui.sign_in_to_runta');
  header.removeAttribute('title');
  header.disabled = false;
  document.querySelector('#notice').hidden = true;
  tab('agents');
  send('refresh');
};
const appearance = {value:'system'};
try { appearance.value = localStorage.getItem('appearance') || 'system'; } catch (_) {}
function applyAppearance() {
  document.documentElement.dataset.theme = appearance.value;
  try { localStorage.setItem('appearance', appearance.value); } catch (_) {}
  document.querySelectorAll('[data-theme-choice]').forEach(button=>{
    const selected=button.dataset.themeChoice===appearance.value;
    button.setAttribute('aria-checked',String(selected));button.tabIndex=selected?0:-1;
  });
}
appearance.onchange = applyAppearance;
applyAppearance();
const themeButtons=[...document.querySelectorAll('[data-theme-choice]')];
themeButtons.forEach((button,index)=>{
 const select=()=>{appearance.value=button.dataset.themeChoice;applyAppearance();send('settings:'+JSON.stringify({theme:appearance.value}));};
 button.onclick=select;
 button.onkeydown=event=>{
  let next;
  if(event.key==='ArrowRight'||event.key==='ArrowDown')next=(index+1)%3;
  else if(event.key==='ArrowLeft'||event.key==='ArrowUp')next=(index+2)%3;
  else if(event.key==='Home')next=0;
  else if(event.key==='End')next=2;
  else return;
  event.preventDefault();themeButtons[next].click();themeButtons[next].focus();
 };
});
window.exportLocalPreferences = () => ({theme:appearance.value,language:document.querySelector('#language')?.value || 'zh-CN'});
window.renderMagpiePreference = enabled => {
 if(magpieEnabled!==enabled){nativeModelStates.clear();nativeModelRequests.clear();nativeAccountStates.clear();nativeAccountPending.clear();localGatewayModels.clear();localGatewayPending.clear();}
 magpieEnabled=enabled;
 document.querySelector('#magpie-integration')?.setAttribute('aria-checked',String(magpieEnabled));
};
window.renderPreferences = value => {

 window.renderMagpiePreference(value.magpie === true);
 window.renderUpdatePreferences?.(value);
 window.renderAgents(detectedAgents);
 if(['system','light','dark'].includes(value.theme) && appearance.value!==value.theme){appearance.value=value.theme;applyAppearance();}
 if(typeof supportedLanguages!=='undefined' && supportedLanguages.includes(value.language) && language!==value.language){
  language=value.language;languageSelect.value=language;
  try{localStorage.setItem('language',language);}catch(_){}
  localize();
  window.rerenderUpdater?.();
  const trigger=languageSelect.nextElementSibling;
  if(trigger?.classList.contains('choice-trigger')){const label=trigger.querySelector('span');if(label)label.textContent=languageSelect.selectedOptions[0].textContent;}
 }
};
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', applyAppearance);

const loginItemSwitch = document.querySelector('#launch-at-login');
let magpieEnabled = false;
const magpieSwitch = document.querySelector('#magpie-integration');
if(magpieSwitch) magpieSwitch.onclick=()=>send('settings:'+JSON.stringify({magpie:!magpieEnabled}));
const loginItemApproval = document.querySelector('#login-item-approval');
window.renderLoginItem = value => {
  if (!loginItemSwitch || !loginItemApproval) return;
  loginItemSwitch.disabled = !value.supported;
  loginItemSwitch.setAttribute('aria-checked', String(!!value.enabled));
  loginItemApproval.hidden = !value.approvalRequired;
  if (value.error) showFeedbackToast("[[i18n:ui.could_not_update_login_item_try_again]]");
};
if (loginItemSwitch) loginItemSwitch.onclick = () => {
  loginItemSwitch.disabled = true;
  send('login-item:' + (loginItemSwitch.getAttribute('aria-checked') === 'true' ? 'off' : 'on'));
};
if (loginItemApproval) loginItemApproval.onclick = () => send('login-item:settings');
window.addEventListener('focus', () => send('login-item:status'));

document.querySelector("#magpie-license").textContent = "MIT License\n\nCopyright (c) 2026 yetone\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\nof this software and associated documentation files (the \"Software\"), to deal\nin the Software without restriction, including without limitation the rights\nto use, copy, modify, merge, publish, distribute, sublicense, and/or sell\ncopies of the Software, and to permit persons to whom the Software is\nfurnished to do so, subject to the following conditions:\n\nThe above copyright notice and this permission notice shall be included in all\ncopies or substantial portions of the Software.\n\nTHE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR\nIMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,\nFITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE\nAUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER\nLIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,\nOUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE\nSOFTWARE.\n";

window.renderApprovals = requests => {
  const region = document.querySelector('#approvals');
  region.replaceChildren(); region.hidden = !requests.length;
  for (const request of requests) {
    const card = document.createElement('div'); card.className = 'approval-card';
    const heading = document.createElement('strong'); heading.textContent = "[[i18n:ui.request_to_run_a_local_command]]";
    const source = document.createElement('p'); source.textContent = request.fingerprint;
    const command = document.createElement('pre'); command.textContent = request.command;
    const warning = document.createElement('p'); warning.textContent = "[[i18n:ui.runs_as_you_with_access_to_your_files_and_network]]";
    const deny = document.createElement('button'); deny.textContent = "[[i18n:ui.deny]]"; deny.onclick = () => send('deny:' + request.id);
    const approve = document.createElement('button'); approve.textContent = "[[i18n:ui.allow_once]]"; approve.onclick = () => send('approve:' + request.id);
    card.append(heading, source, command, warning, deny, approve); region.append(card);
  }
};

window.renderPreparation = state => {
  const previousStage = window.fennecPreparationStage || '';
  window.fennecPreparationStage = state.stage || '';
  sessionListConnecting = !['agents_installed', 'disabled'].includes(state.stage);
  document.querySelector('#runtime-panel').hidden = !authorized || state.stage === 'disabled' || (sessionListConnecting && visibleSessionCount === 0);
  const panel = document.querySelector('#preparation');
  panel.replaceChildren();
	if(state.stage==='disabled'){
	 window.fennecPreparationVisible=true;
	 document.querySelector('#runtime-panel').hidden=true;panel.hidden=false;panel.setAttribute('aria-busy','false');
	 const row=document.createElement('div');row.className='preparation-row';
	 const title=document.createElement('span');title.className='preparation-title';title.textContent='[[i18n:ui.cloud_computer_is_not_ready]]';
	 row.append(title);panel.append(row);return;
	}
  // Completed infrastructure is not a global error when individual agents
  // still need sign-in or model selection; those actions belong to their cards.
  panel.hidden = !state.stage || ['waiting_credentials', 'credentials_required', 'agents_installed'].includes(state.stage);
  window.fennecPreparationVisible = !panel.hidden;
  if (!panel.hidden) {
    const runtimeNote = document.querySelector('#runtime-message');
    if (runtimeNote && runtimeNote.className === 'runtime-note') {
      runtimeNote.hidden = true;
      runtimeNote.replaceChildren();
    }
  }
  if (previousStage === 'offline' && state.stage !== 'offline' && typeof nativeAccountStates !== 'undefined') {
    let cleared = false;
    for (const [key, value] of nativeAccountStates) {
      if (key.endsWith(':cloud') && value?.status === 'unavailable') { nativeAccountStates.delete(key); cleared = true; }
    }
    if (typeof nativeModelRequests !== 'undefined') {
      for (const [agent, status] of nativeModelRequests) {
        if (status === 'failed') { nativeModelRequests.delete(agent); cleared = true; }
      }
    }
    if (cleared && typeof window.renderAgents === 'function') window.renderAgents(detectedAgents);
  } else if ((previousStage === 'offline') !== (state.stage === 'offline') && typeof window.renderAgents === 'function') {
    window.renderAgents(detectedAgents);
  }
  if (panel.hidden) return;
  const titles = {checking_resources:"[[i18n:ui.verifying_cloud_computer_resources]]", installing_session_service:"[[i18n:ui.preparing_session_service]]", restoring_model_accounts:"[[i18n:ui.restoring_model_accounts]]", configuring_routes:"[[i18n:ui.configuring_agent_models]]", installing_model_plugins:"[[i18n:ui.configuring_model_plugins]]", migrating_credentials:"[[i18n:ui.migrating_model_credentials]]", discovering_providers:"[[i18n:ui.loading_model_configuration]]", waiting_credentials:"[[i18n:ui.reading_sign_in_credentials]]", credentials_required:"[[i18n:ui.sign_in_again_to_continue_setup]]", installing_pi:"[[i18n:ui.installing_pi]]", installing_opencode:"[[i18n:ui.installing_opencode]]", starting_magpie:"[[i18n:ui.starting_model_gateway]]", installing_node:"[[i18n:ui.installing_runtime_tools]]", installing_magpie:"[[i18n:ui.installing_model_gateway]]", detecting_versions:"[[i18n:ui.discovering_available_agents]]", syncing_versions:"[[i18n:ui.checking_cloud_agents]]", installing_claude:"[[i18n:ui.installing_claude_code]]", installing_codex:"[[i18n:ui.installing_codex]]", agents_installed:"[[i18n:ui.waiting_for_model_gateway_configuration]]", allocating:"[[i18n:ui.allocating_cloud_computer]]", starting:"[[i18n:ui.starting_cloud_computer]]", connecting:"[[i18n:ui.connecting_to_cloud_computer]]", runtime_ready:"[[i18n:ui.environment_started_agent_installation_pending]]", offline:"[[i18n:ui.offline_waiting_for_network]]", failed:"[[i18n:ui.environment_preparation_failed]]"};
  const row = document.createElement('div'); row.className = 'preparation-row';
  const title = document.createElement('span'); title.className = 'preparation-title';
  const reasons = {network_offline:"[[i18n:ui.offline_waiting_for_network]]", native_credentials_need_attention:"[[i18n:ui.sign_in_to_the_agents_you_want_to_use_on_your_mac_then_retry]]",model_routes_need_attention:"[[i18n:ui.some_agent_models_need_configuration]]", routes_configured_inference_pending:"[[i18n:ui.models_configured_verification_pending]]", no_local_model_credentials:"[[i18n:ui.no_local_model_credentials_configured]]", no_model_providers:"[[i18n:ui.no_model_providers_configured]]", no_compatible_model_providers:"[[i18n:ui.no_compatible_model_configuration]]", provider_catalog_unavailable:"[[i18n:ui.unable_to_load_model_configuration]]", provider_selection_required:"[[i18n:ui.waiting_for_model_provider_selection]]"};
  title.textContent = reasons[state.status] || titles[state.stage] || "[[i18n:ui.preparing_2]]";
  const indicator = document.createElement('span'); indicator.className = 'preparation-indicator';
  const isOffline = state.stage === 'offline';
  const needsAction = ['failed', 'credentials_required', 'agents_installed'].includes(state.stage);
  panel.setAttribute('aria-busy', String(!needsAction && !isOffline));
  if (isOffline) {
    indicator.innerHTML = '<svg viewBox="0 0 20 20" aria-hidden="true"><path d="M3 3l14 14M16.7 13.5A4 4 0 0 0 15 6.1h-.7A5.5 5.5 0 0 0 4.6 4.7M3.5 8.1A4.5 4.5 0 0 0 5.5 16h8"/></svg>';
  } else if (needsAction) {
    indicator.innerHTML = '<svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="7.5"/><path d="M10 6v5m0 3h.01"/></svg>';
  } else {
    title.classList.add('loading-shimmer');
    indicator.classList.add('working');
    indicator.setAttribute('role', 'progressbar');
    indicator.setAttribute('aria-label', "[[i18n:ui.setup_progress]]");
    indicator.innerHTML = '<svg viewBox="0 0 20 20" aria-hidden="true"><circle class="progress-track" cx="10" cy="10" r="7.5"/><path d="M10 2.5a7.5 7.5 0 0 1 7.5 7.5"/></svg>';
  }
  row.append(title, indicator); panel.append(row);
  if (isOffline) {
    const detail = document.createElement('p');
    detail.textContent = "[[i18n:ui.reconnects_automatically_when_back_online]]";
    panel.append(detail);
  }
  if (state.stage === 'failed') {
    const detail = document.createElement('p');
    detail.textContent = "[[i18n:ui.unable_to_finish_setup_please_retry]]";
    panel.append(detail);
  }
  if (needsAction) {
    const retry = document.createElement('button');
    const loginRequired = state.stage === 'credentials_required';
    retry.textContent = loginRequired ? "[[i18n:ui.sign_in]]" : "[[i18n:ui.retry]]";
    retry.onclick = () => send(loginRequired ? 'login' : 'retry-runtime');
    panel.append(retry);
  }
};

send('ready');

window.renderProviderCatalog = candidates => {
  // Preserve a typed candidate list for the upcoming configuration control.
  window.fennecProviderCandidates = candidates.map(({id,name,model,compatible}) => ({id,name,model,compatible}));
};

let lastBillingPayload = {};
const billingFields = [
 ['balance',"[[i18n:ui.available_balance]]",'<path d="M4 6h12a2 2 0 0 1 2 2v8H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h11v2M18 10h-5v4h5"/><path d="M15 12h.01"/>'],
 ['spend',"[[i18n:ui.last_30_days_spend]]",'<rect x="3" y="4" width="14" height="14" rx="2"/><path d="M6 2v4m8-4v4M3 8h14m-11 6 3-3 2 2 3-3"/>'],
 ['cpu','CPU','<rect x="5" y="5" width="10" height="10" rx="2"/><rect x="8" y="8" width="4" height="4" rx=".5"/><path d="M7 2v3m6-3v3M7 15v3m6-3v3M2 7h3m-3 6h3m10-6h3m-3 6h3"/>'],
 ['memory',"[[i18n:ui.memory]]",'<rect x="2" y="5" width="16" height="10" rx="2"/><path d="M5 8v4m3-4v4m4-4v4m3-4v4M5 15v2m5-2v2m5-2v2"/>'],
 ['disk',"[[i18n:ui.disk]]",'<path d="m4 4-2 8v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4l-2-8Z"/><path d="M2 12h16m-4 3h.01m-3 0h.01"/>']
];
function renderBillingRows(payload,loading){
 const content=document.querySelector('#billing-content');content.replaceChildren();
 content.setAttribute('aria-busy',String(loading));
 const resources=document.createElement('div');resources.className='billing-resources';
 let missing=false;
 for(const [key,label,path] of billingFields){
  const raw=payload[key];
  const money=key==='balance'||key==='spend';
  const valid=money?typeof raw==='number'&&Number.isFinite(raw):raw!==null&&raw!==undefined&&raw!=='';
  missing ||= !valid;
  const row=document.createElement('div');row.className='billing-row';
  const title=document.createElement('span');title.className='billing-field-label';
  const icon=document.createElement('span');icon.className='billing-field-icon';icon.setAttribute('aria-hidden','true');
  icon.innerHTML=`<svg viewBox="0 0 20 20">${path}</svg>`;
  const text=document.createElement('span');text.textContent=label;title.append(icon,text);
  const value=document.createElement('span');value.className='billing-value';
  if(loading&&!valid){value.classList.add('setting-skeleton','billing-skeleton-value');value.setAttribute('aria-label',"[[i18n:ui.loading]]");}
  else value.textContent=valid?(money?new Intl.NumberFormat(document.documentElement.lang,{style:'currency',currency:payload.currency||'USD'}).format(raw/100):String(raw)):'—';
  row.append(title,value);
  if(money)content.append(row);else resources.append(row);
 }
 content.append(resources);
 const note=document.createElement('p');note.className='billing-footnote';note.textContent="[[i18n:ui.resource_usage_last_30_days]]";content.append(note);
 const footer=document.createElement('div');footer.className='billing-footer';
 const status=document.createElement('span');status.className='billing-status';
 const webOnly=payload.web_only===true;
 const offline=payload.offline===true;
 status.textContent=!loading?(offline?"[[i18n:ui.offline_waiting_for_network]]":webOnly?"[[i18n:ui.view_billing_in_runta_dashboard]]":(payload.error||(payload.stale?"[[i18n:ui.updating_showing_last_available_data]]":''))):'';
 const retry=document.createElement('button');retry.type='button';retry.textContent="[[i18n:ui.retry]]";
 if(webOnly){
  retry.textContent="[[i18n:ui.open_in_browser]]";
  retry.style.visibility=!loading?'visible':'hidden';
  retry.disabled=loading;
  retry.onclick=()=>send('open-dashboard');
 }else{
  retry.style.visibility=!loading&&!offline&&(missing||payload.error||payload.stale)?'visible':'hidden';
  retry.disabled=loading;
  retry.onclick=()=>{window.renderBillingLoading();send('load-billing');};
 }
 footer.append(status,retry);content.append(footer);
}
window.renderBillingLoading = () => renderBillingRows(lastBillingPayload,true);
window.renderBilling = payload => {lastBillingPayload=payload;renderBillingRows(payload,false);};

window.renderRouteResults = results => {
  const previous=routeResults;
  routeResults = new Map(results.map(result => [result.agent, result]));
  for(const result of results){const key=result.agent+':cloud';if(nativeAccountStates.get(key)?.status==='unavailable'||previous.get(result.agent)?.status!==result.status){nativeAccountStates.delete(key);if(nativeModelRequests.get(result.agent)==='failed')nativeModelRequests.delete(result.agent);}}
  window.renderAgents(detectedAgents);
};

window.renderNativeAccount = state => {if(!state?.agent||!state?.location)return;const key=state.agent+':'+state.location;nativeAccountPending.delete(key);nativeAccountStates.set(key,state);window.renderAgents(detectedAgents);};
window.resetNativeAccountViews = () => {modelSaving=false;agentEffortEditing=false;nativeAccountStates.clear();nativeAccountPending.clear();nativeModelStates.clear();nativeModelRequests.clear();};
window.renderLocalModel = state => {if(!state?.agent)return;localGatewayPending.delete(state.agent);localGatewayModels.set(state.agent,{...localGatewayModels.get(state.agent),...state});window.renderAgents(detectedAgents);};
window.renderNativeModelFailure = agent => {nativeModelRequests.set(agent,'failed');window.renderAgents(detectedAgents);};
window.renderNativeModels = state => { if(!state || magpieEnabled || !Array.isArray(state.models))return;nativeModelStates.set(state.agent,state);window.renderAgents(detectedAgents); };
window.renderModelCatalog = models => { gatewayModels = models; window.renderAgents(detectedAgents); };

window.renderModelSaving = busy => {
  modelSaving=busy;
  document.querySelectorAll('.agent-model-trigger,.agent-effort input').forEach(control=>{control.disabled=busy;});
  if(!busy)window.renderAgents(detectedAgents);
};

document.querySelector('#clear-trust').onclick = () => {
  document.querySelector('#clear-trust').disabled=true;
  send('clear-trust');
};
window.renderTrustResult = success => {
  document.querySelector('#clear-trust').disabled=false;
  const notice=document.querySelector('#trust-result');notice.hidden=true;notice.textContent='';
  showFeedbackToast(success?"[[i18n:ui.automatic_access_revoked]]":"[[i18n:ui.could_not_revoke_access_try_again]]");
};

let lastSessionTerminals=[];
const confirmedStoppedSessions=new Set();
window.renderSessionStopped = sessionID => {
 confirmedStoppedSessions.add(sessionID);
 window.renderHerdrSessions(lastSessionTerminals);
};
window.renderHerdrSessions = terminals => {
 lastSessionTerminals=terminals.filter(terminal=>!confirmedStoppedSessions.has(terminal.labels?.['fennec.session']));
 terminals=lastSessionTerminals;
 const labels={working:"[[i18n:ui.working]]",blocked:"[[i18n:ui.needs_input]]",idle:"[[i18n:ui.idle]]",error:"[[i18n:ui.errored]]"};
 const readableName=value=>{
  if(typeof value!=='string')return '';
  const title=value.trim().replace(/\s*[|·]\s*(?:[a-f0-9]{24,64}|[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}|[a-f0-9][a-f0-9-]{7,63}(?:\.{3}|…))\s*$/i,'').trim();
  return /^(?:[a-f0-9]{24,64}|[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}|[a-f0-9][a-f0-9-]{7,63}(?:\.{3}|…))$/i.test(title)?'':title;
 };
 const rows=terminals.filter(terminal=>terminal.state==='live').map(terminal=>{
   const raw=typeof terminal.status==='string'?terminal.status:terminal.status?.state;
   const group=Object.hasOwn(labels,raw)?raw:'idle';
   const displayStatus=labels[group];
   const sessionID=terminal.labels?.['fennec.session'];
   const name=readableName(terminal.title)||readableName(terminal.name);
   return {agent:terminal.labels?.["fennec.agent"] || terminal.agent,name:name||"[[i18n:ui.untitled_session]]",untitled:!name,group,status:raw||'',displayStatus,address:terminal.address,parent:typeof terminal.parent==='string'?terminal.parent:null,sessionID,canStop:terminal.state==='live' && /^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$/.test(sessionID||'')};
 });
 window.renderRuntimes(rows,'');
};

window.renderLaunchStatus = (agent,message) => {
 for(const status of document.querySelectorAll('[data-launch-agent]')){
  if(status.dataset.launchAgent===agent)status.textContent=message;
 }
};

window.renderSessionRefreshError = () => {
 if (window.fennecPreparationVisible) return;
 const note=document.querySelector('#runtime-message');
 note.replaceChildren();
 note.hidden=false;
 note.className='runtime-note';
 note.textContent="[[i18n:ui.session_status_unavailable_refresh_to_retry]]";
 // Existing rows remain visible; absence of a response is not an empty list.
};

function showFeedbackToast(message){
 const localize=text=>typeof translate==='function'?translate(text):text;
 let toast=document.querySelector('#session-toast');
 if(!toast){
  toast=document.createElement('div');toast.id='session-toast';
  toast.setAttribute('role','status');toast.setAttribute('aria-live','polite');toast.setAttribute('aria-atomic','true');
  document.body.append(toast);
 }
 clearTimeout(toast.hideTimer);
 toast.textContent=localize(message);
 toast.hidden=false;
 toast.hideTimer=setTimeout(()=>{toast.hidden=true;},2200);
}
window.renderAttachCopyResult = (sessionID,success) => {
  const feedback = {text:success?'Copied':'Failed',until:Date.now()+1800};
  sessionCopyFeedback.set(sessionID,feedback);
  const update = () => document.querySelectorAll('.session-copy').forEach(button => {
    if(button.dataset.sessionId===sessionID) updateSessionCopyButton(button);
  });
  update();
  setTimeout(() => {
    if(sessionCopyFeedback.get(sessionID)!==feedback) return;
    sessionCopyFeedback.delete(sessionID);
    update();
  },1800);
};

window.renderSessionActionError = () => {
 const note=document.querySelector('#runtime-message');note.hidden=false;note.textContent="[[i18n:ui.session_action_unconfirmed_refresh_and_retry]]";
 document.querySelectorAll('.session-stop').forEach(button=>{
  if(button.dataset.canStop==='true')button.disabled=false;
 });
};

window.renderSessionDetail = result => {
 if(!result){sessionDetailOpen=true;tab('session-detail');}
 else if(!sessionDetailOpen)return;
 const output=document.querySelector('#session-output');
 const terminalElement=document.querySelector('#session-terminal');
 if(terminalElement){
  const live=!!result && !result.error && result.terminal?.state==='live';
  terminalElement.hidden=!live;output.hidden=live;
  if(live && activeSession){
   document.querySelector('#session-detail-status').textContent='';
   window.openSessionTerminal(terminalElement,activeSession);
   document.querySelector('#session-input-form').hidden=true;
   return;
  }
  window.closeSessionTerminal?.();
 }
 const form=document.querySelector('#session-input-form');
 form.hidden=!result || !!result.error || result.terminal?.state!=='live';
 const note=document.querySelector('#session-detail-status');
 if(!result){output.textContent='';note.textContent="[[i18n:ui.loading]]";return;}
 if(result.error){output.textContent='';note.textContent="[[i18n:ui.could_not_read_session_retry]]";return;}
 output.textContent=result.screen?.text || '';
 note.textContent=result.screen?.complete===false?"[[i18n:ui.current_screen_only_history_is_incomplete]]":'';
};
document.querySelector('#session-back').onclick=()=>tab('sessions');

document.querySelector('#session-input-form').onsubmit = event => {
 event.preventDefault();
 const input=document.querySelector('#session-input');
 if(!activeSession||sessionSending||!input.value.trim())return;
 sessionSending=true;document.querySelector('#session-send').disabled=true;
 document.querySelector('#session-send-status').textContent="[[i18n:ui.submitting]]";
 send('send-session:'+JSON.stringify({...activeSession,text:input.value}));
};
window.renderSessionSent = (sessionID,success) => {
 sessionSending=false;document.querySelector('#session-send').disabled=false;
 if(!sessionDetailOpen||activeSession?.session_id!==sessionID)return;
 document.querySelector('#session-send-status').textContent=success?"[[i18n:ui.submitted]]":"[[i18n:ui.submission_unconfirmed_inspect_session_before_retrying]]";
 if(success)document.querySelector('#session-input').value='';
};

window.renderTakeover = value => {
  takeoverSelectionKnown=value.selectionKnown===true;
 takeoverAgents=new Set(value.agents||[]);activeTakeoverAgents=new Set(value.activeAgents||[]);takeoverChanging=!!value.changing;
 window.renderAgents(detectedAgents);
};

window.renderEntryCoverage = value => {
 entryCoverage.set(value.agent,value.coverage);
 window.renderAgents(detectedAgents);
};

let foxMotionFrame = null;
let foxMotionPending = false;
let foxReadyFrame = null;
window.requestFoxMotion = () => {
 foxMotionPending=true;
 cancelAnimationFrame(foxReadyFrame);
 if(document.hidden) return;
 foxReadyFrame=requestAnimationFrame(()=>{
  foxReadyFrame=requestAnimationFrame(()=>{
   if(document.hidden || !foxMotionPending) return;
   foxMotionPending=false;
   send('fox-motion-ready');
  });
 });
};
document.addEventListener('visibilitychange',()=>{
 if(!document.hidden && foxMotionPending) window.requestFoxMotion();
});
window.animateFox = motion => {
 const fox=document.querySelector('.brand .fennec-mark');
 if(!fox || !motion) return;
 cancelAnimationFrame(foxMotionFrame);
 const draw=(left,right,eye,body)=>{
  fox.style.transform=body?`rotate(${body}deg)`:'';
  const lx=left*4.27,ly=left*4.98,rx=right*4.27,ry=right*4.98;
  fox.querySelector('path:first-of-type')?.setAttribute('d',`M5 39L${5+lx} ${9+ly}C${5+lx} ${6+ly} ${7+lx} ${5+ly} ${9+lx} ${7+ly}L32 22L${55-rx} ${7+ry}C${57-rx} ${5+ry} ${59-rx} ${6+ry} ${59-rx} ${9+ry}L59 39L34 57c-1.2.9-2.8.9-4 0Z`);
  fox.querySelector('.fox-ear-left')?.setAttribute('d',`M${8+lx} ${6+ly} 32 22 20 31Z`);
  fox.querySelector('.fox-ear-right')?.setAttribute('d',`M${56-rx} ${6+ry} 32 22 44 31Z`);
  fox.querySelectorAll('.fox-eye').forEach(node=>node.style.transform=`scaleY(${eye})`);
 };
 const tick=()=>{
  const progress=(Date.now()-motion.started)/motion.duration;
  if(progress>=1 || document.hidden || matchMedia('(prefers-reduced-motion: reduce)').matches){draw(0,0,1,0);foxMotionFrame=null;return;}
  const index=Math.max(1,motion.frames.findIndex(frame=>frame[0]>=progress));
  const a=motion.frames[index-1],b=motion.frames[index];
  const t=Math.max(0,Math.min(1,(progress-a[0])/(b[0]-a[0])));
  let low=0,high=1;
  for(let i=0;i<16;i++){const u=(low+high)/2,v=1-u,x=.9*v*v*u+.9*v*u*u+u*u*u;if(x<t)low=u;else high=u;}
  const u=(low+high)/2,ease=t===0?0:t===1?1:3*(1-u)*u*u+u*u*u;
  draw(...[1,2,3,4].map(i=>a[i]+(b[i]-a[i])*ease));
  foxMotionFrame=requestAnimationFrame(tick);
 };
 tick();
};
