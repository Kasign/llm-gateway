(() => {
  var __defProp = Object.defineProperty;
  var __getOwnPropNames = Object.getOwnPropertyNames;
  var __esm = (fn, res) => function __init() {
    return fn && (res = (0, fn[__getOwnPropNames(fn)[0]])(fn = 0)), res;
  };
  var __export = (target, all) => {
    for (var name in all)
      __defProp(target, name, { get: all[name], enumerable: true });
  };

  // js/expose.js
  function exposeGlobals(bindings) {
    Object.assign(globalThis, bindings);
  }
  var init_expose = __esm({
    "js/expose.js"() {
    }
  });

  // js/core/dom.js
  function esc(s) {
    return String(s ?? "").replace(
      /[&<>'"]/g,
      (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" })[c]
    );
  }
  var $;
  var init_dom = __esm({
    "js/core/dom.js"() {
      $ = (id) => document.getElementById(id);
    }
  });

  // js/core/api.js
  function setSessionToken(token) {
    const t = (token || "").trim();
    if (t) localStorage.setItem(SESSION_STORAGE_KEY, t);
    else localStorage.removeItem(SESSION_STORAGE_KEY);
  }
  function isAuthenticated() {
    return !!getSessionToken();
  }
  function initAdminKey() {
    const el = $("adminKey");
    if (el) el.value = localStorage.getItem("llm_gateway_admin_key") || "";
  }
  function redirectToLogin() {
    setSessionToken("");
    if (typeof globalThis.forceLoginScreen === "function") {
      globalThis.forceLoginScreen();
    }
  }
  function apiPath(url) {
    try {
      if (url.startsWith("http")) return new URL(url).pathname;
    } catch {
    }
    return url.split("?")[0];
  }
  function requireWebSession(url) {
    const path = apiPath(url);
    if (!path.startsWith("/api/")) return;
    if (PUBLIC_API_PATHS.has(path)) return;
    if (!getSessionToken()) {
      redirectToLogin();
      throw new Error("\u8BF7\u5148\u767B\u5F55");
    }
  }
  function authHeaders(extra = {}) {
    const headers = { ...extra };
    delete headers["X-Admin-Key"];
    const session = getSessionToken();
    if (session) headers.Authorization = `Bearer ${session}`;
    return headers;
  }
  async function apiPublic(url, opt = {}) {
    opt.headers = { ...opt.headers || {} };
    const r = await fetch(url, opt);
    let j = null;
    try {
      j = await r.json();
    } catch {
    }
    if (!r.ok) throw new Error(j?.error?.message || j?.message || `HTTP ${r.status}`);
    return j;
  }
  async function api(url, opt = {}) {
    requireWebSession(url);
    opt.headers = authHeaders(opt.headers || {});
    const r = await fetch(url, opt);
    let j = null;
    try {
      j = await r.json();
    } catch {
    }
    if (!r.ok) {
      const path = apiPath(url);
      if (r.status === 401 && path.startsWith("/api/") && !PUBLIC_API_PATHS.has(path)) {
        redirectToLogin();
      }
      throw new Error(j?.error?.message || j?.message || `HTTP ${r.status}`);
    }
    return j;
  }
  async function apiServiceKeys(url, opt = {}) {
    const path = apiPath(url);
    if (path !== "/api/admin/keys" && !path.startsWith("/api/admin/keys/")) {
      throw new Error("\u7BA1\u7406\u5458 API Key \u4EC5\u5141\u8BB8\u7528\u4E8E\u670D\u52A1\u5BC6\u94A5\u7BA1\u7406\u63A5\u53E3");
    }
    const key = getSavedAdminKey();
    if (!key) throw new Error("\u8BF7\u5148\u5728\u201C\u670D\u52A1\u5BC6\u94A5\u201D\u9875\u9762\u8F93\u5165\u5E76\u9A8C\u8BC1\u7BA1\u7406\u5458 API Key");
    const headers = { ...opt.headers || {} };
    delete headers.Authorization;
    headers["X-Admin-Key"] = key;
    const response = await fetch(url, { ...opt, headers });
    let result = null;
    try {
      result = await response.json();
    } catch {
    }
    if (!response.ok) {
      if (response.status === 401) {
        localStorage.removeItem("llm_gateway_admin_key");
        const input = $("adminKey");
        if (input) input.value = "";
        if (typeof globalThis.adminKeyStatus === "function") globalThis.adminKeyStatus();
      }
      throw new Error(result?.error?.message || result?.message || `HTTP ${response.status}`);
    }
    return result;
  }
  var SESSION_STORAGE_KEY, PUBLIC_API_PATHS, getKey, getSavedAdminKey, getSessionToken;
  var init_api = __esm({
    "js/core/api.js"() {
      init_dom();
      SESSION_STORAGE_KEY = "llm_gateway_session_token";
      PUBLIC_API_PATHS = /* @__PURE__ */ new Set(["/api/auth/login", "/api/auth/status"]);
      getKey = () => ($("adminKey")?.value || "").trim();
      getSavedAdminKey = () => localStorage.getItem("llm_gateway_admin_key") || "";
      getSessionToken = () => localStorage.getItem(SESSION_STORAGE_KEY) || "";
    }
  });

  // js/core/format.js
  function fmtSize(n) {
    n = Number(n || 0);
    if (!n) return "\u2014";
    const u = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return n.toFixed(i ? 2 : 0) + " " + u[i];
  }
  function fmtDate(s) {
    return s ? new Date(s).toLocaleString() : "\u2014";
  }
  function displayRequestTarget(row) {
    const method = String(row?.method || "").trim();
    const url = String(row?.request_url || "").trim() || String(row?.endpoint || "").trim() || "\u2014";
    return method ? method + " " + url : url;
  }
  function displayUpstreamModel(row) {
    const actual = String(row?.model_actual ?? "").trim();
    return actual || "\u2014";
  }
  function slug(s) {
    return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  }
  function statusLabel(s) {
    const m = {
      loaded: "\u5DF2\u52A0\u8F7D",
      unloaded: "\u5DF2\u5378\u8F7D",
      loading: "\u52A0\u8F7D\u4E2D",
      sleeping: "\u4F11\u7720",
      installed: "\u5DF2\u5B89\u88C5",
      registered: "\u5DF2\u6CE8\u518C",
      downloading: "\u4E0B\u8F7D\u4E2D",
      ready: "\u5DF2\u5B8C\u6210",
      download_failed: "\u4E0B\u8F7D\u5931\u8D25",
      download_cancelled: "\u5DF2\u53D6\u6D88\u4E0B\u8F7D",
      success: "\u6210\u529F",
      error: "\u5931\u8D25",
      active: "\u542F\u7528",
      disabled: "\u5DF2\u7981\u7528",
      available: "\u672A\u5728\u5185\u5B58",
      running: "\u8FD0\u884C\u4E2D",
      completed: "\u5DF2\u5B8C\u6210",
      failed: "\u5931\u8D25",
      cancelled: "\u5DF2\u505C\u6B62"
    };
    const k = String(s || "").toLowerCase();
    return m[k] || s || "\u2014";
  }
  function statusPill(s, extra = "") {
    const v = String(s || "").toLowerCase();
    let cls = "pill";
    if (v.includes("load") && !v.includes("unload")) cls += " pill-loaded";
    else if (v.includes("unload") || v === "installed" || v === "registered") cls += " pill-unloaded";
    else if (v.includes("download")) cls += " pill-downloading";
    else if (v.includes("fail") || v === "error") cls += " pill-error";
    else if (v === "success" || v === "active") cls += " pill-success";
    else if (v === "running") cls += " pill-running";
    else if (v.includes("cancel")) cls += " pill-warn";
    return `<span class="${cls}${extra}">${esc(statusLabel(s))}</span>`;
  }
  function emptyState(t) {
    return `<div class="empty">${esc(t)}</div>`;
  }
  function wrapTable(h) {
    return `<div class="table-wrap"><table>${h}</table></div>`;
  }
  var init_format = __esm({
    "js/core/format.js"() {
      init_dom();
    }
  });

  // js/features/admin-store.js
  var admin_store_exports = {};
  __export(admin_store_exports, {
    adminStore: () => adminStore
  });
  var adminStore;
  var init_admin_store = __esm({
    "js/features/admin-store.js"() {
      init_api();
      adminStore = {
        bootstrapped: false,
        upstream: null,
        inferenceSummary: {},
        dependencies: { upstream: "", redis: "" },
        host: null,
        applyBootstrap(j) {
          if (!j) return;
          this.bootstrapped = true;
          this.upstream = j.upstream || null;
          this.inferenceSummary = j.inference_summary || {};
          this.dependencies = j.dependencies || { upstream: "", redis: "" };
          this.host = j.host || null;
        },
        applyLite(j) {
          if (!j) return;
          if (j.inference_summary) this.inferenceSummary = j.inference_summary;
          if (j.dependencies) this.dependencies = j.dependencies;
          if (j.host) this.host = j.host;
        },
        inferenceForUI() {
          const out = {};
          for (const be of ["llamacpp", "ollama"]) {
            const sum = this.inferenceSummary[be];
            if (!sum) continue;
            out[be] = {
              reachable: sum.reachable === true,
              loaded_count: Number(sum.loaded_count) || 0,
              base_url: sum.base_url || ""
            };
          }
          return out;
        },
        /** @param {'bootstrap'|'lite'|'auto'} mode */
        async syncDashboard(mode = "auto") {
          if (mode === "bootstrap" || mode === "auto" && !this.bootstrapped) {
            const j2 = await api("/api/admin/bootstrap");
            this.applyBootstrap(j2);
            return j2;
          }
          const j = await api("/api/admin/bootstrap/lite");
          this.applyLite(j);
          return j;
        },
        reset() {
          this.bootstrapped = false;
          this.upstream = null;
          this.inferenceSummary = {};
          this.dependencies = { upstream: "", redis: "" };
          this.host = null;
        }
      };
    }
  });

  // js/core/notify.js
  function showToast(text, kind) {
    const host = $("toastHost");
    const msg = String(text || "").trim();
    if (!host || !msg) return;
    const el = document.createElement("div");
    el.className = "toast" + (kind ? " " + kind : "");
    el.textContent = msg;
    host.appendChild(el);
    const timer = setTimeout(() => el.remove(), 2800);
    el.addEventListener("click", () => {
      clearTimeout(timer);
      el.remove();
    });
  }
  function setGlobalMsg(html) {
    const gm = $("globalMsg");
    if (!gm) return;
    gm.innerHTML = html || "";
    const onDash = $("dashboard")?.classList.contains("active");
    gm.hidden = !html || onDash;
  }
  var init_notify = __esm({
    "js/core/notify.js"() {
      init_dom();
    }
  });

  // js/features/requests.js
  function requestsListHasRows() {
    return !!$("requestsTable")?.querySelector("table td");
  }
  function statsFmt(value) {
    const n = Number(value || 0);
    return Number.isFinite(n) ? Math.round(n).toLocaleString() : "0";
  }
  async function loadRequestStats() {
    const el = $("requestStats");
    if (!el) return;
    const q = new URLSearchParams();
    q.set("range", $("rqStatsRange")?.value || "24h");
    const provider = $("rqProvider")?.value?.trim();
    if (provider) q.set("provider", provider);
    el.innerHTML = '<p class="muted small">\u6B63\u5728\u6C47\u603B\u7EDF\u8BA1\u2026</p>';
    try {
      const s = await api("/api/requests/stats?" + q.toString());
      const r = s.requests || {};
      const lat = s.latency || {};
      const tok = s.tokens || {};
      const actual = tok.upstream_usage || {};
      const local = tok.llama_timings || {};
      const unknown = tok.unknown_source || {};
      const cards = [
        ["\u8BF7\u6C42\u603B\u91CF", statsFmt(r.total_requests), statsFmt(r.running_requests) + " \u6761\u8FDB\u884C\u4E2D"],
        ["\u6210\u529F\u7387", Number(r.success_rate_percent || 0).toFixed(1) + "%", statsFmt(r.success_requests) + " \u6210\u529F / " + statsFmt(r.completed_requests) + " \u5DF2\u7ED3\u675F"],
        ["\u5931\u8D25\u8BF7\u6C42", statsFmt(r.error_requests), statsFmt(r.cancelled_requests) + " \u6761\u5DF2\u53D6\u6D88"],
        ["\u5E73\u5747\u8017\u65F6", lat.average_ms == null ? "\u2014" : statsFmt(lat.average_ms) + " ms", statsFmt(lat.sample_count) + " \u6761\u5EF6\u8FDF\u6837\u672C"],
        ["\u4E0A\u6E38 usage Token", statsFmt(actual.total_tokens), statsFmt(actual.requests) + " \u6761 \xB7 \u8F93\u5165 " + statsFmt(actual.input_tokens) + " / \u8F93\u51FA " + statsFmt(actual.output_tokens)],
        ["llama.cpp timings", statsFmt(local.total_tokens), statsFmt(local.requests) + " \u6761 \xB7 \u8F93\u5165 " + statsFmt(local.input_tokens) + " / \u8F93\u51FA " + statsFmt(local.output_tokens)],
        ["\u6765\u6E90\u672A\u77E5 Token", statsFmt(unknown.total_tokens), statsFmt(unknown.requests) + " \u6761 \xB7 \u4E0D\u63A8\u6D4B\u8BA1\u8D39\u6765\u6E90"]
      ];
      const tiles = cards.map(
        ([title, value, note]) => '<div class="card" style="padding:10px;min-width:0"><div class="small muted">' + esc(title) + '</div><div style="font-size:22px;font-weight:650;margin:4px 0;overflow-wrap:anywhere">' + esc(value) + '</div><div class="small muted">' + esc(note) + "</div></div>"
      ).join("");
      const buckets = Array.isArray(lat.buckets) ? lat.buckets : [];
      const maxBucket = Math.max(1, ...buckets.map((x) => Number(x.count || 0)));
      const distribution = buckets.map((x) => {
        const count = Number(x.count || 0);
        const width = Math.max(0, Math.min(100, count / maxBucket * 100));
        return '<div style="display:grid;grid-template-columns:76px minmax(50px,1fr) 64px;gap:8px;align-items:center;margin:7px 0"><span class="small">' + esc(x.label || "") + '</span><div style="height:7px;background:var(--border,#ddd);border-radius:4px;overflow:hidden"><div style="height:100%;width:' + width.toFixed(1) + '%;background:var(--accent,#4f83cc)"></div></div><span class="small" style="text-align:right">' + statsFmt(count) + "</span></div>";
      }).join("");
      const from = s.from ? new Date(s.from).toLocaleString() : "\u2014";
      const to = s.to ? new Date(s.to).toLocaleString() : "\u2014";
      el.innerHTML = '<div class="small muted">\u7EDF\u8BA1\u533A\u95F4\uFF1A' + esc(from) + " \u81F3 " + esc(to) + (s.provider_id ? " \xB7 Provider\uFF1A" + esc(s.provider_id) : " \xB7 \u5168\u90E8 Provider") + '\u3002\u7EDF\u8BA1\u6309\u8BF7\u6C42\u5F00\u59CB\u65F6\u95F4\u5F52\u5C5E\uFF0C\u6210\u529F\u7387\u5206\u6BCD\u4E3A\u5DF2\u7ED3\u675F\u8BF7\u6C42\u3002</div><div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(155px,1fr));gap:8px;margin:10px 0">' + tiles + '</div><div class="card" style="padding:12px"><div style="font-weight:600;margin-bottom:6px">\u5EF6\u8FDF\u5206\u5E03</div><div class="small muted">\u4EC5\u7EDF\u8BA1\u5DF2\u7ED3\u675F\u4E14\u6709\u5EF6\u8FDF\u503C\u7684\u8BF7\u6C42\uFF0C\u5171 ' + statsFmt(lat.sample_count) + " \u6761\uFF1B\u6A2A\u6761\u7528\u4E8E\u6BD4\u8F83\u5404\u533A\u95F4\u6570\u91CF\u3002</div>" + (distribution || '<span class="muted small">\u6682\u65E0\u6570\u636E</span>') + '</div><p class="small muted" style="margin:8px 0">Token \u53E3\u5F84\uFF1A\u4E0A\u6E38 usage \u662F\u4E0A\u6E38\u54CD\u5E94\u63D0\u4F9B\u7684 usage\uFF1Bllama.cpp timings \u662F\u672C\u5730\u5F15\u64CE\u8BA1\u6570\uFF0C\u4E0D\u7B49\u540C\u4E8E\u4F9B\u5E94\u5546\u8BA1\u8D39\u6570\u636E\uFF1B\u6765\u6E90\u672A\u77E5\u7684\u6570\u636E\u5355\u72EC\u5C55\u793A\uFF0C\u4E0D\u4F1A\u63A8\u65AD\u4E3A\u771F\u5B9E usage\u3002</p>';
    } catch (e) {
      el.innerHTML = '<span class="err">' + esc(e.message || "\u7EDF\u8BA1\u52A0\u8F7D\u5931\u8D25") + "</span>";
    }
  }
  async function loadRequests() {
    const tableEl = $("requestsTable");
    if (!tableEl) return;
    tableEl.setAttribute("aria-busy", "true");
    tableEl.innerHTML = '<p class="muted small">\u6B63\u5728\u52A0\u8F7D\u8C03\u7528\u8BB0\u5F55\u2026</p>';
    loadRequestStats();
    const q = new URLSearchParams();
    q.set("limit", "100");
    if ($("rqModel").value) q.set("model", $("rqModel").value);
    if ($("rqStatus").value) q.set("status", $("rqStatus").value);
    if ($("rqKey").value) q.set("key", $("rqKey").value);
    if ($("rqProvider").value.trim()) q.set("provider", $("rqProvider").value.trim());
    try {
      const j = await api("/api/requests?" + q);
      const rows = j.data || [];
      tableEl.innerHTML = !rows.length ? emptyState("\u6682\u65E0\u8BB0\u5F55") : wrapTable(
        "<tr><th>\u65F6\u95F4</th><th>\u5BA2\u6237\u7AEF / \u5BC6\u94A5</th><th>\u8BF7\u6C42 URL</th><th>Provider / \u6A21\u578B</th><th>\u72B6\u6001 / \u6D41\u5F0F</th><th>HTTP</th><th>\u8017\u65F6 / TTFT</th><th>Token / \u4F53\u79EF</th><th>\u64CD\u4F5C</th></tr>" + rows.map((x) => {
          const rid = String(x.request_id || "");
          const ridShort = rid.length > 10 ? rid.slice(0, 8) + "\u2026" : rid;
          const target = displayRequestTarget(x);
          const model = displayUpstreamModel(x);
          const sz = (Number(x.request_bytes || 0) / 1024).toFixed(1) + " / " + (Number(x.response_bytes || 0) / 1024).toFixed(1) + " KB";
          const stopBtn = x.status === "running" ? `<button type="button" class="btn sm danger" onclick="cancelRequest('${encodeURIComponent(x.request_id).replace(/'/g, "%27")}')">\u7ED3\u675F</button>` : "";
          return `<tr><td class="small req-time">${fmtDate(x.started_at)}</td><td><div class="req-cell-main">${esc(x.client_name || "\u2014")}</div><div class="req-cell-sub">\u5BC6\u94A5\uFF1A${esc(x.api_key_name || "\u2014")}</div><div class="req-cell-sub" title="${esc(rid)}">ID\uFF1A<code>${esc(ridShort)}</code></div></td><td class="small req-url" title="${esc(target)}">${esc(target)}</td><td><div class="req-cell-main"><code>${esc(x.provider_id || "\u2014")}</code></div><div class="req-cell-sub" title="${esc(model)}">\u6A21\u578B\uFF1A${esc(model)}</div></td><td><div>${statusPill(x.status)}</div><div class="req-cell-sub">${x.stream ? "\u6D41\u5F0F" : x.ttft_ms != null && x.ttft_ms > 0 ? "\u63A8\u65AD\u6D41\u5F0F" : "\u975E\u6D41\u5F0F"}</div></td><td>${x.http_status}${x.upstream_http_status != null && x.upstream_http_status !== x.http_status ? " / " + x.upstream_http_status : ""}</td><td><div class="req-cell-main">${x.latency_ms != null ? x.latency_ms + " ms" : "\u2014"}</div><div class="req-cell-sub">TTFT\uFF1A${x.ttft_ms != null ? x.ttft_ms + " ms" : "\u2014"}</div></td><td><div class="req-cell-main">${x.total_tokens ?? "\u2014"}</div><div class="req-cell-sub">\u4F53\u79EF\uFF1A${sz}</div></td><td><div class="td-actions"><button type="button" class="btn sm" onclick="showDetail('${encodeURIComponent(x.request_id).replace(/'/g, "%27")}')">\u8BE6\u60C5</button>${stopBtn}</div></td></tr>`;
        }).join("")
      );
    } catch (e) {
      tableEl.innerHTML = '<div class="msg err" role="alert"><span>\u52A0\u8F7D\u8C03\u7528\u8BB0\u5F55\u5931\u8D25\uFF1A' + esc(e.message || "\u672A\u77E5\u9519\u8BEF") + '</span> <button type="button" class="btn sm ghost" onclick="loadRequests()">\u91CD\u8BD5</button></div>';
    } finally {
      tableEl.setAttribute("aria-busy", "false");
    }
  }
  async function clearRequests() {
    if (!confirm("\u786E\u5B9A\u6E05\u7A7A\u5168\u90E8\u8C03\u7528\u8BB0\u5F55\uFF1F\uFF08\u542B payload \u8868\uFF09")) return;
    try {
      const j = await api("/api/requests", { method: "DELETE" });
      setGlobalMsg('<span class="ok">\u5DF2\u5220\u9664 ' + esc(j.deleted) + " \u6761\u8BB0\u5F55</span>");
      loadRequests();
    } catch (e) {
      setGlobalMsg('<span class="err">' + esc(e.message) + "</span>");
    }
  }
  function reqKv(rows) {
    return '<dl class="req-kv">' + rows.filter((x) => x[1] != null && x[1] !== "" && x[1] !== "\u2014").map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join("") + "</dl>";
  }
  function prettyBodyText(s) {
    if (s == null || s === "") return "\u2014";
    const t = String(s).trim();
    if (t.startsWith("data:") || /\ndata:\s/.test(t)) return String(s);
    try {
      return JSON.stringify(JSON.parse(s), null, 2);
    } catch {
      return String(s);
    }
  }
  function reqPanelFold(title, innerHtml) {
    return `<section class="req-section req-panel-fold"><details class="req-fold req-body-fold"><summary>${esc(title)}</summary><div class="req-fold-inner">${innerHtml}</div></details></section>`;
  }
  function reqCodeBlock(text, large) {
    const cls = "req-pre" + (large ? " req-pre-lg" : "");
    return `<div class="req-code-wrap"><div class="req-code-bar"><button type="button" class="btn sm ghost req-copy-btn" data-req-copy>\u590D\u5236</button></div><pre class="${cls}">${esc(text)}</pre></div>`;
  }
  async function flashCopyBtn(btn, ok) {
    const prev = btn.textContent;
    btn.textContent = ok ? "\u5DF2\u590D\u5236" : "\u5931\u8D25";
    btn.disabled = !ok;
    setTimeout(() => {
      btn.textContent = prev;
      btn.disabled = false;
    }, 1400);
  }
  async function copyReqCode(btn) {
    const pre = btn.closest(".req-code-wrap")?.querySelector("pre");
    if (!pre) return;
    await flashCopyBtn(btn, await writeClipboard(pre.textContent || ""));
  }
  async function copyReqStreamAll(btn) {
    const pre = btn.closest(".req-fold-inner")?.querySelector(".req-sse-all-source pre");
    if (!pre) return;
    await flashCopyBtn(btn, await writeClipboard(pre.textContent || ""));
  }
  async function writeClipboard(text) {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
        return true;
      }
    } catch {
    }
    try {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.cssText = "position:fixed;left:-9999px;top:0;opacity:0";
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand("copy");
      ta.remove();
      return ok;
    } catch {
      return false;
    }
  }
  function formatStreamEventBody(e) {
    const hasRaw = e.raw_line != null && String(e.raw_line).trim() !== "";
    if (hasRaw) return String(e.raw_line);
    const fallback = String(e.preview || "").trim();
    if (!fallback) return "\uFF08\u672C\u6761\u65E0 raw_line\uFF1B\u591A\u4E3A\u65E7\u8BB0\u5F55\uFF0C\u8BF7\u5BF9\u65B0\u8BF7\u6C42\u67E5\u770B\u5B8C\u6574 JSON\uFF09";
    return fallback + "\n\uFF08\u65E7\u6570\u636E\uFF1A\u4EC5\u4E3A\u89E3\u6790\u6458\u8981\uFF0C\u975E\u539F\u59CB JSON\uFF09";
  }
  function formatStreamEventsDump(evs) {
    return evs.map((e) => {
      const body = formatStreamEventBody(e);
      const meta = `#${e.seq} \xB7 ${e.at_ms} ms \xB7 ${e.kind || "?"}${e.field ? " \xB7 " + e.field : ""}${e.delta_len ? " \xB7 +" + e.delta_len + " \u5B57\u7B26" : ""}`;
      return meta + "\n" + body;
    }).join("\n\n");
  }
  function renderStreamEventsSection(j) {
    const evs = j.stream_events || [];
    if (!evs.length) return "";
    const n = j.stream_event_count ?? evs.length;
    const dump = formatStreamEventsDump(evs);
    let inner = "";
    if (j.stream_events_note) inner += `<p class="req-note">${esc(j.stream_events_note)}</p>`;
    inner += `<div class="req-stream-toolbar"><button type="button" class="btn sm ghost req-copy-btn" data-req-copy-sse-all>\u590D\u5236\u5168\u90E8</button></div>`;
    inner += `<div class="req-sse-all-source" aria-hidden="true"><pre>${esc(dump)}</pre></div>`;
    inner += '<div class="req-stream-list">';
    for (const e of evs) {
      const body = formatStreamEventBody(e);
      const label = `<code>#${e.seq}</code> \xB7 ${e.at_ms} ms \xB7 ${esc(e.kind || "?")}${e.field ? " \xB7 " + esc(e.field) : ""}${e.delta_len ? " \xB7 +" + e.delta_len + " \u5B57\u7B26" : ""}`;
      inner += `<details class="req-fold req-nested-fold"><summary>${label}</summary><div class="req-fold-inner">${reqCodeBlock(body, false)}</div></details>`;
    }
    inner += "</div>";
    return reqPanelFold(`\u6D41\u5F0F SSE \u9010\u6761 (${n})`, inner);
  }
  function headerRows(obj) {
    if (!obj || typeof obj !== "object") return [];
    return Object.keys(obj).sort((a, b) => a.localeCompare(b)).map((k) => [k, obj[k]]);
  }
  function serviceParamRows(obj) {
    if (!obj || typeof obj !== "object") return [];
    return Object.keys(obj).map((k) => [k, typeof obj[k] === "object" ? JSON.stringify(obj[k]) : String(obj[k])]);
  }
  async function cancelRequest(id) {
    try {
      id = decodeURIComponent(id);
    } catch {
    }
    if (!confirm("\u786E\u5B9A\u7ED3\u675F\u8FD9\u6761\u6B63\u5728\u8FDB\u884C\u7684\u8C03\u7528\uFF1F\u8C03\u7528\u65B9\u4F1A\u770B\u5230\u8FDE\u63A5\u4E2D\u65AD\u3002")) return;
    try {
      await api("/api/requests/" + encodeURIComponent(id) + "/cancel", { method: "POST" });
      showToast("\u5DF2\u7ED3\u675F\u8FD9\u6761\u8C03\u7528", "ok");
      if ($("requests")?.classList.contains("active")) loadRequests();
      if (detailPollId === id) refreshDetailBody(id).catch(() => {
      });
    } catch (e) {
      showToast(e.message || "\u7ED3\u675F\u5931\u8D25", "err");
      if ($("requests")?.classList.contains("active")) loadRequests();
    }
  }
  function detailRequestId(j) {
    const s = j.summary || {};
    const log = j.log || {};
    return String(s.request_id || log.request_id || "");
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
    const running = j.live || s.status === "running" || log.status === "running";
    if (running && rid) {
      add(
        "running",
        "swap",
        `<p style="margin:0 0 10px"><button type="button" class="btn sm danger" onclick="cancelRequest('${encodeURIComponent(rid).replace(/'/g, "%27")}')">\u7ED3\u675F\u8FD9\u6761\u8C03\u7528</button></p>`
      );
    }
    if (j.payload_note) add("note", "swap", `<p class="req-note warn" style="margin:0 0 10px">${esc(j.payload_note)}</p>`);
    add(
      "overview",
      "swap",
      `<section class="req-section"><h3>\u6982\u89C8</h3>${reqKv([
        ["Request ID", s.request_id || log.request_id],
        ["\u72B6\u6001", (s.status || log.status) + " \xB7 Gateway HTTP " + (s.http_status ?? log.http_status ?? "\u2014") + ((s.upstream_http_status ?? log.upstream_http_status) != null ? " \xB7 \u4E0A\u6E38 HTTP " + (s.upstream_http_status ?? log.upstream_http_status) : "")],
        ["\u8BF7\u6C42 URL", displayRequestTarget({ method: s.method || log.method, request_url: s.request_url || log.request_url, endpoint: s.endpoint || log.endpoint })],
        ["API Key", (s.api_key || log.api_key_name || "\u2014") + (s.api_key_id ? " (#" + s.api_key_id + ")" : "")],
        ["\u5BA2\u6237\u7AEF", s.client_name || log.client_name || "\u2014"],
        ["IP", s.client_ip || log.client_ip],
        ["\u9519\u8BEF\u7801", s.error_code || log.error_code],
        ["\u9519\u8BEF\u4FE1\u606F", s.error_message || log.error_message]
      ])}</section>`
    );
    const clientHeaders = headerRows(j.request_headers);
    const upstreamHeaders = headerRows(j.upstream_headers);
    let headerInner = "";
    if (j.headers_note) headerInner += `<p class="req-note">${esc(j.headers_note)}</p>`;
    if (clientHeaders.length) {
      headerInner += `<h4>\u5BA2\u6237\u7AEF \u2192 \u7F51\u5173</h4>${reqKv(clientHeaders)}`;
    }
    if (upstreamHeaders.length) {
      headerInner += `<h4>\u7F51\u5173 \u2192 \u4E0A\u6E38</h4>${reqKv(upstreamHeaders)}`;
    }
    if (headerInner) add("headers", "swap", `<section class="req-section"><h3>\u8BF7\u6C42\u5934</h3>${headerInner}</section>`);
    const p = j.request_params || {};
    const paramRows = [
      ["Provider ID", log.provider_id || s.provider_id || "\u2014"],
      ["\u5BA2\u6237\u7AEF model", p.model_client || s.model_client || log.model_requested || "\u2014"],
      ["\u4E0A\u6E38 model", p.model_upstream || s.model_upstream || log.model_actual || "\u2014"],
      ["\u6A21\u578B\u8BF4\u660E", p.model_note || ""],
      ["\u6D41\u5F0F", p.stream_label || ""],
      ["\u6D41\u5F0F\u8BF4\u660E", p.stream_note || ""],
      ["\u601D\u8003", p.thinking || ""],
      ["\u8981\u6C42\u8FD4\u56DE token", p.include_usage_label || ""],
      ...serviceParamRows(p.service_params)
    ];
    add("params", "swap", `<section class="req-section"><h3>\u8BF7\u6C42\u53C2\u6570</h3>${reqKv(paramRows)}</section>`);
    add(
      "timing",
      "swap",
      `<section class="req-section"><h3>\u8017\u65F6\u4E0E Token</h3>${reqKv([
        ["\u5F00\u59CB", fmtDate(t.started_at || log.started_at)],
        ["\u7ED3\u675F", fmtDate(t.finished_at || log.finished_at)],
        ["\u603B\u8017\u65F6", t.latency_human || (t.latency_ms ?? log.latency_ms) + " ms"],
        ["\u9996 token (TTFT)", t.ttft_human || (t.ttft_ms != null ? t.ttft_ms + " ms" : "\u2014")],
        ["\u751F\u6210\u9636\u6BB5", t.generation_human || (t.generation_ms != null ? t.generation_ms + " ms" : "\u2014")],
        [
          "Token in/out/total",
          [t.input_tokens ?? log.input_tokens, t.output_tokens ?? log.output_tokens, t.total_tokens ?? log.total_tokens].map((x) => x ?? "\u2014").join(" / ")
        ],
        ["Token \u6765\u6E90", t.token_source_label || ""],
        ["\u8BF7\u6C42/\u54CD\u5E94\u5927\u5C0F", Number(s.request_kb || 0).toFixed(1) + " KB / " + Number(s.response_kb || 0).toFixed(1) + " KB"]
      ])}${j.token_note ? `<p class="req-note">${esc(j.token_note)}</p>` : ""}</section>`
    );
    if (j.analysis) {
      const ph = j.analysis.phases_ms || {};
      const tp = j.analysis.throughput || {};
      let block = `<section class="req-section"><h3>\u8BF7\u6C42\u5206\u6790</h3>${reqKv([
        ["Gateway \u51C6\u5907", ph.prep_ms != null ? ph.prep_ms + " ms" : null],
        ["\u6A21\u578B\u52A0\u8F7D/\u7B49\u5F85", ph.model_load_ms != null ? ph.model_load_ms + " ms" : null],
        ["\u4E0A\u6E38\u54CD\u5E94\u5934", ph.time_to_upstream_headers_ms != null ? ph.time_to_upstream_headers_ms + " ms" : null],
        ["\u9996\u5B57\u8282\u5230\u8FBE", ph.time_to_first_byte_ms != null ? ph.time_to_first_byte_ms + " ms" : null],
        ["\u9996\u4E2A\u5B9E\u9645\u8F93\u51FA (TTFT)", ph.ttft_ms != null ? ph.ttft_ms + " ms" : null],
        ["\u4E0A\u6E38\u63A8\u7406", ph.upstream_ms != null ? ph.upstream_ms + " ms" : null],
        ["\u9996\u6761\u6B63\u6587 content", ph.ttft_content_ms != null ? ph.ttft_content_ms + " ms" : null],
        ["Prefill\u2192\u9996 token", ph.prefill_until_first_ms != null ? ph.prefill_until_first_ms + " ms" : null],
        ["\u601D\u8003\u9636\u6BB5", ph.thinking_ms != null ? ph.thinking_ms + " ms" : null],
        ["\u6B63\u6587\u751F\u6210\u9636\u6BB5", ph.content_generation_ms != null ? ph.content_generation_ms + " ms" : null],
        [
          "\u8F93\u51FA\u901F\u5EA6",
          tp.output_tokens_per_sec != null ? tp.output_tokens_per_sec.toFixed(2) + " tok/s (" + tp.generation_window_ms + " ms \u7A97\u53E3)" : null
        ]
      ])}`;
      if (j.analysis_note) block += `<p class="req-note">${esc(j.analysis_note)}</p>`;
      if (tp.note) block += `<p class="req-note">${esc(tp.note)}</p>`;
      if ((j.analysis.summary || []).length) {
        block += `<ul class="req-hints">${j.analysis.summary.map((line) => `<li>${esc(line)}</li>`).join("")}</ul>`;
      }
      block += `</section>`;
      add("analysis", "swap", block);
    }
    const hints = j.diagnosis || [];
    if (hints.length) {
      add(
        "hints",
        "swap",
        `<section class="req-section"><h3>\u8BCA\u65AD\u63D0\u793A</h3><ul class="req-hints">${hints.map((h) => `<li>${esc(h)}</li>`).join("")}</ul></section>`
      );
    }
    if (j.content_text) {
      add("content", "text", `<section class="req-section"><h3>\u805A\u5408\u6B63\u6587 content</h3><div class="req-text-block">${esc(j.content_text)}</div></section>`);
    }
    if (j.reasoning_text) {
      add(
        "reasoning",
        "text",
        `<section class="req-section"><h3>\u805A\u5408\u601D\u8003 reasoning</h3><div class="req-text-block reasoning">${esc(j.reasoning_text)}</div></section>`
      );
    }
    const stream = renderStreamEventsSection(j);
    if (stream) add("stream", "stream", stream);
    const reqBody = j.request_body || j.request_preview || "";
    if (reqBody) {
      let inner = "";
      if (j.request_body_note) inner += `<p class="req-note">${esc(j.request_body_note)}</p>`;
      else inner += `<p class="req-note">Gateway \u8BB0\u5F55\u5E76\u8F6C\u53D1\u7ED9\u4E0A\u6E38\u7684 JSON body</p>`;
      if (j.request_body_bytes) inner += `<p class="req-note">${j.request_body_bytes} bytes</p>`;
      inner += reqCodeBlock(prettyBodyText(reqBody), true);
      add("request", "fold", reqPanelFold("\u539F\u59CB\u8BF7\u6C42 body", inner));
    }
    const resBody = j.response_body || j.response_preview || "";
    if (resBody) {
      let inner = `<p class="req-note">${j.response_body_bytes ? j.response_body_bytes + " bytes \xB7 " : ""}\u6D41\u5F0F\u4E3A\u5B8C\u6574 SSE \u6587\u672C\uFF1B\u975E\u6D41\u5F0F\u4E3A\u4E0A\u6E38 JSON</p>`;
      inner += reqCodeBlock(prettyBodyText(resBody), true);
      add("response", "fold", reqPanelFold("\u539F\u59CB\u54CD\u5E94 body", inner));
    }
    add("raw", "fold", reqPanelFold("API \u539F\u59CB JSON\uFF08\u8C03\u8BD5\uFF09", reqCodeBlock(JSON.stringify(j, null, 2), true)));
    return sections;
  }
  function htmlToSection(id, html) {
    const tpl = document.createElement("template");
    tpl.innerHTML = html.trim();
    const el = tpl.content.firstElementChild;
    el.dataset.detailSection = id;
    return el;
  }
  function patchTextSection(el, html) {
    const neu = htmlToSection("tmp", html);
    const a = el.querySelector(".req-text-block");
    const b = neu.querySelector(".req-text-block");
    if (a && b) {
      if (a.textContent !== b.textContent) a.textContent = b.textContent;
      return el;
    }
    neu.dataset.detailSection = el.dataset.detailSection;
    el.replaceWith(neu);
    return neu;
  }
  function patchFoldSection(el, html) {
    const neu = htmlToSection("tmp", html);
    const sum = el.querySelector("details > summary");
    const nsum = neu.querySelector("details > summary");
    if (sum && nsum && sum.textContent !== nsum.textContent) sum.textContent = nsum.textContent;
    const pres = [...el.querySelectorAll("pre")];
    const npres = [...neu.querySelectorAll("pre")];
    const notes = [...el.querySelectorAll(".req-note")];
    const nnotes = [...neu.querySelectorAll(".req-note")];
    if (pres.length && pres.length === npres.length && notes.length === nnotes.length) {
      pres.forEach((p, i) => {
        if (p.textContent !== npres[i].textContent) p.textContent = npres[i].textContent;
      });
      notes.forEach((n, i) => {
        if (n.textContent !== nnotes[i].textContent) n.textContent = nnotes[i].textContent;
      });
      return;
    }
    const inner = el.querySelector(".req-fold-inner");
    const ninner = neu.querySelector(".req-fold-inner");
    if (inner && ninner) inner.replaceWith(ninner);
  }
  function streamRowLabel(node) {
    return node.querySelector("summary")?.textContent || "";
  }
  function patchStreamSection(el, html) {
    const neu = htmlToSection("tmp", html);
    const sum = el.querySelector("details > summary");
    const nsum = neu.querySelector("details > summary");
    if (sum && nsum && sum.textContent !== nsum.textContent) sum.textContent = nsum.textContent;
    const list = el.querySelector(".req-stream-list");
    const nlist = neu.querySelector(".req-stream-list");
    if (list && nlist) {
      const haveNodes = [...list.querySelectorAll(":scope > details")];
      const items = [...nlist.querySelectorAll(":scope > details")];
      const prefixOk = haveNodes.length <= items.length && haveNodes.every((node, i) => streamRowLabel(node) === streamRowLabel(items[i]));
      if (!prefixOk) list.replaceChildren(...items);
      else {
        for (let i = haveNodes.length; i < items.length; i++) list.appendChild(items[i]);
      }
    }
    const dump = el.querySelector(".req-sse-all-source pre");
    const ndump = neu.querySelector(".req-sse-all-source pre");
    if (dump && ndump && dump.textContent !== ndump.textContent) dump.textContent = ndump.textContent;
    const note = el.querySelector(".req-note");
    const nnote = neu.querySelector(".req-note");
    if (note && nnote && note.textContent !== nnote.textContent) note.textContent = nnote.textContent;
  }
  function applyRequestDetail(root, j) {
    const rid = detailRequestId(j);
    if (root.dataset.detailRequest !== rid) {
      root.replaceChildren();
      root.dataset.detailRequest = rid;
      const card = root.closest(".card");
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
      if (sec.kind === "text") live = patchTextSection(el, sec.html);
      else if (sec.kind === "fold") patchFoldSection(el, sec.html);
      else if (sec.kind === "stream") patchStreamSection(el, sec.html);
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
    const j = await api("/api/requests/" + encodeURIComponent(id));
    if (epoch !== detailEpoch || detailPollId !== id) return j;
    const b = $("detailBody");
    if (b) applyRequestDetail(b, j);
    return j;
  }
  function stopDetailPoll() {
    if (detailPollTimer) {
      clearInterval(detailPollTimer);
      detailPollTimer = null;
    }
    detailPollId = "";
  }
  async function showDetail(id) {
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
      const modal = $("detailModal");
      modal?.classList.add("show");
      modal?.setAttribute("aria-hidden", "false");
      modal?.querySelector(".card-head button")?.focus();
      if (j.live) {
        detailPollTimer = setInterval(async () => {
          if (epoch !== detailEpoch || detailPollId !== id) return;
          if (!$("detailModal")?.classList.contains("show")) {
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
        }, 2e3);
      }
    } catch (e) {
      if (epoch && epoch !== detailEpoch) return;
      detailTrigger = null;
      alert(e.message);
    }
  }
  function resetDetailView() {
    const body = $("detailBody");
    if (!body) return;
    body.replaceChildren();
    delete body.dataset.detailRequest;
    const card = body.closest(".card");
    if (card) card.scrollTop = 0;
  }
  function closeModal() {
    stopDetailPoll();
    resetDetailView();
    const modal = $("detailModal");
    modal?.classList.remove("show");
    modal?.setAttribute("aria-hidden", "true");
    const trigger = detailTrigger;
    detailTrigger = null;
    if (trigger?.isConnected) trigger.focus();
  }
  var detailPollTimer, detailPollId, detailEpoch, detailTrigger;
  var init_requests = __esm({
    "js/features/requests.js"() {
      init_dom();
      init_api();
      init_format();
      init_notify();
      detailPollTimer = null;
      detailPollId = "";
      detailEpoch = 0;
      detailTrigger = null;
      $("detailModal")?.addEventListener("click", (e) => {
        if (e.target.id === "detailModal") closeModal();
        const copyBtn = e.target.closest?.("[data-req-copy]");
        if (copyBtn) {
          e.preventDefault();
          copyReqCode(copyBtn);
          return;
        }
        const copyAllSse = e.target.closest?.("[data-req-copy-sse-all]");
        if (copyAllSse) {
          e.preventDefault();
          copyReqStreamAll(copyAllSse);
        }
      });
      document.addEventListener("keydown", (event) => {
        const modal = $("detailModal");
        if (!modal?.classList.contains("show")) return;
        if (event.key === "Escape") {
          event.preventDefault();
          closeModal();
          return;
        }
        if (event.key !== "Tab") return;
        const focusable = [...modal.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])')].filter((el) => {
          const closedDetails = el.closest("details:not([open])");
          return !el.hidden && el.getClientRects().length > 0 && getComputedStyle(el).visibility !== "hidden" && (!closedDetails || el.matches("details:not([open]) > summary"));
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
    }
  });

  // js/app/main.js
  var main_exports = {};
  function syncTopbarRefresh() {
    const btn = $("btnRefreshDashboard");
    if (!btn) return;
    btn.hidden = !$("dashboard")?.classList.contains("active");
  }
  function inferButtonHtml(backend, action, { modelId = "", modelKey = "", title = "", confirmMsg = "", label = "", extraClass = "" }) {
    const cls = ["btn", "sm", extraClass];
    if (action === "load") cls.push("primary");
    if (action === "unload") cls.push("danger");
    const parts = [
      'type="button"',
      'class="' + cls.filter(Boolean).join(" ") + '"',
      'data-infer=""',
      'data-infer-backend="' + esc(backend) + '"',
      'data-infer-action="' + esc(action) + '"'
    ];
    if (modelId) parts.push('data-model-id="' + esc(modelId) + '"');
    if (modelKey) parts.push('data-model-key="' + esc(modelKey) + '"');
    if (title) parts.push('data-infer-title="' + esc(title) + '"');
    if (confirmMsg) parts.push('data-confirm="' + esc(confirmMsg) + '"');
    const text = label || (action === "load" ? "\u52A0\u8F7D" : action === "unload" ? "\u5378\u8F7D" : action);
    return "<button " + parts.join(" ") + ">" + esc(text) + "</button>";
  }
  function scrollMmActionLog() {
    const card = $("mmActionLogCard");
    if (card) card.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }
  function markActionLogActive() {
    $("mmActionLogCard")?.classList.add("has-activity");
    scrollMmActionLog();
  }
  function handleInferClick(ev) {
    const delBtn = ev.target.closest?.("button[data-model-delete]");
    if (delBtn && !delBtn.disabled) {
      const kind = delBtn.dataset.modelDelete || "";
      const modelKey2 = (delBtn.dataset.modelKey || "").trim();
      const modelId2 = (delBtn.dataset.modelId || "").trim();
      const modelPath = (delBtn.dataset.modelPath || "").trim();
      if (kind === "gguf" && modelKey2) {
        void deleteModel(modelKey2);
        return;
      }
      if (kind === "gguf-disk" && modelPath) {
        void deleteDiskGGUFModel(modelPath);
        return;
      }
      if (kind === "ollama" && (modelId2 || modelKey2)) {
        void ollamaDelete(modelId2 || modelKey2);
        return;
      }
    }
    const btn = ev.target.closest?.("button[data-infer]");
    if (!btn || btn.disabled) return;
    const backend = btn.dataset.inferBackend || "";
    const action = btn.dataset.inferAction || "";
    const modelId = btn.dataset.modelId || "";
    const modelKey = btn.dataset.modelKey || "";
    const title = btn.dataset.inferTitle || `${backend} \xB7 ${action}` + (modelId ? ` \xB7 ${modelId}` : "") + (modelKey ? ` \xB7 ${modelKey}` : "");
    markActionLogActive();
    appendOpsLog([{ level: "info", message: "\u6536\u5230\u70B9\u51FB \u2192 " + action + " backend=" + backend + (modelId ? " id=" + modelId : "") + (modelKey ? " key=" + modelKey : "") }], title);
    const cmsg = btn.dataset.confirm;
    if (cmsg && !confirm(cmsg)) {
      appendOpsLog([{ level: "warn", message: "\u5DF2\u5728\u786E\u8BA4\u6846\u70B9\u53D6\u6D88\uFF0C\u672A\u53D1\u9001\u8BF7\u6C42" }]);
      return;
    }
    if (backend === "llamacpp" && (action === "load" || action === "unload")) {
      if (action === "load") setGgufMsg("", "");
      inferenceOp(backend, action, { model_id: modelId, model_key: modelKey }, title).then((j) => {
        if (action === "load" && j?.success) setGgufMsg("ok", "\u5DF2\u52A0\u8F7D\u5230 llama.cpp");
        else if (action === "load" && (!j || !j.success)) setGgufMsg("err", j && j.error || "\u52A0\u8F7D\u5931\u8D25");
        else if (action === "unload" && j?.success) setGgufMsg("ok", "\u5DF2\u4ECE llama.cpp \u5378\u8F7D");
        else if (action === "unload" && (!j || !j.success)) setGgufMsg("err", j && j.error || "\u5378\u8F7D\u5931\u8D25");
      });
      return;
    }
    if (backend === "ollama") {
      inferenceOp(backend, action, { model_id: modelId, model_key: modelKey }, title).then((j) => {
        if (j?.success && action === "load") $("ollamaMsg").innerHTML = '<span class="ok">\u5DF2\u52A0\u8F7D ' + esc(modelId) + "</span>";
        else if ((!j || !j.success) && action === "load") $("ollamaMsg").innerHTML = '<span class="err">\u52A0\u8F7D\u5931\u8D25\uFF0C\u89C1\u64CD\u4F5C\u65E5\u5FD7</span>';
        else if (j?.success && action === "unload") $("ollamaMsg").innerHTML = '<span class="ok">\u5DF2\u5378\u8F7D ' + esc(modelId) + "</span>";
        else if ((!j || !j.success) && action === "unload") $("ollamaMsg").innerHTML = '<span class="err">\u5378\u8F7D\u5931\u8D25\uFF0C\u89C1\u64CD\u4F5C\u65E5\u5FD7</span>';
      });
      return;
    }
    inferenceOp(backend, action, { model_id: modelId, model_key: modelKey }, title);
  }
  function appendActionLogTo(el, lines, prependTitle) {
    if (!el) return;
    if (el.classList.contains("muted")) {
      el.classList.remove("muted");
      el.innerHTML = "";
    }
    if (prependTitle) {
      const h = document.createElement("p");
      h.className = "ops-log-line info";
      h.innerHTML = `<span class="t">${(/* @__PURE__ */ new Date()).toLocaleTimeString()}</span><span class="m">\u2014\u2014 ${esc(prependTitle)} \u2014\u2014</span>`;
      el.appendChild(h);
    }
    (lines || []).forEach((x) => {
      const p = document.createElement("p");
      const lv = (x.level || "info").toLowerCase();
      p.className = "ops-log-line " + lv;
      const ts = x.at ? new Date(x.at).toLocaleTimeString() : "";
      p.innerHTML = `<span class="t">${esc(ts)}</span><span class="m">${esc(x.message || "")}</span>`;
      el.appendChild(p);
    });
    el.scrollTop = el.scrollHeight;
  }
  function appendOpsLog(lines, prependTitle) {
    appendActionLogTo($("mmActionLog"), lines, prependTitle);
    if ($("mmActionLog") && lines?.length) markActionLogActive();
  }
  function clearMmActionLog() {
    const el = $("mmActionLog");
    if (!el) return;
    el.innerHTML = "\u6682\u65E0\u64CD\u4F5C\uFF1B\u5728 Llama \u6A21\u578B\u6216 Ollama \u5E93\u4E2D\u70B9\u51FB\u52A0\u8F7D/\u5378\u8F7D\u540E\u663E\u793A\u2026";
    el.classList.add("muted");
  }
  async function refreshAfterInference(backend) {
    const jobs = [loadMmBackendPills().catch(() => {
    })];
    if ($("models-local")?.classList.contains("active")) {
      jobs.push((async () => {
        await modelHub.refresh().catch(() => {
        });
        renderRuntimeCatalog();
        renderGatewayModelTable();
        if (backend === "ollama") await loadOllamaLibrary().catch(() => {
        });
      })());
    }
    if ($("dashboard")?.classList.contains("active")) jobs.push(loadSystem().catch(() => {
    }));
    await Promise.all(jobs);
  }
  async function inferenceOp(backend, action, { model_id = "", model_key = "" } = {}, title) {
    markActionLogActive();
    if (!isAuthenticated()) {
      appendOpsLog([{ level: "err", message: "\u8BF7\u5148\u767B\u5F55\u63A7\u5236\u53F0" }], title || `${backend} \xB7 ${action}`);
      return null;
    }
    model_id = (model_id || "").trim();
    model_key = (model_key || "").trim();
    const head = title || `${backend} \xB7 ${action}` + (model_id ? ` \xB7 ${model_id}` : "") + (model_key ? ` \xB7 key=${model_key}` : "");
    appendOpsLog([{ level: "info", message: "POST /api/admin/model-ops" }], head);
    try {
      const j = await api("/api/admin/model-ops", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ backend, action, model_id, model_key })
      });
      appendOpsLog(j.logs || []);
      if (!j.success) {
        const errMsg = j.error || "\u64CD\u4F5C\u5931\u8D25";
        appendOpsLog([{ level: "err", message: errMsg }]);
        if (backend === "llamacpp") setGgufMsg("err", errMsg);
        else if (backend === "ollama" && $("ollamaMsg")) $("ollamaMsg").innerHTML = '<span class="err">' + esc(errMsg) + "</span>";
        return j;
      }
      appendOpsLog([{ level: "ok", message: "\u5B8C\u6210 success=true" }]);
      await refreshAfterInference(backend);
      return j;
    } catch (e) {
      appendOpsLog([{ level: "err", message: e.message }]);
      if (backend === "llamacpp") setGgufMsg("err", e.message);
      else if (backend === "ollama" && $("ollamaMsg")) $("ollamaMsg").innerHTML = '<span class="err">' + esc(e.message) + "</span>";
      return null;
    }
  }
  function goTab(name) {
    if (name === "account" && globalThis.openAccountSettings) {
      globalThis.openAccountSettings();
      return;
    }
    const btn = document.querySelector('.nav button[data-tab="' + name + '"]');
    if (btn) btn.click();
  }
  async function loadMmBackendPills() {
    const el = $("mmBackendPills");
    if (!el) return;
    try {
      let a = adminStore.upstream;
      if (!a?.backend) {
        const j = await api("/api/admin/backend");
        a = j.active || {};
      }
      const be = a.backend === "ollama" ? "Ollama" : a.backend === "llamacpp" ? "llama.cpp" : a.backend || "\u2014";
      const mn = (a.model_name || "").trim();
      el.innerHTML = `<span class="pill pill-running">${esc(be)}</span>` + (mn ? `<span class="pill pill-loaded">\u5F53\u524D ${esc(mn)}</span>` : `<span class="pill">\u672A\u52A0\u8F7D\u6A21\u578B</span>`);
    } catch {
      el.innerHTML = '<span class="pill pill-warn">\u4E0A\u6E38\u4FE1\u606F\u6682\u4E0D\u53EF\u7528\uFF0C\u8BF7\u68C0\u67E5\u670D\u52A1\u72B6\u6001\u540E\u5237\u65B0</span>';
    }
  }
  function modelsNavActive() {
    return $("models-local")?.classList.contains("active") || $("models-download")?.classList.contains("active") || $("models-inference")?.classList.contains("active");
  }
  async function loadLocalModelsPanel() {
    await Promise.all([loadMmBackendPills(), modelHub.refresh()]);
    renderRuntimeCatalog();
    renderGatewayModelTable();
    await loadOllamaLibrary();
  }
  async function loadDownloadPanel(opts = {}) {
    await loadDownloads();
    if (globalThis.ModelDownload?.refreshLocalInventory) await globalThis.ModelDownload.refreshLocalInventory().catch(() => {
    });
    if (opts.refreshCatalog && globalThis.ModelDownload) await globalThis.ModelDownload.search().catch(() => {
    });
  }
  async function loadModelsPanel(opts = {}) {
    if ($("models-download")?.classList.contains("active")) return loadDownloadPanel(opts);
    return loadLocalModelsPanel();
  }
  function setOllamaPullFeedback(html) {
    const onDownload = $("models-download")?.classList.contains("active");
    const el = onDownload ? $("catalogMsg") : $("ollamaMsg");
    if (el) el.innerHTML = html;
  }
  async function ollamaPull(nameArg) {
    const name = (nameArg || $("ollamaPullName")?.value || "").trim();
    if (!name) {
      setOllamaPullFeedback('<span class="err">\u8BF7\u8F93\u5165\u6A21\u578B\u540D</span>');
      return;
    }
    if (typeof globalThis.ModelDownload !== "undefined" && globalThis.ModelDownload.startOllamaPull) {
      await globalThis.ModelDownload.startOllamaPull(name);
      setOllamaPullFeedback('<span class="ok">\u5DF2\u52A0\u5165\u4E0B\u8F7D\u4EFB\u52A1\uFF0C\u89C1\u300C\u6A21\u578B\u4E0B\u8F7D \u2192 \u4E0B\u8F7D\u4EFB\u52A1\u300D</span>');
      return;
    }
    try {
      await api("/api/admin/ollama/pull", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
      setOllamaPullFeedback('<span class="ok">\u5DF2\u52A0\u5165\u4E0B\u8F7D\u4EFB\u52A1</span>');
      await loadDownloads();
    } catch (e) {
      setOllamaPullFeedback('<span class="err">' + esc(e.message) + "</span>");
    }
  }
  function ollamaModelInMemory(x, loadedIds, active) {
    if (x.loaded) return true;
    if (loadedIds && loadedIds.has(x.id)) return true;
    if (loadedIds) {
      for (const id of loadedIds) {
        if (id === x.id || id.startsWith(x.id + ":") || x.id.startsWith(id + ":")) return true;
      }
    }
    if (active && (active === x.id || active.startsWith(x.id + ":") || x.id.startsWith(active + ":"))) return true;
    return false;
  }
  function ollamaCatalogSubtitle(x) {
    const q = String(x?.quantization || "").trim();
    const p = String(x?.parameter_size || "").trim();
    if (q && p) return p + " \xB7 " + q;
    return q || p || "";
  }
  async function loadOllamaLibrary() {
    const el = $("ollamaTable");
    if (!el) return;
    $("ollamaMsg").textContent = "";
    try {
      const j = await api("/api/admin/ollama");
      const loadedIds = new Set((j.loaded || []).map((x) => x.id));
      const active = j.active || "";
      const rows = j.catalog || [];
      if (!rows.length) {
        el.innerHTML = emptyState("\u6682\u65E0\u6A21\u578B\uFF0C\u8BF7\u5728\u300C\u6A21\u578B\u4E0B\u8F7D\u300D\u4E2D\u5B89\u88C5");
        return;
      }
      el.innerHTML = wrapTable("<tr><th>\u6A21\u578B</th><th>\u5927\u5C0F</th><th>\u72B6\u6001</th><th>\u64CD\u4F5C</th></tr>" + rows.map((x) => {
        const running = ollamaModelInMemory(x, loadedIds, active);
        const isActive = active && (x.id === active || active.startsWith(x.id + ":") || x.id.startsWith(active + ":"));
        const loadLabel = running ? "\u91CD\u65B0\u52A0\u8F7D" : "\u52A0\u8F7D";
        const tLoad = `Ollama \u5E93 \xB7 \u52A0\u8F7D \xB7 ${x.id}`;
        const tUnload = `Ollama \u5E93 \xB7 \u5378\u8F7D \xB7 ${x.id}`;
        const sub = ollamaCatalogSubtitle(x);
        return `<tr>
          <td><code>${esc(x.id)}</code>${sub ? '<div class="small muted">' + esc(sub) + "</div>" : ""}${isActive ? " " + statusPill("\u5F53\u524D", " pill-active") : ""}</td>
          <td>${fmtSize(x.disk_bytes)}</td>
          <td>${running ? statusPill("loaded") : statusPill("installed")}</td>
          <td><div class="td-actions">
            ${inferButtonHtml("ollama", "load", { modelId: x.id, title: tLoad, label: loadLabel })}
            ${inferButtonHtml("ollama", "unload", { modelId: x.id, title: tUnload, confirmMsg: "\u4ECE\u5185\u5B58\u5378\u8F7D " + x.id + "\uFF1F", label: "\u5378\u8F7D", extraClass: "" })}
            <button type="button" class="btn sm danger" data-model-delete="ollama" data-model-id="${esc(x.id)}">\u5220\u9664</button>
          </div></td></tr>`;
      }).join(""));
    } catch (e) {
      el.innerHTML = emptyState("\u65E0\u6CD5\u8BFB\u53D6 Ollama");
      $("ollamaMsg").innerHTML = '<span class="err">' + esc(e.message) + "</span>";
    }
  }
  async function ollamaDelete(name) {
    if (!isAuthenticated()) {
      if ($("ollamaMsg")) $("ollamaMsg").innerHTML = '<span class="err">\u8BF7\u5148\u767B\u5F55\u63A7\u5236\u53F0</span>';
      return;
    }
    if (!confirm("\u4ECE Ollama \u78C1\u76D8\u5220\u9664 " + name + "\uFF1F\u4E0D\u53EF\u6062\u590D")) return;
    const head = `Ollama \u5E93 \xB7 \u5220\u9664 \xB7 ${name}`;
    markActionLogActive();
    appendOpsLog([{ level: "info", message: "POST /api/admin/ollama/delete name=" + name }], head);
    try {
      const j = await api("/api/admin/ollama/delete", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
      appendOpsLog(j.logs || []);
      if (!j.success) {
        appendOpsLog([{ level: "err", message: j.error || "\u5220\u9664\u5931\u8D25" }]);
        $("ollamaMsg").innerHTML = '<span class="err">' + esc(j.error || "\u5220\u9664\u5931\u8D25") + "</span>";
        return;
      }
      await loadOllamaLibrary();
      appendOpsLog([{ level: "ok", message: "\u5B8C\u6210 success=true" }]);
      $("ollamaMsg").innerHTML = '<span class="ok">\u5DF2\u5220\u9664 ' + esc(name) + "</span>";
    } catch (e) {
      appendOpsLog([{ level: "err", message: e.message }]);
      $("ollamaMsg").innerHTML = '<span class="err">' + esc(e.message) + "</span>";
    }
  }
  async function loadDashboardRecent() {
    const el = $("dashRecentRequests");
    if (!el) return;
    try {
      const j = await api("/api/requests?limit=5");
      const rows = j.data || [];
      if (!rows.length) {
        el.innerHTML = emptyState("\u6682\u65E0\u8C03\u7528");
        return;
      }
      el.innerHTML = wrapTable("<tr><th>\u65F6\u95F4</th><th>\u6A21\u578B</th><th>\u72B6\u6001</th><th>\u8017\u65F6</th></tr>" + rows.map((x) => `<tr><td class="small">${fmtDate(x.started_at)}</td><td class="small">${esc(displayUpstreamModel(x))}</td><td>${statusPill(x.status)}</td><td>${x.latency_ms ?? "\u2014"} ms</td></tr>`).join(""));
    } catch (e) {
      el.innerHTML = '<span class="err small">' + esc(e.message) + "</span>";
    }
  }
  function adminKeyStatus() {
    const el = $("adminKeyStatus");
    if (!el) return;
    if (getSavedAdminKey()) {
      el.innerHTML = '<span class="pill pill-success">\u7BA1\u7406\u5458 Key \u5DF2\u914D\u7F6E</span>';
      return;
    }
    el.innerHTML = '<span class="pill">\u672A\u914D\u7F6E\u7BA1\u7406\u5458 Key</span>';
  }
  async function saveKey(event) {
    event?.preventDefault?.();
    const input = $("adminKey"), message = $("adminKeyMsg"), button = $("adminKeySaveButton");
    const candidate = getKey();
    if (!candidate) {
      if (message) message.innerHTML = '<span class="err">\u8BF7\u8F93\u5165\u7BA1\u7406\u5458 API Key</span>';
      return;
    }
    if (button) button.disabled = true;
    if (message) message.textContent = "\u6B63\u5728\u9A8C\u8BC1\u7BA1\u7406\u5458 API Key\u2026";
    try {
      await apiPublic("/api/admin/keys", { headers: { "X-Admin-Key": candidate }, cache: "no-store" });
      localStorage.setItem("llm_gateway_admin_key", candidate);
      if (input) input.value = candidate;
      adminKeyStatus();
      if (message) message.innerHTML = '<span class="ok">\u9A8C\u8BC1\u6210\u529F\uFF0C\u5DF2\u4FDD\u5B58\u5230\u5F53\u524D\u6D4F\u89C8\u5668\u3002\u8BE5 Key \u53EA\u7528\u4E8E\u670D\u52A1\u5BC6\u94A5\u7BA1\u7406\u3002</span>';
      await loadKeysPanel();
    } catch (e) {
      if (message) message.innerHTML = '<span class="err">\u9A8C\u8BC1\u5931\u8D25\uFF1A' + esc(e.message) + "</span>";
    } finally {
      if (button) button.disabled = false;
    }
  }
  function clearAdminKey() {
    localStorage.removeItem("llm_gateway_admin_key");
    const input = $("adminKey"), message = $("adminKeyMsg");
    if (input) input.value = "";
    if (message) message.innerHTML = '<span class="muted">\u7BA1\u7406\u5458 Key \u5DF2\u6E05\u9664\uFF1BWeb \u767B\u5F55\u72B6\u6001\u4E0D\u53D7\u5F71\u54CD\u3002</span>';
    adminKeyStatus();
    const table = $("keyTable");
    if (table) table.innerHTML = emptyState("\u8BF7\u5148\u8F93\u5165\u5E76\u9A8C\u8BC1\u7BA1\u7406\u5458 API Key\uFF0C\u518D\u7BA1\u7406\u670D\u52A1\u5BC6\u94A5");
  }
  function setGgufMsg(kind, text) {
    const el = $("ggufMsg");
    if (!el) return;
    if (!text) {
      el.innerHTML = "";
      return;
    }
    const cls = kind === "ok" ? "ok" : kind === "warn" ? "warn" : "err";
    el.innerHTML = '<span class="' + cls + '">' + esc(text) + "</span>";
  }
  async function refreshModelsOnly() {
    await Promise.all([loadMmBackendPills(), modelHub.refresh(), loadDownloads()]);
    renderRuntimeCatalog();
    renderGatewayModelTable();
  }
  async function refreshAll() {
    adminKeyStatus();
    if (!isAuthenticated()) {
      setGlobalMsg($("dashboard")?.classList.contains("active") ? "" : '<span class="warn">\u8BF7\u5148\u767B\u5F55\u63A7\u5236\u53F0</span>');
      return;
    }
    try {
      const jobs = [loadDownloads()];
      if ($("dashboard")?.classList.contains("active")) {
        adminStore.reset();
        jobs.push(
          loadSystem({ force: "bootstrap" }).then(() => applyBackendFormFromStore()).catch(() => {
          }),
          loadDashboardRecent()
        );
      }
      if ($("keys")?.classList.contains("active")) jobs.push(loadKeys());
      if ($("requests")?.classList.contains("active")) jobs.push(loadRequests());
      if ($("models-local")?.classList.contains("active")) jobs.push(loadLocalModelsPanel().catch(() => {
      }));
      else if ($("models-download")?.classList.contains("active")) jobs.push(loadDownloadPanel().catch(() => {
      }));
      else if (!modelsNavActive()) jobs.push(loadModels().catch(() => {
      }), loadMmBackendPills());
      await Promise.all(jobs);
      if ($("benchmark")?.classList.contains("active")) await loadBenchPanel();
      setGlobalMsg("");
    } catch (e) {
      setGlobalMsg('<span class="err">' + esc(e.message) + "</span>");
    }
  }
  function findActiveRuntimeItem(j) {
    const loaded = Array.isArray(j.loaded) ? j.loaded : [];
    if (!loaded.length) return null;
    const actual = String(j.active_model || "").trim();
    const route = String(j.gateway_model || "").trim();
    for (const target of [actual, route]) {
      if (!target) continue;
      const hit = loaded.find((x) => String(x.id || "") === target || x.id && target && modelHub._idMatch(x.id, target));
      if (hit) return hit;
    }
    return loaded.length === 1 ? loaded[0] : null;
  }
  function ggufLoadLabel(st) {
    const s = String(st || "unknown").toLowerCase();
    if (s === "loaded") return "\u5DF2\u52A0\u8F7D";
    if (s === "sleeping") return "\u5DF2\u52A0\u8F7D\uFF08\u4F11\u7720\uFF09";
    if (s === "loading") return "\u52A0\u8F7D\u4E2D";
    if (s === "unloaded") return "\u5DF2\u6CE8\u518C\uFF0C\u672A\u52A0\u8F7D";
    if (s === "not_registered") return "\u78C1\u76D8\u5B58\u5728\uFF0C\u5C1A\u672A\u6CE8\u518C";
    if (s === "downloading") return "\u4E0B\u8F7D\u4E2D";
    if (s === "download_failed") return "\u4E0B\u8F7D\u5931\u8D25";
    if (s === "download_cancelled") return "\u4E0B\u8F7D\u5DF2\u53D6\u6D88";
    return "\u65E0\u6CD5\u786E\u8BA4";
  }
  function ggufLoadPill(st) {
    const s = String(st || "unknown").toLowerCase();
    const cls = ["loaded", "sleeping"].includes(s) ? "pill-loaded" : s === "loading" || s === "downloading" ? "pill-running" : s === "unloaded" ? "pill-unloaded" : "pill-warn";
    return `<span class="pill ${cls}">${esc(ggufLoadLabel(st))}</span>`;
  }
  function runtimeMetricCell(label, value, sub) {
    const v = String(value || "").trim() || "\u2014";
    return '<div class="runtime-metric"><span class="runtime-metric-label">' + esc(label) + '</span><span class="runtime-metric-value">' + esc(v) + "</span>" + (sub ? '<span class="runtime-metric-sub">' + esc(sub) + "</span>" : "") + "</div>";
  }
  function runtimeOverviewMeta(j, activeItem) {
    if (!activeItem) return "";
    const cat = (j.catalog || []).find((c) => String(c?.id || "") === String(activeItem.id || ""));
    const merged = { ...cat, ...activeItem };
    const ollamaSub = ollamaCatalogSubtitle(merged);
    if (ollamaSub) return ollamaSub;
    const gk = String(j.gateway_model_key || "").trim();
    if (gk) {
      const row = (modelHub.rows || []).find((r) => r.model_key === gk);
      if (row?.quantization) return String(row.quantization).trim();
    }
    return "";
  }
  function renderRuntimeOverview() {
    const el = $("runtimeOverviewSummary");
    if (!el) return;
    const j = modelHub.runtime || {};
    const noteEl = $("runtimeUnloadNote");
    if (modelHub.runtimeError) {
      el.innerHTML = '<div class="runtime-hero runtime-hero-idle"><div class="runtime-hero-icon" aria-hidden="true"></div><div class="runtime-hero-copy"><p class="runtime-hero-title">\u8FD0\u884C\u65F6\u72B6\u6001\u6682\u4E0D\u53EF\u7528</p><p class="runtime-hero-desc">\u5DF2\u767B\u8BB0\u7684\u672C\u5730\u6A21\u578B\u4ECD\u53EF\u67E5\u770B\u548C\u7BA1\u7406\uFF1B\u8BF7\u68C0\u67E5\u5F53\u524D\u540E\u7AEF\u5730\u5740\u53CA\u670D\u52A1\u72B6\u6001\u540E\u5237\u65B0\u3002</p></div></div>';
      if (noteEl) noteEl.innerHTML = "";
      return;
    }
    const backend = String(j.backend || "llamacpp").toLowerCase();
    const backendLabel3 = backend === "ollama" ? "Ollama" : "llama.cpp";
    const loaded = Array.isArray(j.loaded) ? j.loaded : [];
    const configured = String(j.gateway_model || "").trim();
    const activeItem = findActiveRuntimeItem(j);
    const activeModel = String(activeItem?.id || "").trim();
    const typeLine = runtimeOverviewMeta(j, activeItem);
    const disk = Number(activeItem?.disk_bytes || 0);
    const vram = Number(activeItem?.vram_bytes || 0);
    const ram = Number(activeItem?.ram_bytes || 0);
    if (!loaded.length) {
      const idleHint = backend === "ollama" ? "\u5728\u4E0B\u65B9 <strong>Ollama\u6A21\u578B</strong> \u4E2D\u9009\u62E9\u6761\u76EE\u5E76\u70B9\u51FB\u300C\u52A0\u8F7D\u300D" : "\u5728\u4E0B\u65B9 <strong>Llama \u6A21\u578B\uFF08GGUF\uFF09</strong> \u4E2D\u9009\u62E9\u6761\u76EE\u5E76\u70B9\u51FB\u300C\u52A0\u8F7D\u300D";
      const routeHint = configured ? '<p class="runtime-hero-desc">Gateway \u9ED8\u8BA4\u76EE\u6807\uFF1A<code>' + esc(configured) + "</code>\uFF08\u5F53\u524D\u672A\u786E\u8BA4\u52A0\u8F7D\uFF09</p>" : "";
      el.innerHTML = '<div class="runtime-hero runtime-hero-idle"><div class="runtime-hero-icon" aria-hidden="true"></div><div class="runtime-hero-copy"><p class="runtime-hero-title">\u6682\u65E0\u6A21\u578B\u5728\u5185\u5B58\u4E2D</p><p class="runtime-hero-desc">' + idleHint + "</p>" + routeHint + "</div></div>";
    } else if (!activeItem) {
      const names = loaded.map((x) => "<code>" + esc(x.id || "\u672A\u77E5\u6A21\u578B") + "</code> " + ggufLoadPill(x.status)).join("\u3001");
      const routeHint = configured ? '<p class="runtime-hero-desc">Gateway \u9ED8\u8BA4\u76EE\u6807\uFF1A<code>' + esc(configured) + "</code>\uFF1B\u8BE5\u76EE\u6807\u4E0D\u5728\u5F53\u524D\u5DF2\u52A0\u8F7D\u6E05\u5355\u4E2D\u3002</p>" : "";
      el.innerHTML = '<div class="runtime-hero runtime-hero-live"><div class="runtime-hero-main"><div class="runtime-hero-badges"><span class="pill pill-loaded">\u5185\u5B58\u4E2D ' + loaded.length + ' \u4E2A\u6A21\u578B</span><span class="pill pill-backend">' + esc(backendLabel3) + '</span></div><div class="runtime-hero-desc">' + names + "</div>" + routeHint + "</div></div>";
    } else {
      const displayName = activeModel || "\u2014";
      const status = String(activeItem.status || "loaded").toLowerCase();
      const stateLabel = status === "sleeping" ? "\u5DF2\u52A0\u8F7D\uFF08\u4F11\u7720\uFF09" : status === "loading" ? "\u52A0\u8F7D\u4E2D" : "\u5DF2\u52A0\u8F7D";
      const metrics = [];
      if (typeLine) metrics.push(runtimeMetricCell("\u89C4\u683C", typeLine, ""));
      if (disk > 0) metrics.push(runtimeMetricCell("\u6587\u4EF6\u5927\u5C0F", fmtSize(disk), ""));
      if (backend !== "ollama") {
        if (vram > 0) metrics.push(runtimeMetricCell("\u663E\u5B58", fmtSize(vram), ""));
        if (ram > 0) metrics.push(runtimeMetricCell("\u5185\u5B58", fmtSize(ram), ""));
      }
      const metricsHtml = metrics.length ? '<div class="runtime-hero-metrics">' + metrics.join("") + "</div>" : "";
      const routeHint = configured && modelHub._idMatch(configured, displayName) ? "" : '<p class="runtime-hero-desc">Gateway \u9ED8\u8BA4\u76EE\u6807\uFF1A<code>' + esc(configured || "\u672A\u914D\u7F6E") + "</code></p>";
      el.innerHTML = '<div class="runtime-hero runtime-hero-live' + (metrics.length ? "" : " runtime-hero-live-compact") + '"><div class="runtime-hero-main"><div class="runtime-hero-badges"><span class="pill pill-loaded">' + stateLabel + '</span><span class="pill pill-backend">' + esc(backendLabel3) + '</span></div><span class="runtime-model-pill" title="' + esc(displayName) + '">' + esc(displayName) + "</span>" + routeHint + "</div>" + metricsHtml + "</div>";
    }
    if (noteEl) {
      const notices = [];
      if (j.hot_unload_supported === false) notices.push(j.unload_note || "\u5F53\u524D llama.cpp \u4E0D\u652F\u6301\u70ED\u52A0\u8F7D/\u5378\u8F7D\u5176\u5B83\u6A21\u578B");
      if (modelHub.runtimeWarning) notices.push(modelHub.runtimeWarning);
      noteEl.innerHTML = notices.map((x) => '<span class="warn">' + esc(x) + "</span>").join(" ");
    }
  }
  function backendLabel2(id, backends = []) {
    const item = backends.find((x) => String(x.id || "") === String(id || ""));
    if (item?.label) return String(item.label);
    return id === "ollama" ? "Ollama" : id === "llamacpp" ? "llama.cpp" : String(id || "\u63A8\u7406\u540E\u7AEF");
  }
  function populateBackendOptions(backends, active) {
    const select = $("beBackend");
    if (!select) return;
    const list = Array.isArray(backends) && backends.length ? backends : [
      { id: "llamacpp", label: "llama.cpp", kind: "local" },
      { id: "ollama", label: "Ollama", kind: "local" }
    ];
    const target = String(active || select.value || "llamacpp");
    select.innerHTML = list.map((item) => {
      const id = String(item.id || "").trim();
      if (!id) return "";
      const kind = String(item.kind || "local");
      const model = kind === "remote" && item.default_model ? " \xB7 " + String(item.default_model) : "";
      return '<option value="' + esc(id) + '" data-kind="' + esc(kind) + '">' + esc(String(item.label || id) + model) + "</option>";
    }).join("");
    select.value = list.some((item) => String(item.id || "") === target) ? target : list[0]?.id || "llamacpp";
  }
  function selectedBackendOption() {
    const select = $("beBackend");
    return select?.selectedOptions?.[0] || null;
  }
  function backendProbeStatus(block) {
    if (block?.configured_remote === true) return { cls: "ok", text: "\u5DF2\u914D\u7F6E \xB7 \u9996\u6B21\u63A8\u7406\u65F6\u9A8C\u8BC1\u8FDE\u63A5" };
    if (!block?.base_url) return { cls: "muted", text: "\u672A\u914D\u7F6E" };
    if (block.reachable === true) return { cls: "ok", text: "\u53EF\u8FBE" };
    if (block.reachable === false) {
      const hint = String(block.error || "").trim();
      return { cls: "err", text: hint ? "\u4E0D\u53EF\u8FBE \xB7 " + hint.slice(0, 80) : "\u4E0D\u53EF\u8FBE" };
    }
    const checks = block.checks || [];
    if (!checks.length) return { cls: "muted", text: "\u672A\u914D\u7F6E" };
    const ok = checks.every((c) => Number(c.http_status) >= 200 && Number(c.http_status) < 400);
    const partial = checks.some((c) => Number(c.http_status) >= 200 && Number(c.http_status) < 400);
    if (ok) return { cls: "ok", text: "\u53EF\u8FBE" };
    if (partial) return { cls: "warn", text: "\u90E8\u5206\u53EF\u8FBE" };
    return { cls: "err", text: "\u4E0D\u53EF\u8FBE" };
  }
  function renderBackendProbeCompact(j) {
    const active = (j.active || {}).backend || "llamacpp";
    const backends = Array.isArray(j.available_backends) ? j.available_backends : [
      { id: "llamacpp", label: "llama.cpp", kind: "local" },
      { id: "ollama", label: "Ollama", kind: "local" }
    ];
    populateBackendOptions(backends, active);
    const activeItem = backends.find((x) => String(x.id || "") === active);
    const activeLabel = backendLabel2(active, backends);
    const activeRemote = activeItem?.kind === "remote";
    const probes = j.upstream_probes || {};
    const pills = [];
    for (const item of backends) {
      const id = String(item.id || "");
      const block = probes[id];
      if (!id || !block?.base_url) continue;
      const status = backendProbeStatus(block);
      const current = id === active ? " backend-probe-pill-current" : "";
      pills.push('<span class="backend-probe-pill ' + status.cls + current + '">' + esc(backendLabel2(id, backends)) + " \xB7 " + esc(status.text) + "</span>");
    }
    const proxyOk = j.health === "ok";
    const head = activeRemote ? '<span class="backend-probe-lead ok">\u5F53\u524D ' + esc(activeLabel) + " \xB7 \u5DF2\u914D\u7F6E\uFF08\u9996\u6B21\u63A8\u7406\u65F6\u9A8C\u8BC1\u8FDE\u63A5\uFF09</span>" : proxyOk ? '<span class="backend-probe-lead ok">\u5F53\u524D ' + esc(activeLabel) + " \xB7 \u4EE3\u7406\u53EF\u8FBE</span>" : '<span class="backend-probe-lead err">\u5F53\u524D ' + esc(activeLabel) + " \xB7 " + esc(j.health || "\u5F02\u5E38") + "</span>";
    const body = pills.length ? '<div class="backend-probe-pills">' + pills.join("") + "</div>" : "";
    return head + body;
  }
  function applyBackendProbeUI(j) {
    const probeEl = $("backendProbe");
    if (!probeEl) return;
    probeEl.innerHTML = renderBackendProbeCompact(j);
  }
  async function probeUpstream(ev) {
    ev?.preventDefault?.();
    if (backendProbeInFlight) return;
    const btn = $("btnProbeUpstream");
    const probeEl = $("backendProbe");
    backendProbeInFlight = true;
    if (btn) {
      btn.disabled = true;
      btn.setAttribute("aria-busy", "true");
    }
    if (probeEl) probeEl.innerHTML = '<span class="muted">\u68C0\u6D4B\u4E2D\u2026</span>';
    try {
      const j = await api("/api/admin/backend?refresh=1");
      applyBackendProbeUI(j);
      if (probeEl) probeEl.dataset.probed = "1";
      if ($("dashboard")?.classList.contains("active")) await loadSystem({ force: "lite" }).catch(() => {
      });
    } catch (e) {
      if (probeEl) probeEl.innerHTML = '<span class="err">' + esc(e.message) + "</span>";
    } finally {
      backendProbeInFlight = false;
      if (btn) {
        btn.disabled = false;
        btn.removeAttribute("aria-busy");
      }
    }
  }
  function renderRuntimeCatalog() {
    renderRuntimeOverview();
  }
  async function loadRuntime() {
    await modelHub.refresh();
    renderRuntimeOverview();
  }
  function applyBackendFormFromStore() {
    const a = adminStore.upstream;
    if ($("beBackend") && a?.backend) {
      const select = $("beBackend");
      if ([...select.options].some((option) => option.value === a.backend)) select.value = a.backend;
    }
    const probeEl = $("backendProbe");
    if (probeEl && !probeEl.dataset.probed) {
      probeEl.innerHTML = '<span class="muted small">\u70B9\u51FB\u300C\u68C0\u6D4B\u8FDE\u901A\u6027\u300D\u67E5\u770B\u540E\u7AEF\u72B6\u6001\uFF1B\u8FDC\u7A0B\u8FDE\u63A5\u5728\u9996\u6B21\u63A8\u7406\u65F6\u9A8C\u8BC1</span>';
    }
  }
  async function loadBackendForm(opts = {}) {
    const probeOnly = opts.probeOnly === true;
    const probeEl = $("backendProbe");
    try {
      const j = await api(probeOnly ? "/api/admin/backend?refresh=1" : "/api/admin/backend");
      const active = (j.active || {}).backend || "llamacpp";
      populateBackendOptions(j.available_backends, active);
      if (probeOnly) {
        applyBackendProbeUI(j);
        if (probeEl) probeEl.dataset.probed = "1";
      } else if (probeEl && !probeEl.dataset.probed) {
        if (String(j.health || "").startsWith("remote Provider configured")) {
          applyBackendProbeUI(j);
        } else {
          probeEl.innerHTML = '<span class="muted small">\u70B9\u51FB\u300C\u68C0\u6D4B\u8FDE\u901A\u6027\u300D\u67E5\u770B\u540E\u7AEF\u72B6\u6001\uFF1B\u8FDC\u7A0B\u8FDE\u63A5\u5728\u9996\u6B21\u63A8\u7406\u65F6\u9A8C\u8BC1</span>';
        }
      }
    } catch (e) {
      if (probeEl) probeEl.innerHTML = '<span class="err">' + esc(e.message) + "</span>";
    }
  }
  async function saveBackend() {
    const selected = selectedBackendOption();
    const backend = $("beBackend")?.value || "llamacpp";
    const targetRemote = selected?.dataset?.kind === "remote";
    const targetLabel = selected?.textContent || backend;
    const cur = (await api("/api/admin/backend")).active || {};
    const probeEl = $("backendProbe");
    if (backend === cur.backend) {
      if (probeEl) probeEl.innerHTML = '<span class="muted">\u5F53\u524D\u5DF2\u662F\u8BE5\u4E0A\u6E38\u670D\u52A1</span>';
      return;
    }
    const confirmText = targetRemote ? "\u5207\u6362\u5230\u8FDC\u7A0B Provider\u300C" + targetLabel + "\u300D\u540E\uFF0C\u5C06\u4F7F\u7528\u5176\u914D\u7F6E\u7684\u9ED8\u8BA4\u6A21\u578B\uFF1B\u4E0D\u4F1A\u5378\u8F7D\u5F53\u524D\u672C\u5730\u6A21\u578B\u3002\u662F\u5426\u7EE7\u7EED\uFF1F" : "\u5207\u6362\u672C\u5730\u63A8\u7406\u540E\u7AEF\u53EF\u80FD\u5378\u8F7D\u65E7\u672C\u5730\u540E\u7AEF\u5DF2\u52A0\u8F7D\u7684\u6A21\u578B\uFF0C\u662F\u5426\u7EE7\u7EED\uFF1F";
    if (!confirm(confirmText)) return;
    if (probeEl) probeEl.textContent = "\u5207\u6362\u4E2D\u2026";
    try {
      const result = await api("/api/admin/backend", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ backend }) });
      const active = result.active || {};
      if (probeEl) {
        probeEl.innerHTML = targetRemote ? '<span class="ok">\u5DF2\u5207\u6362\u5230\u8FDC\u7A0B Provider \xB7 \u9ED8\u8BA4\u6A21\u578B ' + esc(active.model_name || "\u914D\u7F6E\u6A21\u578B") + "\u3002\u8FDC\u7A0B\u8FDE\u63A5\u5C06\u5728\u9996\u6B21\u63A8\u7406\u65F6\u9A8C\u8BC1\uFF0C\u672C\u5730\u6A21\u578B\u4E0D\u53D7\u5F71\u54CD\u3002</span>" : '<span class="ok">\u5DF2\u5207\u6362\u672C\u5730\u540E\u7AEF\uFF0C\u8BF7\u5728\u300C\u672C\u5730\u6A21\u578B\u300D\u4E2D\u52A0\u8F7D\u6A21\u578B</span>';
      }
      adminStore.reset();
      await loadSystem({ force: "bootstrap" });
      await loadBackendForm();
      if (!targetRemote && $("models-local")?.classList.contains("active")) await loadRuntime();
      await loadModels();
      if (!targetRemote && $("benchmark")?.classList.contains("active")) await loadBenchPanel({ resume: false });
    } catch (e) {
      if (probeEl) probeEl.innerHTML = '<span class="err">' + esc(e.message) + "</span>";
    }
  }
  async function unloadAllUpstream() {
    if (!confirm("\u5378\u8F7D\u5F53\u524D\u540E\u7AEF\u5168\u90E8\u5DF2\u52A0\u8F7D\u6A21\u578B\uFF1F")) return;
    try {
      await api("/api/admin/backend/unload-all", { method: "POST" });
      if ($("dashboard")?.classList.contains("active")) await loadSystem({ force: "lite" }).catch(() => {
      });
      await loadRuntime();
      await loadModels();
    } catch (e) {
      setGgufMsg("err", e.message);
    }
  }
  function gaugePolar(cx, cy, r, deg) {
    const rad = deg * Math.PI / 180;
    return [cx + r * Math.cos(rad), cy + r * Math.sin(rad)];
  }
  function gaugeSweepDeg(a, b) {
    let s = b - a;
    while (s <= 0) s += 360;
    return s;
  }
  function gaugeArcD(cx, cy, r, deg1, deg2) {
    const [x1, y1] = gaugePolar(cx, cy, r, deg1);
    const [x2, y2] = gaugePolar(cx, cy, r, deg2);
    const sweep = gaugeSweepDeg(deg1, deg2);
    const large = sweep > 180 ? 1 : 0;
    return `M ${x1.toFixed(2)} ${y1.toFixed(2)} A ${r} ${r} 0 ${large} 1 ${x2.toFixed(2)} ${y2.toFixed(2)}`;
  }
  function gaugeValueDeg(pct) {
    const { angMin, angMax } = GAUGE;
    const sweep = gaugeSweepDeg(angMin, angMax);
    return angMin + pct / 100 * sweep;
  }
  function gaugeColorsForPct(p, themeKey) {
    const theme = GAUGE_THEMES[themeKey] || GAUGE_THEMES.cpu;
    if (p >= 90) return { zone: "#ef4444", needle: "#f87171" };
    if (p >= 75) return { zone: "#fbbf24", needle: "#fcd34d" };
    return { zone: theme.zone, needle: theme.needle };
  }
  function gaugeNeedleColor(p, themeKey = "cpu") {
    return gaugeColorsForPct(p, themeKey).needle;
  }
  function gaugeZoneColor(p, themeKey = "cpu") {
    return gaugeColorsForPct(p, themeKey).zone;
  }
  function gaugeSVG(pct, gradId, themeKey = "cpu") {
    const p = Math.max(0, Math.min(100, Number(pct) || 0));
    const theme = GAUGE_THEMES[themeKey] || GAUGE_THEMES.cpu;
    const { cx, cy, rDial, rTrack, rTickOut, rTickIn, rNum, angMin, angMax } = GAUGE;
    const needleAng = gaugeValueDeg(p);
    let ticks = "", nums = "";
    for (let v = 0; v <= 100; v += 5) {
      const deg = gaugeValueDeg(v);
      const major = v % 25 === 0;
      const [x1, y1] = gaugePolar(cx, cy, rTickOut, deg);
      const [x2, y2] = gaugePolar(cx, cy, major ? rTickIn - 3 : rTickIn, deg);
      ticks += `<line class="gauge-tick${major ? " gauge-tick-major" : ""}" x1="${x1.toFixed(1)}" y1="${y1.toFixed(1)}" x2="${x2.toFixed(1)}" y2="${y2.toFixed(1)}"/>`;
      if (v % 50 === 0) {
        const [nx, ny] = gaugePolar(cx, cy, rNum, deg);
        nums += `<text class="gauge-num" x="${nx.toFixed(1)}" y="${(ny + 4).toFixed(1)}" text-anchor="middle">${v}</text>`;
      }
    }
    const { zone: zoneCol, needle: needleCol } = gaugeColorsForPct(p, themeKey);
    const faceArc = gaugeArcD(cx, cy, rDial, angMin, angMax);
    const trackArc = gaugeArcD(cx, cy, rTrack, angMin, angMax);
    const zoneArc = `<path class="gauge-arc-zone" d="${p > 0.5 ? gaugeArcD(cx, cy, rTrack, angMin, needleAng) : ""}" stroke="${p > 0.5 ? zoneCol : "none"}"/>`;
    const rNeedle = rTrack - 10;
    return `<svg class="gauge-svg" viewBox="0 0 200 128" role="img" aria-label="${p.toFixed(0)} percent">
    <defs>
      <radialGradient id="${gradId}" cx="50%" cy="85%" r="70%">
        <stop offset="0%" stop-color="${theme.faceTop}"/>
        <stop offset="100%" stop-color="${theme.faceBot}"/>
      </radialGradient>
    </defs>
    <path class="gauge-face" d="${faceArc} L ${cx} ${cy} Z" fill="url(#${gradId})"/>
    <path class="gauge-bezel" d="${faceArc}" fill="none"/>
    <path class="gauge-arc-bg" d="${trackArc}" fill="none"/>
    ${zoneArc}
    ${ticks}
    ${nums}
    <g class="gauge-needle" transform="rotate(${needleAng.toFixed(2)} ${cx} ${cy})">
      <line class="gauge-needle-line" x1="${cx}" y1="${cy}" x2="${cx + rNeedle}" y2="${cy}" stroke="${needleCol}" stroke-width="3.2"/>
    </g>
    <circle class="gauge-hub-ring" cx="${cx}" cy="${cy}" r="8"/>
    <circle cx="${cx}" cy="${cy}" r="4" fill="#64748b"/>
  </svg>`;
  }
  function easeOutCubic(t) {
    return 1 - Math.pow(1 - t, 4);
  }
  function renderGaugeNeedle(svg, pct, themeKey = "cpu") {
    if (!svg) return;
    const p = Math.max(0, Math.min(100, Number(pct) || 0));
    const needleAng = gaugeValueDeg(p);
    const { zone, needle } = gaugeColorsForPct(p, themeKey);
    const g = svg.querySelector(".gauge-needle");
    if (g) g.setAttribute("transform", `rotate(${needleAng.toFixed(2)} ${GAUGE.cx} ${GAUGE.cy})`);
    const zoneEl = svg.querySelector(".gauge-arc-zone");
    if (zoneEl) {
      if (p <= 0.5) {
        zoneEl.setAttribute("d", "");
        zoneEl.setAttribute("stroke", "none");
      } else {
        zoneEl.setAttribute("d", gaugeArcD(GAUGE.cx, GAUGE.cy, GAUGE.rTrack, GAUGE.angMin, needleAng));
        zoneEl.setAttribute("stroke", zone);
      }
    }
    const line = svg.querySelector(".gauge-needle-line");
    if (line) line.setAttribute("stroke", needle);
  }
  function animateGauges(items, animate) {
    items = items.map((it) => ({ ...it, target: Math.max(0, Math.min(100, Number(it.target) || 0)) }));
    if (!animate || items.some((it) => !it.svg)) {
      items.forEach((it) => {
        gaugeShown[it.key] = it.target;
        renderGaugeNeedle(it.svg, it.target, it.theme);
        if (it.readout) it.readout.textContent = it.target.toFixed(1) + "%";
      });
      return;
    }
    const token = ++gaugeAnimToken;
    const starts = items.map((it) => gaugeShown[it.key] ?? 0);
    const t0 = performance.now();
    function frame(now) {
      if (token !== gaugeAnimToken) return;
      const u = Math.min(1, (now - t0) / GAUGE_ANIM_MS);
      const e = easeOutCubic(u);
      items.forEach((it, i) => {
        const v = starts[i] + (it.target - starts[i]) * e;
        renderGaugeNeedle(it.svg, v, it.theme);
        if (it.readout) it.readout.textContent = v.toFixed(1) + "%";
      });
      if (u < 1) requestAnimationFrame(frame);
      else items.forEach((it) => {
        gaugeShown[it.key] = it.target;
      });
    }
    requestAnimationFrame(frame);
  }
  function renderSysRuntimePanel(inf, llamaOk, redisOk, j) {
    return `<div class="sys-runtime-panel sys-cell-runtime">
    <div class="sys-runtime-deps-col">
      <p class="sys-runtime-kicker">\u4F9D\u8D56\u670D\u52A1</p>
      <div class="sys-runtime-deps-row">
        <div class="sys-status-chip svc-tile-llama ${llamaOk ? "ok" : "err"}">
          <span class="sys-status-dot" aria-hidden="true"></span>
          <div class="sys-status-copy"><span class="sys-status-label">\u4E0A\u6E38\u63A8\u7406</span><b>${llamaOk ? "\u53EF\u8FBE" : esc(j.llama || "\u5F02\u5E38")}</b></div>
        </div>
        <div class="sys-status-chip svc-tile-redis ${redisOk ? "ok" : "err"}">
          <span class="sys-status-dot" aria-hidden="true"></span>
          <div class="sys-status-copy"><span class="sys-status-label">Redis</span><b>${redisOk ? "\u6B63\u5E38" : esc(j.redis || "\u5F02\u5E38")}</b></div>
        </div>
      </div>
    </div>
    <div class="sys-inference-section">
      <p class="sys-runtime-kicker sys-inference-kicker">\u63A8\u7406\u5185\u5B58</p>
      <div class="sys-inference">${renderInferenceStats(inf)}</div>
    </div>
  </div>`;
  }
  function memoryUsagePct(mem, j) {
    const fromApi = Number(j?.memory_usage_percent);
    if (Number.isFinite(fromApi) && fromApi >= 0) return Math.min(100, fromApi);
    const total = Number(mem?.total || 0);
    const used = Number(mem?.used ?? total - Number(mem?.available || 0));
    if (!total) return 0;
    return Math.max(0, Math.min(100, used / total * 100));
  }
  function memoryUsedBytes(mem) {
    const total = Number(mem?.total || 0);
    const used = Number(mem?.used);
    if (Number.isFinite(used) && used >= 0) return used;
    return Math.max(0, total - Number(mem?.available || 0));
  }
  function barClass(p) {
    return p >= 90 ? "sys-bar err" : p >= 80 ? "sys-bar warn" : "sys-bar";
  }
  function inferenceBlockReachable(x) {
    if (!x) return false;
    return x.reachable === true;
  }
  function renderInferenceStats(inf) {
    inf = inf || {};
    const block = (key, label) => {
      const x = inf[key];
      if (!x) return "";
      const ok = inferenceBlockReachable(x);
      const count = Number(x.loaded_count) || 0;
      const reachBadge = ok ? '<span class="inf-badge ok">\u53EF\u8FBE</span>' : '<span class="inf-badge warn">\u5F02\u5E38</span>';
      return `<article class="inf-card inf-card-compact">
      <div class="inf-card-head"><h4>${label}</h4><div class="inf-card-tags">${reachBadge}<span class="inf-badge">${count} \u5728\u5185\u5B58</span></div></div>
    </article>`;
    };
    const html = block("llamacpp", "llama.cpp") + block("ollama", "Ollama");
    return html ? `<div class="inf-stack inf-stack-compact">${html}</div>` : '<div class="inf-empty">\u672A\u914D\u7F6E\u63A8\u7406\u540E\u7AEF</div>';
  }
  function systemViewFromAdminStore() {
    const h = adminStore.host || {};
    const dep = adminStore.dependencies || {};
    const up = String(dep.upstream || "");
    const redis = String(dep.redis || "");
    return {
      cpu: h.cpu,
      cpu_usage_percent: h.cpu_usage_percent,
      memory: h.memory || {},
      memory_usage_percent: h.memory_usage_percent,
      disk: h.disk || {},
      llama: up === "ok" ? "ok" : up || "\u5F02\u5E38",
      redis: redis === "ok" ? "ok" : redis || "\u5F02\u5E38",
      inference: adminStore.inferenceForUI()
    };
  }
  async function loadSystem(opts = {}) {
    const root = $("system");
    if (!root) return;
    const force = opts.force === "bootstrap" || opts.force === "lite" ? opts.force : "auto";
    try {
      await adminStore.syncDashboard(force);
    } catch (e) {
      root.innerHTML = '<span class="err">' + esc(e.message) + "</span>";
      return;
    }
    const j = systemViewFromAdminStore();
    const mem = j.memory || {}, disk = j.disk || {};
    const cpuPct = Number(j.cpu_usage_percent) || 0;
    const memPct = memoryUsagePct(mem, j);
    const memUsed = memoryUsedBytes(mem);
    const memTotal = Number(mem.total || 0);
    const diskTotal = Number(disk.total || 0);
    let diskUsed = Number(disk.used);
    if (!Number.isFinite(diskUsed) || diskUsed < 0) {
      diskUsed = Math.max(0, diskTotal - Number(disk.available || 0));
    }
    const diskPct = diskTotal ? Math.max(0, Math.min(100, diskUsed / diskTotal * 100)) : 0;
    const llamaOk = j.llama === "ok";
    const redisOk = j.redis === "ok";
    let cpuSvg = root.querySelector(".gauge-cpu");
    let memSvg = root.querySelector(".gauge-mem");
    if (cpuSvg && !cpuSvg.querySelector(".gauge-needle-line")) root.innerHTML = "";
    cpuSvg = root.querySelector(".gauge-cpu");
    memSvg = root.querySelector(".gauge-mem");
    let diskSvg = root.querySelector(".gauge-disk");
    const runtimePanel = root.querySelector(".sys-runtime-panel");
    if (cpuSvg && memSvg && diskSvg && runtimePanel) {
      const ro = root.querySelector(".gauge-readout-cpu");
      const rm = root.querySelector(".gauge-readout-mem");
      const rd = root.querySelector(".gauge-readout-disk");
      animateGauges([
        { key: "cpu", target: cpuPct, svg: cpuSvg, readout: ro, theme: "cpu" },
        { key: "mem", target: memPct, svg: memSvg, readout: rm, theme: "mem" },
        { key: "disk", target: diskPct, svg: diskSvg, readout: rd, theme: "disk" }
      ], gaugeDomReady);
      gaugeDomReady = true;
      const so = root.querySelector(".gauge-sub-cpu");
      if (so) so.textContent = `${esc(j.cpu || "\u2014")} \u6838`;
      const sm = root.querySelector(".gauge-sub-mem");
      if (sm) sm.textContent = `${fmtSize(memUsed)} / ${fmtSize(memTotal)}`;
      const sd = root.querySelector(".gauge-sub-disk");
      if (sd) sd.textContent = `${fmtSize(diskUsed)} / ${fmtSize(diskTotal)}`;
      const inf = root.querySelector(".sys-inference");
      if (inf) inf.innerHTML = renderInferenceStats(j.inference);
      const tileL = root.querySelector(".svc-tile-llama");
      if (tileL) {
        tileL.className = "sys-status-chip svc-tile-llama " + (llamaOk ? "ok" : "err");
        const b = tileL.querySelector("b");
        if (b) b.textContent = llamaOk ? "\u53EF\u8FBE" : j.llama || "\u5F02\u5E38";
      }
      const tileR = root.querySelector(".svc-tile-redis");
      if (tileR) {
        tileR.className = "sys-status-chip svc-tile-redis " + (redisOk ? "ok" : "err");
        const b = tileR.querySelector("b");
        if (b) b.textContent = redisOk ? "\u6B63\u5E38" : j.redis || "\u5F02\u5E38";
      }
      dashboardPoll.noteSample(cpuPct, memPct);
      return;
    }
    root.innerHTML = `
    <div class="sys-dashboard">
      <div class="gauge-unit gauge-theme-cpu sys-cell-cpu">
        ${gaugeSVG(cpuPct, "gaugeGradCpu", "cpu")}
        <div class="gauge-foot">
          <div class="gauge-readout gauge-readout-cpu">${cpuPct.toFixed(1)}%</div>
          <div class="gauge-caption"><span class="gauge-title">\u5BBF\u4E3B\u673A CPU</span><span class="gauge-sep">\xB7</span><span class="gauge-sub gauge-sub-cpu">${esc(j.cpu || "\u2014")} \u6838</span></div>
        </div>
      </div>
      <div class="gauge-unit gauge-theme-mem sys-cell-mem">
        ${gaugeSVG(memPct, "gaugeGradMem", "mem")}
        <div class="gauge-foot">
          <div class="gauge-readout gauge-readout-mem">${memPct.toFixed(1)}%</div>
          <div class="gauge-caption"><span class="gauge-title">\u5BBF\u4E3B\u673A\u5185\u5B58</span><span class="gauge-sep">\xB7</span><span class="gauge-sub gauge-sub-mem">${fmtSize(memUsed)} / ${fmtSize(memTotal)}</span></div>
        </div>
      </div>
      <div class="gauge-unit gauge-theme-disk sys-cell-disk">
        ${gaugeSVG(diskPct, "gaugeGradDisk", "disk")}
        <div class="gauge-foot">
          <div class="gauge-readout gauge-readout-disk">${diskPct.toFixed(1)}%</div>
          <div class="gauge-caption"><span class="gauge-title">\u786C\u76D8</span><span class="gauge-sep">\xB7</span><span class="gauge-sub gauge-sub-disk">${fmtSize(diskUsed)} / ${fmtSize(diskTotal)}</span></div>
        </div>
      </div>
      ${renderSysRuntimePanel(j.inference, llamaOk, redisOk, j)}
    </div>`;
    root.querySelector(".sys-cell-cpu svg")?.classList.add("gauge-cpu");
    root.querySelector(".sys-cell-mem svg")?.classList.add("gauge-mem");
    root.querySelector(".sys-cell-disk svg")?.classList.add("gauge-disk");
    gaugeShown.cpu = cpuPct;
    gaugeShown.mem = memPct;
    gaugeShown.disk = diskPct;
    gaugeDomReady = true;
    dashboardPoll.noteSample(cpuPct, memPct);
  }
  async function listFiles() {
    const repo = $("repo").value.trim(), rev = $("revision").value.trim() || "main";
    if (!repo) {
      $("filesMsg").textContent = "\u8BF7\u8F93\u5165\u4ED3\u5E93\u540D";
      return;
    }
    $("filesMsg").textContent = "\u8BFB\u53D6\u4E2D\u2026";
    try {
      const j = await api("/api/hf/files?repo=" + encodeURIComponent(repo) + "&revision=" + encodeURIComponent(rev));
      const fs = j.data || [];
      $("filesMsg").textContent = `${fs.length} \u4E2A GGUF`;
      $("filesBox").innerHTML = !fs.length ? emptyState("\u65E0\u6587\u4EF6") : wrapTable("<tr><th>\u6587\u4EF6</th><th>\u5927\u5C0F</th><th>SHA256</th><th></th></tr>" + fs.map((f) => `<tr><td>${esc(f.path)}</td><td>${fmtSize(f.size)}</td><td class="small">${esc(f.sha256 || "\u2014")}</td><td><button type="button" class="btn sm" data-hf-file="${esc(f.path)}" data-hf-repo="${esc(repo)}">\u9009\u62E9</button></td></tr>`).join(""));
    } catch (e) {
      $("filesMsg").innerHTML = '<span class="err">' + esc(e.message) + "</span>";
    }
  }
  function chooseFile(file, repo) {
    $("filename").value = file;
    $("repo").value = repo;
    $("displayName").value = file.split("/").pop().replace(/\.gguf$/i, "");
    $("modelKey").value = slug($("displayName").value);
    $("quant").value = (($("displayName").value.match(/(Q[0-9][A-Z0-9_\-]*)/i) || [])[1] || "").toUpperCase();
    $("installForm").style.display = "block";
    if (globalThis.ModelDownload?.isGgufLocal?.(repo, file)) {
      $("filesMsg").innerHTML = '<span class="warn">\u672C\u5730\u5DF2\u5B58\u5728\u8BE5 GGUF\uFF08' + esc(repo + " / " + file) + "\uFF09\uFF0C\u65E0\u9700\u91CD\u590D\u4E0B\u8F7D</span>";
    }
  }
  async function installModel() {
    const b = { model_key: $("modelKey").value.trim(), display_name: $("displayName").value.trim(), repo: $("repo").value.trim(), revision: $("revision").value.trim() || "main", filename: $("filename").value.trim(), quantization: $("quant").value.trim() };
    if (globalThis.ModelDownload?.isGgufLocal?.(b.repo, b.filename)) {
      $("filesMsg").innerHTML = '<span class="warn">\u672C\u5730\u5DF2\u5B58\u5728\u8BE5 GGUF \u6587\u4EF6\uFF0C\u65E0\u9700\u91CD\u590D\u4E0B\u8F7D</span>';
      return;
    }
    try {
      await api("/api/models/install", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(b) });
      $("filesMsg").innerHTML = '<span class="ok">\u4EFB\u52A1\u5DF2\u521B\u5EFA</span>';
      refreshAll();
    } catch (e) {
      const msg = e.message || "";
      $("filesMsg").innerHTML = '<span class="err">' + esc(msg.includes("\u5DF2\u5B58\u5728") ? msg : "\u5B89\u88C5\u5931\u8D25\uFF1A" + msg) + "</span>";
    }
  }
  function renderGatewayModelTable() {
    const el = $("modelTable");
    if (!el) return;
    const rows = modelHub.tableRows();
    const warnings = $("modelHubWarnings");
    if (warnings) {
      const notices = [];
      if (modelHub.rowsError) notices.push('<span class="err">' + esc(modelHub.rowsError) + "</span>");
      if (modelHub.runtimeError) notices.push('<span class="warn">' + esc(modelHub.runtimeError) + "</span>");
      if (modelHub.runtimeWarning) notices.push('<span class="warn">' + esc(modelHub.runtimeWarning) + "</span>");
      warnings.innerHTML = notices.join("<br>");
      warnings.hidden = !notices.length;
    }
    el.innerHTML = !rows.length ? modelHub.rowsError ? "" : emptyState("\u6682\u65E0 GGUF \u6A21\u578B\uFF1B\u8BF7\u68C0\u67E5\u5171\u4EAB\u6A21\u578B\u76EE\u5F55\u6216\u4ECE\u300C\u6A21\u578B\u4E0B\u8F7D\u300D\u5B89\u88C5") : wrapTable(
      "<tr><th>\u6A21\u578B\u540D\u79F0</th><th>\u6A21\u578B ID / \u952E</th><th>\u5927\u5C0F</th><th>\u72B6\u6001</th><th>\u64CD\u4F5C</th></tr>" + rows.map((x) => {
        const rt = x._runtime || modelHub.runtimeItemForRow(x);
        const managed = x._managed === true;
        const modelKey = String(x.model_key || "").trim();
        const modelId = String(rt?.id || x.upstream_name || x.filename || x.local_path || "").trim();
        const name = String(x.display_name || rt?.display_name || x.filename || modelId || modelKey).trim();
        const st = modelHub.loadState(x);
        const sizeBytes = modelHub.diskBytes(x);
        const routerMode = Boolean(modelHub.runtime?.hot_unload_supported);
        const canOperate = Boolean(routerMode && !modelHub.runtimeError && (rt?.registered || rt?.loaded));
        const loadButton = canOperate && rt?.registered && !rt.loaded && st !== "loading" ? inferButtonHtml("llamacpp", "load", { modelId, modelKey, title: "Llama \u6A21\u578B \xB7 \u52A0\u8F7D \xB7 " + (modelId || modelKey), label: "\u52A0\u8F7D" }) : "";
        const unloadButton = canOperate && rt.loaded ? inferButtonHtml("llamacpp", "unload", { modelId, modelKey, title: "Llama \u6A21\u578B \xB7 \u5378\u8F7D \xB7 " + (modelId || modelKey), confirmMsg: "\u4ECE llama.cpp \u5378\u8F7D " + (modelId || modelKey) + "\uFF1F", label: "\u5378\u8F7D" }) : "";
        const deleteButton = managed && modelKey ? '<button type="button" class="btn sm danger" data-model-delete="gguf" data-model-key="' + esc(modelKey) + '">\u5220\u9664</button>' : rt?.disk_present && rt?.path ? '<button type="button" class="btn sm danger" data-model-delete="gguf-disk" data-model-path="' + esc(String(rt.path)) + '">\u5220\u9664</button>' : "";
        const defaultPill = rt?.active ? ' <span class="pill pill-active">Gateway \u9ED8\u8BA4</span>' : "";
        return "<tr><td>" + esc(name) + (x.quantization ? '<div class="small muted">' + esc(x.quantization) + "</div>" : "") + defaultPill + "</td><td><code>" + esc(modelKey || modelId || "\u2014") + '</code></td><td class="tabular-nums">' + fmtSize(sizeBytes) + "</td><td>" + ggufLoadPill(st) + '</td><td><div class="td-actions">' + loadButton + unloadButton + deleteButton + "</div></td></tr>";
      }).join("")
    );
  }
  async function loadModels() {
    await modelHub.refresh();
    renderGatewayModelTable();
  }
  async function selectGatewayModel(key) {
    return inferenceOp("llamacpp", "load", { model_key: key }, `\u672C\u5730 GGUF \xB7 \u52A0\u8F7D \xB7 ${key}`);
  }
  function setDownloadFeedback(html) {
    const cm = $("catalogMsg");
    if (cm) {
      cm.innerHTML = html;
      return;
    }
    setGlobalMsg(html);
  }
  async function modelAction(k, a) {
    if (a === "load" || a === "unload") {
      await inferenceOp("llamacpp", a, { model_key: k }, `\u672C\u5730 GGUF \xB7 ${a} \xB7 ${k}`);
      return;
    }
    try {
      await api("/api/models/" + encodeURIComponent(k) + "/" + a, { method: "POST" });
      if (a === "cancel" || a === "retry") {
        if (a === "retry") setDownloadFeedback('<span class="ok">\u5DF2\u7EE7\u7EED\u4E0B\u8F7D <code>' + esc(k) + "</code></span>");
        await refreshAll();
      } else await refreshModelsOnly();
    } catch (e) {
      setDownloadFeedback('<span class="err">' + esc(e.message) + "</span>");
      setGgufMsg("err", e.message);
    }
  }
  async function retryHFDownload(modelKey, btn) {
    modelKey = String(modelKey || "").trim();
    if (!modelKey) return;
    if (btn) {
      btn.disabled = true;
      btn.setAttribute("aria-busy", "true");
    }
    setDownloadFeedback('<span class="muted">\u6B63\u5728\u7EE7\u7EED\u4E0B\u8F7D <code>' + esc(modelKey) + "</code>\u2026</span>");
    try {
      await api("/api/models/" + encodeURIComponent(modelKey) + "/retry", { method: "POST" });
      setDownloadFeedback('<span class="ok">\u5DF2\u7EE7\u7EED\u4E0B\u8F7D <code>' + esc(modelKey) + "</code>\uFF0C\u8BF7\u770B\u4E0B\u65B9\u8FDB\u5EA6</span>");
      await loadDownloads();
      $("downloads")?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    } catch (e) {
      setDownloadFeedback('<span class="err">' + esc(e.message) + "</span>");
    } finally {
      if (btn) {
        btn.disabled = false;
        btn.removeAttribute("aria-busy");
      }
    }
  }
  async function deleteModel(k) {
    if (!isAuthenticated()) {
      setGgufMsg("err", "\u8BF7\u5148\u767B\u5F55\u63A7\u5236\u53F0");
      return;
    }
    if (!confirm("\u786E\u5B9A\u5220\u9664\u78C1\u76D8\u4E0A\u7684 GGUF \u53CA\u6570\u636E\u5E93\u8BB0\u5F55\uFF1F")) return;
    try {
      await api("/api/models/" + encodeURIComponent(k), { method: "DELETE" });
      setGgufMsg("ok", "\u5DF2\u5220\u9664");
      await modelHub.refresh({ force: true });
      renderGatewayModelTable();
      renderRuntimeCatalog();
    } catch (e) {
      setGgufMsg("err", e.message);
    }
  }
  async function deleteDiskGGUFModel(modelPath) {
    if (!isAuthenticated()) {
      setGgufMsg("err", "\u8BF7\u5148\u767B\u5F55\u63A7\u5236\u53F0");
      return;
    }
    if (!confirm("\u786E\u5B9A\u4ECE\u5171\u4EAB\u6A21\u578B\u76EE\u5F55\u6C38\u4E45\u5220\u9664\u8BE5 GGUF \u6A21\u578B\uFF1F\u5982\u679C\u6A21\u578B\u7531\u591A\u4E2A\u5206\u7247\u7EC4\u6210\uFF0C\u6240\u6709\u5206\u7247\u90FD\u4F1A\u4E00\u8D77\u5220\u9664\uFF1B\u6B64\u64CD\u4F5C\u4E0D\u53EF\u6062\u590D\u3002")) return;
    try {
      const result = await api("/api/admin/gguf/delete", {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ model_path: modelPath })
      });
      const deleted = Number(result.deleted_files || 0);
      setGgufMsg(result.warning ? "warn" : "ok", result.warning || "\u5DF2\u5220\u9664 GGUF \u6A21\u578B\uFF08" + deleted + " \u4E2A\u6587\u4EF6\uFF09");
      await modelHub.refresh({ force: true });
      renderGatewayModelTable();
      renderRuntimeCatalog();
    } catch (e) {
      setGgufMsg("err", e.message);
    }
  }
  function isTerminalDownloadStatus(st) {
    const v = String(st || "").toLowerCase();
    return v === "download_failed" || v === "download_cancelled" || v === "failed";
  }
  function initDownloadsTableActions() {
    const root = $("downloads");
    if (!root || root.dataset.dlBound) return;
    root.dataset.dlBound = "1";
    root.addEventListener("click", (ev) => {
      const btn = ev.target.closest?.("button[data-dl-action]");
      if (!btn || btn.disabled) return;
      const action = btn.dataset.dlAction || "";
      const key = btn.dataset.dlKey || "";
      const name = btn.dataset.dlName || "";
      if (action === "retry-hf") void retryHFDownload(key, btn);
      else if (action === "retry-ollama") void retryOllamaPull(name, btn);
      else if (action === "cancel-hf") void modelAction(key, "cancel");
      else if (action === "cancel-ollama") void cancelOllamaPull(key);
      else if (action === "dismiss") void dismissDownloadRecord(key);
    });
  }
  async function dismissDownloadRecord(modelKey) {
    if (!modelKey) return;
    try {
      await api("/api/downloads/dismiss", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ model_key: modelKey }) });
      await loadDownloads();
    } catch (e) {
      setGlobalMsg('<span class="err">' + esc(e.message) + "</span>");
    }
  }
  async function clearFailedDownloads() {
    if (!confirm("\u6E05\u9664\u6240\u6709\u5DF2\u5931\u8D25/\u5DF2\u53D6\u6D88\u7684\u4E0B\u8F7D\u4EFB\u52A1\u8BB0\u5F55\uFF1F\u4E0D\u4F1A\u5220\u9664\u5DF2\u6210\u529F\u5B89\u88C5\u7684\u6A21\u578B\u3002")) return;
    try {
      const j = await api("/api/downloads/dismiss", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ all_failed: true }) });
      const n = Number(j.cleared || 0);
      if ($("catalogMsg")) $("catalogMsg").innerHTML = '<span class="ok">\u5DF2\u6E05\u9664 ' + n + " \u6761\u8BB0\u5F55</span>";
      await loadDownloads();
    } catch (e) {
      setGlobalMsg('<span class="err">' + esc(e.message) + "</span>");
    }
  }
  async function loadDownloads() {
    const el = $("downloads");
    if (!el) return;
    let rows = [];
    try {
      const j = await api("/api/downloads");
      rows = j.data || [];
    } catch (e) {
      el.innerHTML = '<span class="err">\u65E0\u6CD5\u52A0\u8F7D\u4E0B\u8F7D\u4EFB\u52A1\uFF1A' + esc(e.message) + "</span>";
      return;
    }
    const hasFailed = rows.some((x) => isTerminalDownloadStatus(x.status));
    const clearBtn = $("btnClearFailedDownloads");
    if (clearBtn) clearBtn.hidden = !hasFailed;
    el.innerHTML = !rows.length ? emptyState("\u65E0\u4EFB\u52A1") : wrapTable(
      "<tr><th>\u4EFB\u52A1</th><th>\u7C7B\u578B</th><th>\u72B6\u6001</th><th>\u8FDB\u5EA6</th><th>\u5927\u5C0F</th><th></th></tr>" + rows.map((x) => {
        const label = x.kind === "ollama" ? x.name || x.model_key : x.model_key;
        const kind = x.kind === "ollama" ? "Ollama" : "HF GGUF";
        const p = x.total ? Math.min(100, x.done / x.total * 100) : 0;
        const pct = x.total ? p.toFixed(0) + "%" : x.status === "downloading" ? "\u2026" : "\u2014";
        let actions = "";
        if (x.status === "downloading") {
          actions = x.kind === "ollama" ? `<button type="button" class="btn sm" data-dl-action="cancel-ollama" data-dl-key="${esc(x.model_key)}">\u53D6\u6D88</button>` : `<button type="button" class="btn sm" data-dl-action="cancel-hf" data-dl-key="${esc(x.model_key)}">\u53D6\u6D88</button>`;
        } else if (isTerminalDownloadStatus(x.status)) {
          if (x.kind === "ollama" && (x.name || x.model_key)) {
            const pullName = String(x.name || "").trim() || String(x.model_key || "").replace(/^ollama\//, "");
            if (pullName) {
              actions = `<button type="button" class="btn sm primary" data-dl-action="retry-ollama" data-dl-name="${esc(pullName)}">\u7EE7\u7EED\u4E0B\u8F7D</button> `;
            }
          } else if (x.kind !== "ollama" && x.model_key) {
            actions = `<button type="button" class="btn sm primary" data-dl-action="retry-hf" data-dl-key="${esc(x.model_key)}">\u7EE7\u7EED\u4E0B\u8F7D</button> `;
          }
          actions += `<button type="button" class="btn sm ghost" data-dl-action="dismiss" data-dl-key="${esc(x.model_key)}">\u6E05\u9664</button>`;
        }
        const showErr = x.error && String(x.status || "").toLowerCase() !== "downloading";
        const errHint = showErr ? `<div class="small err">${esc(x.error)}</div>` : "";
        return `<tr><td><code>${esc(label)}</code>${errHint}</td><td class="small">${esc(kind)}</td><td>${statusPill(x.status)}</td><td><span class="progress"><i style="width:${p}%"></i></span> ${pct}</td><td>${fmtSize(x.done)}${x.total ? "/" + fmtSize(x.total) : ""}</td><td>${actions}</td></tr>`;
      }).join("")
    );
  }
  async function cancelOllamaPull(jobId) {
    if (!jobId) return;
    try {
      await api("/api/admin/ollama/pull/cancel", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ job_id: jobId }) });
      await loadDownloads();
      if ($("catalogMsg")) $("catalogMsg").innerHTML = '<span class="muted">\u5DF2\u8BF7\u6C42\u53D6\u6D88 Ollama \u4E0B\u8F7D</span>';
    } catch (e) {
      setGlobalMsg('<span class="err">' + esc(e.message) + "</span>");
    }
  }
  async function retryOllamaPull(name, btn) {
    name = String(name || "").trim();
    if (!name) return;
    if (btn) {
      btn.disabled = true;
      btn.setAttribute("aria-busy", "true");
    }
    setDownloadFeedback('<span class="muted">\u6B63\u5728\u7EE7\u7EED Ollama \u4E0B\u8F7D <code>' + esc(name) + "</code>\u2026</span>");
    try {
      await api("/api/admin/ollama/pull", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
      setDownloadFeedback('<span class="ok">\u5DF2\u7EE7\u7EED Ollama \u4E0B\u8F7D <code>' + esc(name) + "</code>\uFF0C\u8BF7\u770B\u4E0B\u65B9\u8FDB\u5EA6</span>");
      await loadDownloads();
      $("downloads")?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    } catch (e) {
      setDownloadFeedback('<span class="err">' + esc(e.message) + "</span>");
    } finally {
      if (btn) {
        btn.disabled = false;
        btn.removeAttribute("aria-busy");
      }
    }
  }
  async function loadKeysPanel() {
    adminKeyStatus();
    if (!getSavedAdminKey()) {
      const table = $("keyTable");
      if (table) table.innerHTML = emptyState("\u8BF7\u5148\u5728\u4E0A\u65B9\u8F93\u5165\u5E76\u9A8C\u8BC1\u7BA1\u7406\u5458 API Key\uFF0C\u518D\u7BA1\u7406\u670D\u52A1\u5BC6\u94A5");
      return;
    }
    await loadKeys();
  }
  async function loadKeys() {
    const table = $("keyTable");
    try {
      const j = await apiServiceKeys("/api/admin/keys");
      const rows = j.data || [];
      table.innerHTML = !rows.length ? emptyState("\u6682\u65E0\u5BC6\u94A5") : wrapTable("<tr><th>\u540D\u79F0</th><th>\u524D\u7F00</th><th>\u72B6\u6001</th><th>\u9650\u6D41</th><th>\u5141\u8BB8\u6A21\u578B</th><th>\u8FC7\u671F</th><th>\u64CD\u4F5C</th></tr>" + rows.map((x) => `<tr><td>${esc(x.name)}</td><td><code>${esc(x.prefix)}\u2026</code></td><td>${statusPill(x.status)}</td><td>${x.rpm}/\u5206\u949F</td><td class="small">${esc((x.allowed_models || []).join(", ") || "\u5168\u90E8")}</td><td class="small">${fmtDate(x.expires_at)}</td><td><div class="td-actions"><button type="button" class="btn sm" onclick="toggleKey(${x.id},'${x.status === "active" ? "disable" : "enable"}')">${x.status === "active" ? "\u7981\u7528" : "\u542F\u7528"}</button><button type="button" class="btn sm" onclick="rotateKey(${x.id})">\u8F6E\u6362</button><button type="button" class="btn sm danger" onclick="deleteKey(${x.id})">\u5220\u9664</button></div></td></tr>`).join(""));
    } catch (e) {
      if (table) table.innerHTML = '<div class="msg"><span class="err">' + esc(e.message) + "</span></div>";
      throw e;
    }
  }
  let creatingKey = false;
  async function createKey() {
    if (creatingKey) return;
    creatingKey = true;
    const button = $("keyCreateButton");
    const wasDisabled = button?.disabled ?? false;
    if (button) button.disabled = true;
    const models = $("keyModels").value.split(",").map((x) => x.trim()).filter(Boolean);
    try {
      const j = await apiServiceKeys("/api/admin/keys", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: $("keyName").value.trim(), rpm: Number($("keyRPM").value) || 60, allowed_models: models, expires_at: $("keyExpires").value.trim() || null }) });
      $("newKeyBox").style.display = "block";
      $("newKeyBox").innerHTML = '<div class="keybox">' + esc(j.key) + '</div><div class="msg muted">请立即复制</div>';
      loadKeys().catch(() => {});
    } catch (e) {
      $("newKeyBox").style.display = "block";
      $("newKeyBox").innerHTML = '<span class="err">' + esc(e.message) + "</span>";
    } finally {
      creatingKey = false;
      if (button) button.disabled = wasDisabled;
    }
  }
  async function toggleKey(id, a) {
    try {
      await apiServiceKeys("/api/admin/keys/" + id + "/" + a, { method: "POST" });
      loadKeys().catch(() => {
      });
    } catch (e) {
      alert(e.message);
    }
  }
  async function rotateKey(id) {
    if (!confirm("\u8F6E\u6362\u540E\u65E7\u5BC6\u94A5\u7ACB\u5373\u5931\u6548\uFF0C\u7EE7\u7EED\uFF1F")) return;
    try {
      const j = await apiServiceKeys("/api/admin/keys/" + id + "/rotate", { method: "POST" });
      alert("\u65B0\u5BC6\u94A5\uFF1A\\n\\n" + j.key);
      loadKeys().catch(() => {
      });
    } catch (e) {
      alert(e.message);
    }
  }
  async function deleteKey(id) {
    if (!confirm("\u786E\u5B9A\u5220\u9664\u8BE5\u5BC6\u94A5\uFF1F")) return;
    try {
      await apiServiceKeys("/api/admin/keys/" + id, { method: "DELETE" });
      loadKeys().catch(() => {
      });
    } catch (e) {
      alert(e.message);
    }
  }
  function benchBackendKey(be) {
    return String(be || "").toLowerCase() === "ollama" ? "ollama" : "llamacpp";
  }
  function benchDefaultsForBackend(be) {
    const key = benchBackendKey(be);
    return { ...BENCH_SHARED, ...BENCH_PRESETS[key] || BENCH_PRESETS.llamacpp };
  }
  function readBenchStoreRaw() {
    try {
      return JSON.parse(localStorage.getItem(BENCH_STORE_KEY) || "{}");
    } catch {
      return {};
    }
  }
  function normalizeBenchStore() {
    const st = readBenchStoreRaw();
    if (st.params && !st.paramsByBackend) {
      const key = benchBackendKey(st.lastBackend || "llamacpp");
      st.paramsByBackend = { [key]: st.params };
      delete st.params;
      localStorage.setItem(BENCH_STORE_KEY, JSON.stringify(st));
    }
    return readBenchStoreRaw();
  }
  function readBenchStore() {
    return normalizeBenchStore();
  }
  function writeBenchStore(patch) {
    const prev = readBenchStore();
    const next = { ...prev, ...patch };
    if (patch.paramsByBackend) {
      next.paramsByBackend = { ...prev.paramsByBackend || {}, ...patch.paramsByBackend };
    }
    localStorage.setItem(BENCH_STORE_KEY, JSON.stringify(next));
  }
  function persistBenchParams() {
    if (!benchCurrentBackend) return;
    writeBenchStore({
      lastBackend: benchCurrentBackend,
      paramsByBackend: { [benchCurrentBackend]: collectBenchParams() }
    });
  }
  function updateBenchPresetHint(be) {
    const el = $("benchPresetHint");
    if (!el) return;
    const key = benchBackendKey(be);
    const name = key === "ollama" ? "Ollama" : "llama.cpp";
    const d = BENCH_PRESETS[key];
    const bits = [
      `\u53C2\u6570\u9884\u8BBE\uFF1A<b>${esc(name)}</b>\uFF08\u5207\u6362\u4E0A\u6E38\u670D\u52A1\u4F1A\u81EA\u52A8\u5207\u6362\uFF1B\u5DF2\u4FDD\u5B58\u7684\u8BE5\u540E\u7AEF\u53C2\u6570\u4F1A\u4FDD\u7559\uFF09`,
      d.min_p != null ? `min_p ${d.min_p}` : "min_p \u4E0D\u4F20",
      `repeat ${d.repeat_penalty}`,
      d.enable_thinking === false ? "thinking \u5173" : d.enable_thinking === true ? "thinking \u5F00" : "thinking \u900F\u4F20"
    ];
    el.innerHTML = bits.join(" \xB7 ");
  }
  function syncBenchParamsForBackend(be) {
    const key = benchBackendKey(be);
    benchCurrentBackend = key;
    const st = readBenchStore();
    const saved = st.paramsByBackend?.[key];
    applyBenchParams({ ...benchDefaultsForBackend(key), ...saved || {} });
    writeBenchStore({ lastBackend: key });
    updateBenchPresetHint(key);
  }
  function benchOptionalNum(id) {
    const el = $(id);
    if (!el) return void 0;
    const t = String(el.value ?? "").trim();
    if (t === "") return void 0;
    const n = Number(t);
    return Number.isFinite(n) ? n : void 0;
  }
  function benchOptionalInt(id) {
    const n = benchOptionalNum(id);
    return n == null ? void 0 : Math.trunc(n);
  }
  function benchNumFromInput(id, fallback) {
    const n = benchOptionalNum(id);
    return n == null ? fallback : n;
  }
  function benchIntFromInput(id, fallback) {
    const n = benchOptionalInt(id);
    return n == null ? fallback : n;
  }
  function collectBenchParams() {
    const stopRaw = ($("benchStop")?.value || "").split("\n").map((s) => s.trim()).filter(Boolean);
    const think = $("benchThinking")?.value ?? "";
    let enable_thinking;
    if (think === "true") enable_thinking = true;
    else if (think === "false") enable_thinking = false;
    const d = benchDefaultsForBackend(benchCurrentBackend);
    return {
      prompt: ($("benchPrompt")?.value || "").trim() || d.prompt,
      max_tokens: benchIntFromInput("benchTokens", d.max_tokens),
      temperature: benchNumFromInput("benchTemperature", d.temperature),
      top_p: benchNumFromInput("benchTopP", d.top_p),
      top_k: benchIntFromInput("benchTopK", d.top_k),
      min_p: benchOptionalNum("benchMinP"),
      frequency_penalty: benchNumFromInput("benchFreqPen", d.frequency_penalty),
      presence_penalty: benchNumFromInput("benchPresPen", d.presence_penalty),
      repeat_penalty: benchNumFromInput("benchRepeatPen", d.repeat_penalty),
      seed: benchOptionalInt("benchSeed"),
      enable_thinking,
      stop: stopRaw.length ? stopRaw : void 0
    };
  }
  function applyBenchParams(p) {
    const m = { ...benchDefaultsForBackend(benchCurrentBackend), ...p || {} };
    if ($("benchPrompt")) $("benchPrompt").value = m.prompt ?? "";
    if ($("benchTokens")) $("benchTokens").value = m.max_tokens ?? "";
    const setNum = (id, v) => {
      const el = $(id);
      if (!el) return;
      el.value = v == null || v === "" ? "" : v;
    };
    setNum("benchTemperature", m.temperature);
    setNum("benchTopP", m.top_p);
    setNum("benchTopK", m.top_k);
    setNum("benchMinP", m.min_p);
    setNum("benchFreqPen", m.frequency_penalty);
    setNum("benchPresPen", m.presence_penalty);
    setNum("benchRepeatPen", m.repeat_penalty);
    setNum("benchSeed", m.seed);
    if ($("benchThinking")) {
      if (m.enable_thinking === true) $("benchThinking").value = "true";
      else if (m.enable_thinking === false) $("benchThinking").value = "false";
      else $("benchThinking").value = "";
    }
    if ($("benchStop")) $("benchStop").value = m.stop && m.stop.length ? m.stop.join("\n") : "";
  }
  function benchPayloadForApi(target) {
    const upstream = typeof target === "string" ? target : target?.upstream || "";
    const gatewayKey = (typeof target === "string" ? "" : target?.gatewayKey || "").trim();
    const p = collectBenchParams();
    const body = gatewayKey ? { model_key: gatewayKey, prompt: p.prompt, max_tokens: p.max_tokens } : { use_active: true, model_id: upstream, prompt: p.prompt, max_tokens: p.max_tokens };
    if (p.temperature != null) body.temperature = p.temperature;
    if (p.top_p != null) body.top_p = p.top_p;
    if (p.top_k != null) body.top_k = p.top_k;
    if (p.min_p != null) body.min_p = p.min_p;
    if (p.frequency_penalty != null) body.frequency_penalty = p.frequency_penalty;
    if (p.presence_penalty != null) body.presence_penalty = p.presence_penalty;
    if (p.repeat_penalty != null) body.repeat_penalty = p.repeat_penalty;
    if (p.seed != null) body.seed = p.seed;
    if (p.enable_thinking != null) body.enable_thinking = p.enable_thinking;
    if (p.stop?.length) body.stop = p.stop;
    return body;
  }
  function setBenchRunning(running) {
    const run = $("benchRunBtn"), stop = $("benchStopBtn");
    if (run) run.disabled = !!running;
    if (stop) stop.disabled = !running;
  }
  function benchDrawerFocusable(root) {
    const panel = root?.querySelector(".drawer-panel");
    if (!panel) return [];
    return [...panel.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])')].filter((el) => {
      const closedDetails = el.closest("details:not([open])");
      return !el.hidden && el.getClientRects().length > 0 && getComputedStyle(el).visibility !== "hidden" && (!closedDetails || el.matches("details:not([open]) > summary"));
    });
  }
  function openBenchParamsDrawer() {
    const root = $("benchParamsDrawer");
    if (!root || root.classList.contains("open")) return;
    benchDrawerTrigger = document.activeElement;
    root.hidden = false;
    root.classList.add("open");
    root.setAttribute("aria-hidden", "false");
    document.body.classList.add("drawer-open");
    root.querySelector(".drawer-head button")?.focus();
  }
  function closeBenchParamsDrawer() {
    const root = $("benchParamsDrawer");
    if (!root || !root.classList.contains("open")) return;
    root.classList.remove("open");
    root.hidden = true;
    root.setAttribute("aria-hidden", "true");
    document.body.classList.remove("drawer-open");
    const trigger = benchDrawerTrigger;
    benchDrawerTrigger = null;
    if (trigger?.isConnected) trigger.focus();
  }
  async function loadBenchPanel(opts = {}) {
    try {
      const beJ = await api("/api/admin/backend");
      const be = (beJ.active || {}).backend || "llamacpp";
      syncBenchParamsForBackend(be);
      let name = (beJ.inference_active_model_id || "").trim();
      let gk = "";
      if (be === "llamacpp") {
        try {
          const rt = await api("/api/admin/runtime");
          gk = rt.gateway_model_key || "";
        } catch {
        }
      }
      if (!name) {
        const j = await api("/api/admin/runtime/preview?backend=" + encodeURIComponent(be));
        name = (j.active_model || "").trim();
        if (!name && (j.loaded || []).length) name = (j.loaded[0].id || j.loaded[0].ID || "").trim();
      }
      if (!name) {
        $("benchTarget").innerHTML = '<span class="err">\u5C1A\u672A\u52A0\u8F7D\u4E0A\u6E38\u6A21\u578B</span> \xB7 \u8BF7\u5728\u300C\u6A21\u578B\u7BA1\u7406\u300D\u5BF9\u5F53\u524D API \u540E\u7AEF\uFF08' + esc(be) + "\uFF09\u52A0\u8F7D\u6A21\u578B";
        return;
      }
      $("benchTarget").innerHTML = "\u63A8\u7406\u540E\u7AEF\uFF08API \u6D41\u91CF\uFF09\uFF1A<b>" + esc(be === "ollama" ? "Ollama" : be === "llamacpp" ? "llama.cpp" : be) + "</b> \xB7 \u4E0A\u6E38\u6A21\u578B\uFF1A<b><code>" + esc(name) + "</code></b>" + (gk ? " \xB7 \u7F51\u5173\u6A21\u578B\u952E\uFF1A<code>" + esc(gk) + "</code>" : "");
      updateBenchReqPreview(name);
      if (opts.resume !== false) await resumeBenchmarkSession();
      return { upstream: name, gatewayKey: gk };
    } catch (e) {
      $("benchTarget").innerHTML = '<span class="err">' + esc(e.message) + "</span>";
      return "";
    }
  }
  function updateBenchReqPreview(upstreamModel) {
    const p = collectBenchParams();
    const body = {
      model: upstreamModel || "\uFF08\u5F53\u524D\u4E0A\u6E38\uFF09",
      messages: [{ role: "user", content: p.prompt }],
      stream: true,
      max_tokens: p.max_tokens
    };
    if (p.temperature != null) body.temperature = p.temperature;
    if (p.top_p != null) body.top_p = p.top_p;
    if (p.top_k != null) body.top_k = p.top_k;
    if (p.min_p != null) body.min_p = p.min_p;
    if (p.frequency_penalty != null) body.frequency_penalty = p.frequency_penalty;
    if (p.presence_penalty != null) body.presence_penalty = p.presence_penalty;
    if (p.repeat_penalty != null) body.repeat_penalty = p.repeat_penalty;
    if (p.seed != null) body.seed = p.seed;
    if (p.enable_thinking != null) body.enable_thinking = p.enable_thinking;
    if (p.stop?.length) body.stop = p.stop;
    $("benchReqPreview").textContent = JSON.stringify(body, null, 2);
    persistBenchParams();
  }
  function benchJobPayload(j) {
    return j.job || j;
  }
  function benchTextsFromJob(j) {
    let content = (j.content_text || "").trim();
    let reasoning = (j.reasoning_text || "").trim();
    if (!content && j.answer) {
      const a = String(j.answer);
      if (a.startsWith("\uFF08\u6A21\u578B\u4ECD\u5728/\u4EC5\u8F93\u51FA reasoning") || a.startsWith("\uFF08\u4EC5\u6709 reasoning")) {
        reasoning = reasoning || a.replace(/^[^\n]*\n?/, "");
      } else content = a;
    }
    if (!content && j.response && !String(j.response).trim().startsWith("data:")) {
      try {
        const o = JSON.parse(j.response);
        content = (o.choices?.[0]?.message?.content || o.choices?.[0]?.text || "").trim();
      } catch {
      }
    }
    return { content, reasoning };
  }
  function renderBenchAnalysis(wrap) {
    const box = $("benchAnalysis");
    if (!box) return;
    const a = wrap.analysis;
    if (!a || !a.phases_ms) {
      box.style.display = "none";
      box.innerHTML = "";
      return;
    }
    const p = a.phases_ms, tp = a.throughput || {};
    box.style.display = "block";
    box.innerHTML = '<h3 style="margin:0 0 10px;font-size:14px">\u8BF7\u6C42\u5206\u6790</h3><dl>' + [
      ["\u603B\u8017\u65F6", p.total_ms != null ? p.total_ms + " ms" : null],
      ["Gateway \u51C6\u5907", p.prep_ms + " ms"],
      ["\u6A21\u578B\u52A0\u8F7D", p.model_load_ms + " ms"],
      ["\u4E0A\u6E38\u63A8\u7406", p.upstream_ms + " ms"],
      ["\u9996 token", p.ttft_ms != null ? p.ttft_ms + " ms" : null],
      ["\u9996\u6761\u6B63\u6587", p.ttft_content_ms != null ? p.ttft_content_ms + " ms" : null],
      ["\u601D\u8003\u9636\u6BB5", p.thinking_ms != null ? p.thinking_ms + " ms" : null],
      ["\u6B63\u6587\u751F\u6210", p.content_generation_ms != null ? p.content_generation_ms + " ms" : null],
      ["\u8F93\u51FA\u901F\u5EA6", tp.output_tokens_per_sec != null ? tp.output_tokens_per_sec.toFixed(2) + " tok/s" : null]
    ].filter((x) => x[1] != null).map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join("") + "</dl>" + (a.summary?.length ? `<ul class="req-hints" style="margin:10px 0 0">${a.summary.map((l) => `<li>${esc(l)}</li>`).join("")}</ul>` : "") + (tp.note ? `<p class="req-note">${esc(tp.note)}</p>` : "");
  }
  function renderBenchStreamEvents(evs) {
    const fold = $("benchEventsFold"), tbl = $("benchEventsTable"), cnt = $("benchEventsCount");
    if (!fold || !tbl) return;
    if (!evs?.length) {
      fold.style.display = "none";
      return;
    }
    fold.style.display = "block";
    if (cnt) cnt.textContent = "(" + evs.length + " \u6761)";
    const show = evs.length > 200 ? evs.slice(-200) : evs;
    const note = evs.length > 200 ? `<p class="req-note">\u4EC5\u5C55\u793A\u6700\u8FD1 ${show.length} \u6761</p>` : "";
    tbl.innerHTML = note + wrapTable("<tr><th>#</th><th>ms</th><th>\u7C7B\u578B</th><th>\u5B57\u6BB5</th><th>+\u5B57\u7B26</th><th>\u9884\u89C8</th></tr>" + show.map((e) => `<tr><td>${e.seq}</td><td>${e.at_ms}</td><td>${esc(e.kind)}</td><td>${esc(e.field || "\u2014")}</td><td>${e.delta_len ?? "\u2014"}</td><td class="small">${esc(e.preview || "\u2014")}</td></tr>`).join(""));
  }
  function renderBenchLive(wrap, running) {
    const j = benchJobPayload(wrap);
    const evs = wrap.stream_events || [];
    const ok = j.status === "completed";
    const failed = j.status === "failed" || j.status === "cancelled";
    const { content, reasoning } = benchTextsFromJob(j);
    const ans = $("benchAnswer");
    if (ans) {
      if (failed) ans.className = "bench-answer err", ans.textContent = j.error || (j.status === "cancelled" ? "\u4EFB\u52A1\u5DF2\u505C\u6B62" : "\u6D4B\u8BD5\u5931\u8D25");
      else if (running && !content && !reasoning) ans.className = "bench-answer muted", ans.textContent = "\u63A8\u7406\u4E2D\u2026";
      else ans.className = "bench-answer", ans.textContent = content || "\uFF08\u5C1A\u65E0\u6B63\u6587 content\uFF09";
    }
    const rw = $("benchReasoningWrap"), rb = $("benchReasoning");
    if (rw && rb) {
      if (reasoning) {
        rw.style.display = "block";
        rb.className = "bench-answer bench-reasoning";
        rb.textContent = reasoning;
      } else rw.style.display = "none", rb.textContent = "";
    }
    renderBenchAnalysis(wrap);
    renderBenchStreamEvents(evs);
  }
  function renderBenchResult(wrap) {
    renderBenchLive(wrap, false);
    const j = benchJobPayload(wrap);
    const evs = wrap.stream_events || [];
    const ok = j.status === "completed";
    const met = $("benchMetrics");
    if (!met) return;
    const tps = wrap.analysis?.throughput?.output_tokens_per_sec;
    met.innerHTML = [
      statusPill(j.status === "completed" ? "success" : j.status === "failed" ? "error" : j.status === "cancelled" ? "cancelled" : j.status),
      j.stream ? `<span class="pill">\u6D41\u5F0F</span>` : "",
      j.max_tokens ? `<span class="pill">max_tokens ${j.max_tokens}</span>` : "",
      j.prompt_len ? `<span class="pill">\u63D0\u793A ${j.prompt_len} \u5B57\u7B26</span>` : "",
      j.latency_ms != null ? `<span class="pill">\u603B\u8017\u65F6 ${j.latency_ms} ms</span>` : "",
      j.ttft_ms != null ? `<span class="pill">TTFT ${j.ttft_ms} ms</span>` : "",
      j.input_tokens ? `<span class="pill">\u8F93\u5165 ${j.input_tokens} tok</span>` : "",
      j.output_tokens ? `<span class="pill">\u8F93\u51FA ${j.output_tokens} tok</span>` : "",
      tps != null ? `<span class="pill">${tps.toFixed(2)} tok/s</span>` : "",
      j.content_chars ? `<span class="pill">\u6B63\u6587 ${j.content_chars} \u5B57</span>` : "",
      j.reasoning_chars ? `<span class="pill">\u601D\u8003 ${j.reasoning_chars} \u5B57</span>` : "",
      j.response_bytes ? `<span class="pill">\u54CD\u5E94 ${(j.response_bytes / 1024).toFixed(1)} KB</span>` : "",
      j.stream_event_count ? `<span class="pill">\u4E8B\u4EF6 ${j.stream_event_count}</span>` : "",
      j.http_status ? `<span class="pill">HTTP ${j.http_status}</span>` : "",
      j.llama_model ? `<span class="pill mono small">${esc(j.llama_model)}</span>` : "",
      j.request_id ? `<button type="button" class="btn sm ghost" onclick="showDetail('${encodeURIComponent(j.request_id).replace(/'/g, "%27")}')">\u5B8C\u6574\u8C03\u7528\u8BB0\u5F55</button>` : ""
    ].filter(Boolean).join("");
    const rawBody = j.response || "";
    $("benchRaw").textContent = rawBody.trim().startsWith("data:") ? rawBody : JSON.stringify({ analysis: wrap.analysis, job: j, stream_events: evs, response: tryParse(rawBody) || rawBody }, null, 2);
    const ef = $("benchEventsFold");
    if (ef && evs.length) ef.open = evs.length <= 80;
  }
  function tryParse(s) {
    try {
      return JSON.parse(s);
    } catch {
      return s;
    }
  }
  function renderBenchRunningMetrics(wrap, t0) {
    const j = benchJobPayload(wrap);
    const metR = $("benchMetrics");
    if (!metR) return;
    metR.innerHTML = [
      statusPill("running"),
      j.ttft_ms != null ? `<span class="pill">TTFT ${j.ttft_ms} ms</span>` : "",
      wrap.stream_events?.length ? `<span class="pill">\u4E8B\u4EF6 ${wrap.stream_events.length}</span>` : "",
      j.content_chars ? `<span class="pill">\u6B63\u6587 ${j.content_chars} \u5B57</span>` : "",
      j.reasoning_chars ? `<span class="pill">\u601D\u8003 ${j.reasoning_chars} \u5B57</span>` : "",
      `<span class="pill">\u5DF2\u7B49\u5F85 ${Math.round((Date.now() - t0) / 1e3)} s</span>`
    ].filter(Boolean).join("");
  }
  async function pollBenchmarkJob(jobId, t0, token) {
    while (true) {
      if (token !== benchPollToken) return null;
      const wrap = await api("/api/benchmark/" + encodeURIComponent(jobId));
      const j = benchJobPayload(wrap);
      if (j.status === "running") {
        renderBenchLive(wrap, true);
        renderBenchRunningMetrics(wrap, t0);
        await new Promise((r) => setTimeout(r, 800));
        continue;
      }
      renderBenchResult(wrap);
      writeBenchStore({ jobId });
      persistBenchParams();
      return wrap;
    }
  }
  async function resumeBenchmarkSession() {
    const st = readBenchStore();
    if (!st.jobId) return;
    const token = ++benchPollToken;
    try {
      const wrap = await api("/api/benchmark/" + encodeURIComponent(st.jobId));
      if (token !== benchPollToken) return;
      const j = benchJobPayload(wrap);
      if (j.status === "running") {
        setBenchRunning(true);
        const t0 = Date.parse(j.started_at) || Date.now();
        const ans0 = $("benchAnswer");
        if (ans0) {
          ans0.className = "bench-answer muted";
          ans0.textContent = "\u6062\u590D\u8F6E\u8BE2\u4E2D\u2026";
        }
        await pollBenchmarkJob(st.jobId, t0, token);
      } else {
        renderBenchResult(wrap);
      }
    } catch {
      writeBenchStore({ jobId: null });
    } finally {
      if (token === benchPollToken) setBenchRunning(false);
    }
  }
  async function stopBenchmark() {
    const st = readBenchStore();
    if (!st.jobId) {
      setBenchRunning(false);
      return;
    }
    benchPollToken++;
    setBenchRunning(false);
    try {
      await api("/api/benchmark/" + encodeURIComponent(st.jobId), { method: "DELETE" });
      const wrap = await api("/api/benchmark/" + encodeURIComponent(st.jobId));
      renderBenchResult(wrap);
    } catch (e) {
      const ansE = $("benchAnswer");
      if (ansE) {
        ansE.className = "bench-answer err";
        ansE.textContent = e.message;
      }
    }
  }
  async function runBenchmark() {
    benchPollToken++;
    const token = benchPollToken;
    setBenchRunning(true);
    const ans0 = $("benchAnswer");
    if (ans0) {
      ans0.className = "bench-answer muted";
      ans0.textContent = "\u6B63\u5728\u8FDE\u63A5\u4E0A\u6E38\u2026";
    }
    $("benchReasoningWrap") && ($("benchReasoningWrap").style.display = "none");
    $("benchAnalysis") && ($("benchAnalysis").style.display = "none", $("benchAnalysis").innerHTML = "");
    $("benchEventsFold") && ($("benchEventsFold").style.display = "none");
    const met0 = $("benchMetrics");
    if (met0) met0.innerHTML = statusPill("running");
    try {
      const benchTarget = await loadBenchPanel({ resume: false });
      if (token !== benchPollToken) return;
      const upstream = typeof benchTarget === "string" ? benchTarget : benchTarget?.upstream || "";
      if (!upstream) {
        throw new Error("\u5F53\u524D API \u63A8\u7406\u540E\u7AEF\u65E0\u5DF2\u52A0\u8F7D\u6A21\u578B\uFF0C\u8BF7\u5148\u5728\u300C\u6A21\u578B\u7BA1\u7406\u300D\u52A0\u8F7D");
      }
      updateBenchReqPreview(upstream);
      const start = await api("/api/benchmark", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(benchPayloadForApi(benchTarget)) });
      if (token !== benchPollToken) return;
      const jobId = start.job_id, t0 = Date.now();
      writeBenchStore({ jobId });
      persistBenchParams();
      await pollBenchmarkJob(jobId, t0, token);
    } catch (e) {
      if (token !== benchPollToken) return;
      const ansE = $("benchAnswer");
      if (ansE) {
        ansE.className = "bench-answer err";
        ansE.textContent = e.message;
      }
      const metE = $("benchMetrics");
      if (metE) metE.innerHTML = "";
    } finally {
      if (token === benchPollToken) setBenchRunning(false);
    }
  }
  var modelHub, backendProbeInFlight, GAUGE, GAUGE_THEMES, GAUGE_ANIM_MS, gaugeShown, gaugeAnimToken, gaugeDomReady, dashboardPoll, BENCH_STORE_KEY, BENCH_SHARED, BENCH_PRESETS, benchPollToken, benchCurrentBackend, benchDrawerTrigger, REQUESTS_POLL_MS, DOWNLOADS_POLL_MS;
  var init_main = __esm({
    "js/app/main.js"() {
      init_dom();
      init_api();
      init_notify();
      init_admin_store();
      init_format();
      init_requests();
      init_expose();
      initAdminKey();
      document.querySelectorAll(".nav button").forEach((b) => b.onclick = () => {
        document.querySelectorAll(".nav button").forEach((x) => x.classList.remove("active"));
        $("sidebarAccountBtn")?.classList.remove("active");
        document.querySelectorAll(".panel").forEach((x) => x.classList.remove("active"));
        b.classList.add("active");
        const tabPanel = $(b.dataset.tab);
        if (tabPanel) tabPanel.classList.add("active");
        if (b.dataset.tab === "benchmark") loadBenchPanel().catch(() => {
        });
        if (b.dataset.tab === "models-local") loadLocalModelsPanel().catch(() => {
        });
        if (b.dataset.tab === "models-download") loadDownloadPanel({ refreshCatalog: true }).catch(() => {
        });
        if (b.dataset.tab === "models-inference" && globalThis.InferenceParams) globalThis.InferenceParams.load().catch(() => {
        });
        if (b.dataset.tab === "dashboard") {
          loadDashboardRecent().catch(() => {
          });
          loadSystem().catch(() => {
          });
          dashboardPoll.start({ skipImmediate: true });
          const gm = $("globalMsg");
          if (gm) {
            gm.innerHTML = "";
            gm.hidden = true;
          }
        } else {
          dashboardPoll.stop();
          const gm = $("globalMsg");
          if (gm) gm.hidden = false;
        }
        if (b.dataset.tab === "keys") loadKeysPanel().catch(() => {
        });
        if (b.dataset.tab === "requests" && !requestsListHasRows()) loadRequests().catch(() => {
        });
        syncTopbarRefresh();
      });
      syncTopbarRefresh();
      $("models-local")?.addEventListener("click", handleInferClick);
      modelHub = {
        rows: [],
        runtime: null,
        catalogById: /* @__PURE__ */ new Map(),
        rowsLoaded: false,
        rowsError: "",
        runtimeError: "",
        runtimeWarning: "",
        _inflight: null,
        async refresh(opts = {}) {
          if (opts.force) this._inflight = null;
          if (this._inflight) return this._inflight;
          this._inflight = Promise.allSettled([api("/api/models"), api("/api/admin/runtime")]).then(([modelsResult, runtimeResult]) => {
            if (modelsResult.status === "fulfilled" && Array.isArray(modelsResult.value?.data)) {
              this.rows = modelsResult.value.data;
              this.rowsLoaded = true;
              this.rowsError = "";
            } else {
              this.rowsError = this.rowsLoaded ? "\u672C\u5730\u6A21\u578B\u6E05\u5355\u5237\u65B0\u5931\u8D25\uFF0C\u5F53\u524D\u5C55\u793A\u4E0A\u6B21\u6210\u529F\u8BFB\u53D6\u7684\u6570\u636E\uFF1B\u8BF7\u68C0\u67E5\u6570\u636E\u5E93\u8FDE\u63A5\u540E\u5237\u65B0\u3002" : "\u672C\u5730\u6A21\u578B\u6E05\u5355\u8BFB\u53D6\u5931\u8D25\uFF0C\u8BF7\u68C0\u67E5\u7F51\u5173\u6570\u636E\u5E93\u8FDE\u63A5\u540E\u5237\u65B0\u3002";
            }
            if (runtimeResult.status === "fulfilled") {
              this.runtime = runtimeResult.value;
              this.runtimeError = "";
              this.runtimeWarning = [this.runtime?.upstream_warning, this.runtime?.catalog_warning].filter(Boolean).join("\uFF1B");
            } else {
              this.runtime = null;
              this.runtimeError = "\u5F53\u524D\u63A8\u7406\u540E\u7AEF\u8FD0\u884C\u72B6\u6001\u6682\u4E0D\u53EF\u7528\uFF1B\u672C\u5730\u6A21\u578B\u6E05\u5355\u4ECD\u53EF\u72EC\u7ACB\u67E5\u770B\u3002";
              this.runtimeWarning = "";
            }
            this._reindexRuntime();
          }).finally(() => {
            this._inflight = null;
          });
          return this._inflight;
        },
        _reindexRuntime() {
          this.catalogById = /* @__PURE__ */ new Map();
          const rt = this.runtime || {};
          for (const x of [...rt.catalog || [], ...rt.loaded || []]) {
            if (x && x.id && !this.catalogById.has(x.id)) this.catalogById.set(x.id, x);
          }
        },
        _stripGguf(s) {
          return String(s || "").replace(/\.gguf$/i, "");
        },
        _idMatch(a, b) {
          a = this._stripGguf(a);
          b = this._stripGguf(b);
          if (!a || !b) return false;
          if (a === b) return true;
          const baseA = a.includes("/") ? a.split("/").pop() : a;
          const baseB = b.includes("/") ? b.split("/").pop() : b;
          return baseA === b || baseB === a || baseA === baseB;
        },
        _samePath(a, b) {
          a = String(a || "").trim().replace(/\\/g, "/").toLowerCase();
          b = String(b || "").trim().replace(/\\/g, "/").toLowerCase();
          return Boolean(a && b && (a === b || a.endsWith("/" + b) || b.endsWith("/" + a)));
        },
        keysForRow(row) {
          const out = /* @__PURE__ */ new Set();
          const add = (v) => {
            v = String(v || "").trim();
            if (!v) return;
            out.add(v);
            out.add(this._stripGguf(v));
            if (v.includes("/")) out.add(v.split("/").pop());
          };
          add(row.model_key);
          add(row.upstream_name);
          add(row.upstream_llamacpp_id);
          add(row.llama_model_name);
          add(row.filename);
          if (row.local_path) add(row.local_path);
          return out;
        },
        _runtimeMatchesRow(item, row) {
          if (!item || !row) return false;
          if (item.path && row.local_path) {
            if (this._samePath(item.path, row.local_path)) return true;
            if (item.path.startsWith("/") && String(row.local_path).startsWith("/")) return false;
          }
          const keys = this.keysForRow(row);
          for (const k of keys) {
            if (String(item.id || "") === k || this._idMatch(item.id, k)) return true;
          }
          return false;
        },
        runtimeItemForRow(row) {
          if (row?._runtime) return row._runtime;
          for (const [id, item] of this.catalogById) {
            if (this._runtimeMatchesRow(item, row)) return item;
          }
          return null;
        },
        tableRows() {
          const rows = (this.rows || []).map((row) => ({ ...row, _managed: true, _runtime: this.runtimeItemForRow(row) }));
          for (const item of this.runtime?.catalog || []) {
            if (rows.some((row) => this._runtimeMatchesRow(item, row))) continue;
            rows.push({
              model_key: "",
              display_name: item.display_name || item.id,
              filename: item.display_name || item.id,
              local_path: item.path || "",
              size_bytes: Number(item.disk_bytes || 0),
              status: item.status || "unknown",
              quantization: item.quantization || "",
              upstream_name: item.id,
              _managed: false,
              _runtime: item
            });
          }
          return rows;
        },
        diskBytes(row) {
          const it = this.runtimeItemForRow(row);
          if (it && Number(it.disk_bytes) > 0) return Number(it.disk_bytes);
          const db = Number(row.disk_bytes ?? row.size_bytes ?? 0);
          return db > 0 ? db : 0;
        },
        loadState(row) {
          const dbStatus = String(row.status || "").toLowerCase();
          const rt = this.runtimeItemForRow(row);
          const downloadStates = ["downloading", "download_failed", "download_cancelled"];
          if (rt) {
            const status = String(rt.status || "unknown").toLowerCase();
            if (status && status !== "unknown") return status;
            if (rt.loaded) return "loaded";
            if (downloadStates.includes(dbStatus)) return dbStatus;
            return "unknown";
          }
          return downloadStates.includes(dbStatus) ? dbStatus : "unknown";
        }
      };
      backendProbeInFlight = false;
      $("btnProbeUpstream")?.addEventListener("click", probeUpstream);
      GAUGE = { cx: 100, cy: 106, rDial: 84, rTrack: 64, rTickOut: 70, rTickIn: 56, rNum: 46, angMin: 180, angMax: 0 };
      GAUGE_THEMES = {
        cpu: { zone: "#22d3ee", needle: "#67e8f9", faceTop: "#1a2a3a", faceBot: "#091018" },
        mem: { zone: "#818cf1", needle: "#c4b5fd", faceTop: "#252040", faceBot: "#0c0a18" },
        disk: { zone: "#34d399", needle: "#6ee7b7", faceTop: "#142820", faceBot: "#081210" }
      };
      GAUGE_ANIM_MS = 920;
      gaugeShown = { cpu: 0, mem: 0, disk: 0 };
      gaugeAnimToken = 0;
      gaugeDomReady = false;
      dashboardPoll = {
        timer: null,
        intervalMs: 6e3,
        inFlight: false,
        auxTick: 0,
        lastCpu: null,
        lastMem: null,
        changeTimes: [],
        THRESH: 1.5,
        SLOW: 6e3,
        MED: 4e3,
        FAST: 2e3,
        WINDOW_MS: 9e4,
        active: false,
        stop() {
          this.active = false;
          if (this.timer) {
            clearTimeout(this.timer);
            this.timer = null;
          }
        },
        start(opts = {}) {
          if (!isAuthenticated()) return;
          this.active = true;
          if (opts.skipImmediate) this.schedule(this.intervalMs);
          else this.schedule(0);
        },
        schedule(delayMs) {
          if (this.timer) clearTimeout(this.timer);
          if (!this.active) return;
          const d = typeof delayMs === "number" ? delayMs : this.intervalMs;
          this.timer = setTimeout(() => {
            this.tick().catch(() => {
            });
          }, d);
        },
        noteSample(cpuPct, memPct) {
          if (this.lastCpu != null) {
            const dCpu = Math.abs(cpuPct - this.lastCpu), dMem = Math.abs(memPct - this.lastMem);
            if (dCpu >= this.THRESH || dMem >= this.THRESH) this.changeTimes.push(Date.now());
          }
          this.lastCpu = cpuPct;
          this.lastMem = memPct;
          const now = Date.now();
          this.changeTimes = this.changeTimes.filter((t) => now - t < this.WINDOW_MS);
          const n = this.changeTimes.length;
          let next = this.SLOW;
          if (n >= 3) next = this.FAST;
          else if (n >= 1) next = this.MED;
          const changed = next !== this.intervalMs;
          this.intervalMs = next;
          if (changed && this.active && !this.inFlight) this.schedule(this.intervalMs);
        },
        async tick() {
          if (!isAuthenticated() || !$("dashboard")?.classList.contains("active")) {
            this.stop();
            return;
          }
          if (this.inFlight) {
            this.schedule();
            return;
          }
          this.inFlight = true;
          try {
            await loadSystem();
            this.auxTick++;
            const every = this.intervalMs <= 4e3 ? 2 : 3;
            if (this.auxTick % every === 0) {
              await loadDashboardRecent().catch(() => {
              });
            }
          } finally {
            this.inFlight = false;
            if (this.active && $("dashboard")?.classList.contains("active")) this.schedule();
          }
        }
      };
      $("filesBox")?.addEventListener("click", (ev) => {
        const btn = ev.target.closest?.("button[data-hf-file]");
        if (!btn) return;
        chooseFile(btn.dataset.hfFile || "", btn.dataset.hfRepo || "");
      });
      BENCH_STORE_KEY = "llm-gateway.bench.v1";
      BENCH_SHARED = {
        prompt: "Write a small, correct Objective-C function and explain why it is thread-safe.",
        stop: []
      };
      BENCH_PRESETS = {
        llamacpp: {
          max_tokens: 4e3,
          temperature: 0.2,
          top_p: 0.9,
          top_k: 40,
          min_p: 0.05,
          frequency_penalty: 0,
          presence_penalty: 0,
          repeat_penalty: 1.1,
          seed: null,
          enable_thinking: null
        },
        ollama: {
          max_tokens: 4e3,
          temperature: 0.2,
          top_p: 0.9,
          top_k: 40,
          min_p: null,
          frequency_penalty: 0,
          presence_penalty: 0,
          repeat_penalty: 1.15,
          seed: null,
          enable_thinking: false
        }
      };
      benchPollToken = 0;
      benchCurrentBackend = "llamacpp";
      benchDrawerTrigger = null;
      document.addEventListener("keydown", (e) => {
        const root = $("benchParamsDrawer");
        if (!root?.classList.contains("open")) return;
        if (e.key === "Escape") {
          e.preventDefault();
          closeBenchParamsDrawer();
          return;
        }
        if (e.key !== "Tab") return;
        const focusable = benchDrawerFocusable(root);
        if (!focusable.length) return;
        const first = focusable[0], last = focusable[focusable.length - 1], active = document.activeElement;
        if (e.shiftKey && (active === first || !root.contains(active))) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && (active === last || !root.contains(active))) {
          e.preventDefault();
          first.focus();
        }
      });
      ["benchPrompt", "benchTokens", "benchTemperature", "benchTopP", "benchTopK", "benchMinP", "benchFreqPen", "benchPresPen", "benchRepeatPen", "benchSeed", "benchThinking", "benchStop"].forEach((id) => {
        $(id)?.addEventListener("input", () => {
          const n = $("benchTarget")?.querySelector("code")?.textContent;
          updateBenchReqPreview(n || "");
        });
        $(id)?.addEventListener("change", () => {
          const n = $("benchTarget")?.querySelector("code")?.textContent;
          updateBenchReqPreview(n || "");
        });
      });
      exposeGlobals({
        $,
        esc,
        getKey,
        api,
        fmtSize,
        fmtDate,
        slug,
        statusLabel,
        statusPill,
        emptyState,
        wrapTable,
        adminKeyStatus,
        appendActionLogTo,
        appendOpsLog,
        applyBenchParams,
        barClass,
        benchBackendKey,
        benchDefaultsForBackend,
        benchIntFromInput,
        benchJobPayload,
        benchNumFromInput,
        benchOptionalInt,
        benchOptionalNum,
        benchPayloadForApi,
        benchTextsFromJob,
        chooseFile,
        cancelOllamaPull,
        retryOllamaPull,
        retryHFDownload,
        clearFailedDownloads,
        dismissDownloadRecord,
        clearMmActionLog,
        clearRequests,
        closeBenchParamsDrawer,
        closeModal,
        collectBenchParams,
        copyReqCode,
        copyReqStreamAll,
        createKey,
        deleteKey,
        deleteModel,
        easeOutCubic,
        flashCopyBtn,
        formatStreamEventBody,
        formatStreamEventsDump,
        gaugeArcD,
        gaugeNeedleColor,
        gaugePolar,
        gaugeSVG,
        gaugeSweepDeg,
        gaugeValueDeg,
        gaugeZoneColor,
        goTab,
        handleInferClick,
        inferButtonHtml,
        inferenceOp,
        installModel,
        listFiles,
        loadBackendForm,
        probeUpstream,
        loadBenchPanel,
        loadDashboardRecent,
        loadDownloads,
        loadKeys,
        loadKeysPanel,
        loadMmBackendPills,
        loadModels,
        loadDownloadPanel,
        loadLocalModelsPanel,
        loadModelsPanel,
        loadOllamaLibrary,
        loadRequests,
        cancelRequest,
        loadRuntime,
        loadSystem,
        markActionLogActive,
        memoryUsagePct,
        memoryUsedBytes,
        modelAction,
        normalizeBenchStore,
        ollamaDelete,
        ollamaModelInMemory,
        ollamaPull,
        openBenchParamsDrawer,
        applyBackendFormFromStore,
        persistBenchParams,
        pollBenchmarkJob,
        prettyBodyText,
        readBenchStore,
        readBenchStoreRaw,
        refreshAfterInference,
        refreshAll,
        refreshModelsOnly,
        renderBenchAnalysis,
        renderBenchLive,
        renderBenchResult,
        renderBenchRunningMetrics,
        renderBenchStreamEvents,
        renderGatewayModelTable,
        renderGaugeNeedle,
        renderInferenceStats,
        applyRequestDetail,
        renderRuntimeCatalog,
        renderStreamEventsSection,
        reqCodeBlock,
        reqKv,
        reqPanelFold,
        resumeBenchmarkSession,
        rotateKey,
        runBenchmark,
        saveBackend,
        saveKey,
        clearAdminKey,
        scrollMmActionLog,
        selectGatewayModel,
        setBenchRunning,
        setGgufMsg,
        showDetail,
        stopBenchmark,
        syncBenchParamsForBackend,
        toggleKey,
        tryParse,
        unloadAllUpstream,
        updateBenchPresetHint,
        updateBenchReqPreview,
        writeBenchStore,
        writeClipboard
      });
      exposeGlobals({ setGlobalMsg, refreshAll, syncTopbarRefresh, dashboardPoll, adminStore });
      initDownloadsTableActions();
      adminKeyStatus();
      if (isAuthenticated()) refreshAll();
      else setGlobalMsg($("dashboard")?.classList.contains("active") ? "" : '<span class="muted">\u8BF7\u5148\u767B\u5F55\u63A7\u5236\u53F0</span>');
      REQUESTS_POLL_MS = 15e3;
      DOWNLOADS_POLL_MS = 5e3;
      setInterval(() => {
        if (!isAuthenticated()) return;
        if ($("requests")?.classList.contains("active")) loadRequests().catch(() => {
        });
      }, REQUESTS_POLL_MS);
      setInterval(() => {
        if (!isAuthenticated()) return;
        if ($("models-download")?.classList.contains("active")) loadDownloads().catch(() => {
        });
      }, DOWNLOADS_POLL_MS);
      if (isAuthenticated() && $("dashboard")?.classList.contains("active")) dashboardPoll.start();
    }
  });

  // js/entry.js
  init_expose();

  // js/features/model-download.js
  init_dom();
  init_api();
  init_format();
  var debounceTimer = 0;
  var pullModalEl = null;
  var localInv = { ollama: /* @__PURE__ */ new Set(), ggufRepoFile: /* @__PURE__ */ new Set() };
  async function refreshLocalInventory() {
    const j = await api("/api/admin/local-inventory");
    localInv.ollama = new Set((j.ollama_names || []).map((n) => String(n).trim().toLowerCase()));
    localInv.ggufRepoFile = /* @__PURE__ */ new Set();
    (j.gguf || []).forEach((g) => {
      if (g.repo && g.filename) localInv.ggufRepoFile.add(g.repo + "\0" + g.filename);
    });
  }
  function isOllamaLocal(fullName) {
    return localInv.ollama.has(String(fullName || "").trim().toLowerCase());
  }
  function isGgufLocal(repo, filename) {
    const fn = String(filename || "").trim();
    const base = fn.includes("/") ? fn.split("/").pop() : fn;
    return localInv.ggufRepoFile.has(String(repo || "").trim() + "\0" + base);
  }
  function ollamaBaseHasLocal(base) {
    base = String(base || "").trim().toLowerCase();
    if (!base) return false;
    for (const id of localInv.ollama) {
      if (id === base || id.startsWith(base + ":")) return true;
    }
    return false;
  }
  function fmtCount(n) {
    if (n == null) return "\u2014";
    if (n >= 1e9) return (n / 1e9).toFixed(1) + "B";
    if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
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
    return "\u2014";
  }
  function sourceLabel(s) {
    return s === "ollama" ? "Ollama" : "Hugging Face";
  }
  function setCatalogMsg(html) {
    const el = $("catalogMsg");
    if (el) el.innerHTML = html;
  }
  function scrollToDownloadTasks() {
    $("downloads")?.closest(".card")?.scrollIntoView({ behavior: "smooth", block: "start" });
  }
  function closePullModal() {
    pullModalEl?.remove();
    pullModalEl = null;
  }
  async function startOllamaPull(fullName) {
    fullName = String(fullName || "").trim();
    if (!fullName) return;
    await refreshLocalInventory().catch(() => {
    });
    if (isOllamaLocal(fullName)) {
      setCatalogMsg('<span class="warn">\u672C\u5730 Ollama \u5DF2\u5B58\u5728 <code>' + esc(fullName) + "</code>\uFF0C\u65E0\u9700\u91CD\u590D\u4E0B\u8F7D</span>");
      return;
    }
    setCatalogMsg('<span class="muted">\u6B63\u5728\u521B\u5EFA\u4E0B\u8F7D\u4EFB\u52A1 <code>' + esc(fullName) + "</code>\u2026</span>");
    try {
      await api("/api/admin/ollama/pull", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: fullName })
      });
      closePullModal();
      setCatalogMsg('<span class="ok">\u5DF2\u52A0\u5165\u4E0B\u8F7D\u4EFB\u52A1\uFF1A<code>' + esc(fullName) + "</code>\uFF0C\u8BF7\u5728\u4E0B\u65B9\u7684\u300C\u4E0B\u8F7D\u4EFB\u52A1\u300D\u67E5\u770B\u8FDB\u5EA6</span>");
      scrollToDownloadTasks();
      if (typeof globalThis.loadDownloads === "function") {
        await globalThis.loadDownloads().catch(() => {
        });
      }
    } catch (e) {
      setCatalogMsg('<span class="err">' + esc(e.message) + "</span>");
    }
  }
  async function openOllamaPullDialog(model, hintTags) {
    model = String(model || "").trim();
    if (!model) return;
    closePullModal();
    const overlay = document.createElement("div");
    overlay.className = "ollama-pull-overlay";
    overlay.innerHTML = '<div class="ollama-pull-dialog card"><div class="card-head"><div><h2>\u9009\u62E9 Ollama \u89C4\u683C</h2><p class="small muted">\u6A21\u578B <code>' + esc(model) + '</code> \xB7 \u4E0D\u540C tag \u5BF9\u5E94\u4E0D\u540C\u4F53\u79EF\u4E0E\u80FD\u529B</p></div><button type="button" class="btn sm ghost" data-close>\u5173\u95ED</button></div><div class="ollama-pull-body muted">\u6B63\u5728\u8BFB\u53D6\u53EF\u7528 tag\u2026</div></div>';
    document.body.appendChild(overlay);
    pullModalEl = overlay;
    overlay.querySelector("[data-close]").onclick = () => closePullModal();
    overlay.addEventListener("click", (ev) => {
      if (ev.target === overlay) closePullModal();
    });
    const body = overlay.querySelector(".ollama-pull-body");
    let tags = [];
    try {
      const j = await api("/api/admin/catalog/ollama/tags?model=" + encodeURIComponent(model));
      tags = j.tags || [];
    } catch (e) {
      body.innerHTML = '<span class="err">' + esc(e.message) + "</span>";
      return;
    }
    if (!tags.length) {
      tags = [{ tag: "latest", full_name: model + ":latest" }];
    }
    const hintSet = new Set((hintTags || []).map((t) => String(t).toLowerCase()));
    let html = '<p class="small" style="margin:0 0 10px">\u9009\u62E9\u8981 pull \u7684 tag\uFF08\u4F53\u79EF\u6765\u81EA Ollama \u5E93\u9875\uFF09\uFF1A</p><div class="ollama-tag-list">';
    tags.forEach((t, i) => {
      const installed = isOllamaLocal(t.full_name);
      const checked = !installed && i === 0 ? " checked" : "";
      const hint = hintSet.has(String(t.tag).toLowerCase()) ? " \xB7 \u5E93\u5185\u6807\u7B7E" : "";
      const metaParts = [];
      if (t.size) metaParts.push(t.size);
      if (t.context) metaParts.push(t.context + " context");
      if (t.input) metaParts.push(t.input);
      const meta = metaParts.length ? metaParts.join(" \xB7 ") : "\u4F53\u79EF\u672A\u77E5";
      html += '<label class="ollama-tag-option' + (installed ? " disabled" : "") + '"><input type="radio" name="ollamaTagPick" value="' + esc(t.full_name) + '"' + checked + (installed ? " disabled" : "") + "> <code>" + esc(t.full_name) + '</code><span class="small muted">' + esc(meta) + esc(hint) + "</span>" + (installed ? '<span class="small ok">\u5DF2\u672C\u5730</span>' : "") + "</label>";
    });
    html += '</div><div class="field" style="margin-top:12px"><label>\u6216\u81EA\u5B9A\u4E49</label><input id="ollamaTagCustom" placeholder="' + esc(model + ":latest") + '"></div><div class="btn-row" style="margin-top:14px"><button type="button" class="btn primary" data-start>\u5F00\u59CB\u4E0B\u8F7D</button></div>';
    body.classList.remove("muted");
    body.innerHTML = html;
    body.querySelector("[data-start]").onclick = () => {
      const custom = ($("ollamaTagCustom")?.value || "").trim();
      const picked = body.querySelector('input[name="ollamaTagPick"]:checked:not(:disabled)')?.value || "";
      startOllamaPull(custom || picked);
    };
  }
  async function copyText(text) {
    text = String(text || "").trim();
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      ta.remove();
    }
    setCatalogMsg('<span class="ok">\u5DF2\u590D\u5236\uFF1A<code>' + esc(text) + "</code></span>");
  }
  function renderRows(rows) {
    const el = $("catalogResults");
    if (!el) return;
    if (!rows.length) {
      el.innerHTML = emptyState("\u65E0\u7ED3\u679C\uFF0C\u8BF7\u6362\u5173\u952E\u8BCD\u6216\u6E20\u9053");
      return;
    }
    const showDl = rows.some((r) => r.source === "huggingface" || r.source === "ollama");
    const showOllamaSpec = rows.some((r) => r.source === "ollama");
    el.innerHTML = wrapTable(
      "<tr><th>\u540D\u79F0</th><th>\u6E20\u9053</th>" + (showDl ? "<th>\u4E0B\u8F7D\u91CF</th>" : "") + (showOllamaSpec ? "<th>\u89C4\u683C</th>" : "") + "<th>\u53D1\u5E03\u65F6\u95F4</th><th>\u6458\u8981</th><th></th></tr>" + rows.map((r) => {
        const dl = r.downloads != null ? fmtCount(r.downloads) : r.source === "ollama" ? '<span class="muted">\u2014</span>' : "\u2014";
        const spec = r.source === "ollama" ? (r.param_tags || []).length ? esc((r.param_tags || []).join(" \xB7 ")) + '<div class="small muted">\u70B9 pull \u67E5\u770B\u5404 tag \u4F53\u79EF</div>' : '<span class="muted">\u70B9 pull \u67E5\u770B\u4F53\u79EF</span>' : "";
        const actions = [];
        actions.push(
          '<button type="button" class="btn sm" data-copy="' + esc(r.copy_name || r.id) + '">\u590D\u5236\u540D</button>'
        );
        if (r.source === "huggingface") {
          actions.push(
            '<button type="button" class="btn sm primary" data-hf-repo="' + esc(r.id) + '">\u9009\u6587\u4EF6</button>'
          );
        } else if (r.source === "ollama") {
          const localMark = ollamaBaseHasLocal(r.id) ? ' <span class="pill pill-success">\u5DF2\u672C\u5730</span>' : "";
          actions.push(
            '<button type="button" class="btn sm primary" data-ollama-pick="' + esc(r.id) + '">pull</button>' + localMark
          );
        }
        return `<tr>
          <td><code>${esc(r.id)}</code><div class="catalog-meta">${esc(r.display_name || "")}</div></td>
          <td>${esc(sourceLabel(r.source))}</td>
          ${showDl ? `<td>${dl}</td>` : ""}
          ${showOllamaSpec ? `<td class="small">${spec}</td>` : ""}
          <td class="small">${esc(fmtPublished(r))}</td>
          <td class="small muted">${esc((r.description || "").slice(0, 120))}</td>
          <td><div class="td-actions">${actions.join("")}</div></td></tr>`;
      }).join("")
    );
  }
  function onCatalogResultsClick(ev) {
    const copyBtn = ev.target.closest("[data-copy]");
    if (copyBtn) {
      ev.preventDefault();
      copyText(copyBtn.getAttribute("data-copy"));
      return;
    }
    const hfBtn = ev.target.closest("[data-hf-repo]");
    if (hfBtn) {
      ev.preventDefault();
      const repo = hfBtn.getAttribute("data-hf-repo");
      if ($("repo")) $("repo").value = repo;
      if (typeof globalThis.listFiles === "function") globalThis.listFiles();
      setCatalogMsg("\u5DF2\u586B\u5165\u4ED3\u5E93 " + esc(repo) + "\uFF0C\u8BF7\u5728\u4E0B\u65B9\u8BFB\u53D6 GGUF");
      return;
    }
    const pickBtn = ev.target.closest("[data-ollama-pick]");
    if (pickBtn) {
      ev.preventDefault();
      const model = pickBtn.getAttribute("data-ollama-pick");
      setCatalogMsg('<span class="muted">\u6B63\u5728\u6253\u5F00 <code>' + esc(model) + "</code> \u7684 tag \u5217\u8868\u2026</span>");
      openOllamaPullDialog(model, []).catch((e) => {
        setCatalogMsg('<span class="err">' + esc(e?.message || String(e)) + "</span>");
      });
    }
  }
  async function search() {
    const query = ($("catalogQuery")?.value || "").trim();
    const source = $("catalogSource")?.value || "all";
    const sort = $("catalogSort")?.value || "hot";
    const hfGguf = $("catalogHfGguf")?.value !== "0";
    const msg = $("catalogMsg");
    if (msg) msg.textContent = "\u641C\u7D22\u4E2D\u2026";
    await refreshLocalInventory().catch(() => {
    });
    try {
      const params = new URLSearchParams({ q: query, source, sort, limit: "40", hf_gguf: hfGguf ? "1" : "0" });
      const j = await api("/api/admin/catalog/search?" + params);
      renderRows(j.data || []);
      if (msg)
        msg.textContent = query ? "\u5171 " + (j.data || []).length + " \u6761" : "\u5C55\u793A\u70ED\u95E8/\u6700\u8FD1\u6A21\u578B\uFF08\u53EF\u8F93\u5165\u5173\u952E\u8BCD\u7F29\u5C0F\u8303\u56F4\uFF09";
    } catch (e) {
      if (msg) msg.innerHTML = '<span class="err">' + esc(e.message) + "</span>";
      if ($("catalogResults")) $("catalogResults").innerHTML = "";
    }
  }
  function scheduleSearch() {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => search().catch(() => {
    }), 350);
  }
  function init() {
    const input = $("catalogQuery");
    if (input) {
      input.addEventListener("input", scheduleSearch);
      input.addEventListener("keydown", (ev) => {
        if (ev.key === "Enter") {
          ev.preventDefault();
          search().catch(() => {
          });
        }
      });
    }
    ["catalogSource", "catalogSort", "catalogHfGguf"].forEach((id) => {
      $(id)?.addEventListener("change", () => search().catch(() => {
      }));
    });
    $("catalogResults")?.addEventListener("click", onCatalogResultsClick);
  }
  var ModelDownload = {
    search,
    copyText,
    init,
    startOllamaPull,
    refreshLocalInventory,
    isOllamaLocal,
    isGgufLocal
  };

  // js/features/inference-params.js
  init_dom();
  init_api();
  var schemaFields = [];
  var sectionTitles = {};
  var sectionDescriptions = {};
  var currentBackend = "llamacpp";
  var currentParams = {};
  var currentPolicy = { mode: "client_first" };
  var factoryDefaults = { llamacpp: {}, ollama: {} };
  function backendLabel() {
    return currentBackend === "ollama" ? "Ollama" : "llama.cpp";
  }
  function exampleForBackend(f) {
    const m = f.examples || {};
    return (m[currentBackend] ?? f.example ?? "").trim();
  }
  function fieldValue(key) {
    const el = document.querySelector('[data-inf-key="' + key + '"]');
    if (!el) return void 0;
    if (el.tagName === "SELECT") return el.value;
    if (el.tagName === "TEXTAREA") {
      const lines = el.value.split("\n").map((s) => s.trim()).filter(Boolean);
      return lines.length ? lines : void 0;
    }
    const t = el.value.trim();
    if (t === "") return void 0;
    if (el.type === "number") {
      const n = Number(t);
      return Number.isFinite(n) ? n : void 0;
    }
    return t;
  }
  function setFieldValue(key, val) {
    const el = document.querySelector('[data-inf-key="' + key + '"]');
    if (!el) return;
    if (val == null) {
      el.value = "";
      return;
    }
    if (el.tagName === "TEXTAREA" && Array.isArray(val)) {
      el.value = val.join("\n");
      return;
    }
    el.value = val;
  }
  function collectParams() {
    const p = {};
    schemaFields.forEach((f) => {
      if (f.section === "policy") return;
      const v = fieldValue(f.key);
      if (v !== void 0) p[f.key] = v;
    });
    return p;
  }
  function factoryValueForKey(key) {
    const bag = factoryDefaults[currentBackend] || {};
    const v = bag[key];
    if (v == null || v === "") return null;
    if (Array.isArray(v)) return v.length ? v.join("\n") : null;
    return String(v);
  }
  function fieldInputHints(f) {
    const factory = factoryValueForKey(f.key);
    const gatewayKeys = /* @__PURE__ */ new Set(["max_output_cap"]);
    if (factory != null) {
      const note2 = gatewayKeys.has(f.key) ? "\u6765\u81EA config.yaml\uFF0C\u4E0E\u300C" + backendLabel() + " \xB7 \u8FD8\u539F\u51FA\u5382\u9ED8\u8BA4\u300D\u4E00\u81F4" : backendLabel() + " \u51FA\u5382\u9ED8\u8BA4\uFF08\u70B9\u300C\u8FD8\u539F\u51FA\u5382\u9ED8\u8BA4\u300D\u5199\u5165\u5F53\u524D\u540E\u7AEF\uFF1B\u672A\u4FDD\u5B58\u524D\u7559\u7A7A=\u4E0D\u6CE8\u5165\uFF09";
      return { placeholder: factory, note: note2, noteClass: gatewayKeys.has(f.key) ? "from-config" : "from-factory" };
    }
    const ex = exampleForBackend(f);
    let note = "\u5F53\u524D\u540E\u7AEF\u65E0\u51FA\u5382\u9ED8\u8BA4\u9879\uFF1A\u7559\u7A7A\u8868\u793A\u7F51\u5173\u4E0D\u5728 JSON \u5199\u5165\uFF0C\u7531\u5BA2\u6237\u7AEF\u6216\u4E0A\u6E38\u51B3\u5B9A";
    if (ex) note += " \xB7 " + backendLabel() + " \u5E38\u89C1\u53C2\u8003 " + ex;
    return { placeholder: "\u7559\u7A7A = \u4E0D\u6CE8\u5165", note, noteClass: "no-factory" };
  }
  function metaBlock(f) {
    const parts = [];
    if (f.description) parts.push('<p class="param-desc">' + esc(f.description) + "</p>");
    if (f.impact) parts.push('<p class="param-meta"><span class="k">\u5F71\u54CD</span>' + esc(f.impact) + "</p>");
    if (f.range) parts.push('<p class="param-meta"><span class="k">\u8303\u56F4</span>' + esc(f.range) + "</p>");
    if (f.recommend) parts.push('<p class="param-meta param-rec"><span class="k">\u63A8\u8350</span>' + esc(f.recommend) + "</p>");
    return parts.join("");
  }
  function renderFieldControl(f) {
    const hints = fieldInputHints(f);
    const ph = esc(hints.placeholder);
    const title = esc(f.description || f.hint || "");
    const note = '<p class="param-input-note ' + esc(hints.noteClass) + '">' + esc(hints.note) + "</p>";
    if (f.type === "select") {
      let h = '<select data-inf-key="' + esc(f.key) + '" title="' + title + '">';
      (f.select || []).forEach((opt) => {
        h += '<option value="' + esc(opt.value) + '">' + esc(opt.label) + "</option>";
      });
      h += "</select>" + note;
      return h;
    }
    if (f.type === "stop_lines") {
      return '<textarea data-inf-key="' + esc(f.key) + '" rows="3" placeholder="' + ph + '" title="' + title + '"></textarea>' + note;
    }
    return '<input data-inf-key="' + esc(f.key) + '" type="' + (f.type === "number" ? "number" : "text") + '" placeholder="' + ph + '" title="' + title + '">' + note;
  }
  function bindToolbar() {
    const sel = $("infBackendSelect");
    if (sel) {
      sel.value = currentBackend;
      sel.onchange = () => {
        currentBackend = sel.value;
        load();
      };
    }
    $("infSaveBtn") && ($("infSaveBtn").onclick = () => save().catch((e) => alert(e.message)));
    $("infResetBtn") && ($("infResetBtn").onclick = () => resetParams().catch((e) => alert(e.message)));
  }
  function renderPolicyRoot(root) {
    if (!root) return;
    const policyField = schemaFields.find((f) => f.key === "mode" && f.section === "policy");
    let html = '<div class="inf-toolbar row"><div class="field sm"><label>\u540E\u7AEF</label><select id="infBackendSelect"><option value="llamacpp">llama.cpp</option><option value="ollama">Ollama</option></select></div><div class="btn-row" style="margin:0;align-self:flex-end"><button type="button" class="btn sm primary" id="infSaveBtn">\u4FDD\u5B58</button><button type="button" class="btn sm ghost" id="infResetBtn">\u8FD8\u539F\u51FA\u5382\u9ED8\u8BA4</button></div></div>';
    if (sectionDescriptions.policy) {
      html += '<p class="param-section-desc inf-policy-lead">' + esc(sectionDescriptions.policy) + "</p>";
    }
    if (policyField) html += metaBlock(policyField);
    html += '<div class="policy-row inf-policy-choices">';
    html += '<label class="inf-policy-option"><input type="radio" name="infPolicy" value="client_first" ' + (currentPolicy.mode !== "gateway_only" ? "checked" : "") + '> <span><b>\u5BA2\u6237\u7AEF\u4F18\u5148</b><span class="small muted">client_first \xB7 \u5BA2\u6237\u7AEF\u5B57\u6BB5\u8986\u76D6\u7F51\u5173\u9ED8\u8BA4</span></span></label>';
    html += '<label class="inf-policy-option"><input type="radio" name="infPolicy" value="gateway_only" ' + (currentPolicy.mode === "gateway_only" ? "checked" : "") + '> <span><b>\u4EC5\u7F51\u5173\u914D\u7F6E</b><span class="small muted">gateway_only \xB7 \u5FFD\u7565\u5BA2\u6237\u7AEF\u91C7\u6837\u5B57\u6BB5</span></span></label>';
    html += "</div>";
    root.innerHTML = html;
    root.classList.remove("muted");
    bindToolbar();
  }
  function renderParamsRoot(root) {
    if (!root) return;
    const sections = {};
    schemaFields.forEach((f) => {
      if (f.section === "policy") return;
      if (!f.backends.includes("both") && !f.backends.includes(currentBackend)) return;
      (sections[f.section] = sections[f.section] || []).push(f);
    });
    let html = '<p class="small muted inf-hint">\u53C2\u6570\u4E0E\u5408\u5E76\u7B56\u7565\u6309 <strong>' + esc(backendLabel()) + "</strong> \u5206\u522B\u4FDD\u5B58\u3002\u8F6C\u53D1 /v1 \u65F6\u4F7F\u7528\u300C\u51FA\u5382\u9ED8\u8BA4 + \u5DF2\u4FDD\u5B58\u8986\u76D6\u300D\uFF1A<strong>\u6709\u503C\u5C31\u4F1A\u5199\u5165\u8BF7\u6C42</strong>\uFF08\u4E0E\u8F93\u5165\u6846\u662F\u5426\u7559\u7A7A\u65E0\u5173\uFF09\u3002\u4E0B\u65B9\u8F93\u5165\u6846\u53EA\u5F71\u54CD\u4F60\u663E\u5F0F\u4FDD\u5B58\u7684\u8986\u76D6\u9879\u3002</p>";
    Object.keys(sections).forEach((sec) => {
      html += '<div class="param-section"><h3>' + esc(sectionTitles[sec] || sec) + "</h3>";
      if (sectionDescriptions[sec]) {
        html += '<p class="param-section-desc">' + esc(sectionDescriptions[sec]) + "</p>";
      }
      html += '<div class="param-field-list">';
      sections[sec].forEach((f) => {
        html += '<div class="param-field-card"><div class="param-field-head"><label title="' + esc(f.description || "") + '">' + esc(f.label) + '</label><code class="param-key">' + esc(f.key) + "</code></div>" + metaBlock(f) + '<div class="param-control">' + renderFieldControl(f) + "</div></div>";
      });
      html += "</div></div>";
    });
    root.innerHTML = html;
    root.classList.remove("muted");
    Object.entries(currentParams).forEach(([k, v]) => setFieldValue(k, v));
  }
  function render() {
    renderPolicyRoot($("inferencePolicyRoot"));
    renderParamsRoot($("inferenceParamsRoot"));
  }
  async function load() {
    const policyRoot = $("inferencePolicyRoot");
    const paramsRoot = $("inferenceParamsRoot");
    if (!policyRoot && !paramsRoot) return;
    try {
      if (!schemaFields.length) {
        const sch = await api("/api/admin/inference-params/schema");
        schemaFields = sch.fields || [];
        sectionTitles = sch.sections || {};
        sectionDescriptions = sch.section_descriptions || {};
      }
      const j = await api("/api/admin/inference-params?backend=" + encodeURIComponent(currentBackend));
      currentParams = j.params || {};
      currentPolicy = j.policy || { mode: "client_first" };
      if (j.defaults) {
        factoryDefaults = {
          llamacpp: j.defaults.llamacpp || {},
          ollama: j.defaults.ollama || {}
        };
      }
      render();
    } catch (e) {
      const err = '<span class="err">' + esc(e.message) + "</span>";
      if (policyRoot) policyRoot.innerHTML = err;
      if (paramsRoot) paramsRoot.innerHTML = err;
    }
  }
  async function save() {
    const mode = document.querySelector('input[name="infPolicy"]:checked')?.value || "client_first";
    await api("/api/admin/inference-params", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ backend: currentBackend, params: collectParams(), policy: { mode } })
    });
    alert("\u5DF2\u4FDD\u5B58");
    await load();
  }
  async function resetParams() {
    if (!confirm("\u5C06\u8BE5\u540E\u7AEF\u7684\u53C2\u6570\u8FD8\u539F\u4E3A config.yaml \u51FA\u5382\u9ED8\u8BA4\uFF1F")) return;
    const j = await api("/api/admin/inference-params/reset", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ backend: currentBackend })
    });
    currentParams = j.params || {};
    await load();
  }
  var InferenceParams = { load };

  // js/features/auth.js
  init_dom();
  init_api();
  var currentUser = null;
  var passwordProtectionEnabled = true;
  var passwordProtectionAtLoad = true;
  var passwordEyeIcon = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12Z"></path><circle cx="12" cy="12" r="3"></circle></svg>';
  var passwordEyeOffIcon = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="m3 3 18 18"></path><path d="M10.6 10.6a2 2 0 0 0 2.8 2.8"></path><path d="M9.9 5.2A10.9 10.9 0 0 1 12 5c6.4 0 10 7 10 7a17.5 17.5 0 0 1-3.1 3.8"></path><path d="M6.6 6.6C3.7 8.5 2 12 2 12s3.6 7 10 7c1 0 1.9-.2 2.8-.5"></path></svg>';
  function resetPasswordField(id) {
    const input = $(id);
    if (!input) return;
    input.value = "";
    input.type = "password";
    const button = input.closest(".password-input-wrap")?.querySelector(".password-toggle");
    if (!button) return;
    button.innerHTML = passwordEyeIcon;
    button.title = "\u663E\u793A\u660E\u6587";
    button.setAttribute("aria-label", "\u663E\u793A\u660E\u6587");
    button.setAttribute("aria-pressed", "false");
  }
  function setAuthScreenVisible(show) {
    const screen = $("authScreen");
    const app = $("appShell");
    if (screen) {
      screen.classList.toggle("show", show);
      screen.setAttribute("aria-hidden", show ? "false" : "true");
    }
    if (app) app.hidden = show;
    document.body.classList.toggle("auth-locked", show);
  }
  function forceLoginScreen() {
    currentUser = null;
    $("sidebarAccountBtn")?.classList.remove("active");
    setAuthScreenVisible(true);
  }
  function updateAuthChrome() {
    setAuthScreenVisible(!getSessionToken());
  }
  function openAccountSettings() {
    if (!getSessionToken()) {
      forceLoginScreen();
      return;
    }
    document.querySelectorAll(".nav button").forEach((x) => x.classList.remove("active"));
    document.querySelectorAll(".panel").forEach((x) => x.classList.remove("active"));
    $("account")?.classList.add("active");
    $("sidebarAccountBtn")?.classList.add("active");
    if (globalThis.dashboardPoll) globalThis.dashboardPoll.stop();
    const message = $("globalMsg");
    if (message) message.hidden = false;
    if (globalThis.syncTopbarRefresh) globalThis.syncTopbarRefresh();
    loadAccountSettings().catch(() => {
    });
  }
  function bindAccountPageActions() {
    $("btnAccountLogout")?.addEventListener("click", (event) => {
      event.preventDefault();
      logout();
    });
    $("accountForm")?.addEventListener("submit", (event) => {
      void submitAccountCredentials(event);
    });
    $("accountPasswordEnabled")?.addEventListener("change", updateCredentialFormPolicy);
    $("acctNewPass")?.addEventListener("input", updateCredentialFormPolicy);
  }
  function initPasswordToggles() {
    document.querySelectorAll('input[type="password"]').forEach((input) => {
      let wrapper = input.closest(".password-input-wrap");
      if (!wrapper) {
        wrapper = document.createElement("span");
        wrapper.className = "password-input-wrap";
        input.parentNode.insertBefore(wrapper, input);
        wrapper.appendChild(input);
      }
      let button = wrapper.querySelector(".password-toggle");
      if (!button) {
        button = document.createElement("button");
        button.type = "button";
        button.className = "password-toggle";
        wrapper.appendChild(button);
      }
      button.innerHTML = passwordEyeIcon;
      button.title = "\u663E\u793A\u660E\u6587";
      button.setAttribute("aria-label", "\u663E\u793A\u660E\u6587");
      button.setAttribute("aria-pressed", "false");
      if (button.dataset.passwordToggleBound === "true") return;
      button.dataset.passwordToggleBound = "true";
      button.addEventListener("click", () => {
        const show = input.type === "password";
        input.type = show ? "text" : "password";
        button.innerHTML = show ? passwordEyeOffIcon : passwordEyeIcon;
        button.title = show ? "\u9690\u85CF\u660E\u6587" : "\u663E\u793A\u660E\u6587";
        button.setAttribute("aria-label", button.title);
        button.setAttribute("aria-pressed", String(show));
      });
    });
  }
  function updateLoginModeUI() {
    const field = $("authPasswordField"), password = $("authLoginPass"), button = $("authLoginBtn");
    if (field) field.hidden = !passwordProtectionEnabled;
    if (password) password.required = passwordProtectionEnabled;
    if (button) button.textContent = passwordProtectionEnabled ? "\u767B\u5F55" : "\u76F4\u63A5\u8FDB\u5165\u63A7\u5236\u53F0";
  }
  async function createWebSession(password = "") {
    const response = await apiPublic("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password })
    });
    setSessionToken(response.token);
    currentUser = response.user || { username: "admin" };
    resetPasswordField("authLoginPass");
    $("sidebarAccountBtn")?.classList.remove("active");
    updateAuthChrome();
    if (globalThis.refreshAll) globalThis.refreshAll();
    return response;
  }
  async function initAuth() {
    initPasswordToggles();
    $("authLoginForm")?.addEventListener("submit", (event) => {
      void submitLogin(event);
    });
    bindAccountPageActions();
    try {
      const status = await apiPublic("/api/auth/status");
      passwordProtectionEnabled = status?.password_enabled !== false;
    } catch {
      passwordProtectionEnabled = true;
    }
    updateLoginModeUI();
    if (getSessionToken()) {
      try {
        const response = await api("/api/auth/me");
        currentUser = response.user || null;
      } catch {
        setSessionToken("");
        currentUser = null;
      }
    }
    if (!getSessionToken() && !passwordProtectionEnabled) {
      const message = $("authMsg");
      if (message) message.textContent = "\u5BC6\u7801\u4FDD\u62A4\u5DF2\u5173\u95ED\uFF0C\u6B63\u5728\u8FDB\u5165\u63A7\u5236\u53F0\u2026";
      try {
        await createWebSession("");
      } catch (error) {
        if (message) message.textContent = "\u6682\u65F6\u65E0\u6CD5\u8FDB\u5165\u63A7\u5236\u53F0\uFF1A" + error.message;
      }
    }
    updateAuthChrome();
  }
  async function submitLogin(event) {
    event?.preventDefault?.();
    const message = $("authMsg"), password = $("authLoginPass")?.value || "";
    if (passwordProtectionEnabled && !password) {
      if (message) message.innerHTML = '<span class="err">\u8BF7\u8F93\u5165\u5BC6\u7801</span>';
      return;
    }
    if (message) message.textContent = "";
    const button = $("authLoginBtn");
    if (button) button.disabled = true;
    try {
      await createWebSession(password);
    } catch (error) {
      if (message) message.innerHTML = '<span class="err">' + esc(error.message) + "</span>";
    } finally {
      if (button) button.disabled = false;
    }
  }
  async function logout() {
    try {
      if (getSessionToken()) await api("/api/auth/logout", { method: "POST" });
    } catch {
    }
    const { adminStore: adminStore2 } = await Promise.resolve().then(() => (init_admin_store(), admin_store_exports));
    adminStore2.reset();
    setSessionToken("");
    currentUser = null;
    $("sidebarAccountBtn")?.classList.remove("active");
    document.querySelectorAll(".panel").forEach((x) => x.classList.remove("active"));
    $("dashboard")?.classList.add("active");
    document.querySelector('.nav button[data-tab="dashboard"]')?.classList.add("active");
    forceLoginScreen();
  }
  function updateCredentialFormPolicy() {
    const enabledNow = $("accountPasswordEnabled")?.checked ?? passwordProtectionAtLoad;
    const currentField = $("acctCurrentPassField"), currentPassword = $("acctCurrentPass");
    const newPassword = $("acctNewPass"), confirmPassword = $("acctNewPass2");
    if (currentField) currentField.hidden = !passwordProtectionAtLoad;
    if (currentPassword) currentPassword.required = passwordProtectionAtLoad;
    if (newPassword) newPassword.required = enabledNow && !passwordProtectionAtLoad;
    if (confirmPassword) confirmPassword.required = Boolean(newPassword?.value) || enabledNow && !passwordProtectionAtLoad;
  }
  async function loadAccountSettings() {
    const status = $("accountCurrent"), message = $("accountMsg"), logoutButton = $("btnAccountLogout");
    if (message) message.textContent = "";
    if (!getSessionToken()) {
      if (status) status.textContent = "\u8BF7\u5148\u8FDB\u5165\u63A7\u5236\u53F0\uFF0C\u518D\u4FEE\u6539\u5BC6\u7801\u4FDD\u62A4\u8BBE\u7F6E\u3002";
      if (logoutButton) logoutButton.hidden = true;
      return;
    }
    try {
      const response = await api("/api/auth/credentials");
      passwordProtectionAtLoad = response.password_enabled !== false;
      passwordProtectionEnabled = passwordProtectionAtLoad;
      updateLoginModeUI();
      const toggle = $("accountPasswordEnabled");
      if (toggle) toggle.checked = passwordProtectionAtLoad;
      if (status) {
        const source = response.stored_in_db ? "\u5DF2\u4FDD\u5B58" : "\u4F7F\u7528\u521D\u59CB\u90E8\u7F72\u8BBE\u7F6E";
        status.textContent = (passwordProtectionAtLoad ? "\u5BC6\u7801\u4FDD\u62A4\u5DF2\u5F00\u542F\u3002" : "\u5BC6\u7801\u4FDD\u62A4\u5DF2\u5173\u95ED\u3002") + source + "\u3002";
      }
      if (logoutButton) logoutButton.hidden = !passwordProtectionAtLoad;
      updateCredentialFormPolicy();
    } catch (error) {
      if (status) status.textContent = error.message;
    }
  }
  async function submitAccountCredentials(event) {
    event?.preventDefault?.();
    const message = $("accountMsg"), targetEnabled = $("accountPasswordEnabled")?.checked === true;
    const currentPassword = $("acctCurrentPass")?.value || "";
    const newPassword = $("acctNewPass")?.value || "", confirmPassword = $("acctNewPass2")?.value || "";
    if (newPassword !== confirmPassword) {
      if (message) message.innerHTML = '<span class="err">\u4E24\u6B21\u65B0\u5BC6\u7801\u4E0D\u4E00\u81F4</span>';
      return;
    }
    if (passwordProtectionAtLoad && !currentPassword) {
      if (message) message.innerHTML = '<span class="err">\u8BF7\u8F93\u5165\u5F53\u524D\u5BC6\u7801</span>';
      return;
    }
    if (!passwordProtectionAtLoad && targetEnabled && !newPassword) {
      if (message) message.innerHTML = '<span class="err">\u5F00\u542F\u5BC6\u7801\u4FDD\u62A4\u65F6\uFF0C\u8BF7\u5148\u8BBE\u7F6E\u65B0\u5BC6\u7801</span>';
      return;
    }
    if (!targetEnabled && newPassword) {
      if (message) message.innerHTML = '<span class="err">\u5173\u95ED\u5BC6\u7801\u4FDD\u62A4\u65F6\uFF0C\u8BF7\u6E05\u7A7A\u65B0\u5BC6\u7801\u8F93\u5165\u6846</span>';
      return;
    }
    if (targetEnabled === passwordProtectionAtLoad && !newPassword) {
      if (message) message.innerHTML = '<span class="err">\u6CA1\u6709\u9700\u8981\u4FDD\u5B58\u7684\u66F4\u6539</span>';
      return;
    }
    const button = $("accountSaveButton");
    if (button) button.disabled = true;
    try {
      const body = { current_password: currentPassword, password_enabled: targetEnabled };
      if (newPassword) body.new_password = newPassword;
      await api("/api/auth/credentials", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
      });
      resetPasswordField("acctCurrentPass");
      resetPasswordField("acctNewPass");
      resetPasswordField("acctNewPass2");
      if (message) message.innerHTML = '<span class="ok">\u8BBE\u7F6E\u5DF2\u4FDD\u5B58</span>';
      await loadAccountSettings();
    } catch (error) {
      if (message) message.innerHTML = '<span class="err">' + esc(error.message) + "</span>";
    } finally {
      if (button) button.disabled = false;
    }
  }

  // js/entry.js
  function stripEmptyQueryFromUrl() {
    if (typeof location === "undefined" || typeof history === "undefined") return;
    const { pathname, search: search2, hash } = location;
    if (search2 !== "") return;
    const base = location.href.split("#")[0];
    if (base.endsWith("?") || base.endsWith("/?")) {
      history.replaceState(null, "", pathname + hash);
    }
  }
  (async () => {
    stripEmptyQueryFromUrl();
    await initAuth();
    await Promise.resolve().then(() => (init_main(), main_exports));
    ModelDownload.init();
    exposeGlobals({
      ModelDownload,
      InferenceParams,
      submitLogin,
      logout,
      loadAccountSettings,
      submitAccountCredentials,
      openAccountSettings,
      forceLoginScreen
    });
  })();
})();
