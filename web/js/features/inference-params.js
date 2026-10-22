import { $, esc } from '../core/dom.js';
import { api } from '../core/api.js';

let schemaFields = [];
let sectionTitles = {};
let sectionDescriptions = {};
let currentBackend = 'llamacpp';
let currentParams = {};
let currentPolicy = { mode: 'client_first' };
let factoryDefaults = { llamacpp: {}, ollama: {} };

function backendLabel() {
  return currentBackend === 'ollama' ? 'Ollama' : 'llama.cpp';
}

function exampleForBackend(f) {
  const m = f.examples || {};
  return (m[currentBackend] ?? f.example ?? '').trim();
}

function fieldValue(key) {
  const el = document.querySelector('[data-inf-key="' + key + '"]');
  if (!el) return undefined;
  if (el.tagName === 'SELECT') return el.value;
  if (el.tagName === 'TEXTAREA') {
    const lines = el.value.split('\n').map((s) => s.trim()).filter(Boolean);
    return lines.length ? lines : undefined;
  }
  const t = el.value.trim();
  if (t === '') return undefined;
  if (el.type === 'number') {
    const n = Number(t);
    return Number.isFinite(n) ? n : undefined;
  }
  return t;
}

function setFieldValue(key, val) {
  const el = document.querySelector('[data-inf-key="' + key + '"]');
  if (!el) return;
  if (val == null) {
    el.value = '';
    return;
  }
  if (el.tagName === 'TEXTAREA' && Array.isArray(val)) {
    el.value = val.join('\n');
    return;
  }
  el.value = val;
}

function collectParams() {
  const p = {};
  schemaFields.forEach((f) => {
    if (f.section === 'policy') return;
    const v = fieldValue(f.key);
    if (v !== undefined) p[f.key] = v;
  });
  return p;
}

function factoryValueForKey(key) {
  const bag = factoryDefaults[currentBackend] || {};
  const v = bag[key];
  if (v == null || v === '') return null;
  if (Array.isArray(v)) return v.length ? v.join('\n') : null;
  return String(v);
}

/** 当前后端的出厂默认（还原默认会写入）；未保存时输入框可留空表示不注入 */
function fieldInputHints(f) {
  const factory = factoryValueForKey(f.key);
  const gatewayKeys = new Set(['max_output_cap']);
  if (factory != null) {
    const note = gatewayKeys.has(f.key)
      ? '来自 config.yaml，与「' + backendLabel() + ' · 还原出厂默认」一致'
      : backendLabel() + ' 出厂默认（点「还原出厂默认」写入当前后端；未保存前留空=不注入）';
    return { placeholder: factory, note, noteClass: gatewayKeys.has(f.key) ? 'from-config' : 'from-factory' };
  }
  const ex = exampleForBackend(f);
  let note =
    '当前后端无出厂默认项：留空表示网关不在 JSON 写入，由客户端或上游决定';
  if (ex) note += ' · ' + backendLabel() + ' 常见参考 ' + ex;
  return { placeholder: '留空 = 不注入', note, noteClass: 'no-factory' };
}

function metaBlock(f) {
  const parts = [];
  if (f.description) parts.push('<p class="param-desc">' + esc(f.description) + '</p>');
  if (f.impact) parts.push('<p class="param-meta"><span class="k">影响</span>' + esc(f.impact) + '</p>');
  if (f.range) parts.push('<p class="param-meta"><span class="k">范围</span>' + esc(f.range) + '</p>');
  if (f.recommend) parts.push('<p class="param-meta param-rec"><span class="k">推荐</span>' + esc(f.recommend) + '</p>');
  return parts.join('');
}

function renderFieldControl(f) {
  const hints = fieldInputHints(f);
  const ph = esc(hints.placeholder);
  const title = esc(f.description || f.hint || '');
  const note =
    '<p class="param-input-note ' +
    esc(hints.noteClass) +
    '">' +
    esc(hints.note) +
    '</p>';
  if (f.type === 'select') {
    let h = '<select data-inf-key="' + esc(f.key) + '" title="' + title + '">';
    (f.select || []).forEach((opt) => {
      h += '<option value="' + esc(opt.value) + '">' + esc(opt.label) + '</option>';
    });
    h += '</select>' + note;
    return h;
  }
  if (f.type === 'stop_lines') {
    return (
      '<textarea data-inf-key="' +
      esc(f.key) +
      '" rows="3" placeholder="' +
      ph +
      '" title="' +
      title +
      '"></textarea>' +
      note
    );
  }
  return (
    '<input data-inf-key="' +
    esc(f.key) +
    '" type="' +
    (f.type === 'number' ? 'number' : 'text') +
    '" placeholder="' +
    ph +
    '" title="' +
    title +
    '">' +
    note
  );
}

function bindToolbar() {
  const sel = $('infBackendSelect');
  if (sel) {
    sel.value = currentBackend;
    sel.onchange = () => {
      currentBackend = sel.value;
      load();
    };
  }
  $('infSaveBtn') && ($('infSaveBtn').onclick = () => save().catch((e) => alert(e.message)));
  $('infResetBtn') && ($('infResetBtn').onclick = () => resetParams().catch((e) => alert(e.message)));
}

function renderPolicyRoot(root) {
  if (!root) return;
  const policyField = schemaFields.find((f) => f.key === 'mode' && f.section === 'policy');
  let html =
    '<div class="inf-toolbar row">' +
    '<div class="field sm"><label>后端</label><select id="infBackendSelect"><option value="llamacpp">llama.cpp</option><option value="ollama">Ollama</option></select></div>' +
    '<div class="btn-row" style="margin:0;align-self:flex-end"><button type="button" class="btn sm primary" id="infSaveBtn">保存</button><button type="button" class="btn sm ghost" id="infResetBtn">还原出厂默认</button></div>' +
    '</div>';
  if (sectionDescriptions.policy) {
    html += '<p class="param-section-desc inf-policy-lead">' + esc(sectionDescriptions.policy) + '</p>';
  }
  if (policyField) html += metaBlock(policyField);
  html += '<div class="policy-row inf-policy-choices">';
  html +=
    '<label class="inf-policy-option"><input type="radio" name="infPolicy" value="client_first" ' +
    (currentPolicy.mode !== 'gateway_only' ? 'checked' : '') +
    '> <span><b>客户端优先</b><span class="small muted">client_first · 客户端字段覆盖网关默认</span></span></label>';
  html +=
    '<label class="inf-policy-option"><input type="radio" name="infPolicy" value="gateway_only" ' +
    (currentPolicy.mode === 'gateway_only' ? 'checked' : '') +
    '> <span><b>仅网关配置</b><span class="small muted">gateway_only · 忽略客户端采样字段</span></span></label>';
  html += '</div>';
  root.innerHTML = html;
  root.classList.remove('muted');
  bindToolbar();
}

function renderParamsRoot(root) {
  if (!root) return;
  const sections = {};
  schemaFields.forEach((f) => {
    if (f.section === 'policy') return;
    if (!f.backends.includes('both') && !f.backends.includes(currentBackend)) return;
    (sections[f.section] = sections[f.section] || []).push(f);
  });
  let html =
    '<p class="small muted inf-hint">参数与合并策略按 <strong>' +
    esc(backendLabel()) +
    '</strong> 分别保存。转发 /v1 时使用「出厂默认 + 已保存覆盖」：<strong>有值就会写入请求</strong>（与输入框是否留空无关）。下方输入框只影响你显式保存的覆盖项。</p>';
  Object.keys(sections).forEach((sec) => {
    html += '<div class="param-section"><h3>' + esc(sectionTitles[sec] || sec) + '</h3>';
    if (sectionDescriptions[sec]) {
      html += '<p class="param-section-desc">' + esc(sectionDescriptions[sec]) + '</p>';
    }
    html += '<div class="param-field-list">';
    sections[sec].forEach((f) => {
      html +=
        '<div class="param-field-card"><div class="param-field-head"><label title="' +
        esc(f.description || '') +
        '">' +
        esc(f.label) +
        '</label><code class="param-key">' +
        esc(f.key) +
        '</code></div>' +
        metaBlock(f) +
        '<div class="param-control">' +
        renderFieldControl(f) +
        '</div></div>';
    });
    html += '</div></div>';
  });
  root.innerHTML = html;
  root.classList.remove('muted');
  Object.entries(currentParams).forEach(([k, v]) => setFieldValue(k, v));
}

function render() {
  renderPolicyRoot($('inferencePolicyRoot'));
  renderParamsRoot($('inferenceParamsRoot'));
}

async function load() {
  const policyRoot = $('inferencePolicyRoot');
  const paramsRoot = $('inferenceParamsRoot');
  if (!policyRoot && !paramsRoot) return;
  try {
    if (!schemaFields.length) {
      const sch = await api('/api/admin/inference-params/schema');
      schemaFields = sch.fields || [];
      sectionTitles = sch.sections || {};
      sectionDescriptions = sch.section_descriptions || {};
    }
    const j = await api('/api/admin/inference-params?backend=' + encodeURIComponent(currentBackend));
    currentParams = j.params || {};
    currentPolicy = j.policy || { mode: 'client_first' };
    if (j.defaults) {
      factoryDefaults = {
        llamacpp: j.defaults.llamacpp || {},
        ollama: j.defaults.ollama || {},
      };
    }
    render();
  } catch (e) {
    const err = '<span class="err">' + esc(e.message) + '</span>';
    if (policyRoot) policyRoot.innerHTML = err;
    if (paramsRoot) paramsRoot.innerHTML = err;
  }
}

async function save() {
  const mode = document.querySelector('input[name="infPolicy"]:checked')?.value || 'client_first';
  await api('/api/admin/inference-params', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ backend: currentBackend, params: collectParams(), policy: { mode } }),
  });
  alert('已保存');
  await load();
}

async function resetParams() {
  if (!confirm('将该后端的参数还原为 config.yaml 出厂默认？')) return;
  const j = await api('/api/admin/inference-params/reset', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ backend: currentBackend }),
  });
  currentParams = j.params || {};
  await load();
}

export const InferenceParams = { load };
