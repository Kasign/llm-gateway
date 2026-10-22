import { $, esc } from '../core/dom.js';
import {
  api,
  apiPublic,
  getSessionToken,
  setSessionToken,
} from '../core/api.js';

let currentUser = null;

export function authUser() {
  return currentUser;
}

function setAuthScreenVisible(show) {
  const screen = $('authScreen');
  const app = $('appShell');
  if (screen) {
    screen.classList.toggle('show', show);
    screen.setAttribute('aria-hidden', show ? 'false' : 'true');
  }
  if (app) app.hidden = show;
  document.body.classList.toggle('auth-locked', show);
}

export function forceLoginScreen() {
  currentUser = null;
  $('sidebarAccountBtn')?.classList.remove('active');
  setAuthScreenVisible(true);
}

function updateAuthChrome() {
  setAuthScreenVisible(!getSessionToken());
}

export function openAccountSettings() {
  if (!getSessionToken()) {
    forceLoginScreen();
    return;
  }
  document.querySelectorAll('.nav button').forEach((x) => x.classList.remove('active'));
  document.querySelectorAll('.panel').forEach((x) => x.classList.remove('active'));
  const panel = $('account');
  if (panel) panel.classList.add('active');
  $('sidebarAccountBtn')?.classList.add('active');
  if (globalThis.dashboardPoll) globalThis.dashboardPoll.stop();
  const gm = $('globalMsg');
  if (gm) gm.hidden = false;
  if (globalThis.syncTopbarRefresh) globalThis.syncTopbarRefresh();
  loadAccountSettings().catch(() => {});
}

function bindAccountPageActions() {
  $('btnAccountLogout')?.addEventListener('click', (e) => {
    e.preventDefault();
    logout();
  });
}

export async function initAuth() {
  bindAccountPageActions();
  if (getSessionToken()) {
    try {
      const j = await api('/api/auth/me');
      currentUser = j.user || null;
    } catch {
      setSessionToken('');
      currentUser = null;
    }
  }
  updateAuthChrome();
}

export async function submitLogin(ev) {
  ev?.preventDefault?.();
  const msg = $('authMsg');
  const username = ($('authLoginUser')?.value || '').trim();
  const password = $('authLoginPass')?.value || '';
  if (!username || !password) {
    if (msg) msg.innerHTML = '<span class="err">请填写用户名和密码</span>';
    return;
  }
  if (msg) msg.innerHTML = '';
  const btn = $('authLoginBtn');
  if (btn) btn.disabled = true;
  try {
    const j = await apiPublic('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    });
    setSessionToken(j.token);
    currentUser = j.user || null;
    if ($('authLoginPass')) $('authLoginPass').value = '';
    $('sidebarAccountBtn')?.classList.remove('active');
    updateAuthChrome();
    if (globalThis.refreshAll) globalThis.refreshAll();
  } catch (e) {
    if (msg) msg.innerHTML = '<span class="err">' + esc(e.message) + '</span>';
  } finally {
    if (btn) btn.disabled = false;
  }
}

/** 清除 Web 会话并回到全屏登录页（与是否保存 API 密钥无关）。 */
export async function logout() {
  try {
    if (getSessionToken()) {
      await api('/api/auth/logout', { method: 'POST' });
    }
  } catch {
    /* ignore */
  }
  const { adminStore } = await import('./admin-store.js');
  adminStore.reset();
  setSessionToken('');
  currentUser = null;
  $('sidebarAccountBtn')?.classList.remove('active');
  document.querySelectorAll('.panel').forEach((x) => x.classList.remove('active'));
  $('dashboard')?.classList.add('active');
  document.querySelector('.nav button[data-tab="dashboard"]')?.classList.add('active');
  forceLoginScreen();
}

export function onAdminKeySaved() {
  /* 保存 API 密钥不替代 Web 登录 */
}

export async function loadAccountSettings() {
  const cur = $('accountCurrent');
  const msg = $('accountMsg');
  const logoutBtn = $('btnAccountLogout');
  if (msg) msg.innerHTML = '';
  if (!getSessionToken()) {
    if (cur) cur.textContent = '请使用 Web 登录后再修改账号（仅用 API 密钥无法在此改密）。';
    if (logoutBtn) logoutBtn.hidden = true;
    return;
  }
  if (logoutBtn) logoutBtn.hidden = false;
  try {
    const j = await api('/api/auth/credentials');
    if (cur) {
      cur.textContent = `当前用户名：${j.username || '—'}${j.stored_in_db ? '（已保存到数据库）' : '（初始值来自配置文件，保存后将写入数据库）'}`;
    }
    const u = $('acctNewUser');
    if (u && !u.value) u.placeholder = j.username || '';
  } catch (e) {
    if (cur) cur.textContent = e.message;
  }
}

export async function submitAccountCredentials(ev) {
  ev?.preventDefault?.();
  const msg = $('accountMsg');
  const newPass = $('acctNewPass')?.value || '';
  const newPass2 = $('acctNewPass2')?.value || '';
  const newUser = ($('acctNewUser')?.value || '').trim();
  if (newPass !== newPass2) {
    if (msg) msg.innerHTML = '<span class="err">两次新密码不一致</span>';
    return;
  }
  if (!newUser && !newPass) {
    if (msg) msg.innerHTML = '<span class="err">请填写新用户名或新密码</span>';
    return;
  }
  const body = { current_password: $('acctCurrentPass')?.value || '' };
  if (newUser) body.new_username = newUser;
  if (newPass) body.new_password = newPass;
  try {
    const j = await api('/api/auth/credentials', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    currentUser = j.user || currentUser;
    if ($('acctCurrentPass')) $('acctCurrentPass').value = '';
    if ($('acctNewUser')) $('acctNewUser').value = '';
    if ($('acctNewPass')) $('acctNewPass').value = '';
    if ($('acctNewPass2')) $('acctNewPass2').value = '';
    if (msg) msg.innerHTML = '<span class="ok">已更新，请牢记新凭据</span>';
    await loadAccountSettings();
  } catch (e) {
    if (msg) msg.innerHTML = '<span class="err">' + esc(e.message) + '</span>';
  }
}
