import { $ } from './dom.js';

const SESSION_STORAGE_KEY = 'llm_gateway_session_token';

/** 无需 Web 会话即可调用的管理 API（仅登录接口）。 */
const PUBLIC_API_PATHS = new Set(['/api/auth/login', '/api/auth/status']);

export const getKey = () => ($('adminKey')?.value || '').trim();
export const getSavedAdminKey = () => localStorage.getItem('llm_gateway_admin_key') || '';

export const getSessionToken = () => localStorage.getItem(SESSION_STORAGE_KEY) || '';

export function setSessionToken(token) {
  const t = (token || '').trim();
  if (t) localStorage.setItem(SESSION_STORAGE_KEY, t);
  else localStorage.removeItem(SESSION_STORAGE_KEY);
}

/** 控制台是否已 Web 登录（不含 API 密钥旁路）。 */
export function isAuthenticated() {
  return !!getSessionToken();
}

export function initAdminKey() {
  const el = $('adminKey');
  if (el) el.value = localStorage.getItem('llm_gateway_admin_key') || '';
}

export function redirectToLogin() {
  setSessionToken('');
  if (typeof globalThis.forceLoginScreen === 'function') {
    globalThis.forceLoginScreen();
  }
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
  if (!getSessionToken()) {
    redirectToLogin();
    throw new Error('请先登录');
  }
}

function authHeaders(extra = {}) {
  const headers = { ...extra };
  delete headers['X-Admin-Key'];
  const session = getSessionToken();
  if (session) headers.Authorization = `Bearer ${session}`;
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


/** 管理员 API Key 仅用于服务密钥管理，不参与 Web 登录，也不用于其它管理 API。 */
export async function apiServiceKeys(url, opt = {}) {
  const path = apiPath(url);
  if (path !== '/api/admin/keys' && !path.startsWith('/api/admin/keys/')) {
    throw new Error('管理员 API Key 仅允许用于服务密钥管理接口');
  }
  const key = getSavedAdminKey();
  if (!key) throw new Error('请先在“服务密钥”页面输入并验证管理员 API Key');
  const headers = { ...(opt.headers || {}) };
  delete headers.Authorization;
  headers['X-Admin-Key'] = key;
  const response = await fetch(url, { ...opt, headers });
  let result = null;
  try { result = await response.json(); } catch { /* 空响应 */ }
  if (!response.ok) {
    if (response.status === 401) {
      localStorage.removeItem('llm_gateway_admin_key');
      const input = $('adminKey');
      if (input) input.value = '';
      if (typeof globalThis.adminKeyStatus === 'function') globalThis.adminKeyStatus();
    }
    throw new Error(result?.error?.message || result?.message || `HTTP ${response.status}`);
  }
  return result;
}
