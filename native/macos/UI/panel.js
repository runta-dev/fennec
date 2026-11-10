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
const renderedLocations = new Map();
let pendingAgentRender = false;
window.renderAgents = agents => {
  detectedAgents = agents;
  if (agentFoldTimer !== null) { pendingAgentRender = true; return; }
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
    state.textContent = '本机';
    const arrow = document.createElement('span');
    arrow.className = 'chevron';
    arrow.innerHTML = chevron;
    head.append(icon, name, state, arrow);
    const detail = document.createElement('div');
    detail.className = 'detail';
    detail.id = `detail-${agent.id}`;
    const fold = document.createElement('div');
    fold.className = 'agent-fold' + (expandedAgent === agent.id ? ' open' : '');
    fold.inert = expandedAgent !== agent.id;
    const clip = document.createElement('div');
    clip.className = 'agent-fold-clip';
    detail.innerHTML = `
      <div class="takeover"><span class="takeover-name">运行位置</span><div class="location-segments" role="radiogroup" aria-label="运行位置"><button type="button" role="radio" data-location="local" aria-checked="true" disabled>本机</button><button type="button" role="radio" data-location="cloud" aria-checked="false" disabled>云电脑</button></div></div>
      <div class="takeover-status">登录后开启云端接管</div>
`;
    if (!agent.installed) state.textContent = '未安装';
    head.onclick = () => {
      expandedAgent = expandedAgent === agent.id ? null : agent.id;
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
       if(previous!==undefined&&previous!==enabled)toggle.classList.add(enabled?'slide-to-cloud':'slide-to-local');
       renderedLocations.set(agent.id,enabled);
      }
      const choices=[...toggle.querySelectorAll('button')];
      choices.forEach((button,index)=>{
       const cloud=button.dataset.location==='cloud';
       const selected=cloud===enabled;
       button.setAttribute('aria-checked',String(selected));button.tabIndex=selected?0:-1;
       button.disabled=takeoverChanging || (!takeoverSelectionKnown && !enabled) || (cloud&&!enabled&&route?.status!=='configured');
       button.onclick=()=>{if(cloud!==enabled)send('toggle-takeover:'+agent.id);};
       button.onkeydown=event=>{if(!['ArrowLeft','ArrowRight'].includes(event.key))return;event.preventDefault();const other=choices[1-index];if(!other.disabled){other.click();other.focus();}};
      });
      if (enabled) {
        state.textContent = '云电脑';
      }
      const labels = {distribution_unsupported:'此版本的云端安装适配尚未完成', version_detection_failed:'无法读取智能体版本', installation_failed:'智能体版本安装失败', catalog_refresh_failed:'模型目录刷新失败', configured:'可开启', model_unavailable:'所选模型暂不可用', model_selection_required:'请先在本机选择模型', configuration_failed:'模型配置失败', verification_failed:'模型配置验证失败', model_query_failed:'无法读取本机模型', local_gateway_not_found:'未找到本机模型网关'};
      detail.querySelector('.takeover-status').textContent = activeTakeoverAgents.has(agent.id) ? '' : route && route.status !== 'configured' ? (labels[route.status] || '暂时无法连接，请重试') : enabled ? '正在连接…' : '';

      const statusNode = detail.querySelector('.takeover-status');
      if (!activeTakeoverAgents.has(agent.id)) {
        const recoveryLabels = {model_rate_limited:'模型限流，正在自动重试...', model_temporarily_unavailable:'模型暂时不可用，正在自动重试...', model_authentication_required:'模型凭据失效，请重新授权', model_access_denied:'模型服务拒绝访问，请检查服务限制'};
        if (recoveryLabels[route?.status]) statusNode.textContent = recoveryLabels[route.status];
      }
      const unknown = !takeoverSelectionKnown && agent.cloudSupported && !enabled;
      if(unknown){
        state.textContent='';state.classList.add('agent-location-skeleton','setting-skeleton');state.setAttribute('aria-label','正在读取设置');toggle.hidden=true;statusNode.textContent='';
        const placeholder=document.createElement('span');placeholder.className='takeover-loading setting-skeleton';placeholder.setAttribute('aria-label','正在读取设置');
        detail.querySelector('.takeover').append(placeholder);
      }
      statusNode.hidden = !statusNode.textContent;
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
  header.textContent = '账户';
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
 const stopLabel=document.createElement('span');stopLabel.textContent=typeof translate==='function'?translate('终止会话'):'终止会话';
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
  text.textContent = message || '暂无活跃会话';
  feedback.append(text);
  list.hidden = false;
  const groups = [
    ['idle','空闲'],['working','正在工作'],['blocked','等待输入'],['error','会话出错']
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
    info.append(name); item.append(status);
    const agentIcons={claude:"claudecode-color.svg",codex:"codex-color.svg",pi:"pi.svg",opencode:"opencode.svg",kimi:"kimi.svg",devin:"devin.svg",grok:"xai.svg",cursor:"cursor.svg"};
    if(agentIcons[row.agent]){const icon=document.createElement("img");icon.className="agent-icon session-agent-icon";icon.src="icons/"+agentIcons[row.agent];icon.alt=row.agent;icon.title=row.agent;icon.draggable=false;item.append(icon);}
    item.append(info);
    if(row.sessionID){
      const actions=document.createElement('div');actions.className='session-actions';
      const payload=JSON.stringify({address:row.address,session_id:row.sessionID});
      const copy=document.createElement('button');copy.className='session-copy';copy.type='button';
      copy.dataset.sessionId=row.sessionID;
      copy.setAttribute('aria-label','复制连接命令');
      const terminalIcon=document.createElement('span');terminalIcon.className='session-terminal-icon';
      terminalIcon.innerHTML='<svg viewBox="0 0 20 20" aria-hidden="true"><rect x="7" y="7" width="10" height="10" rx="2"/><path d="M13 7V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2"/></svg>';
      const copyLabel=document.createElement('span');copyLabel.className='session-copy-label';copyLabel.textContent='复制连接命令';
      copy.append(terminalIcon,copyLabel);
      const copyResult=document.createElement('span');copyResult.className='session-copy-result';
      copyResult.setAttribute('role','status');copyResult.setAttribute('aria-live','polite');
      copy.append(copyResult);
      updateSessionCopyButton(copy);
      copy.onclick=()=>send('copy-attach:'+payload);
      actions.append(copy);
      if(row.canStop){
       const more=document.createElement('button');more.className='session-more';more.type='button';
       more.setAttribute('aria-label','会话操作');more.setAttribute('aria-haspopup','menu');more.setAttribute('aria-expanded','false');
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
window.renderProfile = name => {
  const button = document.querySelector('.header-login');
  button.textContent = name;
  button.title = name;
};
window.renderSignedOut = () => {
  authorized = false;
  sessionListConnecting = true;
  visibleSessionCount = 0;
  routeResults.clear();
  takeoverAgents.clear();activeTakeoverAgents.clear();takeoverChanging=false;takeoverSelectionKnown=false;
  gatewayModels = [];
  modelSaving = false;
  window.renderBilling({error:"请先登录"});
  userMenu.hidden = true;
  document.querySelector('#runtime-panel').hidden = true;
  document.querySelector('#runtime-list').replaceChildren();
  document.querySelector('#session-signin').hidden = false;
  const header = document.querySelector('.header-login');
  header.classList.remove('account-link');
  header.classList.add('login');
  header.textContent = '登录 Runta';
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
window.renderPreferences = value => {
 if(['system','light','dark'].includes(value.theme) && appearance.value!==value.theme){appearance.value=value.theme;applyAppearance();}
 if(typeof supportedLanguages!=='undefined' && supportedLanguages.includes(value.language) && language!==value.language){
  language=value.language;languageSelect.value=language;
  try{localStorage.setItem('language',language);}catch(_){}
  localize();
  const trigger=languageSelect.nextElementSibling;
  if(trigger?.classList.contains('choice-trigger')){const label=trigger.querySelector('span');if(label)label.textContent=languageSelect.selectedOptions[0].textContent;}
 }
};
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', applyAppearance);

const loginItemSwitch = document.querySelector('#launch-at-login');
const loginItemApproval = document.querySelector('#login-item-approval');
window.renderLoginItem = value => {
  if (!loginItemSwitch || !loginItemApproval) return;
  loginItemSwitch.disabled = !value.supported;
  loginItemSwitch.setAttribute('aria-checked', String(!!value.enabled));
  loginItemApproval.hidden = !value.approvalRequired;
  if (value.error) showFeedbackToast('无法更新开机自启，请重试');
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
    const heading = document.createElement('strong'); heading.textContent = '请求在本机执行命令';
    const source = document.createElement('p'); source.textContent = request.fingerprint;
    const command = document.createElement('pre'); command.textContent = request.command;
    const warning = document.createElement('p'); warning.textContent = '以当前用户执行，可访问该用户的文件和网络。';
    const deny = document.createElement('button'); deny.textContent = '拒绝'; deny.onclick = () => send('deny:' + request.id);
    const approve = document.createElement('button'); approve.textContent = '允许一次'; approve.onclick = () => send('approve:' + request.id);
    card.append(heading, source, command, warning, deny, approve); region.append(card);
  }
};

window.renderPreparation = state => {
  sessionListConnecting = !['agents_installed', 'failed'].includes(state.stage);
  document.querySelector('#runtime-panel').hidden = !authorized || (sessionListConnecting && visibleSessionCount === 0);
  const panel = document.querySelector('#preparation');
  panel.replaceChildren();
  // Authentication belongs to the header, not a second setup card.
  panel.hidden = !state.stage || ['waiting_credentials', 'credentials_required'].includes(state.stage) ||
    (state.stage === 'agents_installed' && [...routeResults.values()].some(result => result.status === 'configured'));
  if (panel.hidden) return;
  const titles = {checking_resources:'正在确认云电脑资源', installing_session_service:'正在准备会话服务', restoring_model_accounts:'正在恢复模型账户', configuring_routes:'正在配置智能体模型', installing_model_plugins:'正在配置模型插件', migrating_credentials:'正在迁移模型凭据', discovering_providers:'正在读取模型配置', waiting_credentials:'正在读取登录凭证', credentials_required:'请重新登录以继续准备', installing_pi:'正在安装 Pi', installing_opencode:'正在安装 OpenCode', starting_magpie:'正在启动大模型网关', installing_node:'正在安装运行工具', installing_magpie:'正在安装大模型网关', detecting_versions:'正在发现可用智能体', syncing_versions:'正在确认云端智能体', installing_claude:'正在安装 Claude Code', installing_codex:'正在安装 Codex', agents_installed:'等待配置大模型网关', allocating:'正在分配云电脑', starting:'正在启动云电脑', connecting:'正在连接云电脑', runtime_ready:'环境已启动，等待安装智能体', failed:'云电脑准备失败'};
  const row = document.createElement('div'); row.className = 'preparation-row';
  const title = document.createElement('span'); title.className = 'preparation-title';
  const reasons = {model_routes_need_attention:'部分智能体模型需要配置', routes_configured_inference_pending:'模型已配置，等待验证', no_local_model_credentials:'本机尚未配置模型凭据', no_model_providers:'尚未配置模型服务', no_compatible_model_providers:'暂无兼容的模型配置', provider_catalog_unavailable:'无法读取模型配置', provider_selection_required:'等待选择模型服务'};
  title.textContent = reasons[state.status] || titles[state.stage] || '正在准备…';
  const indicator = document.createElement('span'); indicator.className = 'preparation-indicator';
  const needsAction = ['failed', 'credentials_required', 'agents_installed'].includes(state.stage);
  panel.setAttribute('aria-busy', String(!needsAction));
  if (needsAction) {
    indicator.innerHTML = '<svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="7.5"/><path d="M10 6v5m0 3h.01"/></svg>';
  } else {
    title.classList.add('loading-shimmer');
    indicator.classList.add('working');
    indicator.setAttribute('role', 'progressbar');
    indicator.setAttribute('aria-label', '准备进度');
    indicator.innerHTML = '<svg viewBox="0 0 20 20" aria-hidden="true"><circle class="progress-track" cx="10" cy="10" r="7.5"/><path d="M10 2.5a7.5 7.5 0 0 1 7.5 7.5"/></svg>';
  }
  row.append(title, indicator); panel.append(row);
  if (state.stage === 'failed') {
    const detail = document.createElement('p');
    detail.textContent = '暂时无法完成准备，请重试。';
    panel.append(detail);
  }
  if (needsAction) {
    const retry = document.createElement('button');
    const loginRequired = state.stage === 'credentials_required';
    retry.textContent = loginRequired ? '登录' : '重试';
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
 ['balance','剩余余额','<path d="M4 6h12a2 2 0 0 1 2 2v8H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h11v2M18 10h-5v4h5"/><path d="M15 12h.01"/>'],
 ['spend','近30天消费','<rect x="3" y="4" width="14" height="14" rx="2"/><path d="M6 2v4m8-4v4M3 8h14m-11 6 3-3 2 2 3-3"/>'],
 ['cpu','CPU','<rect x="5" y="5" width="10" height="10" rx="2"/><rect x="8" y="8" width="4" height="4" rx=".5"/><path d="M7 2v3m6-3v3M7 15v3m6-3v3M2 7h3m-3 6h3m10-6h3m-3 6h3"/>'],
 ['memory','内存','<rect x="2" y="5" width="16" height="10" rx="2"/><path d="M5 8v4m3-4v4m4-4v4m3-4v4M5 15v2m5-2v2m5-2v2"/>'],
 ['disk','磁盘','<path d="m4 4-2 8v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4l-2-8Z"/><path d="M2 12h16m-4 3h.01m-3 0h.01"/>']
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
  if(loading&&!valid){value.classList.add('setting-skeleton','billing-skeleton-value');value.setAttribute('aria-label','正在加载…');}
  else value.textContent=valid?(money?new Intl.NumberFormat(document.documentElement.lang,{style:'currency',currency:payload.currency||'USD'}).format(raw/100):String(raw)):'—';
  row.append(title,value);
  if(money)content.append(row);else resources.append(row);
 }
 content.append(resources);
 const note=document.createElement('p');note.className='billing-footnote';note.textContent='近30天资源用量';content.append(note);
 const footer=document.createElement('div');footer.className='billing-footer';
 const status=document.createElement('span');status.className='billing-status';
 status.textContent=!loading?(payload.error||(payload.stale?'数据更新中，显示最近记录':'')):'';
 const retry=document.createElement('button');retry.type='button';retry.textContent='重试';
 retry.style.visibility=!loading&&(missing||payload.error||payload.stale)?'visible':'hidden';retry.disabled=loading;
 retry.onclick=()=>{window.renderBillingLoading();send('load-billing');};
 footer.append(status,retry);content.append(footer);
}
window.renderBillingLoading = () => renderBillingRows(lastBillingPayload,true);
window.renderBilling = payload => {lastBillingPayload=payload;renderBillingRows(payload,false);};

window.renderRouteResults = results => {
  routeResults = new Map(results.map(result => [result.agent, result]));
  window.renderAgents(detectedAgents);
};

window.renderModelCatalog = models => { gatewayModels = models; window.renderAgents(detectedAgents); };

window.renderModelSaving = busy => {
  modelSaving=busy;
  const notice=document.querySelector('#notice');
  notice.hidden=!busy;
  if(busy)notice.textContent='正在保存模型选择…';
  window.renderAgents(detectedAgents);
};

document.querySelector('#clear-trust').onclick = () => {
  document.querySelector('#clear-trust').disabled=true;
  send('clear-trust');
};
window.renderTrustResult = success => {
  document.querySelector('#clear-trust').disabled=false;
  const notice=document.querySelector('#trust-result');notice.hidden=true;notice.textContent='';
  showFeedbackToast(success?'已撤销本机自动授权':'撤销失败，请重试');
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
 const labels={working:'正在工作',blocked:'等待输入',idle:'空闲',error:'会话出错'};
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
   return {agent:terminal.labels?.["fennec.agent"] || terminal.agent,name:readableName(terminal.title) || readableName(terminal.name) || '未命名会话', group, status:raw || '',displayStatus,address:terminal.address,parent:typeof terminal.parent==='string'?terminal.parent:null,sessionID,canStop:terminal.state==='live' && /^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$/.test(sessionID||'')};
 });
 window.renderRuntimes(rows,'');
};

window.renderLaunchStatus = (agent,message) => {
 for(const status of document.querySelectorAll('[data-launch-agent]')){
  if(status.dataset.launchAgent===agent)status.textContent=message;
 }
};

window.renderSessionRefreshError = () => {
 const note=document.querySelector('#runtime-message');
 note.replaceChildren();
 note.hidden=false;
 note.className='runtime-note';
 note.textContent='会话状态暂不可用，请刷新重试';
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
 const note=document.querySelector('#runtime-message');note.hidden=false;note.textContent='会话操作未确认，请刷新后重试';
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
 if(!result){output.textContent='';note.textContent='正在加载…';return;}
 if(result.error){output.textContent='';note.textContent='会话读取失败，请重试';return;}
 output.textContent=result.screen?.text || '';
 note.textContent=result.screen?.complete===false?'显示当前屏幕，历史记录未完整加载':'';
};
document.querySelector('#session-back').onclick=()=>tab('sessions');

document.querySelector('#session-input-form').onsubmit = event => {
 event.preventDefault();
 const input=document.querySelector('#session-input');
 if(!activeSession||sessionSending||!input.value.trim())return;
 sessionSending=true;document.querySelector('#session-send').disabled=true;
 document.querySelector('#session-send-status').textContent='正在提交…';
 send('send-session:'+JSON.stringify({...activeSession,text:input.value}));
};
window.renderSessionSent = (sessionID,success) => {
 sessionSending=false;document.querySelector('#session-send').disabled=false;
 if(!sessionDetailOpen||activeSession?.session_id!==sessionID)return;
 document.querySelector('#session-send-status').textContent=success?'已提交':'提交未确认，请查看会话后重试';
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
