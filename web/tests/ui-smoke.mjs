import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { chromium } from 'playwright';

const baseURL = process.env.BROWSER_BASE_URL || 'http://127.0.0.1:8088';
const password = process.env.BROWSER_TEST_PASSWORD || 'ci-browser-pass';
const adminKey = process.env.BROWSER_TEST_ADMIN_KEY || 'browser-smoke-admin-key-not-for-production';
const artifactDir = process.env.BROWSER_ARTIFACT_DIR || path.resolve('web-ui-artifacts');
fs.mkdirSync(artifactDir, { recursive: true });

const report = {
  baseURL,
  commitSHA: process.env.GITHUB_SHA || 'unknown',
  startedAt: new Date().toISOString(),
  checks: [],
  pageErrors: [],
  consoleErrors: [],
  environmentConsoleWarnings: [],
  failedStaticAssets: [],
  authFailures: [],
  expectedAuthFailures: [],
  adminKeySuccesses: [],
  adminKeyRequests: [],
  authHeaderUsage: [],
  serverErrors: [],
  unexpectedDialogs: [],
  layouts: [],
  notes: [
    '测试运行真实 Gateway、MariaDB、Redis 和 Chromium。',
    '本地模型上游地址特意指向未启动的回环端口；本测试不覆盖真实模型推理。',
    '模型目录搜索接口使用浏览器侧空结果夹具，避免 CI 依赖外部模型目录服务。'
  ]
};
const failures = [];
let browser;
let expectedAdminKeyRejection = false;
let expectedLoginRejection = false;
let expectedNonServiceKeyRejection = false;
let expectedExpiredSessionRejection = false;

function check(name, condition, detail) {
  report.checks.push({ name, passed: Boolean(condition), detail: detail ?? null });
  if (!condition) failures.push(name + (detail ? ': ' + JSON.stringify(detail) : ''));
}

function screenshotName(name) {
  return path.join(artifactDir, name + '.png');
}

async function recordLayout(page, name) {
  const sizes = await page.evaluate(() => ({
    viewportWidth: window.innerWidth,
    documentWidth: document.documentElement.scrollWidth,
    bodyWidth: document.body.scrollWidth,
    viewportHeight: window.innerHeight,
    documentHeight: document.documentElement.scrollHeight
  }));
  const pass = sizes.documentWidth <= sizes.viewportWidth + 1 &&
    sizes.bodyWidth <= sizes.viewportWidth + 1;
  report.layouts.push({ name, ...sizes, passed: pass });
  check('页面无横向溢出：' + name, pass, sizes);
  return sizes;
}

async function clickTab(page, tab, title) {
  const button = page.locator('.nav button[data-tab="' + tab + '"]');
  await button.waitFor({ state: 'visible', timeout: 10000 });
  await button.click();
  await page.waitForTimeout(350);
  const active = await page.locator('#' + tab).evaluate(el => el.classList.contains('active'));
  check('导航切换：' + title, active);
  return active;
}

async function login(page) {
  const field = page.locator('#authLoginPass');
  await field.fill(password);
  await page.locator('#authLoginBtn').click();
  await page.waitForFunction(() => {
    const shell = document.querySelector('#appShell');
    const auth = document.querySelector('#authScreen');
    return Boolean(shell && !shell.hidden && auth && !auth.classList.contains('show'));
  }, null, { timeout: 20000 });
}

try {
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.setDefaultTimeout(10000);

  page.on('pageerror', error => report.pageErrors.push(String(error && (error.stack || error.message) || error)));
  page.on('console', message => {
    if (message.type() !== 'error' || /favicon\.ico/i.test(message.text())) return;
    const messageText = message.text();
    if (/Failed to load resource: the server responded with a status of 401 \(Unauthorized\)/.test(messageText) &&
        (expectedAdminKeyRejection || expectedLoginRejection || expectedNonServiceKeyRejection || expectedExpiredSessionRejection)) {
      report.expectedAuthFailures.push({ source: 'browser-console', message: messageText });
      return;
    }
    if (/Failed to load resource: the server responded with a status of 502 \(Bad Gateway\)/.test(messageText)) {
      report.environmentConsoleWarnings.push(messageText);
      return;
    }
    report.consoleErrors.push(messageText);
  });
  
  page.on('dialog', dialog => {
    report.unexpectedDialogs.push(dialog.type() + ': ' + dialog.message());
    dialog.dismiss().catch(() => {});
  });
  page.on('response', response => {
    const url = response.url();
    let pathname = '';
    try { pathname = new URL(url).pathname; } catch {}
    if (url.includes('/static/') && response.status() >= 400) report.failedStaticAssets.push({ status: response.status(), url });
    const isServiceKeyPath = pathname === '/api/admin/keys' || pathname.startsWith('/api/admin/keys/');
    const expectedAuthFailure = response.status() === 401 && (
      (pathname === '/api/auth/login' && expectedLoginRejection) ||
      (isServiceKeyPath && expectedAdminKeyRejection) ||
      (pathname === '/api/admin/runtime' && expectedNonServiceKeyRejection) ||
      (pathname === '/api/auth/me' && expectedExpiredSessionRejection)
    );
    if (expectedAuthFailure) report.expectedAuthFailures.push({ status: response.status(), url });
    if (isServiceKeyPath) {
      report.adminKeyRequests.push({ status: response.status(), url });
      if (response.status() === 200) report.adminKeySuccesses.push({ status: response.status(), url });
      if (response.status() === 401 && !expectedAuthFailure) report.authFailures.push({ status: response.status(), url });
    } else if (url.includes('/api/') && response.status() === 401 &&
        !expectedAuthFailure && pathname !== '/api/auth/status') {
      report.authFailures.push({ status: response.status(), url });
    }
    if (url.includes('/api/') && response.status() >= 500) report.serverErrors.push({ status: response.status(), url });
  });
  page.on('request', request => {
    let pathname = '';
    try { pathname = new URL(request.url()).pathname; } catch {}
    if (!pathname.startsWith('/api/')) return;
    const headers = request.headers();
    report.authHeaderUsage.push({
      path: pathname, hasAdminKey: Boolean(headers['x-admin-key']),
      hasSession: Boolean(headers.authorization && headers.authorization.startsWith('Bearer ')),
      expectedUnauthorizedProbe: expectedNonServiceKeyRejection && pathname === '/api/admin/runtime'
    });
  });

  // 外部模型目录不是浏览器布局验收的依赖；其它 API 请求仍走真实 Gateway。
  await page.route('**/api/admin/catalog/search**', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) })
  );

  const response = await page.goto(baseURL, { waitUntil: 'domcontentloaded' });
  check('首页 HTTP 状态', Boolean(response && response.status() === 200), response ? response.status() : null);
  await page.locator('#authScreen.show').waitFor({ state: 'visible', timeout: 15000 });
  check('未登录时显示独立登录页', await page.locator('#authLoginForm').isVisible());
  check('未登录时管理台被隐藏', await page.locator('#appShell').evaluate(el => el.hidden));
  const loginPassword = page.locator('#authLoginPass'), loginToggle = page.locator('#authScreen .password-toggle');
  await loginToggle.click();
  check('登录密码可显示明文', await loginPassword.evaluate(el => el.type === 'text'));
  await loginToggle.click();
  check('登录密码可恢复隐藏', await loginPassword.evaluate(el => el.type === 'password'));
  expectedLoginRejection = true;
  await loginPassword.fill('incorrect-web-password-for-browser-smoke');
  await page.locator('#authLoginBtn').click();
  await page.waitForFunction(() => (document.getElementById('authMsg')?.textContent || '').includes('密码错误'), null, { timeout: 15000 });
  expectedLoginRejection = false;
  check('错误 Web 密码不能登录且不会创建 Session',
    await page.locator('#appShell').evaluate(el => el.hidden) &&
    await page.evaluate(() => localStorage.getItem('llm_gateway_session_token')) === null);
  await login(page);
  check('密码登录建立 Web Session', await page.locator('#appShell').isVisible());
  check('管理员 Key 与登录凭据独立',
    Boolean(await page.evaluate(() => localStorage.getItem('llm_gateway_session_token'))) &&
    await page.evaluate(() => localStorage.getItem('llm_gateway_admin_key')) === null);

  await clickTab(page, 'keys', '服务密钥');
  const panelText = await page.locator('#keys .admin-auth-card').evaluate(el => el.textContent || '');
  check('管理员 API Key 只用于服务密钥管理', panelText.includes('仅用于服务密钥管理') &&
    panelText.includes('不用于控制台登录或其他管理页面'));
  const adminKeyField = page.locator('#adminKey'), keyToggle = page.locator('#adminKey').locator('xpath=..').locator('.password-toggle');
  check('管理员 Key 输入框存在且默认隐藏', await adminKeyField.count() === 1 && await adminKeyField.evaluate(el => el.type === 'password'));
  await keyToggle.click();
  check('管理员 Key 可显示明文', await adminKeyField.evaluate(el => el.type === 'text'));
  await keyToggle.click();
  check('管理员 Key 可恢复隐藏', await adminKeyField.evaluate(el => el.type === 'password'));

  expectedAdminKeyRejection = true;
  await adminKeyField.fill('incorrect-admin-key-for-browser-smoke');
  await page.locator('#adminKeySaveButton').click();
  await page.waitForFunction(() => (document.getElementById('adminKeyMsg')?.textContent || '').includes('验证失败'), null, { timeout: 15000 });
  expectedAdminKeyRejection = false;
  check('错误管理员 Key 不会保存到浏览器', await page.evaluate(() => localStorage.getItem('llm_gateway_admin_key')) === null);
  check('错误管理员 Key 被服务密钥接口拒绝', report.expectedAuthFailures.some(x => x.status === 401), report.expectedAuthFailures);

  await adminKeyField.fill(adminKey);
  await page.locator('#adminKeySaveButton').click();
  await page.waitForFunction(expected => localStorage.getItem('llm_gateway_admin_key') === expected,
    adminKey, { timeout: 15000 });
  await page.waitForFunction(() => (document.getElementById('adminKeyMsg')?.textContent || '').includes('验证成功'), null, { timeout: 15000 });
  check('正确管理员 Key 独立保存在浏览器', await page.evaluate(expected => localStorage.getItem('llm_gateway_admin_key') === expected, adminKey));
  check('管理员 Key 通过现有服务密钥接口验证', report.adminKeySuccesses.length >= 1, report.adminKeySuccesses);

  await page.locator('#keyServicesList .key-service-option').first().waitFor({ state: 'visible', timeout: 15000 });
  const servicePickerMetrics = await page.locator('#keyServicesList').evaluate(el => ({
    count: el.querySelectorAll('input[type="checkbox"]').length,
    overflowY: getComputedStyle(el).overflowY,
    maxHeight: getComputedStyle(el).maxHeight,
    hasLocalService: Boolean(el.querySelector('input[value="llamacpp"]')),
    createDisabled: document.getElementById('keyCreateButton')?.disabled === true,
    summary: document.getElementById('keyServicesSelectionSummary')?.textContent || ''
  }));
  check('新增密钥的服务范围是可上下滚动的多选列表', servicePickerMetrics.count >= 2 &&
    servicePickerMetrics.overflowY === 'auto' && servicePickerMetrics.maxHeight !== 'none' &&
    servicePickerMetrics.hasLocalService && servicePickerMetrics.createDisabled === false, servicePickerMetrics);
  check('未选择服务时明确显示不限制', servicePickerMetrics.summary.includes('所有已配置服务'));
  await page.locator('#keyServicesList input[value="llamacpp"]').check();
  check('选中服务后同步到密钥权限字段', await page.locator('#keyModels').inputValue() === 'llamacpp');
  await page.locator('#keyName').fill('browser-smoke-service-scope');
  const scopedCreateResponsePromise = page.waitForResponse(response => {
    try { return new URL(response.url()).pathname === '/api/admin/keys' && response.request().method() === 'POST'; }
    catch { return false; }
  });
  await page.locator('#keyCreateButton').click();
  const scopedCreateResponse = await scopedCreateResponsePromise;
  let scopedKey = null; try { scopedKey = await scopedCreateResponse.json(); } catch {}
  check('真实服务密钥接口收到所选服务权限', scopedCreateResponse.status() === 200 &&
    JSON.stringify(scopedKey?.api_key?.allowed_models || []) === JSON.stringify(['llamacpp']),
    {status:scopedCreateResponse.status(),allowed_models:scopedKey?.api_key?.allowed_models||null});
  await page.waitForFunction(() => document.getElementById('newKeyBox')?.style.display === 'block', null, {timeout:10000});

  if(scopedKey?.api_key?.id){
    const scopedId = scopedKey.api_key.id;
    const editButton = page.locator('#keyTable .key-edit-button[data-key-id="' + scopedId + '"]');
    await editButton.waitFor({state:'visible',timeout:10000});
    await editButton.click();
    await page.locator('#keyEditModal.show').waitFor({state:'visible',timeout:10000});
    check('编辑弹窗会恢复当前服务权限', await page.locator('#keyEditServicesList input[value="llamacpp"]').isChecked());
    await page.locator('#keyEditServicesList input[value="llamacpp"]').uncheck();
    await page.locator('#keyEditServicesList input[value="ollama"]').check();
    await page.locator('#keyEditName').fill('browser-smoke-service-scope-edited');
    await page.locator('#keyEditRPM').fill('120');
    const firstUpdatePromise = page.waitForResponse(response => {
      try { return new URL(response.url()).pathname === '/api/admin/keys/' + scopedId && response.request().method() === 'PUT'; }
      catch { return false; }
    });
    await page.locator('#keyEditSave').click();
    const firstUpdate = await firstUpdatePromise;
    check('已有密钥可以通过编辑表单保存服务、限流和名称', firstUpdate.status() === 200, {status:firstUpdate.status()});
    await page.waitForFunction(id => {
      const row = document.querySelector('#keyTable .key-edit-button[data-key-id="' + id + '"]')?.closest('tr');
      return Boolean(row && row.innerText.includes('browser-smoke-service-scope-edited') && row.innerText.includes('ollama'));
    }, scopedId, {timeout:10000});

    const getEditedKey = async () => page.evaluate(async({key,id}) => {
      const response=await fetch('/api/admin/keys',{headers:{'X-Admin-Key':key},cache:'no-store'});
      const body=await response.json();
      return (body.data||[]).find(item=>Number(item.id)===Number(id))||null;
    },{key:adminKey,id:scopedId});
    let editedKey=await getEditedKey();
    check('保存后服务范围与 RPM 已写入后端',editedKey?.name==='browser-smoke-service-scope-edited' &&
      Number(editedKey?.rpm)===120 && JSON.stringify(editedKey?.allowed_models||[])===JSON.stringify(['ollama']),
      {name:editedKey?.name,rpm:editedKey?.rpm,allowed_models:editedKey?.allowed_models});

    const legacySeedStatus=await page.evaluate(async({key,id})=>{
      const response=await fetch('/api/admin/keys/'+id,{method:'PUT',headers:{'X-Admin-Key':key,'Content-Type':'application/json'},
        body:JSON.stringify({name:'browser-smoke-service-scope-edited',rpm:120,allowed_models:['ollama','legacy-model-id'],expires_at:null})});
      return response.status;
    },{key:adminKey,id:scopedId});
    check('为旧模型权限兼容测试准备测试密钥',legacySeedStatus===200,legacySeedStatus);
    await page.evaluate(()=>globalThis.loadKeys());
    await page.locator('#keyTable .key-edit-button[data-key-id="' + scopedId + '"]').click();
    await page.locator('#keyEditModal.show').waitFor({state:'visible',timeout:10000});
    check('编辑旧密钥时识别并默认保留遗留模型 ID',await page.locator('#keyEditPreserveLegacy').isChecked() &&
      (await page.locator('#keyEditLegacyMessage').textContent()).includes('legacy-model-id'));
    const preserveLegacyPromise=page.waitForResponse(response=>{
      try{return new URL(response.url()).pathname==='/api/admin/keys/'+scopedId&&response.request().method()==='PUT'}catch{return false}
    });
    await page.locator('#keyEditSave').click();
    const preserveLegacyResponse=await preserveLegacyPromise;
    editedKey=await getEditedKey();
    check('保存时不会静默丢弃旧模型 ID 权限',preserveLegacyResponse.status()===200 &&
      (editedKey?.allowed_models||[]).includes('legacy-model-id')&&(editedKey?.allowed_models||[]).includes('ollama'),
      {status:preserveLegacyResponse.status(),allowed_models:editedKey?.allowed_models});

    await page.locator('#keyTable .key-edit-button[data-key-id="' + scopedId + '"]').click();
    await page.locator('#keyEditModal.show').waitFor({state:'visible',timeout:10000});
    await page.locator('#keyEditPreserveLegacy').uncheck();
    const dropLegacyPromise=page.waitForResponse(response=>{
      try{return new URL(response.url()).pathname==='/api/admin/keys/'+scopedId&&response.request().method()==='PUT'}catch{return false}
    });
    await page.locator('#keyEditSave').click();
    const dropLegacyResponse=await dropLegacyPromise;
    editedKey=await getEditedKey();
    check('明确取消保留旧权限后可转换为仅服务范围',dropLegacyResponse.status()===200 &&
      JSON.stringify(editedKey?.allowed_models||[])===JSON.stringify(['ollama']),
      {status:dropLegacyResponse.status(),allowed_models:editedKey?.allowed_models});

    const cleanupStatus=await page.evaluate(async({key,id})=>{
      const response=await fetch('/api/admin/keys/'+id,{method:'DELETE',headers:{'X-Admin-Key':key},cache:'no-store'});
      return response.status;
    },{key:adminKey,id:scopedId});
    check('浏览器验收清理临时服务密钥',cleanupStatus===204,cleanupStatus);
  }
  await page.locator('#btnClearKeyServices').click();
  check('清空服务选择恢复为不限制',await page.locator('#keyModels').inputValue()==='' &&
    (await page.locator('#keyServicesSelectionSummary').textContent()).includes('所有已配置服务'));

  await page.evaluate(() => localStorage.removeItem('llm_gateway_session_token'));
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.locator('#authScreen.show').waitFor({ state: 'visible', timeout: 10000 });
  check('已有管理员 Key 不能绕过 Web 密码登录', await page.locator('#authScreen').isVisible() &&
    await page.evaluate(expected => localStorage.getItem('llm_gateway_admin_key') === expected, adminKey));
  expectedNonServiceKeyRejection = true;
  const keyOnlyRuntimeResponse = await page.evaluate(async key => {
    const response = await fetch('/api/admin/runtime', { headers: { 'X-Admin-Key': key }, cache: 'no-store' });
    return { status: response.status, body: await response.text() };
  }, adminKey);
  expectedNonServiceKeyRejection = false;
  check('没有 Web Session 时管理员 Key 不能访问其他管理 API',
    keyOnlyRuntimeResponse.status === 401, keyOnlyRuntimeResponse);
  await login(page);
  check('重新登录成功且管理员 Key 仍保留', await page.locator('#appShell').isVisible() &&
    await page.evaluate(expected => localStorage.getItem('llm_gateway_admin_key') === expected, adminKey));

  expectedExpiredSessionRejection = true;
  await page.evaluate(() => localStorage.setItem('llm_gateway_session_token', 'invalid-expired-browser-session'));
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() =>
    document.getElementById('authScreen')?.classList.contains('show') &&
    localStorage.getItem('llm_gateway_session_token') === null,
    null, { timeout: 15000 });
  expectedExpiredSessionRejection = false;
  check('无效或过期的 Web Session 会被拒绝并清除',
    await page.evaluate(() => localStorage.getItem('llm_gateway_session_token')) === null &&
    await page.locator('#authScreen').evaluate(el => el.classList.contains('show')));
  check('Session 失效不会清除独立管理员 Key',
    await page.evaluate(expected => localStorage.getItem('llm_gateway_admin_key') === expected, adminKey));
  await login(page);
  check('Session 失效后仍可使用 Web 密码重新登录', await page.locator('#appShell').isVisible());

  await clickTab(page, 'keys', '服务密钥-再次加载');
  await page.waitForTimeout(300);
  check('服务密钥请求使用管理员 Key 而非 Web Session',
    report.authHeaderUsage.some(item => item.path === '/api/admin/keys' && item.hasAdminKey && !item.hasSession),
    report.authHeaderUsage);
  await page.screenshot({ path: screenshotName('keys-admin-key-desktop'), fullPage: true });
  await page.locator('.nav button[data-tab="dashboard"]').click();
  await page.waitForTimeout(500);
  check('其它管理 API 只使用 Web Session、不发送管理员 Key',
    report.authHeaderUsage.some(item => item.path.startsWith('/api/') &&
      item.path !== '/api/admin/keys' && !item.path.startsWith('/api/admin/keys/') &&
      item.hasSession && !item.hasAdminKey), report.authHeaderUsage);
  const adminKeyOutsideServiceKeyRequests = report.authHeaderUsage.filter(item => item.hasAdminKey &&
    item.path !== '/api/admin/keys' && !item.path.startsWith('/api/admin/keys/') &&
    !item.expectedUnauthorizedProbe);
  check('X-Admin-Key 只出现在服务密钥管理请求',
    adminKeyOutsideServiceKeyRequests.length === 0, adminKeyOutsideServiceKeyRequests);

  const appBundle = fs.readFileSync(path.resolve('web/static/dist/app.js'), 'utf8');
  const providerBundle = fs.readFileSync(path.resolve('web/static/dist/providers-admin.js'), 'utf8');
  check('远程 Provider 请求不会使用管理员服务密钥',
    !providerBundle.includes('const adminKey = () =>') &&
    !providerBundle.includes("headers['X-Admin-Key'] = adminKey()"));
  check('本地模型 Bundle 已移除来源列渲染逻辑',
    !appBundle.includes('llamaCatalogSourceLabel') &&
    !appBundle.includes(String.raw`<th>\u6765\u6E90</th>`));

  // 桌面主要 Tab：验证可切换、实际渲染以及页面不向右撑破视口。
  const tabs = [
    ['dashboard', '首页'],
    ['models-local', '本地模型'],
    ['models-download', '模型下载'],
    ['models-inference', '服务参数'],
    ['keys', '服务密钥'],
    ['requests', '调用记录'],
    ['benchmark', '性能测试'],
    ['providers', '远程 Provider']
  ];
  for (const [tab, title] of tabs) {
    await clickTab(page, tab, title);
    await recordLayout(page, title + '-desktop');

    if (tab === 'models-local') {
      const sourceHeaders = await page.locator('#modelTable th').evaluateAll(
        headers => headers.map(header => (header.textContent || '').trim()).filter(label => label === '来源')
      );
      check('本地模型表不展示来源列', sourceHeaders.length === 0, sourceHeaders);
    }

    if (tab === 'models-download') {
      const heights = await page.evaluate(() => {
        const selectors = ['#catalogQuery', '#catalogSource', '#catalogSort', '#catalogHfGguf'];
        return selectors.map(selector => {
          const el = document.querySelector(selector);
          return { selector, height: el?.getBoundingClientRect().height ?? null };
        });
      });
      const values = heights.map(item => item.height).filter(value => value != null);
      check('发现模型筛选输入框与下拉框高度统一',
        heights.length === 4 && values.length === 4 &&
        Math.max(...values) - Math.min(...values) <= 1 &&
        values.every(height => Math.abs(height - 42) <= 1),
        heights);
    }

    if (tab === 'requests') {
      const heights = await page.evaluate(() => {
        const selectors = ['#rqProvider', '#rqModel', '#rqStatus', '#rqKey', '#rqStatsRange'];
        return selectors.map(selector => {
          const el = document.querySelector(selector);
          return { selector, height: el?.getBoundingClientRect().height ?? null };
        });
      });
      const values = heights.map(item => item.height).filter(value => value != null);
      check('调用记录筛选输入框与下拉框高度统一',
        heights.length === 5 && values.length === 5 &&
        Math.max(...values) - Math.min(...values) <= 1 &&
        values.every(height => Math.abs(height - 42) <= 1),
        heights);
    }

    if (tab === 'providers') {
      const badges = await page.evaluate(() => {
        const section = document.querySelector('#providers');
        const probeTable = document.createElement('table');
        probeTable.innerHTML = '<tbody><tr><td>占位</td><td>占位</td><td>状态</td></tr></tbody>';
        section.appendChild(probeTable);
        const statusColumnWhiteSpace = getComputedStyle(probeTable.querySelector('td:nth-child(3)')).whiteSpace;
        probeTable.remove();
        return {
          statusColumnWhiteSpace,
          badgeCount: document.querySelectorAll('#providers .table-wrap tbody .pill').length,
          badges: [...document.querySelectorAll('#providers .table-wrap tbody .pill')].map(el => {
            const rect = el.getBoundingClientRect();
            return { text: el.textContent.trim(), height: rect.height, whiteSpace: getComputedStyle(el).whiteSpace };
          }),
          sample: (() => {
            const el = document.createElement('span');
            el.className = 'pill pill-active';
            el.textContent = '已启用';
            el.style.cssText = 'position:absolute;visibility:hidden;left:-10000px;top:0';
            document.body.appendChild(el);
            const rect = el.getBoundingClientRect();
            const result = { height: rect.height, whiteSpace: getComputedStyle(el).whiteSpace, width: rect.width };
            el.remove();
            return result;
          })()
        };
      });
      check('Provider 状态徽标保持单行且高度正常',
        badges.statusColumnWhiteSpace === 'nowrap' &&
        badges.sample.whiteSpace === 'nowrap' &&
        badges.sample.height <= 28 &&
        badges.badges.every(item => item.whiteSpace === 'nowrap' && item.height <= 28),
        badges);
    }

    if (['dashboard', 'models-local', 'models-download', 'requests', 'providers'].includes(tab)) {
      await page.screenshot({ path: screenshotName(tab + '-desktop'), fullPage: true });
    }
  }

  // Provider 表单打开/取消只验证 UI；不创建远程供应商、不发送真实 Provider 请求。
  await clickTab(page, 'providers', '远程 Provider');
  await page.locator('#providerCreate').click();
  const editorShown = await page.locator('#providerEditorCard').isVisible();
  check('远程 Provider 新增表单可打开', editorShown);
  if (editorShown) {
    check('Provider 表单关键字段存在',
      await page.locator('#providerId').count() === 1 &&
      await page.locator('#providerBaseURL').count() === 1 &&
      await page.locator('#providerDefaultModel').count() === 1);
    const templates = await page.locator('#providerTemplate option').evaluateAll(options =>
      options.map(option => ({value:option.value,label:(option.textContent||'').trim()}))
    );
    check('新增 Provider 提供常见厂商与自定义模板',
      ['openai','deepseek','siliconflow','openrouter','dashscope','volcengine','groq','mistral','custom']
        .every(value => templates.some(option => option.value === value)), templates);
    await page.locator('#providerTemplate').selectOption('deepseek');
    check('DeepSeek 模板预填 ID、Base URL、模型及鉴权 Header',
      await page.locator('#providerId').inputValue() === 'deepseek' &&
      await page.locator('#providerBaseURL').inputValue() === 'https://api.deepseek.com' &&
      await page.locator('#providerDefaultModel').inputValue() === 'deepseek-chat' &&
      await page.locator('#providerApiKeyHeader').inputValue() === 'authorization');
    check('厂商模板不会自动填写 API Key',
      await page.locator('#providerApiKey').inputValue() === '');
    await page.locator('#providerTemplate').selectOption('custom');
    check('自定义模板清空基础连接字段并保留手动配置能力',
      await page.locator('#providerId').inputValue() === '' &&
      await page.locator('#providerBaseURL').inputValue() === '' &&
      await page.locator('#providerDefaultModel').inputValue() === '' &&
      await page.locator('#providerApiKeyHeader').inputValue() === 'authorization' &&
      (await page.locator('#providerTemplateHelp').textContent()).includes('自定义模板'));
    await page.locator('#providerCancel').click();
    check('远程 Provider 表单可取消', await page.locator('#providerEditorCard').isHidden());
  }

  // 验证 HF 文件选择事件使用 data-* 属性传值，特殊字符不会进入内联 JavaScript。
  await clickTab(page, 'models-download', '模型下载');
  const transfer = await page.evaluate(() => {
    const box = document.getElementById('filesBox');
    if (!box) return { error: 'filesBox missing' };
    const filename = 'nested/model "quoted" & path.gguf';
    const repository = 'org/repo "quoted" & test';
    box.replaceChildren();
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = '选择测试文件';
    button.dataset.hfFile = filename;
    button.dataset.hfRepo = repository;
    box.appendChild(button);
    button.click();
    return {
      error: null,
      filename: document.getElementById('filename').value,
      repository: document.getElementById('repo').value,
      installVisible: document.getElementById('installForm').style.display !== 'none'
    };
  });
  check('HF 文件按钮能安全传递含引号和符号的文件路径',
    transfer.filename === 'nested/model "quoted" & path.gguf' &&
    transfer.repository === 'org/repo "quoted" & test' &&
    transfer.installVisible, transfer);

  // 账号页的密码显隐、登出和再次登录流程。
  await page.locator('#sidebarAccountBtn').click();
  await page.waitForTimeout(250);
  check('账号设置页可打开',
    await page.locator('#account').evaluate(el => el.classList.contains('active')));
  const newPassword = page.locator('#acctNewPass');
  if (await newPassword.count()) {
    const toggle = page.locator('#acctNewPass').locator('xpath=..').locator('.password-toggle');
    const visible = await toggle.count();
    if (visible) {
      await toggle.first().click();
      check('账号设置新密码可显示明文', await newPassword.evaluate(el => el.type === 'text'));
      await toggle.first().click();
      check('账号设置新密码可恢复隐藏', await newPassword.evaluate(el => el.type === 'password'));
    } else {
      check('账号设置新密码显隐按钮存在', false);
    }
  } else {
    check('账号设置新密码输入框存在', false);
  }
  await page.screenshot({ path: screenshotName('account-desktop'), fullPage: true });

  const logout = page.locator('#btnAccountLogout');
  if (await logout.isVisible()) {
    await logout.click();
    await page.locator('#authScreen.show').waitFor({ state: 'visible', timeout: 10000 });
    check('退出登录后返回登录页', true);
    check('Web 登出清除 Session 但保留管理员 Key',
      await page.evaluate(() => localStorage.getItem('llm_gateway_session_token')) === null &&
      await page.evaluate(expected => localStorage.getItem('llm_gateway_admin_key') === expected, adminKey));
    await login(page);
    check('退出后可以再次登录', await page.locator('#appShell').isVisible());
    check('重新登录后管理员 Key 仍然独立保留',
      Boolean(await page.evaluate(() => localStorage.getItem('llm_gateway_session_token'))) &&
      await page.evaluate(expected => localStorage.getItem('llm_gateway_admin_key') === expected, adminKey));
    await clickTab(page, 'keys', '服务密钥-清除管理员 Key');
    await page.locator('#adminKeyClearButton').click();
    check('清除管理员 Key 后本地 Key 已移除', await page.evaluate(() => localStorage.getItem('llm_gateway_admin_key')) === null);
    check('清除管理员 Key 不会退出 Web 登录',
      Boolean(await page.evaluate(() => localStorage.getItem('llm_gateway_session_token'))) &&
      await page.locator('#appShell').isVisible());
  } else {
    check('密码保护开启时登出按钮可用', false);
  }

  // 窄屏布局：检查页面自身不横向溢出，表格可在自己的滚动容器内浏览。
  await page.setViewportSize({ width: 390, height: 844 });
  await clickTab(page, 'dashboard', '首页-窄屏');
  await recordLayout(page, '首页-mobile-390');
  const inferenceLayout = await page.locator('.sys-runtime-panel .inf-stack-compact').evaluate(stack => {
    const cards = [...stack.querySelectorAll('.inf-card-compact')];
    const rects = cards.map(card => {
      const r = card.getBoundingClientRect();
      const title = card.querySelector('.inf-card-head h4')?.getBoundingClientRect();
      const tags = card.querySelector('.inf-card-tags')?.getBoundingClientRect();
      const overlap = Boolean(title && tags && title.left < tags.right && tags.left < title.right &&
        title.top < tags.bottom && tags.top < title.bottom);
      return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, overlap };
    });
    return { cardCount: cards.length, rects };
  });
  check('窄屏推理内存卡片纵向排列且标题与徽标不重叠',
    inferenceLayout.cardCount === 2 &&
    inferenceLayout.rects[0].left === inferenceLayout.rects[1].left &&
    inferenceLayout.rects[1].top >= inferenceLayout.rects[0].bottom - 1 &&
    inferenceLayout.rects.every(card => !card.overlap),
    inferenceLayout);
  const recentTableLayout = await page.locator('#dashRecentRequests .table-wrap').evaluate(wrap => ({
    tableWidth: wrap.querySelector('table')?.getBoundingClientRect().width || 0,
    clientWidth: wrap.clientWidth,
    scrollWidth: wrap.scrollWidth,
    overflowX: getComputedStyle(wrap).overflowX
  }));
  check('窄屏最近调用表四列均可见且不触发横向滚动',
    recentTableLayout.tableWidth > 0 &&
    recentTableLayout.tableWidth <= recentTableLayout.clientWidth + 1 &&
    recentTableLayout.scrollWidth <= recentTableLayout.clientWidth + 1,
    recentTableLayout);
  await page.screenshot({ path: screenshotName('dashboard-mobile-390'), fullPage: true });
  await clickTab(page, 'providers', '远程 Provider-窄屏');
  await recordLayout(page, 'Provider-mobile-390');
  const providerTableLayout = await page.locator('#providers .table-wrap').first().evaluate(wrap => ({
    tableWidth: wrap.querySelector('table')?.getBoundingClientRect().width || 0,
    clientWidth: wrap.clientWidth,
    scrollWidth: wrap.scrollWidth,
    overflowX: getComputedStyle(wrap).overflowX,
    hintVisible: (wrap.innerText || '').includes('左右滑动可查看完整表格') ||
      getComputedStyle(wrap, '::after').content.includes('左右滑动')
  }));
  check('窄屏 Provider 宽表保留列宽并明确提示横向查看',
    providerTableLayout.tableWidth >= 1000 &&
    providerTableLayout.scrollWidth > providerTableLayout.clientWidth &&
    ['auto', 'scroll'].includes(providerTableLayout.overflowX) &&
    providerTableLayout.hintVisible,
    providerTableLayout);
  await page.screenshot({ path: screenshotName('providers-mobile-390'), fullPage: true });
  await clickTab(page, 'requests', '调用记录-窄屏');
  await recordLayout(page, '调用记录-mobile-390');

  const expectedUnavailableUpstreamPaths = new Set([
    '/api/admin/runtime',
    '/api/admin/runtime/preview',
    '/api/admin/ollama'
  ]);
  const unexpectedServerErrors = report.serverErrors.filter(entry => {
    let pathname = '';
    try { pathname = new URL(entry.url).pathname; } catch { return true; }
    return !(entry.status === 502 && expectedUnavailableUpstreamPaths.has(pathname));
  });
  check('无浏览器未捕获异常', report.pageErrors.length === 0, report.pageErrors);
  check('无浏览器 JavaScript 控制台错误', report.consoleErrors.length === 0, report.consoleErrors);
  check('所有静态 JS/CSS 资源成功加载', report.failedStaticAssets.length === 0, report.failedStaticAssets);
  check('无非预期管理 API 401', report.authFailures.length === 0, report.authFailures);
  check('无非预期管理 API 5xx', unexpectedServerErrors.length === 0, unexpectedServerErrors);
  check('未触发意外浏览器弹窗', report.unexpectedDialogs.length === 0, report.unexpectedDialogs);
} catch (error) {
  failures.push('浏览器验收执行异常: ' + String(error && (error.stack || error.message) || error));
} finally {
  if (browser) await browser.close().catch(() => {});
  report.finishedAt = new Date().toISOString();
  report.failures = failures;
  report.passed = failures.length === 0;
  fs.writeFileSync(path.join(artifactDir, 'report.json'), JSON.stringify(report, null, 2));
}

console.log(JSON.stringify({
  passed: report.passed,
  checks: report.checks.filter(x => x.passed).length + '/' + report.checks.length,
  failures: report.failures,
  layouts: report.layouts,
  pageErrors: report.pageErrors,
  consoleErrors: report.consoleErrors,
  failedStaticAssets: report.failedStaticAssets,
  authFailures: report.authFailures,
  expectedAuthFailures: report.expectedAuthFailures,
  adminKeyRequests: report.adminKeyRequests,
  adminKeySuccesses: report.adminKeySuccesses,
  authHeaderUsage: report.authHeaderUsage,
  serverErrors: report.serverErrors,
  artifactDir
}, null, 2));

if (!report.passed) process.exitCode = 1;
