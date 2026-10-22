import { exposeGlobals } from './expose.js';
import { ModelDownload } from './features/model-download.js';
import { InferenceParams } from './features/inference-params.js';
import {
  initAuth,
  submitLogin,
  logout,
  loadAccountSettings,
  submitAccountCredentials,
  openAccountSettings,
  forceLoginScreen,
} from './features/auth.js';

/** 去掉地址栏无参数的尾随 ?（部分反代/旧式 GET 表单会留下 /?） */
function stripEmptyQueryFromUrl() {
  if (typeof location === 'undefined' || typeof history === 'undefined') return;
  const { pathname, search, hash } = location;
  if (search !== '') return;
  const base = location.href.split('#')[0];
  if (base.endsWith('?') || base.endsWith('/?')) {
    history.replaceState(null, '', pathname + hash);
  }
}

(async () => {
  stripEmptyQueryFromUrl();
  await initAuth();
  await import('./app/main.js');
  ModelDownload.init();
  exposeGlobals({
    ModelDownload,
    InferenceParams,
    submitLogin,
    logout,
    loadAccountSettings,
    submitAccountCredentials,
    openAccountSettings,
    forceLoginScreen,
  });
})();
