// Browser-only presentation fixtures for the unmodified native WebView.
// This bridge has no network transport and never contacts Fennec services.
(() => {
  const emit = (action, value) => parent.postMessage({ source: 'fennec-demo', action, value }, location.origin);
  const agents = [
    ['codex-desktop', 'ChatGPT', 'chatgpt.svg'],
    ['codex', 'Codex', 'codex-color.svg'],
    ['claude', 'Claude Code', 'claudecode-color.svg'],
    ['pi', 'Pi', 'pi.svg'], ['kimi', 'Kimi Code', 'kimi-color.svg'],
    ['opencode', 'OpenCode', 'opencode.svg'], ['grok', 'Grok Build', 'xai.svg'],
    ['cursor', 'Cursor CLI', 'cursor.svg'],
    ['claude-desktop', 'Claude Desktop', 'claude-color.svg'],
    ['gemini', 'Gemini CLI', 'geminicli-color.svg'],
  ].map(([id, name, icon], i) => ({ id, name, icon, installed: true, cloudSupported: i < 8 }));
  let prefs = { magpie: false, theme: 'light', language: 'en-US', permission_mode: 'auto', initialized: true };
  let selected = agents.filter(a => a.cloudSupported).map(a => a.id);
  let entries = [], currentTab = 'agents', automatic = false;
  const billing = { currency: 'USD', balance: 3192, spend: 1808, cpu: '197.35 vCPU-h', memory: '480.85 GiB-h', disk: '3194.79 GiB-h' };
  const metrics = { ready: true, runtime_id: 'demo', cpu_count: 2, cpu_percent: 9.8, memory_used_bytes: 2576980377, memory_total_bytes: 17179869184, disk_used_bytes: 24051816857, disk_total_bytes: 33608119091, net_tx_bytes: 8504035246, net_rx_bytes: 12219181957, cpu_history: [7,7,9,15,9,7,7,8,12,10,18,19,23,10,7,8,7,8,17,14,8,7,10,9.8], mem_history: [2100,2100,2300,2300,2300,2300,2400,2400,2400,2300,2300,2600,2600,2600,2600,2600,2600,2600,2600,2600,2600,2500,2450,2458] };
  const takeover = () => window.renderTakeover({ agents: selected, activeAgents: selected, selectionKnown: true });
  const audit = (filter = 'all') => window.renderAuditLog({ summary: { total_7d: entries.length, blocked_7d: entries.filter(e => e.blocked).length }, entries: entries.filter(e => filter === 'all' || (filter === 'blocked' ? e.blocked : !e.blocked)) });
  window.webkit = { messageHandlers: { fennec: { postMessage(message) {
    if (typeof message !== 'string') return;
    const colon = message.indexOf(':'), action = colon < 0 ? message : message.slice(0, colon), raw = message.slice(colon + 1);
    let value = {}; try { value = JSON.parse(raw); } catch {}
    queueMicrotask(() => {
      if (action === 'visible-tab') { currentTab = raw; if (!automatic) emit('tab', raw); }
      if (action === 'load-billing') { window.renderBilling(billing); window.renderRuntimeMetrics(metrics); }
      if (action === 'audit-list') audit(value.filter);
      if (action === 'audit-clear') { entries = []; audit(); }
      if (action === 'settings') { prefs = { ...prefs, ...value }; window.renderPreferences(prefs); if (value.theme) emit('theme', value.theme); }
      if (action === 'toggle-takeover') { selected = selected.includes(raw) ? selected.filter(x => x !== raw) : [...selected, raw]; takeover(); }
      if (action === 'get-agent-account') window.renderNativeAccount({ agent: value.agent, location: value.location, status: 'signed_in', accounts: [{ provider: value.agent === 'claude' ? 'anthropic' : 'openai', email: 'alex@relay.dev', subscription: 'Pro', auth_type: 'oauth' }] });
      if (action === 'get-model') window.renderNativeModels({ agent: value.agent, model: '', models: [{ id: '', name: 'Default' }] });
      if (action === 'copy-attach') { emit('copy', value.session_id); }
      if (action === 'stop-session') emit('stop', value.session_id);
      if (action === 'browser-attach' || action === 'attach-session' || action === 'open-session' || action === 'open-terminal') emit('attach', value.session_id);
    });
  } } } };
  window.addEventListener('DOMContentLoaded', () => {
    window.renderPreferences(prefs);
    window.renderAuthorized();
    window.renderProfile('alex@relay.dev');
    window.renderAgents(agents);
    takeover();
    window.renderBilling(billing);
    window.renderRuntimeMetrics(metrics);
    document.addEventListener('pointerdown', () => emit('interact'), { capture: true });
    emit('ready');
  });
  window.addEventListener('message', event => {
    if (event.source !== parent || event.origin !== location.origin || event.data?.source !== 'fennec-demo-host') return;
    const { sessions, panel, activity } = event.data;
    if (sessions) window.renderHerdrSessions(sessions);
    if (activity) { entries = activity; audit(document.querySelector('#audit-filter').value); }
    if (panel && panel !== currentTab) {
      currentTab = panel;
      automatic = true;
      window.tab(panel);
      // Native tab() sends through the bridge synchronously; its callback runs
      // before this microtask, so automatic navigation never pauses playback.
      queueMicrotask(() => { automatic = false; });
    }
    if (event.data.copied) window.renderAttachCopyResult(event.data.copied, true);
  });
})();
