import { $ } from './dom.js';

const SESSION_STORAGE_KEY = 'llm_gateway_session_token';
const ADMIN_KEY_STORAGE_KEY = 'llm_gateway_admin_key';

/** 无需 Web 会话即可调用的管理 API（仅登录接口）。 */
const PUBLIC_API_PATHS = new Set(['/api/auth/login', '/api/auth/status']);

export const getKey = () => ($('adminKey')?.value || '').trim();

export const getSessionToken = () => localStorage.getItem(SESSION_STORAGE_KEY) || '';

export function setSessionToken(token) {
  const t = (token || '').trim();
  if (t) localStorage.setItem(SESSION_STORAGE_KEY, t);
  else localStorage.removeItem(SESSION_STORAGE_KEY);
}

/** 是否已在当前浏览器保存管理员 API Key。 */
export function isAuthenticated() {
  return !!localStorage.getItem(ADMIN_KEY_STORAGE_KEY);
}

export function initAdminKey() {
  const el = $('adminKey');
  if (el) el.value = localStorage.getItem('llm_gateway_admin_key') || '';
}

export function redirectToLogin() {
  setSessionToken('');
  localStorage.removeItem(ADMIN_KEY_STORAGE_KEY);
  const input = $('adminKey');
  if (input) input.value = '';
  if (typeof globalThis.forceLoginScreen === 'function') {
    globalThis.forceLoginScreen('管理员 API Key 无效或已变更，请重新输入。');
  }
  if (typeof globalThis.adminKeyStatus === 'function') globalThis.adminKeyStatus();
}

function apiPath(url) {
  try {
    if (url.startsWith('http')) return new URL(url).pathname;
  } catch {
    /* relative url */
  }
  return url.split('?')[0];
}

function requireWebSession(url) {
  const path = apiPath(url);
  if (!path.startsWith('/api/')) return;
  if (PUBLIC_API_PATHS.has(path)) return;
  if (!localStorage.getItem(ADMIN_KEY_STORAGE_KEY)) {
    redirectToLogin();
    throw new Error('请先在“服务密钥”页配置管理员 API Key');
  }
}

function authHeaders(extra = {}) {
  const headers = { ...extra };
  delete headers.Authorization;
  const key = localStorage.getItem(ADMIN_KEY_STORAGE_KEY) || '';
  if (key) headers['X-Admin-Key'] = key;
  return headers;
}

export async function apiPublic(url, opt = {}) {
  opt.headers = { ...(opt.headers || {}) };
  const r = await fetch(url, opt);
  let j = null;
  try {
    j = await r.json();
  } catch {
    /* empty body */
  }
  if (!r.ok) throw new Error(j?.error?.message || j?.message || `HTTP ${r.status}`);
  return j;
}

export async function api(url, opt = {}) {
  requireWebSession(url);
  opt.headers = authHeaders(opt.headers || {});
  const r = await fetch(url, opt);
  let j = null;
  try {
    j = await r.json();
  } catch {
    /* empty body */
  }
  if (!r.ok) {
    const path = apiPath(url);
    if (r.status === 401 && path.startsWith('/api/') && !PUBLIC_API_PATHS.has(path)) {
      redirectToLogin();
    }
    throw new Error(j?.error?.message || j?.message || `HTTP ${r.status}`);
  }
  return j;
}
