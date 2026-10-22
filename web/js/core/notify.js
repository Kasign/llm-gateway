import { $ } from './dom.js';

export function showToast(text, kind) {
  const host = $('toastHost');
  const msg = String(text || '').trim();
  if (!host || !msg) return;
  const el = document.createElement('div');
  el.className = 'toast' + (kind ? ' ' + kind : '');
  el.textContent = msg;
  host.appendChild(el);
  const timer = setTimeout(() => el.remove(), 2800);
  el.addEventListener('click', () => {
    clearTimeout(timer);
    el.remove();
  });
}

export function setGlobalMsg(html) {
  const gm = $('globalMsg');
  if (!gm) return;
  gm.innerHTML = html || '';
  const onDash = $('dashboard')?.classList.contains('active');
  gm.hidden = !html || onDash;
}
