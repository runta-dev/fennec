const pages = document.querySelectorAll('.page');
const navigation = document.querySelectorAll('nav button');

for (const button of navigation) {
  button.addEventListener('click', () => {
    for (const page of pages) page.hidden = page.id !== button.dataset.page;
    for (const item of navigation) {
      item.classList.toggle('active', item === button);
      if (item === button) item.setAttribute('aria-current', 'page');
      else item.removeAttribute('aria-current');
    }
  });
}

try {
  document.documentElement.dataset.theme = localStorage.getItem('fennec-theme') || 'light';
} catch {}
document.querySelector('#theme').addEventListener('click', () => {
  const theme = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = theme;
  try { localStorage.setItem('fennec-theme', theme); } catch {}
});

async function refresh() {
  const error = document.querySelector('#error');
  const refreshButton = document.querySelector('#refresh');
  refreshButton.disabled = true;
  error.hidden = true;
  try {
    const response = await fetch('/api/status');
    if (!response.ok) throw new Error(`Status request failed (${response.status})`);
    const status = await response.json();
    const list = document.querySelector('#agent-list');
    list.replaceChildren();
    for (const agent of status.agents) {
      const card = document.createElement('article');
      card.className = 'agent-card';
      const icon = document.createElement('div');
      icon.className = 'agent-icon';
      icon.textContent = agent.id === 'claude' ? '✳' : '>_';
      const title = document.createElement('h3');
      title.textContent = agent.name;
      const state = document.createElement('p');
      state.textContent = agent.installed ? '✓ Detected on this device' : 'Not found on daemon PATH';
      const action = document.createElement('button');
      action.textContent = 'Cloud launch · coming next';
      action.disabled = true;
      card.append(icon, title, state, action);
      list.append(card);
    }
    const checks = document.querySelector('#checks');
    checks.replaceChildren();
    for (const check of status.checks) {
      const item = document.createElement('li');
      item.textContent = `Not verified — ${check.label}`;
      checks.append(item);
    }
  } catch (failure) {
    document.querySelector('#agent-list').replaceChildren();
    document.querySelector('#checks').replaceChildren();
    error.textContent = `Cannot reach the local daemon. ${failure.message}`;
    error.hidden = false;
  } finally {
    refreshButton.disabled = false;
  }
}

document.querySelector('#refresh').addEventListener('click', refresh);
refresh();
