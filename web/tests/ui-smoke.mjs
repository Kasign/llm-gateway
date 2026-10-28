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
  expectedProviderFailures: [],
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
let expectedProviderListRefreshFailure = false;
let expectedProviderListRefreshConsoleCount = 0;
let expectedProviderListRefreshResponseCount = 0;

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
    if (/Failed to load resource: the server responded with a status of 503 \(Service Unavailable\)/.test(messageText) &&
        expectedProviderListRefreshFailure && expectedProviderListRefreshConsoleCount === 0) {
      expectedProviderListRefreshConsoleCount += 1;
      report.expectedProviderFailures.push({ source: 'browser-console', message: messageText });
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
    if (url.includes('/api/') && response.status() >= 500) {
      if (response.status() === 503 && pathname === '/api/admin/providers' &&
          expectedProviderListRefreshFailure && expectedProviderListRefreshResponseCount === 0) {
        expectedProviderListRefreshResponseCount += 1;
        report.expectedProviderFailures.push({ status: response.status(), url });
      } else {
        report.serverErrors.push({ status: response.status(), url });
      }
    }
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

    // 模拟编辑弹窗关闭时，密钥读取请求仍在进行；迟到的响应不得回写隐藏表单。
    let delayNextKeyRead = false;
    let releaseDelayedKeyRead;
    let resolveDelayedKeyReadStarted;
    const delayedKeyReadStarted = new Promise(resolve => { resolveDelayedKeyReadStarted = resolve; });
    const delayedKeyReadGate = new Promise(resolve => { releaseDelayedKeyRead = resolve; });
    await page.route('**/api/admin/keys*', async route => {
      const request = route.request();
      let pathname = '';
      try { pathname = new URL(request.url()).pathname; } catch {}
      if (delayNextKeyRead && request.method() === 'GET' && pathname === '/api/admin/keys') {
        delayNextKeyRead = false;
        resolveDelayedKeyReadStarted();
        await delayedKeyReadGate;
      }
      await route.continue();
    });
    const editNameBeforeDelayedOpen = await page.locator('#keyEditName').inputValue();
    delayNextKeyRead = true;
    await editButton.click();
    await page.locator('#keyEditModal.show').waitFor({state:'visible',timeout:10000});
    await delayedKeyReadStarted;
    await page.keyboard.press('Escape');
    await page.locator('#keyEditModal').waitFor({state:'hidden',timeout:5000});
    const delayedKeyListResponse = page.waitForResponse(response => {
      try { return new URL(response.url()).pathname === '/api/admin/keys' && response.request().method() === 'GET'; }
      catch { return false; }
    });
    releaseDelayedKeyRead();
    await delayedKeyListResponse;
    await page.waitForTimeout(100);
    check('关闭服务密钥弹窗后忽略迟到的读取响应',
      await page.locator('#keyEditModal').getAttribute('aria-hidden') === 'true' &&
      await page.locator('#keyEditSave').isDisabled() &&
      await page.locator('#keyEditName').inputValue() === editNameBeforeDelayedOpen &&
      await editButton.isEnabled());
    await page.unroute('**/api/admin/keys*');

    await editButton.click();
    await page.locator('#keyEditModal.show').waitFor({state:'visible',timeout:10000});
    await page.waitForFunction(() => {
      const save = document.getElementById('keyEditSave');
      return Boolean(save && !save.disabled);
    }, null, {timeout:10000});
    await page.locator('#keyEditClose').focus();
    await page.keyboard.press('Shift+Tab');
    check('服务密钥编辑弹窗的 Shift+Tab 焦点循环留在弹窗内',
      await page.locator('#keyEditSave').evaluate(el => document.activeElement === el));
    check('键盘焦点在主要操作按钮上有清晰可见的轮廓',
      await page.locator('#keyEditSave').evaluate(el => {
        const style = getComputedStyle(el);
        return style.outlineStyle !== 'none' && parseFloat(style.outlineWidth) >= 2;
      }));
    await page.keyboard.press('Escape');
    await page.locator('#keyEditModal').waitFor({state:'hidden',timeout:5000});
    check('服务密钥编辑弹窗支持 Escape 并返回触发按钮',
      await editButton.evaluate(el => document.activeElement === el));
    await editButton.click();
    await page.locator('#keyEditModal.show').waitFor({state:'visible',timeout:10000});
    await page.waitForFunction(() => {
      const save = document.getElementById('keyEditSave');
      return Boolean(save && !save.disabled);
    }, null, {timeout:10000});
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
    // 弹窗会先显示、再异步读取密钥和服务；断言必须等旧权限提示加载后再执行。
    await page.waitForFunction(() => document.getElementById('keyEditPreserveLegacy')?.checked === true &&
      (document.getElementById('keyEditLegacyMessage')?.textContent || '').includes('legacy-model-id'),
      null, {timeout:10000});
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
  const requestsSource = fs.readFileSync(path.resolve('web/js/features/requests.js'), 'utf8');
  const providersSource = fs.readFileSync(path.resolve('web/js/providers-entry.js'), 'utf8');
  const keyPickerSource = fs.readFileSync(path.resolve('web/js/key-service-picker.js'), 'utf8');
  const keyPickerBundle = fs.readFileSync(path.resolve('web/static/dist/key-service-picker.js'), 'utf8');
  const indexHtml = fs.readFileSync(path.resolve('web/index.html'), 'utf8');
  const appCss = fs.readFileSync(path.resolve('web/static/css/app.css'), 'utf8');
  // 从浏览器实际加载的 CSS 变量计算核心文字与主要深色表面的 WCAG 对比度。
  const textContrastAudit = await page.evaluate(() => {
    const root = getComputedStyle(document.documentElement);
    const color = name => root.getPropertyValue('--' + name).trim();
    const linear = value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
    const luminance = hex => {
      if (!/^#[0-9a-f]{6}$/i.test(hex)) return Number.NaN;
      const rgb = [1, 3, 5].map(offset => linear(parseInt(hex.slice(offset, offset + 2), 16) / 255));
      return 0.2126 * rgb[0] + 0.7152 * rgb[1] + 0.0722 * rgb[2];
    };
    const ratio = (foreground, background) => {
      const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
      return Number(((values[0] + 0.05) / (values[1] + 0.05)).toFixed(2));
    };
    const result = {};
    for (const foreground of ['text', 'text-2', 'text-3']) {
      for (const background of ['bg', 'bg-2', 'sidebar', 'surface', 'surface-2']) {
        result[foreground + ' / ' + background] = ratio(color(foreground), color(background));
      }
    }
    return result;
  });
  check('核心文字颜色在五种主要深色表面达到 WCAG AA（≥4.5:1）',
    Object.values(textContrastAudit).every(value => Number.isFinite(value) && value >= 4.5),
    textContrastAudit);
  const formLabelAudit = await page.evaluate(() => {
    const ids = [
      'beBackend', 'repo', 'revision', 'modelKey', 'displayName', 'quant', 'filename',
      'keyName', 'keyRPM', 'keyExpires', 'rqProvider', 'rqModel', 'rqStatus', 'rqKey',
      'rqStatsRange', 'keyEditName', 'keyEditRPM', 'keyEditExpires'
    ];
    return Object.fromEntries(ids.map(id => {
      const control = document.getElementById(id);
      return [id, Boolean(control && control.labels && control.labels.length > 0)];
    }));
  });
  check('主要表单控件均关联可访问名称', Object.values(formLabelAudit).every(Boolean), formLabelAudit);
  check('服务范围多选组使用可见标题作为无障碍名称',
    indexHtml.includes('id="keyServicesList" class="key-service-list" role="group" aria-labelledby="keyServicesLabel"') &&
    indexHtml.includes('id="keyEditServicesList" class="key-service-list" role="group" aria-labelledby="keyEditServicesLabel"'));
  check('编辑密钥的错误状态使用 assertive live region',
    indexHtml.includes('id="keyEditError" class="msg key-edit-error" role="alert" aria-live="assertive"'));
  const providerIdPattern = await page.locator('#providerId').getAttribute('pattern');
  const providerIdPatternValid = await page.evaluate(pattern => {
    try { new RegExp(pattern, 'v'); return true; }
    catch (error) { return String(error); }
  }, providerIdPattern);
  check('Provider ID 正则兼容新版浏览器 v 模式',
    providerIdPatternValid === true, providerIdPatternValid);
  const providerIdSamples = await page.evaluate(() => {
    const input = document.getElementById('providerId');
    const results = {};
    for (const value of ['openai', 'my-provider_1.test', 'a']) {
      input.value = value;
      results[value] = input.checkValidity();
    }
    for (const value of ['OpenAI', 'has space', 'bad/slash']) {
      input.value = value;
      results[value] = input.checkValidity();
    }
    return results;
  });
  check('Provider ID 正则仍接受小写字母数字及 . _ -，拒绝其他字符',
    providerIdSamples.openai && providerIdSamples['my-provider_1.test'] && providerIdSamples.a &&
    !providerIdSamples.OpenAI && !providerIdSamples['has space'] && !providerIdSamples['bad/slash'],
    providerIdSamples);

  check('Provider 保存具备表单级防重入、重复 ID 校验和加载失败保护',
    providersSource.includes("form.dataset.saving === 'true'") &&
    providersSource.includes('globalThis.__llmGatewayProviderAdminInitialized') &&
    providersSource.includes("if (!state.loaded)") &&
    providersSource.includes("const existingProvider = rowByID(payload.id)") &&
    providerBundle.includes("不能重复新增"));
  check('Provider 列表区分首次加载失败与刷新失败并声明 aria-busy',
    providersSource.includes('Provider 列表加载失败，请点击“刷新”重试') &&
    providersSource.includes('仍保留上次成功加载的数据') &&
    providersSource.includes("setAttribute('aria-busy', 'true')") &&
    indexHtml.includes('id="providerStatus" class="msg muted" role="status" aria-live="polite" aria-busy="false"'));
  check('请求详情弹窗具备可访问语义、Escape 关闭和焦点循环',
    indexHtml.includes('id="detailModal" class="modal" role="dialog" aria-modal="true" aria-labelledby="requestDetailTitle" aria-hidden="true"') &&
    requestsSource.includes('detailTrigger = document.activeElement') &&
    requestsSource.includes("event.key === 'Escape'") &&
    requestsSource.includes('event.shiftKey && (active === first || !modal.contains(active))') &&
    appBundle.includes('detailTrigger = document.activeElement') &&
    appBundle.includes('event.key === "Escape"'));
  check('服务密钥编辑 Bundle 与源码一致，且具备 Escape、焦点返回、Tab 循环和异步关闭保护',
    keyPickerBundle === keyPickerSource &&
    keyPickerSource.includes('function restoreEditFocus()') &&
    keyPickerSource.includes("if(event.key==='Escape')") &&
    keyPickerSource.includes("event.shiftKey&&(active===first||!editModal.contains(active))") &&
    keyPickerSource.includes('const epoch=++editEpoch;') &&
    keyPickerSource.includes('if(epoch!==editEpoch||!editModal.classList.contains(\'show\'))return;') &&
    keyPickerSource.includes('editEpoch+=1;') &&
    keyPickerSource.includes('showEditModal(button)'));
  check('调用记录有明确的加载中、失败重试与无障碍忙碌状态',
    requestsSource.includes('正在加载调用记录…') &&
    requestsSource.includes('role="alert"') &&
    requestsSource.includes('onclick="loadRequests()"') &&
    requestsSource.includes("setAttribute('aria-busy', 'true')") &&
    requestsSource.includes("setAttribute('aria-busy', 'false')") &&
    appBundle.includes('\\u6b63\\u5728\\u52a0\\u8f7d\\u8c03\\u7528\\u8bb0\\u5f55\\u2026') &&
    indexHtml.includes('id="requestsTable" style="margin-top:12px" role="status" aria-live="polite" aria-busy="false"'));
  check('调用记录使用九列摘要并将密钥、请求 ID、模型、TTFT 与体积保留为次级信息',
    requestsSource.includes('<th>时间</th><th>客户端 / 密钥</th><th>请求 URL</th>') &&
    requestsSource.includes('密钥：') && requestsSource.includes('ID：') &&
    requestsSource.includes('模型：') && requestsSource.includes('TTFT：') &&
    requestsSource.includes('体积：'));
  check('调用记录长 URL 单行截断且固定列宽只在表格内部滚动',
    appCss.includes('#requests .table-wrap table{min-width:1262px;table-layout:fixed}') &&
    appCss.includes('#requests .req-url{white-space:nowrap;overflow:hidden;text-overflow:ellipsis}'));
  check('移动端导航规则禁止自动换行并把账号入口放在品牌同行',
    appCss.includes('flex-wrap:nowrap;overflow-x:auto;overflow-y:hidden') &&
    appCss.includes('.sidebar-bottom{grid-column:2;grid-row:1;margin:0;padding:0}'));
  check('Provider 高级配置分组与窄屏按钮样式已声明',
    appCss.includes('.provider-advanced-group>summary') &&
    appCss.includes('#providers .mm-toolbar{display:grid;grid-template-columns:repeat(2,minmax(0,1fr))') &&
    appCss.includes('#dashboard .backend-actions{display:grid;grid-template-columns:minmax(0,1fr)'));
  check('调用记录在手机端缩窄列宽并保留表格内部滚动',
    appCss.includes('#requests .table-wrap table{min-width:950px;table-layout:fixed}') &&
    appCss.includes('#requests .table-wrap th:nth-child(3),#requests .table-wrap td:nth-child(3){width:150px}'));
  check('辅助文字颜色已提升并有移动端首页紧凑布局规则',
    appCss.includes('--text-3:#8190a6;') &&
    appCss.includes('#dashboard .sys-dashboard .gauge-unit{') &&
    appCss.includes('grid-template-columns:minmax(0,128px) minmax(0,1fr)'));
  check('主要交互元素声明通用键盘焦点可见样式',
    appCss.includes('button:focus-visible,a[href]:focus-visible') &&
    appCss.includes('outline:2px solid var(--accent-2)') &&
    appCss.includes('outline-offset:2px'));
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

  // Provider 新增防重复回归：创建请求被 Mock 拦截，不会写入真实 Provider 配置。
  let providerCreatePostCount = 0;
  let failNextProviderListGet = false;
  await page.route('**/api/admin/providers', async route => {
    if (route.request().method() === 'POST') {
      providerCreatePostCount += 1;
      const payload = route.request().postDataJSON();
      await route.fulfill({
        status:200,
        contentType:'application/json',
        body:JSON.stringify({data:{id:payload.id}})
      });
      return;
    }
    if (route.request().method() === 'GET' && failNextProviderListGet) {
      failNextProviderListGet = false;
      await route.fulfill({
        status:503,
        contentType:'application/json',
        body:JSON.stringify({error:{message:'测试刷新失败'}})
      });
      return;
    }
    if (route.request().method() === 'GET') {
      await route.fulfill({
        status:200,
        contentType:'application/json',
        body:JSON.stringify({
          data:[{
            id:'deepseek', source:'deployment', editable:false, enabled:true,
            base_url:'https://api.deepseek.com', default_model:'deepseek-chat',
            api_key_configured:true, requests_per_minute:0, token_budget_per_day:0
          }],
          encryption_available:true
        })
      });
      return;
    }
    await route.continue();
  });

  // Provider 表单打开/取消只验证 UI；新增请求使用 Mock，不创建真实远程供应商。
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
    const limitSummary = await page.locator('#providerConcurrency').evaluate(el =>
      el.closest('details')?.querySelector('summary')?.textContent || '');
    const mappingSummary = await page.locator('#providerMappings').evaluate(el =>
      el.closest('details')?.querySelector('summary')?.textContent || '');
    const securitySummary = await page.locator('#providerAllowPrivate').evaluate(el =>
      el.closest('details')?.querySelector('summary')?.textContent || '');
    check('Provider 高级限流、模型映射与网络安全设置分组存在',
      limitSummary.includes('限流与资源控制') &&
      mappingSummary.includes('模型 ID 映射') &&
      securitySummary.includes('网络与安全'),
      {limitSummary,mappingSummary,securitySummary});
    check('Provider 首屏保留基础连接信息，启用状态仍可直接修改',
      await page.locator('#providerId').count() === 1 &&
      await page.locator('#providerBaseURL').count() === 1 &&
      await page.locator('#providerDefaultModel').count() === 1 &&
      await page.locator('#providerApiKey').count() === 1 &&
      await page.locator('#providerEnabled').count() === 1);
    await page.locator('#providerTemplate').selectOption('deepseek');
    check('DeepSeek 模板预填 ID、Base URL、模型及鉴权 Header',
      await page.locator('#providerId').inputValue() === 'deepseek' &&
      await page.locator('#providerBaseURL').inputValue() === 'https://api.deepseek.com' &&
      await page.locator('#providerDefaultModel').inputValue() === 'deepseek-chat' &&
      await page.locator('#providerApiKeyHeader').inputValue() === 'authorization');
    check('固定模板 ID 与已有部署 Provider 冲突时明确提示复用现有配置',
      (await page.locator('#providerTemplateHelp').textContent()).includes('Provider ID「deepseek」已存在'));
    await page.locator('#providerId').fill('deepseek-browser-test');
    check('修改为唯一 Provider ID 后自动清除重名提示',
      !(await page.locator('#providerTemplateHelp').textContent()).includes('Provider ID「'));
    await page.locator('#providerId').fill('deepseek');
    check('恢复已占用 Provider ID 后重新显示重名提示',
      (await page.locator('#providerTemplateHelp').textContent()).includes('Provider ID「deepseek」已存在'));
    check('厂商模板不会自动填写 API Key',
      await page.locator('#providerApiKey').inputValue() === '');
    await page.locator('#providerApiKey').fill('sk-template-switch-test');
    await page.locator('#providerTemplate').selectOption('openai');
    check('切换厂商且 Key 非空时先确认、不立即改动连接配置',
      await page.locator('#providerTemplateKeyConfirm').isVisible() &&
      await page.locator('#providerId').inputValue() === 'deepseek' &&
      await page.locator('#providerBaseURL').inputValue() === 'https://api.deepseek.com');
    await page.locator('#providerTemplateKeepKey').click();
    check('明确选择保留 Key 后才切换到新厂商',
      await page.locator('#providerId').inputValue() === 'openai' &&
      await page.locator('#providerBaseURL').inputValue() === 'https://api.openai.com/v1' &&
      await page.locator('#providerApiKey').inputValue() === 'sk-template-switch-test' &&
      await page.locator('#providerTemplateKeyConfirm').isHidden());
    await page.locator('#providerTemplate').selectOption('custom');
    check('自定义模板切换提供保留、清除和取消三种操作',
      await page.locator('#providerTemplateKeyConfirm').isVisible() &&
      await page.locator('#providerTemplateKeepKey').count() === 1 &&
      await page.locator('#providerTemplateClearKey').count() === 1 &&
      await page.locator('#providerTemplateCancel').count() === 1);
    await page.locator('#providerTemplateCancel').click();
    check('取消模板切换不会修改连接配置或清除 Key',
      await page.locator('#providerTemplate').inputValue() === 'openai' &&
      await page.locator('#providerId').inputValue() === 'openai' &&
      await page.locator('#providerApiKey').inputValue() === 'sk-template-switch-test');
    await page.locator('#providerTemplate').selectOption('custom');
    await page.locator('#providerTemplateClearKey').click();
    check('选择清除 Key 后切换自定义模板会清空 Key 与连接字段',
      await page.locator('#providerId').inputValue() === '' &&
      await page.locator('#providerBaseURL').inputValue() === '' &&
      await page.locator('#providerDefaultModel').inputValue() === '' &&
      await page.locator('#providerApiKey').inputValue() === '' &&
      await page.locator('#providerApiKeyHeader').inputValue() === 'authorization' &&
      (await page.locator('#providerTemplateHelp').textContent()).includes('自定义模板'));
    await page.locator('#providerCancel').click();
    check('远程 Provider 表单可取消', await page.locator('#providerEditorCard').isHidden());

    await page.locator('#providerCreate').click();
    await page.locator('#providerTemplate').selectOption('deepseek');
    await page.locator('#providerApiKey').fill('sk-mock-create-only');
    await page.waitForTimeout(80);
    check('选择固定模板只预填表单，不会自动创建 Provider', providerCreatePostCount === 0, providerCreatePostCount);
    await page.locator('#providerForm').evaluate(form => form.requestSubmit());
    await page.waitForTimeout(80);
    const duplicateCreateState = {
      postCount: providerCreatePostCount,
      editorHidden: await page.locator('#providerEditorCard').isHidden(),
      message: await page.locator('#providerEditorStatus').textContent()
    };
    check('新增同名部署 Provider 被阻止且不会发送 POST',
      duplicateCreateState.postCount === 0 &&
      !duplicateCreateState.editorHidden &&
      duplicateCreateState.message.includes('已存在') &&
      duplicateCreateState.message.includes('点击“配置”'),
      duplicateCreateState);
    await page.locator('#providerTemplate').selectOption('openai');
    await page.locator('#providerTemplateKeepKey').click();
    await page.locator('#providerForm').evaluate(form => {
      form.requestSubmit();
      form.requestSubmit();
    });
    await page.waitForFunction(() => document.getElementById('providerEditorCard')?.hidden, null, {timeout:10000});
    check('连续触发表单提交只发送一次新增 Provider 请求', providerCreatePostCount === 1, providerCreatePostCount);

    const providerRowsBeforeRefreshFailure = await page.locator('#providerRows').innerText();
    expectedProviderListRefreshFailure = true;
    failNextProviderListGet = true;
    await page.locator('#providerRefresh').click();
    await page.waitForFunction(() =>
      document.getElementById('providerStatus')?.textContent.includes('刷新失败'),
      null, {timeout:10000});
    const providerRefreshFailure = await page.evaluate(() => ({
      message: document.getElementById('providerStatus')?.textContent || '',
      busy: document.getElementById('providerStatus')?.getAttribute('aria-busy'),
      refreshDisabled: document.getElementById('providerRefresh')?.disabled
    }));
    check('Provider 刷新失败提示重试且保留上次数据', 
      providerRefreshFailure.message.includes('仍保留上次成功加载的数据') &&
      providerRefreshFailure.message.includes('点击“刷新”重试') &&
      await page.locator('#providerRows').innerText() === providerRowsBeforeRefreshFailure,
      providerRefreshFailure);
    check('Provider 刷新结束后清除忙碌状态并恢复刷新按钮',
      providerRefreshFailure.busy === 'false' &&
      providerRefreshFailure.refreshDisabled === false,
      providerRefreshFailure);
    check('Provider 刷新测试只注入并记录一次预期 503',
      expectedProviderListRefreshResponseCount === 1,
      { count: expectedProviderListRefreshResponseCount, failures: report.expectedProviderFailures });
    await page.unroute('**/api/admin/providers');
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
  const navLayout = await page.evaluate(() => {
    const nav = document.querySelector('.sidebar .nav');
    const brand = document.querySelector('.sidebar .brand')?.getBoundingClientRect();
    const account = document.querySelector('.sidebar .sidebar-account-btn')?.getBoundingClientRect();
    const navRect = nav?.getBoundingClientRect();
    return {
      navFlexWrap: nav ? getComputedStyle(nav).flexWrap : '',
      navHeight: navRect?.height ?? 0,
      navClientWidth: nav?.clientWidth ?? 0,
      navScrollWidth: nav?.scrollWidth ?? 0,
      brandTop: brand?.top ?? 0,
      accountTop: account?.top ?? 0,
      sidebarHeight: document.querySelector('.sidebar')?.getBoundingClientRect().height ?? 0
    };
  });
  check('窄屏导航单行横向滚动，账号入口与品牌同行',
    navLayout.navFlexWrap === 'nowrap' &&
    navLayout.navHeight <= 56 &&
    navLayout.navScrollWidth > navLayout.navClientWidth &&
    Math.abs(navLayout.accountTop - navLayout.brandTop) < 12 &&
    navLayout.sidebarHeight < 150,
    navLayout);
  await recordLayout(page, '首页-mobile-390');
  const compactGaugeLayout = await page.locator('#dashboard .gauge-unit').evaluateAll(units => units.map(unit => {
    const rect = unit.getBoundingClientRect();
    const svg = unit.querySelector('.gauge-svg')?.getBoundingClientRect();
    return { height: rect.height, width: rect.width, svgWidth: svg?.width ?? 0 };
  }));
  check('窄屏首页三项资源仪表盘已紧凑化，数值与仪表图均可见',
    compactGaugeLayout.length === 3 &&
    compactGaugeLayout.every(item => item.height < 180 && item.svgWidth > 0 && item.width > item.svgWidth + 20),
    compactGaugeLayout);
  const secondaryColor = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--text-3').trim());
  check('窄屏辅助文字使用增强后的对比度颜色', secondaryColor === '#8190a6', secondaryColor);
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
  const providerActionsLayout = await page.evaluate(() => {
    const buttons = [...document.querySelectorAll('#providers #providerCreate, #providers #providerRefresh')];
    return buttons.map(button => ({
      text: button.textContent.trim(),
      width: button.getBoundingClientRect().width,
      height: button.getBoundingClientRect().height,
      whiteSpace: getComputedStyle(button).whiteSpace,
      overflow: button.scrollWidth > button.clientWidth + 1
    }));
  });
  check('窄屏 Provider 顶部操作按钮保持单行且没有文字溢出',
    providerActionsLayout.length === 2 &&
    providerActionsLayout.every(button => button.whiteSpace === 'nowrap' && !button.overflow && button.width > button.height),
    providerActionsLayout);
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
  expectedProviderFailures: report.expectedProviderFailures,
  artifactDir
}, null, 2));

if (!report.passed) process.exitCode = 1;
