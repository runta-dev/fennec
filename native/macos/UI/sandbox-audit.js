// Local macOS Seatbelt sandbox controls and 7-day rolling security audit view.
(() => {
  const modeSelect = document.querySelector('#permission-mode');
  const auditClearBtn = document.querySelector('#audit-clear');
  const filterButtons = [...document.querySelectorAll('[data-audit-filter]')];
  const countAllEl = document.querySelector('#audit-count-all');
  const countBlockedEl = document.querySelector('#audit-count-blocked');
  const countApprovedEl = document.querySelector('#audit-count-approved');
  const auditListEl = document.querySelector('#audit-list');

  const t = token => (typeof translate === 'function' ? translate(token) : token);

  let sandboxState = {
    mode: 'auto',
    summary: { total_7d: 0, today_count: 0, blocked_7d: 0, retention_days: 7 }
  };
  let auditFilter = 'all';
  let auditEntries = [];
  const expandedIds = new Set();

  function normalizeMode(raw) {
    if (raw === 'ask' || raw === 'ask-for-approval' || raw === 'read-only') return 'ask';
    if (raw === 'full' || raw === 'full-access' || raw === 'danger-full-access') return 'full';
    return 'auto';
  }

  function compactHomePath(p) {
    const value = String(p || '');
    const match = value.match(/^\/Users\/[^/]+(\/.*)?$/);
    if (match) return '~' + (match[1] || '');
    return value;
  }

  function formatTimestamp(ms) {
    const ts = Number(ms || 0);
    if (!ts) return '';
    const now = Date.now();
    const diffSec = Math.max(0, Math.floor((now - ts) / 1000));
    if (diffSec < 60) return diffSec <= 5 ? 'now' : diffSec + 's';
    if (diffSec < 3600) return Math.floor(diffSec / 60) + 'm';
    if (diffSec < 86400) return Math.floor(diffSec / 3600) + 'h';
    const d = new Date(ts);
    const mm = String(d.getMonth() + 1).padStart(2, '0');
    const dd = String(d.getDate()).padStart(2, '0');
    const hh = String(d.getHours()).padStart(2, '0');
    const min = String(d.getMinutes()).padStart(2, '0');
    return mm + '-' + dd + ' ' + hh + ':' + min;
  }

  function cleanCommandDisplay(entry) {
    if (entry.kind === 'sftp') {
      return { text: t('[[i18n:ui.audit_kind_files]]'), code: false };
    }
    if (entry.kind === 'tcp_forward') {
      const raw = String(entry.command || '');
      const hostPort = raw.replace(/^(?:tcp-forward|TCP forwarding to)\s+/i, '').trim();
      const label = t('[[i18n:ui.audit_kind_port]]');
      return { text: hostPort ? label + ' ' + hostPort : label, code: false };
    }
    const rawCmd = String(entry.command || '').replace(/\[REDACTED\]/g, '••••').trim();
    return { text: rawCmd, code: true };
  }

  function inferBlockReason(entry) {
    if (entry.decision === 'user_denied') return '';
    const hay = (String(entry.command || '') + ' ' + String(entry.detail || '')).toLowerCase();
    if (/(\.ssh|\.gnupg|\.aws|\.azure|\.kube|keychains|gcloud|\.config\/fennec|\.config\/gh)/.test(hay)) {
      return t('[[i18n:ui.audit_reason_credentials]]');
    }
    if (/\.git\/(hooks|config)/.test(hay)) {
      return t('[[i18n:ui.audit_reason_git]]');
    }
    if (/(network-outbound|network is down|no route to host|curl |wget |nc )/.test(hay) && !entry.network_allowed) {
      return t('[[i18n:ui.audit_reason_network]]');
    }
    return t('[[i18n:ui.audit_reason_outside_workspace]]');
  }

  function decisionLabel(entry) {
    switch (entry.decision) {
      case 'sandbox_blocked':
        return t('[[i18n:ui.audit_decision_seatbelt_blocked]]');
      case 'user_denied':
        return t('[[i18n:ui.audit_decision_user_denied]]');
      case 'user_approved':
        return t('[[i18n:ui.audit_decision_user_approved]]');
      default:
        return t('[[i18n:ui.audit_decision_auto_sandbox]]');
    }
  }

  function modeLabel(mode) {
    const m = normalizeMode(mode);
    if (m === 'full') return t('[[i18n:ui.sandbox_mode_full]]');
    if (m === 'ask') return t('[[i18n:ui.sandbox_mode_ask]]');
    return t('[[i18n:ui.sandbox_mode_auto]]');
  }

  function drawSandboxSettings() {
    const mode = sandboxState.mode;
    if (modeSelect) modeSelect.value = mode;
    window.syncPermissionTrigger?.();
    drawSummaryBadge();
  }

  function drawSummaryBadge() {
    // Counts live on the filter tabs; retention is stated by the empty state.
    const total = Number(sandboxState.summary?.total_7d || 0);
    const blocked = Number(sandboxState.summary?.blocked_7d || 0);
    const approved = Math.max(0, total - blocked);
    if (countAllEl) countAllEl.textContent = String(total);
    if (countBlockedEl) countBlockedEl.textContent = String(blocked);
    if (countApprovedEl) countApprovedEl.textContent = String(approved);
  }

  function drawAuditView() {
    filterButtons.forEach(btn => {
      const selected = btn.dataset.auditFilter === auditFilter;
      btn.setAttribute('aria-selected', String(selected));
      btn.classList.toggle('selected', selected);
    });
    const total = Number(sandboxState.summary?.total_7d || 0);
    if (auditClearBtn) {
      auditClearBtn.disabled = total === 0 && auditEntries.length === 0;
    }
    if (!auditListEl) return;
    if (!Array.isArray(auditEntries) || auditEntries.length === 0) {
      const empty = document.createElement('div');
      empty.className = 'runtime-empty audit-empty';
      const icon = document.createElement('span');
      icon.className = 'runtime-empty-icon';
      icon.setAttribute('aria-hidden', 'true');
      icon.innerHTML = '<svg viewBox="0 0 24 24"><path d="m12 3 7.5 3.2v5.6c0 5-7.5 9.2-7.5 9.2S4.5 16.8 4.5 11.8V6.2Z"/><path d="m8.8 12.2 2.2 2.2 4.4-4.4"/></svg>';
      const text = document.createElement('span');
      text.textContent = auditFilter === 'blocked'
        ? t('[[i18n:ui.audit_empty_blocked]]')
        : t('[[i18n:ui.audit_empty_all]]');
      empty.append(icon, text);
      auditListEl.replaceChildren(empty);
      return;
    }

    const rows = auditEntries.map(entry => {
      const id = String(entry.id || entry.timestamp_ms || '');
      const blocked = Boolean(entry.blocked || entry.decision === 'sandbox_blocked' || entry.decision === 'user_denied');
      const titleInfo = cleanCommandDisplay(entry);
      const canExpand = titleInfo.code && titleInfo.text.length > 44;
      const isExpanded = canExpand && expandedIds.has(id);

      const item = document.createElement('div');
      item.className = 'audit-entry' + (blocked ? ' is-blocked' : '') + (isExpanded ? ' is-open' : '');

      const head = document.createElement(canExpand ? 'button' : 'div');
      if (canExpand) {
        head.type = 'button';
        head.setAttribute('aria-expanded', String(isExpanded));
      }
      head.className = 'audit-entry-head' + (canExpand ? ' is-expandable' : '');

      const statusIcon = document.createElement('span');
      statusIcon.className = 'audit-status-icon' + (blocked ? ' is-blocked' : '');
      statusIcon.setAttribute('aria-hidden', 'true');
      statusIcon.innerHTML = blocked
        ? '<svg viewBox="0 0 16 16"><path d="M8 1.8 13.4 4v3.9c0 3.4-5.4 6.3-5.4 6.3S2.6 11.3 2.6 7.9V4Z"/><path d="m6.1 6.1 3.8 3.8M9.9 6.1 6.1 9.9"/></svg>'
        : '<svg viewBox="0 0 16 16"><circle cx="8" cy="8" r="5.6"/><path d="m5.6 8.1 1.7 1.7 3.3-3.4"/></svg>';

      const body = document.createElement('div');
      body.className = 'audit-entry-body';

      const top = document.createElement('div');
      top.className = 'audit-entry-top';
      const cmd = document.createElement(titleInfo.code ? 'code' : 'span');
      cmd.className = titleInfo.code ? 'audit-command' : 'audit-action-title';
      cmd.textContent = titleInfo.text;
      const timeEl = document.createElement('span');
      timeEl.className = 'audit-time';
      timeEl.textContent = formatTimestamp(entry.timestamp_ms);
      top.append(cmd, timeEl);

      const meta = document.createElement('div');
      meta.className = 'audit-entry-meta';
      const summaryText = document.createElement('span');
      summaryText.className = 'audit-meta-text';
      const parts = [decisionLabel(entry)];
      if (blocked) {
        const reason = inferBlockReason(entry);
        if (reason) parts.push(reason);
      }
      if (entry.workdir) {
        parts.push(compactHomePath(entry.workdir));
      } else if (!blocked) {
        parts.push(modeLabel(entry.sandbox_mode));
      }
      summaryText.textContent = parts.join(' · ');
      meta.append(summaryText);

      body.append(top, meta);
      head.append(statusIcon, body);
      if (canExpand) {
        head.onclick = () => {
          if (expandedIds.has(id)) expandedIds.delete(id);
          else expandedIds.add(id);
          drawAuditView();
        };
      }
      item.append(head);

      if (isExpanded) {
        const detail = document.createElement('div');
        detail.className = 'audit-entry-detail';

        const fullCmd = document.createElement('pre');
        fullCmd.className = 'audit-detail-code';
        fullCmd.textContent = titleInfo.text;
        detail.append(fullCmd);
        item.append(detail);
      }
      return item;
    });
    auditListEl.replaceChildren(...rows);
  }

  function selectMode(nextMode) {
    const normalized = normalizeMode(nextMode);
    if (sandboxState.mode === normalized) return;
    sandboxState = { ...sandboxState, mode: normalized };
    drawSandboxSettings();
    send('settings:' + JSON.stringify({ permission_mode: normalized }));
  }

  function requestAuditLog() {
    send('audit-list:' + JSON.stringify({ filter: auditFilter, limit: 100 }));
  }

  if (modeSelect) {
    modeSelect.onchange = () => selectMode(modeSelect.value);
  }

  if (auditClearBtn) {
    auditClearBtn.onclick = () => {
      expandedIds.clear();
      send('audit-clear');
    };
  }

  filterButtons.forEach(btn => {
    btn.onclick = () => {
      const next = btn.dataset.auditFilter || 'all';
      if (auditFilter === next) return;
      auditFilter = next;
      drawAuditView();
      requestAuditLog();
    };
  });

  window.renderSandboxPreferences = prefs => {
    if (!prefs || typeof prefs !== 'object') return;
    sandboxState = {
      mode: prefs.permission_mode !== undefined
        ? normalizeMode(prefs.permission_mode)
        : prefs.sandbox_mode !== undefined ? normalizeMode(prefs.sandbox_mode) : sandboxState.mode,
      summary: prefs.audit_summary && typeof prefs.audit_summary === 'object' ? prefs.audit_summary : sandboxState.summary
    };
    drawSandboxSettings();
  };

  window.renderAuditLog = payload => {
    if (!payload || typeof payload !== 'object') return;
    if (payload.summary && typeof payload.summary === 'object') {
      sandboxState = { ...sandboxState, summary: payload.summary };
      drawSummaryBadge();
    }
    auditEntries = Array.isArray(payload.entries) ? payload.entries : [];
    drawAuditView();
  };

  window.onGovernanceTabShown = () => {
    requestAuditLog();
  };

  window.openSecurityAuditView = () => {
    if (typeof tab === 'function') tab('governance');
    else requestAuditLog();
  };

  window.rerenderSandboxAudit = () => {
    drawSandboxSettings();
    drawAuditView();
  };

  drawSandboxSettings();
  drawAuditView();
})();
