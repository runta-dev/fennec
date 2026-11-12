// Shared in-panel menu surface. Native selects remain hidden state controls only.
let currentMenu = null;
// Codex desktop permission icons, copied verbatim (20x20, filled with currentColor).
const SHIELD_PATH = 'M9.06543 1.95123C9.66107 1.69076 10.3389 1.69071 10.9346 1.95123L15.9346 4.13873C16.7832 4.51008 17.3311 5.34917 17.3311 6.27545V10.5528C17.3309 14.6017 14.0489 17.8847 10 17.8848C5.95108 17.8846 2.66813 14.6017 2.66797 10.5528V6.27545C2.66797 5.34924 3.21695 4.51012 4.06543 4.13873L9.06543 1.95123ZM10.4014 3.16998C10.1456 3.05814 9.85444 3.05819 9.59863 3.16998L4.59863 5.35748C4.23427 5.51708 3.99805 5.87764 3.99805 6.27545V10.5528C3.99821 13.8671 6.68563 16.5546 10 16.5547C13.3144 16.5546 16.0008 13.8671 16.001 10.5528V6.27545C16.001 5.87756 15.7658 5.51703 15.4014 5.35748L10.4014 3.16998Z';
const PERMISSION_ICONS = {
  ask: '<svg viewBox="0 0 20 20" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M12.6683 4.16699C12.6683 3.84391 12.4065 3.58203 12.0834 3.58203C11.7603 3.58203 11.4984 3.84391 11.4984 4.16699V7.91699L11.4847 8.05078C11.4227 8.35375 11.1547 8.58203 10.8334 8.58203C10.4662 8.58203 10.1685 8.28411 10.1683 7.91699V3.75C10.1683 3.42691 9.90646 3.16504 9.58337 3.16504C9.26029 3.16504 8.99841 3.42691 8.99841 3.75V7.91699C8.99824 8.28411 8.70053 8.58203 8.33337 8.58203C7.96621 8.58203 7.66851 8.28411 7.66833 7.91699V5C7.66833 4.67691 7.40646 4.41504 7.08337 4.41504C6.76029 4.41504 6.49841 4.67691 6.49841 5V9.30371C6.53326 9.3429 6.56715 9.38359 6.59998 9.42578L8.02478 11.2588C8.25005 11.5486 8.19821 11.9659 7.90857 12.1914C7.6187 12.4169 7.20048 12.365 6.97498 12.0752L5.55017 10.2432C5.15812 9.7391 4.41813 9.73637 4.01501 10.1924C4.04396 10.426 4.11486 10.8323 4.25525 11.3486C4.44664 12.0525 4.75404 12.9113 5.21619 13.7383C6.14103 15.3931 7.62465 16.835 10.0004 16.835C12.8545 16.8348 15.1682 14.5211 15.1683 11.667V6.25C15.1683 5.92691 14.9065 5.66504 14.5834 5.66504C14.2603 5.66504 13.9984 5.92691 13.9984 6.25V9.16699C13.9982 9.53411 13.7005 9.83203 13.3334 9.83203C12.9662 9.83203 12.6685 9.53411 12.6683 9.16699V4.16699ZM13.9984 4.42578C14.1828 4.36671 14.3794 4.33496 14.5834 4.33496C15.641 4.33496 16.4984 5.19237 16.4984 6.25V11.667C16.4982 15.2557 13.589 18.1649 10.0004 18.165C6.95953 18.165 5.10939 16.2734 4.05505 14.3867C3.52774 13.4431 3.1843 12.4787 2.97205 11.6982C2.76447 10.9349 2.66834 10.2954 2.66833 10C2.66833 9.87959 2.70117 9.76148 2.76306 9.6582C3.28988 8.78018 4.26555 8.40372 5.16833 8.56152V5C5.16833 3.94237 6.02575 3.08496 7.08337 3.08496C7.31706 3.08496 7.54039 3.12845 7.74744 3.20508C7.98218 2.41297 8.7151 1.83496 9.58337 1.83496C10.1836 1.83496 10.7186 2.11176 11.0697 2.54395C11.3639 2.35978 11.7107 2.25195 12.0834 2.25195C13.141 2.25195 13.9984 3.10937 13.9984 4.16699V4.42578Z" fill="currentColor"/></svg>',
  auto: '<svg viewBox="0 0 20 20" fill="none" xmlns="http://www.w3.org/2000/svg"><path fill-rule="evenodd" clip-rule="evenodd" d="' + SHIELD_PATH + '" fill="currentColor"/><path d="M13.4678 11.4318L13.333 11.4182H10.833C10.466 11.4183 10.1682 11.7162 10.168 12.0832C10.168 12.4504 10.4659 12.7481 10.833 12.7482H13.333L13.4678 12.7346C13.7706 12.6724 13.9981 12.4044 13.9981 12.0832C13.9979 11.7621 13.7706 11.494 13.4678 11.4318Z" fill="currentColor"/><path d="M7.65336 12.426C7.46431 12.7406 7.05607 12.8424 6.74125 12.6535C6.42646 12.4646 6.32395 12.0563 6.51274 11.7414L7.55668 10.0002L6.51274 8.25899C6.32395 7.94412 6.42646 7.53583 6.74125 7.34688C7.05607 7.15799 7.46431 7.25975 7.65336 7.57442L8.90336 9.6584C9.0296 9.86893 9.0296 10.1315 8.90336 10.342L7.65336 12.426Z" fill="currentColor"/></svg>',
  full: '<svg viewBox="0 0 20 20" fill="none" xmlns="http://www.w3.org/2000/svg"><path fill-rule="evenodd" clip-rule="evenodd" d="' + SHIELD_PATH + '" fill="currentColor"/><path d="M10.8883 13.1116C10.8883 13.6025 10.4903 14.0005 9.99936 14.0005C9.50844 14.0005 9.11047 13.6025 9.11047 13.1116C9.11047 12.6207 9.50844 12.2227 9.99936 12.2227C10.4903 12.2227 10.8883 12.6207 10.8883 13.1116Z" fill="currentColor"/><path d="M10.5169 10.8949L11.1135 7.31519C11.2283 6.62672 10.6974 6 9.99941 6C9.30145 6 8.77053 6.62672 8.88528 7.31519L9.4819 10.8949C9.52406 11.1479 9.74294 11.3333 9.99941 11.3333C10.2559 11.3333 10.4748 11.1479 10.5169 10.8949Z" fill="currentColor"/></svg>'
};
const CHECK_SVG = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="m3.5 8.5 3 3 6-7"/></svg>';
// Option copy as an i18n token so the locale observer keeps menus and triggers in the active language.
function optionCopy(option) {
  return option.dataset?.i18n ? '[[i18n:' + option.dataset.i18n + ']]' : option.textContent;
}
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
  const title = select.dataset?.menuTitle || '';
  if (title) {
    const header = document.createElement('div');
    header.className = 'choice-menu-title';
    header.setAttribute('role', 'presentation');
    header.textContent = title;
    menu.append(header);
  }
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
    text.textContent = optionCopy(option);
    const iconSrc = option.dataset?.icon || '';
    const iconSvg = option.dataset?.iconSvg || '';
    const account = option.dataset?.account || '';
    const description = option.dataset?.description || '';
    const mark = document.createElement('span');
    mark.className = 'choice-check';
    mark.setAttribute('aria-hidden', 'true');
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
        const detail = document.createElement('span');
        detail.className = 'choice-item-description';
        detail.textContent = description;
        copy.append(text, detail);
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
      if (select.id === 'permission-mode') { if (option.selected) mark.innerHTML = CHECK_SVG; }
      else mark.textContent = option.selected ? '✓' : '';
      item.append(main, mark);
    } else {
      mark.textContent = option.selected ? '✓' : '';
      item.append(text, mark);
    }
    item.onclick = () => {
      select.value = option.value;
      select.dispatchEvent(new Event('change', { bubbles: true }));
      if (optimistic) (trigger.querySelector('.field-value') || trigger.querySelector('span')).textContent = optionCopy(select.selectedOptions[0]);
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
  // Anchor the menu's right edge to the trigger's right edge so a width
  // change after measurement cannot shift it; clamp to the left edge only when
  // the menu would overflow it.
  menu.style.right = Math.max(10, innerWidth - rect.right) + 'px';
  if (menu.getBoundingClientRect().left < 10) { menu.style.right = 'auto'; menu.style.left = '10px'; }
  menu.style.top = (rect.bottom + bounds.height + 8 <= innerHeight ? rect.bottom + 4 : Math.max(8, rect.top - bounds.height - 4)) + 'px';
  menu.querySelector('[aria-selected=true]')?.focus();
}
for (const select of document.querySelectorAll('.preference-row select, select[data-choice-menu]')) {
  const permission = select.id === 'permission-mode';
  select.hidden = true;
  if (permission) for (const option of select.options) option.dataset.iconSvg = PERMISSION_ICONS[option.value] || '';
  const trigger = document.createElement('button');
  trigger.className = 'choice-trigger' + (permission ? ' permission-trigger' : '') + (select.dataset?.triggerClass ? ' ' + select.dataset.triggerClass : '');
  if (permission) trigger.id = 'permission-trigger';
  trigger.type = 'button';
  trigger.setAttribute('aria-haspopup', 'listbox');
  trigger.setAttribute('aria-expanded', 'false');
  trigger.innerHTML = '<span class="field-value"></span><svg class="choice-chevron" viewBox="0 0 16 16" aria-hidden="true"><path d="m5 6 3 3 3-3"/></svg>';
  const value = trigger.querySelector('.field-value');
  value.textContent = optionCopy(select.selectedOptions[0]);
  // Programmatic value changes (e.g. state reset) keep the trigger label in sync.
  select.addEventListener('change', () => { if (select.selectedOptions[0]) value.textContent = optionCopy(select.selectedOptions[0]); });
  trigger.onclick = () => openChoiceMenu(select, trigger);
  trigger.onkeydown = event => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault(); openChoiceMenu(select, trigger);
    }
  };
  select.after(trigger);
  if (permission) {
    const icon = document.createElement('span');
    icon.className = 'choice-item-icon-wrap';
    icon.setAttribute('aria-hidden', 'true');
    const syncTrigger = () => {
      const selected = select.selectedOptions[0];
      if (!selected) return;
      icon.innerHTML = selected.dataset.iconSvg || '';
      trigger.classList.toggle('is-full', select.value === 'full');
      value.textContent = optionCopy(selected);
    };
    trigger.prepend(icon);
    syncTrigger();
    select.addEventListener('change', syncTrigger);
    // Backend state assigns select.value without a change event.
    globalThis.syncPermissionTrigger = syncTrigger;
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
