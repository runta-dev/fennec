// UI copy is addressed by stable message IDs. User data is never used as a key.
const messages = window.FennecMessages;
const languageSelect = document.querySelector('#language');
const supportedLanguages = ['zh-CN', 'zh-TW', 'en-US', 'en-GB', 'ja', 'ko'];
let language = 'zh-CN';
try { language = localStorage.getItem('language') || language; } catch (_) {}
if (!supportedLanguages.includes(language)) language = 'zh-CN';
languageSelect.value = language;
const textSources = new WeakMap();
const attrSources = new WeakMap();
const messageToken = /\[\[i18n:([a-zA-Z][a-zA-Z0-9_.]*)\]\]/g;
function messageText(key, locale = language) {
  if (!Object.prototype.hasOwnProperty.call(messages,key)) return null;
  const entry = messages[key];
  return entry[locale] ?? (locale === 'en-GB' ? entry['en-US'] : locale === 'zh-TW' ? entry['zh-CN'] : undefined) ?? entry['en-US'];
}
function uiCopy(text, locale) {
  const separator = locale === 'ja' ? '\u3001' : locale?.startsWith('zh') ? '\uff0c' : ' · ';
  return text.replace(/\.{3}/g,'…').replace(/[\u3002\uff0e](?=\S)/g,separator).replace(/[\u3002\uff0e]$/,'').replace(/\.\s+(?=\S)/g,separator).replace(/\.$/,'');
}
function translate(source) {
  return source.replace(messageToken, (token, key) => {const text=messageText(key);return text===null?token:uiCopy(text,language);});
}
function keyedSource(value, key) {
  if (!key || value.includes('[[i18n:')) return value;
  const trimmed = value.trim();
  if (!Object.prototype.hasOwnProperty.call(messages,key)) return value;
  // Cloned controls can already contain another locale's rendered copy.
  const known=Object.entries(messages[key]).some(([locale,text])=>trimmed===text.trim()||trimmed===uiCopy(text,locale).trim());
  if (!known) return value;
  return value.replace(trimmed, '[[i18n:' + key + ']]');
}
function localize() {
  observer.disconnect();
  document.documentElement.lang = language;
  for (const option of languageSelect.querySelectorAll('option[data-i18n]')) {
    option.textContent = messageText(option.dataset.i18n, option.value);
  }
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node;
  while ((node = walker.nextNode())) {
    if (node.parentElement.closest('script,style,#session-output,#session-terminal,#session-detail-title,#language,#runtime-list,.approval-card pre,.header-login.account-link:not([data-i18n]),.billing-account-name:not([data-i18n]),.preference-account,.native-account-name[data-identity],.native-subscription')) continue;
    const previous = textSources.get(node);
    const source = previous && node.nodeValue === previous.output ? previous.source : keyedSource(node.nodeValue, node.parentElement.getAttribute('data-i18n'));
    const output = translate(source);
    if (output !== node.nodeValue) node.nodeValue = output;
    textSources.set(node, {source, output});
  }
  document.querySelectorAll('[aria-label],[placeholder],[title]').forEach(element => {
    if (element.closest('#session-terminal,#session-output,.session-title:not([data-i18n]),.header-login.account-link:not([data-i18n]),.billing-account-name:not([data-i18n]),.native-account-name[data-identity]')) return;
    const records = attrSources.get(element) || {};
    for (const attribute of ['aria-label','placeholder','title']) {
      if (!element.hasAttribute(attribute)) continue;
      const value = element.getAttribute(attribute);
      const previous = records[attribute];
      const source = previous && value === previous.output ? previous.source : keyedSource(value, element.getAttribute('data-i18n-' + attribute));
      const output = translate(source);
      if (output !== value) element.setAttribute(attribute, output);
      records[attribute] = {source,output};
    }
    attrSources.set(element, records);
  });
  // Runtime rows contain user-authored titles. Only explicit UI copy is localized.
  document.querySelectorAll('.runtime-status,.session-copy-label,.session-help-title,.session-help-description,.session-title[data-i18n]').forEach(element => {
    const previous = textSources.get(element);
    const source = previous && element.textContent === previous.output ? previous.source : keyedSource(element.textContent, element.getAttribute('data-i18n'));
    const output = translate(source);
    element.textContent = output;
    textSources.set(element, {source, output});
  });
  observer.observe(document.body, {subtree:true, childList:true, characterData:true, attributes:true, attributeFilter:['aria-label','placeholder','title','data-i18n']});
}
function needsLocalization(records) {
  return records.some(record => {
    const element = record.target.nodeType === 1 ? record.target : record.target.parentElement;
    return !element?.closest('#session-terminal,#session-output');
  });
}
const observer = new MutationObserver(records => {
  if (needsLocalization(records)) localize();
});
languageSelect.onchange = () => {
  language = languageSelect.value;
  try { localStorage.setItem('language', language); } catch (_) {}
  localize();
  window.webkit?.messageHandlers?.fennec?.postMessage('language:' + language);
};
localize();
window.webkit?.messageHandlers?.fennec?.postMessage('native-language:' + language);
