import { $, esc } from '../core/dom.js';
import { api, apiPublic, getSessionToken, setSessionToken, isAuthenticated, initAdminKey } from '../core/api.js';

let currentUser = null;
let passwordProtectionEnabled = true;
let passwordProtectionAtLoad = true;

const passwordEyeIcon = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12Z"></path><circle cx="12" cy="12" r="3"></circle></svg>';
const passwordEyeOffIcon = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="m3 3 18 18"></path><path d="M10.6 10.6a2 2 0 0 0 2.8 2.8"></path><path d="M9.9 5.2A10.9 10.9 0 0 1 12 5c6.4 0 10 7 10 7a17.5 17.5 0 0 1-3.1 3.8"></path><path d="M6.6 6.6C3.7 8.5 2 12 2 12s3.6 7 10 7c1 0 1.9-.2 2.8-.5"></path></svg>';

// 清空密码后同时恢复隐藏状态，避免下一次输入时意外以明文显示。
function resetPasswordField(id) {
  const input = $(id);
  if (!input) return;

  input.value = '';
  input.type = 'password';
  const button = input.closest('.password-input-wrap')?.querySelector('.password-toggle');
  if (!button) return;

  button.innerHTML = passwordEyeIcon;
  button.title = '显示明文';
  button.setAttribute('aria-label', '显示明文');
  button.setAttribute('aria-pressed', 'false');
}

export function authUser() { return currentUser; }

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
export function forceLoginScreen(message = '') {
  currentUser = null;
  setAuthScreenVisible(false);
  $('sidebarAccountBtn')?.classList.remove('active');
  document.querySelectorAll('.nav button').forEach((x) => x.classList.remove('active'));
  document.querySelectorAll('.panel').forEach((x) => x.classList.remove('active'));
  document.querySelector('.nav button[data-tab="keys"]')?.classList.add('active');
  $('keys')?.classList.add('active');
  if (globalThis.dashboardPoll) globalThis.dashboardPoll.stop();
  const status = $('adminKeyMsg');
  if (status && message) status.innerHTML = '<span class="warn">' + esc(message) + '</span>';
  if (globalThis.syncTopbarRefresh) globalThis.syncTopbarRefresh();
}
function updateAuthChrome() { setAuthScreenVisible(false); }

export function openAccountSettings() {
  if (!getSessionToken()) { forceLoginScreen(); return; }
  document.querySelectorAll('.nav button').forEach((x) => x.classList.remove('active'));
  document.querySelectorAll('.panel').forEach((x) => x.classList.remove('active'));
  $('account')?.classList.add('active');
  $('sidebarAccountBtn')?.classList.add('active');
  if (globalThis.dashboardPoll) globalThis.dashboardPoll.stop();
  const message = $('globalMsg');
  if (message) message.hidden = false;
  if (globalThis.syncTopbarRefresh) globalThis.syncTopbarRefresh();
  loadAccountSettings().catch(() => {});
}
function bindAccountPageActions() {
  $('btnAccountLogout')?.addEventListener('click', (event) => { event.preventDefault(); logout(); });
  $('accountForm')?.addEventListener('submit', (event) => { void submitAccountCredentials(event); });
  $('accountPasswordEnabled')?.addEventListener('change', updateCredentialFormPolicy);
  $('acctNewPass')?.addEventListener('input', updateCredentialFormPolicy);
}
function initPasswordToggles() {
  document.querySelectorAll('input[type="password"]').forEach((input) => {
    let wrapper = input.closest('.password-input-wrap');
    if (!wrapper) {
      wrapper = document.createElement('span');
      wrapper.className = 'password-input-wrap';
      input.parentNode.insertBefore(wrapper, input);
      wrapper.appendChild(input);
    }

    let button = wrapper.querySelector('.password-toggle');
    if (!button) {
      button = document.createElement('button');
      button.type = 'button';
      button.className = 'password-toggle';
      wrapper.appendChild(button);
    }
    button.innerHTML = passwordEyeIcon;
    button.title = '显示明文';
    button.setAttribute('aria-label', '显示明文');
    button.setAttribute('aria-pressed', 'false');
    if (button.dataset.passwordToggleBound === 'true') return;
    button.dataset.passwordToggleBound = 'true';
    button.addEventListener('click', () => {
      const show = input.type === 'password';
      input.type = show ? 'text' : 'password';
      button.innerHTML = show ? passwordEyeOffIcon : passwordEyeIcon;
      button.title = show ? '隐藏明文' : '显示明文';
      button.setAttribute('aria-label', button.title);
      button.setAttribute('aria-pressed', String(show));
    });
  });
}

function updateLoginModeUI() {
  const field = $('authPasswordField'), password = $('authLoginPass'), button = $('authLoginBtn');
  if (field) field.hidden = !passwordProtectionEnabled;
  if (password) password.required = passwordProtectionEnabled;
  if (button) button.textContent = passwordProtectionEnabled ? '登录' : '直接进入控制台';
}
async function createWebSession(password = '') {
  const response = await apiPublic('/api/auth/login', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password }),
  });
  setSessionToken(response.token);
  currentUser = response.user || { username: 'admin' };
  resetPasswordField('authLoginPass');
  $('sidebarAccountBtn')?.classList.remove('active');
  updateAuthChrome();
  if (globalThis.refreshAll) globalThis.refreshAll();
  return response;
}
async function submitAdminKey(event) {
  event?.preventDefault?.();
  const input = $('adminKey'), message = $('adminKeyMsg'), button = $('adminKeySaveButton');
  const candidate = input?.value.trim() || '';
  if (!candidate) {
    if (message) message.innerHTML = '<span class="err">请输入管理员 API Key</span>';
    return;
  }
  if (button) button.disabled = true;
  if (message) message.textContent = '正在验证管理员 API Key…';
  try {
    await apiPublic('/api/admin/keys', {
      headers: { 'X-Admin-Key': candidate },
      cache: 'no-store',
    });
    localStorage.setItem('llm_gateway_admin_key', candidate);
    if (message) message.innerHTML = '<span class="ok">验证成功，已保存在当前浏览器。</span>';
    globalThis.adminKeyStatus?.();
    if (globalThis.refreshAll) await globalThis.refreshAll();
  } catch (error) {
    if (message) message.innerHTML = '<span class="err">验证失败：' + esc(error.message) + '</span>';
  } finally {
    if (button) button.disabled = false;
  }
}

function clearAdminKey() {
  localStorage.removeItem('llm_gateway_admin_key');
  const input = $('adminKey'), message = $('adminKeyMsg');
  if (input) input.value = '';
  if (message) message.innerHTML = '<span class="muted">已清除本地保存的管理员 API Key。</span>';
  globalThis.adminKeyStatus?.();
  forceLoginScreen();
}

export async function initAuth() {
  setSessionToken('');
  initAdminKey();
  initPasswordToggles();
  $('adminKeyForm')?.addEventListener('submit', (event) => { void submitAdminKey(event); });
  $('adminKeyClearButton')?.addEventListener('click', (event) => {
    event.preventDefault();
    clearAdminKey();
  });
  setAuthScreenVisible(false);
  if (!isAuthenticated()) forceLoginScreen('请先输入服务端已配置的管理员 API Key。');
}
export async function submitLogin(event) {
  event?.preventDefault?.();
  const message = $('authMsg'), password = $('authLoginPass')?.value || '';
  if (passwordProtectionEnabled && !password) {
    if (message) message.innerHTML = '<span class="err">请输入密码</span>';
    return;
  }
  if (message) message.textContent = '';
  const button = $('authLoginBtn');
  if (button) button.disabled = true;
  try { await createWebSession(password); } catch (error) {
    if (message) message.innerHTML = '<span class="err">' + esc(error.message) + '</span>';
  } finally {
    if (button) button.disabled = false;
  }
}
export async function logout() {
  try { if (getSessionToken()) await api('/api/auth/logout', { method: 'POST' }); } catch { /* ignore */ }
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
export function onAdminKeySaved() {}

function updateCredentialFormPolicy() {
  const enabledNow = $('accountPasswordEnabled')?.checked ?? passwordProtectionAtLoad;
  const currentField = $('acctCurrentPassField'), currentPassword = $('acctCurrentPass');
  const newPassword = $('acctNewPass'), confirmPassword = $('acctNewPass2');
  if (currentField) currentField.hidden = !passwordProtectionAtLoad;
  if (currentPassword) currentPassword.required = passwordProtectionAtLoad;
  if (newPassword) newPassword.required = enabledNow && !passwordProtectionAtLoad;
  if (confirmPassword) confirmPassword.required = Boolean(newPassword?.value) || (enabledNow && !passwordProtectionAtLoad);
}
export async function loadAccountSettings() {
  const status = $('accountCurrent'), message = $('accountMsg'), logoutButton = $('btnAccountLogout');
  if (message) message.textContent = '';
  if (!getSessionToken()) {
    if (status) status.textContent = '请先进入控制台，再修改密码保护设置。';
    if (logoutButton) logoutButton.hidden = true;
    return;
  }
  try {
    const response = await api('/api/auth/credentials');
    passwordProtectionAtLoad = response.password_enabled !== false;
    passwordProtectionEnabled = passwordProtectionAtLoad;
    updateLoginModeUI();
    const toggle = $('accountPasswordEnabled');
    if (toggle) toggle.checked = passwordProtectionAtLoad;
    if (status) {
      const source = response.stored_in_db ? '已保存' : '使用初始部署设置';
      status.textContent = (passwordProtectionAtLoad ? '密码保护已开启。' : '密码保护已关闭。') + source + '。';
    }
    if (logoutButton) logoutButton.hidden = !passwordProtectionAtLoad;
    updateCredentialFormPolicy();
  } catch (error) { if (status) status.textContent = error.message; }
}
export async function submitAccountCredentials(event) {
  event?.preventDefault?.();
  const message = $('accountMsg'), targetEnabled = $('accountPasswordEnabled')?.checked === true;
  const currentPassword = $('acctCurrentPass')?.value || '';
  const newPassword = $('acctNewPass')?.value || '', confirmPassword = $('acctNewPass2')?.value || '';
  if (newPassword !== confirmPassword) {
    if (message) message.innerHTML = '<span class="err">两次新密码不一致</span>';
    return;
  }
  if (passwordProtectionAtLoad && !currentPassword) {
    if (message) message.innerHTML = '<span class="err">请输入当前密码</span>';
    return;
  }
  if (!passwordProtectionAtLoad && targetEnabled && !newPassword) {
    if (message) message.innerHTML = '<span class="err">开启密码保护时，请先设置新密码</span>';
    return;
  }
  if (!targetEnabled && newPassword) {
    if (message) message.innerHTML = '<span class="err">关闭密码保护时，请清空新密码输入框</span>';
    return;
  }
  if (targetEnabled === passwordProtectionAtLoad && !newPassword) {
    if (message) message.innerHTML = '<span class="err">没有需要保存的更改</span>';
    return;
  }
  const button = $('accountSaveButton');
  if (button) button.disabled = true;
  try {
    const body = { current_password: currentPassword, password_enabled: targetEnabled };
    if (newPassword) body.new_password = newPassword;
    await api('/api/auth/credentials', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    });
    resetPasswordField('acctCurrentPass');
    resetPasswordField('acctNewPass');
    resetPasswordField('acctNewPass2');
    if (message) message.innerHTML = '<span class="ok">设置已保存</span>';
    await loadAccountSettings();
  } catch (error) {
    if (message) message.innerHTML = '<span class="err">' + esc(error.message) + '</span>';
  } finally {
    if (button) button.disabled = false;
  }
}
