import { esc } from './dom.js';

export function fmtSize(n) {
  n = Number(n || 0);
  if (!n) return '—';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return n.toFixed(i ? 2 : 0) + ' ' + u[i];
}

export function fmtDate(s) {
  return s ? new Date(s).toLocaleString() : '—';
}

/** 调用记录：完整 URL（协议 + 域名或 IP + 路径）。旧记录没有 request_url 时退回路径。 */
export function displayRequestTarget(row) {
  const method = String(row?.method || '').trim();
  const url = String(row?.request_url || '').trim() || String(row?.endpoint || '').trim() || '—';
  return method ? method + ' ' + url : url;
}

/** 调用记录列表：只展示网关实际上游 model（model_actual），不用客户端传入的 model。 */
export function displayUpstreamModel(row) {
  const actual = String(row?.model_actual ?? '').trim();
  return actual || '—';
}

export function slug(s) {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
}

export function statusLabel(s) {
  const m = {
    loaded: '已加载',
    unloaded: '已卸载',
    loading: '加载中',
    sleeping: '休眠',
    installed: '已安装',
    registered: '已注册',
    downloading: '下载中',
    ready: '已完成',
    download_failed: '下载失败',
    download_cancelled: '已取消下载',
    success: '成功',
    error: '失败',
    active: '启用',
    disabled: '已禁用',
    available: '未在内存',
    running: '运行中',
    completed: '已完成',
    failed: '失败',
    cancelled: '已停止',
  };
  const k = String(s || '').toLowerCase();
  return m[k] || s || '—';
}

export function statusPill(s, extra = '') {
  const v = String(s || '').toLowerCase();
  let cls = 'pill';
  if (v.includes('load') && !v.includes('unload')) cls += ' pill-loaded';
  else if (v.includes('unload') || v === 'installed' || v === 'registered') cls += ' pill-unloaded';
  else if (v.includes('download')) cls += ' pill-downloading';
  else if (v.includes('fail') || v === 'error') cls += ' pill-error';
  else if (v === 'success' || v === 'active') cls += ' pill-success';
  else if (v === 'running') cls += ' pill-running';
  else if (v.includes('cancel')) cls += ' pill-warn';
  return `<span class="${cls}${extra}">${esc(statusLabel(s))}</span>`;
}

export function emptyState(t) {
  return `<div class="empty">${esc(t)}</div>`;
}

export function wrapTable(h) {
  return `<div class="table-wrap"><table>${h}</table></div>`;
}
