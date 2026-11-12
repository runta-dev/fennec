// Shared in-panel menu surface. Native selects remain hidden state controls only.
let currentMenu = null;
function closeMenu(restoreFocus = false) {
  if (!currentMenu) return;
  const { trigger, menu } = currentMenu;
  menu.remove();
  trigger.setAttribute('aria-expanded', 'false');
  currentMenu = null;
  if (restoreFocus) trigger.focus();
}
function openChoiceMenu(select, trigger, { optimistic = true } = {}) {
  if (currentMenu?.trigger === trigger) { closeMenu(); return; }
  closeMenu();
  document.querySelector('#user-menu').hidden = true;
  const menu = document.createElement('div');
  menu.className = 'choice-menu';
  if (select.id === 'permission-mode') menu.id = 'permission-menu';
  menu.setAttribute('role', 'listbox');
  menu.setAttribute('aria-label', select.labels?.[0]?.textContent || '');
  let lastGroup = '';
  for (const option of select.options) {
    const group = option.dataset?.group || '';
    if (group && group !== lastGroup) {
      lastGroup = group;
      const header = document.createElement('div');
      header.className = 'choice-group-header';
      header.setAttribute('role', 'presentation');
      header.textContent = group;
      menu.append(header);
    }
    const item = document.createElement('button');
    item.type = 'button';
    item.dataset.value = option.value;
    item.setAttribute('role', 'option');
    item.setAttribute('aria-selected', String(option.selected));
    const text = document.createElement('span');
    text.className = 'choice-item-label';
    text.textContent = option.textContent;
    const iconSrc = option.dataset?.icon || '';
    const iconSvg = option.dataset?.iconSvg || '';
    const account = option.dataset?.account || '';
    const description = option.dataset?.description || '';
    if (iconSrc || iconSvg || account || description) {
      item.className = 'choice-item-rich';
      const main = document.createElement('span');
      main.className = 'choice-item-main';
      if (iconSvg) {
        const wrap = document.createElement('span');
        wrap.className = 'choice-item-icon-wrap';
        wrap.setAttribute('aria-hidden', 'true');
        wrap.innerHTML = iconSvg;
        main.append(wrap);
      } else if (iconSrc) {
        const img = document.createElement('img');
        img.className = 'choice-item-icon' + (option.dataset?.mono === '1' ? ' is-mono' : '');
        img.src = iconSrc;
        img.alt = '';
        main.append(img);
      }
      if (description) {
        const copy = document.createElement('span');
        copy.className = 'choice-item-copy';
        const label = text;
        const detail = document.createElement('span');
        detail.className = 'choice-item-description';
        detail.textContent = description;
        copy.append(label, detail);
        main.append(copy);
      } else {
        main.append(text);
      }
      if (account) {
        const meta = document.createElement('span');
        meta.className = 'choice-item-account';
        meta.textContent = account;
        main.append(meta);
      }
      const mark = document.createElement('span');
      mark.className = 'choice-check';
      mark.textContent = option.selected ? '✓' : '';
      mark.setAttribute('aria-hidden', 'true');
      item.append(main, mark);
    } else {
      const mark = document.createElement('span');
      mark.className = 'choice-check';
      mark.textContent = option.selected ? '✓' : '';
      mark.setAttribute('aria-hidden', 'true');
      item.append(text, mark);
    }
    item.onclick = () => {
      select.value = option.value;
      select.dispatchEvent(new Event('change', { bubbles: true }));
      if (optimistic) (trigger.querySelector('.field-value') || trigger.querySelector('span')).textContent = select.selectedOptions[0].textContent;
      closeMenu(true);
    };
    menu.append(item);
  }
  document.body.append(menu);
  currentMenu = {trigger, menu};
  trigger.setAttribute('aria-expanded', 'true');
  const rect = trigger.getBoundingClientRect();
  menu.style.minWidth = Math.max(rect.width, 170) + 'px';
  const bounds = menu.getBoundingClientRect();
  menu.style.left = Math.max(10, Math.min(rect.right - bounds.width, innerWidth - bounds.width - 10)) + 'px';
  menu.style.top = (rect.bottom + bounds.height + 8 <= innerHeight ? rect.bottom + 4 : Math.max(8, rect.top - bounds.height - 4)) + 'px';
  menu.querySelector('[aria-selected=true]')?.focus();
}
for (const select of document.querySelectorAll('.preference-row select')) {
  select.hidden = true;
  const trigger = document.createElement('button');
  trigger.className = 'choice-trigger' + (select.id === 'permission-mode' ? ' permission-trigger' : '');
  if (select.id === 'permission-mode') trigger.id = 'permission-trigger';
  trigger.type = 'button';
  trigger.setAttribute('aria-haspopup', 'listbox');
  trigger.setAttribute('aria-expanded', 'false');
  trigger.innerHTML = '<span></span><svg viewBox="0 0 16 16" aria-hidden="true"><path d="m5 6 3 3 3-3"/></svg>';
  (trigger.querySelector('.field-value') || trigger.querySelector('span')).textContent = select.selectedOptions[0].textContent;
  trigger.onclick = () => openChoiceMenu(select, trigger);
  trigger.onkeydown = event => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault(); openChoiceMenu(select, trigger);
    }
  };
  select.after(trigger);
  if (select.id === 'permission-mode') {
    const icon = document.createElement('span');
    icon.className = 'choice-item-icon-wrap';
    icon.setAttribute('aria-hidden', 'true');
    const setTriggerState = () => {
      const selected = select.selectedOptions[0];
      icon.innerHTML = (selected.dataset.iconSvg || '').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&amp;/g, '&');
      trigger.classList.toggle('is-full', select.value === 'full');
      (trigger.querySelector('.field-value') || trigger.querySelector('span')).textContent = selected.textContent;
    };
    trigger.prepend(icon);
    setTriggerState();
    select.addEventListener('change', setTriggerState);
  }
  // Labels focus the custom trigger rather than an invisible native control.
  for (const label of select.labels) label.onclick = () => trigger.focus();
}
document.addEventListener('pointerdown', event => {
  if (currentMenu && !currentMenu.menu.contains(event.target) && !currentMenu.trigger.contains(event.target)) closeMenu();
});
document.addEventListener('keydown', event => {
  if (!currentMenu) return;
  if (event.key === 'Escape') { event.preventDefault(); closeMenu(true); return; }
  if (event.key === 'Tab') { closeMenu(); return; }
  const items = [...currentMenu.menu.querySelectorAll('button')];
  const index = items.indexOf(document.activeElement);
  if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
    event.preventDefault();
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
    items[next].focus();
  }
});
