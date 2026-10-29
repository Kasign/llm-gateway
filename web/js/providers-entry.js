(() => {
  'use strict';

  const state = { rows: [], editing: null, loading: false, loaded: false, activeTemplate: '', pendingTemplate: null };
  const PROVIDER_TEMPLATES = {
    openai: {
      label: 'OpenAI',
      id: 'openai',
      base_url: 'https://api.openai.com/v1',
      default_model: 'gpt-4.1-mini',
      api_key_header: 'authorization',
      hint: 'OpenAI 官方 API；请按账号实际可用模型调整。'
    },
    deepseek: {
      label: 'DeepSeek',
      id: 'deepseek',
      base_url: 'https://api.deepseek.com',
      default_model: 'deepseek-chat',
      api_key_header: 'authorization',
      hint: 'DeepSeek API；保存前确认模型 ID 与账号权限一致。'
    },
    siliconflow: {
      label: '硅基流动 SiliconFlow',
      id: 'siliconflow',
      base_url: 'https://api.siliconflow.cn/v1',
      default_model: 'Qwen/Qwen3-8B',
      api_key_header: 'authorization',
      hint: '硅基流动 OpenAI 兼容接口；模型示例可替换为平台模型列表中的 ID。'
    },
    openrouter: {
      label: 'OpenRouter',
      id: 'openrouter',
      base_url: 'https://openrouter.ai/api/v1',
      default_model: 'openai/gpt-4.1-mini',
      api_key_header: 'authorization',
      hint: 'OpenRouter 模型 ID 通常需要包含供应方前缀，例如 openai/...。'
    },
    dashscope: {
      label: '阿里云百炼（兼容模式）',
      id: 'dashscope',
      base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
      default_model: 'qwen-plus',
      api_key_header: 'authorization',
      hint: '使用百炼 OpenAI 兼容模式；模型 ID 请以账号当前可用列表为准。'
    },
    volcengine: {
      label: '火山方舟（需 Endpoint ID）',
      id: 'volcengine',
      base_url: 'https://ark.cn-beijing.volces.com/api/v3',
      default_model: 'ep-your-endpoint-id',
      api_key_header: 'authorization',
      hint: '火山方舟通常使用推理接入点 ID；请将 ep-your-endpoint-id 替换为真实 Endpoint ID。'
    },
    groq: {
      label: 'Groq',
      id: 'groq',
      base_url: 'https://api.groq.com/openai/v1',
      default_model: 'llama-3.3-70b-versatile',
      api_key_header: 'authorization',
      hint: 'Groq OpenAI 兼容接口；模型示例可能随平台下线策略变化。'
    },
    mistral: {
      label: 'Mistral AI',
      id: 'mistral',
      base_url: 'https://api.mistral.ai/v1',
      default_model: 'mistral-small-latest',
      api_key_header: 'authorization',
      hint: 'Mistral AI OpenAI 兼容接口；模型 ID 请以账号实际可用列表为准。'
    }
  };
  const $ = (id) => document.getElementById(id);
  const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (ch) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[ch]);
  const token = () => localStorage.getItem('llm_gateway_session_token') || '';
  // Provider 管理接口只使用已登录的 Web Session，不回退到管理员服务密钥。
  const authHeaders = (extra = {}) => {
    const headers = { ...extra };
    delete headers['X-Admin-Key'];
    if (token()) headers.Authorization = 'Bearer ' + token();
    return headers;
  };
  async function api(url, options = {}) {
    const headers = authHeaders(options.headers || {});
    if (options.body && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
    const response = await fetch(url, { ...options, headers });
    let result = null;
    try { result = await response.json(); } catch { /* empty response, e.g. 204 */ }
    if (!response.ok) {
      const message = result?.error?.message || result?.message || ('HTTP ' + response.status);
      throw new Error(message);
    }
    return result;
  }
  function setStatus(message, kind = 'muted', editor = false) {
    const el = $(editor ? 'providerEditorStatus' : 'providerStatus');
    if (!el) return;
    el.hidden = false;
    el.className = 'msg ' + kind;
    el.textContent = message;
  }
  function rowByID(id) {
    const normalized = String(id ?? '').trim().toLowerCase();
    if (!normalized) return null;
    return state.rows.find((row) => String(row.id ?? '').trim().toLowerCase() === normalized) || null;
  }
  function renderRows() {
    const target = $('providerRows');
    if (!target) return;
    if (!state.rows.length) {
      target.innerHTML = '<tr><td colspan="9" class="muted">当前没有远程 Provider。请点击“新增 Provider”在 Web 管理台添加连接。</td></tr>';
      return;
    }
    target.innerHTML = state.rows.map((row) => {
      const status = row.secret_unavailable ? 'Key 不可解密' : (row.enabled ? '已启用' : '已停用');
      const statusClass = row.secret_unavailable ? 'provider-state-bad' : (row.enabled ? 'provider-state-on' : 'provider-state-off');
      let actions = '<button class="btn sm ghost" data-action="test" data-id="' + esc(row.id) + '">测试</button>';
      actions += ' <button class="btn sm ghost" data-action="edit" data-id="' + esc(row.id) + '">配置</button>';
      actions += row.enabled
        ? ' <button class="btn sm ghost" data-action="disable" data-id="' + esc(row.id) + '">停用</button>'
        : ' <button class="btn sm ghost" data-action="enable" data-id="' + esc(row.id) + '">启用</button>';
      if (row.api_key_configured) actions += ' <button class="btn sm ghost" data-action="clear-key" data-id="' + esc(row.id) + '">清除 Key</button>';
      actions += ' <button class="btn sm danger" data-action="delete" data-id="' + esc(row.id) + '">删除</button>';
      return '<tr><td><code>' + esc(row.id) + '</code></td><td><span class="provider-state ' + statusClass + '">' + esc(status) + '</span></td><td><code>' + esc(row.default_model || '—') + '</code></td><td>' + (Number(row.requests_per_minute) > 0 ? (row.requests_per_minute_usage_available ? esc(row.requests_per_minute_used ?? 0) + ' / ' + esc(row.requests_per_minute) + ' · 剩 ' + esc(row.requests_per_minute_remaining ?? 0) + ' · ' + esc(row.requests_per_minute_reset_seconds ?? 0) + ' 秒重置' : esc(row.requests_per_minute) + '/分钟 · 用量暂不可用') : '不限') + '</td><td>' + (Number(row.token_budget_per_day) > 0 ? (row.token_budget_per_day_usage_available ? esc(row.token_budget_per_day_used ?? 0) + ' / ' + esc(row.token_budget_per_day) + ' · 剩 ' + esc(row.token_budget_per_day_remaining ?? 0) + ' · ' + esc(row.token_budget_per_day_reset_seconds ?? 0) + ' 秒重置' : esc(row.token_budget_per_day) + '/日 · 用量暂不可用') : '不限') + '</td><td>' + (row.concurrent_requests_usage_available ? (esc(row.concurrent_requests_used ?? 0) + ' / ' + esc(row.concurrent_requests_limit ?? 0) + ' · 剩 ' + esc(row.concurrent_requests_remaining ?? 0) + ' · 队列 ' + esc(row.queued_requests ?? 0) + '/' + esc(row.queue_limit ?? row.max_queue_size ?? 0)) : '—') + '</td><td>' + (row.concurrent_requests_usage_available ? (esc(row.concurrent_requests_rejected ?? 0) + ' · 队列超时 ' + esc(row.queue_timeouts ?? 0)) : '—') + '</td><td>' + (row.api_key_configured ? '<span class="provider-key-set">已配置</span>' : '<span class="muted">未配置</span>') + '</td><td class="provider-row-actions">' + actions + '</td></tr>';
    }).join('');
  }
  async function loadProviders() {
    if (state.loading) return;
    const hasPreviousData = state.loaded;
    state.loading = true;
    const status = $('providerStatus');
    if (status) status.setAttribute('aria-busy', 'true');
    $('providerRefresh').disabled = true;
    setStatus(hasPreviousData ? '正在刷新 Provider 列表…' : '正在加载 Provider 列表…', 'muted');
    try {
      const result = await api('/api/admin/providers');
      state.rows = Array.isArray(result?.data) ? result.data : [];
      state.loaded = true;
      renderRows();
      setStatus('已加载 ' + state.rows.length + ' 个 Provider。Web API 不返回任何 Key 明文或密文；加密密钥' + (result?.encryption_available ? '已配置。' : '未配置，新增/修改 Key 时需要先配置。'), result?.encryption_available ? 'ok' : 'warn');
    } catch (error) {
      const reason = error.message || '未知错误';
      if (!state.loaded) {
        const target = $('providerRows');
        if (target) target.innerHTML = '<tr><td colspan="9" class="muted">Provider 列表加载失败。请点击上方“刷新”重试。</td></tr>';
        setStatus('Provider 列表加载失败，请点击“刷新”重试。原因：' + reason, 'err');
      } else {
        setStatus('Provider 列表刷新失败，仍保留上次成功加载的数据。点击“刷新”重试。原因：' + reason, 'err');
      }
    } finally {
      state.loading = false;
      if (status) status.setAttribute('aria-busy', 'false');
      $('providerRefresh').disabled = false;
    }
  }
  function applyProviderTemplate(key) {
    const template = PROVIDER_TEMPLATES[key];
    if (key === 'custom') {
      $('providerId').value = '';
      $('providerBaseURL').value = '';
      $('providerDefaultModel').value = '';
      $('providerApiKeyHeader').value = 'authorization';
      $('providerTemplateHelp').textContent = '自定义模板已清空 Provider ID、Base URL 和默认模型；已输入的 API Key 不会被自动清除。';
      state.activeTemplate = key;
      refreshProviderIDValidation();
      return;
    }
    if (!template) {
      $('providerTemplateHelp').textContent = '选择模板只会预填基础连接参数，不会自动填写 API Key；默认模型是示例值，保存前请核对。';
      state.activeTemplate = '';
      refreshProviderIDValidation();
      return;
    }
    $('providerId').value = template.id;
    $('providerBaseURL').value = template.base_url;
    $('providerDefaultModel').value = template.default_model;
    $('providerApiKeyHeader').value = template.api_key_header;
    state.activeTemplate = key;
    refreshProviderTemplateHelp();
  }
  function refreshProviderTemplateHelp() {
    const template = PROVIDER_TEMPLATES[state.activeTemplate];
    const id = $('providerId').value.trim();
    if (!template) {
      $('providerTemplateHelp').textContent = '选择模板只会预填基础连接参数，不会自动填写 API Key；默认模型是示例值，保存前请核对。';
      refreshProviderIDValidation();
      return;
    }
    const existingProvider = !state.editing && id ? rowByID(id) : null;
    $('providerTemplateHelp').textContent = template.hint +
      ' 模板不会自动填入 API Key；如模型名称已变化，请修改默认模型 ID。' +
      (existingProvider
        ? ' 当前 Provider ID「' + id + '」已存在，不能重复新增。请填写未占用的唯一 ID，或返回列表配置现有项。'
        : '');
    refreshProviderIDValidation();
  }
  function refreshProviderIDValidation() {
    const input = $('providerId');
    const save = $('providerSave');
    const form = $('providerForm');
    if (!input || !save || !form) return;
    const id = input.value.trim().toLowerCase();
    const duplicate = !state.editing && Boolean(id && state.loaded && rowByID(id));
    const message = duplicate ? 'Provider ID「' + id + '」已存在，不能重复新增。请填写唯一 ID。' : '';
    input.setCustomValidity(message);
    save.disabled = duplicate || form.dataset.saving === 'true';
    if (duplicate) {
      setStatus(message + ' 返回列表后可配置现有 Provider。', 'err', true);
    } else {
      const status = $('providerEditorStatus');
      if (status && status.textContent.includes('不能重复新增')) status.hidden = true;
    }
  }
  function requestProviderTemplateChange(key) {
    if (!key) {
      applyProviderTemplate('');
      return;
    }
    const template = PROVIDER_TEMPLATES[key];
    if (!template && key !== 'custom') return;
    const nextID = key === 'custom' ? '' : template.id;
    const nextURL = key === 'custom' ? '' : template.base_url;
    const connectionChanged = nextID !== $('providerId').value || nextURL !== $('providerBaseURL').value;
    if ($('providerApiKey').value.length > 0 && connectionChanged) {
      state.pendingTemplate = key;
      $('providerTemplateKeyConfirmText').textContent =
        (key === 'custom'
          ? '切换到自定义模板会清空 Provider ID 和 Base URL。'
          : '切换到 ' + template.label + ' 会修改 Provider ID 和 Base URL。') +
        ' 当前输入的 API Key 会随新连接配置保留；若不希望沿用，请选择清除。清除只会清空本表单输入，不会撤销上游已签发的密钥。';
      $('providerTemplateKeyConfirm').hidden = false;
      $('providerTemplate').disabled = true;
      $('providerTemplateHelp').textContent = '请先确认当前 API Key 的处理方式。';
      return;
    }
    applyProviderTemplate(key);
  }
  function resolveProviderTemplateChange(action) {
    if (state.pendingTemplate == null) return;
    const nextTemplate = state.pendingTemplate;
    state.pendingTemplate = null;
    $('providerTemplateKeyConfirm').hidden = true;
    $('providerTemplate').disabled = Boolean(state.editing);
    if (action === 'cancel') {
      $('providerTemplate').value = state.activeTemplate;
      $('providerTemplateHelp').textContent = '已取消模板切换；当前 API Key 和连接配置保持不变。';
      return;
    }
    if (action === 'clear') $('providerApiKey').value = '';
    applyProviderTemplate(nextTemplate);
  }
  function showEditor(row = null) {
    state.editing = row ? row.id : null;
    state.activeTemplate = '';
    state.pendingTemplate = null;
    $('providerTemplateKeyConfirm').hidden = true;
    $('providerForm').reset();
    $('providerTemplatePanel').hidden = !!row;
    $('providerTemplate').disabled = !!row;
    $('providerTemplate').value = '';
    $('providerTemplateHelp').textContent = '选择模板只会预填基础连接参数，不会自动填写 API Key；默认模型是示例值，保存前请核对。';
    $('providerEditorTitle').textContent = row ? '编辑 Provider：' + row.id : '新增 Provider';
    $('providerId').disabled = !!row;
    $('providerId').value = row?.id || '';
    $('providerBaseURL').value = row?.base_url || '';
    $('providerDefaultModel').value = row?.default_model || '';
    $('providerApiKey').value = '';
    $('providerApiKeyHeader').value = row?.api_key_header || 'authorization';
    $('providerConcurrency').value = row?.max_concurrent_requests ?? 64;
    $('providerMaxQueue').value = row?.max_queue_size ?? 0;
    $('providerQueueWaitTimeout').value = row?.queue_wait_timeout_seconds ?? 30;
    $('providerRPM').value = row?.requests_per_minute ?? 0;
    $('providerTokenBudget').value = row?.token_budget_per_day ?? 0;
    $('providerMaxResponse').value = row?.max_response_bytes ?? 0;
    $('providerRequestTimeout').value = row?.request_timeout_seconds ?? 0;
    $('providerEnabled').value = String(row?.enabled ?? true);
    $('providerMappings').value = JSON.stringify(row?.model_mappings || {}, null, 2);
    $('providerAllowPrivate').checked = !!row?.allow_private_network;
    $('providerAllowHTTP').checked = !!row?.allow_insecure_http;
    $('providerKeyHelp').textContent = row?.api_key_configured
      ? '当前 Key 已保存；留空保留原值。替换 Key 时填写新值，清除请返回列表点击“清除 Key”。'
      : '新增/更新时可填写 Key；编辑留空不修改。';
    $('providerEditorStatus').hidden = true;
    $('providerEditorCard').hidden = false;
    $('providerEditorCard').scrollIntoView({ behavior: 'smooth', block: 'start' });
    refreshProviderIDValidation();
  }
  function hideEditor() {
    $('providerEditorCard').hidden = true;
    $('providerTemplateKeyConfirm').hidden = true;
    $('providerTemplate').disabled = false;
    state.activeTemplate = '';
    state.pendingTemplate = null;
    state.editing = null;
    $('providerForm').reset();
  }
  function payloadFromForm() {
    let mappings = {};
    const rawMappings = $('providerMappings').value.trim();
    if (rawMappings) {
      mappings = JSON.parse(rawMappings);
      if (!mappings || typeof mappings !== 'object' || Array.isArray(mappings)) throw new Error('模型映射必须是 JSON 对象。');
    }
    return {
      id: $('providerId').value.trim(),
      enabled: $('providerEnabled').value === 'true',
      base_url: $('providerBaseURL').value.trim(),
      api_key: $('providerApiKey').value,
      api_key_header: $('providerApiKeyHeader').value,
      default_model: $('providerDefaultModel').value.trim(),
      model_mappings: mappings,
      allow_insecure_http: $('providerAllowHTTP').checked,
      allow_private_network: $('providerAllowPrivate').checked,
      max_response_bytes: Number($('providerMaxResponse').value || 0),
      max_concurrent_requests: Number($('providerConcurrency').value || 0),
      max_queue_size: Number($('providerMaxQueue').value || 0),
      queue_wait_timeout_seconds: Number($('providerQueueWaitTimeout').value || 0),
      requests_per_minute: Number($('providerRPM').value || 0),
      token_budget_per_day: Number($('providerTokenBudget').value || 0),
      request_timeout_seconds: Number($('providerRequestTimeout').value || 0),
    };
  }
  async function saveProvider(event) {
    event.preventDefault();
    const form = $('providerForm');
    // 同一表单只能有一个保存请求在途；即使重复绑定监听器或快速双击也只提交一次。
    if (!form || form.dataset.saving === 'true') return;
    form.dataset.saving = 'true';
    const button = $('providerSave');
    button.disabled = true;
    try {
      const payload = payloadFromForm();
      if (!state.editing) {
        if (!state.loaded) {
          throw new Error('尚未成功加载 Provider 列表，暂时无法检查同名配置。请先刷新列表后再保存。');
        }
        const existingProvider = rowByID(payload.id);
        if (existingProvider) {
          throw new Error('Provider ID「' + payload.id + '」已存在，不能重复新增。请填写未占用的唯一 ID，或返回列表配置现有项。');
        }
      }
      const endpoint = state.editing
        ? '/api/admin/providers/' + encodeURIComponent(state.editing)
        : '/api/admin/providers';
      await api(endpoint, { method: state.editing ? 'PUT' : 'POST', body: JSON.stringify(payload) });
      setStatus('Provider 配置已保存并动态生效。', 'ok', true);
      await loadProviders();
      hideEditor();
    } catch (error) {
      setStatus(error instanceof SyntaxError ? '模型映射 JSON 格式错误。' : (error.message || '保存失败。'), 'err', true);
    } finally {
      button.disabled = false;
      form.dataset.saving = 'false';
    }
  }
  async function testProvider(row = null) {
    let payload = {};
    let endpoint;
    try {
      if (row) {
        endpoint = '/api/admin/providers/' + encodeURIComponent(row.id) + '/test';
      } else {
        payload = payloadFromForm();
        endpoint = '/api/admin/providers/' + encodeURIComponent(payload.id) + '/test';
        if (!payload.id) throw new Error('请先填写 Provider ID。');
      }
      const result = await api(endpoint, { method: 'POST', body: JSON.stringify(payload) });
      const models = Array.isArray(result?.models) ? result.models.join(', ') : '';
      const message = result?.success ? ('连接成功；发现模型：' + (models || '接口可用但未返回模型 ID。')) : ('连接失败：' + (result?.error || '上游不可用。'));
      setStatus(message, result?.success ? 'ok' : 'err', !row);
    } catch (error) {
      setStatus(error.message || '连接测试失败。', 'err', !row);
    }
  }
  async function runAction(action, id) {
    const row = rowByID(id);
    if (!row) return;
    if (action === 'delete' && !window.confirm('确定删除 Web Provider「' + id + '」吗？该操作会立即从新请求路由中移除；如它是当前活动上游，请先切换到其他后端。')) return;
    if (action === 'clear-key' && !window.confirm('确定清除 Provider「' + id + '」已保存的 API Key？此操作不可自动恢复。')) return;
    try {
      await api('/api/admin/providers/' + encodeURIComponent(id) + '/' + action, { method: 'POST' });
      setStatus(action === 'delete' ? 'Provider 已删除。' : 'Provider 操作已生效。', 'ok');
      await loadProviders();
    } catch (error) {
      setStatus(error.message || 'Provider 操作失败。当前活动 Provider 不能直接停用或删除，请先切换上游。', 'err');
    }
  }
  function init() {
    // 防止同一页面重复加载 Bundle 时重复绑定事件并发送多次保存请求。
    if (globalThis.__llmGatewayProviderAdminInitialized) return;
    const nav = document.querySelector('.nav button[data-tab="providers"]');
    if (!nav || !$('providers')) return;
    globalThis.__llmGatewayProviderAdminInitialized = true;
    const style = document.createElement('style');
    style.textContent = [
      '.provider-form{display:flex;flex-direction:column;gap:14px}',
      '.provider-template-panel{padding:12px 14px;border:1px solid var(--border);border-radius:12px;background:var(--surface-2)}',
      '.provider-template-panel .field{max-width:760px;margin:0}',
      '.provider-template-panel select{min-height:42px}',
      '.provider-template-label{font-weight:650}',
      '.provider-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}',
      '.provider-form .field{min-width:0}',
      '.provider-form input:not([type=checkbox]),.provider-form select,.provider-form textarea{width:100%;box-sizing:border-box}',
      '.provider-form small{display:block;color:var(--muted,#858b98);font-size:12px;margin-top:5px}',
      '.provider-checks{display:flex;flex-wrap:wrap;gap:16px}',
      '.provider-checks label{display:flex;align-items:center;gap:7px}',
      '.provider-actions{display:flex;gap:8px;flex-wrap:wrap}',
      '.provider-row-actions{min-width:260px;white-space:normal}',
      '.provider-row-actions .btn{margin:2px}',
      '.provider-state{display:inline-flex;flex-shrink:0;white-space:nowrap;border-radius:999px;padding:3px 8px;font-size:12px}',
      '.provider-state-on{background:#e3f5e9;color:#176d3a}',
      '.provider-state-off{background:#edf0f4;color:#596273}',
      '.provider-state-bad{background:#fff0df;color:#925300}',
      '.provider-key-set{color:var(--ok,#34d399)}',
      '.provider-footnote{font-size:12px;line-height:1.6;margin:10px 2px}',
      '@media(max-width:720px){.provider-grid{grid-template-columns:1fr}.provider-row-actions{min-width:200px}}',
    ].join('');
    document.head.appendChild(style);
    $('providerRefresh')?.addEventListener('click', loadProviders);
    $('providerCreate')?.addEventListener('click', () => showEditor());
    $('providerTemplate')?.addEventListener('change', (event) => requestProviderTemplateChange(event.target.value));
    $('providerId')?.addEventListener('input', refreshProviderTemplateHelp);
    $('providerTemplateKeepKey')?.addEventListener('click', () => resolveProviderTemplateChange('keep'));
    $('providerTemplateClearKey')?.addEventListener('click', () => resolveProviderTemplateChange('clear'));
    $('providerTemplateCancel')?.addEventListener('click', () => resolveProviderTemplateChange('cancel'));
    $('providerCancel')?.addEventListener('click', hideEditor);
    $('providerForm')?.addEventListener('submit', saveProvider);
    $('providerTestDraft')?.addEventListener('click', () => testProvider());
    $('providerRows')?.addEventListener('click', (event) => {
      const button = event.target.closest('button[data-action]');
      if (!button) return;
      const { action, id } = button.dataset;
      const row = rowByID(id);
      if (!row) return;
      if (action === 'test') testProvider(row);
      else if (action === 'edit') showEditor(row);
      else runAction(action, id);
    });
    nav.addEventListener('click', () => {
      if (globalThis.dashboardPoll) globalThis.dashboardPoll.stop();
      loadProviders();
    });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();
