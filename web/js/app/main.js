import { $, esc } from '../core/dom.js';
import { getKey, getSavedAdminKey, api, apiPublic, apiServiceKeys, initAdminKey, isAuthenticated } from '../core/api.js';
import { setGlobalMsg } from '../core/notify.js';
import { adminStore } from '../features/admin-store.js';
import { fmtSize, fmtDate, slug, statusLabel, statusPill, emptyState, wrapTable, displayUpstreamModel } from '../core/format.js';
import {
  requestsListHasRows,
  loadRequests,
  clearRequests,
  cancelRequest,
  showDetail,
  closeModal,
  applyRequestDetail,
  reqKv,
  prettyBodyText,
  reqPanelFold,
  reqCodeBlock,
  flashCopyBtn,
  copyReqCode,
  copyReqStreamAll,
  writeClipboard,
  formatStreamEventBody,
  formatStreamEventsDump,
  renderStreamEventsSection,
} from '../features/requests.js';
import { exposeGlobals } from '../expose.js';

// 管理员 API Key 与 Web Session 分开保存；启动时仅回填密钥管理表单。
initAdminKey();
function syncTopbarRefresh(){
  const btn=$('btnRefreshDashboard');
  if(!btn)return;
  btn.hidden=!$('dashboard')?.classList.contains('active');
}

document.querySelectorAll('.nav button').forEach(b=>b.onclick=()=>{
  document.querySelectorAll('.nav button').forEach(x=>x.classList.remove('active'));
  $('sidebarAccountBtn')?.classList.remove('active');
  document.querySelectorAll('.panel').forEach(x=>x.classList.remove('active'));
  b.classList.add('active');
  const tabPanel=$(b.dataset.tab);if(tabPanel)tabPanel.classList.add('active');
  if(b.dataset.tab==='model-test'){loadBenchPanel().catch(()=>{});renderBenchmarkSuiteReport();}
  if(b.dataset.tab==='models-local')loadLocalModelsPanel().catch(()=>{});
  if(b.dataset.tab==='models-download')loadDownloadPanel({refreshCatalog:true}).catch(()=>{});
  if(b.dataset.tab==='models-inference'&&globalThis.InferenceParams)globalThis.InferenceParams.load().catch(()=>{});
  if(b.dataset.tab==='dashboard'){
    loadDashboardRecent().catch(()=>{});
    loadDashboardStats().catch(()=>{});
    loadSystem().catch(()=>{});
    dashboardPoll.start({ skipImmediate: true });
    const gm=$('globalMsg');
    if(gm){gm.innerHTML='';gm.hidden=true;}
  }else{
    dashboardPoll.stop();
    const gm=$('globalMsg');
    if(gm)gm.hidden=false;
  }
  if(b.dataset.tab==='keys')loadKeysPanel().catch(()=>{});
  // 调用记录已有数据行时不刷新；空白、暂无记录或加载失败才拉一次。
  if(b.dataset.tab==='requests'&&!requestsListHasRows())loadRequests().catch(()=>{});
  syncTopbarRefresh();
});
syncTopbarRefresh();
$('dashTokenRange')?.addEventListener('change',()=>loadDashboardStats().catch(()=>{}));
function inferButtonHtml(backend,action,{modelId='',modelKey='',title='',confirmMsg='',label='',extraClass=''}){
  const cls=['btn','sm',extraClass];
  if(action==='load')cls.push('primary');
  if(action==='unload')cls.push('danger');
  const parts=[
    'type="button"','class="'+cls.filter(Boolean).join(' ')+'"','data-infer=""',
    'data-infer-backend="'+esc(backend)+'"','data-infer-action="'+esc(action)+'"'
  ];
  if(modelId)parts.push('data-model-id="'+esc(modelId)+'"');
  if(modelKey)parts.push('data-model-key="'+esc(modelKey)+'"');
  if(title)parts.push('data-infer-title="'+esc(title)+'"');
  if(confirmMsg)parts.push('data-confirm="'+esc(confirmMsg)+'"');
  const text=label||(action==='load'?'加载':action==='unload'?'卸载':action);
  return '<button '+parts.join(' ')+'>'+esc(text)+'</button>';
}
function scrollMmActionLog(){
  const card=$('mmActionLogCard');
  if(card)card.scrollIntoView({block:'nearest',behavior:'smooth'});
}
function markActionLogActive(){
  $('mmActionLogCard')?.classList.add('has-activity');
  scrollMmActionLog();
}
function handleInferClick(ev){
  const delBtn=ev.target.closest?.('button[data-model-delete]');
  if(delBtn&&!delBtn.disabled){
    const kind=delBtn.dataset.modelDelete||'';
    const modelKey=(delBtn.dataset.modelKey||'').trim();
    const modelId=(delBtn.dataset.modelId||'').trim();
    const modelPath=(delBtn.dataset.modelPath||'').trim();
    if(kind==='gguf'&&modelKey){void deleteModel(modelKey);return}
    if(kind==='gguf-disk'&&modelPath){void deleteDiskGGUFModel(modelPath);return}
    if(kind==='ollama'&&(modelId||modelKey)){void ollamaDelete(modelId||modelKey);return}
  }
  const btn=ev.target.closest?.('button[data-infer]');
  if(!btn||btn.disabled)return;
  const backend=btn.dataset.inferBackend||'';
  const action=btn.dataset.inferAction||'';
  const modelId=btn.dataset.modelId||'';
  const modelKey=btn.dataset.modelKey||'';
  const title=btn.dataset.inferTitle||(`${backend} · ${action}`+(modelId?` · ${modelId}`:'')+(modelKey?` · ${modelKey}`:''));
  markActionLogActive();
  appendOpsLog([{level:'info',message:'收到点击 → '+action+' backend='+backend+(modelId?' id='+modelId:'')+(modelKey?' key='+modelKey:'')}],title);
  const cmsg=btn.dataset.confirm;
  if(cmsg&&!confirm(cmsg)){
    appendOpsLog([{level:'warn',message:'已在确认框点取消，未发送请求'}]);
    return;
  }
  if(backend==='llamacpp'&&(action==='load'||action==='unload')){
    if(action==='load')setGgufMsg('','');
    inferenceOp(backend,action,{model_id:modelId,model_key:modelKey},title).then(j=>{
      if(action==='load'&&j?.success)setGgufMsg('ok','已加载到 llama.cpp');
      else if(action==='load'&&(!j||!j.success))setGgufMsg('err',(j&&j.error)||'加载失败');
      else if(action==='unload'&&j?.success)setGgufMsg('ok','已从 llama.cpp 卸载');
      else if(action==='unload'&&(!j||!j.success))setGgufMsg('err',(j&&j.error)||'卸载失败');
    });
    return;
  }
  if(backend==='ollama'){
    inferenceOp(backend,action,{model_id:modelId,model_key:modelKey},title).then(j=>{
      if(j?.success&&action==='load')$('ollamaMsg').innerHTML='<span class="ok">已加载 '+esc(modelId)+'</span>';
      else if((!j||!j.success)&&action==='load')$('ollamaMsg').innerHTML='<span class="err">加载失败，见操作日志</span>';
      else if(j?.success&&action==='unload')$('ollamaMsg').innerHTML='<span class="ok">已卸载 '+esc(modelId)+'</span>';
      else if((!j||!j.success)&&action==='unload')$('ollamaMsg').innerHTML='<span class="err">卸载失败，见操作日志</span>';
    });
    return;
  }
  inferenceOp(backend,action,{model_id:modelId,model_key:modelKey},title);
}
$('models-local')?.addEventListener('click',handleInferClick);

function appendActionLogTo(el,lines,prependTitle){
  if(!el)return;
  if(el.classList.contains('muted')){el.classList.remove('muted');el.innerHTML=''}
  if(prependTitle){
    const h=document.createElement('p');h.className='ops-log-line info';
    h.innerHTML=`<span class="t">${new Date().toLocaleTimeString()}</span><span class="m">—— ${esc(prependTitle)} ——</span>`;
    el.appendChild(h);
  }
  (lines||[]).forEach(x=>{
    const p=document.createElement('p');
    const lv=(x.level||'info').toLowerCase();
    p.className='ops-log-line '+lv;
    const ts=x.at?new Date(x.at).toLocaleTimeString():'';
    p.innerHTML=`<span class="t">${esc(ts)}</span><span class="m">${esc(x.message||'')}</span>`;
    el.appendChild(p);
  });
  el.scrollTop=el.scrollHeight;
}
function appendOpsLog(lines,prependTitle){
  appendActionLogTo($('mmActionLog'),lines,prependTitle);
  if($('mmActionLog')&&lines?.length)markActionLogActive();
}
function clearMmActionLog(){
  const el=$('mmActionLog');if(!el)return;
  el.innerHTML='暂无操作；在 Llama 模型或 Ollama 库中点击加载/卸载后显示…';
  el.classList.add('muted');
}
async function refreshAfterInference(backend){
  const jobs=[loadMmBackendPills().catch(()=>{})];
  if($('models-local')?.classList.contains('active')){
    jobs.push((async()=>{
      await modelHub.refresh().catch(()=>{});
      renderRuntimeCatalog();
      renderGatewayModelTable();
      if(backend==='ollama')await loadOllamaLibrary().catch(()=>{});
    })());
  }
  if($('dashboard')?.classList.contains('active'))jobs.push(loadSystem().catch(()=>{}));
  await Promise.all(jobs);
}
async function inferenceOp(backend,action,{model_id='',model_key=''}={},title){
  markActionLogActive();
  if(!isAuthenticated()){
    appendOpsLog([{level:'err',message:'请先登录控制台'}],title||`${backend} · ${action}`);
    return null;
  }
  model_id=(model_id||'').trim();
  model_key=(model_key||'').trim();
  const head=title||`${backend} · ${action}`+(model_id?` · ${model_id}`:'')+(model_key?` · key=${model_key}`:'');
  appendOpsLog([{level:'info',message:'POST /api/admin/model-ops'}],head);
  try{
    const j=await api('/api/admin/model-ops',{
      method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({backend,action,model_id,model_key})
    });
    appendOpsLog(j.logs||[]);
    if(!j.success){
      const errMsg=j.error||'操作失败';
      appendOpsLog([{level:'err',message:errMsg}]);
      if(backend==='llamacpp')setGgufMsg('err',errMsg);
      else if(backend==='ollama'&&$('ollamaMsg'))$('ollamaMsg').innerHTML='<span class="err">'+esc(errMsg)+'</span>';
      return j;
    }
    appendOpsLog([{level:'ok',message:'完成 success=true'}]);
    await refreshAfterInference(backend);
    return j;
  }catch(e){
    appendOpsLog([{level:'err',message:e.message}]);
    if(backend==='llamacpp')setGgufMsg('err',e.message);
    else if(backend==='ollama'&&$('ollamaMsg'))$('ollamaMsg').innerHTML='<span class="err">'+esc(e.message)+'</span>';
    return null;
  }
}
function goTab(name){
  if(name==='account'&&globalThis.openAccountSettings){globalThis.openAccountSettings();return}
  const btn=document.querySelector('.nav button[data-tab="'+name+'"]');
  if(btn)btn.click();
}
async function loadMmBackendPills(){
  const el=$('mmBackendPills');
  if(!el)return;
  try{
    let a=adminStore.upstream;
    if(!a?.backend){
      const j=await api('/api/admin/backend');
      a=j.active||{};
    }
    const be=a.backend==='ollama'?'Ollama':a.backend==='llamacpp'?'llama.cpp':(a.backend||'—');
    const mn=(a.model_name||'').trim();
    el.innerHTML=`<span class="pill pill-running">${esc(be)}</span>`+
      (mn?`<span class="pill pill-loaded">当前 ${esc(mn)}</span>`:`<span class="pill">未加载模型</span>`);
  }catch{
    el.innerHTML='<span class="pill pill-warn">上游信息暂不可用，请检查服务状态后刷新</span>';
  }
}
/** 网关模型 + 上游 runtime 快照：列表/大小/加载态统一从此读取，避免各 Tab 各拉一套逻辑 */
const modelHub={
  rows:[],runtime:null,catalogById:new Map(),rowsLoaded:false,rowsError:'',runtimeError:'',runtimeWarning:'',_inflight:null,
  async refresh(opts={}){
    if(opts.force)this._inflight=null;
    if(this._inflight)return this._inflight;
    this._inflight=Promise.allSettled([api('/api/models'),api('/api/admin/runtime')])
      .then(([modelsResult,runtimeResult])=>{
        if(modelsResult.status==='fulfilled'&&Array.isArray(modelsResult.value?.data)){
          this.rows=modelsResult.value.data;
          this.rowsLoaded=true;
          this.rowsError='';
        }else{
          this.rowsError=this.rowsLoaded?'本地模型清单刷新失败，当前展示上次成功读取的数据；请检查数据库连接后刷新。':'本地模型清单读取失败，请检查网关数据库连接后刷新。';
        }
        if(runtimeResult.status==='fulfilled'){
          this.runtime=runtimeResult.value;
          this.runtimeError='';
          this.runtimeWarning=[this.runtime?.upstream_warning,this.runtime?.catalog_warning].filter(Boolean).join('；');
        }else{
          this.runtime=null;
          this.runtimeError='当前推理后端运行状态暂不可用；本地模型清单仍可独立查看。';
          this.runtimeWarning='';
        }
        this._reindexRuntime();
      })
      .finally(()=>{this._inflight=null});
    return this._inflight;
  },
  _reindexRuntime(){
    this.catalogById=new Map();
    const rt=this.runtime||{};
    for(const x of [...(rt.catalog||[]),...(rt.loaded||[])]){
      if(x&&x.id&&!this.catalogById.has(x.id))this.catalogById.set(x.id,x);
    }
  },
  _stripGguf(s){return String(s||'').replace(/\.gguf$/i,'');},
  _idMatch(a,b){
    a=this._stripGguf(a);b=this._stripGguf(b);
    if(!a||!b)return false;
    if(a===b)return true;
    const baseA=a.includes('/')?a.split('/').pop():a;
    const baseB=b.includes('/')?b.split('/').pop():b;
    return baseA===b||baseB===a||baseA===baseB;
  },
  _samePath(a,b){
    a=String(a||'').trim().replace(/\\/g,'/').toLowerCase();
    b=String(b||'').trim().replace(/\\/g,'/').toLowerCase();
    return Boolean(a&&b&&(a===b||a.endsWith('/'+b)||b.endsWith('/'+a)));
  },
  keysForRow(row){
    const out=new Set();
    const add=v=>{
      v=String(v||'').trim();
      if(!v)return;
      out.add(v);out.add(this._stripGguf(v));
      if(v.includes('/'))out.add(v.split('/').pop());
    };
    add(row.model_key);add(row.upstream_name);add(row.upstream_llamacpp_id);
    add(row.llama_model_name);add(row.filename);
    if(row.local_path)add(row.local_path);
    return out;
  },
  _runtimeMatchesRow(item,row){
    if(!item||!row)return false;
    if(item.path&&row.local_path){
      if(this._samePath(item.path,row.local_path))return true;
      if(item.path.startsWith('/')&&String(row.local_path).startsWith('/'))return false;
    }
    const keys=this.keysForRow(row);
    for(const k of keys){if(String(item.id||'')===k||this._idMatch(item.id,k))return true;}
    return false;
  },
  runtimeItemForRow(row){
    if(row?._runtime)return row._runtime;
    for(const [id,item] of this.catalogById){
      if(this._runtimeMatchesRow(item,row))return item;
    }
    return null;
  },
  tableRows(){
    const rows=(this.rows||[]).map(row=>({...row,_managed:true,_runtime:this.runtimeItemForRow(row)}));
    for(const item of (this.runtime?.catalog||[])){
      if(rows.some(row=>this._runtimeMatchesRow(item,row)))continue;
      rows.push({
        model_key:'',display_name:item.display_name||item.id,filename:item.display_name||item.id,
        local_path:item.path||'',size_bytes:Number(item.disk_bytes||0),status:item.status||'unknown',
        quantization:item.quantization||'',upstream_name:item.id,_managed:false,_runtime:item,
      });
    }
    return rows;
  },
  diskBytes(row){
    const it=this.runtimeItemForRow(row);
    if(it&&Number(it.disk_bytes)>0)return Number(it.disk_bytes);
    const db=Number(row.disk_bytes??row.size_bytes??0);
    return db>0?db:0;
  },
  loadState(row){
    const dbStatus=String(row.status||'').toLowerCase();
    const rt=this.runtimeItemForRow(row);
    const downloadStates=['downloading','download_failed','download_cancelled'];
    if(rt){
      const status=String(rt.status||'unknown').toLowerCase();
      if(status&&status!=='unknown')return status;
      if(rt.loaded)return 'loaded';
      if(downloadStates.includes(dbStatus))return dbStatus;
      return 'unknown';
    }
    return downloadStates.includes(dbStatus)?dbStatus:'unknown';
  },
};

function modelsNavActive(){
  return $('models-local')?.classList.contains('active')||$('models-download')?.classList.contains('active')||$('models-inference')?.classList.contains('active');
}
async function refreshLlamaModels(){
  const btn=$('btnRefreshLlamaModels');
  const msg=$('ggufMsg');
  if(btn){btn.disabled=true;btn.setAttribute('aria-busy','true');}
  if(msg)msg.textContent='正在刷新 Router 注册目录与共享磁盘清单…';
  try{
    await api('/api/admin/runtime/refresh',{method:'POST'});
    await modelHub.refresh({force:true});
    renderRuntimeCatalog();
    renderGatewayModelTable();
    if(msg)msg.innerHTML='<span class="ok">模型目录已刷新</span>';
    await loadOllamaLibrary();
  }catch(e){
    if(msg)msg.innerHTML='<span class="err">'+esc(e.message)+'</span>';
    await modelHub.refresh({force:true}).catch(()=>{});
    renderRuntimeCatalog();
    renderGatewayModelTable();
  }finally{
    if(btn){btn.disabled=false;btn.removeAttribute('aria-busy');}
  }
}
async function loadLocalModelsPanel(){
  await Promise.all([loadMmBackendPills(),modelHub.refresh()]);
  renderRuntimeCatalog();
  renderGatewayModelTable();
  await loadOllamaLibrary();
}
async function loadDownloadPanel(opts={}){
  await loadDownloads();
  if(globalThis.ModelDownload?.refreshLocalInventory)await globalThis.ModelDownload.refreshLocalInventory().catch(()=>{});
  if(opts.refreshCatalog&&globalThis.ModelDownload)await globalThis.ModelDownload.search().catch(()=>{});
}
async function loadModelsPanel(opts={}){
  if($('models-download')?.classList.contains('active'))return loadDownloadPanel(opts);
  return loadLocalModelsPanel();
}
function setOllamaPullFeedback(html){
  const onDownload=$('models-download')?.classList.contains('active');
  const el=onDownload?$('catalogMsg'):$('ollamaMsg');
  if(el)el.innerHTML=html;
}
async function ollamaPull(nameArg){
  const name=(nameArg||$('ollamaPullName')?.value||'').trim();
  if(!name){setOllamaPullFeedback('<span class="err">请输入模型名</span>');return}
  if(typeof globalThis.ModelDownload!=='undefined'&&globalThis.ModelDownload.startOllamaPull){
    await globalThis.ModelDownload.startOllamaPull(name);
    setOllamaPullFeedback('<span class="ok">已加入下载任务，见「模型下载 → 下载任务」</span>');
    return;
  }
  try{
    await api('/api/admin/ollama/pull',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name})});
    setOllamaPullFeedback('<span class="ok">已加入下载任务</span>');
    await loadDownloads();
  }catch(e){setOllamaPullFeedback('<span class="err">'+esc(e.message)+'</span>')}
}
function ollamaModelInMemory(x,loadedIds,active){
  if(x.loaded)return true;
  if(loadedIds&&loadedIds.has(x.id))return true;
  if(loadedIds){
    for(const id of loadedIds){
      if(id===x.id||id.startsWith(x.id+':')||x.id.startsWith(id+':'))return true;
    }
  }
  if(active&&(active===x.id||active.startsWith(x.id+':')||x.id.startsWith(active+':')))return true;
  return false;
}
function ollamaCatalogSubtitle(x){
  const q=String(x?.quantization||'').trim();
  const p=String(x?.parameter_size||'').trim();
  if(q&&p)return p+' · '+q;
  return q||p||'';
}
async function loadOllamaLibrary(){
  const el=$('ollamaTable');
  if(!el)return;
  $('ollamaMsg').textContent='';
  try{
    const j=await api('/api/admin/ollama');
    const loadedIds=new Set((j.loaded||[]).map(x=>x.id));
    const active=j.active||'';
    const rows=j.catalog||[];
    if(!rows.length){el.innerHTML=emptyState('暂无模型，请在「模型下载」中安装');return}
    el.innerHTML=wrapTable('<tr><th>模型</th><th>大小</th><th>状态</th><th>操作</th></tr>'+
      rows.map(x=>{
        const running=ollamaModelInMemory(x,loadedIds,active);
        const isActive=active&&(x.id===active||active.startsWith(x.id+':')||x.id.startsWith(active+':'));
        const loadLabel=running?'重新加载':'加载';
        const tLoad=`Ollama 库 · 加载 · ${x.id}`;
        const tUnload=`Ollama 库 · 卸载 · ${x.id}`;
        const sub=ollamaCatalogSubtitle(x);
        return `<tr>
          <td><code>${esc(x.id)}</code>${sub?'<div class="small muted">'+esc(sub)+'</div>':''}${isActive?' '+statusPill('当前',' pill-active'):''}</td>
          <td>${fmtSize(x.disk_bytes)}</td>
          <td>${running?statusPill('loaded'):statusPill('installed')}</td>
          <td><div class="td-actions">
            ${inferButtonHtml('ollama','load',{modelId:x.id,title:tLoad,label:loadLabel})}
            ${inferButtonHtml('ollama','unload',{modelId:x.id,title:tUnload,confirmMsg:'从内存卸载 '+x.id+'？',label:'卸载',extraClass:''})}
            <button type="button" class="btn sm danger" data-model-delete="ollama" data-model-id="${esc(x.id)}">删除</button>
          </div></td></tr>`;
      }).join(''));
  }catch(e){
    el.innerHTML=emptyState('无法读取 Ollama');
    $('ollamaMsg').innerHTML='<span class="err">'+esc(e.message)+'</span>';
  }
}
async function ollamaDelete(name){
  if(!isAuthenticated()){if($('ollamaMsg'))$('ollamaMsg').innerHTML='<span class="err">请先登录控制台</span>';return}
  if(!confirm('从 Ollama 磁盘删除 '+name+'？不可恢复'))return;
  const head=`Ollama 库 · 删除 · ${name}`;
  markActionLogActive();
  appendOpsLog([{level:'info',message:'POST /api/admin/ollama/delete name='+name}],head);
  try{
    const j=await api('/api/admin/ollama/delete',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name})});
    appendOpsLog(j.logs||[]);
    if(!j.success){
      appendOpsLog([{level:'err',message:j.error||'删除失败'}]);
      $('ollamaMsg').innerHTML='<span class="err">'+esc(j.error||'删除失败')+'</span>';
      return;
    }
    await loadOllamaLibrary();
    appendOpsLog([{level:'ok',message:'完成 success=true'}]);
    $('ollamaMsg').innerHTML='<span class="ok">已删除 '+esc(name)+'</span>';
  }catch(e){
    appendOpsLog([{level:'err',message:e.message}]);
    $('ollamaMsg').innerHTML='<span class="err">'+esc(e.message)+'</span>';
  }
}

async function loadDashboardRecent(){
  const el=$('dashRecentRequests');
  if(!el)return;
  try{
    const j=await api('/api/requests?limit=5');
    const rows=j.data||[];
    if(!rows.length){el.innerHTML=emptyState('暂无调用');return}
    el.innerHTML=wrapTable('<tr><th>时间</th><th>模型</th><th>状态</th><th>耗时</th></tr>'+
      rows.map(x=>`<tr><td class="small">${fmtDate(x.started_at)}</td><td class="small">${esc(displayUpstreamModel(x))}</td><td>${statusPill(x.status)}</td><td>${x.latency_ms??'—'} ms</td></tr>`).join(''));
  }catch(e){el.innerHTML='<span class="err small">'+esc(e.message)+'</span>'}
}


function formatTokenCount(value){
  const n=Number(value);
  return Number.isFinite(n)&&n>=0?Math.trunc(n).toLocaleString('zh-CN'):'—';
}
async function loadDashboardStats(){
  const root=$('dashTokenStats');
  const range=$('dashTokenRange');
  if(!root)return;
  const selected=['24h','7d','30d'].includes(range?.value)?range.value:'24h';
  root.setAttribute('aria-busy','true');
  try{
    const stats=await api('/api/requests/stats?range='+encodeURIComponent(selected));
    const sources=[
      {key:'upstream_usage',label:'远程上游 usage',hint:'上游返回的 Token 用量'},
      {key:'llama_timings',label:'本地 llama.cpp',hint:'本地引擎 timings 统计'},
      {key:'unknown_source',label:'来源未知',hint:'旧记录或缺少可识别来源'},
    ];
    const totals=sources.reduce((sum,item)=>{
      const v=stats.tokens?.[item.key]||{};
      sum.input+=Number(v.input_tokens)||0;
      sum.output+=Number(v.output_tokens)||0;
      sum.total+=Number(v.total_tokens)||0;
      return sum;
    },{input:0,output:0,total:0});
    const requests=stats.requests||{};
    root.innerHTML=`
      <div class="token-stats-summary">
        <div class="stat"><span>请求总数</span><b>${formatTokenCount(requests.total_requests)}</b><small>成功 ${formatTokenCount(requests.success_requests)} · 失败 ${formatTokenCount(requests.error_requests)} · 取消 ${formatTokenCount(requests.cancelled_requests)}</small></div>
        <div class="stat"><span>Token 总量</span><b>${formatTokenCount(totals.total)}</b><small>输入 ${formatTokenCount(totals.input)} · 输出 ${formatTokenCount(totals.output)}</small></div>
        <div class="stat"><span>成功率</span><b>${Number(requests.success_rate_percent||0).toFixed(1)}%</b><small>按已结束请求计算</small></div>
        <div class="stat"><span>平均延迟</span><b>${stats.latency?.average_ms==null?'—':Math.round(Number(stats.latency.average_ms)).toLocaleString('zh-CN')+' ms'}</b><small>${formatTokenCount(stats.latency?.sample_count)} 个有效样本</small></div>
      </div>
      <div class="table-wrap token-source-table">
        <table><thead><tr><th>统计来源</th><th>有 Token 记录的请求</th><th>输入 Token</th><th>输出 Token</th><th>合计</th></tr></thead><tbody>
          ${sources.map(item=>{
            const v=stats.tokens?.[item.key]||{};
            return `<tr><td><strong>${item.label}</strong><div class="small muted">${item.hint}</div></td><td>${formatTokenCount(v.requests)}</td><td>${formatTokenCount(v.input_tokens)}</td><td>${formatTokenCount(v.output_tokens)}</td><td><strong>${formatTokenCount(v.total_tokens)}</strong></td></tr>`;
          }).join('')}
        </tbody></table>
      </div>
      <p class="small muted token-stats-note">${esc(stats.tokens?.note||'Token 来源按请求审计记录分类；不同来源不可直接视为供应商账单用量。')}</p>`;
  }catch(e){
    root.innerHTML='<span class="err small">Token 统计加载失败：'+esc(e.message)+'</span>';
  }finally{
    root.setAttribute('aria-busy','false');
  }
}

function adminKeyStatus(){
  const el=$('adminKeyStatus');
  if(!el)return;
  if(getSavedAdminKey()){
    el.innerHTML='<span class="pill pill-success">管理员 Key 已配置</span>';
    return;
  }
  el.innerHTML='<span class="pill">未配置管理员 Key</span>';
}
async function saveKey(event){
  event?.preventDefault?.();
  const input=$('adminKey'), message=$('adminKeyMsg'), button=$('adminKeySaveButton');
  const candidate=getKey();
  if(!candidate){
    if(message)message.innerHTML='<span class="err">请输入管理员 API Key</span>';
    return;
  }
  if(button)button.disabled=true;
  if(message)message.textContent='正在验证管理员 API Key…';
  try{
    await apiPublic('/api/admin/keys',{headers:{'X-Admin-Key':candidate},cache:'no-store'});
    localStorage.setItem('llm_gateway_admin_key',candidate);
    window.dispatchEvent(new CustomEvent('llm-gateway-admin-key-changed',{detail:{configured:true}}));
    if(input)input.value=candidate;
    adminKeyStatus();
    if(message)message.innerHTML='<span class="ok">验证成功，已保存到当前浏览器。该 Key 只用于服务密钥管理。</span>';
    await loadKeysPanel();
  }catch(e){
    if(message)message.innerHTML='<span class="err">验证失败：'+esc(e.message)+'</span>';
  }finally{
    if(button)button.disabled=false;
  }
}
function clearAdminKey(){
  localStorage.removeItem('llm_gateway_admin_key');
  window.dispatchEvent(new CustomEvent('llm-gateway-admin-key-changed',{detail:{configured:false}}));
  const input=$('adminKey'), message=$('adminKeyMsg');
  if(input)input.value='';
  if(message)message.innerHTML='<span class="muted">管理员 Key 已清除；Web 登录状态不受影响。</span>';
  adminKeyStatus();
  const table=$('keyTable');
  if(table)table.innerHTML=emptyState('请先输入并验证管理员 API Key，再管理服务密钥');
}
function setGgufMsg(kind,text){
  const el=$('ggufMsg');if(!el)return;
  if(!text){el.innerHTML='';return}
  const cls=kind==='ok'?'ok':kind==='warn'?'warn':'err';
  el.innerHTML='<span class="'+cls+'">'+esc(text)+'</span>';
}
async function refreshModelsOnly(){
  await Promise.all([loadMmBackendPills(),modelHub.refresh(),loadDownloads()]);
  renderRuntimeCatalog();
  renderGatewayModelTable();
}
async function refreshAll(){
  adminKeyStatus();
  if(!isAuthenticated()){
    setGlobalMsg($('dashboard')?.classList.contains('active')?'':'<span class="warn">请先登录控制台</span>');
    return;
  }
  try{
    const jobs=[loadDownloads()];
    if($('dashboard')?.classList.contains('active')){
      adminStore.reset();
      jobs.push(
        loadSystem({ force: 'bootstrap' }).then(() => applyBackendFormFromStore()).catch(() => {}),
        loadDashboardRecent(),
        loadDashboardStats(),
      );
    }
    if($('keys')?.classList.contains('active'))jobs.push(loadKeys());
    if($('requests')?.classList.contains('active'))jobs.push(loadRequests());
    if($('models-local')?.classList.contains('active'))jobs.push(loadLocalModelsPanel().catch(()=>{}));
    else if($('models-download')?.classList.contains('active'))jobs.push(loadDownloadPanel().catch(()=>{}));
    else if(!modelsNavActive())jobs.push(loadModels().catch(()=>{}),loadMmBackendPills());
    await Promise.all(jobs);
    if($('benchmark')?.classList.contains('active'))await loadBenchPanel();
    setGlobalMsg('');
  }catch(e){setGlobalMsg('<span class="err">'+esc(e.message)+'</span>')}
}

function findActiveRuntimeItem(j){
  const loaded=Array.isArray(j.loaded)?j.loaded:[];
  if(!loaded.length)return null;
  const actual=String(j.active_model||'').trim();
  const route=String(j.gateway_model||'').trim();
  for(const target of [actual,route]){
    if(!target)continue;
    const hit=loaded.find(x=>String(x.id||'')===target||(x.id&&target&&modelHub._idMatch(x.id,target)));
    if(hit)return hit;
  }
  return loaded.length===1?loaded[0]:null;
}
function ggufLoadLabel(st){
  const s=String(st||'unknown').toLowerCase();
  if(s==='loaded')return '已加载';
  if(s==='sleeping')return '已加载（休眠）';
  if(s==='loading')return '加载中';
  if(s==='unloaded')return '已注册，未加载';
  if(s==='not_registered')return '磁盘存在，尚未注册';
  if(s==='downloading')return '下载中';
  if(s==='download_failed')return '下载失败';
  if(s==='download_cancelled')return '下载已取消';
  return '无法确认';
}
function ggufLoadPill(st){
  const s=String(st||'unknown').toLowerCase();
  const cls=['loaded','sleeping'].includes(s)?'pill-loaded':
    s==='loading'||s==='downloading'?'pill-running':
    s==='unloaded'?'pill-unloaded':'pill-warn';
  return `<span class="pill ${cls}">${esc(ggufLoadLabel(st))}</span>`;
}
function runtimeMetricCell(label,value,sub){
  const v=String(value||'').trim()||'—';
  return '<div class="runtime-metric"><span class="runtime-metric-label">'+esc(label)+'</span>'+
    '<span class="runtime-metric-value">'+esc(v)+'</span>'+
    (sub?'<span class="runtime-metric-sub">'+esc(sub)+'</span>':'')+'</div>';
}
function runtimeOverviewMeta(j,activeItem){
  if(!activeItem)return '';
  const cat=(j.catalog||[]).find(c=>String(c?.id||'')===String(activeItem.id||''));
  const merged={...cat,...activeItem};
  const ollamaSub=ollamaCatalogSubtitle(merged);
  if(ollamaSub)return ollamaSub;
  const gk=String(j.gateway_model_key||'').trim();
  if(gk){
    const row=(modelHub.rows||[]).find(r=>r.model_key===gk);
    if(row?.quantization)return String(row.quantization).trim();
  }
  return '';
}
function renderServerMetricsPanel(snapshot){
  if(!snapshot)return '';
  const status=String(snapshot.status||'unavailable');
  const labels={
    complete:'采集正常',skipped:'未采集',unsupported:'端点未启用',
    needs_model:'需要模型标识',unavailable:'暂不可用',timeout:'采样超时',
    cancelled:'采样已取消',invalid_response:'响应格式异常',stale:'数据已过期'
  };
  const samples=Array.isArray(snapshot.samples)?snapshot.samples:[];
  const processInstanceText=String(snapshot.process_instance_id||'').trim();
  const age=Number(snapshot.age_ms);
  const ageText=Number.isFinite(age)&&age>=0?' · '+Math.round(age/1000)+' 秒前':'';
  const sampled=snapshot.sampled_at?new Date(snapshot.sampled_at).toLocaleString():'';
  const reason=String(snapshot.reason_code||'').trim();
  const rows=samples.map(item=>{
    const name=String(item?.name||'未知指标');
    const type=String(item?.type||'unknown');
    const valueText=String(item?.value??'—');
    const labelText=item?.labels&&Object.keys(item.labels).length?JSON.stringify(item.labels):'—';
    const deltaStatus=String(item?.delta_status||'');
    const deltaReason=String(item?.delta_reason||'');
    const deltaText=deltaStatus==='comparable'
      ?String(item.delta_value??'—')+' / '+String(item.window_ms??'—')+' ms'
      :deltaStatus==='not_comparable'?'不可比较：'+(deltaReason||'原因未知'):'—';
    return '<tr><td title="'+esc(String(item?.help||''))+'"><code>'+esc(name)+'</code></td>'+
      '<td>'+esc(type)+'</td><td class="tabular-nums">'+esc(valueText)+'</td>'+
      '<td><code class="runtime-server-metric-labels">'+esc(labelText)+'</code></td>'+
      '<td>'+esc(deltaText)+'</td></tr>';
  }).join('');
  const table=samples.length
    ?'<div class="runtime-server-metrics-table"><table><thead><tr><th>指标</th><th>类型</th><th>值</th><th>标签</th><th>Counter 窗口增量</th></tr></thead><tbody>'+rows+'</tbody></table></div>'
    :'<p class="runtime-server-metrics-empty">当前没有可用指标</p>';
  return '<section class="runtime-server-metrics"><div class="runtime-server-metrics-head">'+
    '<strong>llama.cpp Server Metrics</strong><span class="pill '+(status==='complete'?'pill-loaded':'pill-warn')+'">'+esc(labels[status]||status)+'</span>'+
    '<span class="runtime-server-metrics-meta">'+esc(samples.length+' 项指标'+ageText+(sampled?' · '+sampled:''))+'</span></div>'+
    (reason?'<p class="runtime-server-metrics-reason">诊断状态：<code>'+esc(reason)+'</code></p>':'')+
    (processInstanceText?'<p class="runtime-server-metrics-meta">进程实例标识：<code>'+esc(processInstanceText)+'</code></p>':'<p class="runtime-server-metrics-meta">进程身份不可用；Counter 窗口增量不会跨采样计算</p>')+
    '<details><summary>查看动态发现的指标</summary>'+table+'</details></section>';
}
function renderRuntimeOverview(){
  const el=$('runtimeOverviewSummary');
  if(!el)return;
  const j=modelHub.runtime||{};
  const noteEl=$('runtimeUnloadNote');
  if(modelHub.runtimeError){
    el.innerHTML='<div class="runtime-hero runtime-hero-idle"><div class="runtime-hero-icon" aria-hidden="true"></div><div class="runtime-hero-copy"><p class="runtime-hero-title">运行时状态暂不可用</p><p class="runtime-hero-desc">已登记的本地模型仍可查看和管理；请检查当前后端地址及服务状态后刷新。</p></div></div>';
    if(noteEl)noteEl.innerHTML='';
    return;
  }
  const backend=String(j.backend||'llamacpp').toLowerCase();
  const backendLabel=backend==='ollama'?'Ollama':'llama.cpp';
  const loaded=Array.isArray(j.loaded)?j.loaded:[];
  const configured=String(j.gateway_model||'').trim();
  const activeItem=findActiveRuntimeItem(j);
  const activeModel=String(activeItem?.id||'').trim();
  const typeLine=runtimeOverviewMeta(j,activeItem);
  const disk=Number(activeItem?.disk_bytes||0);
  const vram=Number(activeItem?.vram_bytes||0);
  const ram=Number(activeItem?.ram_bytes||0);
  if(!loaded.length){
    const idleHint=backend==='ollama'
      ?'在下方 <strong>Ollama模型</strong> 中选择条目并点击「加载」'
      :'在下方 <strong>Llama 模型（GGUF）</strong> 中选择条目并点击「加载」';
    const routeHint=configured?'<p class="runtime-hero-desc">Gateway 默认目标：<code>'+esc(configured)+'</code>（当前未确认加载）</p>':'';
    el.innerHTML='<div class="runtime-hero runtime-hero-idle"><div class="runtime-hero-icon" aria-hidden="true"></div><div class="runtime-hero-copy"><p class="runtime-hero-title">暂无模型在内存中</p><p class="runtime-hero-desc">'+idleHint+'</p>'+routeHint+'</div></div>';
  }else if(!activeItem){
    const names=loaded.map(x=>'<code>'+esc(x.id||'未知模型')+'</code> '+ggufLoadPill(x.status)).join('、');
    const routeHint=configured?'<p class="runtime-hero-desc">Gateway 默认目标：<code>'+esc(configured)+'</code>；该目标不在当前已加载清单中。</p>':'';
    el.innerHTML='<div class="runtime-hero runtime-hero-live"><div class="runtime-hero-main"><div class="runtime-hero-badges"><span class="pill pill-loaded">内存中 '+loaded.length+' 个模型</span><span class="pill pill-backend">'+esc(backendLabel)+'</span></div><div class="runtime-hero-desc">'+names+'</div>'+routeHint+'</div></div>';
  }else{
    const displayName=activeModel||'—';
    const status=String(activeItem.status||'loaded').toLowerCase();
    const stateLabel=status==='sleeping'?'已加载（休眠）':status==='loading'?'加载中':'已加载';
    const metrics=[];
    if(typeLine)metrics.push(runtimeMetricCell('规格',typeLine,''));
    if(disk>0)metrics.push(runtimeMetricCell('文件大小',fmtSize(disk),''));
    if(backend!=='ollama'){
      if(vram>0)metrics.push(runtimeMetricCell('显存',fmtSize(vram),''));
      if(ram>0)metrics.push(runtimeMetricCell('内存',fmtSize(ram),''));
    }
    const metricsHtml=metrics.length?'<div class="runtime-hero-metrics">'+metrics.join('')+'</div>':'';
    const routeHint=configured&&modelHub._idMatch(configured,displayName)?'':'<p class="runtime-hero-desc">Gateway 默认目标：<code>'+esc(configured||'未配置')+'</code></p>';
    el.innerHTML='<div class="runtime-hero runtime-hero-live'+(metrics.length?'':' runtime-hero-live-compact')+'"><div class="runtime-hero-main"><div class="runtime-hero-badges"><span class="pill pill-loaded">'+stateLabel+'</span><span class="pill pill-backend">'+esc(backendLabel)+'</span></div><span class="runtime-model-pill" title="'+esc(displayName)+'">'+esc(displayName)+'</span>'+routeHint+'</div>'+metricsHtml+'</div>';
  }
  el.insertAdjacentHTML('beforeend',renderServerMetricsPanel(j.server_metrics));
  if(noteEl){
    const notices=[];
    if(j.hot_unload_supported===false)notices.push(j.unload_note||'当前 llama.cpp 不支持热加载/卸载其它模型');
    if(modelHub.runtimeWarning)notices.push(modelHub.runtimeWarning);
    noteEl.innerHTML=notices.map(x=>'<span class="warn">'+esc(x)+'</span>').join(' ');
  }
}
function backendLabel(id, backends=[]) {
  const item=backends.find(x=>String(x.id||'')===String(id||''));
  if(item?.label)return String(item.label);
  return id==='ollama'?'Ollama':id==='llamacpp'?'llama.cpp':String(id||'推理后端');
}
function populateBackendOptions(backends, active) {
  const select=$('beBackend');
  if(!select)return;
  const list=Array.isArray(backends)&&backends.length?backends:[
    {id:'llamacpp',label:'llama.cpp',kind:'local'},
    {id:'ollama',label:'Ollama',kind:'local'},
  ];
  const target=String(active||select.value||'llamacpp');
  select.innerHTML=list.map(item=>{
    const id=String(item.id||'').trim();
    if(!id)return '';
    const kind=String(item.kind||'local');
    const model=kind==='remote'&&item.default_model?' · '+String(item.default_model):'';
    return '<option value="'+esc(id)+'" data-kind="'+esc(kind)+'">'+esc(String(item.label||id)+model)+'</option>';
  }).join('');
  select.value=list.some(item=>String(item.id||'')===target)?target:(list[0]?.id||'llamacpp');
}
function selectedBackendOption() {
  const select=$('beBackend');
  return select?.selectedOptions?.[0]||null;
}
function backendProbeStatus(block){
  if(block?.configured_remote===true)return{cls:'ok',text:'已配置 · 首次推理时验证连接'};
  if(!block?.base_url)return{cls:'muted',text:'未配置'};
  if(block.reachable===true)return{cls:'ok',text:'可达'};
  if(block.reachable===false){
    const hint=String(block.error||'').trim();
    return{cls:'err',text:hint?('不可达 · '+hint.slice(0,80)):'不可达'};
  }
  const checks=block.checks||[];
  if(!checks.length)return{cls:'muted',text:'未配置'};
  const ok=checks.every(c=>Number(c.http_status)>=200&&Number(c.http_status)<400);
  const partial=checks.some(c=>Number(c.http_status)>=200&&Number(c.http_status)<400);
  if(ok)return{cls:'ok',text:'可达'};
  if(partial)return{cls:'warn',text:'部分可达'};
  return{cls:'err',text:'不可达'};
}
function renderBackendProbeCompact(j){
  const active=(j.active||{}).backend||'llamacpp';
  const backends=Array.isArray(j.available_backends)?j.available_backends:[
    {id:'llamacpp',label:'llama.cpp',kind:'local'},
    {id:'ollama',label:'Ollama',kind:'local'},
  ];
  populateBackendOptions(backends,active);
  const activeItem=backends.find(x=>String(x.id||'')===active);
  const activeLabel=backendLabel(active,backends);
  const activeRemote=activeItem?.kind==='remote';
  const probes=j.upstream_probes||{};
  const pills=[];
  for(const item of backends){
    const id=String(item.id||'');
    const block=probes[id];
    if(!id||!block?.base_url)continue;
    const status=backendProbeStatus(block);
    const current=id===active?' backend-probe-pill-current':'';
    pills.push('<span class="backend-probe-pill '+status.cls+current+'">'+esc(backendLabel(id,backends))+' · '+esc(status.text)+'</span>');
  }
  const proxyOk=j.health==='ok';
  const head=activeRemote
    ?'<span class="backend-probe-lead ok">当前 '+esc(activeLabel)+' · 已配置（首次推理时验证连接）</span>'
    :proxyOk
      ?'<span class="backend-probe-lead ok">当前 '+esc(activeLabel)+' · 代理可达</span>'
      :'<span class="backend-probe-lead err">当前 '+esc(activeLabel)+' · '+esc(j.health||'异常')+'</span>';
  const body=pills.length?'<div class="backend-probe-pills">'+pills.join('')+'</div>':'';
  return head+body;
}
let backendProbeInFlight=false;
function applyBackendProbeUI(j){
  const probeEl=$('backendProbe');
  if(!probeEl)return;
  probeEl.innerHTML=renderBackendProbeCompact(j);
}
async function probeUpstream(ev){
  ev?.preventDefault?.();
  if(backendProbeInFlight)return;
  const btn=$('btnProbeUpstream');
  const probeEl=$('backendProbe');
  backendProbeInFlight=true;
  if(btn){btn.disabled=true;btn.setAttribute('aria-busy','true');}
  if(probeEl)probeEl.innerHTML='<span class="muted">检测中…</span>';
  try{
    const j=await api('/api/admin/backend?refresh=1');
    applyBackendProbeUI(j);
    if(probeEl)probeEl.dataset.probed='1';
    if($('dashboard')?.classList.contains('active'))await loadSystem({ force: 'lite' }).catch(()=>{});
  }catch(e){
    if(probeEl)probeEl.innerHTML='<span class="err">'+esc(e.message)+'</span>';
  }finally{
    backendProbeInFlight=false;
    if(btn){btn.disabled=false;btn.removeAttribute('aria-busy');}
  }
}
$('btnProbeUpstream')?.addEventListener('click',probeUpstream);
function renderRuntimeCatalog(){
  renderRuntimeOverview();
}
async function loadRuntime(){
  await modelHub.refresh();
  renderRuntimeOverview();
}

function applyBackendFormFromStore(){
  const a=adminStore.upstream;
  if($('beBackend')&&a?.backend){
    const select=$('beBackend');
    if([...select.options].some(option=>option.value===a.backend))select.value=a.backend;
  }
  const probeEl=$('backendProbe');
  if(probeEl&&!probeEl.dataset.probed){
    probeEl.innerHTML='<span class="muted small">点击「检测连通性」查看后端状态；远程连接在首次推理时验证</span>';
  }
}
async function loadBackendForm(opts={}){
  const probeOnly=opts.probeOnly===true;
  const probeEl=$('backendProbe');
  try{
    const j=await api(probeOnly?'/api/admin/backend?refresh=1':'/api/admin/backend');
    const active=(j.active||{}).backend||'llamacpp';
    populateBackendOptions(j.available_backends,active);
    if(probeOnly){
      applyBackendProbeUI(j);
      if(probeEl)probeEl.dataset.probed='1';
    }else if(probeEl&&!probeEl.dataset.probed){
      if(String(j.health||'').startsWith('remote Provider configured')){
        applyBackendProbeUI(j);
      }else{
        probeEl.innerHTML='<span class="muted small">点击「检测连通性」查看后端状态；远程连接在首次推理时验证</span>';
      }
    }
  }catch(e){
    if(probeEl)probeEl.innerHTML='<span class="err">'+esc(e.message)+'</span>';
  }
}
async function saveBackend(){
  const selected=selectedBackendOption();
  const backend=$('beBackend')?.value||'llamacpp';
  const targetRemote=selected?.dataset?.kind==='remote';
  const targetLabel=selected?.textContent||backend;
  const cur=(await api('/api/admin/backend')).active||{};
  const probeEl=$('backendProbe');
  if(backend===cur.backend){
    if(probeEl)probeEl.innerHTML='<span class="muted">当前已是该上游服务</span>';
    return;
  }
  const confirmText=targetRemote
    ?'切换到远程 Provider「'+targetLabel+'」后，将使用其配置的默认模型；不会卸载当前本地模型。是否继续？'
    :'切换本地推理后端可能卸载旧本地后端已加载的模型，是否继续？';
  if(!confirm(confirmText))return;
  if(probeEl)probeEl.textContent='切换中…';
  try{
    const result=await api('/api/admin/backend',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({backend})});
    const active=result.active||{};
    if(probeEl){
      probeEl.innerHTML=targetRemote
        ?'<span class="ok">已切换到远程 Provider · 默认模型 '+esc(active.model_name||'配置模型')+'。远程连接将在首次推理时验证，本地模型不受影响。</span>'
        :'<span class="ok">已切换本地后端，请在「本地模型」中加载模型</span>';
    }
    adminStore.reset();
    await loadSystem({force:'bootstrap'});
    await loadBackendForm();
    if(!targetRemote&&$('models-local')?.classList.contains('active'))await loadRuntime();
    await loadModels();
    if(!targetRemote&&$('benchmark')?.classList.contains('active'))await loadBenchPanel({resume:false});
  }catch(e){if(probeEl)probeEl.innerHTML='<span class="err">'+esc(e.message)+'</span>'}
}
async function unloadAllUpstream(){
  if(!confirm('卸载当前后端全部已加载模型？'))return;
  try{
    await api('/api/admin/backend/unload-all',{method:'POST'});
    if($('dashboard')?.classList.contains('active'))await loadSystem({ force: 'lite' }).catch(()=>{});
    await loadRuntime();
    await loadModels();
  }catch(e){setGgufMsg('err',e.message)}
}

// 半圆表盘：0% 在左侧水平、100% 在右侧水平（弧经下方）
const GAUGE={cx:100,cy:106,rDial:84,rTrack:64,rTickOut:70,rTickIn:56,rNum:46,angMin:180,angMax:0};
function gaugePolar(cx,cy,r,deg){
  const rad=deg*Math.PI/180;
  return [cx+r*Math.cos(rad),cy+r*Math.sin(rad)];
}
function gaugeSweepDeg(a,b){
  let s=b-a;
  while(s<=0)s+=360;
  return s;
}
function gaugeArcD(cx,cy,r,deg1,deg2){
  const [x1,y1]=gaugePolar(cx,cy,r,deg1);
  const [x2,y2]=gaugePolar(cx,cy,r,deg2);
  const sweep=gaugeSweepDeg(deg1,deg2);
  const large=sweep>180?1:0;
  return `M ${x1.toFixed(2)} ${y1.toFixed(2)} A ${r} ${r} 0 ${large} 1 ${x2.toFixed(2)} ${y2.toFixed(2)}`;
}
function gaugeValueDeg(pct){
  const {angMin,angMax}=GAUGE;
  const sweep=gaugeSweepDeg(angMin,angMax);
  return angMin+(pct/100)*sweep;
}
const GAUGE_THEMES={
  cpu:{zone:'#22d3ee',needle:'#67e8f9',faceTop:'#1a2a3a',faceBot:'#091018'},
  mem:{zone:'#818cf1',needle:'#c4b5fd',faceTop:'#252040',faceBot:'#0c0a18'},
  disk:{zone:'#34d399',needle:'#6ee7b7',faceTop:'#142820',faceBot:'#081210'},
};
function gaugeColorsForPct(p,themeKey){
  const theme=GAUGE_THEMES[themeKey]||GAUGE_THEMES.cpu;
  if(p>=90)return{zone:'#ef4444',needle:'#f87171'};
  if(p>=75)return{zone:'#fbbf24',needle:'#fcd34d'};
  return{zone:theme.zone,needle:theme.needle};
}
function gaugeNeedleColor(p,themeKey='cpu'){
  return gaugeColorsForPct(p,themeKey).needle;
}
function gaugeZoneColor(p,themeKey='cpu'){
  return gaugeColorsForPct(p,themeKey).zone;
}
function gaugeSVG(pct,gradId,themeKey='cpu'){
  const p=Math.max(0,Math.min(100,Number(pct)||0));
  const theme=GAUGE_THEMES[themeKey]||GAUGE_THEMES.cpu;
  const {cx,cy,rDial,rTrack,rTickOut,rTickIn,rNum,angMin,angMax}=GAUGE;
  const needleAng=gaugeValueDeg(p);
  let ticks='',nums='';
  for(let v=0;v<=100;v+=5){
    const deg=gaugeValueDeg(v);
    const major=v%25===0;
    const [x1,y1]=gaugePolar(cx,cy,rTickOut,deg);
    const [x2,y2]=gaugePolar(cx,cy,major?rTickIn-3:rTickIn,deg);
    ticks+=`<line class="gauge-tick${major?' gauge-tick-major':''}" x1="${x1.toFixed(1)}" y1="${y1.toFixed(1)}" x2="${x2.toFixed(1)}" y2="${y2.toFixed(1)}"/>`;
    if(v%50===0){
      const [nx,ny]=gaugePolar(cx,cy,rNum,deg);
      nums+=`<text class="gauge-num" x="${nx.toFixed(1)}" y="${(ny+4).toFixed(1)}" text-anchor="middle">${v}</text>`;
    }
  }
  const {zone:zoneCol,needle:needleCol}=gaugeColorsForPct(p,themeKey);
  const faceArc=gaugeArcD(cx,cy,rDial,angMin,angMax);
  const trackArc=gaugeArcD(cx,cy,rTrack,angMin,angMax);
  const zoneArc=`<path class="gauge-arc-zone" d="${p>0.5?gaugeArcD(cx,cy,rTrack,angMin,needleAng):''}" stroke="${p>0.5?zoneCol:'none'}"/>`;
  const rNeedle=rTrack-10;
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
      <line class="gauge-needle-line" x1="${cx}" y1="${cy}" x2="${cx+rNeedle}" y2="${cy}" stroke="${needleCol}" stroke-width="3.2"/>
    </g>
    <circle class="gauge-hub-ring" cx="${cx}" cy="${cy}" r="8"/>
    <circle cx="${cx}" cy="${cy}" r="4" fill="#64748b"/>
  </svg>`;
}
const GAUGE_ANIM_MS=920;
let gaugeShown={cpu:0,mem:0,disk:0};
let gaugeAnimToken=0;
let gaugeDomReady=false;
function easeOutCubic(t){return 1-Math.pow(1-t,4)}
function renderGaugeNeedle(svg,pct,themeKey='cpu'){
  if(!svg)return;
  const p=Math.max(0,Math.min(100,Number(pct)||0));
  const needleAng=gaugeValueDeg(p);
  const {zone,needle}=gaugeColorsForPct(p,themeKey);
  const g=svg.querySelector('.gauge-needle');
  if(g)g.setAttribute('transform',`rotate(${needleAng.toFixed(2)} ${GAUGE.cx} ${GAUGE.cy})`);
  const zoneEl=svg.querySelector('.gauge-arc-zone');
  if(zoneEl){
    if(p<=0.5){zoneEl.setAttribute('d','');zoneEl.setAttribute('stroke','none')}
    else{
      zoneEl.setAttribute('d',gaugeArcD(GAUGE.cx,GAUGE.cy,GAUGE.rTrack,GAUGE.angMin,needleAng));
      zoneEl.setAttribute('stroke',zone);
    }
  }
  const line=svg.querySelector('.gauge-needle-line');
  if(line)line.setAttribute('stroke',needle);
}
function animateGauges(items,animate){
  items=items.map(it=>({...it,target:Math.max(0,Math.min(100,Number(it.target)||0))}));
  if(!animate||items.some(it=>!it.svg)){
    items.forEach(it=>{
      gaugeShown[it.key]=it.target;
      renderGaugeNeedle(it.svg,it.target,it.theme);
      if(it.readout)it.readout.textContent=it.target.toFixed(1)+'%';
    });
    return;
  }
  const token=++gaugeAnimToken;
  const starts=items.map(it=>gaugeShown[it.key]??0);
  const t0=performance.now();
  function frame(now){
    if(token!==gaugeAnimToken)return;
    const u=Math.min(1,(now-t0)/GAUGE_ANIM_MS);
    const e=easeOutCubic(u);
    items.forEach((it,i)=>{
      const v=starts[i]+(it.target-starts[i])*e;
      renderGaugeNeedle(it.svg,v,it.theme);
      if(it.readout)it.readout.textContent=v.toFixed(1)+'%';
    });
    if(u<1)requestAnimationFrame(frame);
    else items.forEach(it=>{gaugeShown[it.key]=it.target;});
  }
  requestAnimationFrame(frame);
}
function renderSysRuntimePanel(inf,llamaOk,redisOk,j){
  return `<div class="sys-runtime-panel sys-cell-runtime">
    <div class="sys-runtime-deps-col">
      <p class="sys-runtime-kicker">依赖服务</p>
      <div class="sys-runtime-deps-row">
        <div class="sys-status-chip svc-tile-llama ${llamaOk?'ok':'err'}">
          <span class="sys-status-dot" aria-hidden="true"></span>
          <div class="sys-status-copy"><span class="sys-status-label">上游推理</span><b>${llamaOk?'可达':esc(j.llama||'异常')}</b></div>
        </div>
        <div class="sys-status-chip svc-tile-redis ${redisOk?'ok':'err'}">
          <span class="sys-status-dot" aria-hidden="true"></span>
          <div class="sys-status-copy"><span class="sys-status-label">Redis</span><b>${redisOk?'正常':esc(j.redis||'异常')}</b></div>
        </div>
      </div>
    </div>
    <div class="sys-inference-section">
      <p class="sys-runtime-kicker sys-inference-kicker">推理内存</p>
      <div class="sys-inference">${renderInferenceStats(inf)}</div>
    </div>
  </div>`;
}
function memoryUsagePct(mem,j){
  const fromApi=Number(j?.memory_usage_percent);
  if(Number.isFinite(fromApi)&&fromApi>=0)return Math.min(100,fromApi);
  const total=Number(mem?.total||0);
  const used=Number(mem?.used??(total-Number(mem?.available||0)));
  if(!total)return 0;
  return Math.max(0,Math.min(100,used/total*100));
}
function memoryUsedBytes(mem){
  const total=Number(mem?.total||0);
  const used=Number(mem?.used);
  if(Number.isFinite(used)&&used>=0)return used;
  return Math.max(0,total-Number(mem?.available||0));
}
function barClass(p){return p>=90?'sys-bar err':p>=80?'sys-bar warn':'sys-bar';}

function inferenceBlockReachable(x){
  if(!x)return false;
  return x.reachable===true;
}
function renderInferenceStats(inf){
  inf=inf||{};
  const block=(key,label)=>{
    const x=inf[key];if(!x)return '';
    const ok=inferenceBlockReachable(x);
    const count=Number(x.loaded_count)||0;
    const reachBadge=ok
      ?'<span class="inf-badge ok">可达</span>'
      :'<span class="inf-badge warn">异常</span>';
    return `<article class="inf-card inf-card-compact">
      <div class="inf-card-head"><h4>${label}</h4><div class="inf-card-tags">${reachBadge}<span class="inf-badge">${count} 在内存</span></div></div>
    </article>`;
  };
  const html=block('llamacpp','llama.cpp')+block('ollama','Ollama');
  return html?`<div class="inf-stack inf-stack-compact">${html}</div>`:'<div class="inf-empty">未配置推理后端</div>';
}

/** 首页 CPU/内存：稳定 6s；近 90s 内 1 次显著波动 4s；≥3 次 2s（显著 = CPU 或内存较上次 ≥1.5%） */
const dashboardPoll={
  timer:null,intervalMs:6000,inFlight:false,auxTick:0,lastCpu:null,lastMem:null,changeTimes:[],
  THRESH:1.5,SLOW:6000,MED:4000,FAST:2000,WINDOW_MS:90000,active:false,
  stop(){
    this.active=false;
    if(this.timer){clearTimeout(this.timer);this.timer=null;}
  },
  start(opts={}){
    if(!isAuthenticated())return;
    this.active=true;
    if(opts.skipImmediate)this.schedule(this.intervalMs);
    else this.schedule(0);
  },
  schedule(delayMs){
    if(this.timer)clearTimeout(this.timer);
    if(!this.active)return;
    const d=typeof delayMs==='number'?delayMs:this.intervalMs;
    this.timer=setTimeout(()=>{this.tick().catch(()=>{});},d);
  },
  noteSample(cpuPct,memPct){
    if(this.lastCpu!=null){
      const dCpu=Math.abs(cpuPct-this.lastCpu),dMem=Math.abs(memPct-this.lastMem);
      if(dCpu>=this.THRESH||dMem>=this.THRESH)this.changeTimes.push(Date.now());
    }
    this.lastCpu=cpuPct;
    this.lastMem=memPct;
    const now=Date.now();
    this.changeTimes=this.changeTimes.filter(t=>now-t<this.WINDOW_MS);
    const n=this.changeTimes.length;
    let next=this.SLOW;
    if(n>=3)next=this.FAST;
    else if(n>=1)next=this.MED;
    const changed=next!==this.intervalMs;
    this.intervalMs=next;
    if(changed&&this.active&&!this.inFlight)this.schedule(this.intervalMs);
  },
  async tick(){
    if(!isAuthenticated()||!$('dashboard')?.classList.contains('active')){this.stop();return;}
    if(this.inFlight){this.schedule();return;}
    this.inFlight=true;
    try{
      await loadSystem();
      this.auxTick++;
      const every=this.intervalMs<=4000?2:3;
      if(this.auxTick%every===0){
        await loadDashboardRecent().catch(()=>{});
      }
      const statsEvery=this.intervalMs<=4000?30:10;
      if(this.auxTick%statsEvery===0)await loadDashboardStats().catch(()=>{});
    }finally{
      this.inFlight=false;
      if(this.active&&$('dashboard')?.classList.contains('active'))this.schedule();
    }
  },
};

function systemViewFromAdminStore(){
  const h=adminStore.host||{};
  const dep=adminStore.dependencies||{};
  const up=String(dep.upstream||'');
  const redis=String(dep.redis||'');
  return {
    cpu:h.cpu,
    cpu_usage_percent:h.cpu_usage_percent,
    memory:h.memory||{},
    memory_usage_percent:h.memory_usage_percent,
    disk:h.disk||{},
    llama:up==='ok'?'ok':up||'异常',
    redis:redis==='ok'?'ok':redis||'异常',
    inference:adminStore.inferenceForUI(),
  };
}
async function loadSystem(opts={}){
  const root=$('system');
  if(!root)return;
  const force=opts.force==='bootstrap'||opts.force==='lite'?opts.force:'auto';
  try{
    await adminStore.syncDashboard(force);
  }catch(e){
    root.innerHTML='<span class="err">'+esc(e.message)+'</span>';
    return;
  }
  const j=systemViewFromAdminStore();
  const mem=j.memory||{},disk=j.disk||{};
  const cpuPct=Number(j.cpu_usage_percent)||0;
  const memPct=memoryUsagePct(mem,j);
  const memUsed=memoryUsedBytes(mem);
  const memTotal=Number(mem.total||0);
  const diskTotal=Number(disk.total||0);
  let diskUsed=Number(disk.used);
  if(!Number.isFinite(diskUsed)||diskUsed<0){
    diskUsed=Math.max(0,diskTotal-Number(disk.available||0));
  }
  const diskPct=diskTotal?Math.max(0,Math.min(100,diskUsed/diskTotal*100)):0;
  const llamaOk=j.llama==='ok';
  const redisOk=j.redis==='ok';
  let cpuSvg=root.querySelector('.gauge-cpu');
  let memSvg=root.querySelector('.gauge-mem');
  if(cpuSvg&&!cpuSvg.querySelector('.gauge-needle-line'))root.innerHTML='';
  cpuSvg=root.querySelector('.gauge-cpu');
  memSvg=root.querySelector('.gauge-mem');
  let diskSvg=root.querySelector('.gauge-disk');
  const runtimePanel=root.querySelector('.sys-runtime-panel');
  if(cpuSvg&&memSvg&&diskSvg&&runtimePanel){
    const ro=root.querySelector('.gauge-readout-cpu');
    const rm=root.querySelector('.gauge-readout-mem');
    const rd=root.querySelector('.gauge-readout-disk');
    animateGauges([
      {key:'cpu',target:cpuPct,svg:cpuSvg,readout:ro,theme:'cpu'},
      {key:'mem',target:memPct,svg:memSvg,readout:rm,theme:'mem'},
      {key:'disk',target:diskPct,svg:diskSvg,readout:rd,theme:'disk'},
    ],gaugeDomReady);
    gaugeDomReady=true;
    const so=root.querySelector('.gauge-sub-cpu');if(so)so.textContent=`${esc(j.cpu||'—')} 核`;
    const sm=root.querySelector('.gauge-sub-mem');if(sm)sm.textContent=`${fmtSize(memUsed)} / ${fmtSize(memTotal)}`;
    const sd=root.querySelector('.gauge-sub-disk');if(sd)sd.textContent=`${fmtSize(diskUsed)} / ${fmtSize(diskTotal)}`;
    const inf=root.querySelector('.sys-inference');if(inf)inf.innerHTML=renderInferenceStats(j.inference);
    const tileL=root.querySelector('.svc-tile-llama');if(tileL){
      tileL.className='sys-status-chip svc-tile-llama '+(llamaOk?'ok':'err');
      const b=tileL.querySelector('b');if(b)b.textContent=llamaOk?'可达':(j.llama||'异常');
    }
    const tileR=root.querySelector('.svc-tile-redis');if(tileR){
      tileR.className='sys-status-chip svc-tile-redis '+(redisOk?'ok':'err');
      const b=tileR.querySelector('b');if(b)b.textContent=redisOk?'正常':(j.redis||'异常');
    }
    dashboardPoll.noteSample(cpuPct,memPct);
    return;
  }
  root.innerHTML=`
    <div class="sys-dashboard">
      <div class="gauge-unit gauge-theme-cpu sys-cell-cpu">
        ${gaugeSVG(cpuPct,'gaugeGradCpu','cpu')}
        <div class="gauge-foot">
          <div class="gauge-readout gauge-readout-cpu">${cpuPct.toFixed(1)}%</div>
          <div class="gauge-caption"><span class="gauge-title">宿主机 CPU</span><span class="gauge-sep">·</span><span class="gauge-sub gauge-sub-cpu">${esc(j.cpu||'—')} 核</span></div>
        </div>
      </div>
      <div class="gauge-unit gauge-theme-mem sys-cell-mem">
        ${gaugeSVG(memPct,'gaugeGradMem','mem')}
        <div class="gauge-foot">
          <div class="gauge-readout gauge-readout-mem">${memPct.toFixed(1)}%</div>
          <div class="gauge-caption"><span class="gauge-title">宿主机内存</span><span class="gauge-sep">·</span><span class="gauge-sub gauge-sub-mem">${fmtSize(memUsed)} / ${fmtSize(memTotal)}</span></div>
        </div>
      </div>
      <div class="gauge-unit gauge-theme-disk sys-cell-disk">
        ${gaugeSVG(diskPct,'gaugeGradDisk','disk')}
        <div class="gauge-foot">
          <div class="gauge-readout gauge-readout-disk">${diskPct.toFixed(1)}%</div>
          <div class="gauge-caption"><span class="gauge-title">硬盘</span><span class="gauge-sep">·</span><span class="gauge-sub gauge-sub-disk">${fmtSize(diskUsed)} / ${fmtSize(diskTotal)}</span></div>
        </div>
      </div>
      ${renderSysRuntimePanel(j.inference,llamaOk,redisOk,j)}
    </div>`;
  root.querySelector('.sys-cell-cpu svg')?.classList.add('gauge-cpu');
  root.querySelector('.sys-cell-mem svg')?.classList.add('gauge-mem');
  root.querySelector('.sys-cell-disk svg')?.classList.add('gauge-disk');
  gaugeShown.cpu=cpuPct;gaugeShown.mem=memPct;gaugeShown.disk=diskPct;
  gaugeDomReady=true;
  dashboardPoll.noteSample(cpuPct,memPct);
}

async function listFiles(){
  const repo=$('repo').value.trim(),rev=$('revision').value.trim()||'main';
  if(!repo){$('filesMsg').textContent='请输入仓库名';return}
  $('filesMsg').textContent='读取中…';
  try{
    const j=await api('/api/hf/files?repo='+encodeURIComponent(repo)+'&revision='+encodeURIComponent(rev));
    const fs=j.data||[];$('filesMsg').textContent=`${fs.length} 个 GGUF`;
    $('filesBox').innerHTML=!fs.length?emptyState('无文件'):wrapTable('<tr><th>文件</th><th>大小</th><th>SHA256</th><th></th></tr>'+fs.map(f=>`<tr><td>${esc(f.path)}</td><td>${fmtSize(f.size)}</td><td class="small">${esc(f.sha256||'—')}</td><td><button type="button" class="btn sm" data-hf-file="${esc(f.path)}" data-hf-repo="${esc(repo)}">选择</button></td></tr>`).join(''));
  }catch(e){$('filesMsg').innerHTML='<span class="err">'+esc(e.message)+'</span>'}
}
function chooseFile(file,repo){
  $('filename').value=file;$('repo').value=repo;
  $('displayName').value=file.split('/').pop().replace(/\.gguf$/i,'');
  $('modelKey').value=slug($('displayName').value);
  $('quant').value=(($('displayName').value.match(/(Q[0-9][A-Z0-9_\-]*)/i)||[])[1]||'').toUpperCase();
  $('installForm').style.display='block';
  if(globalThis.ModelDownload?.isGgufLocal?.(repo,file)){
    $('filesMsg').innerHTML='<span class="warn">本地已存在该 GGUF（'+esc(repo+' / '+file)+'），无需重复下载</span>';
  }
}
$('filesBox')?.addEventListener('click',ev=>{
  const btn=ev.target.closest?.('button[data-hf-file]');
  if(!btn)return;
  chooseFile(btn.dataset.hfFile||'',btn.dataset.hfRepo||'');
});

async function installModel(){
  const b={model_key:$('modelKey').value.trim(),display_name:$('displayName').value.trim(),repo:$('repo').value.trim(),revision:$('revision').value.trim()||'main',filename:$('filename').value.trim(),quantization:$('quant').value.trim()};
  if(globalThis.ModelDownload?.isGgufLocal?.(b.repo,b.filename)){
    $('filesMsg').innerHTML='<span class="warn">本地已存在该 GGUF 文件，无需重复下载</span>';
    return;
  }
  try{
    await api('/api/models/install',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(b)});
    $('filesMsg').innerHTML='<span class="ok">任务已创建</span>';
    refreshAll();
  }catch(e){
    const msg=e.message||'';
    $('filesMsg').innerHTML='<span class="err">'+esc(msg.includes('已存在')?msg:'安装失败：'+msg)+'</span>';
  }
}

function renderGatewayModelTable(){
  const el=$('modelTable');if(!el)return;
  const rows=modelHub.tableRows();
  const warnings=$('modelHubWarnings');
  if(warnings){
    const notices=[];
    if(modelHub.rowsError)notices.push('<span class="err">'+esc(modelHub.rowsError)+'</span>');
    if(modelHub.runtimeError)notices.push('<span class="warn">'+esc(modelHub.runtimeError)+'</span>');
    if(modelHub.runtimeWarning)notices.push('<span class="warn">'+esc(modelHub.runtimeWarning)+'</span>');
    warnings.innerHTML=notices.join('<br>');
    warnings.hidden=!notices.length;
  }
  el.innerHTML=!rows.length?(modelHub.rowsError?'':emptyState('暂无 GGUF 模型；请检查共享模型目录或从「模型下载」安装')):wrapTable(
    '<tr><th>模型名称</th><th>模型 ID / 键</th><th>大小</th><th>状态</th><th>操作</th></tr>'+
    rows.map(x=>{
      const rt=x._runtime||modelHub.runtimeItemForRow(x);
      const managed=x._managed===true;
      const modelKey=String(x.model_key||'').trim();
      const modelId=String(rt?.id||x.upstream_name||x.filename||x.local_path||'').trim();
      const name=String(x.display_name||rt?.display_name||x.filename||modelId||modelKey).trim();
      const st=modelHub.loadState(x);
      const sizeBytes=modelHub.diskBytes(x);
      const routerMode=Boolean(modelHub.runtime?.hot_unload_supported);
      const canOperate=Boolean(routerMode&&!modelHub.runtimeError&&(rt?.registered||rt?.loaded));
      const loadButton=canOperate&&rt?.registered&&!rt.loaded&&st!=='loading'
        ?inferButtonHtml('llamacpp','load',{modelId,modelKey,title:'Llama 模型 · 加载 · '+(modelId||modelKey),label:'加载'})
        :'';
      const unloadButton=canOperate&&rt.loaded
        ?inferButtonHtml('llamacpp','unload',{modelId,modelKey,title:'Llama 模型 · 卸载 · '+(modelId||modelKey),confirmMsg:'从 llama.cpp 卸载 '+(modelId||modelKey)+'？',label:'卸载'})
        :'';
      const deleteButton=managed&&modelKey
        ?'<button type="button" class="btn sm danger" data-model-delete="gguf" data-model-key="'+esc(modelKey)+'">删除</button>'
        :rt?.disk_present&&rt?.path
          ?'<button type="button" class="btn sm danger" data-model-delete="gguf-disk" data-model-path="'+esc(String(rt.path))+'">删除</button>'
          :'';
      const defaultPill=rt?.active?' <span class="pill pill-active">Gateway 默认</span>':'';
      return '<tr><td>'+esc(name)+(x.quantization?'<div class="small muted">'+esc(x.quantization)+'</div>':'')+defaultPill+'</td>'+
        '<td><code>'+esc(modelKey||modelId||'—')+'</code></td>'+
        '<td class="tabular-nums">'+fmtSize(sizeBytes)+'</td>'+
        '<td>'+ggufLoadPill(st)+'</td>'+
        '<td><div class="td-actions">'+loadButton+unloadButton+deleteButton+'</div></td></tr>';
    }).join(''));
}
async function loadModels(){
  await modelHub.refresh();
  renderGatewayModelTable();
}
async function selectGatewayModel(key){
  return inferenceOp('llamacpp','load',{model_key:key},`本地 GGUF · 加载 · ${key}`);
}
function setDownloadFeedback(html){
  const cm=$('catalogMsg');
  if(cm){cm.innerHTML=html;return}
  setGlobalMsg(html);
}
async function modelAction(k,a){
  if(a==='load'||a==='unload'){
    await inferenceOp('llamacpp',a,{model_key:k},`本地 GGUF · ${a} · ${k}`);
    return;
  }
  try{
    await api('/api/models/'+encodeURIComponent(k)+'/'+a,{method:'POST'});
    if(a==='cancel'||a==='retry'){
      if(a==='retry')setDownloadFeedback('<span class="ok">已继续下载 <code>'+esc(k)+'</code></span>');
      await refreshAll();
    }else await refreshModelsOnly();
  }catch(e){
    setDownloadFeedback('<span class="err">'+esc(e.message)+'</span>');
    setGgufMsg('err',e.message);
  }
}
async function retryHFDownload(modelKey,btn){
  modelKey=String(modelKey||'').trim();
  if(!modelKey)return;
  if(btn){btn.disabled=true;btn.setAttribute('aria-busy','true')}
  setDownloadFeedback('<span class="muted">正在继续下载 <code>'+esc(modelKey)+'</code>…</span>');
  try{
    await api('/api/models/'+encodeURIComponent(modelKey)+'/retry',{method:'POST'});
    setDownloadFeedback('<span class="ok">已继续下载 <code>'+esc(modelKey)+'</code>，请看下方进度</span>');
    await loadDownloads();
    $('downloads')?.scrollIntoView({block:'nearest',behavior:'smooth'});
  }catch(e){
    setDownloadFeedback('<span class="err">'+esc(e.message)+'</span>');
  }finally{
    if(btn){btn.disabled=false;btn.removeAttribute('aria-busy')}
  }
}
async function deleteModel(k){
  if(!isAuthenticated()){setGgufMsg('err','请先登录控制台');return}
  if(!confirm('确定删除磁盘上的 GGUF 及数据库记录？'))return;
  try{
    await api('/api/models/'+encodeURIComponent(k),{method:'DELETE'});
    setGgufMsg('ok','已删除');
    await modelHub.refresh({force:true});
    renderGatewayModelTable();
    renderRuntimeCatalog();
  }catch(e){setGgufMsg('err',e.message)}
}


async function deleteDiskGGUFModel(modelPath){
  if(!isAuthenticated()){setGgufMsg('err','请先登录控制台');return}
  if(!confirm('确定从共享模型目录永久删除该 GGUF 模型？如果模型由多个分片组成，所有分片都会一起删除；此操作不可恢复。'))return;
  try{
    const result=await api('/api/admin/gguf/delete',{
      method:'DELETE',
      headers:{'Content-Type':'application/json'},
      body:JSON.stringify({model_path:modelPath})
    });
    const deleted=Number(result.deleted_files||0);
    setGgufMsg(result.warning?'warn':'ok',result.warning||('已删除 GGUF 模型（'+deleted+' 个文件）'));
    await modelHub.refresh({force:true});
    renderGatewayModelTable();
    renderRuntimeCatalog();
  }catch(e){
    setGgufMsg('err',e.message);
  }
}

function isTerminalDownloadStatus(st){
  const v=String(st||'').toLowerCase();
  return v==='download_failed'||v==='download_cancelled'||v==='failed';
}
function initDownloadsTableActions(){
  const root=$('downloads');
  if(!root||root.dataset.dlBound)return;
  root.dataset.dlBound='1';
  root.addEventListener('click',(ev)=>{
    const btn=ev.target.closest?.('button[data-dl-action]');
    if(!btn||btn.disabled)return;
    const action=btn.dataset.dlAction||'';
    const key=btn.dataset.dlKey||'';
    const name=btn.dataset.dlName||'';
    if(action==='retry-hf')void retryHFDownload(key,btn);
    else if(action==='retry-ollama')void retryOllamaPull(name,btn);
    else if(action==='cancel-hf')void modelAction(key,'cancel');
    else if(action==='cancel-ollama')void cancelOllamaPull(key);
    else if(action==='dismiss')void dismissDownloadRecord(key);
  });
}
async function dismissDownloadRecord(modelKey){
  if(!modelKey)return;
  try{
    await api('/api/downloads/dismiss',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({model_key:modelKey})});
    await loadDownloads();
  }catch(e){
    setGlobalMsg('<span class="err">'+esc(e.message)+'</span>');
  }
}
async function clearFailedDownloads(){
  if(!confirm('清除所有已失败/已取消的下载任务记录？不会删除已成功安装的模型。'))return;
  try{
    const j=await api('/api/downloads/dismiss',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({all_failed:true})});
    const n=Number(j.cleared||0);
    if($('catalogMsg'))$('catalogMsg').innerHTML='<span class="ok">已清除 '+n+' 条记录</span>';
    await loadDownloads();
  }catch(e){
    setGlobalMsg('<span class="err">'+esc(e.message)+'</span>');
  }
}
async function loadDownloads(){
  const el=$('downloads');
  if(!el)return;
  let rows=[];
  try{
    const j=await api('/api/downloads');
    rows=j.data||[];
  }catch(e){
    el.innerHTML='<span class="err">无法加载下载任务：'+esc(e.message)+'</span>';
    return;
  }
  const hasFailed=rows.some(x=>isTerminalDownloadStatus(x.status));
  const clearBtn=$('btnClearFailedDownloads');
  if(clearBtn)clearBtn.hidden=!hasFailed;
  el.innerHTML=!rows.length?emptyState('无任务'):wrapTable(
    '<tr><th>任务</th><th>类型</th><th>状态</th><th>进度</th><th>大小</th><th></th></tr>'+
    rows.map(x=>{
      const label=x.kind==='ollama'?(x.name||x.model_key):x.model_key;
      const kind=x.kind==='ollama'?'Ollama':'HF GGUF';
      const p=x.total?Math.min(100,x.done/x.total*100):0;
      const pct=x.total?p.toFixed(0)+'%':(x.status==='downloading'?'…':'—');
      let actions='';
      if(x.status==='downloading'){
        actions=x.kind==='ollama'
          ?`<button type="button" class="btn sm" data-dl-action="cancel-ollama" data-dl-key="${esc(x.model_key)}">取消</button>`
          :`<button type="button" class="btn sm" data-dl-action="cancel-hf" data-dl-key="${esc(x.model_key)}">取消</button>`;
      }else if(isTerminalDownloadStatus(x.status)){
        if(x.kind==='ollama'&&(x.name||x.model_key)){
          const pullName=String(x.name||'').trim()||String(x.model_key||'').replace(/^ollama\//,'');
          if(pullName){
            actions=`<button type="button" class="btn sm primary" data-dl-action="retry-ollama" data-dl-name="${esc(pullName)}">继续下载</button> `;
          }
        }else if(x.kind!=='ollama'&&x.model_key){
          actions=`<button type="button" class="btn sm primary" data-dl-action="retry-hf" data-dl-key="${esc(x.model_key)}">继续下载</button> `;
        }
        actions+=`<button type="button" class="btn sm ghost" data-dl-action="dismiss" data-dl-key="${esc(x.model_key)}">清除</button>`;
      }
      const showErr=x.error&&String(x.status||'').toLowerCase()!=='downloading';
      const errHint=showErr?`<div class="small err">${esc(x.error)}</div>`:'';
      return `<tr><td><code>${esc(label)}</code>${errHint}</td><td class="small">${esc(kind)}</td><td>${statusPill(x.status)}</td><td><span class="progress"><i style="width:${p}%"></i></span> ${pct}</td><td>${fmtSize(x.done)}${x.total?'/'+fmtSize(x.total):''}</td><td>${actions}</td></tr>`;
    }).join(''));
}
async function cancelOllamaPull(jobId){
  if(!jobId)return;
  try{
    await api('/api/admin/ollama/pull/cancel',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({job_id:jobId})});
    await loadDownloads();
    if($('catalogMsg'))$('catalogMsg').innerHTML='<span class="muted">已请求取消 Ollama 下载</span>';
  }catch(e){
    setGlobalMsg('<span class="err">'+esc(e.message)+'</span>');
  }
}
async function retryOllamaPull(name,btn){
  name=String(name||'').trim();
  if(!name)return;
  if(btn){btn.disabled=true;btn.setAttribute('aria-busy','true')}
  setDownloadFeedback('<span class="muted">正在继续 Ollama 下载 <code>'+esc(name)+'</code>…</span>');
  try{
    await api('/api/admin/ollama/pull',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name})});
    setDownloadFeedback('<span class="ok">已继续 Ollama 下载 <code>'+esc(name)+'</code>，请看下方进度</span>');
    await loadDownloads();
    $('downloads')?.scrollIntoView({block:'nearest',behavior:'smooth'});
  }catch(e){
    setDownloadFeedback('<span class="err">'+esc(e.message)+'</span>');
  }finally{
    if(btn){btn.disabled=false;btn.removeAttribute('aria-busy')}
  }
}

async function loadKeysPanel(){
  adminKeyStatus();
  if(!getSavedAdminKey()){
    const table=$('keyTable');
    if(table)table.innerHTML=emptyState('请先在上方输入并验证管理员 API Key，再管理服务密钥');
    return;
  }
  await loadKeys();
}
async function loadKeys(){
  const table=$('keyTable');
  try{
    const j=await apiServiceKeys('/api/admin/keys');const rows=j.data||[];
    table.innerHTML=!rows.length?emptyState('暂无密钥'):wrapTable('<tr><th>名称</th><th>前缀</th><th>状态</th><th>限流</th><th>允许模型</th><th>允许工具</th><th>过期</th><th>操作</th></tr>'+rows.map(x=>`<tr><td>${esc(x.name)}</td><td><code>${esc(x.prefix)}…</code></td><td>${statusPill(x.status)}</td><td>${x.rpm}/分钟</td><td class="small">${esc((x.allowed_models||[]).join(', ')||'全部')}</td><td class="small">${esc((x.allowed_tools||[]).join(', ')||'无')}</td><td class="small">${fmtDate(x.expires_at)}</td><td><div class="td-actions"><button type="button" class="btn sm" onclick="toggleKey(${x.id},'${x.status==='active'?'disable':'enable'}')">${x.status==='active'?'禁用':'启用'}</button><button type="button" class="btn sm" onclick="rotateKey(${x.id})">轮换</button><button type="button" class="btn sm danger" onclick="deleteKey(${x.id})">删除</button></div></td></tr>`).join(''));
  }catch(e){
    if(table)table.innerHTML='<div class="msg"><span class="err">'+esc(e.message)+'</span></div>';
    throw e;
  }
}
let creatingKey = false;
async function createKey(){
  if(creatingKey)return;
  creatingKey=true;
  const button=$('keyCreateButton');
  const wasDisabled=button?.disabled??false;
  if(button)button.disabled=true;
  const models=$('keyModels').value.split(',').map(x=>x.trim()).filter(Boolean);
  try{
    const j=await apiServiceKeys('/api/admin/keys',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:$('keyName').value.trim(),rpm:Number($('keyRPM').value)||60,allowed_models:models,allowed_tools:$('keyTools').value.split(',').map(x=>x.trim()).filter(Boolean),expires_at:$('keyExpires').value.trim()||null})});
    const resultBox=$('newKeyBox');
    if(!j.key)throw new Error('密钥已创建，但服务端未返回明文密钥；请检查接口响应，必要时轮换该密钥。');
    resultBox.style.display='block';
    resultBox.innerHTML='<div class="msg ok" role="status">服务密钥创建成功。该明文仅显示这一次，请立即复制并妥善保存；离开此页面后无法再次查看。</div>'+
      '<label for="createdServiceKey" class="small">新生成的服务密钥</label>'+
      '<textarea id="createdServiceKey" readonly rows="3" spellcheck="false" style="display:block;width:100%;box-sizing:border-box;margin:8px 0;resize:vertical;overflow-wrap:anywhere;word-break:break-all"></textarea>'+
      '<div class="row" style="align-items:center"><button type="button" id="copyCreatedServiceKey" class="btn primary">复制密钥</button><span id="copyCreatedServiceKeyStatus" class="small muted" role="status" aria-live="polite">请复制后保存到安全位置。</span></div>';
    $('createdServiceKey').value=j.key;
    $('copyCreatedServiceKey').addEventListener('click',async()=>{
      const copied=await writeClipboard(j.key);
      const copyButton=$('copyCreatedServiceKey');
      const status=$('copyCreatedServiceKeyStatus');
      if(copied){
        copyButton.textContent='已复制';
        if(status)status.textContent='复制成功，请妥善保存。';
      }else{
        if(status)status.textContent='自动复制失败，请选中上方密钥并手动复制。';
      }
    });
    resultBox.scrollIntoView({block:'nearest',behavior:'smooth'});
    loadKeys().catch(()=>{});
  }catch(e){$('newKeyBox').style.display='block';$('newKeyBox').innerHTML='<span class="err">'+esc(e.message)+'</span>'}
  finally{creatingKey=false;if(button)button.disabled=wasDisabled}
}
async function toggleKey(id,a){try{await apiServiceKeys('/api/admin/keys/'+id+'/'+a,{method:'POST'});loadKeys().catch(()=>{})}catch(e){alert(e.message)}}
async function rotateKey(id){if(!confirm('轮换后旧密钥立即失效，继续？'))return;try{const j=await apiServiceKeys('/api/admin/keys/'+id+'/rotate',{method:'POST'});alert('新密钥：\\n\\n'+j.key);loadKeys().catch(()=>{})}catch(e){alert(e.message)}}
async function deleteKey(id){if(!confirm('确定删除该密钥？'))return;try{await apiServiceKeys('/api/admin/keys/'+id,{method:'DELETE'});loadKeys().catch(()=>{})}catch(e){alert(e.message)}}

const BENCH_STORE_KEY='llm-gateway.bench.v1';
const BENCH_SHARED={
  prompt:'Write a small, correct Objective-C function and explain why it is thread-safe.',
  stop:[],
};
/** 各上游推荐默认（差异主要在 min_p / repeat / thinking） */
const BENCH_PRESETS={
  llamacpp:{
    max_tokens:4000,
    temperature:0.2,
    top_p:0.9,
    top_k:40,
    min_p:0.05,
    frequency_penalty:0,
    presence_penalty:0,
    repeat_penalty:1.1,
    seed:null,
    enable_thinking:null,
  },
  ollama:{
    max_tokens:4000,
    temperature:0.2,
    top_p:0.9,
    top_k:40,
    min_p:null,
    frequency_penalty:0,
    presence_penalty:0,
    repeat_penalty:1.15,
    seed:null,
    enable_thinking:false,
  },
};
let benchPollToken=0;
let benchCurrentBackend='llamacpp';

function benchBackendKey(be){
  return String(be||'').toLowerCase()==='ollama'?'ollama':'llamacpp';
}
function benchDefaultsForBackend(be){
  const key=benchBackendKey(be);
  return {...BENCH_SHARED,...(BENCH_PRESETS[key]||BENCH_PRESETS.llamacpp)};
}
function readBenchStoreRaw(){
  try{return JSON.parse(localStorage.getItem(BENCH_STORE_KEY)||'{}')}catch{return {}}
}
function normalizeBenchStore(){
  const st=readBenchStoreRaw();
  if(st.params&&!st.paramsByBackend){
    const key=benchBackendKey(st.lastBackend||'llamacpp');
    st.paramsByBackend={[key]:st.params};
    delete st.params;
    localStorage.setItem(BENCH_STORE_KEY,JSON.stringify(st));
  }
  return readBenchStoreRaw();
}
function readBenchStore(){
  return normalizeBenchStore();
}
function writeBenchStore(patch){
  const prev=readBenchStore();
  const next={...prev,...patch};
  if(patch.paramsByBackend){
    next.paramsByBackend={...(prev.paramsByBackend||{}),...patch.paramsByBackend};
  }
  localStorage.setItem(BENCH_STORE_KEY,JSON.stringify(next));
}
function persistBenchParams(){
  if(!benchCurrentBackend)return;
  writeBenchStore({
    lastBackend:benchCurrentBackend,
    paramsByBackend:{[benchCurrentBackend]:collectBenchParams()},
  });
}
function updateBenchPresetHint(be){
  const el=$('benchPresetHint');if(!el)return;
  const key=benchBackendKey(be);
  const name=key==='ollama'?'Ollama':'llama.cpp';
  const d=BENCH_PRESETS[key];
  const bits=[
    `参数预设：<b>${esc(name)}</b>（切换上游服务会自动切换；已保存的该后端参数会保留）`,
    d.min_p!=null?`min_p ${d.min_p}`:'min_p 不传',
    `repeat ${d.repeat_penalty}`,
    d.enable_thinking===false?'thinking 关':d.enable_thinking===true?'thinking 开':'thinking 透传',
  ];
  el.innerHTML=bits.join(' · ');
}
function syncBenchParamsForBackend(be){
  const key=benchBackendKey(be);
  benchCurrentBackend=key;
  const st=readBenchStore();
  const saved=st.paramsByBackend?.[key];
  applyBenchParams({...benchDefaultsForBackend(key),...(saved||{})});
  writeBenchStore({lastBackend:key});
  updateBenchPresetHint(key);
}
function benchOptionalNum(id){
  const el=$(id);if(!el)return undefined;
  const t=String(el.value??'').trim();
  if(t==='')return undefined;
  const n=Number(t);
  return Number.isFinite(n)?n:undefined;
}
function benchOptionalInt(id){
  const n=benchOptionalNum(id);
  return n==null?undefined:Math.trunc(n);
}
function benchNumFromInput(id,fallback){
  const n=benchOptionalNum(id);
  return n==null?fallback:n;
}
function benchIntFromInput(id,fallback){
  const n=benchOptionalInt(id);
  return n==null?fallback:n;
}
function collectBenchParams(){
  const stopRaw=($('benchStop')?.value||'').split('\n').map(s=>s.trim()).filter(Boolean);
  const think=$('benchThinking')?.value??'';
  let enable_thinking;
  if(think==='true')enable_thinking=true;
  else if(think==='false')enable_thinking=false;
  const d=benchDefaultsForBackend(benchCurrentBackend);
  return {
    prompt:($('benchPrompt')?.value||'').trim()||d.prompt,
    max_tokens:benchIntFromInput('benchTokens',d.max_tokens),
    temperature:benchNumFromInput('benchTemperature',d.temperature),
    top_p:benchNumFromInput('benchTopP',d.top_p),
    top_k:benchIntFromInput('benchTopK',d.top_k),
    min_p:benchOptionalNum('benchMinP'),
    frequency_penalty:benchNumFromInput('benchFreqPen',d.frequency_penalty),
    presence_penalty:benchNumFromInput('benchPresPen',d.presence_penalty),
    repeat_penalty:benchNumFromInput('benchRepeatPen',d.repeat_penalty),
    seed:benchOptionalInt('benchSeed'),
    enable_thinking,
    stop:stopRaw.length?stopRaw:undefined,
  };
}
function applyBenchParams(p){
  const m={...benchDefaultsForBackend(benchCurrentBackend),...(p||{})};
  if($('benchPrompt'))$('benchPrompt').value=m.prompt??'';
  if($('benchTokens'))$('benchTokens').value=m.max_tokens??'';
  const setNum=(id,v)=>{const el=$(id);if(!el)return;el.value=v==null||v===''?'':v};
  setNum('benchTemperature',m.temperature);
  setNum('benchTopP',m.top_p);
  setNum('benchTopK',m.top_k);
  setNum('benchMinP',m.min_p);
  setNum('benchFreqPen',m.frequency_penalty);
  setNum('benchPresPen',m.presence_penalty);
  setNum('benchRepeatPen',m.repeat_penalty);
  setNum('benchSeed',m.seed);
  if($('benchThinking')){
    if(m.enable_thinking===true)$('benchThinking').value='true';
    else if(m.enable_thinking===false)$('benchThinking').value='false';
    else $('benchThinking').value='';
  }
  if($('benchStop'))$('benchStop').value=(m.stop&&m.stop.length)?m.stop.join('\n'):'';
}
function benchPayloadForApi(target,scenario='performance',promptOverride='',acceptanceCaseID=''){
  const upstream=typeof target==='string'?target:(target?.upstream||'');
  const gatewayKey=(typeof target==='string'?'':(target?.gatewayKey||'')).trim();
  const p=collectBenchParams();
  if(typeof promptOverride==='string'&&promptOverride.trim())p.prompt=promptOverride.trim();
  // 有网关 model_key 时优先用它（小写业务键）；否则 fallback 到上游 loaded 的 model_id。
  // 发往 llama 的 chat body 里 "model" 仍是上游注册名，见 updateBenchReqPreview。
  const body=gatewayKey
    ?{model_key:gatewayKey,prompt:p.prompt,max_tokens:p.max_tokens}
    :{use_active:true,model_id:upstream,prompt:p.prompt,max_tokens:p.max_tokens};
  if(scenario==='tool_performance'||scenario==='tool_performance_auto')body.scenario=scenario;
  if(acceptanceCaseID)body.acceptance_case_id=acceptanceCaseID;
  if(p.temperature!=null)body.temperature=p.temperature;
  if(p.top_p!=null)body.top_p=p.top_p;
  if(p.top_k!=null)body.top_k=p.top_k;
  if(p.min_p!=null)body.min_p=p.min_p;
  if(p.frequency_penalty!=null)body.frequency_penalty=p.frequency_penalty;
  if(p.presence_penalty!=null)body.presence_penalty=p.presence_penalty;
  if(p.repeat_penalty!=null)body.repeat_penalty=p.repeat_penalty;
  if(p.seed!=null)body.seed=p.seed;
  if(p.enable_thinking!=null)body.enable_thinking=p.enable_thinking;
  if(p.stop?.length)body.stop=p.stop;
  return body;
}
function setBenchRunning(running){
  const ids=['benchRunBtn','benchSuiteRunBtn','benchToolRunBtn','benchToolForceRunBtn','benchAcceptanceRunBtn'];
  ids.forEach(id=>{const button=$(id);if(button)button.disabled=!!running;});
  ['benchStopBtn','toolBenchStopBtn','acceptanceBenchStopBtn'].forEach(id=>{const button=$(id);if(button)button.disabled=!running;});
}


let benchHistoryOffset=0;
let benchHistorySelected=new Set();
const BENCH_HISTORY_PAGE_SIZE=20;

async function loadBenchHistory(){
  const list=$('benchHistoryList');
  if(!list)return;
  list.innerHTML='<div class="empty-state">正在加载历史任务…</div>';
  const query=new URLSearchParams({limit:String(BENCH_HISTORY_PAGE_SIZE),offset:String(benchHistoryOffset)});
  const scenario=$('benchHistoryScenario')?.value||'';
  const status=$('benchHistoryStatus')?.value||'';
  if(scenario)query.set('scenario',scenario);
  if(status)query.set('status',status);
  try{
    const result=await api('/api/benchmark?'+query.toString());
    const items=Array.isArray(result.items)?result.items:[];
    const total=Number(result.total)||0;
    if(!items.length){
      list.innerHTML=emptyState('暂无符合条件的 Benchmark 任务');
    }else{
      list.innerHTML=wrapTable('<tr><th>选</th><th>时间</th><th>场景</th><th>验收用例</th><th>模型</th><th>状态</th><th>总耗时</th><th>条件</th><th>详情</th></tr>'+
        items.map(item=>{
          const scenarioLabel=item.scenario==='tool_performance_auto'?'工具调用性能（自然决策）':(item.scenario==='tool_performance'?'工具调用性能（强制调用诊断）':'普通性能');
          const conditionLabel={complete:'完整',partial:'部分',unknown:'未知'}[item.condition_status]||'未知';
          const latency=item.status==='running'||item.status==='cancelling'?'进行中':(item.latency_ms!=null?item.latency_ms+' ms':'未知');
          const model=item.model_id||item.model_key||item.backend||'—';
          return '<tr><td><input type="checkbox" data-bench-compare-id="'+esc(item.job_id)+'" '+(benchHistorySelected.has(item.job_id)?'checked':'')+' '+(item.status!=='completed'?'disabled':'')+' aria-label="选择任务进行比较"></td><td class="small">'+esc(fmtDate(item.started_at))+'</td><td>'+esc(scenarioLabel)+'</td><td>'+(item.acceptance_case_id?'<span class="pill">'+esc(item.acceptance_case_id)+'</span>':'—')+'</td><td class="small">'+esc(model)+'</td><td>'+statusPill(item.status)+'</td><td class="small">'+esc(latency)+'</td><td><span class="pill">'+conditionLabel+'</span></td><td><button type="button" class="btn sm ghost" data-bench-history-id="'+esc(item.job_id)+'" aria-controls="benchHistoryDetail" aria-expanded="false">查看</button></td></tr>';
        }).join(''));
    }
    const page=$('benchHistoryPage');
    if(page)page.textContent=total?((benchHistoryOffset+1)+'–'+Math.min(benchHistoryOffset+items.length,total)+' / '+total):'0 条';
    const prev=$('benchHistoryPrev'),next=$('benchHistoryNext');
    if(prev)prev.disabled=benchHistoryOffset<=0;
    if(next)next.disabled=benchHistoryOffset+items.length>=total;
    updateBenchmarkCompareSelection();
  }catch(error){
    list.innerHTML='<div class="msg err">'+esc(error.message||'历史任务加载失败')+'</div>';
    const page=$('benchHistoryPage');if(page)page.textContent='加载失败';
    $('benchHistoryPrev')&&($('benchHistoryPrev').disabled=true);
    $('benchHistoryNext')&&($('benchHistoryNext').disabled=true);
  }
}


function updateBenchmarkCompareSelection(){
  const button=$('benchHistoryCompareBtn');
  const label=$('benchHistorySelection');
  if(button)button.disabled=benchHistorySelected.size!==2;
  if(label)label.textContent=benchHistorySelected.size+' / 2 项已选择；仅完成且条件完整一致的任务可严格比较';
}

async function openBenchmarkComparison(){
  if(benchHistorySelected.size!==2)return;
  const [left,right]=[...benchHistorySelected];
  const box=$('benchCompareResult');
  if(!box)return;
  box.hidden=false;
  box.style.display='block';
  box.innerHTML='<p class="muted">正在比较…</p>';
  try{
    const query=new URLSearchParams({left,right});
    const result=await api('/api/benchmark/compare?'+query.toString());
    const reasonLabels={
      task_not_completed:'两个任务都必须已完成',
      scenario_mismatch:'测试场景不同',
      condition_incomplete:'至少一个任务的测试条件不完整',
      condition_mismatch:'测试条件指纹不同',
    };
    let body='<h3 style="margin:0 0 8px;font-size:14px">'+(result.comparable?'严格可比':'不可严格比较')+'</h3>';
    if(!result.comparable){
      body+='<ul class="req-hints">'+(result.reasons||[]).map(reason=>'<li>'+esc(reasonLabels[reason]||reason)+'</li>').join('')+'</ul>';
      const missing=[...(result.left?.condition_missing||[]),...(result.right?.condition_missing||[])];
      if(missing.length)body+='<p class="req-note">条件缺失维度：'+esc([...new Set(missing)].join('、'))+'</p>';
    }else{
      const labels={
        latency_ms:'任务总耗时',inference_latency_ms:'Pipeline 耗时',model_load_ms:'模型加载/就绪',
        ttft_ms:'TTFT',input_tokens:'输入 Token',output_tokens:'输出 Token',total_tokens:'总 Token',
        model_decision_ms:'模型决策',tool_execution_total_ms:'工具执行合计',model_final_generation_ms:'模型最终生成',
      };
      const rows=Object.entries(result.deltas||{}).map(([key,value])=>{
        const fmt=v=>v==null?'未知':String(v);
        return '<tr><td>'+esc(labels[key]||key)+'</td><td>'+fmt(value.left)+'</td><td>'+fmt(value.right)+'</td><td>'+fmt(value.right_minus_left)+'</td></tr>';
      }).join('');
      body+='<p class="req-note">差值方向：右侧任务 − 左侧任务；未测指标保持未知。</p>'+
        wrapTable('<tr><th>指标</th><th>左侧</th><th>右侧</th><th>差值</th></tr>'+rows);
    }
    box.innerHTML=body;
  }catch(error){
    box.innerHTML='<p class="err">'+esc(error.message||'比较失败')+'</p>';
  }
}

function benchmarkResponseObject(job){
  const raw=String(job?.response||'').trim();
  if(!raw||raw.startsWith('data:'))return null;
  try{return JSON.parse(raw)}catch{return null}
}
function benchmarkToolCalls(wrap,job){
  const response=benchmarkResponseObject(job);
  const message=response?.choices?.[0]?.message||response?.message||{};
  const candidates=[
    ...(Array.isArray(wrap?.tool_calls)?wrap.tool_calls:[]),
    ...(Array.isArray(job?.tool_calls)?job.tool_calls:[]),
    ...(Array.isArray(message.tool_calls)?message.tool_calls:[]),
  ];
  const seen=new Set();
  return candidates.filter((call,index)=>{
    if(!call||typeof call!=='object')return false;
    const fn=call.function||call;
    const key=[call.id||'',fn.name||call.name||'',JSON.stringify(fn.arguments??call.arguments??'')].join('|');
    if(seen.has(key))return false;
    seen.add(key);
    return true;
  });
}
function sanitizeBenchmarkToolArguments(value,depth=0){
  if(depth>8)return '[嵌套层级过深]';
  if(Array.isArray(value))return value.slice(0,100).map(item=>sanitizeBenchmarkToolArguments(item,depth+1));
  if(value&&typeof value==='object'){
    const out={};
    for(const [key,item] of Object.entries(value).slice(0,100)){
      out[key]=/(?:(?:api|client|admin|private)[_-]?key|authorization|cookie|secret|password|credential|access[_-]?token|refresh[_-]?token|session(?:[_-]?id)?)/i.test(key)
        ?'[已脱敏]':sanitizeBenchmarkToolArguments(item,depth+1);
    }
    return out;
  }
  if(typeof value==='string'){
    return value
      .replace(/Bearer\s+[^\s"',}]+/gi,'Bearer [已脱敏]')
      .replace(/((?:(?:api|client|admin|private)[_-]?key|authorization|cookie|secret|password|credential|access[_-]?token|refresh[_-]?token|session(?:[_-]?id)?)\s*[:=]\s*)["']?[^,\s"'}]+/gi,'$1[已脱敏]');
  }
  return value;
}
function renderBenchmarkToolCalls(wrap,job){
  const calls=benchmarkToolCalls(wrap,job);
  const events=Array.isArray(wrap?.tool_call_events)?wrap.tool_call_events:[];
  const isToolScenario=['tool_performance','tool_performance_auto'].includes(job?.scenario);
  const metrics=isToolScenario?(wrap?.tool_metrics||job?.tool_metrics):null;
  if(!calls.length&&!events.length&&!metrics)return '';
  const countParts=[];
  if(calls.length)countParts.push(calls.length+' 次调用');
  if(events.length)countParts.push(events.length+' 条执行事件');
  let html='<details class="bench-detail-fold"><summary>工具调用详情'+(countParts.length?'（'+countParts.join(' · ')+'）':'')+'</summary><div class="bench-detail-fold-body">';
  if(calls.length){
    html+='<h4>模型发起的调用</h4>'+calls.map((call,index)=>{
      const fn=call.function||call;
      const name=fn.name||call.name||'未知工具';
      let args=fn.arguments??call.arguments??call.input??null;
      if(typeof args==='string'){try{args=JSON.parse(args)}catch{}}
      const safeArgs=args==null?null:sanitizeBenchmarkToolArguments(args);
      const argsText=safeArgs==null?'未提供参数':JSON.stringify(safeArgs,null,2);
      return '<section class="bench-tool-call"><strong>调用 '+(index+1)+' · '+esc(name)+'</strong>'+
        (call.id?'<p class="req-note">调用 ID：<code>'+esc(call.id)+'</code></p>':'')+
        '<pre class="bench-out">'+esc(argsText)+'</pre></section>';
    }).join('');
  }else if(events.length){
    html+='<p class="req-note">任务记录未保留结构化工具调用参数，以下展示可用的脱敏执行事件摘要。</p>';
  }
  if(events.length){
    html+='<h4>工具执行事件</h4>'+wrapTable(
      '<tr><th>#</th><th>轮次</th><th>工具</th><th>状态</th><th>耗时</th><th>结果字节</th><th>错误码</th></tr>'+
      events.map(event=>'<tr><td>'+esc(String(event.seq??'—'))+'</td><td>'+esc(String(event.round??'—'))+'</td>'+
        '<td>'+esc(event.tool_name||'—')+'</td><td>'+esc(event.status||'—')+'</td>'+
        '<td>'+esc(String(event.duration_ms??'—'))+(event.duration_ms!=null?' ms':'')+'</td>'+
        '<td>'+esc(String(event.result_bytes??'—'))+'</td><td>'+esc(event.error_code||'—')+'</td></tr>').join('')
    );
  }
  if(metrics){
    const value=(v,status)=>status==='measured'&&v!=null?String(v)+' ms':(status==='combined_with_decision'&&v!=null?String(v)+' ms（与决策合并测量）':(status==='not_applicable'?'不适用':(v==null?'未知':String(v))));
    html+='<h4>工具阶段指标</h4><dl class="bench-history-detail-grid">'+[
      ['模型决策',value(metrics.model_decision_ms,metrics.model_decision_status)],
      ['工具执行合计',value(metrics.tool_execution_total_ms,metrics.tool_execution_status)],
      ['模型最终生成',value(metrics.model_final_generation_ms,metrics.model_final_generation_status)],
      ['模型轮数',metrics.model_rounds??'未知'],['工具轮数',metrics.tool_rounds??'未知'],
      ['模型工具决策',metrics.tool_call_decision==='called'?'调用了工具':(metrics.tool_call_decision==='not_called'?'未调用工具':'未知')],
      ['执行尝试 / 成功 / 失败',(metrics.tool_execution_attempts??0)+' / '+(metrics.tool_execution_successes??0)+' / '+(metrics.tool_execution_failures??0)],
      ['执行成功率',metrics.tool_execution_success_rate!=null?Number(metrics.tool_execution_success_rate).toFixed(1)+'%':'未知'],
      ['工具调用数',metrics.tool_calls??calls.length]
    ].map(([k,v])=>'<dt>'+esc(k)+'</dt><dd>'+esc(String(v))+'</dd>').join('')+'</dl>';
  }
  return html+'</div></details>';
}
function renderBenchmarkModelTimings(analysis){
  const t=analysis?.throughput?.llama_timings;
  if(!t)return '';
  const rows=[
    ['Prompt Eval Token（非缓存）',t.prompt_n],
    ['缓存 Token（llama.cpp cache_n）',t.cache_n],
    ['缓存 Token（上游 usage）',t.usage_cached_tokens],
    ['缓存 Token（/completion）',t.tokens_cached],
    ['Tokens Evaluated（上游字段）',t.tokens_evaluated],
    ['Prompt Eval 耗时',t.prompt_ms!=null?t.prompt_ms+' ms':null],
    ['Prompt Eval 每 Token',t.prompt_per_token_ms!=null?Number(t.prompt_per_token_ms).toFixed(4)+' ms/token':null],
    ['Prompt Eval 速度',t.prompt_per_second!=null?Number(t.prompt_per_second).toFixed(2)+' token/s':null],
    ['Decode Token',t.predicted_n],
    ['Decode 耗时',t.predicted_ms!=null?t.predicted_ms+' ms':null],
    ['Decode 每 Token',t.predicted_per_token_ms!=null?Number(t.predicted_per_token_ms).toFixed(4)+' ms/token':null],
    ['Decode 速度',t.predicted_per_second!=null?Number(t.predicted_per_second).toFixed(2)+' token/s':null],
  ];
  let html='<details class="bench-detail-fold"><summary>模型原生 Timings</summary><div class="bench-detail-fold-body">'+reqKv(rows);
  const samples=Array.isArray(t.prompt_progress)?t.prompt_progress:[];
  if(samples.length){
    const shown=samples.slice(-20);
    html+='<h4>Prompt progress（'+samples.length+' 个采样点）</h4>'+shown.map((sample,index)=>{
      const total=Number(sample.total),cache=Number(sample.cache),processed=Number(sample.processed),timeMS=Number(sample.time_ms);
      const denominator=total-cache,numerator=processed-cache;
      const percent=Number.isFinite(denominator)&&denominator>0&&Number.isFinite(numerator)&&numerator>=0&&numerator<=denominator
        ?(numerator/denominator*100).toFixed(1)+'%':'未知';
      return reqKv([
        ['采样序号',String(samples.length-shown.length+index+1)],
        ['已处理 / 总量',Number.isFinite(processed)&&Number.isFinite(total)?processed+' / '+total:'未知'],
        ['缓存 Token',Number.isFinite(cache)?cache:'未知'],
        ['非缓存 Prompt 进度',percent],
        ['上游耗时',Number.isFinite(timeMS)?timeMS+' ms':'未知'],
      ]);
    }).join('')+'<p class="req-note">进度按 (processed - cache) / (total - cache) 计算；无效分母或越界计数不推算百分比。</p>';
  }
  return html+'</div></details>';
}
function renderBenchmarkHistoryDetail(wrap){
  const box=$('benchHistoryDetail');
  if(!box)return;
  const job=benchJobPayload(wrap);
  const {content,reasoning}=benchTextsFromJob(job);
  const analysis=wrap.analysis||job.analysis||{};
  const phases=analysis.phases_ms||{};
  const throughput=analysis.throughput||{};
  const coreMetrics=[
    ['状态',statusLabel(job.status)],
    ['场景',job.scenario==='tool_performance_auto'?'工具调用性能（自然决策）':(job.scenario==='tool_performance'?'工具调用性能（强制调用诊断）':'普通性能')],
    ['验收用例',job.acceptance_case_id||'未关联'],
    ['模型',job.model_id||job.model_key||'—'],
    ['Backend',job.backend||'—'],
    ['总耗时',job.latency_ms!=null?job.latency_ms+' ms':'未知'],
    ['TTFT',job.ttft_ms!=null?job.ttft_ms+' ms':'未知'],
    ['输入 Token',job.input_tokens??'未知'],
    ['输出 Token',job.output_tokens??'未知'],
    ['总 Token',job.total_tokens??'未知'],
    ['输出速度',throughput.output_tokens_per_sec!=null?Number(throughput.output_tokens_per_sec).toFixed(2)+' tok/s':'未知']
  ];
  const metadata=[
    ['请求 ID',job.request_id],['HTTP 状态',job.http_status],
    ['上游模型标识',job.llama_model],['开始时间',job.started_at?fmtDate(job.started_at):null],
    ['结束时间',job.finished_at?fmtDate(job.finished_at):null],
    ['正文字符',job.content_chars],['思考字符',job.reasoning_chars],
    ['响应字节',job.response_bytes],['流式事件数',job.stream_event_count]
  ].filter(([,value])=>value!=null&&value!=='');
  let html='<div class="card-head"><div><h3 tabindex="-1">请求详情</h3><p class="small muted">任务 <code>'+esc(job.job_id||'—')+'</code></p></div><button type="button" class="btn sm ghost" data-bench-history-detail-close>收起详情</button></div>';
  html+='<h4>核心结果</h4><dl class="bench-history-detail-grid">'+coreMetrics.map(([k,v])=>'<dt>'+esc(k)+'</dt><dd>'+esc(String(v??'未知'))+'</dd>').join('')+'</dl>';
  html+='<details class="bench-detail-fold"><summary>测试条件与请求元数据</summary><div class="bench-detail-fold-body">'+renderBenchCondition(job)+
    (metadata.length?'<dl class="bench-history-detail-grid">'+metadata.map(([k,v])=>'<dt>'+esc(k)+'</dt><dd>'+esc(String(v))+'</dd>').join('')+'</dl>':'')+
    '</div></details>';
  const phaseLabels={
    prep_ms:'Gateway 准备',model_load_ms:'模型加载',upstream_ms:'上游推理',
    time_to_upstream_headers_ms:'上游响应头',time_to_first_byte_ms:'上游首字节',
    ttft_content_ms:'首条正文',thinking_ms:'思考阶段',content_generation_ms:'正文生成',
    prefill_until_first_ms:'首 Token 前 Prompt 处理',gateway_before_infer_ms:'Gateway 推理前'
  };
  const phaseRows=Object.entries(phases).filter(([key,value])=>value!=null&&!['total_ms','ttft_ms'].includes(key));
  const extraThroughput=Object.entries(throughput).filter(([key,value])=>key!=='output_tokens_per_sec'&&key!=='note'&&value!=null&&typeof value!=='object');
  if(phaseRows.length||extraThroughput.length||throughput.note){
    html+='<details class="bench-detail-fold"><summary>阶段耗时与吞吐明细</summary><div class="bench-detail-fold-body">';
    if(phaseRows.length)html+='<h4>阶段耗时</h4><dl class="bench-history-detail-grid">'+phaseRows.map(([key,value])=>'<dt>'+esc(phaseLabels[key]||key)+'</dt><dd>'+esc(String(value))+' ms</dd>').join('')+'</dl>';
    if(extraThroughput.length)html+='<h4>其他吞吐指标</h4><dl class="bench-history-detail-grid">'+extraThroughput.map(([key,value])=>'<dt>'+esc(key)+'</dt><dd>'+esc(String(value))+'</dd>').join('')+'</dl>';
    if(throughput.note)html+='<p class="req-note">'+esc(throughput.note)+'</p>';
    html+='</div></details>';
  }
  html+=renderBenchmarkModelTimings(analysis);
  if(wrap.acceptance){
    const acceptance=wrap.acceptance;
    html+='<section class="bench-detail-fold"><h4>验收判定</h4><p><span class="pill">'+esc(acceptance.case_id)+' · '+esc(acceptance.status)+'</span></p><p>'+esc(acceptance.message)+'</p><p class="small muted">原因码：<code>'+esc(acceptance.reason_code)+'</code> · 规则版本：'+esc(acceptance.rules_version)+'</p></section>';
  }
  html+=renderBenchmarkToolCalls(wrap,job);
  if(content)html+='<details class="bench-detail-fold"><summary>正文 content（'+formatTokenCount(job.content_chars??content.length)+' 字符）</summary><div class="bench-detail-fold-body"><pre class="bench-out">'+esc(content)+'</pre></div></details>';
  if(reasoning)html+='<details class="bench-detail-fold"><summary>思考 reasoning（'+formatTokenCount(job.reasoning_chars??reasoning.length)+' 字符）</summary><div class="bench-detail-fold-body"><pre class="bench-out">'+esc(reasoning)+'</pre></div></details>';
  if(job.error)html+='<p class="msg err">'+esc(job.error)+'</p>';
  if(job.error_code)html+='<p class="req-note">错误码：<code>'+esc(job.error_code)+'</code></p>';
  box.innerHTML=html;
  box.hidden=false;
  box.scrollIntoView({block:'nearest',behavior:'smooth'});
  box.querySelector('h3')?.focus({preventScroll:true});
}
let benchHistoryDetailTrigger=null;
async function openBenchmarkHistory(jobId,trigger=null){
  if(!jobId)return;
  if(benchHistoryDetailTrigger&&benchHistoryDetailTrigger!==trigger)benchHistoryDetailTrigger.setAttribute('aria-expanded','false');
  if(trigger)benchHistoryDetailTrigger=trigger;
  if(benchHistoryDetailTrigger)benchHistoryDetailTrigger.setAttribute('aria-expanded','true');
  const box=$('benchHistoryDetail');
  if(box){box.hidden=false;box.innerHTML='<p class="muted">正在加载任务详情…</p>';}
  try{
    const result=await api('/api/benchmark/'+encodeURIComponent(jobId));
    renderBenchmarkHistoryDetail(result);
  }catch(error){
    if(box){box.hidden=false;box.innerHTML='<div class="msg err">'+esc(error.message||'历史任务详情加载失败')+' <button type="button" class="btn sm ghost" data-bench-history-detail-close>关闭详情</button></div>';}
  }
}


let benchDrawerTrigger = null;
function benchDrawerFocusable(root){
  const panel=root?.querySelector('.drawer-panel');
  if(!panel)return [];
  return [...panel.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])')]
    .filter(el=>{
      const closedDetails=el.closest('details:not([open])');
      return !el.hidden&&el.getClientRects().length>0&&getComputedStyle(el).visibility!=='hidden'&&(!closedDetails||el.matches('details:not([open]) > summary'));
    });
}
function openBenchParamsDrawer(){
  const root=$('benchParamsDrawer');
  if(!root||root.classList.contains('open'))return;
  benchDrawerTrigger=document.activeElement;
  root.hidden=false;
  root.classList.add('open');
  root.setAttribute('aria-hidden','false');
  document.body.classList.add('drawer-open');
  root.querySelector('.drawer-head button')?.focus();
}
function closeBenchParamsDrawer(){
  const root=$('benchParamsDrawer');
  if(!root||!root.classList.contains('open'))return;
  root.classList.remove('open');
  root.hidden=true;
  root.setAttribute('aria-hidden','true');
  document.body.classList.remove('drawer-open');
  const trigger=benchDrawerTrigger;
  benchDrawerTrigger=null;
  if(trigger?.isConnected)trigger.focus();
}
document.addEventListener('keydown',e=>{
  const root=$('benchParamsDrawer');
  if(!root?.classList.contains('open'))return;
  if(e.key==='Escape'){
    e.preventDefault();
    closeBenchParamsDrawer();
    return;
  }
  if(e.key!=='Tab')return;
  const focusable=benchDrawerFocusable(root);
  if(!focusable.length)return;
  const first=focusable[0],last=focusable[focusable.length-1],active=document.activeElement;
  if(e.shiftKey&&(active===first||!root.contains(active))){
    e.preventDefault();
    last.focus();
  }else if(!e.shiftKey&&(active===last||!root.contains(active))){
    e.preventDefault();
    first.focus();
  }
});
async function loadBenchPanel(opts={}){
  try{
    const beJ=await api('/api/admin/backend');
    const be=(beJ.active||{}).backend||'llamacpp';
    syncBenchParamsForBackend(be);
    let name=(beJ.inference_active_model_id||'').trim();
    let gk='';
    if(be==='llamacpp'){
      try{
        const rt=await api('/api/admin/runtime');
        gk=rt.gateway_model_key||'';
      }catch{}
    }
    if(!name){
      const j=await api('/api/admin/runtime/preview?backend='+encodeURIComponent(be));
      name=(j.active_model||'').trim();
      if(!name&&(j.loaded||[]).length)name=(j.loaded[0].id||j.loaded[0].ID||'').trim();
    }
    if(!name){
      $('benchTarget').innerHTML='<span class="err">尚未加载上游模型</span> · 请在「模型管理」对当前 API 后端（'+esc(be)+'）加载模型';
      return;
    }
    $('benchTarget').innerHTML=
      '推理后端（API 流量）：<b>'+esc(be==='ollama'?'Ollama':be==='llamacpp'?'llama.cpp':be)+'</b> · '+
      '上游模型：<b><code>'+esc(name)+'</code></b>'+
      (gk?' · 网关模型键：<code>'+esc(gk)+'</code>':'');
    updateBenchReqPreview(name);
    if(opts.resume!==false)await resumeBenchmarkSession();
    return {upstream:name,gatewayKey:gk};
  }catch(e){
    $('benchTarget').innerHTML='<span class="err">'+esc(e.message)+'</span>';
    return '';
  }
}

function updateBenchReqPreview(upstreamModel,scenario='performance',promptOverride=''){
  const p=collectBenchParams();
  if(promptOverride.trim())p.prompt=promptOverride.trim();
  // 预览的是网关 → llama.cpp 的 /v1/chat/completions 体；"model" 必须是上游注册名（常与 GGUF 文件名同大小写），不是 model_key。
  const body={
    model:upstreamModel||'（当前上游）',
    messages:[{role:'user',content:p.prompt}],
    stream:scenario!=='tool_performance'&&scenario!=='tool_performance_auto',
    max_tokens:p.max_tokens,
  };
  if(scenario==='tool_performance_auto')body.tool_choice='auto';
  if(scenario==='tool_performance')body.tool_choice={type:'function',function:{name:'web.search'}};
  if(p.temperature!=null)body.temperature=p.temperature;
  if(p.top_p!=null)body.top_p=p.top_p;
  if(p.top_k!=null)body.top_k=p.top_k;
  if(p.min_p!=null)body.min_p=p.min_p;
  if(p.frequency_penalty!=null)body.frequency_penalty=p.frequency_penalty;
  if(p.presence_penalty!=null)body.presence_penalty=p.presence_penalty;
  if(p.repeat_penalty!=null)body.repeat_penalty=p.repeat_penalty;
  if(p.seed!=null)body.seed=p.seed;
  if(p.enable_thinking!=null)body.enable_thinking=p.enable_thinking;
  if(p.stop?.length)body.stop=p.stop;
  benchResultElement('reqPreview').textContent=JSON.stringify(body,null,2);
  persistBenchParams();
}
['benchPrompt','benchTokens','benchTemperature','benchTopP','benchTopK','benchMinP','benchFreqPen','benchPresPen','benchRepeatPen','benchSeed','benchThinking','benchStop'].forEach(id=>{
  $(id)?.addEventListener('input',()=>{const n=$('benchTarget')?.querySelector('code')?.textContent;updateBenchReqPreview(n||'')});
  $(id)?.addEventListener('change',()=>{const n=$('benchTarget')?.querySelector('code')?.textContent;updateBenchReqPreview(n||'')});
});
function benchJobPayload(j){return j.job||j}
function benchTextsFromJob(j){
  let content=(j.content_text||'').trim();
  let reasoning=(j.reasoning_text||'').trim();
  if(!content&&j.answer){
    const a=String(j.answer);
    if(a.startsWith('（模型仍在/仅输出 reasoning')||a.startsWith('（仅有 reasoning')){
      reasoning=reasoning||a.replace(/^[^\n]*\n?/,'');
    }else content=a;
  }
  if(!content&&j.response&&!String(j.response).trim().startsWith('data:')){
    try{
      const o=JSON.parse(j.response);
      content=(o.choices?.[0]?.message?.content||o.choices?.[0]?.text||'').trim();
    }catch{}
  }
  return {content,reasoning};
}
const BENCH_CONDITION_MISSING_LABELS={
  model_sha256_unknown:'模型文件 SHA-256',
  gateway_revision_unknown:'Gateway 构建版本',
  gateway_build_modified:'Gateway 构建包含未提交修改',
  gateway_build_cleanliness_unknown:'Gateway 工作区状态',
  backend_version_unknown:'Backend 版本',
  model_load_state_unknown:'测试前模型加载状态',
  hardware_model_unknown:'CPU 型号',
  hardware_memory_unknown:'物理内存容量'
};
function renderBenchCondition(j){
  const status=j.condition_status||'unknown';
  const labels={complete:'完整',partial:'部分',unknown:'未知'};
  const missing=Array.isArray(j.condition_missing)?j.condition_missing:[];
  const detail=status==='partial'
    ?'缺失维度：'+(missing.map(x=>BENCH_CONDITION_MISSING_LABELS[x]||x).join('、')||'未提供')
    :status==='unknown'
      ?'历史任务未保存条件快照，无法判断是否可比较。'
      :'条件信息完整；仍需结合具体指标判断测试结果。';
  const fingerprint=typeof j.condition_sha256==='string'?j.condition_sha256:'';
  return '<section class="bench-condition"><h3 style="margin:0 0 6px;font-size:13px">测试条件：'+esc(labels[status]||labels.unknown)+'</h3>'+
    '<p class="req-note" style="margin:0">'+esc(detail)+'</p>'+
    (fingerprint?'<p class="req-note mono" style="margin:4px 0 0">条件指纹：'+esc(fingerprint.slice(0,16))+'</p>':'')+
    '</section>';
}
let benchResultScope='performance';
const BENCH_RESULT_IDS={
 performance:{answer:'benchAnswer',reasoningWrap:'benchReasoningWrap',reasoning:'benchReasoning',analysis:'benchAnalysis',eventsFold:'benchEventsFold',eventsTable:'benchEventsTable',eventsCount:'benchEventsCount',metrics:'benchMetrics',raw:'benchRaw',reqPreview:'benchReqPreview'},
 tools:{answer:'toolBenchAnswer',reasoningWrap:'toolBenchReasoningWrap',reasoning:'toolBenchReasoning',analysis:'toolBenchAnalysis',eventsFold:'toolBenchEventsFold',eventsTable:'toolBenchEventsTable',eventsCount:'toolBenchEventsCount',metrics:'toolBenchMetrics',raw:'toolBenchRaw',reqPreview:'toolBenchReqPreview'},
 acceptance:{answer:'acceptanceBenchAnswer',reasoningWrap:'acceptanceBenchReasoningWrap',reasoning:'acceptanceBenchReasoning',analysis:'acceptanceBenchAnalysis',eventsFold:'acceptanceBenchEventsFold',eventsTable:'acceptanceBenchEventsTable',eventsCount:'acceptanceBenchEventsCount',metrics:'acceptanceBenchMetrics',raw:'acceptanceBenchRaw',reqPreview:'acceptanceBenchReqPreview'}
};
function benchResultElement(key){return $(BENCH_RESULT_IDS[benchResultScope]?.[key]||BENCH_RESULT_IDS.performance[key]);}
function setBenchResultScopeFromJob(wrap){
 const job=benchJobPayload(wrap);
 if(wrap?.acceptance||job.acceptance_case_id)benchResultScope='acceptance';
 else if(job.scenario==='tool_performance'||job.scenario==='tool_performance_auto')benchResultScope='tools';
 else benchResultScope='performance';
}

function renderBenchAnalysis(wrap){
  const box=benchResultElement('analysis');if(!box)return;
  const a=wrap.analysis||{};
  const p=a.phases_ms||{};
  const hasPhases=Object.keys(p).some(key=>!['total_ms','ttft_ms'].includes(key)&&p[key]!=null);
  const conditionHTML=renderBenchCondition(benchJobPayload(wrap));
  const toolScenario=['tool_performance','tool_performance_auto'].includes(benchJobPayload(wrap).scenario);
  const toolMetrics=toolScenario?(wrap.tool_metrics||null):null;
  const toolEvents=Array.isArray(wrap.tool_call_events)?wrap.tool_call_events:[];
  let html=conditionHTML;
  if(toolMetrics){
    const metricValue=(value,status)=>status==='measured'&&value!=null?value+' ms':(status==='combined_with_decision'&&value!=null?value+' ms（与决策合并测量）':(status==='not_applicable'?'不适用':'未知'));
    const rows=[
      ['工具选择策略',toolMetrics.tool_choice_mode==='auto'?'自然决策（auto）':'强制调用（web.search）'],
      ['模型工具决策',toolMetrics.tool_call_decision==='called'?'调用了工具':(toolMetrics.tool_call_decision==='not_called'?'未调用工具':'未知')],
      ['执行尝试 / 成功 / 失败',(toolMetrics.tool_execution_attempts??0)+' / '+(toolMetrics.tool_execution_successes??0)+' / '+(toolMetrics.tool_execution_failures??0)],
      ['执行成功率',toolMetrics.tool_execution_success_rate!=null?Number(toolMetrics.tool_execution_success_rate).toFixed(1)+'%':'未知'],
      ['模型决策',metricValue(toolMetrics.model_decision_ms,toolMetrics.model_decision_status)],
      ['工具执行合计',metricValue(toolMetrics.tool_execution_total_ms,toolMetrics.tool_execution_status)],
      ['模型最终生成',metricValue(toolMetrics.model_final_generation_ms,toolMetrics.model_final_generation_status)],
      ['模型轮数',toolMetrics.model_rounds??0],['工具轮数',toolMetrics.tool_rounds??0],['工具调用数',toolMetrics.tool_calls??0],
    ];
    html+='<h3 style="margin:12px 0 8px;font-size:14px">工具调用阶段</h3><dl>'+
      rows.map(([label,value])=>'<dt>'+esc(label)+'</dt><dd>'+esc(String(value))+'</dd>').join('')+'</dl>';
    if(toolEvents.length){
      html+='<details class="bench-detail-fold"><summary>工具执行事件（'+toolEvents.length+' 条）</summary><div class="bench-detail-fold-body">'+
        wrapTable('<tr><th>#</th><th>轮次</th><th>工具</th><th>状态</th><th>耗时</th><th>结果字节</th><th>错误码</th></tr>'+
        toolEvents.map(event=>'<tr><td>'+esc(String(event.seq??''))+'</td><td>'+esc(String(event.round??''))+'</td><td>'+esc(event.tool_name||'—')+'</td><td>'+esc(event.status||'—')+'</td><td>'+esc(String(event.duration_ms??'—'))+(event.duration_ms!=null?' ms':'')+'</td><td>'+esc(String(event.result_bytes??'—'))+'</td><td>'+esc(event.error_code||'—')+'</td></tr>').join(''))+
        '</div></details>';
    }
  }
  if(hasPhases){
    const labels={
      prep_ms:'Gateway 准备',model_load_ms:'模型加载',upstream_ms:'上游推理',
      time_to_upstream_headers_ms:'上游响应头',time_to_first_byte_ms:'上游首字节',
      ttft_content_ms:'首条正文',thinking_ms:'思考阶段',content_generation_ms:'正文生成',
      prefill_until_first_ms:'首 Token 前 Prompt 处理',gateway_before_infer_ms:'Gateway 推理前'
    };
    const rows=Object.entries(p).filter(([key,value])=>value!=null&&!['total_ms','ttft_ms'].includes(key));
    html+='<h3 style="margin:12px 0 10px;font-size:14px">阶段耗时分解</h3><dl>'+
      rows.map(([key,value])=>'<dt>'+esc(labels[key]||key)+'</dt><dd>'+esc(String(value))+' ms</dd>').join('')+'</dl>';
  }
  html+=renderBenchmarkModelTimings(a);
  if(a.summary?.length)html+='<ul class="req-hints" style="margin:10px 0 0">'+a.summary.map(l=>'<li>'+esc(l)+'</li>').join('')+'</ul>';
  if(a.throughput?.note)html+='<p class="req-note">'+esc(a.throughput.note)+'</p>';
  box.style.display='block';
  box.innerHTML=html;
}
function renderBenchStreamEvents(evs){
  const fold=benchResultElement('eventsFold'),tbl=benchResultElement('eventsTable'),cnt=benchResultElement('eventsCount');
  if(!fold||!tbl)return;
  if(!evs?.length){fold.style.display='none';return}
  fold.style.display='block';
  if(cnt)cnt.textContent='('+evs.length+' 条)';
  const show=evs.length>200?evs.slice(-200):evs;
  const note=evs.length>200?`<p class="req-note">仅展示最近 ${show.length} 条</p>`:'';
  tbl.innerHTML=note+wrapTable('<tr><th>#</th><th>ms</th><th>类型</th><th>字段</th><th>+字符</th><th>预览</th></tr>'+
    show.map(e=>`<tr><td>${e.seq}</td><td>${e.at_ms}</td><td>${esc(e.kind)}</td><td>${esc(e.field||'—')}</td><td>${e.delta_len??'—'}</td><td class="small">${esc(e.preview||'—')}</td></tr>`).join(''));
}
function renderBenchLive(wrap,running){
  const j=benchJobPayload(wrap);
  const evs=wrap.stream_events||[];
  const ok=j.status==='completed';
  const failed=j.status==='failed'||j.status==='cancelled';
  const {content,reasoning}=benchTextsFromJob(j);
  const ans=benchResultElement('answer');
  if(ans){
    if(failed)ans.className='bench-answer err',ans.textContent=j.error||(j.status==='cancelled'?'任务已停止':'测试失败');
    else if(running&&!content&&!reasoning)ans.className='bench-answer muted',ans.textContent='推理中…';
    else ans.className='bench-answer',ans.textContent=content||'（尚无正文 content）';
  }
  const rw=benchResultElement('reasoningWrap'),rb=benchResultElement('reasoning');
  if(rw&&rb){
    if(reasoning){rw.style.display='block';rb.className='bench-answer bench-reasoning';rb.textContent=reasoning}
    else rw.style.display='none',rb.textContent='';
  }
  renderBenchAnalysis(wrap);
  renderBenchStreamEvents(evs);
}
function renderBenchResult(wrap){
  renderBenchLive(wrap,false);
  const j=benchJobPayload(wrap);
  const evs=wrap.stream_events||[];
  const ok=j.status==='completed';
  const met=benchResultElement('metrics');if(!met)return;
  const tps=wrap.analysis?.throughput?.output_tokens_per_sec;
  met.innerHTML=[
    statusPill(j.status==='completed'?'success':j.status==='failed'?'error':j.status==='cancelled'?'cancelled':j.status),
    wrap.acceptance?('<span class="pill">验收 '+esc(wrap.acceptance.case_id)+' · '+esc(wrap.acceptance.status)+'</span><span class="pill">'+esc(wrap.acceptance.message)+'</span>'):'',
    j.stream?`<span class="pill">流式</span>`:'',
    j.max_tokens?`<span class="pill">max_tokens ${j.max_tokens}</span>`:'',
    j.prompt_len?`<span class="pill">提示 ${j.prompt_len} 字符</span>`:'',
    j.latency_ms!=null?`<span class="pill">总耗时 ${j.latency_ms} ms</span>`:'',
    j.ttft_ms!=null?`<span class="pill">TTFT ${j.ttft_ms} ms</span>`:'',
    j.input_tokens?`<span class="pill">输入 ${j.input_tokens} tok</span>`:'',
    j.output_tokens?`<span class="pill">输出 ${j.output_tokens} tok</span>`:'',
    tps!=null?`<span class="pill">${tps.toFixed(2)} tok/s</span>`:'',
    j.content_chars?`<span class="pill">正文 ${j.content_chars} 字</span>`:'',
    j.reasoning_chars?`<span class="pill">思考 ${j.reasoning_chars} 字</span>`:'',
    j.response_bytes?`<span class="pill">响应 ${(j.response_bytes/1024).toFixed(1)} KB</span>`:'',
    j.stream_event_count?`<span class="pill">事件 ${j.stream_event_count}</span>`:'',
    j.http_status?`<span class="pill">HTTP ${j.http_status}</span>`:'',
    j.llama_model?`<span class="pill mono small">${esc(j.llama_model)}</span>`:'',
    j.request_id?`<button type="button" class="btn sm ghost" onclick="showDetail('${encodeURIComponent(j.request_id).replace(/'/g, "%27")}')">完整调用记录</button>`:'',
  ].filter(Boolean).join('');
  const rawBody=j.response||'';
  benchResultElement('raw').textContent=rawBody.trim().startsWith('data:')?rawBody:JSON.stringify({analysis:wrap.analysis,job:j,acceptance:wrap.acceptance,stream_events:evs,response:tryParse(rawBody)||rawBody},null,2);
  const ef=benchResultElement('eventsFold');if(ef&&evs.length)ef.open=evs.length<=80;
  renderBenchmarkAcceptanceResult(wrap);
}
function renderBenchmarkAcceptanceResult(wrap){
  const box=$('benchAcceptanceResult');
  if(!box)return;
  const acceptance=wrap?.acceptance;
  if(!acceptance)return;
  const labels={pass:'通过',fail:'失败',inconclusive:'证据不足',not_run:'未执行'};
  const status=acceptance.status||'inconclusive';
  box.className='bench-acceptance-result msg '+(status==='pass'?'ok':status==='fail'?'err':'warn');
  const job=benchJobPayload(wrap);
  const request=job.request_id?' · Request ID：'+job.request_id:'';
  box.textContent=acceptance.case_id+' · '+(labels[status]||status)+'：'+(acceptance.message||'无判定说明')+request;
}
function tryParse(s){try{return JSON.parse(s)}catch{return s}}

function renderBenchRunningMetrics(wrap,t0){
  const j=benchJobPayload(wrap);
  const metR=benchResultElement('metrics');
  if(!metR)return;
  metR.innerHTML=[statusPill('running'),
    j.ttft_ms!=null?`<span class="pill">TTFT ${j.ttft_ms} ms</span>`:'',
    wrap.stream_events?.length?`<span class="pill">事件 ${wrap.stream_events.length}</span>`:'',
    j.content_chars?`<span class="pill">正文 ${j.content_chars} 字</span>`:'',
    j.reasoning_chars?`<span class="pill">思考 ${j.reasoning_chars} 字</span>`:'',
    `<span class="pill">已等待 ${Math.round((Date.now()-t0)/1000)} s</span>`,
  ].filter(Boolean).join('');
}

async function pollBenchmarkJob(jobId,t0,token){
  while(true){
    if(token!==benchPollToken)return null;
    const wrap=await api('/api/benchmark/'+encodeURIComponent(jobId));
    setBenchResultScopeFromJob(wrap);
    const j=benchJobPayload(wrap);
    if(j.status==='running'){
      renderBenchLive(wrap,true);
      renderBenchRunningMetrics(wrap,t0);
      await new Promise(r=>setTimeout(r,800));
      continue;
    }
    renderBenchResult(wrap);
    writeBenchStore({jobId});
    persistBenchParams();
    loadBenchHistory().catch(()=>{});
    return wrap;
  }
}

async function resumeBenchmarkSession(){
  const st=readBenchStore();
  if(!st.jobId)return;
  const token=++benchPollToken;
  try{
    const wrap=await api('/api/benchmark/'+encodeURIComponent(st.jobId));
    setBenchResultScopeFromJob(wrap);
    if(token!==benchPollToken)return;
    const j=benchJobPayload(wrap);
    if(j.status==='running'){
      setBenchRunning(true);
      const t0=Date.parse(j.started_at)||Date.now();
      const ans0=benchResultElement('answer');if(ans0){ans0.className='bench-answer muted';ans0.textContent='恢复轮询中…'}
      await pollBenchmarkJob(st.jobId,t0,token);
    }else{
      renderBenchResult(wrap);
      loadBenchHistory().catch(()=>{});
    }
  }catch{
    writeBenchStore({jobId:null});
  }finally{
    if(token===benchPollToken)setBenchRunning(false);
  }
}

async function stopBenchmark(){
  if(benchSuiteActive){
    benchSuiteCancelRequested=true;
    const activeJobId=benchSuiteCurrentJobId;
    benchPollToken++;
    setBenchRunning(false);
    if(!activeJobId)return;
    try{
      await api('/api/benchmark/'+encodeURIComponent(activeJobId),{method:'DELETE'});
      const wrap=await api('/api/benchmark/'+encodeURIComponent(activeJobId));
      renderBenchResult(wrap);
    }catch(e){
      const ansE=benchResultElement('answer');if(ansE){ansE.className='bench-answer err';ansE.textContent=e.message}
    }
    return;
  }
  const st=readBenchStore();
  if(!st.jobId){setBenchRunning(false);return}
  benchPollToken++;
  setBenchRunning(false);
  try{
    await api('/api/benchmark/'+encodeURIComponent(st.jobId),{method:'DELETE'});
    const wrap=await api('/api/benchmark/'+encodeURIComponent(st.jobId));
    renderBenchResult(wrap);
  }catch(e){
    const ansE=benchResultElement('answer');if(ansE){ansE.className='bench-answer err';ansE.textContent=e.message}
  }
}


const BENCH_SUITE_STORAGE_KEY='llm-gateway.bench-suite.v1';
let benchSuiteActive=false;
let benchSuiteCancelRequested=false;
let benchSuiteCurrentJobId=null;

function buildBenchmarkSuitePrompts(){
  const regions=['North','South','East','West','Central','Coastal','Highland','Metro'];
  const categories=['hardware','software','accessories','storage','network','display','audio','service'];
  const months=['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug'];
  const rows=Array.from({length:50},(_,index)=>{
    const n=index+1;
    const region=regions[index%regions.length];
    const category=categories[(index*3+Math.floor(index/8))%categories.length];
    const units=80+((index*37)%420);
    const returns=(index*7+3)%19;
    const delivery=2+((index*5)%9);
    const stockout=index%11===0?'yes':'no';
    const margin=(8+((index*13)%190)/10).toFixed(1);
    const quality=82+((index*11)%19);
    const month=months[index%months.length];
    const trend=['stable demand','rising demand','seasonal demand','volatile demand'][(index*5)%4];
    return 'R'+String(n).padStart(3,'0')+' region '+region+' category '+category+' units '+units+' returns '+returns+' delivery '+delivery+'d stockout '+stockout+' margin '+margin+' quality '+quality+' month '+month+' trend '+trend;
  });
  const instruction='以下记录均为性能测试专门构造的合成数据，不代表真实业务。请阅读全部记录，按区域和品类分析销量、退货、交付、缺货、毛利与质量之间的关系；指出值得关注的异常，给出有记录编号依据的结论和可执行建议。不要编造记录中不存在的数据。请用中文按“总体结论、区域对比、品类差异、异常记录、运营建议”结构输出，每部分至少两句。';
  return {
    short:instruction+'\n\n合成记录：\n'+rows.slice(0,1).join('\n'),
    long:instruction+'\n\n合成记录（共50条）：\n'+rows.join('\n'),
    version:'perf12-synthetic-dataset-v2'
  };
}
function benchmarkSuitePromptForCase(prompt, caseID){
  // 每个样本使用独立随机前缀，避免四组之间共享可复用的 Prompt Cache 前缀。
  const caseNonce=createBenchmarkSuiteNonce();
  return caseNonce+'_PERF12_CASE_'+String(caseID).replace(/[^A-Z0-9]/gi,'_')+'\n'+prompt;
}
function createBenchmarkSuiteNonce(){
  try{
    const bytes=new Uint8Array(12);
    globalThis.crypto.getRandomValues(bytes);
    return [...bytes].map(value=>value.toString(16).padStart(2,'0')).join('');
  }catch{
    return Date.now().toString(36)+'_'+Math.random().toString(36).slice(2,14);
  }
}
async function benchmarkTextSHA256(value){
  try{
    if(!globalThis.crypto?.subtle)return null;
    const bytes=new TextEncoder().encode(String(value));
    const digest=await globalThis.crypto.subtle.digest('SHA-256',bytes);
    return [...new Uint8Array(digest)].map(byte=>byte.toString(16).padStart(2,'0')).join('');
  }catch{return null}
}
function readBenchmarkSuiteState(){
  try{return JSON.parse(localStorage.getItem(BENCH_SUITE_STORAGE_KEY)||'null')}catch{return null}
}
function saveBenchmarkSuiteState(value){
  try{localStorage.setItem(BENCH_SUITE_STORAGE_KEY,JSON.stringify(value));return true}catch{return false}
}
function benchmarkEvidenceValue(value){
  if(value===undefined||value===null||value==='')return '缺失';
  return String(value);
}
function benchmarkRuntimeEvidence(snapshot){
  if(!snapshot)return '不可用';
  if(snapshot.status!=='observed')return snapshot.error||snapshot.status||'不可用';
  const value=snapshot.value||{};
  const metrics=value.server_metrics||value.metrics||null;
  const cpu=value.host_cpu_percent??value.cpu_percent??value.host?.cpu_percent;
  const memory=value.memory_used_bytes??value.memory_used??value.host?.memory_used_bytes;
  const bits=[];
  if(cpu!=null)bits.push('CPU '+cpu);
  if(memory!=null)bits.push('内存 '+memory);
  if(metrics)bits.push('Server Metrics 已采集');
  return bits.join('；')||'已采集快照，具体字段见原始 JSON';
}
function buildBenchmarkSuiteMarkdown(suite){
  if(!suite)return '尚无四组性能测试报告。';
  const cases=Array.isArray(suite.cases)?suite.cases:[];
  const rows=cases.map(item=>{
    const job=item.job||{};
    const analysis=item.analysis||{};
    const timings=analysis.throughput?.llama_timings||analysis.llama_timings||analysis.model_timings||{};
    const tps=analysis.throughput?.output_tokens_per_sec;
    const input=job.input_tokens;
    const output=job.output_tokens;
    const inputCheck=input==null?'不可判定':(item.input_target==='short'?(input>=96&&input<=180?'达标':'未达标'):(input>=1800&&input<=2400?'达标':'未达标'));
    const outputCheck=output==null?'不可判定':(item.output_target==='short'?(output>=32?'达标':'未达标'):(output>=256?'达标':'未达标'));
    return '| '+item.case_id+' | '+(item.input_target==='short'?'≈128':'≈2048')+' | '+(item.output_target==='short'?'≥32':'≥256')+' | '+benchmarkEvidenceValue(job.status||item.status)+' | '+benchmarkEvidenceValue(input)+' | '+benchmarkEvidenceValue(output)+' | '+benchmarkEvidenceValue(job.max_tokens??item.max_tokens)+' | '+benchmarkEvidenceValue(job.latency_ms)+' | '+benchmarkEvidenceValue(job.ttft_ms)+' | '+benchmarkEvidenceValue(timings.prompt_ms)+' | '+benchmarkEvidenceValue(timings.predicted_ms)+' | '+benchmarkEvidenceValue(tps!=null?Number(tps).toFixed(2):null)+' | '+inputCheck+' / '+outputCheck+' |';
  }).join('\n');
  const details=cases.map(item=>{
    const job=item.job||{};
    const analysis=item.analysis||{};
    const phases=analysis.phases_ms||{};
    const timings=analysis.throughput?.llama_timings||analysis.llama_timings||analysis.model_timings||{};
    return [
      '### '+item.case_id,
      '- 状态：'+benchmarkEvidenceValue(job.status||item.status),
      '- Job ID：'+benchmarkEvidenceValue(job.job_id||item.job_id),
      '- Request ID：'+benchmarkEvidenceValue(job.request_id),
      '- 模型：'+benchmarkEvidenceValue(job.model_id||job.model_key||suite.model_id),
      '- 条件：'+benchmarkEvidenceValue(job.condition_status)+'；指纹 '+benchmarkEvidenceValue(job.condition_sha256),
      '- 输入/输出目标：'+(item.input_target==='short'?'约 128':'约 2,048')+' / '+(item.output_target==='short'?'至少 32':'至少 256')+' tokens',
      '- Prompt SHA-256：'+benchmarkEvidenceValue(item.prompt_sha256),
      '- 条件缺失字段：'+JSON.stringify(job.condition_missing||[]),
      '- 时间：'+benchmarkEvidenceValue(job.started_at)+' → '+benchmarkEvidenceValue(job.finished_at),
      '- HTTP / TTFT 状态：'+benchmarkEvidenceValue(job.http_status)+' / '+benchmarkEvidenceValue(job.ttft_status),
      '- 条件快照：'+JSON.stringify(job.condition_snapshot||job.condition||{}),
      '- 输入/输出 Token：'+benchmarkEvidenceValue(job.input_tokens)+' / '+benchmarkEvidenceValue(job.output_tokens),
      '- 总耗时 / TTFT：'+benchmarkEvidenceValue(job.latency_ms)+' ms / '+benchmarkEvidenceValue(job.ttft_ms)+' ms',
      '- Prompt Eval / Decode：'+benchmarkEvidenceValue(timings.prompt_ms)+' ms / '+benchmarkEvidenceValue(timings.predicted_ms)+' ms',
      '- Prompt/Decode Token 数：'+benchmarkEvidenceValue(timings.prompt_n)+' / '+benchmarkEvidenceValue(timings.predicted_n),
      '- Gateway 阶段：'+JSON.stringify(phases),
      '- 吞吐：'+JSON.stringify(analysis.throughput||{}),
      '- 测试前 Runtime：'+benchmarkRuntimeEvidence(item.runtime_before),
      '- 测试后 Runtime：'+benchmarkRuntimeEvidence(item.runtime_after),
      '- 结果校验：'+(item.validation?.join('；')||'未提供'),
      ''
    ].join('\n');
  }).join('\n');
  return [
    '# PERF-12 四组真实推理性能基线报告',
    '',
    '- 报告版本：'+benchmarkEvidenceValue(suite.suite_version),
    '- 执行状态：'+benchmarkEvidenceValue(suite.status),
    '- 基线资格：'+benchmarkEvidenceValue(suite.qualification_status||'not_evaluated'),
    '- 开始时间：'+benchmarkEvidenceValue(suite.started_at),
    '- 完成时间：'+benchmarkEvidenceValue(suite.finished_at),
    '- Backend / 模型：'+benchmarkEvidenceValue(suite.backend)+' / '+benchmarkEvidenceValue(suite.model_id),
    '- 提示词版本：'+benchmarkEvidenceValue(suite.prompt_version),
    '- 固定参数：'+JSON.stringify(suite.parameters||{}),
    '- 预热任务：'+benchmarkEvidenceValue(suite.warmup?.job_id)+'（'+benchmarkEvidenceValue(suite.warmup?.status)+'）',
    '',
    '## 四组结果对比',
    '',
    '| 组别 | 输入目标 | 输出目标 | 状态 | 输入 Token | 输出 Token | max_tokens | 总耗时 ms | TTFT ms | Prompt Eval ms | Decode ms | Decode tok/s | 输入/输出目标校验 |',
    '|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---|',
    rows||'| — | 未执行 | — | — | — | — | — | — | — | — | — |',
    '',
    '## 逐组证据',
    '',
    details||'尚无组别结果。',
    '## 判读边界',
    '',
    '- 实际 Token 数以服务端返回为准；字符数不替代 Token 数。',
    '- 短输入目标范围为 96–180 tokens，长输入目标范围为 1800–2400 tokens。',
    '- 短输出至少 32 tokens、长输出至少 256 tokens；未达到时保留样本但不用于对应吞吐结论。',
    '- Prompt Eval / Decode 只读取上游原生 timings；缺失不从总耗时反推。',
    '- 每组 Prompt 首部使用独立批次/组别标记，降低前序请求 Prompt Cache 前缀复用；仍以 llama.cpp 返回的 cache_n 实测判断是否为冷缓存。',
    '- Prompt Cache 命中样本保留为 observed，但不纳入冷缓存 Prompt Eval 基线。',
    '- 四组 Prompt 内容不同，条件指纹不同；四组是交叉场景观察，不是严格同条件历史比较。',
    '- Runtime / Server Metrics 是快照或进程窗口证据，不归属单个请求；不得据此虚构请求级 CPU 或内存值。',
    '- 首轮结果只建立 observed 基线，不自动判定主瓶颈。',
    ''
  ].join('\n');
}
function buildBenchmarkSuiteJSON(suite){
  return JSON.stringify(suite?sanitizeBenchmarkToolArguments(suite):{},null,2);
}
function renderBenchmarkSuiteReport(){
  const resumeButton=$('benchSuiteResumeBtn');
  const suite=readBenchmarkSuiteState();
  const output=$('benchSuiteReport');
  const status=$('benchSuiteStatus');
  const copyMD=$('benchSuiteCopyMarkdown');
  const copyJSON=$('benchSuiteCopyJSON');
  if(!output)return;
  if(!suite){
    if(resumeButton)resumeButton.hidden=true;
    output.textContent='尚未执行四组测试。';
    if(status)status.textContent='固定提示词和四组顺序已就绪。';
    if(copyMD)copyMD.disabled=true;
    if(copyJSON)copyJSON.disabled=true;
    return;
  }
  if(resumeButton)resumeButton.hidden=!benchmarkSuiteCanResume(suite);
  output.textContent=buildBenchmarkSuiteMarkdown(suite);
  if(status)status.textContent='报告状态：'+(suite.status||'unknown')+' · 已记录 '+(suite.cases?.length||0)+'/4 组结果。';
  if(copyMD)copyMD.disabled=false;
  if(copyJSON)copyJSON.disabled=false;
}
async function copyBenchmarkSuiteReport(format='markdown'){
  const suite=readBenchmarkSuiteState();
  if(!suite){renderBenchmarkSuiteReport();return}
  const value=format==='json'?buildBenchmarkSuiteJSON(suite):buildBenchmarkSuiteMarkdown(suite);
  const copied=await writeClipboard(value);
  const status=$('benchSuiteStatus');
  if(status)status.textContent=copied?'已复制'+(format==='json'?'原始 JSON':'Markdown 报告')+'，可直接粘贴发送。':'复制失败，请在下方报告区域手动选择复制。';
}
async function captureBenchmarkRuntime(){
  try{
    const value=await api('/api/admin/runtime');
    return {status:'observed',observed_at:new Date().toISOString(),value:sanitizeBenchmarkToolArguments(value)};
  }catch(error){
    return {status:'unavailable',observed_at:new Date().toISOString(),error:error?.message||'Runtime Snapshot 获取失败'};
  }
}
async function submitBenchmarkSuiteTask(target,prompt,maxTokens,token,params,onCreated=null){
  applyBenchParams({...params,prompt,max_tokens:maxTokens});
  updateBenchReqPreview(target.upstream,'performance',prompt);
  const start=await api('/api/benchmark',{
    method:'POST',
    headers:{'Content-Type':'application/json','X-Client-API-Key':($('modelTestApiKey')?.value||'').trim()},
    body:JSON.stringify(benchPayloadForApi(target,'performance',prompt))
  });
  benchSuiteCurrentJobId=start.job_id;
  writeBenchStore({jobId:start.job_id});
  if(typeof onCreated==='function')onCreated(start.job_id);
  try{
    if(token!==benchPollToken||benchSuiteCancelRequested){
      await api('/api/benchmark/'+encodeURIComponent(start.job_id),{method:'DELETE'}).catch(()=>{});
      return null;
    }
    return await pollBenchmarkJob(start.job_id,Date.now(),token);
  }finally{
    if(benchSuiteCurrentJobId===start.job_id)benchSuiteCurrentJobId=null;
  }
}
function benchmarkSuiteQualification(suite){
  const caseOrder=['S-P / S-O','L-P / S-O','S-P / L-O','L-P / L-O'];
  const cases=Array.isArray(suite?.cases)?suite.cases:[];
  const allCasesRecorded=caseOrder.every(id=>cases.some(item=>item.case_id===id));
  if(!allCasesRecorded)return 'baseline_not_qualified';
  const allTargetsMet=caseOrder.every(id=>{
    const item=cases.find(entry=>entry.case_id===id);
    return item.validation?.some(v=>v.startsWith('输入目标 达标'))&&item.validation?.some(v=>v.startsWith('输出目标 达标'));
  });
  const allCold=caseOrder.every(id=>{
    const item=cases.find(entry=>entry.case_id===id);
    return item.status==='completed'&&item.validation?.some(v=>v.startsWith('Prompt Cache 未命中（cache_n=0）'))&&item.validation?.some(v=>v.startsWith('Prompt Eval Token 覆盖 '))&&!item.validation?.some(v=>v.startsWith('不满足冷缓存条件'))&&!item.validation?.some(v=>v.startsWith('原生 Prompt/Decode timings 不完整'));
  });
  const allConditionsComplete=caseOrder.every(id=>{
    const item=cases.find(entry=>entry.case_id===id);
    const job=item.job||{};
    return job.condition_status==='complete'&&(!Array.isArray(job.condition_missing)||job.condition_missing.length===0);
  });
  return allTargetsMet&&allCold&&allConditionsComplete&&suite.resume_parameter_status==='complete_snapshot'?'baseline_candidate':'baseline_not_qualified';
}

function benchmarkSuiteCanResume(suite){
  if(!suite||suite.suite_version!=='perf12-four-case-v2'||suite.prompt_version!=='perf12-synthetic-dataset-v2')return false;
  if(!suite.prompts?.short||!suite.prompts?.long)return false;
  const cases=Array.isArray(suite.cases)?suite.cases:[];
  const warmupReady=suite.warmup?.status==='completed'||(suite.warmup?.status==='running'&&suite.warmup?.job_id);
  if(!warmupReady)return false;
  if(cases.some(item=>item.status==='submitting'&&!item.job_id))return false;
  if(suite.status==='running'&&suite.warmup?.status!=='running'&&!cases.some(item=>item.status==='running'&&item.job_id))return false;
  if(suite.status==='completed')return false;
  const ids=['S-P / S-O','L-P / S-O','S-P / L-O','L-P / L-O'];
  return ids.some(id=>!cases.some(item=>item.case_id===id&&item.status==='completed'));
}

async function runBenchmarkSuite(resumeExisting=false){
  benchResultScope='performance';
  if(benchSuiteActive||$('benchSuiteRunBtn')?.disabled)return;
  const previousSuite=readBenchmarkSuiteState();
  if(resumeExisting&&!benchmarkSuiteCanResume(previousSuite)){
    const status=$('benchSuiteStatus');
    if(status)status.textContent='当前没有满足条件的未完成测试组，不能续跑。';
    renderBenchmarkSuiteReport();
    return;
  }
  const confirmMessage=resumeExisting
    ?'只补跑尚未取得 completed 结果的测试组；已完成的样本会保留，不重复执行。要求当前后端和模型与原测试一致。继续？'
    :'将创建一轮新的测试，先预热再串行执行四组性能测试。长输出最多生成 512 tokens，整个流程可能持续数分钟。新报告会替换当前报告。测试期间请勿发送其他推理请求。继续？';
  if(!confirm(confirmMessage))return;
  const clientKey=($('modelTestApiKey')?.value||'').trim();
  if(!clientKey){
    const status=$('benchSuiteStatus');
    if(status)status.textContent='请先输入客户端服务密钥。';
    $('modelTestApiKey')?.focus();
    return;
  }
  let restoreParams=collectBenchParams();
  let suiteParams=null;
  const token=++benchPollToken;
  const prompts=resumeExisting
    ?{...previousSuite.prompts,version:previousSuite.prompt_version}
    :buildBenchmarkSuitePrompts();
  benchSuiteActive=true;
  benchSuiteCancelRequested=false;
  setBenchRunning(true);
  const runButton=$('benchSuiteRunBtn');
  const resumeButton=$('benchSuiteResumeBtn');
  if(runButton)runButton.disabled=true;
  if(resumeButton)resumeButton.disabled=true;
  const status=$('benchSuiteStatus');
  if(status)status.textContent=resumeExisting?'正在校验原测试条件，准备补跑未完成组…':'正在检查模型并准备预热…';
  const suite=resumeExisting?previousSuite:{
    suite_version:'perf12-four-case-v2',
    status:'running',
    started_at:new Date().toISOString(),
    backend:benchCurrentBackend,
    parameters:{},
    prompt_version:prompts.version,
    prompts:{short:prompts.short,long:prompts.long},
    cases:[]
  };
  suite.status='running';
  if(!resumeExisting)suite.current_case_id=null;
  if(resumeExisting){
    suite.resumed_at=new Date().toISOString();
    suite.resume_count=(Number(suite.resume_count)||0)+1;
    delete suite.finished_at;
    delete suite.error;
  }
  saveBenchmarkSuiteState(suite);
  renderBenchmarkSuiteReport();
  try{
    const target=await loadBenchPanel({resume:false});
    if(token!==benchPollToken||!target?.upstream)throw new Error('当前 API 推理后端没有已加载模型，无法执行性能测试。');
    if(resumeExisting){
      if(suite.backend!==benchCurrentBackend||suite.model_id!==target.upstream){
        throw new Error('续跑条件不一致：当前后端或模型与原测试不同。请恢复原后端和模型，或开始一轮全新的测试。');
      }
      if(suite.warmup?.status!=='completed')throw new Error('原测试预热未成功，不能跳过预热续跑。');
      if(suite.execution_parameters&&typeof suite.execution_parameters==='object'){
        suiteParams={...restoreParams,...suite.execution_parameters};
        suite.resume_parameter_status='complete_snapshot';
      }else{
        suiteParams={...restoreParams,...(suite.parameters||{})};
        suite.resume_parameter_status='reconstructed_from_current_saved_ui_settings';
      }
    }else{
      suite.backend=benchCurrentBackend;
      suite.model_id=target.upstream;
      restoreParams=collectBenchParams();
      suiteParams={...restoreParams,temperature:0,top_p:1,top_k:40,min_p:null,frequency_penalty:0,presence_penalty:0,repeat_penalty:benchCurrentBackend==='ollama'?1.15:1.1,seed:42,enable_thinking:false,stop:[]};
      suite.parameters={temperature:0,top_p:1,top_k:40,min_p:null,frequency_penalty:0,presence_penalty:0,repeat_penalty:suiteParams.repeat_penalty,seed:42,enable_thinking:false,stop:[]};
      suite.resume_parameter_status='complete_snapshot';
    }
    suite.execution_parameters=sanitizeBenchmarkToolArguments(suiteParams);
    if(!resumeExisting){
      applyBenchParams({...suiteParams,prompt:prompts.short,max_tokens:8});
      if(status)status.textContent='正在执行预热请求（不计入四组结果）…';
      const warmup=await submitBenchmarkSuiteTask(target,'只回答 READY。',8,token,suiteParams,jobId=>{
        suite.warmup={job_id:jobId,status:'running',started_at:new Date().toISOString()};
        saveBenchmarkSuiteState(suite);
      });
      suite.warmup={...suite.warmup,job_id:benchJobPayload(warmup||{}).job_id||suite.warmup?.job_id||readBenchStore().jobId,status:benchJobPayload(warmup||{}).status||'unknown'};
      saveBenchmarkSuiteState(suite);
      if(token!==benchPollToken||benchSuiteCancelRequested)throw new Error('测试已停止。');
      if(suite.warmup.status!=='completed')throw new Error('预热请求未成功，停止正式测试。');
    }else if(suite.warmup?.status==='running'&&suite.warmup?.job_id){
      if(status)status.textContent='正在恢复预热任务 '+suite.warmup.job_id+'…';
      const warmup=await pollBenchmarkJob(suite.warmup.job_id,Date.parse(suite.warmup.started_at)||Date.now(),token);
      suite.warmup={...suite.warmup,status:benchJobPayload(warmup||{}).status||'unknown'};
      saveBenchmarkSuiteState(suite);
      if(!warmup||suite.warmup.status!=='completed')throw new Error('恢复的预热任务未成功完成，停止正式测试。');
    }
    const cases=[
      {case_id:'S-P / S-O',prompt:benchmarkSuitePromptForCase(prompts.short,'SP_SO'),max_tokens:64,input_target:'short',output_target:'short'},
      {case_id:'L-P / S-O',prompt:benchmarkSuitePromptForCase(prompts.long,'LP_SO'),max_tokens:64,input_target:'long',output_target:'short'},
      {case_id:'S-P / L-O',prompt:benchmarkSuitePromptForCase(prompts.short,'SP_LO'),max_tokens:512,input_target:'short',output_target:'long'},
      {case_id:'L-P / L-O',prompt:benchmarkSuitePromptForCase(prompts.long,'LP_LO'),max_tokens:512,input_target:'long',output_target:'long'}
    ];
    for(let index=0;index<cases.length;index++){
      if(token!==benchPollToken||benchSuiteCancelRequested)throw new Error('测试已停止。');
      const item=cases[index];
      const previousIndex=(suite.cases||[]).findIndex(entry=>entry.case_id===item.case_id);
      const previous=previousIndex>=0?suite.cases[previousIndex]:null;
      if(previous?.status==='completed')continue;
      if(status)status.textContent=(resumeExisting?'补跑':'正在执行')+'第 '+(index+1)+'/4 组：'+item.case_id+'…';
      suite.current_case_id=item.case_id;
      saveBenchmarkSuiteState(suite);
      const promptSHA=previous?.prompt_sha256||await benchmarkTextSHA256(item.prompt);
      const runtimeBefore=previous?.runtime_before||await captureBenchmarkRuntime();
      if(token!==benchPollToken||benchSuiteCancelRequested)throw new Error('测试已停止。');
      let wrap=null;
      let failure=null;
      try{
        if(previous?.status==='running'&&previous.job_id){
          if(status)status.textContent='正在恢复第 '+(index+1)+'/4 组任务 '+previous.job_id+'…';
          benchSuiteCurrentJobId=previous.job_id;
          try{wrap=await pollBenchmarkJob(previous.job_id,Date.parse(previous.started_at)||Date.now(),token)}
          finally{if(benchSuiteCurrentJobId===previous.job_id)benchSuiteCurrentJobId=null}
        }else if(previous?.status==='submitting'&&!previous.job_id){
          throw new Error('上次提交尚未保存 Job ID，状态不确定。为避免重复推理，请先在任务历史中核对该时间段的任务，再决定是否重建本组。');
        }else{
          const pending={
            case_id:item.case_id,status:'submitting',job_id:null,prompt_version:prompts.version,
            prompt_sha256:promptSHA,input_target:item.input_target,output_target:item.output_target,
            max_tokens:item.max_tokens,runtime_before:runtimeBefore,started_at:new Date().toISOString(),
            attempts:Array.isArray(previous?.attempts)?previous.attempts.slice():[]
          };
          if(previousIndex>=0)suite.cases[previousIndex]=pending;
          else suite.cases.push(pending);
          saveBenchmarkSuiteState(suite);
          wrap=await submitBenchmarkSuiteTask(target,item.prompt,item.max_tokens,token,suiteParams,jobId=>{
            const currentIndex=suite.cases.findIndex(entry=>entry.case_id===item.case_id);
            if(currentIndex>=0)suite.cases[currentIndex]={...suite.cases[currentIndex],status:'running',job_id:jobId,job:{job_id:jobId,status:'running'}};
            saveBenchmarkSuiteState(suite);
          });
        }
      }catch(error){failure=error?.message||'任务执行失败'}
      const runtimeAfter=await captureBenchmarkRuntime();
      const job=benchJobPayload(wrap||{});
      const validation=[];
      const input=job.input_tokens==null?NaN:Number(job.input_tokens);
      const output=job.output_tokens==null?NaN:Number(job.output_tokens);
      if(Number.isFinite(input)&&input>0){
        const min=item.input_target==='short'?96:1800;
        const max=item.input_target==='short'?180:2400;
        validation.push('输入目标 '+(input>=min&&input<=max?'达标':'未达标')+'（'+input+' tokens；目标 '+min+'–'+max+'）');
      }else validation.push('输入 Token 缺失，无法验证目标长度');
      if(Number.isFinite(output)&&output>=0){
        const min=item.output_target==='short'?32:256;
        validation.push('输出目标 '+(output>=min?'达标':'未达标')+'（'+output+' tokens；最低 '+min+'）');
      }else validation.push('输出 Token 缺失，无法验证目标长度');
      if(job.status!=='completed')validation.push('任务状态不是 completed，不纳入性能比较');
      const modelTimings=wrap?.analysis?.throughput?.llama_timings||wrap?.analysis?.llama_timings||{};
      const cacheN=modelTimings.cache_n==null?null:Number(modelTimings.cache_n);
      const promptN=modelTimings.prompt_n==null?null:Number(modelTimings.prompt_n);
      const coldPrompt=cacheN===0&&Number.isFinite(input)&&promptN===input;
      if(cacheN!=null)validation.push('Prompt Cache '+(cacheN===0?'未命中（cache_n=0）':'命中 '+cacheN+' tokens'));
      else validation.push('Prompt Cache 状态未知');
      if(promptN!=null&&Number.isFinite(input))validation.push('Prompt Eval Token 覆盖 '+promptN+'/'+input+' tokens');
      else validation.push('Prompt Eval Token 覆盖情况未知');
      if(!coldPrompt)validation.push('不满足冷缓存条件（要求 cache_n=0 且 prompt_n=input_tokens）');
      const predictedN=modelTimings.predicted_n==null?null:Number(modelTimings.predicted_n);
      const timingComplete=modelTimings.prompt_ms!=null&&modelTimings.predicted_ms!=null&&promptN!=null&&predictedN===output;
      if(!timingComplete)validation.push('原生 Prompt/Decode timings 不完整或 Token 数不匹配');
      const cancelled=token!==benchPollToken||benchSuiteCancelRequested;
      const previousAttempts=Array.isArray(previous?.attempts)?previous.attempts.slice():[];
      if(previous)previousAttempts.push({status:previous.status||'unknown',job_id:previous.job_id||null,prompt_sha256:previous.prompt_sha256||null,failure:previous.failure||null,recorded_at:previous.finished_at||null,job:previous.job||null,analysis:previous.analysis||null,acceptance:previous.acceptance||null,runtime_before:previous.runtime_before||null,runtime_after:previous.runtime_after||null,validation:previous.validation||[]});
      const record={
        case_id:item.case_id,status:cancelled?'cancelled':(wrap?(job.status||'unknown'):(failure?'failed':'unknown')),
        job_id:job.job_id||previous?.job_id||readBenchStore().jobId||null,
        prompt_version:prompts.version,prompt_sha256:promptSHA,
        input_target:item.input_target,output_target:item.output_target,
        max_tokens:item.max_tokens,job:sanitizeBenchmarkToolArguments(job),
        analysis:sanitizeBenchmarkToolArguments(wrap?.analysis||{}),
        acceptance:wrap?.acceptance||null,
        runtime_before:runtimeBefore,runtime_after:runtimeAfter,
        validation,failure:failure||(cancelled?'轮询被取消，未取得任务最终结果':null),
        finished_at:new Date().toISOString(),attempts:previousAttempts
      };
      if(previousIndex>=0)suite.cases[previousIndex]=record;
      else suite.cases.push(record);
      const caseOrder=['S-P / S-O','L-P / S-O','S-P / L-O','L-P / L-O'];
      suite.cases.sort((a,b)=>caseOrder.indexOf(a.case_id)-caseOrder.indexOf(b.case_id));
      suite.current_case_id=null;
      saveBenchmarkSuiteState(suite);
      renderBenchmarkSuiteReport();
      if(token!==benchPollToken||benchSuiteCancelRequested)throw new Error('测试已停止。');
    }
    suite.current_case_id=null;
    const caseOrder=['S-P / S-O','L-P / S-O','S-P / L-O','L-P / L-O'];
    suite.cases.sort((a,b)=>caseOrder.indexOf(a.case_id)-caseOrder.indexOf(b.case_id));
    const allCasesRecorded=caseOrder.every(id=>suite.cases.some(item=>item.case_id===id));
    suite.status=allCasesRecorded?(suite.cases.every(item=>item.status==='completed')?'completed':'completed_with_failures'):'partial';
    suite.qualification_status=benchmarkSuiteQualification(suite);
  }catch(error){
    suite.status=suite.cases.length?'partial':'failed';
    suite.error=error?.message||'四组测试执行失败';
  }finally{
    suite.current_case_id=null;
    suite.finished_at=new Date().toISOString();
    saveBenchmarkSuiteState(suite);
    renderBenchmarkSuiteReport();
    applyBenchParams(restoreParams);
    const upstream=$('benchTarget')?.querySelector('code')?.textContent||suite.model_id||'';
    if(upstream)updateBenchReqPreview(upstream);
    benchSuiteActive=false;
    benchSuiteCancelRequested=false;
    setBenchRunning(false);
    if(runButton)runButton.disabled=false;
    if(resumeButton)resumeButton.disabled=false;
    if(status&&suite.error)status.textContent='执行未完整结束：'+suite.error+'。已保留当前已完成结果。';
    else if(status)status.textContent=suite.status==='completed'?'四组测试全部完成。请复制 Markdown 报告或原始 JSON 发送。':'测试流程结束，仍有未完成或不合格组；可以继续补跑未完成组。';
  }
}
async function runBenchmark(scenario='performance'){
  const isAcceptance=scenario==='acceptance';
  const acceptanceCaseID=isAcceptance?($('benchAcceptanceCase')?.value||''):'';
  if(isAcceptance){
    benchResultScope='acceptance';
    if(!acceptanceCaseID){const answer=benchResultElement('answer');if(answer){answer.className='bench-answer err';answer.textContent='请先选择验收用例。';}return;}
    scenario=acceptanceCaseID==='TM-05'?'tool_performance':'tool_performance_auto';
  }
  const toolScenario=scenario==='tool_performance'||scenario==='tool_performance_auto';
  if(!isAcceptance)benchResultScope=toolScenario?'tools':'performance';
  const promptOverride=toolScenario?($(isAcceptance?'benchAcceptancePrompt':'benchToolPrompt')?.value||'').trim():'';
  if(toolScenario&&!promptOverride){const answer=benchResultElement('answer');if(answer){answer.className='bench-answer err';answer.textContent='工具性能测试提示词不能为空。';}return;}
  const clientKey=($('modelTestApiKey')?.value||'').trim();
  if(!clientKey){
    const answer=benchResultElement('answer');
    if(answer){answer.className='bench-answer err';answer.textContent='请先在模型测试页面输入客户端服务密钥。';}
    $('modelTestApiKey')?.focus();
    return;
  }
  benchPollToken++;
  const token=benchPollToken;
  setBenchRunning(true);
  const ans0=benchResultElement('answer');if(ans0){ans0.className='bench-answer muted';ans0.textContent='正在连接上游…'}
  if(isAcceptance){const box=$('benchAcceptanceResult');if(box){box.className='bench-acceptance-result msg muted';box.textContent='正在提交验收任务…';}}
  benchResultElement('reasoningWrap')&&( benchResultElement('reasoningWrap').style.display='none');
  benchResultElement('analysis')&&(benchResultElement('analysis').style.display='none',benchResultElement('analysis').innerHTML='');
  benchResultElement('eventsFold')&&(benchResultElement('eventsFold').style.display='none');
  const met0=benchResultElement('metrics');if(met0)met0.innerHTML=statusPill('running');
  try{
    const benchTarget=await loadBenchPanel({resume:false});
    if(token!==benchPollToken)return;
    const upstream=typeof benchTarget==='string'?benchTarget:(benchTarget?.upstream||'');
    if(!upstream){
      throw new Error('当前 API 推理后端无已加载模型，请先在「模型管理」加载');
    }
    updateBenchReqPreview(upstream,scenario,promptOverride);
    const start=await api('/api/benchmark',{method:'POST',headers:{'Content-Type':'application/json','X-Client-API-Key':clientKey},body:JSON.stringify(benchPayloadForApi(benchTarget,scenario,promptOverride,acceptanceCaseID))});
    if(token!==benchPollToken)return;
    const jobId=start.job_id,t0=Date.now();
    writeBenchStore({jobId});
    persistBenchParams();
    await pollBenchmarkJob(jobId,t0,token);
  }catch(e){
    if(token!==benchPollToken)return;
    const ansE=benchResultElement('answer');if(ansE){ansE.className='bench-answer err';ansE.textContent=e.message}
    const metE=benchResultElement('metrics');if(metE)metE.innerHTML='';
    if(isAcceptance){const box=$('benchAcceptanceResult');if(box){box.className='bench-acceptance-result msg err';box.textContent=e.message||'验收任务提交失败';}}
  }finally{
    if(token===benchPollToken)setBenchRunning(false);
  }
}

async function loadBenchmarkAcceptanceCases(){
  const select=$('benchAcceptanceCase');
  if(!select||select.dataset.loaded==='true')return;
  try{
    const result=await api('/api/benchmark/acceptance-cases');
    const items=Array.isArray(result.items)?result.items:[];
    select.innerHTML=items.map(item=>{
      const suffix=item.manual_only?'（仅隔离环境，暂不可自动执行）':'';
      return '<option value="'+esc(item.id)+'" '+(item.manual_only?'disabled':'')+'>'+esc(item.id)+' · '+esc(item.title)+suffix+'</option>';
    }).join('');
    select.dataset.loaded='true';
    select.value='TM-01';
    updateBenchmarkAcceptancePrompt();
  }catch(e){
    select.innerHTML='<option value="">验收用例加载失败：'+esc(e.message)+'</option>';
  }
}
function updateBenchmarkAcceptancePrompt(){
  const id=$('benchAcceptanceCase')?.value;
  const prompts={
    'TM-01':'Go 语言当前最新正式稳定版的版本号和发布日期是什么？请说明依据并给出来源链接。',
    'TM-02':'请计算 17×23，并只给出结果。',
    'TM-03':'Go 语言当前最新正式稳定版的版本号和发布日期是什么？请说明依据并给出来源链接。',
    'TM-04':'Go 语言当前最新正式稳定版的版本号和发布日期是什么？请说明依据并给出来源链接。',
    'TM-05':'请搜索 Go 官方发布信息，并总结最新稳定版本。'
  };
  if(prompts[id]&&$('benchAcceptancePrompt'))$('benchAcceptancePrompt').value=prompts[id];
}
$('benchAcceptanceCase')?.addEventListener('change',updateBenchmarkAcceptancePrompt);
$('benchHistoryCompareBtn')?.addEventListener('click',()=>openBenchmarkComparison());
$('benchHistoryList')?.addEventListener('change',event=>{
  const input=event.target.closest('[data-bench-compare-id]');
  if(!input)return;
  const id=input.dataset.benchCompareId;
  if(input.checked){
    if(benchHistorySelected.size>=2){input.checked=false;return;}
    benchHistorySelected.add(id);
  }else{
    benchHistorySelected.delete(id);
  }
  updateBenchmarkCompareSelection();
});
$('benchHistoryRefreshBtn')?.addEventListener('click',()=>loadBenchHistory());
$('benchHistoryScenario')?.addEventListener('change',()=>{benchHistoryOffset=0;loadBenchHistory().catch(()=>{});});
$('benchHistoryStatus')?.addEventListener('change',()=>{benchHistoryOffset=0;loadBenchHistory().catch(()=>{});});
$('benchHistoryPrev')?.addEventListener('click',()=>{benchHistoryOffset=Math.max(0,benchHistoryOffset-BENCH_HISTORY_PAGE_SIZE);loadBenchHistory().catch(()=>{});});
$('benchHistoryNext')?.addEventListener('click',()=>{benchHistoryOffset+=BENCH_HISTORY_PAGE_SIZE;loadBenchHistory().catch(()=>{});});
$('benchHistoryList')?.addEventListener('click',event=>{
  const button=event.target.closest('[data-bench-history-id]');
  if(button)openBenchmarkHistory(button.dataset.benchHistoryId,button);
});
$('benchHistoryDetail')?.addEventListener('click',event=>{
  if(!event.target.closest('[data-bench-history-detail-close]'))return;
  $('benchHistoryDetail').hidden=true;
  if(benchHistoryDetailTrigger){
    benchHistoryDetailTrigger.setAttribute('aria-expanded','false');
    if(benchHistoryDetailTrigger.isConnected)benchHistoryDetailTrigger.focus();
  }
  benchHistoryDetailTrigger=null;
});

exposeGlobals({
  $, esc, getKey, api, fmtSize, fmtDate, slug, statusLabel, statusPill, emptyState, wrapTable,
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
  loadBenchHistory,
  loadBenchmarkAcceptanceCases,
  loadDashboardRecent,
  loadDashboardStats,
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
  runBenchmarkSuite,
  buildBenchmarkSuitePrompts,
  benchmarkSuitePromptForCase,
  benchmarkSuiteQualification,
  benchmarkSuiteCanResume,
  copyBenchmarkSuiteReport,
  renderBenchmarkSuiteReport,
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
if(isAuthenticated())refreshAll();
else setGlobalMsg($('dashboard')?.classList.contains('active')?'':'<span class="muted">请先登录控制台</span>');
const REQUESTS_POLL_MS=15000;
const DOWNLOADS_POLL_MS=5000;
setInterval(()=>{
  if(!isAuthenticated())return;
  if($('requests')?.classList.contains('active'))loadRequests({silent:true}).catch(()=>{});
},REQUESTS_POLL_MS);
setInterval(()=>{
  if(!isAuthenticated())return;
  if($('models-download')?.classList.contains('active'))loadDownloads().catch(()=>{});
},DOWNLOADS_POLL_MS);
if(isAuthenticated()&&$('dashboard')?.classList.contains('active'))dashboardPoll.start();
