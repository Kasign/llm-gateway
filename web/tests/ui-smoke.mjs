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
  expectedSilentPollFailures: [],
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
let expectedSilentRequestPollFailure = false;
let expectedSilentRequestPollConsoleCount = 0;
let expectedSilentRequestPollResponseCount = 0;

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
    if (/Failed to load resource: the server responded with a status of 503 \(Service Unavailable\)/.test(messageText) &&
        expectedSilentRequestPollFailure && expectedSilentRequestPollConsoleCount === 0) {
      expectedSilentRequestPollConsoleCount += 1;
      report.expectedSilentPollFailures.push({ source: 'browser-console', message: messageText });
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
      if (response.status() === 503 && pathname === '/api/requests' &&
          expectedSilentRequestPollFailure && expectedSilentRequestPollResponseCount === 0) {
        expectedSilentRequestPollResponseCount += 1;
        report.expectedSilentPollFailures.push({ status: response.status(), url });
      } else if (response.status() === 503 && pathname === '/api/admin/providers' &&
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

  const researchComparisonFixture = {
    status:'completed', conclusion:'improved', comparability_level:'strict',
    changed_parameter:'threads', material_delta_threshold_percent:5,
    cache_groups:{cold:{
      baseline_sample_count:5,candidate_sample_count:5,
      metrics:{latency_ms:{
        baseline:{count:5,min:900,median:1000,p95:1100,max:1200},
        candidate:{count:5,min:800,median:900,p95:990,max:1000},
        median_delta_percent:-10,p95_delta_percent:-10,conclusion:'improved',direction:'lower_is_better'
      }}
    }},
    excluded_samples:[]
  };
  const researchExperimentFixture={
    experiment_id:'research-smoke-1',name:'线程数单变量比较',status:'completed',
    changed_parameter:'threads',baseline_value:4,candidate_value:6,
    planned_samples:5,baseline_job_ids:['baseline-job-1'],candidate_job_ids:['candidate-job-1'],
    report:{comparison:researchComparisonFixture}
  };
  let researchComparePostCount=0;
  await page.route('**/api/admin/research/experiments**',async route=>{
    const request=route.request(),url=new URL(request.url());
    if(url.pathname==='/api/admin/research/experiments'&&request.method()==='GET'){
      await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({items:[researchExperimentFixture],total:1})});return;
    }
    if(url.pathname.endsWith('/research-smoke-1')&&request.method()==='GET'){
      await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({experiment:researchExperimentFixture})});return;
    }
    if(url.pathname.endsWith('/research-smoke-1/compare')&&request.method()==='POST'){
      researchComparePostCount++;
      await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({comparison:researchComparisonFixture})});return;
    }
    await route.continue();
  });
  await clickTab(page,'research','研究');
  await page.locator('[data-research-open="research-smoke-1"]').click();
  await page.locator('#researchDetail').getByText('单变量比较结论').waitFor({state:'visible'});
  const researchDetailText=await page.locator('#researchDetail').innerText();
  check('研究详情展示结构化性能比较指标',
    researchDetailText.includes('总延迟（ms）')&&researchDetailText.includes('1000.00')&&researchDetailText.includes('改善'),
    researchDetailText);
  const researchCompareResponse=page.waitForResponse(response=>response.url().includes('/research-smoke-1/compare')&&response.request().method()==='POST');
  await page.locator('[data-research-compare="research-smoke-1"]').click();
  const researchCompareResult=await researchCompareResponse;
  await researchCompareResult.finished();
  check('研究比较操作使用 POST 并刷新报告',researchComparePostCount===1&&researchCompareResult.status()===200,{count:researchComparePostCount,status:researchCompareResult.status()});

  await page.route('**/api/benchmark**', async route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/benchmark' && route.request().method() === 'GET') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
        total: 1,
        items: [{
          job_id: 'benchmark-history-smoke-1',
          scenario: 'performance',
          model_id: 'smoke-model',
          status: 'completed',
          started_at: '2026-11-18T10:00:00Z',
          latency_ms: 1250,
          condition_status: 'complete'
        }]
      }) });
      return;
    }
    if (url.pathname === '/api/benchmark/benchmark-history-smoke-1' && route.request().method() === 'GET') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
        job: {
          job_id: 'benchmark-history-smoke-1',
          scenario: 'performance',
          model_id: 'smoke-model',
          status: 'completed',
          started_at: '2026-11-18T10:00:00Z',
          finished_at: '2026-11-18T10:00:01Z',
          latency_ms: 1250,
          input_tokens: 12,
          output_tokens: 34,
          content_text: '历史任务详情正文',
          condition_status: 'complete',
          condition_missing: []
        },
        analysis: {
          phases_ms: { total_ms: 1250, upstream_ms: 1100 },
          throughput: {
            output_tokens_per_sec: 27.2,
            llama_timings: {
              cache_n: 80, prompt_n: 20, predicted_n: 34, tokens_cached: 80,
              usage_cached_tokens: 75, tokens_evaluated: 20,
              prompt_ms: 100, prompt_per_token_ms: 5, prompt_per_second: 200,
              predicted_ms: 100, predicted_per_token_ms: 2.9412, predicted_per_second: 34,
              prompt_progress: [{ total: 100, cache: 80, processed: 90, time_ms: 500 }]
            }
          }
        },
        stream_events: []
      }) });
      return;
    }
    await route.continue();
  });

  await clickTab(page, 'model-test', '模型测试');
  const modelTestKey = page.locator('#modelTestApiKey');
  check('模型测试统一使用密码型客户端服务密钥',
    await modelTestKey.count() === 1 && await modelTestKey.evaluate(el => el.type === 'password'));
  check('模型测试显示五个横向测试类型 Tab',
    await page.locator('[data-model-test-tab]').count() === 5 &&
    await page.locator('#modelTestTabPerformance').getAttribute('aria-selected') === 'true');
  check('默认只显示性能测试面板',
    await page.locator('#modelTestPanelPerformance').isVisible() &&
    !(await page.locator('#modelTestPanelSuite').isVisible()) &&
    !(await page.locator('#modelTestPanelTools').isVisible()) &&
    !(await page.locator('#modelTestPanelAcceptance').isVisible()) &&
    !(await page.locator('#modelTestPanelHistory').isVisible()));
  check('未完成组续跑入口默认隐藏，且只在存在可续跑报告时显示',
    await page.locator('#benchSuiteResumeBtn').count() === 1 &&
    !(await page.locator('#benchSuiteResumeBtn').isVisible()));
  await page.evaluate(() => localStorage.setItem('llm-gateway.bench-suite.v1', JSON.stringify({
    suite_version:'perf12-four-case-v2',status:'partial',prompt_version:'perf12-synthetic-dataset-v2',
    prompts:{short:'short prompt',long:'long prompt'},warmup:{status:'completed'},
    cases:[{case_id:'S-P / S-O',status:'completed'},{case_id:'L-P / S-O',status:'cancelled'}]
  })));
  await clickTab(page, 'model-test', '模型测试');
  await page.locator('#modelTestTabSuite').click();
  check('部分完成报告在四组测试 Tab 显示续跑入口',
    await page.locator('#benchSuiteResumeBtn').isVisible());
  await page.locator('#modelTestTabPerformance').click();
  const duplicateRecovery = await page.evaluate(() => {
    const suite = {
      suite_version:'perf12-four-case-v2',status:'partial',prompt_version:'perf12-synthetic-dataset-v2',
      prompts:{short:'short prompt',long:'long prompt'},warmup:{status:'completed'},
      cases:[
        {case_id:'S-P / S-O',status:'running',job_id:'perf12-job-1'},
        {case_id:'S-P / S-O',status:'completed',job_id:'perf12-job-1',finished_at:'2026-11-20T10:00:00Z',job:{status:'completed'}}
      ]
    };
    localStorage.setItem('llm-gateway.bench-suite.v1',JSON.stringify(suite));
    const normalized=readBenchmarkSuiteState();
    const persisted=JSON.parse(localStorage.getItem('llm-gateway.bench-suite.v1'));
    return {
      count:normalized.cases.length,
      status:normalized.cases[0]?.status,
      jobId:normalized.cases[0]?.job_id,
      persistedCount:persisted.cases.length
    };
  });
  check('旧报告同一组 running/completed 重复记录自动合并，保留 completed 证据',
    duplicateRecovery.count===1&&duplicateRecovery.status==='completed'&&
    duplicateRecovery.jobId==='perf12-job-1'&&duplicateRecovery.persistedCount===1,
    duplicateRecovery);
  await page.evaluate(() => localStorage.removeItem('llm-gateway.bench-suite.v1'));
  await clickTab(page, 'model-test', '模型测试');
  const recoveryEligibility = await page.evaluate(() => {
    const base = {
      suite_version:'perf12-four-case-v2',
      prompt_version:'perf12-synthetic-dataset-v2',
      prompts:{short:'short prompt',long:'long prompt'},
      warmup:{status:'completed'},
      cases:[
        {case_id:'S-P / S-O',status:'completed'},
        {case_id:'L-P / S-O',status:'running',job_id:'job-inflight-123'}
      ]
    };
    const runningJob = benchmarkSuiteCanResume({...base,status:'running'});
    const missingJob = benchmarkSuiteCanResume({...base,status:'running',cases:[
      {case_id:'S-P / S-O',status:'completed'},
      {case_id:'L-P / S-O',status:'submitting',job_id:null}
    ]});
    const runningWarmupSuite = {
      ...base,status:'running',warmup:{status:'running',job_id:'warmup-inflight-456'},cases:[]
    };
    const runningWarmup = benchmarkSuiteCanResume(runningWarmupSuite);
    const warmupGateAllowsRunning = benchmarkSuiteWarmupCanResume(runningWarmupSuite);
    const warmupGateRejectsUnknown = !benchmarkSuiteWarmupCanResume({...runningWarmupSuite,warmup:{status:'unknown',job_id:'warmup-inflight-456'}});
    const warmupCanRetryCancelled = benchmarkSuiteWarmupCanResume({...runningWarmupSuite,warmup:{status:'cancelled',job_id:'warmup-cancelled-789'}});
    const warmupCanRetryFailed = benchmarkSuiteWarmupCanResume({...runningWarmupSuite,warmup:{status:'failed',job_id:null}});
    return {runningJob,missingJob,runningWarmup,warmupGateAllowsRunning,warmupGateRejectsUnknown,warmupCanRetryCancelled,warmupCanRetryFailed};
  });
  const recoveredTaskStates = await page.evaluate(() => ({
    transientPollFailure: benchmarkSuiteTaskStatus(null,'job-inflight-123',false),
    explicitCancellation: benchmarkSuiteTaskStatus(null,'job-inflight-123',true),
    submitFailureWithoutJob: benchmarkSuiteTaskStatus(null,null,false),
    terminalCompletion: benchmarkSuiteTaskStatus('completed','job-inflight-123',false)
  }));
  check('轮询异常保留原 Job 并暂停后续组，只有明确取消才标记 cancelled',
    recoveredTaskStates.transientPollFailure === 'running' &&
    recoveredTaskStates.explicitCancellation === 'cancelled' &&
    recoveredTaskStates.submitFailureWithoutJob === 'failed' &&
    recoveredTaskStates.terminalCompletion === 'completed',
    recoveredTaskStates);
  check('刷新后可恢复已有 Job ID 的在途组，不会自动重提不确定的任务',
    recoveryEligibility.runningJob === true &&
    recoveryEligibility.missingJob === false &&
    recoveryEligibility.runningWarmup === true &&
    recoveryEligibility.warmupGateAllowsRunning === true &&
    recoveryEligibility.warmupGateRejectsUnknown === true &&
    recoveryEligibility.warmupCanRetryCancelled === true &&
    recoveryEligibility.warmupCanRetryFailed === true,
    recoveryEligibility);
  check('清除部分报告后隐藏续跑入口',
    !(await page.locator('#benchSuiteResumeBtn').isVisible()));
  check('性能测试区只保留普通性能测试入口',
    await page.locator('#benchSuiteRunBtn').count() === 1 &&
    await page.locator('#benchRunBtn').count() === 1 &&
    await page.locator('#benchSuiteReport').count() === 1 &&
    !(await page.locator('#benchSuiteRunBtn').isVisible()) &&
    !(await page.locator('#benchToolRunBtn').isVisible()));
  check('普通性能测试提供 1～20 次重复采样，默认 10 次',
    await page.locator('#benchRepeatCount').inputValue() === '10' &&
    await page.locator('#benchRepeatCount').getAttribute('min') === '1' &&
    await page.locator('#benchRepeatCount').getAttribute('max') === '20' &&
    await page.locator('#benchRepeatRunBtn').isVisible() &&
    await page.locator('#benchRepeatReport').isVisible());
  await page.locator('#benchRepeatCount').fill('0');
  await page.locator('#benchRepeatRunBtn').click();
  check('重复采样拒绝超出范围的次数且不提交任务',
    (await page.locator('#benchRepeatStatus').textContent()).includes('1～20') &&
    !(await page.locator('#benchStopBtn').isEnabled()));
  await page.locator('#benchRepeatCount').fill('10');
  const repeatReportSmoke = await page.evaluate(() => {
    const key='llm-gateway.bench-repeat.v1';
    const previous=localStorage.getItem(key);
    localStorage.setItem(key,JSON.stringify({
      version:1,status:'completed',requested_count:1,
      started_at:'2026-11-23T00:00:00Z',finished_at:'2026-11-23T00:00:01Z',
      samples:[{index:1,job_id:'benchmark-repeat-smoke-job',status:'completed',
        condition_status:'complete',condition_sha256:'smoke-condition',
        model:'smoke-model',cache_state:'cold',latency_ms:1250,ttft_ms:300,
        input_tokens:12,output_tokens:34,output_tokens_per_sec:27.2}]
    }));
    renderBenchmarkRepeatReport();
    const result={
      reportVisible:(document.getElementById('benchRepeatReport')?.textContent||'').includes('benchmark-repeat-smoke-job'),
      copyEnabled:!document.getElementById('benchRepeatCopyBtn')?.disabled,
      noPromptStored:!localStorage.getItem(key).includes('Write a small')
    };
    if(previous===null)localStorage.removeItem(key);else localStorage.setItem(key,previous);
    renderBenchmarkRepeatReport();
    return result;
  });
  check('重复采样报告展示独立 Job ID 并提供 Markdown 复制，且不持久化提示词',
    repeatReportSmoke.reportVisible&&repeatReportSmoke.copyEnabled&&repeatReportSmoke.noPromptStored,
    repeatReportSmoke);
  await page.locator('#modelTestTabSuite').click();
  check('一键四组测试独立 Tab 与工具、验收并列',
    await page.locator('#modelTestPanelSuite').isVisible() &&
    await page.locator('#benchSuiteRunBtn').isVisible() &&
    await page.locator('#benchSuiteReport').isVisible() &&
    !(await page.locator('#modelTestPanelPerformance').isVisible()) &&
    !(await page.locator('#modelTestPanelTools').isVisible()));
  const suitePrompts = await page.evaluate(() => {
    const prompts = buildBenchmarkSuitePrompts();
    const shortPrefix = prompts.short.split('\n\n合成记录：')[0];
    const longPrefix = prompts.long.split('\n\n合成记录（共50条）：')[0];
    const first = benchmarkSuitePromptForCase(prompts.short,'SP_SO');
    const second = benchmarkSuitePromptForCase(prompts.short,'SP_LO');
    const firstNonce = first.split('_PERF12_CASE_')[0];
    const secondNonce = second.split('_PERF12_CASE_')[0];
    return {
      version: prompts.version,
      shortChars: prompts.short.length,
      longChars: prompts.long.length,
      sameTask: shortPrefix === longPrefix,
      shortRecordCount: (prompts.short.match(/^R\d{3} /gm) || []).length,
      recordCount: (prompts.long.match(/^R\d{3} /gm) || []).length,
      casePromptsDiffer: first !== second && firstNonce.length===24 && secondNonce.length===24 && firstNonce!==secondNonce && first.startsWith(firstNonce+'_PERF12_CASE_SP_SO') && second.startsWith(secondNonce+'_PERF12_CASE_SP_LO')
    };
  });
  const qualification = await page.evaluate(() => {
    const ids = ['S-P / S-O','L-P / S-O','S-P / L-O','L-P / L-O'];
    const suite = {
      resume_parameter_status:'complete_snapshot',
      model_id:'Qwen3.5-4B-IQ4_NL',backend:'llamacpp',
      cases:ids.map(case_id => ({
        case_id,status:'completed',
        job:{
          condition_status:'partial',condition_missing:['model_sha256_unknown','gateway_revision_unknown'],
          input_tokens:128,output_tokens:64,
          condition_snapshot:{backend:'llamacpp',model_id:'Qwen3.5-4B-IQ4_NL',model_load_state_before:'loaded'}
        },
        analysis:{throughput:{llama_timings:{cache_n:0,prompt_n:128,predicted_n:64,prompt_ms:100,predicted_ms:200}}},
        validation:['输入目标 达标（128 tokens）','输出目标 达标（64 tokens）']
      }))
    };
    const partialEnvironment = benchmarkSuiteQualification(suite);
    const legacyReportRecalculation = benchmarkSuiteQualification(suite);
    suite.cases[0].job.condition_snapshot.model_load_state_before='unknown';
    const missingLoadEvidence = benchmarkSuiteQualification(suite);
    const cacheStates = [
      benchmarkCacheClassification({cache_n:0,prompt_n:128},128),
      benchmarkCacheClassification({cache_n:7,prompt_n:4},11),
      benchmarkCacheClassification({cache_n:11,prompt_n:0},11),
      benchmarkCacheClassification({cache_n:7,prompt_n:3},11)
    ];
    return {partialEnvironment,legacyReportRecalculation,missingLoadEvidence,cacheStates};
  });
  check('环境复现信息缺失不否决有效性能基线，历史报告可按原始 timings 重算，测试前加载状态缺失仍阻止晋级',
    qualification.partialEnvironment === 'baseline_candidate' &&
    qualification.legacyReportRecalculation === 'baseline_candidate' &&
    qualification.missingLoadEvidence === 'baseline_not_qualified' &&
    JSON.stringify(qualification.cacheStates)===JSON.stringify(['cold','partial','full','inconsistent']),
    qualification);
  check('四组测试使用同一分析任务和固定版本提示词',
    suitePrompts.version === 'perf12-synthetic-dataset-v2' && suitePrompts.sameTask);
  check('短长提示词分别使用1条与50条固定合成记录，并为每组加入独立缓存隔离标记',
    suitePrompts.shortRecordCount === 1 && suitePrompts.recordCount === 50 && suitePrompts.casePromptsDiffer,
    suitePrompts);
  await page.locator('#benchSuiteRunBtn').click();
  check('四组测试缺少客户端 Key 时在本地阻断',
    (await page.locator('#benchSuiteStatus').textContent()).includes('请先输入客户端服务密钥'));

  await page.locator('#modelTestTabTools').click();
  check('工具测试 Tab 独立显示工具测试面板',
    await page.locator('#modelTestPanelTools').isVisible() &&
    !(await page.locator('#modelTestPanelPerformance').isVisible()));
  check('工具调用性能测试与普通性能结果区分离',
    await page.locator('#benchToolRunBtn').count() === 1 &&
    await page.locator('#toolBenchAnswer').count() === 1 &&
    await page.locator('#toolBenchMetrics').count() === 1 &&
    await page.locator('#toolTestAnswer').count() === 1);
  await page.locator('#toolTestRunBtn').click();
  check('工具链路功能测试缺少客户端 Key 时本地拒绝',
    (await page.locator('#toolTestStatus').textContent()).includes('请先输入'));
  await page.locator('#benchToolRunBtn').click();
  check('工具调用性能缺少客户端 Key 时写入工具专属回复区',
    (await page.locator('#toolBenchAnswer').textContent()).includes('请先在模型测试页面输入客户端服务密钥'));
  check('工具测试不会污染性能测试回复区',
    !(await page.locator('#benchAnswer').textContent()).includes('请先在模型测试页面输入客户端服务密钥'));

  await page.locator('#modelTestTabAcceptance').click();
  await page.waitForFunction(() => document.getElementById('benchAcceptanceCase')?.value === 'TM-01', null, { timeout: 5000 });
  check('验收测试 Tab 独立显示项目验收表单',
    await page.locator('#modelTestPanelAcceptance').isVisible() &&
    await page.locator('#benchAcceptanceCase').count() === 1 &&
    await page.locator('#acceptanceBenchAnswer').count() === 1);
  await page.locator('#benchAcceptanceRunBtn').click();
  check('验收测试缺少客户端 Key 时写入验收专属结果区',
    (await page.locator('#acceptanceBenchAnswer').textContent()).includes('请先在模型测试页面输入客户端服务密钥'));

  await page.locator('#modelTestTabHistory').click();
  check('测试历史独立显示 Benchmark 历史任务与比较区域',
    await page.locator('#modelTestPanelHistory').isVisible() &&
    await page.locator('#benchHistoryCard').isVisible() &&
    await page.locator('#benchHistoryDetail').count() === 1 &&
    await page.locator('#benchHistoryCompareBtn').count() === 1 &&
    await page.locator('#benchCompareResult').count() === 1);
  await page.locator('#benchHistoryList [data-bench-history-id="benchmark-history-smoke-1"]').click();
  await page.locator('#benchHistoryDetail').waitFor({ state: 'visible', timeout: 5000 });
  await page.waitForFunction(() => {
    const detail = document.getElementById('benchHistoryDetail')?.textContent || '';
    return detail.includes('历史任务详情正文') && detail.includes('smoke-model');
  }, null, { timeout: 5000 });
  check('历史任务详情在测试历史 Tab 内展示',
    (await page.locator('#benchHistoryDetail').textContent()).includes('历史任务详情正文') &&
    (await page.locator('#benchHistoryDetail').textContent()).includes('smoke-model'));
  check('历史详情提供复制分析数据按钮',
    await page.locator('#benchHistoryDetail [data-bench-history-copy]').isVisible());
  const copiedAnalysis = await page.evaluate(() => benchmarkHistoryAnalysisPayload({
    job:{job_id:'job-copy',request_id:'req-copy',scenario:'performance',status:'completed',model_id:'model-copy',
      latency_ms:1200,input_tokens:100,output_tokens:40,response:'RAW_RESPONSE_MUST_NOT_COPY',
      content_text:'MODEL_CONTENT_MUST_NOT_COPY',condition_status:'complete',condition_sha256:'fingerprint',
      condition_snapshot:{model_identity:{model_key:'model-key',source_type:'hf',sha256:'abc123'},model_load_state_before:'loaded'}},
    analysis:{phases_ms:{upstream_ms:900},throughput:{output_tokens_per_sec:33.3,llama_timings:{prompt_n:100,prompt_ms:200,predicted_n:40,predicted_ms:800}}}
  }));
  check('历史复制数据包含分析指标和模型身份，不包含正文或原始响应',
    copiedAnalysis.task.job_id==='job-copy' &&
    copiedAnalysis.model_identity.sha256==='abc123' &&
    copiedAnalysis.llama_timings.prompt_ms===200 &&
    !JSON.stringify(copiedAnalysis).includes('RAW_RESPONSE_MUST_NOT_COPY') &&
    !JSON.stringify(copiedAnalysis).includes('MODEL_CONTENT_MUST_NOT_COPY'),
    copiedAnalysis);
  const benchmarkTimingDetail = await page.locator('#benchHistoryDetail').textContent();
  check('Benchmark 历史详情展示缓存、每 Token timing 与 Prompt progress',
    benchmarkTimingDetail.includes('模型原生 Timings') &&
    benchmarkTimingDetail.includes('Prompt Eval 每 Token') &&
    benchmarkTimingDetail.includes('Decode 每 Token') &&
    benchmarkTimingDetail.includes('Prompt progress') &&
    benchmarkTimingDetail.includes('50.0%'));

  await page.locator('#modelTestTabPerformance').click();
  check('横向 Tab 可返回性能测试，且历史区域不再混排',
    await page.locator('#modelTestPanelPerformance').isVisible() &&
    !(await page.locator('#benchHistoryCard').isVisible()));
  await modelTestKey.fill('sk-browser-smoke-not-a-real-secret');
  check('模型测试密钥只保留在页面内存，不写入浏览器存储',
    await page.evaluate(() => localStorage.getItem('llm_gateway_model_test_key')) === null &&
    await page.evaluate(() => sessionStorage.getItem('llm_gateway_model_test_key')) === null);
  await page.locator('#modelTestClearKeyBtn').click();
  check('统一入口可以主动清除客户端密钥', await modelTestKey.inputValue() === '');

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
  await page.locator('#keyToolsList input[value="web.search"]').waitFor({ state: 'visible', timeout: 15000 });
  check('工具授权目录只显示服务端已注册的 web.search', await page.locator('#keyToolsList input[value="web.search"]').count() === 1);
  await page.locator('#keyToolsList input[value="web.search"]').check();
  check('创建表单选择工具后同步独立授权字段', await page.locator('#keyTools').inputValue() === 'web.search');
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
  check('真实服务密钥接口保存服务和工具两套独立权限', scopedCreateResponse.status() === 200 &&
    JSON.stringify(scopedKey?.api_key?.allowed_models || []) === JSON.stringify(['llamacpp']) &&
    JSON.stringify(scopedKey?.api_key?.allowed_tools || []) === JSON.stringify(['web.search']),
    {status:scopedCreateResponse.status(),allowed_models:scopedKey?.api_key?.allowed_models||null,allowed_tools:scopedKey?.api_key?.allowed_tools||null});
  await page.waitForFunction(() => document.getElementById('newKeyBox')?.style.display === 'block', null, {timeout:10000});

  if(scopedKey?.api_key?.id){
    const aclSemantics = await page.evaluate(async({key,id}) => {
      const headers={'X-Admin-Key':key,'Content-Type':'application/json'};
      const get=async()=>{const r=await fetch('/api/admin/keys',{headers:{'X-Admin-Key':key},cache:'no-store'});const b=await r.json();return (b.data||[]).find(x=>Number(x.id)===Number(id))||null};
      const put=async(body)=>{const r=await fetch('/api/admin/keys/'+id,{method:'PUT',headers,body:JSON.stringify(body)});return r.status};
      const initial=await get();
      const common={name:initial.name,rpm:initial.rpm,allowed_models:initial.allowed_models,expires_at:null};
      const nullStatus=await put({...common,allowed_tools:null});
      const afterNull=await get();
      const omittedStatus=await put(common);
      const afterOmitted=await get();
      const clearStatus=await put({...common,allowed_tools:[]});
      const afterClear=await get();
      const restoreStatus=await put({...common,allowed_tools:['web.search']});
      const afterRestore=await get();
      const rotate=await fetch('/api/admin/keys/'+id+'/rotate',{method:'POST',headers:{'X-Admin-Key':key}});
      const afterRotate=await get();
      return {nullStatus,omittedStatus,clearStatus,restoreStatus,rotateStatus:rotate.status,
        afterNull:afterNull?.allowed_tools,afterOmitted:afterOmitted?.allowed_tools,
        afterClear:afterClear?.allowed_tools,afterRestore:afterRestore?.allowed_tools,afterRotate:afterRotate?.allowed_tools};
    },{key:adminKey,id:scopedKey.api_key.id});
    check('更新时 null 和省略字段保留工具授权、空数组清空授权',aclSemantics.nullStatus===200&&aclSemantics.omittedStatus===200&&
      aclSemantics.clearStatus===200&&aclSemantics.restoreStatus===200&&
      JSON.stringify(aclSemantics.afterNull||[])===JSON.stringify(['web.search'])&&
      JSON.stringify(aclSemantics.afterOmitted||[])===JSON.stringify(['web.search'])&&
      JSON.stringify(aclSemantics.afterClear||[])===JSON.stringify([])&&
      JSON.stringify(aclSemantics.afterRestore||[])===JSON.stringify(['web.search']),aclSemantics);
    check('Key 轮换保留原工具授权',aclSemantics.rotateStatus===200&&
      JSON.stringify(aclSemantics.afterRotate||[])===JSON.stringify(['web.search']),aclSemantics);

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
    check('编辑弹窗会恢复当前工具授权', await page.locator('#keyEditToolsList input[value="web.search"]').isChecked());
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
    check('保存后服务范围、工具授权与 RPM 已写入后端',editedKey?.name==='browser-smoke-service-scope-edited' &&
      Number(editedKey?.rpm)===120 && JSON.stringify(editedKey?.allowed_models||[])===JSON.stringify(['ollama']) &&
      JSON.stringify(editedKey?.allowed_tools||[])===JSON.stringify(['web.search']),
      {name:editedKey?.name,rpm:editedKey?.rpm,allowed_models:editedKey?.allowed_models,allowed_tools:editedKey?.allowed_tools});

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
  const mainSource = fs.readFileSync(path.resolve('web/js/app/main.js'), 'utf8');
  const providersSource = fs.readFileSync(path.resolve('web/js/providers-entry.js'), 'utf8');
  const keyPickerSource = fs.readFileSync(path.resolve('web/js/key-service-picker.js'), 'utf8');
  const keyPickerBundle = fs.readFileSync(path.resolve('web/static/dist/key-service-picker.js'), 'utf8');
  const indexHtml = fs.readFileSync(path.resolve('web/index.html'), 'utf8');
  const appCss = fs.readFileSync(path.resolve('web/static/css/app.css'), 'utf8');
  const modelDownloadCss = fs.readFileSync(path.resolve('web/static/css/model-download.css'), 'utf8');
  // 静态 CSS 的主题变量必须有定义；跨模块引用允许显式 fallback。
  const cssSources = [appCss, modelDownloadCss, providersSource];
  const declaredThemeVariables = new Set(
    cssSources.flatMap(source => [...source.matchAll(/--([a-z][\w-]*)\s*:/gi)].map(match => match[1]))
  );
  const unresolvedThemeVariables = [];
  for (const source of cssSources) {
    for (const match of source.matchAll(/var\(\s*--([a-z][\w-]*)([^)]*)\)/gi)) {
      if (declaredThemeVariables.has(match[1]) || match[2].trimStart().startsWith(',')) continue;
      unresolvedThemeVariables.push(match[1]);
    }
  }
  check('静态样式引用的主题变量均已定义或提供 fallback',
    unresolvedThemeVariables.length === 0,
    [...new Set(unresolvedThemeVariables)]);

  const placeholderContrastAudit = await page.evaluate(() => {
    const host = document.createElement('div');
    host.className = 'inference-params-root';
    host.style.cssText = 'position:fixed;left:-10000px;top:0;visibility:hidden';
    const field = document.createElement('div');
    field.className = 'param-control';
    const input = document.createElement('input');
    input.placeholder = '示例占位文字';
    field.appendChild(input);
    host.appendChild(field);
    document.body.appendChild(host);
    const pseudo = getComputedStyle(input, '::placeholder');
    const foreground = pseudo.color;
    const opacity = Number(pseudo.opacity || 1);
    const background = getComputedStyle(input).backgroundColor;
    const channels = value => (value.match(/[\d.]+/g) || []).slice(0, 4).map(Number);
    const fg = channels(foreground), bg = channels(background);
    if (fg.length < 3 || bg.length < 3) {
      host.remove();
      return { foreground, opacity, background, ratio: Number.NaN };
    }
    const effective = fg.slice(0, 3).map((v, i) => v * opacity + bg[i] * (1 - opacity));
    const luminance = values => {
      const linear = values.map(value => {
        const channel = value / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
    };
    const values = [luminance(effective), luminance(bg.slice(0, 3))].sort((a, b) => b - a);
    const result = { foreground, opacity, background, effectiveForeground: effective.map(Math.round), ratio: Number(((values[0] + 0.05) / (values[1] + 0.05)).toFixed(2)) };
    host.remove();
    return result;
  });
  check('模型参数输入框占位文字在半透明合成后达到 WCAG AA（≥4.5:1）',
    modelDownloadCss.includes('::placeholder{color:var(--text-3);opacity:1}') &&
    Number.isFinite(placeholderContrastAudit.ratio) && placeholderContrastAudit.ratio >= 4.5,
    placeholderContrastAudit);

  const infoChipContrastAudit = await page.evaluate(() => {
    const host = document.createElement('div');
    host.style.cssText = 'position:fixed;left:-10000px;top:0;visibility:hidden;background:var(--surface-2)';
    const card = document.createElement('div');
    card.className = 'inf-card';
    const chip = document.createElement('span');
    chip.className = 'inf-chip';
    const suffix = document.createElement('em');
    suffix.textContent = '来源';
    chip.appendChild(suffix);
    card.appendChild(chip);
    host.appendChild(card);
    document.body.appendChild(host);
    const channels = value => (value.match(/[\d.]+/g) || []).slice(0, 4).map(Number);
    const surface = channels(getComputedStyle(host).backgroundColor);
    const cardBackground = channels(getComputedStyle(card).backgroundColor);
    const cardImage = getComputedStyle(card).backgroundImage;
    const gradientMatch = cardImage.match(/rgba\(\s*(\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\s*\)/i);
    const chipBackground = channels(getComputedStyle(chip).backgroundColor);
    const textStyle = getComputedStyle(suffix);
    const foreground = channels(textStyle.color);
    const textOpacity = Number(textStyle.opacity || 1);
    if (surface.length < 3 || cardBackground.length < 4 || chipBackground.length < 4 || foreground.length < 3 || !gradientMatch) {
      host.remove();
      return { cardImage, textColor: textStyle.color, ratio: Number.NaN };
    }
    const blend = (top, bottom, alpha) => top.slice(0, 3).map((value, i) => value * alpha + bottom[i] * (1 - alpha));
    const cardComposite = blend(cardBackground, surface, cardBackground[3]);
    const gradientColor = gradientMatch.slice(1, 4).map(Number);
    const gradientOpacity = Number(gradientMatch[4]);
    const gradientComposite = blend(gradientColor, cardComposite, gradientOpacity);
    const chipComposite = blend(chipBackground, gradientComposite, chipBackground[3]);
    const effectiveForeground = blend(foreground, chipComposite, textOpacity);
    const luminance = values => {
      const linear = values.map(value => {
        const channel = value / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
    };
    const values = [luminance(effectiveForeground), luminance(chipComposite)].sort((a, b) => b - a);
    const result = { textColor: textStyle.color, textOpacity, surface, cardBackground, gradientOpacity, chipBackground, effectiveForeground: effectiveForeground.map(Math.round), compositeBackground: chipComposite.map(Math.round), ratio: Number(((values[0] + 0.05) / (values[1] + 0.05)).toFixed(2)) };
    host.remove();
    return result;
  });
  check('推理信息标签次级文字在渐变与半透明叠色后达到 WCAG AA（≥4.5:1）',
    appCss.includes('.inf-chip em{font-style:normal;font-size:10px;font-weight:650;color:var(--text-2);margin-left:4px}') &&
    Number.isFinite(infoChipContrastAudit.ratio) && infoChipContrastAudit.ratio >= 4.5,
    infoChipContrastAudit);

  const runtimeMetricContrastAudit = await page.evaluate(() => {
    const host = document.createElement('div');
    host.className = 'runtime-hero runtime-hero-live';
    host.style.cssText = 'position:fixed;left:-10000px;top:0;visibility:hidden';
    const label = document.createElement('span');
    label.className = 'runtime-metric-label';
    label.textContent = '运行时指标';
    const sub = document.createElement('span');
    sub.className = 'runtime-metric-sub';
    sub.textContent = '附加说明';
    host.append(label, sub);
    document.body.appendChild(host);
    const channels = value => (value.match(/[\d.]+/g) || []).slice(0, 4).map(Number);
    const surface = channels(getComputedStyle(host).backgroundColor);
    const bgImage = getComputedStyle(host).backgroundImage;
    const gradient = bgImage.match(/rgba\(\s*(\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\s*\)/i);
    if (surface.length < 3 || !gradient) {
      host.remove();
      return { surface, bgImage, labelRatio: Number.NaN, subRatio: Number.NaN };
    }
    const base = surface.slice(0, 3);
    const color = gradient.slice(1, 4).map(Number);
    const alpha = Number(gradient[4]);
    const composite = color.map((value, i) => value * alpha + base[i] * (1 - alpha));
    const luminance = values => {
      const linear = values.map(value => {
        const channel = value / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
    };
    const calculate = element => {
      const style = getComputedStyle(element);
      const foreground = channels(style.color);
      const opacity = Number(style.opacity || 1);
      if (foreground.length < 3) return { color: style.color, opacity, ratio: Number.NaN };
      const effective = foreground.slice(0, 3).map((value, i) => value * opacity + composite[i] * (1 - opacity));
      const values = [luminance(effective), luminance(composite)].sort((a, b) => b - a);
      return { color: style.color, opacity, effectiveForeground: effective.map(Math.round), ratio: Number(((values[0] + 0.05) / (values[1] + 0.05)).toFixed(2)) };
    };
    const result = { background: base, gradientColor: color, gradientOpacity: alpha, label: calculate(label), sub: calculate(sub) };
    host.remove();
    return result;
  });
  check('运行态 Hero 次级指标在最亮渐变端达到 WCAG AA（≥4.5:1）',
    appCss.includes('.runtime-metric-label{font-size:11px;font-weight:600;color:var(--text-2);letter-spacing:.04em}') &&
    appCss.includes('.runtime-metric-sub{font-size:11px;color:var(--text-2);line-height:1.35;max-width:12rem}') &&
    Number.isFinite(runtimeMetricContrastAudit.label.ratio) && runtimeMetricContrastAudit.label.ratio >= 4.5 &&
    Number.isFinite(runtimeMetricContrastAudit.sub.ratio) && runtimeMetricContrastAudit.sub.ratio >= 4.5,
    runtimeMetricContrastAudit);

  const configNoteContrastAudit = await page.evaluate(() => {
    const host = document.createElement('div');
    host.className = 'inference-params-root';
    host.style.cssText = 'position:fixed;left:-10000px;top:0;visibility:hidden';
    const card = document.createElement('div');
    card.className = 'param-field-card';
    const note = document.createElement('p');
    note.className = 'param-input-note from-config';
    note.textContent = '当前值来自配置';
    card.appendChild(note);
    host.appendChild(card);
    document.body.appendChild(host);
    const style = getComputedStyle(note);
    const foreground = style.color;
    const opacity = Number(style.opacity || 1);
    const background = getComputedStyle(card).backgroundColor;
    const channels = value => (value.match(/[\d.]+/g) || []).slice(0, 4).map(Number);
    const fg = channels(foreground), bg = channels(background);
    if (fg.length < 3 || bg.length < 3) {
      host.remove();
      return { foreground, opacity, background, ratio: Number.NaN };
    }
    const effective = fg.slice(0, 3).map((v, i) => v * opacity + bg[i] * (1 - opacity));
    const luminance = values => {
      const linear = values.map(value => {
        const channel = value / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
    };
    const values = [luminance(effective), luminance(bg.slice(0, 3))].sort((a, b) => b - a);
    const result = { foreground, opacity, background, effectiveForeground: effective.map(Math.round), ratio: Number(((values[0] + 0.05) / (values[1] + 0.05)).toFixed(2)) };
    host.remove();
    return result;
  });
  check('来自配置的参数提示文字在深色卡片上达到 WCAG AA（≥4.5:1）',
    modelDownloadCss.includes('.inference-params-root .param-input-note.from-config{color:#a5b4fc;opacity:1}') &&
    Number.isFinite(configNoteContrastAudit.ratio) && configNoteContrastAudit.ratio >= 4.5,
    configNoteContrastAudit);


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
  const idleBadgeContrast = await page.evaluate(() => {
    const host = document.createElement('div');
    host.style.cssText = 'position:fixed;left:-10000px;top:0;visibility:hidden;background:var(--surface-2)';
    const badge = document.createElement('span');
    badge.className = 'pill pill-idle';
    badge.textContent = '未加载';
    host.appendChild(badge);
    document.body.appendChild(host);

    const parseRgb = value => (value.match(/[\d.]+/g) || []).slice(0, 4).map(Number);
    const foreground = getComputedStyle(badge).color;
    const background = getComputedStyle(badge).backgroundColor;
    const surface = getComputedStyle(host).backgroundColor;
    const fg = parseRgb(foreground);
    const bg = parseRgb(background);
    const base = parseRgb(surface);
    if (fg.length < 3 || bg.length < 3 || base.length < 3) {
      host.remove();
      return { foreground, background, surface, ratio: Number.NaN };
    }
    const alpha = bg[3] ?? 1;
    const composite = bg.slice(0, 3).map((channel, index) => channel * alpha + base[index] * (1 - alpha));
    const luminance = channels => {
      const linear = channels.map(channel => {
        const srgb = channel / 255;
        return srgb <= 0.04045 ? srgb / 12.92 : ((srgb + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
    };
    const values = [luminance(fg), luminance(composite)].sort((a, b) => b - a);
    const result = {
      foreground,
      background,
      surface,
      composite: composite.map(Math.round),
      ratio: Number(((values[0] + 0.05) / (values[1] + 0.05)).toFixed(2))
    };
    host.remove();
    return result;
  });
  check('空闲状态徽标在较亮的深色表面达到 WCAG AA（≥4.5:1）',
    appCss.includes('.pill-idle{background:rgba(148,163,184,.1);color:var(--text-2)}') &&
    Number.isFinite(idleBadgeContrast.ratio) &&
    idleBadgeContrast.ratio >= 4.5,
    idleBadgeContrast);

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
    requestsSource.includes("const closedDetails = el.closest('details:not([open])')") &&
    requestsSource.includes("!closedDetails || el.matches('details:not([open]) > summary')") &&
    appBundle.includes('details:not([open]) > summary') &&
    appBundle.includes('detailTrigger = document.activeElement') &&
    appBundle.includes('event.key === "Escape"'));
  check('性能测试结果展示条件快照完整性、缺失维度与指纹，兼容旧任务未知状态',
    mainSource.includes('function renderBenchCondition(j)') &&
    mainSource.includes('model_load_state_unknown') &&
    mainSource.includes('condition_missing') &&
    appBundle.includes('function renderBenchCondition(j)') &&
    appBundle.includes('condition_sha256'));
  check('性能测试参数抽屉具备焦点进入、Escape 关闭、焦点循环和返回触发按钮',
    indexHtml.includes('id="benchParamsDrawer" class="drawer-root" aria-hidden="true" hidden') &&
    mainSource.includes('function benchDrawerFocusable(root)') &&
    mainSource.includes("const closedDetails=el.closest('details:not([open])')") &&
    mainSource.includes("!closedDetails||el.matches('details:not([open]) > summary')") &&
    mainSource.includes('benchDrawerTrigger=document.activeElement') &&
    mainSource.includes('if(e.key===\'Escape\')') &&
    mainSource.includes('if(e.shiftKey&&(active===first||!root.contains(active)))') &&
    mainSource.includes('if(trigger?.isConnected)trigger.focus()') &&
    appBundle.includes('function benchDrawerFocusable(root)') &&
    appBundle.includes('benchDrawerTrigger = document.activeElement'));
  check('服务密钥编辑 Bundle 与源码一致，且具备 Escape、焦点返回、Tab 循环和异步关闭保护',
    keyPickerBundle === keyPickerSource &&
    keyPickerSource.includes('function restoreEditFocus()') &&
    keyPickerSource.includes("if(event.key==='Escape')") &&
    keyPickerSource.includes("event.shiftKey&&(active===first||!editModal.contains(active))") &&
    keyPickerSource.includes('const epoch=++editEpoch;') &&
    keyPickerSource.includes('if(epoch!==editEpoch||!editModal.classList.contains(\'show\'))return;') &&
    keyPickerSource.includes('editEpoch+=1;') &&
    keyPickerSource.includes('if(epoch===editEpoch&&button)button.disabled=false;') &&
    keyPickerSource.includes('showEditModal(button)'));
  check('调用记录有明确的加载中、失败重试与无障碍忙碌状态',
    requestsSource.includes('正在加载调用记录…') &&
    requestsSource.includes('role="alert"') &&
    requestsSource.includes('onclick="loadRequests()"') &&
    requestsSource.includes("setAttribute('aria-busy', 'true')") &&
    requestsSource.includes("setAttribute('aria-busy', 'false')") &&
    appBundle.toLowerCase().includes('\\u6b63\\u5728\\u52a0\\u8f7d\\u8c03\\u7528\\u8bb0\\u5f55\\u2026') &&
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
    ['model-test', '模型测试'],
    ['providers', '远程 Provider']
  ];
  for (const [tab, title] of tabs) {
    await clickTab(page, tab, title);
    await recordLayout(page, title + '-desktop');

    if (tab === 'models-inference') {
      check('服务参数页包含 Backend 运行参数档案管理入口',
        await page.locator('#backendRuntimeProfilesRoot').count() === 1 &&
        await page.locator('#backendRuntimeProfileForm').count() === 1);
    }

    if (tab === 'model-test') {
      check('模型测试页包含 Backend 运行参数档案选择器',
        await page.locator('#benchRuntimeProfile').count() === 1);
    }

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

    if (tab === 'model-test') {
      const opener = page.locator('#benchParamsOpenBtn');
      const drawer = page.locator('#benchParamsDrawer');
      const launcherSemantics = await opener.evaluate(el => ({
        popup: el.getAttribute('aria-haspopup'),
        controls: el.getAttribute('aria-controls'),
        targetExists: Boolean(document.getElementById(el.getAttribute('aria-controls')))
      }));
      check('性能测试参数入口声明对话框类型与关联目标',
        launcherSemantics.popup === 'dialog' &&
        launcherSemantics.controls === 'benchParamsDrawer' &&
        launcherSemantics.targetExists,
        launcherSemantics);

      await opener.click();
      await drawer.waitFor({ state: 'visible', timeout: 5000 });
      const dialogSemantics = await drawer.evaluate(root => {
        const dialog = root.querySelector('.drawer-panel');
        const titleId = dialog?.getAttribute('aria-labelledby');
        return {
          hidden: root.hidden,
          ariaHidden: root.getAttribute('aria-hidden'),
          role: dialog?.getAttribute('role'),
          modal: dialog?.getAttribute('aria-modal'),
          labelledBy: titleId,
          accessibleName: titleId ? document.getElementById(titleId)?.textContent.trim() : ''
        };
      });
      check('性能测试参数抽屉打开后具有已命名的模态对话框语义',
        !dialogSemantics.hidden &&
        dialogSemantics.ariaHidden === 'false' &&
        dialogSemantics.role === 'dialog' &&
        dialogSemantics.modal === 'true' &&
        dialogSemantics.labelledBy === 'benchParamsDrawerTitle' &&
        dialogSemantics.accessibleName === '推理参数',
        dialogSemantics);
      const closeButton = drawer.locator('.drawer-head button');
      await page.waitForFunction(() => document.activeElement === document.querySelector('#benchParamsDrawer .drawer-head button'));
      check('性能测试参数抽屉打开后将焦点移入关闭按钮',
        await closeButton.evaluate(el => document.activeElement === el));
      await page.keyboard.press('Shift+Tab');
      check('性能测试参数抽屉 Shift+Tab 循环到最后一个控件',
        await page.locator('#benchStop').evaluate(el => document.activeElement === el));
      await page.keyboard.press('Tab');
      check('性能测试参数抽屉 Tab 焦点循环回第一个控件',
        await closeButton.evaluate(el => document.activeElement === el));
      await page.keyboard.press('Escape');
      await drawer.waitFor({ state: 'hidden', timeout: 5000 });
      const closedSemantics = await drawer.evaluate(root => ({
        hidden: root.hidden,
        ariaHidden: root.getAttribute('aria-hidden')
      }));
      check('性能测试参数抽屉关闭后从辅助技术树隐藏',
        closedSemantics.hidden && closedSemantics.ariaHidden === 'true',
        closedSemantics);
      check('性能测试参数抽屉支持 Escape 并返回触发按钮',
        await opener.evaluate(el => document.activeElement === el));
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

      // 检查运行时注入的 Provider 密钥状态文字，而不只检查主题 CSS 变量。
      const providerKeyContrast = await page.evaluate(() => {
        const host = document.createElement('div');
        host.style.cssText = 'position:fixed;left:-10000px;top:0;visibility:hidden;background:var(--surface)';
        const label = document.createElement('span');
        label.className = 'provider-key-set';
        label.textContent = '已配置';
        host.appendChild(label);
        document.body.appendChild(host);

        const channels = value => (value.match(/[\d.]+/g) || []).slice(0, 3).map(Number);
        const luminance = value => {
          const rgb = channels(value);
          if (rgb.length !== 3) return Number.NaN;
          const linear = rgb.map(channel => {
            const srgb = channel / 255;
            return srgb <= 0.04045 ? srgb / 12.92 : ((srgb + 0.055) / 1.055) ** 2.4;
          });
          return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
        };
        const foreground = getComputedStyle(label).color;
        const background = getComputedStyle(host).backgroundColor;
        const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
        const result = {
          foreground,
          background,
          ratio: Number(((values[0] + 0.05) / (values[1] + 0.05)).toFixed(2))
        };
        host.remove();
        return result;
      });
      check('Provider 密钥已配置状态文字在深色表面达到 WCAG AA（≥4.5:1）',
        providersSource.includes("'.provider-key-set{color:var(--ok,#34d399)}'") &&
        Number.isFinite(providerKeyContrast.ratio) &&
        providerKeyContrast.ratio >= 4.5,
        providerKeyContrast);

      const providerStateBadgeContrast = await page.evaluate(() => {
        const host = document.createElement('div');
        host.style.cssText = 'position:fixed;left:-10000px;top:0;visibility:hidden;background:var(--surface)';
        const variants = [
          { name: 'enabled', className: 'provider-state-on' },
          { name: 'disabled', className: 'provider-state-off' },
          { name: 'warning', className: 'provider-state-bad' }
        ];
        for (const variant of variants) {
          const badge = document.createElement('span');
          badge.className = 'provider-state ' + variant.className;
          badge.textContent = variant.name;
          host.appendChild(badge);
        }
        document.body.appendChild(host);
        const channels = value => (value.match(/[\d.]+/g) || []).slice(0, 3).map(Number);
        const luminance = value => {
          const rgb = channels(value);
          if (rgb.length !== 3) return Number.NaN;
          const linear = rgb.map(channel => {
            const srgb = channel / 255;
            return srgb <= 0.04045 ? srgb / 12.92 : ((srgb + 0.055) / 1.055) ** 2.4;
          });
          return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
        };
        const results = [...host.querySelectorAll('.provider-state')].map((badge, index) => {
          const foreground = getComputedStyle(badge).color;
          const background = getComputedStyle(badge).backgroundColor;
          const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
          return { name: variants[index].name, foreground, background, ratio: Number(((values[0] + 0.05) / (values[1] + 0.05)).toFixed(2)) };
        });
        host.remove();
        return results;
      });
      check('运行时注入的 Provider 启用、停用、异常徽标均达到 WCAG AA（≥4.5:1）',
        providersSource.includes("'.provider-state-on{background:#e3f5e9;color:#176d3a}'") &&
        providersSource.includes("'.provider-state-off{background:#edf0f4;color:#596273}'") &&
        providersSource.includes("'.provider-state-bad{background:#fff0df;color:#925300}'") &&
        providerStateBadgeContrast.length === 3 &&
        providerStateBadgeContrast.every(item => Number.isFinite(item.ratio) && item.ratio >= 4.5),
        providerStateBadgeContrast);

    }

    if (['dashboard', 'models-local', 'models-download', 'requests', 'providers'].includes(tab)) {
      await page.screenshot({ path: screenshotName(tab + '-desktop'), fullPage: true });
    }
  }

  // 请求详情弹窗使用只读 Mock 验证真实键盘路径，不写入 Gateway 审计记录。
  const smokeRequestID = 'browser-smoke-detail-1';
  let failNextRequestList = false;
  let mockDetailRequestCount = 0;
  await page.route('**/api/requests**', async route => {
    const request = route.request();
    if (request.method() !== 'GET') return route.continue();
    const pathname = new URL(request.url()).pathname;
    if (pathname === '/api/requests/stats') {
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          requests: { total_requests: 1, running_requests: 0, success_rate_percent: 100, success_requests: 1, completed_requests: 1, error_requests: 0, cancelled_requests: 0 },
          latency: { average_ms: 120, sample_count: 1, buckets: [] },
          tokens: { upstream_usage: { total_tokens: 12, requests: 1, input_tokens: 5, output_tokens: 7 }, llama_timings: {}, unknown_source: {} }
        })
      });
      return;
    }
    if (pathname === '/api/requests') {
      if (failNextRequestList) {
        failNextRequestList = false;
        await route.fulfill({
          status: 503, contentType: 'application/json',
          body: JSON.stringify({ error: { type: 'temporarily_unavailable', message: 'simulated request-list outage' } })
        });
        return;
      }
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ data: [{
          request_id: smokeRequestID, status: 'completed', started_at: '2026-10-28T12:00:00Z',
          client_name: 'Browser Smoke', api_key_name: 'Smoke Key',
          request_url: 'https://example.test/v1/chat/completions', provider_id: 'mock-provider',
          model_requested: 'mock-model', model_actual: 'mock-model', http_status: 200,
          latency_ms: 120, ttft_ms: 15, total_tokens: 12, request_bytes: 128, response_bytes: 512, stream: false
        }] })
      });
      return;
    }
    if (pathname.startsWith('/api/requests/')) {
      mockDetailRequestCount += 1;
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          summary: { request_id: smokeRequestID, status: 'completed', http_status: 200, method: 'POST', request_url: 'https://example.test/v1/chat/completions', api_key: 'Smoke Key', client_name: 'Browser Smoke', provider_id: 'mock-provider', model_requested: 'mock-model' },
          timing: { started_at: '2026-10-28T12:00:00Z', finished_at: '2026-10-28T12:00:01Z', latency_ms: 120, ttft_ms: 15, input_tokens: 5, output_tokens: 7, total_tokens: 12 },
          analysis: {
            timeline_only: true,
            timeline: [
              { name: 'request_received', offset_ms: 0, status: 'measured' },
              { name: 'auth_started', offset_ms: 2, status: 'measured' },
              { name: 'gateway_write_failed', offset_ms: 3, status: 'measured' },
              { name: 'pipeline_started', offset_ms: null, status: 'not_reached' }
            ]
          },
          tool_calls: [
            { seq: 1, round: 1, tool_name: 'web.search', status: 'succeeded', duration_ms: 35, result_bytes: 128, created_at: '2026-10-28T12:00:00Z' },
            { seq: 2, round: 1, tool_name: 'web.search', status: 'failed', duration_ms: 5, result_bytes: 0, error_code: 'tool_execution_failed', created_at: '2026-10-28T12:00:01Z' }
          ],
          request_params: { model_client: 'mock-model', model_upstream: 'mock-model', stream_label: '非流式' }
        })
      });
      return;
    }
    await route.continue();
  });
  // 请求记录标签此前已在全局布局检查中加载过；Mock 注册后显式重载，确保详情请求使用固定测试数据。
  await page.evaluate(() => window.loadRequests({ forceStats: true }));
  await clickTab(page, 'requests', '调用记录');
  const detailTrigger = page.locator('#requestsTable button').filter({ hasText: '详情' }).first();
  await detailTrigger.waitFor({ state: 'visible', timeout: 10000 });
  await detailTrigger.focus();
  await detailTrigger.click();
  const detailModal = page.locator('#detailModal');
  await page.locator('#detailModal.show').waitFor({ state: 'visible', timeout: 10000 });
  check('请求详情使用 Mock 详情接口响应', mockDetailRequestCount === 1, mockDetailRequestCount);
  await page.waitForFunction(() => document.activeElement === document.querySelector('#detailModal .card-head button'), null, { timeout: 5000 });
  check('请求详情弹窗打开后将焦点移入关闭按钮',
    await detailModal.locator('.card-head button').evaluate(el => document.activeElement === el));
  const timelineSummary = detailModal.locator('summary').filter({ hasText: '请求时间线 (4)' });
  check('timeline_only 早期请求详情展示时间线入口', await timelineSummary.count() === 1);
  if (await timelineSummary.count()) {
    await timelineSummary.click();
    check('时间线显示真实零毫秒、已测量和未到达状态', 
      await detailModal.getByText('0 ms · 已测量', { exact: false }).count() >= 1 &&
      await detailModal.getByText('— · 未到达', { exact: false }).count() >= 1);
    check('时间线展示 Gateway 正文写回失败事件', await detailModal.getByText('Gateway 正文写回失败', { exact: false }).count() === 1);
    check('timeline_only 不显示虚构的阶段耗时', 
      await detailModal.getByText('请求分析', { exact: true }).count() === 0);
  }
  const toolCallSummary = detailModal.locator('summary').filter({ hasText: '工具调用明细 (2)' });
  check('请求详情展示工具调用审计明细及事件数量', await toolCallSummary.count() === 1);
  await toolCallSummary.click();
  check('工具调用明细展示顺序、轮次、工具名与执行状态',
    await detailModal.getByText('#1 · 第1轮 · web.search · 成功 · 35 ms', { exact: true }).count() === 1 &&
    await detailModal.getByText('#2 · 第1轮 · web.search · 失败 · 5 ms', { exact: true }).count() === 1);
  const focusState = await detailModal.evaluate(root => {
    const selector = 'a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])';
    const visible = [...root.querySelectorAll(selector)].filter(el => {
      const closedDetails = el.closest('details:not([open])');
      return !el.hidden && el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden' && (!closedDetails || el.matches('details:not([open]) > summary'));
    });
    if (!visible.length) return { count: 0, firstIsActive: false, lastIsActive: false };
    visible[visible.length - 1].focus();
    const lastIsActive = document.activeElement === visible[visible.length - 1];
    return { count: visible.length, lastIsActive, firstIsActive: false };
  });
  check('请求详情弹窗具有可实际聚焦的键盘控件', focusState.count > 0 && focusState.lastIsActive, focusState);
  await page.keyboard.press('Tab');
  check('请求详情弹窗 Tab 从最后一个控件循环到第一个',
    await detailModal.evaluate(root => {
      const selector = 'a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])';
      const visible = [...root.querySelectorAll(selector)].filter(el => {
      const closedDetails = el.closest('details:not([open])');
      return !el.hidden && el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden' && (!closedDetails || el.matches('details:not([open]) > summary'));
    });
      return visible.length > 0 && document.activeElement === visible[0];
    }));
  const firstFocusState = await detailModal.evaluate(root => {
    const selector = 'a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])';
    const visible = [...root.querySelectorAll(selector)].filter(el => {
      const closedDetails = el.closest('details:not([open])');
      return !el.hidden && el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden' && (!closedDetails || el.matches('details:not([open]) > summary'));
    });
    if (!visible.length) return { count: 0, firstIsActive: false, lastIsActive: false };
    visible[0].focus();
    return { count: visible.length, firstIsActive: document.activeElement === visible[0], lastIsActive: false };
  });
  await page.keyboard.press('Shift+Tab');
  check('请求详情弹窗 Shift+Tab 从第一个控件循环到最后一个',
    firstFocusState.count > 0 && firstFocusState.firstIsActive && await detailModal.evaluate(root => {
      const selector = 'a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])';
      const visible = [...root.querySelectorAll(selector)].filter(el => {
      const closedDetails = el.closest('details:not([open])');
      return !el.hidden && el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden' && (!closedDetails || el.matches('details:not([open]) > summary'));
    });
      return visible.length > 0 && document.activeElement === visible[visible.length - 1];
    }));
  await page.keyboard.press('Escape');
  await page.locator('#detailModal').waitFor({ state: 'hidden', timeout: 5000 });
  check('请求详情弹窗 Escape 关闭并将焦点返回触发按钮',
    await detailTrigger.evaluate(el => document.activeElement === el) &&
    await page.locator('#detailModal').getAttribute('aria-hidden') === 'true');
  const requestTimingDetail = await page.evaluate(() => {
    const root = document.createElement('div');
    window.applyRequestDetail(root, {
      summary: { request_id: 'browser-smoke-timing-detail', status: 'completed', http_status: 200, method: 'POST', request_url: '/v1/chat/completions', provider_id: 'llamacpp' },
      timing: { latency_ms: 1000, input_tokens: 100, output_tokens: 34, total_tokens: 134 },
      analysis: {
        timeline_only: false,
        throughput: { llama_timings: {
          cache_n: 80, prompt_n: 20, predicted_n: 34, usage_cached_tokens: 75,
          prompt_ms: 100, prompt_per_token_ms: 5, predicted_ms: 100,
          predicted_per_token_ms: 2.94,
          prompt_progress: [{ total: 100, cache: 80, processed: 90, time_ms: 500 }]
        }},
        performance_diagnosis: {
          status: 'observed_only',
          candidates: [{
            category: 'prompt_eval', status: 'candidate', title: 'Prompt Eval',
            summary: '已取得 llama.cpp 原生 Prompt Eval 计时；需要同条件样本比较。',
            evidence: [{ metric: 'prompt_ms', value: 100, unit: 'ms', source: 'llama.cpp_response_timings', scope: 'request' }],
            next_steps: ['使用相同模型、输入规模和缓存状态重复测量']
          }],
          limitations: ['当前没有同条件基线，不输出主瓶颈。']
        }
      }
    });
    return root.textContent;
  });
  check('请求详情展示缓存 Token、每 Token timing 与 Prompt progress',
    requestTimingDetail.includes('缓存 Token（llama.cpp cache_n）') &&
    requestTimingDetail.includes('Prompt Eval 每 Token') &&
    requestTimingDetail.includes('Decode 每 Token') &&
    requestTimingDetail.includes('Prompt progress') &&
    requestTimingDetail.includes('50.0%'));
  check('请求详情展示性能候选、原始证据和结论限制',
    requestTimingDetail.includes('性能诊断') &&
    requestTimingDetail.includes('已有观测，尚未确认瓶颈') &&
    requestTimingDetail.includes('prompt_ms') &&
    requestTimingDetail.includes('使用相同模型、输入规模和缓存状态重复测量') &&
    requestTimingDetail.includes('当前没有同条件基线，不输出主瓶颈。'));
  const toastCountBeforeSilentPoll = await page.locator('#toastHost .toast').count();
  failNextRequestList = true;
  expectedSilentRequestPollFailure = true;
  await page.evaluate(() => window.loadRequests({ silent: true }));
  await page.waitForTimeout(100);
  expectedSilentRequestPollFailure = false;
  const toastCountAfterSilentPoll = await page.locator('#toastHost .toast').count();
  check('调用记录静默轮询遇到 HTTP 503 时保留现有内容且不弹重复提示',
    !failNextRequestList && expectedSilentRequestPollResponseCount === 1 &&
    toastCountAfterSilentPoll === toastCountBeforeSilentPoll,
    { before: toastCountBeforeSilentPoll, after: toastCountAfterSilentPoll, expected503: expectedSilentRequestPollResponseCount });
  await page.unroute('**/api/requests*');

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
            id:'deepseek', enabled:true,
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
    check('固定模板 ID 与已有 Provider 冲突时显示唯一 ID 提示',
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
    check('新增同名 Provider 被阻止且不会发送 POST',
      duplicateCreateState.postCount === 0 &&
      !duplicateCreateState.editorHidden &&
      duplicateCreateState.message.includes('已存在') &&
      duplicateCreateState.message.includes('不能重复新增') &&
      await page.locator('#providerSave').isDisabled(),
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
  }
  // 真实 Gateway 验收：只创建停用的临时 Provider；确认网页看到真实列表项后，重复新增必须在前端被阻止。
  await page.unroute('**/api/admin/providers');
  const liveProviderID = 'browser-smoke-live-' + Date.now().toString(36);
  const liveProviderPayload = {
    id: liveProviderID, enabled: false, base_url: 'https://browser-smoke.invalid/v1',
    api_key: '', api_key_header: 'authorization', default_model: 'browser-smoke-model',
    model_mappings: {}, allow_insecure_http: false, allow_private_network: false,
    max_response_bytes: 0, max_concurrent_requests: 1, max_queue_size: 0,
    queue_wait_timeout_seconds: 0, requests_per_minute: 0, token_budget_per_day: 0, request_timeout_seconds: 0
  };
  const liveCreateResult = await page.evaluate(async ({ id, payload }) => {
    const token = localStorage.getItem('llm_gateway_session_token');
    if (!token) return { tokenPresent: false, createStatus: 0, listStatus: 0, matches: 0, error: 'Web Session 不存在' };
    const headers = { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' };
    let createResponse, createBody = null, listResponse, listBody = null;
    try {
      createResponse = await fetch('/api/admin/providers', { method: 'POST', headers, body: JSON.stringify(payload) });
      try { createBody = await createResponse.json(); } catch { /* 无 JSON 响应 */ }
      listResponse = await fetch('/api/admin/providers', { headers: { Authorization: `Bearer ${token}` } });
      try { listBody = await listResponse.json(); } catch { /* 无 JSON 响应 */ }
      const rows = Array.isArray(listBody?.data) ? listBody.data : [];
      return {
        tokenPresent: true, createStatus: createResponse.status, listStatus: listResponse.status,
        matches: rows.filter(row => row.id === id).length,
        createdDisabled: rows.find(row => row.id === id)?.enabled === false,
        error: createBody?.error?.message || createBody?.message || ''
      };
    } catch (error) {
      return { tokenPresent: true, createStatus: createResponse?.status || 0, listStatus: listResponse?.status || 0, matches: 0, error: String(error) };
    }
  }, { id: liveProviderID, payload: liveProviderPayload });
  check('真实 Gateway API 可创建停用的临时 Provider，并在真实列表中唯一出现',
    liveCreateResult.tokenPresent && liveCreateResult.createStatus === 201 &&
    liveCreateResult.listStatus === 200 && liveCreateResult.matches === 1 && liveCreateResult.createdDisabled,
    liveCreateResult);
  if (liveCreateResult.createStatus === 201) {
    let duplicateLivePostCount = 0;
    let liveProviderCleanupStatus = 0;
    const countLiveDuplicatePost = request => {
      if (new URL(request.url()).pathname === '/api/admin/providers' && request.method() === 'POST') duplicateLivePostCount += 1;
    };
    try {
      await clickTab(page, 'providers', '远程 Provider');
      await page.locator('#providerRefresh').click();
      await page.waitForFunction(id => (document.getElementById('providerRows')?.textContent || '').includes(id),
        liveProviderID, { timeout: 10000 });
      await page.locator('#providerCreate').click();
      await page.locator('#providerTemplate').selectOption('custom');
      await page.locator('#providerId').fill(liveProviderID);
      await page.locator('#providerBaseURL').fill('https://browser-smoke.invalid/v1');
      await page.locator('#providerDefaultModel').fill('browser-smoke-model');
      page.on('request', countLiveDuplicatePost);
      await page.locator('#providerForm').evaluate(form => form.requestSubmit());
      await page.waitForFunction(() => (document.getElementById('providerEditorStatus')?.textContent || '').includes('已存在'),
        null, { timeout: 10000 });
      await page.waitForTimeout(100);
      const liveDuplicateState = {
        postCount: duplicateLivePostCount,
        editorVisible: await page.locator('#providerEditorCard').isVisible(),
        message: await page.locator('#providerEditorStatus').textContent()
      };
      check('真实 Provider 列表中的同 ID 再次新增被拦截且没有发出第二个 POST',
        liveDuplicateState.postCount === 0 && liveDuplicateState.editorVisible &&
        liveDuplicateState.message.includes('已存在') && liveDuplicateState.message.includes('不能重复新增'),
        liveDuplicateState);
      await page.locator('#providerCancel').click();
    } finally {
      page.off('request', countLiveDuplicatePost);
      const cleanup = await page.evaluate(async id => {
        const token = localStorage.getItem('llm_gateway_session_token');
        if (!token) return { status: 0, error: 'Web Session 不存在' };
        try {
          const response = await fetch('/api/admin/providers/' + encodeURIComponent(id), {
            method: 'DELETE', headers: { Authorization: `Bearer ${token}` }
          });
          return { status: response.status, error: response.status === 204 ? '' : await response.text() };
        } catch (error) { return { status: 0, error: String(error) }; }
      }, liveProviderID);
      liveProviderCleanupStatus = cleanup.status;
      check('真实 Gateway 验收后已删除临时 Provider 配置', cleanup.status === 204, cleanup);
    }
  }

  // 验证 HF 文件选择事件使用 data-* 属性传值，特殊字符不会进入内联 JavaScript.
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
