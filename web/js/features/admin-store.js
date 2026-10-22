import { api } from '../core/api.js';

/** 首页 bootstrap / lite 与全局可达性摘要（单容器 AdminState 的浏览器投影） */
export const adminStore = {
  bootstrapped: false,
  upstream: null,
  inferenceSummary: {},
  dependencies: { upstream: '', redis: '' },
  host: null,

  applyBootstrap(j) {
    if (!j) return;
    this.bootstrapped = true;
    this.upstream = j.upstream || null;
    this.inferenceSummary = j.inference_summary || {};
    this.dependencies = j.dependencies || { upstream: '', redis: '' };
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
    for (const be of ['llamacpp', 'ollama']) {
      const sum = this.inferenceSummary[be];
      if (!sum) continue;
      out[be] = {
        reachable: sum.reachable === true,
        loaded_count: Number(sum.loaded_count) || 0,
        base_url: sum.base_url || '',
      };
    }
    return out;
  },

  /** @param {'bootstrap'|'lite'|'auto'} mode */
  async syncDashboard(mode = 'auto') {
    if (mode === 'bootstrap' || (mode === 'auto' && !this.bootstrapped)) {
      const j = await api('/api/admin/bootstrap');
      this.applyBootstrap(j);
      return j;
    }
    const j = await api('/api/admin/bootstrap/lite');
    this.applyLite(j);
    return j;
  },

  reset() {
    this.bootstrapped = false;
    this.upstream = null;
    this.inferenceSummary = {};
    this.dependencies = { upstream: '', redis: '' };
    this.host = null;
  },
};
