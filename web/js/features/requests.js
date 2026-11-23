import { $, esc } from '../core/dom.js';
import { api } from '../core/api.js';
import { fmtDate, statusPill, emptyState, wrapTable, displayUpstreamModel, displayRequestTarget } from '../core/format.js';
import { setGlobalMsg, showToast } from '../core/notify.js';

let detailPollTimer = null;
let detailPollId = '';
let detailEpoch = 0;
let detailTrigger = null;

export function requestsListHasRows() {
  return !!$('requestsTable')?.querySelector('table td');
}

function statsFmt(value) {
  const n = Number(value || 0);
  return Number.isFinite(n) ? Math.round(n).toLocaleString() : '0';
}

let requestLoadEpoch = 0;
let requestSnapshot = '';
let requestStatsKey = '';
let requestStatsLoadedAt = 0;
let requestStatsEpoch = 0;

async function loadRequestStats(force = false) {
  const el = $('requestStats');
  if (!el) return;
  const q = new URLSearchParams();
  q.set('range', $('rqStatsRange')?.value || '24h');
  const provider = $('rqProvider')?.value?.trim();
  if (provider) q.set('provider', provider);
  const key = q.toString();
  const now = Date.now();
  if (!force && key === requestStatsKey && now - requestStatsLoadedAt < 60000) return;
  requestStatsKey = key;
  const epoch = ++requestStatsEpoch;
  if (!el.hasChildNodes()) el.innerHTML = '<p class="muted small">正在汇总统计…</p>';
  try {
    const s = await api('/api/requests/stats?' + key);
    if (epoch !== requestStatsEpoch) return;
    requestStatsLoadedAt = Date.now();
    const r = s.requests || {};
    const lat = s.latency || {};
    const tok = s.tokens || {};
    const actual = tok.upstream_usage || {};
    const local = tok.llama_timings || {};
    const unknown = tok.unknown_source || {};
    const cards = [
      ['请求总量', statsFmt(r.total_requests), statsFmt(r.running_requests) + ' 条进行中'],
      ['成功率', Number(r.success_rate_percent || 0).toFixed(1) + '%', statsFmt(r.success_requests) + ' 成功 / ' + statsFmt(r.completed_requests) + ' 已结束'],
      ['失败请求', statsFmt(r.error_requests), statsFmt(r.cancelled_requests) + ' 条已取消'],
      ['平均耗时', lat.average_ms == null ? '—' : statsFmt(lat.average_ms) + ' ms', statsFmt(lat.sample_count) + ' 条延迟样本'],
      ['上游 usage Token', statsFmt(actual.total_tokens), statsFmt(actual.requests) + ' 条 · 输入 ' + statsFmt(actual.input_tokens) + ' / 输出 ' + statsFmt(actual.output_tokens)],
      ['llama.cpp timings', statsFmt(local.total_tokens), statsFmt(local.requests) + ' 条 · 输入 ' + statsFmt(local.input_tokens) + ' / 输出 ' + statsFmt(local.output_tokens)],
      ['来源未知 Token', statsFmt(unknown.total_tokens), statsFmt(unknown.requests) + ' 条 · 不推测计费来源'],
    ];
    const tiles = cards.map(([title, value, note]) =>
      '<div class="card" style="padding:10px;min-width:0"><div class="small muted">' + esc(title) +
      '</div><div style="font-size:22px;font-weight:650;margin:4px 0;overflow-wrap:anywhere">' + esc(value) +
      '</div><div class="small muted">' + esc(note) + '</div></div>'
    ).join('');
    const buckets = Array.isArray(lat.buckets) ? lat.buckets : [];
    const maxBucket = Math.max(1, ...buckets.map(x => Number(x.count || 0)));
    const distribution = buckets.map(x => {
      const count = Number(x.count || 0);
      const width = Math.max(0, Math.min(100, count / maxBucket * 100));
      return '<div style="display:grid;grid-template-columns:76px minmax(50px,1fr) 64px;gap:8px;align-items:center;margin:7px 0">' +
        '<span class="small">' + esc(x.label || '') + '</span><div style="height:7px;background:var(--border,#ddd);border-radius:4px;overflow:hidden">' +
        '<div style="height:100%;width:' + width.toFixed(1) + '%;background:var(--accent,#4f83cc)"></div></div>' +
        '<span class="small" style="text-align:right">' + statsFmt(count) + '</span></div>';
    }).join('');
    const from = s.from ? new Date(s.from).toLocaleString() : '—';
    const to = s.to ? new Date(s.to).toLocaleString() : '—';
    el.innerHTML =
      '<div class="small muted">统计区间：' + esc(from) + ' 至 ' + esc(to) +
      (s.provider_id ? ' · Provider：' + esc(s.provider_id) : ' · 全部 Provider') +
      '。统计按请求开始时间归属，成功率分母为已结束请求。</div>' +
      '<div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(155px,1fr));gap:8px;margin:10px 0">' + tiles + '</div>' +
      '<div class="card" style="padding:12px"><div style="font-weight:600;margin-bottom:6px">延迟分布</div>' +
      '<div class="small muted">仅统计已结束且有延迟值的请求，共 ' + statsFmt(lat.sample_count) + ' 条；横条用于比较各区间数量。</div>' +
      (distribution || '<span class="muted small">暂无数据</span>') + '</div>' +
      '<p class="small muted" style="margin:8px 0">Token 口径：上游 usage 是上游响应提供的 usage；llama.cpp timings 是本地引擎计数，不等同于供应商计费数据；来源未知的数据单独展示，不会推断为真实 usage。</p>';
  } catch (e) {
    if (epoch !== requestStatsEpoch) return;
    if (!el.hasChildNodes()) el.innerHTML = '<span class="err">' + esc(e.message || '统计加载失败') + '</span>';
  }
}

export async function loadRequests(options = {}) {
  const tableEl = $('requestsTable');
  if (!tableEl) return;
  const epoch = ++requestLoadEpoch;
  const q = new URLSearchParams();
  q.set('limit', '100');
  if ($('rqModel').value) q.set('model', $('rqModel').value);
  if ($('rqStatus').value) q.set('status', $('rqStatus').value);
  if ($('rqKey').value) q.set('key', $('rqKey').value);
  if ($('rqProvider').value.trim()) q.set('provider', $('rqProvider').value.trim());
  const queryKey = q.toString();
  loadRequestStats(Boolean(options.forceStats));
  tableEl.setAttribute('aria-busy', 'true');
  if (!tableEl.hasChildNodes()) tableEl.innerHTML = '<p class="muted small">正在加载调用记录…</p>';
  try {
    const j = await api('/api/requests?' + queryKey);
    if (epoch !== requestLoadEpoch) return;
    const rows = j.data || [];
    const snapshot = JSON.stringify(rows);
    if (snapshot === requestSnapshot && queryKey === tableEl.dataset.queryKey) return;
    requestSnapshot = snapshot;
    tableEl.dataset.queryKey = queryKey;
    tableEl.innerHTML = !rows.length
      ? emptyState('暂无记录')
      : wrapTable(
          '<tr><th>时间</th><th>客户端 / 密钥</th><th>请求 URL</th><th>Provider / 模型</th><th>状态 / 流式</th><th>HTTP</th><th>耗时 / TTFT</th><th>Token / 体积</th><th>操作</th></tr>' +
            rows
              .map((x) => {
                const rid = String(x.request_id || '');
                const ridShort = rid.length > 10 ? rid.slice(0, 8) + '…' : rid;
                const target = displayRequestTarget(x);
                const model = displayUpstreamModel(x);
                const sz =
                  (Number(x.request_bytes || 0) / 1024).toFixed(1) +
                  ' / ' +
                  (Number(x.response_bytes || 0) / 1024).toFixed(1) +
                  ' KB';
                const stopBtn =
                  x.status === 'running'
                    ? `<button type="button" class="btn sm danger" onclick="cancelRequest('${encodeURIComponent(x.request_id).replace(/'/g, "%27")}')">结束</button>`
                    : '';
                return `<tr><td class="small req-time">${fmtDate(x.started_at)}</td><td><div class="req-cell-main">${esc(x.client_name || '—')}</div><div class="req-cell-sub">密钥：${esc(x.api_key_name || '—')}</div><div class="req-cell-sub" title="${esc(rid)}">ID：<code>${esc(ridShort)}</code></div></td><td class="small req-url" title="${esc(target)}">${esc(target)}</td><td><div class="req-cell-main"><code>${esc(x.provider_id || '—')}</code></div><div class="req-cell-sub" title="${esc(model)}">模型：${esc(model)}</div></td><td><div>${statusPill(x.status)}</div><div class="req-cell-sub">${x.stream ? '流式' : x.ttft_ms != null && x.ttft_ms > 0 ? '推断流式' : '非流式'}</div></td><td>${x.http_status}${x.upstream_http_status != null && x.upstream_http_status !== x.http_status ? ' / ' + x.upstream_http_status : ''}</td><td><div class="req-cell-main">${x.latency_ms != null ? x.latency_ms + ' ms' : '—'}</div><div class="req-cell-sub">TTFT：${x.ttft_ms != null ? x.ttft_ms + ' ms' : '—'}</div></td><td><div class="req-cell-main">${x.total_tokens ?? '—'}</div><div class="req-cell-sub">体积：${sz}</div></td><td><div class="td-actions"><button type="button" class="btn sm" onclick="showDetail('${encodeURIComponent(x.request_id).replace(/'/g, "%27")}')">详情</button>${stopBtn}</div></td></tr>`;
              })
              .join(''),
        );
  } catch (e) {
    if (epoch !== requestLoadEpoch || options.silent) return;
    if (!requestSnapshot) {
      tableEl.innerHTML = '<div class="msg err" role="alert"><span>加载调用记录失败：' + esc(e.message || '未知错误') +
        '</span> <button type="button" class="btn sm ghost" onclick="loadRequests()">重试</button></div>';
    } else {
      showToast('调用记录刷新失败：' + (e.message || '未知错误'), 'err');
    }
  } finally {
    if (epoch === requestLoadEpoch) tableEl.setAttribute('aria-busy', 'false');
  }
}

export async function clearRequests() {
  if (!confirm('确定清空全部调用记录？（含 payload 表）')) return;
  try {
    const j = await api('/api/requests', { method: 'DELETE' });
    setGlobalMsg('<span class="ok">已删除 ' + esc(j.deleted) + ' 条记录</span>');
    loadRequests();
  } catch (e) {
    setGlobalMsg('<span class="err">' + esc(e.message) + '</span>');
  }
}

export function reqKv(rows) {
  return (
    '<dl class="req-kv">' +
    rows
      .filter((x) => x[1] != null && x[1] !== '' && x[1] !== '—')
      .map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`)
      .join('') +
    '</dl>'
  );
}

export function prettyBodyText(s) {
  if (s == null || s === '') return '—';
  const t = String(s).trim();
  if (t.startsWith('data:') || /\ndata:\s/.test(t)) return String(s);
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return String(s);
  }
}

export function reqPanelFold(title, innerHtml) {
  return `<section class="req-section req-panel-fold"><details class="req-fold req-body-fold"><summary>${esc(title)}</summary><div class="req-fold-inner">${innerHtml}</div></details></section>`;
}

export function reqCodeBlock(text, large) {
  const cls = 'req-pre' + (large ? ' req-pre-lg' : '');
  return `<div class="req-code-wrap"><div class="req-code-bar"><button type="button" class="btn sm ghost req-copy-btn" data-req-copy>复制</button></div><pre class="${cls}">${esc(text)}</pre></div>`;
}

export async function flashCopyBtn(btn, ok) {
  const prev = btn.textContent;
  btn.textContent = ok ? '已复制' : '失败';
  btn.disabled = !ok;
  setTimeout(() => {
    btn.textContent = prev;
    btn.disabled = false;
  }, 1400);
}

export async function copyReqCode(btn) {
  const pre = btn.closest('.req-code-wrap')?.querySelector('pre');
  if (!pre) return;
  await flashCopyBtn(btn, await writeClipboard(pre.textContent || ''));
}

export async function copyReqStreamAll(btn) {
  const pre = btn.closest('.req-fold-inner')?.querySelector('.req-sse-all-source pre');
  if (!pre) return;
  await flashCopyBtn(btn, await writeClipboard(pre.textContent || ''));
}

export async function writeClipboard(text) {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* 非安全上下文没有 clipboard */
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.cssText = 'position:fixed;left:-9999px;top:0;opacity:0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}

export function formatStreamEventBody(e) {
  const hasRaw = e.raw_line != null && String(e.raw_line).trim() !== '';
  if (hasRaw) return String(e.raw_line);
  const fallback = String(e.preview || '').trim();
  if (!fallback) return '（本条无 raw_line；多为旧记录，请对新请求查看完整 JSON）';
  return fallback + '\n（旧数据：仅为解析摘要，非原始 JSON）';
}

export function formatStreamEventsDump(evs) {
  return evs
    .map((e) => {
      const body = formatStreamEventBody(e);
      const meta = `#${e.seq} · ${e.at_ms} ms · ${e.kind || '?'}${e.field ? ' · ' + e.field : ''}${e.delta_len ? ' · +' + e.delta_len + ' 字符' : ''}`;
      return meta + '\n' + body;
    })
    .join('\n\n');
}

export function renderStreamEventsSection(j) {
  const evs = j.stream_events || [];
  if (!evs.length) return '';
  const n = j.stream_event_count ?? evs.length;
  const dump = formatStreamEventsDump(evs);
  let inner = '';
  if (j.stream_events_note) inner += `<p class="req-note">${esc(j.stream_events_note)}</p>`;
  inner += `<div class="req-stream-toolbar"><button type="button" class="btn sm ghost req-copy-btn" data-req-copy-sse-all>复制全部</button></div>`;
  inner += `<div class="req-sse-all-source" aria-hidden="true"><pre>${esc(dump)}</pre></div>`;
  inner += '<div class="req-stream-list">';
  for (const e of evs) {
    const body = formatStreamEventBody(e);
    const label = `<code>#${e.seq}</code> · ${e.at_ms} ms · ${esc(e.kind || '?')}${e.field ? ' · ' + esc(e.field) : ''}${e.delta_len ? ' · +' + e.delta_len + ' 字符' : ''}`;
    inner += `<details class="req-fold req-nested-fold"><summary>${label}</summary><div class="req-fold-inner">${reqCodeBlock(body, false)}</div></details>`;
  }
  inner += '</div>';
  return reqPanelFold(`流式 SSE 逐条 (${n})`, inner);
}

function headerRows(obj) {
  if (!obj || typeof obj !== 'object') return [];
  return Object.keys(obj)
    .sort((a, b) => a.localeCompare(b))
    .map((k) => [k, obj[k]]);
}

function serviceParamRows(obj) {
  if (!obj || typeof obj !== 'object') return [];
  return Object.keys(obj).map((k) => [k, typeof obj[k] === 'object' ? JSON.stringify(obj[k]) : String(obj[k])]);
}

export async function cancelRequest(id) {
  try {
    id = decodeURIComponent(id);
  } catch {
    /* 已经是明文 id */
  }
  if (!confirm('确定结束这条正在进行的调用？调用方会看到连接中断。')) return;
  try {
    await api('/api/requests/' + encodeURIComponent(id) + '/cancel', { method: 'POST' });
    showToast('已结束这条调用', 'ok');
    if ($('requests')?.classList.contains('active')) loadRequests();
    if (detailPollId === id) refreshDetailBody(id).catch(() => {});
  } catch (e) {
    showToast(e.message || '结束失败', 'err');
    if ($('requests')?.classList.contains('active')) loadRequests();
  }
}

function detailRequestId(j) {
  const s = j.summary || {};
  const log = j.log || {};
  return String(s.request_id || log.request_id || '');
}

function renderToolCallsSection(j) {
  const events = Array.isArray(j.tool_calls) ? j.tool_calls : [];
  if (!events.length) return '';
  const statusLabels = {
    succeeded: '成功',
    failed: '失败',
    rejected: '拒绝',
    cancelled: '已取消',
    timed_out: '超时',
  };
  const inner = '<div class="req-stream-list">' + events.map((event) => {
    const status = statusLabels[event.status] || event.status || '未知';
    const label = `#${event.seq} · 第${event.round}轮 · ${esc(event.tool_name || '未知工具')} · ${esc(status)} · ${Number(event.duration_ms || 0)} ms`;
    const rows = [
      ['状态', status],
      ['耗时', Number(event.duration_ms || 0) + ' ms'],
      ['结果大小', Number(event.result_bytes || 0).toLocaleString() + ' bytes'],
      ['错误分类', event.error_code || '—'],
      ['记录时间', event.created_at ? fmtDate(event.created_at) : '—'],
    ];
    return `<details class="req-fold req-nested-fold"><summary>${label}</summary><div class="req-fold-inner">${reqKv(rows)}</div></details>`;
  }).join('') + '</dl>';
  return reqPanelFold(`工具调用明细 (${events.length})`, inner);
}

function diagnosticBundle(j) {
  const s = j.summary || {};
  const log = j.log || {};
  const t = j.timing || {};
  const analysis = j.analysis || {};
  const payload = j.payload || {};
  const safeHeaders = (headers) => {
    if (!headers || typeof headers !== 'object') return {};
    const allowed = ['content-type', 'content-length'];
    return Object.fromEntries(Object.entries(headers).filter(([key]) => allowed.includes(key.toLowerCase())));
  };
  const parseBody = (value) => {
    if (!value) return null;
    try { return JSON.parse(value); } catch { return String(value).slice(0, 12000); }
  };
  return {
    report_type: 'llm_gateway_request_diagnostic', report_version: 1,
    request: {
      request_id: detailRequestId(j), status: s.status || log.status || null,
      gateway_http_status: s.http_status ?? log.http_status ?? null,
      upstream_http_status: s.upstream_http_status ?? log.upstream_http_status ?? null,
      error_code: s.error_code || log.error_code || null, error_message: s.error_message || log.error_message || null,
      provider_id: s.provider_id || log.provider_id || null,
      model_client: s.model_client || j.request_params?.model_client || log.model_requested || null,
      model_upstream: s.model_upstream || j.request_params?.model_upstream || log.model_actual || null,
      endpoint: s.endpoint || log.endpoint || null, method: s.method || log.method || null,
      stream: s.stream ?? log.stream ?? null, started_at: t.started_at || log.started_at || null,
      finished_at: t.finished_at || log.finished_at || null, latency_ms: t.latency_ms ?? log.latency_ms ?? null,
      input_tokens: t.input_tokens ?? log.input_tokens ?? null, output_tokens: t.output_tokens ?? log.output_tokens ?? null,
      total_tokens: t.total_tokens ?? log.total_tokens ?? null
    },
    analysis: { phases_ms: analysis.phases_ms || null, throughput: analysis.throughput || null,
      runtime_snapshot: analysis.runtime_snapshot || null, summary: analysis.summary || [],
      performance_diagnosis: analysis.performance_diagnosis || null,
      diagnosis: j.diagnosis || null, timeline: analysis.timeline || [] },
    tool_calls: Array.isArray(j.tool_calls) ? j.tool_calls : [],
    tool_call_count: j.tool_call_count ?? (Array.isArray(j.tool_calls) ? j.tool_calls.length : 0),
    request_params: j.request_params || null,
    recorded_request_body_note: j.request_body_note || j.payload_note || null,
    recorded_request_body: parseBody(j.request_body || j.request_preview || payload.request_body),
    upstream_response_body: parseBody(j.response_body || j.response_preview || payload.response_body),
    upstream_headers: safeHeaders(j.upstream_headers || payload.upstream_headers)
  };
}

export async function copyDiagnosticBundle(btn) {
  const j = window.__requestDetailSnapshot || {};
  const bundle = diagnosticBundle(j);
  if (!bundle.request.request_id) { await flashCopyBtn(btn, false); return; }
  await flashCopyBtn(btn, await writeClipboard(JSON.stringify(bundle, null, 2)));
}
function renderPerformanceDiagnosis(d) {
  if (!d || typeof d !== 'object') return '';
  const statusLabels = {
    observed_only: '已有观测，尚未确认瓶颈',
    insufficient_data: '性能证据不足'
  };
  const categoryLabels = {
    gateway_pre_inference: 'Gateway 推理前阶段',
    provider_queue: 'Provider 并发排队',
    prompt_eval: 'Prompt Eval',
    decode: 'Decode 生成',
    upstream_headers_wait: '上游响应头等待',
    upstream_body_first_byte_wait: '上游响应头到首字节等待'
  };
  const findings = Array.isArray(d.candidates) ? d.candidates : [];
  const limitations = Array.isArray(d.limitations) ? d.limitations : [];
  const findingHtml = findings.map((item) => {
    const evidence = Array.isArray(item.evidence) ? item.evidence : [];
    const evidenceHtml = evidence.length
      ? '<dl class="req-kv">' + evidence.map((e) =>
          '<dt>' + esc(e.metric || '指标') + '</dt><dd>' + esc(String(e.value)) + ' ' + esc(e.unit || '') +
          '<span class="muted"> · ' + esc(e.source || '') + ' · ' + esc(e.scope || '') +
          (e.note ? ' · ' + esc(e.note) : '') + '</span></dd>'
        ).join('') + '</div>'
      : '<p class="req-note">没有可展示的原始证据。</p>';
    const next = Array.isArray(item.next_steps) && item.next_steps.length
      ? '<ul class="req-hints">' + item.next_steps.map((x) => '<li>' + esc(x) + '</li>').join('') + '</ul>'
      : '';
    return '<div class="card" style="padding:10px;margin:8px 0"><strong>' +
      esc(categoryLabels[item.category] || item.title || item.category || '待排查方向') +
      '</strong><p class="req-note">' + esc(item.summary || '') + '</p>' + evidenceHtml + next + '</div>';
  }).join('');
  const limitationHtml = limitations.length
    ? '<ul class="req-hints">' + limitations.map((x) => '<li>' + esc(x) + '</li>').join('') + '</ul>'
    : '';
  const primary = d.primary_bottleneck
    ? '<p><strong>已确认主瓶颈：</strong>' + esc(d.primary_bottleneck.title || d.primary_bottleneck.category || '') + '</p>'
    : '<p class="req-note">当前没有足够的同条件基线或性能 SLA，以下仅是候选排查方向，不代表已确认瓶颈。</p>';
  return '<section class="req-section"><h3>性能诊断</h3><p><strong>' +
    esc(statusLabels[d.status] || d.status || '状态未知') + '</strong></p>' + primary +
    (findingHtml || '<p class="req-note">没有可用的候选阶段证据。</p>') +
    (limitationHtml ? '<h4>结论限制</h4>' + limitationHtml : '') + '</section>';
}

function detailSections(j) {
  const s = j.summary || {};
  const t = j.timing || {};
  const log = j.log || {};
  const sections = [];
  const add = (id, kind, html) => {
    if (html) sections.push({ id, kind, html });
  };
  const rid = detailRequestId(j);
  const running = j.live || s.status === 'running' || log.status === 'running';
  if (running && rid) {
    add(
      'running',
      'swap',
      `<p style="margin:0 0 10px"><button type="button" class="btn sm danger" onclick="cancelRequest('${encodeURIComponent(rid).replace(/'/g, "%27")}')">结束这条调用</button></p>`,
    );
  }
  add('diagnostic-copy', 'swap', '<section class="req-section req-diagnostic-copy"><div><h3>问题反馈</h3><p class="req-note">复制精简诊断包，包含请求错误、模型耗时、工具调用轮次和必要请求参数；不包含 API Key 凭证及请求头敏感字段。</p></div><button type="button" class="btn sm" data-req-copy-diagnostic>复制诊断信息</button></section>');
  if (j.payload_note) add('note', 'swap', `<p class="req-note warn" style="margin:0 0 10px">${esc(j.payload_note)}</p>`);
  add(
    'overview',
    'swap',
    `<section class="req-section"><h3>概览</h3>${reqKv([
      ['Request ID', s.request_id || log.request_id],
      ['状态', (s.status || log.status) + ' · Gateway HTTP ' + (s.http_status ?? log.http_status ?? '—') + ((s.upstream_http_status ?? log.upstream_http_status) != null ? ' · 上游 HTTP ' + (s.upstream_http_status ?? log.upstream_http_status) : '')],
      ['请求 URL', displayRequestTarget({ method: s.method || log.method, request_url: s.request_url || log.request_url, endpoint: s.endpoint || log.endpoint })],
      ['API Key', (s.api_key || log.api_key_name || '—') + (s.api_key_id ? ' (#' + s.api_key_id + ')' : '')],
      ['客户端', s.client_name || log.client_name || '—'],
      ['IP', s.client_ip || log.client_ip],
      ['错误码', s.error_code || log.error_code],
      ['错误信息', s.error_message || log.error_message],
    ])}</section>`,
  );
  const clientHeaders = headerRows(j.request_headers);
  const upstreamHeaders = headerRows(j.upstream_headers);
  let headerInner = '';
  if (j.headers_note) headerInner += `<p class="req-note">${esc(j.headers_note)}</p>`;
  if (clientHeaders.length) {
    headerInner += `<h4>客户端 → 网关</h4>${reqKv(clientHeaders)}`;
  }
  if (upstreamHeaders.length) {
    headerInner += `<h4>网关 → 上游</h4>${reqKv(upstreamHeaders)}`;
  }
  if (headerInner) add('headers', 'swap', `<section class="req-section"><h3>请求头</h3>${headerInner}</section>`);
  const p = j.request_params || {};
  const paramRows = [
    ['Provider ID', log.provider_id || s.provider_id || '—'],
    ['客户端 model', p.model_client || s.model_client || log.model_requested || '—'],
    ['上游 model', p.model_upstream || s.model_upstream || log.model_actual || '—'],
    ['模型说明', p.model_note || ''],
    ['流式', p.stream_label || ''],
    ['流式说明', p.stream_note || ''],
    ['思考', p.thinking || ''],
    ['要求返回 token', p.include_usage_label || ''],
    ...serviceParamRows(p.service_params),
  ];
  add('params', 'swap', `<section class="req-section"><h3>请求参数</h3>${reqKv(paramRows)}</section>`);
  add(
    'timing',
    'swap',
    `<section class="req-section"><h3>耗时与 Token</h3>${reqKv([
      ['开始', fmtDate(t.started_at || log.started_at)],
      ['结束', fmtDate(t.finished_at || log.finished_at)],
      ['总耗时', t.latency_human || (t.latency_ms ?? log.latency_ms) + ' ms'],
      ['首 token (TTFT)', t.ttft_human || (t.ttft_ms != null ? t.ttft_ms + ' ms' : '—')],
      ['生成阶段', t.generation_human || (t.generation_ms != null ? t.generation_ms + ' ms' : '—')],
      [
        'Token in/out/total',
        [t.input_tokens ?? log.input_tokens, t.output_tokens ?? log.output_tokens, t.total_tokens ?? log.total_tokens]
          .map((x) => x ?? '—')
          .join(' / '),
      ],
      ['Token 来源', t.token_source_label || ''],
      ['请求/响应大小', Number(s.request_kb || 0).toFixed(1) + ' KB / ' + Number(s.response_kb || 0).toFixed(1) + ' KB'],
    ])}${j.token_note ? `<p class="req-note">${esc(j.token_note)}</p>` : ''}</section>`,
  );
  if (j.analysis && !j.analysis.timeline_only) {
    const ph = j.analysis.phases_ms || {};
    const tp = j.analysis.throughput || {};
    let block = `<section class="req-section"><h3>请求分析</h3>${reqKv([
      ['Gateway 准备', ph.prep_ms != null ? ph.prep_ms + ' ms' : null],
      ['模型加载/等待', ph.model_load_ms != null ? ph.model_load_ms + ' ms' : null],
      ['Provider 队列等待', ph.provider_queue_wait_ms != null ? ph.provider_queue_wait_ms + ' ms' : null],
      ['Provider 队列状态', ph.provider_queue_wait_status || null],
      ['上游响应头', ph.time_to_upstream_headers_ms != null ? ph.time_to_upstream_headers_ms + ' ms' : null],
      ['首字节到达', ph.time_to_first_byte_ms != null ? ph.time_to_first_byte_ms + ' ms' : null],
      ['首个实际输出 (TTFT)', ph.ttft_ms != null ? ph.ttft_ms + ' ms' : null],
      ['上游推理', ph.upstream_ms != null ? ph.upstream_ms + ' ms' : null],
      ['首条正文 content', ph.ttft_content_ms != null ? ph.ttft_content_ms + ' ms' : null],
      ['Prefill→首 token', ph.prefill_until_first_ms != null ? ph.prefill_until_first_ms + ' ms' : null],
      ['思考阶段', ph.thinking_ms != null ? ph.thinking_ms + ' ms' : null],
      ['正文生成阶段', ph.content_generation_ms != null ? ph.content_generation_ms + ' ms' : null],
      ['Token 数来源', tp.token_source === 'upstream_usage' ? '上游 usage' : tp.token_source === 'llama_timings' ? 'llama.cpp timings' : (tp.token_source || '未知')],
      ['速度来源', tp.speed_source_label || (tp.speed_source === 'llama_timings' ? 'llama.cpp Decode 实测' : tp.speed_source === 'gateway_window' ? 'Gateway 生成窗口派生' : '未知')],
      [
        '输出速度',
        tp.output_tokens_per_sec != null
          ? tp.output_tokens_per_sec.toFixed(2) + ' tok/s' + (tp.speed_source === 'gateway_window' && Number(tp.generation_window_ms) > 0 ? ' (' + tp.generation_window_ms + ' ms Gateway 窗口)' : tp.speed_source === 'llama_timings' ? '（llama.cpp Decode 实测）' : '（速度来源未知）')
          : null,
      ],
      ['Prompt Eval Token（非缓存）', tp.llama_timings?.prompt_n ?? null],
      ['缓存 Token（llama.cpp cache_n）', tp.llama_timings?.cache_n ?? null],
      ['缓存 Token（上游 usage）', tp.llama_timings?.usage_cached_tokens ?? null],
      ['缓存 Token（/completion）', tp.llama_timings?.tokens_cached ?? null],
      ['Tokens Evaluated（上游字段）', tp.llama_timings?.tokens_evaluated ?? null],
      ['Prompt Eval 耗时', tp.llama_timings?.prompt_ms != null ? tp.llama_timings.prompt_ms + ' ms' : null],
      ['Prompt Eval 每 Token', tp.llama_timings?.prompt_per_token_ms != null ? Number(tp.llama_timings.prompt_per_token_ms).toFixed(4) + ' ms/token' : null],
      ['Prompt Eval 速度', tp.llama_timings?.prompt_per_second != null ? tp.llama_timings.prompt_per_second.toFixed(2) + ' token/s' : null],
      ['Decode Token', tp.llama_timings?.predicted_n ?? null],
      ['Decode 耗时', tp.llama_timings?.predicted_ms != null ? tp.llama_timings.predicted_ms + ' ms' : null],
      ['Decode 每 Token', tp.llama_timings?.predicted_per_token_ms != null ? Number(tp.llama_timings.predicted_per_token_ms).toFixed(4) + ' ms/token' : null],
      ['Decode 速度（原始 timings）', tp.llama_timings?.predicted_per_second != null ? tp.llama_timings.predicted_per_second.toFixed(2) + ' token/s' : null],
    ])}`;
    const progressSamples = Array.isArray(tp.llama_timings?.prompt_progress) ? tp.llama_timings.prompt_progress : [];
    if (progressSamples.length) {
      const shown = progressSamples.slice(-20);
      const progressRows = shown.map((sample, index) => {
        const total = Number(sample.total), cache = Number(sample.cache), processed = Number(sample.processed), timeMS = Number(sample.time_ms);
        const denominator = total - cache, numerator = processed - cache;
        const percent = Number.isFinite(denominator) && denominator > 0 && Number.isFinite(numerator) &&
          numerator >= 0 && numerator <= denominator ? (numerator / denominator * 100).toFixed(1) + '%' : '未知';
        return reqKv([
          ['采样序号', String(progressSamples.length - shown.length + index + 1)],
          ['已处理 / 总量', Number.isFinite(processed) && Number.isFinite(total) ? processed + ' / ' + total : '未知'],
          ['缓存 Token', Number.isFinite(cache) ? cache : '未知'],
          ['非缓存 Prompt 进度', percent],
          ['上游耗时', Number.isFinite(timeMS) ? timeMS + ' ms' : '未知'],
        ]);
      }).join('');
      block += reqPanelFold('Prompt progress（' + progressSamples.length + ' 个采样点，展示最近 ' + shown.length + ' 个）',
        progressRows + '<p class="req-note">进度按 (processed - cache) / (total - cache) 计算；分母无效或计数越界时不推算百分比。</p>');
    }
    const runtime = j.analysis.runtime_snapshot || null;
    if (runtime) {
      const runtimeRows = [
        ['本地运行时状态', runtime.status || 'unknown'],
        ['Backend', runtime.backend || null],
        ['活动模型', runtime.active_model || null],
        ['已加载模型数', runtime.loaded_model_count ?? null],
        ['模型 RAM', runtime.loaded_ram_bytes != null ? (runtime.loaded_ram_bytes / 1073741824).toFixed(2) + ' GiB' : null],
        ['模型 VRAM', runtime.loaded_vram_bytes != null ? (runtime.loaded_vram_bytes / 1073741824).toFixed(2) + ' GiB' : null],
        ['主机 CPU', runtime.host_cpu_percent != null ? runtime.host_cpu_percent.toFixed(1) + '%' : null],
        ['采样时间', runtime.sampled_at || null],
        ['采样年龄', runtime.age_ms != null ? runtime.age_ms + ' ms' : null],
        ['快照来源', runtime.source || null],
      ];
      block += `<h4>请求结束时最近的运行时快照</h4>${reqKv(runtimeRows)}`;
      block += '<p class="req-note">运行时数据来自后台低频采样，取请求结束时最近的一份快照；它不保证采样发生在本次请求执行区间内，也不代表请求瞬时硬件状态。状态和采样年龄用于判断数据是否可用。</p>';
    }
    if (j.analysis_note) block += `<p class="req-note">${esc(j.analysis_note)}</p>`;
    if (tp.note) block += `<p class="req-note">${esc(tp.note)}</p>`;
    if ((j.analysis.summary || []).length) {
      block += `<ul class="req-hints">${j.analysis.summary.map((line) => `<li>${esc(line)}</li>`).join('')}</ul>`;
    }
    block += renderPerformanceDiagnosis(j.analysis.performance_diagnosis);
    block += `</section>`;
    add('analysis', 'swap', block);
  }
  const timelineEvents = Array.isArray(j.analysis?.timeline) ? j.analysis.timeline : [];
  if (timelineEvents.length) {
    const eventLabels = {
      request_received: '请求进入 Gateway',
      auth_started: '鉴权开始',
      auth_finished: '鉴权结束',
      client_rpm_started: '客户端 RPM 检查开始',
      client_rpm_finished: '客户端 RPM 检查结束',
      body_read_started: '读取请求体开始',
      body_read_finished: '读取请求体结束',
      pipeline_started: 'Pipeline 开始',
      prepared: '请求解析与改写完成',
      ready: '模型与请求就绪',
      warm_finished: 'WarmStart 完成',
      upstream_started: '进入 Provider 调用',
      provider_queue_wait: 'Provider 队列等待结果',
      upstream_dispatched: '上游请求发出',
      upstream_headers: '收到上游响应头',
      upstream_first_byte: '读取上游首字节',
      stream_first_any: '收到首个流式输出',
      stream_first_reasoning: '收到首个 reasoning',
      stream_first_content: '收到首个 content',
      gateway_first_write: 'Gateway 首次写回正文',
      gateway_last_write: 'Gateway 最后一次写回正文',
      gateway_write_failed: 'Gateway 正文写回失败',
      pipeline_finished: 'Pipeline 收尾完成',
      handler_exit_started: 'HTTP Handler 收尾开始',
    };
    const statusLabels = {
      measured: '已测量',
      not_applicable: '不适用',
      not_reached: '未到达',
      unknown: '未知',
    };
    const rows = timelineEvents.map((event, index) => {
      const label = eventLabels[event.name] || event.name || '未知事件';
      const offset = event.offset_ms != null ? Number(event.offset_ms) + ' ms' : '—';
      const status = statusLabels[event.status] || event.status || '未知';
      return [String(index + 1) + '. ' + label, offset + ' · ' + status];
    });
    add('timeline', 'fold',
      reqPanelFold('请求时间线 (' + timelineEvents.length + ')',
        '<p class="req-note">时间相对 HTTP Handler 入口；只显示真实记录的事件偏移，未测量事件不会补成 0 ms。</p>' +
        reqKv(rows)));
  }
  const hints = j.diagnosis || [];
  if (hints.length) {
    add(
      'hints',
      'swap',
      `<section class="req-section"><h3>诊断提示</h3><ul class="req-hints">${hints.map((h) => `<li>${esc(h)}</li>`).join('')}</ul></section>`,
    );
  }
  if (j.content_text) {
    add('content', 'text', `<section class="req-section"><h3>聚合正文 content</h3><div class="req-text-block">${esc(j.content_text)}</div></section>`);
  }
  if (j.reasoning_text) {
    add(
      'reasoning',
      'text',
      `<section class="req-section"><h3>聚合思考 reasoning</h3><div class="req-text-block reasoning">${esc(j.reasoning_text)}</div></section>`,
    );
  }
  const stream = renderStreamEventsSection(j);
  if (stream) add('stream', 'stream', stream);
  const toolCalls = renderToolCallsSection(j);
  if (toolCalls) add('tool-calls', 'fold', toolCalls);
  const reqBody = j.request_body || j.request_preview || '';
  if (reqBody) {
    let inner = '';
    if (j.request_body_note) inner += `<p class="req-note">${esc(j.request_body_note)}</p>`;
    else inner += `<p class="req-note">Gateway 记录并转发给上游的 JSON body</p>`;
    if (j.request_body_bytes) inner += `<p class="req-note">${j.request_body_bytes} bytes</p>`;
    inner += reqCodeBlock(prettyBodyText(reqBody), true);
    add('request', 'fold', reqPanelFold('原始请求 body', inner));
  }
  const resBody = j.response_body || j.response_preview || '';
  if (resBody) {
    let inner = `<p class="req-note">${j.response_body_bytes ? j.response_body_bytes + ' bytes · ' : ''}流式为完整 SSE 文本；非流式为上游 JSON</p>`;
    inner += reqCodeBlock(prettyBodyText(resBody), true);
    add('response', 'fold', reqPanelFold('原始响应 body', inner));
  }
  add('raw', 'fold', reqPanelFold('API 原始 JSON（调试）', reqCodeBlock(JSON.stringify(j, null, 2), true)));
  return sections;
}

function htmlToSection(id, html) {
  const tpl = document.createElement('template');
  tpl.innerHTML = html.trim();
  const el = tpl.content.firstElementChild;
  el.dataset.detailSection = id;
  return el;
}

function patchTextSection(el, html) {
  const neu = htmlToSection('tmp', html);
  const a = el.querySelector('.req-text-block');
  const b = neu.querySelector('.req-text-block');
  if (a && b) {
    if (a.textContent !== b.textContent) a.textContent = b.textContent;
    return el;
  }
  neu.dataset.detailSection = el.dataset.detailSection;
  el.replaceWith(neu);
  return neu;
}

function patchFoldSection(el, html) {
  const neu = htmlToSection('tmp', html);
  const sum = el.querySelector('details > summary');
  const nsum = neu.querySelector('details > summary');
  if (sum && nsum && sum.textContent !== nsum.textContent) sum.textContent = nsum.textContent;
  const pres = [...el.querySelectorAll('pre')];
  const npres = [...neu.querySelectorAll('pre')];
  const notes = [...el.querySelectorAll('.req-note')];
  const nnotes = [...neu.querySelectorAll('.req-note')];
  if (pres.length && pres.length === npres.length && notes.length === nnotes.length) {
    pres.forEach((p, i) => {
      if (p.textContent !== npres[i].textContent) p.textContent = npres[i].textContent;
    });
    notes.forEach((n, i) => {
      if (n.textContent !== nnotes[i].textContent) n.textContent = nnotes[i].textContent;
    });
    return;
  }
  const inner = el.querySelector('.req-fold-inner');
  const ninner = neu.querySelector('.req-fold-inner');
  if (inner && ninner) inner.replaceWith(ninner);
}

function streamRowLabel(node) {
  return node.querySelector('summary')?.textContent || '';
}

function patchStreamSection(el, html) {
  const neu = htmlToSection('tmp', html);
  const sum = el.querySelector('details > summary');
  const nsum = neu.querySelector('details > summary');
  if (sum && nsum && sum.textContent !== nsum.textContent) sum.textContent = nsum.textContent;
  const list = el.querySelector('.req-stream-list');
  const nlist = neu.querySelector('.req-stream-list');
  if (list && nlist) {
    const haveNodes = [...list.querySelectorAll(':scope > details')];
    const items = [...nlist.querySelectorAll(':scope > details')];
    const prefixOk =
      haveNodes.length <= items.length &&
      haveNodes.every((node, i) => streamRowLabel(node) === streamRowLabel(items[i]));
    if (!prefixOk) list.replaceChildren(...items);
    else {
      for (let i = haveNodes.length; i < items.length; i++) list.appendChild(items[i]);
    }
  }
  const dump = el.querySelector('.req-sse-all-source pre');
  const ndump = neu.querySelector('.req-sse-all-source pre');
  if (dump && ndump && dump.textContent !== ndump.textContent) dump.textContent = ndump.textContent;
  const note = el.querySelector('.req-note');
  const nnote = neu.querySelector('.req-note');
  if (note && nnote && note.textContent !== nnote.textContent) note.textContent = nnote.textContent;
}

// 同一条记录轮询时只改变化的区块，折叠和滚动留在原节点上。
// 换记录，或关闭后再打开，则整页重画，不沿用上一份详情。
export function applyRequestDetail(root, j) {
  window.__requestDetailSnapshot = j;
  const rid = detailRequestId(j);
  if (root.dataset.detailRequest !== rid) {
    root.replaceChildren();
    root.dataset.detailRequest = rid;
    const card = root.closest('.card');
    if (card) card.scrollTop = 0;
  }
  const sections = detailSections(j);
  const want = new Set(sections.map((s) => s.id));
  [...root.children].forEach((el) => {
    if (!want.has(el.dataset.detailSection)) el.remove();
  });
  sections.forEach((sec, i) => {
    let el = [...root.children].find((n) => n.dataset.detailSection === sec.id);
    if (!el) {
      el = htmlToSection(sec.id, sec.html);
      el._detailHtml = sec.html;
      root.insertBefore(el, root.children[i] || null);
      return;
    }
    if (root.children[i] !== el) root.insertBefore(el, root.children[i] || null);
    if (el._detailHtml === sec.html) return;
    let live = el;
    if (sec.kind === 'text') live = patchTextSection(el, sec.html);
    else if (sec.kind === 'fold') patchFoldSection(el, sec.html);
    else if (sec.kind === 'stream') patchStreamSection(el, sec.html);
    else {
      const neu = htmlToSection(sec.id, sec.html);
      neu._detailHtml = sec.html;
      el.replaceWith(neu);
      return;
    }
    live._detailHtml = sec.html;
  });
}

async function refreshDetailBody(id) {
  const epoch = detailEpoch;
  const j = await api('/api/requests/' + encodeURIComponent(id));
  if (epoch !== detailEpoch || detailPollId !== id) return j;
  const b = $('detailBody');
  if (b) applyRequestDetail(b, j);
  return j;
}

function stopDetailPoll() {
  if (detailPollTimer) {
    clearInterval(detailPollTimer);
    detailPollTimer = null;
  }
  detailPollId = '';
}

export async function showDetail(id) {
  let epoch = 0;
  detailTrigger = document.activeElement;
  try {
    id = decodeURIComponent(id);
    stopDetailPoll();
    detailEpoch += 1;
    epoch = detailEpoch;
    detailPollId = id;
    const j = await refreshDetailBody(id);
    if (epoch !== detailEpoch) return;
    const modal = $('detailModal');
    modal?.classList.add('show');
    modal?.setAttribute('aria-hidden', 'false');
    modal?.querySelector('.card-head button')?.focus();
    if (j.live) {
      detailPollTimer = setInterval(async () => {
        if (epoch !== detailEpoch || detailPollId !== id) return;
        if (!$('detailModal')?.classList.contains('show')) {
          stopDetailPoll();
          return;
        }
        try {
          const nj = await refreshDetailBody(id);
          if (epoch !== detailEpoch || detailPollId !== id) return;
          if (!nj.live) stopDetailPoll();
        } catch {
          if (epoch === detailEpoch && detailPollId === id) stopDetailPoll();
        }
      }, 2000);
    }
  } catch (e) {
    if (epoch && epoch !== detailEpoch) return;
    detailTrigger = null;
    alert(e.message);
  }
}

function resetDetailView() {
  const body = $('detailBody');
  if (!body) return;
  body.replaceChildren();
  delete body.dataset.detailRequest;
  const card = body.closest('.card');
  if (card) card.scrollTop = 0;
}

export function closeModal() {
  stopDetailPoll();
  resetDetailView();
  const modal = $('detailModal');
  modal?.classList.remove('show');
  modal?.setAttribute('aria-hidden', 'true');
  const trigger = detailTrigger;
  detailTrigger = null;
  if (trigger?.isConnected) trigger.focus();
}

$('detailModal')?.addEventListener('click', (e) => {
  if (e.target.id === 'detailModal') closeModal();
  const diagnosticBtn = e.target.closest?.('[data-req-copy-diagnostic]');
  if (diagnosticBtn) {
    e.preventDefault();
    copyDiagnosticBundle(diagnosticBtn);
    return;
  }
  const copyBtn = e.target.closest?.('[data-req-copy]');
  if (copyBtn) {
    e.preventDefault();
    copyReqCode(copyBtn);
    return;
  }
  const copyAllSse = e.target.closest?.('[data-req-copy-sse-all]');
  if (copyAllSse) {
    e.preventDefault();
    copyReqStreamAll(copyAllSse);
  }
});


document.addEventListener('keydown', (event) => {
  const modal = $('detailModal');
  if (!modal?.classList.contains('show')) return;
  if (event.key === 'Escape') {
    event.preventDefault();
    closeModal();
    return;
  }
  if (event.key !== 'Tab') return;
  const focusable = [...modal.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])')]
    .filter((el) => {
      const closedDetails = el.closest('details:not([open])');
      return !el.hidden && el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden' && (!closedDetails || el.matches('details:not([open]) > summary'));
    });
  if (!focusable.length) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  const active = document.activeElement;
  if (event.shiftKey && (active === first || !modal.contains(active))) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && (active === last || !modal.contains(active))) {
    event.preventDefault();
    first.focus();
  }
});
