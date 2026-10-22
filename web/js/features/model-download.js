import { $, esc } from '../core/dom.js';
import { api } from '../core/api.js';
import { emptyState, wrapTable } from '../core/format.js';

let debounceTimer = 0;
let pullModalEl = null;
const localInv = { ollama: new Set(), ggufRepoFile: new Set() };

async function refreshLocalInventory() {
  const j = await api('/api/admin/local-inventory');
  localInv.ollama = new Set((j.ollama_names || []).map((n) => String(n).trim().toLowerCase()));
  localInv.ggufRepoFile = new Set();
  (j.gguf || []).forEach((g) => {
    if (g.repo && g.filename) localInv.ggufRepoFile.add(g.repo + '\0' + g.filename);
  });
}

function isOllamaLocal(fullName) {
  return localInv.ollama.has(String(fullName || '').trim().toLowerCase());
}

function isGgufLocal(repo, filename) {
  const fn = String(filename || '').trim();
  const base = fn.includes('/') ? fn.split('/').pop() : fn;
  return localInv.ggufRepoFile.has(String(repo || '').trim() + '\0' + base);
}

function ollamaBaseHasLocal(base) {
  base = String(base || '').trim().toLowerCase();
  if (!base) return false;
  for (const id of localInv.ollama) {
    if (id === base || id.startsWith(base + ':')) return true;
  }
  return false;
}

function fmtCount(n) {
  if (n == null) return '—';
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(n);
}

function fmtPublished(row) {
  if (row.updated_label) return row.updated_label;
  if (row.published_at) {
    try {
      return new Date(row.published_at).toLocaleDateString();
    } catch {
      return row.published_at;
    }
  }
  return '—';
}

function sourceLabel(s) {
  return s === 'ollama' ? 'Ollama' : 'Hugging Face';
}

function setCatalogMsg(html) {
  const el = $('catalogMsg');
  if (el) el.innerHTML = html;
}

function scrollToDownloadTasks() {
  $('downloads')?.closest('.card')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
}

function closePullModal() {
  pullModalEl?.remove();
  pullModalEl = null;
}

async function startOllamaPull(fullName) {
  fullName = String(fullName || '').trim();
  if (!fullName) return;
  await refreshLocalInventory().catch(() => {});
  if (isOllamaLocal(fullName)) {
    setCatalogMsg('<span class="warn">本地 Ollama 已存在 <code>' + esc(fullName) + '</code>，无需重复下载</span>');
    return;
  }
  setCatalogMsg('<span class="muted">正在创建下载任务 <code>' + esc(fullName) + '</code>…</span>');
  try {
    await api('/api/admin/ollama/pull', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: fullName }),
    });
    closePullModal();
    setCatalogMsg('<span class="ok">已加入下载任务：<code>' + esc(fullName) + '</code>，请在下方的「下载任务」查看进度</span>');
    scrollToDownloadTasks();
    if (typeof globalThis.loadDownloads === 'function') {
      await globalThis.loadDownloads().catch(() => {});
    }
  } catch (e) {
    setCatalogMsg('<span class="err">' + esc(e.message) + '</span>');
  }
}

async function openOllamaPullDialog(model, hintTags) {
  model = String(model || '').trim();
  if (!model) return;
  closePullModal();
  const overlay = document.createElement('div');
  overlay.className = 'ollama-pull-overlay';
  overlay.innerHTML =
    '<div class="ollama-pull-dialog card">' +
    '<div class="card-head"><div><h2>选择 Ollama 规格</h2><p class="small muted">模型 <code>' +
    esc(model) +
    '</code> · 不同 tag 对应不同体积与能力</p></div>' +
    '<button type="button" class="btn sm ghost" data-close>关闭</button></div>' +
    '<div class="ollama-pull-body muted">正在读取可用 tag…</div></div>';
  document.body.appendChild(overlay);
  pullModalEl = overlay;
  overlay.querySelector('[data-close]').onclick = () => closePullModal();
  overlay.addEventListener('click', (ev) => {
    if (ev.target === overlay) closePullModal();
  });
  const body = overlay.querySelector('.ollama-pull-body');
  let tags = [];
  try {
    const j = await api('/api/admin/catalog/ollama/tags?model=' + encodeURIComponent(model));
    tags = j.tags || [];
  } catch (e) {
    body.innerHTML = '<span class="err">' + esc(e.message) + '</span>';
    return;
  }
  if (!tags.length) {
    tags = [{ tag: 'latest', full_name: model + ':latest' }];
  }
  const hintSet = new Set((hintTags || []).map((t) => String(t).toLowerCase()));
  let html =
    '<p class="small" style="margin:0 0 10px">选择要 pull 的 tag（体积来自 Ollama 库页）：</p><div class="ollama-tag-list">';
  tags.forEach((t, i) => {
    const installed = isOllamaLocal(t.full_name);
    const checked = !installed && i === 0 ? ' checked' : '';
    const hint = hintSet.has(String(t.tag).toLowerCase()) ? ' · 库内标签' : '';
    const metaParts = [];
    if (t.size) metaParts.push(t.size);
    if (t.context) metaParts.push(t.context + ' context');
    if (t.input) metaParts.push(t.input);
    const meta = metaParts.length ? metaParts.join(' · ') : '体积未知';
    html +=
      '<label class="ollama-tag-option' +
      (installed ? ' disabled' : '') +
      '"><input type="radio" name="ollamaTagPick" value="' +
      esc(t.full_name) +
      '"' +
      checked +
      (installed ? ' disabled' : '') +
      '> <code>' +
      esc(t.full_name) +
      '</code>' +
      '<span class="small muted">' +
      esc(meta) +
      esc(hint) +
      '</span>' +
      (installed ? '<span class="small ok">已本地</span>' : '') +
      '</label>';
  });
  html +=
    '</div><div class="field" style="margin-top:12px"><label>或自定义</label><input id="ollamaTagCustom" placeholder="' +
    esc(model + ':latest') +
    '"></div>' +
    '<div class="btn-row" style="margin-top:14px"><button type="button" class="btn primary" data-start>开始下载</button></div>';
  body.classList.remove('muted');
  body.innerHTML = html;
  body.querySelector('[data-start]').onclick = () => {
    const custom = ($('ollamaTagCustom')?.value || '').trim();
    const picked = body.querySelector('input[name="ollamaTagPick"]:checked:not(:disabled)')?.value || '';
    startOllamaPull(custom || picked);
  };
}

async function copyText(text) {
  text = String(text || '').trim();
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    ta.remove();
  }
  setCatalogMsg('<span class="ok">已复制：<code>' + esc(text) + '</code></span>');
}

function renderRows(rows) {
  const el = $('catalogResults');
  if (!el) return;
  if (!rows.length) {
    el.innerHTML = emptyState('无结果，请换关键词或渠道');
    return;
  }
  const showDl = rows.some((r) => r.source === 'huggingface' || r.source === 'ollama');
  const showOllamaSpec = rows.some((r) => r.source === 'ollama');
  el.innerHTML = wrapTable(
    '<tr><th>名称</th><th>渠道</th>' +
      (showDl ? '<th>下载量</th>' : '') +
      (showOllamaSpec ? '<th>规格</th>' : '') +
      '<th>发布时间</th><th>摘要</th><th></th></tr>' +
      rows
        .map((r) => {
          const dl =
            r.downloads != null
              ? fmtCount(r.downloads)
              : r.source === 'ollama'
                ? '<span class="muted">—</span>'
                : '—';
          const spec =
            r.source === 'ollama'
              ? (r.param_tags || []).length
                ? esc((r.param_tags || []).join(' · ')) + '<div class="small muted">点 pull 查看各 tag 体积</div>'
                : '<span class="muted">点 pull 查看体积</span>'
              : '';
          const actions = [];
          actions.push(
            '<button type="button" class="btn sm" data-copy="' + esc(r.copy_name || r.id) + '">复制名</button>',
          );
          if (r.source === 'huggingface') {
            actions.push(
              '<button type="button" class="btn sm primary" data-hf-repo="' + esc(r.id) + '">选文件</button>',
            );
          } else if (r.source === 'ollama') {
            const localMark = ollamaBaseHasLocal(r.id) ? ' <span class="pill pill-success">已本地</span>' : '';
            actions.push(
              '<button type="button" class="btn sm primary" data-ollama-pick="' +
                esc(r.id) +
                '">pull</button>' +
                localMark,
            );
          }
          return `<tr>
          <td><code>${esc(r.id)}</code><div class="catalog-meta">${esc(r.display_name || '')}</div></td>
          <td>${esc(sourceLabel(r.source))}</td>
          ${showDl ? `<td>${dl}</td>` : ''}
          ${showOllamaSpec ? `<td class="small">${spec}</td>` : ''}
          <td class="small">${esc(fmtPublished(r))}</td>
          <td class="small muted">${esc((r.description || '').slice(0, 120))}</td>
          <td><div class="td-actions">${actions.join('')}</div></td></tr>`;
        })
        .join(''),
  );
}

function onCatalogResultsClick(ev) {
  const copyBtn = ev.target.closest('[data-copy]');
  if (copyBtn) {
    ev.preventDefault();
    copyText(copyBtn.getAttribute('data-copy'));
    return;
  }
  const hfBtn = ev.target.closest('[data-hf-repo]');
  if (hfBtn) {
    ev.preventDefault();
    const repo = hfBtn.getAttribute('data-hf-repo');
    if ($('repo')) $('repo').value = repo;
    if (typeof globalThis.listFiles === 'function') globalThis.listFiles();
    setCatalogMsg('已填入仓库 ' + esc(repo) + '，请在下方读取 GGUF');
    return;
  }
  const pickBtn = ev.target.closest('[data-ollama-pick]');
  if (pickBtn) {
    ev.preventDefault();
    const model = pickBtn.getAttribute('data-ollama-pick');
    setCatalogMsg('<span class="muted">正在打开 <code>' + esc(model) + '</code> 的 tag 列表…</span>');
    openOllamaPullDialog(model, []).catch((e) => {
      setCatalogMsg('<span class="err">' + esc(e?.message || String(e)) + '</span>');
    });
  }
}

async function search() {
  const query = ($('catalogQuery')?.value || '').trim();
  const source = $('catalogSource')?.value || 'all';
  const sort = $('catalogSort')?.value || 'hot';
  const hfGguf = $('catalogHfGguf')?.value !== '0';
  const msg = $('catalogMsg');
  if (msg) msg.textContent = '搜索中…';
  await refreshLocalInventory().catch(() => {});
  try {
    const params = new URLSearchParams({ q: query, source, sort, limit: '40', hf_gguf: hfGguf ? '1' : '0' });
    const j = await api('/api/admin/catalog/search?' + params);
    renderRows(j.data || []);
    if (msg)
      msg.textContent = query
        ? '共 ' + (j.data || []).length + ' 条'
        : '展示热门/最近模型（可输入关键词缩小范围）';
  } catch (e) {
    if (msg) msg.innerHTML = '<span class="err">' + esc(e.message) + '</span>';
    if ($('catalogResults')) $('catalogResults').innerHTML = '';
  }
}

function scheduleSearch() {
  clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => search().catch(() => {}), 350);
}

function init() {
  const input = $('catalogQuery');
  if (input) {
    input.addEventListener('input', scheduleSearch);
    input.addEventListener('keydown', (ev) => {
      if (ev.key === 'Enter') {
        ev.preventDefault();
        search().catch(() => {});
      }
    });
  }
  ['catalogSource', 'catalogSort', 'catalogHfGguf'].forEach((id) => {
    $(id)?.addEventListener('change', () => search().catch(() => {}));
  });
  $('catalogResults')?.addEventListener('click', onCatalogResultsClick);
}

export const ModelDownload = {
  search,
  copyText,
  init,
  startOllamaPull,
  refreshLocalInventory,
  isOllamaLocal,
  isGgufLocal,
};
