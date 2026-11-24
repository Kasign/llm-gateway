import { $, esc } from '../core/dom.js';
import { api } from '../core/api.js';

let profiles = [];

function fieldValue(id) {
  const el = $(id);
  return el ? String(el.value ?? '').trim() : '';
}
function optionalNumber(id) {
  const value = fieldValue(id);
  if (!value) return undefined;
  const n = Number(value);
  if (!Number.isFinite(n)) throw new Error('参数必须是有效数字');
  return n;
}
function setValue(id, value) {
  const el = $(id);
  if (el) el.value = value == null ? '' : String(value);
}
function clearForm() {
  ['runtimeProfileID','runtimeProfileName','runtimeProfileModelID','runtimeProfileThreads',
   'runtimeProfileThreadsBatch','runtimeProfileBatchSize','runtimeProfileUBatchSize',
   'runtimeProfileContextSize','runtimeProfileGPULayers','runtimeProfileCacheTypeK',
   'runtimeProfileCacheTypeV','runtimeProfileNotes'].forEach((id) => setValue(id, ''));
  setValue('runtimeProfileFlashAttention', '');
  const msg = $('runtimeProfileMsg');
  if (msg) msg.textContent = '档案只记录实际部署参数，不会修改或重启 llama.cpp。';
}
function profileParameters(profile) {
  const p = profile.parameters || {};
  return Object.entries(p).map(([key, value]) => key + '=' + String(value)).join(' · ') || '参数未知';
}
function renderList() {
  const root = $('backendRuntimeProfilesList');
  if (!root) return;
  if (!profiles.length) {
    root.innerHTML = '<div class="empty-state">暂无 Backend 运行参数档案。请按当前实际 llama.cpp 启动参数创建。</div>';
    return;
  }
  root.innerHTML = '<div class="table-wrap"><table><thead><tr><th>档案</th><th>模型</th><th>版本</th><th>声明参数</th><th>操作</th></tr></thead><tbody>' +
    profiles.map((p) => '<tr><td><strong>' + esc(p.name) + '</strong><div class="small muted">' + esc(p.backend) + ' · user_declared</div></td>' +
      '<td><code>' + esc(p.model_id) + '</code></td><td>v' + esc(String(p.version || 1)) + '</td>' +
      '<td class="small">' + esc(profileParameters(p)) + '</td>' +
      '<td><button type="button" class="btn sm ghost" data-runtime-profile-edit="' + esc(p.id) + '">编辑</button> ' +
      '<button type="button" class="btn sm danger" data-runtime-profile-delete="' + esc(p.id) + '">删除</button></td></tr>').join('') +
    '</tbody></table></div>';
  root.querySelectorAll('[data-runtime-profile-edit]').forEach((button) => {
    button.addEventListener('click', () => editProfile(button.dataset.runtimeProfileEdit));
  });
  root.querySelectorAll('[data-runtime-profile-delete]').forEach((button) => {
    button.addEventListener('click', () => deleteProfile(button.dataset.runtimeProfileDelete).catch(showError));
  });
}
function editProfile(id) {
  const p = profiles.find((item) => item.id === id);
  if (!p) return;
  const v = p.parameters || {};
  setValue('runtimeProfileID', p.id);
  setValue('runtimeProfileName', p.name);
  setValue('runtimeProfileModelID', p.model_id);
  setValue('runtimeProfileThreads', v.threads);
  setValue('runtimeProfileThreadsBatch', v.threads_batch);
  setValue('runtimeProfileBatchSize', v.batch_size);
  setValue('runtimeProfileUBatchSize', v.ubatch_size);
  setValue('runtimeProfileContextSize', v.ctx_size);
  setValue('runtimeProfileGPULayers', v.n_gpu_layers);
  setValue('runtimeProfileFlashAttention', v.flash_attn == null ? '' : String(v.flash_attn));
  setValue('runtimeProfileCacheTypeK', v.cache_type_k);
  setValue('runtimeProfileCacheTypeV', v.cache_type_v);
  setValue('runtimeProfileNotes', p.notes);
  const msg = $('runtimeProfileMsg');
  if (msg) msg.textContent = '正在编辑 v' + p.version + '；保存后会生成新档案版本。';
}
async function deleteProfile(id) {
  const p = profiles.find((item) => item.id === id);
  if (!p || !confirm('删除运行参数档案「' + p.name + '」？历史 Benchmark 中已保存的快照不会删除。')) return;
  await api('/api/admin/backend-runtime-profiles/' + encodeURIComponent(id), { method: 'DELETE' });
  await load();
  clearForm();
}
function showError(error) {
  const msg = $('runtimeProfileMsg');
  if (msg) msg.textContent = '操作失败：' + (error?.message || String(error));
}
async function saveProfile(event) {
  event?.preventDefault();
  try {
    const id = fieldValue('runtimeProfileID');
    const parameters = {};
    const values = {
      threads: optionalNumber('runtimeProfileThreads'),
      threads_batch: optionalNumber('runtimeProfileThreadsBatch'),
      batch_size: optionalNumber('runtimeProfileBatchSize'),
      ubatch_size: optionalNumber('runtimeProfileUBatchSize'),
      ctx_size: optionalNumber('runtimeProfileContextSize'),
      n_gpu_layers: optionalNumber('runtimeProfileGPULayers'),
    };
    Object.entries(values).forEach(([key, value]) => { if (value !== undefined) parameters[key] = value; });
    const flash = fieldValue('runtimeProfileFlashAttention');
    if (flash !== '') parameters.flash_attn = flash === 'true';
    const cacheK = fieldValue('runtimeProfileCacheTypeK');
    const cacheV = fieldValue('runtimeProfileCacheTypeV');
    if (cacheK) parameters.cache_type_k = cacheK;
    if (cacheV) parameters.cache_type_v = cacheV;
    const payload = {
      id,
      name: fieldValue('runtimeProfileName'),
      backend: 'llamacpp',
      model_id: fieldValue('runtimeProfileModelID'),
      parameters,
      notes: fieldValue('runtimeProfileNotes'),
    };
    const result = await api('/api/admin/backend-runtime-profiles', {
      method: id ? 'PUT' : 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    const savedVersion = result.profile.version;
    clearForm();
    await load();
    const msg = $('runtimeProfileMsg');
    if (msg) msg.textContent = '已保存档案 v' + savedVersion + '。请确认档案值与当前 llama.cpp 实际启动参数一致。';
  } catch (error) {
    showError(error);
  }
}
async function load() {
  const root = $('backendRuntimeProfilesRoot');
  if (!root) return;
  try {
    const result = await api('/api/admin/backend-runtime-profiles');
    profiles = Array.isArray(result.items) ? result.items : [];
    renderList();
    const form = $('backendRuntimeProfileForm');
    if (form && form.dataset.bound !== 'true') {
      form.addEventListener('submit', saveProfile);
      form.dataset.bound = 'true';
      $('runtimeProfileClearBtn')?.addEventListener('click', clearForm);
    }
    await loadBenchmarkOptions();
  } catch (error) {
    root.innerHTML = '<span class="err">' + esc(error.message || '运行参数档案加载失败') + '</span>';
  }
}
async function loadBenchmarkOptions(backend = '', modelID = '', selectID = 'benchRuntimeProfile') {
  const select = $(selectID);
  if (!select) return;
  if (backend && backend !== 'llamacpp') {
    select.innerHTML = '<option value="">当前 Backend 暂不支持运行参数档案</option>';
    return;
  }
  try {
    const query = new URLSearchParams();
    if (backend) query.set('backend', backend);
    if (modelID) query.set('model_id', modelID);
    const result = await api('/api/admin/backend-runtime-profiles' + (query.toString() ? '?' + query.toString() : ''));
    const items = Array.isArray(result.items) ? result.items : [];
    const previous = select.value;
    select.innerHTML = '<option value="">不关联档案（运行参数未知）</option>' + items.map((p) =>
      '<option value="' + esc(p.id) + '">' + esc(p.name) + ' · v' + esc(String(p.version)) + '</option>'
    ).join('');
    if (items.some((p) => p.id === previous)) select.value = previous;
  } catch (error) {
    select.innerHTML = '<option value="">档案加载失败：' + esc(error.message || '') + '</option>';
  }
}

export const BackendRuntimeProfiles = { load, loadBenchmarkOptions };
