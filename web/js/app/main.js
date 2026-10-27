import { $, esc } from '../core/dom.js';
import { getKey, api, initAdminKey, isAuthenticated } from '../core/api.js';
import { setGlobalMsg } from '../core/notify.js';
import { onAdminKeySaved } from '../features/auth.js';
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
  if(b.dataset.tab==='benchmark')loadBenchPanel().catch(()=>{});
  if(b.dataset.tab==='models-local')loadLocalModelsPanel().catch(()=>{});
  if(b.dataset.tab==='models-download')loadDownloadPanel({refreshCatalog:true}).catch(()=>{});
  if(b.dataset.tab==='models-inference'&&globalThis.InferenceParams)globalThis.InferenceParams.load().catch(()=>{});
  if(b.dataset.tab==='dashboard'){
    loadDashboardRecent().catch(()=>{});
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
function inferButtonHtml(backend,action,{modelId='',modelKey='',title='',confirmMsg='',label='',extraClass=''}){
  const cls=['btn','sm',extraClass];
  if(action==='load')cls.push('primary');
  if(action==='unload')cls.push('danger');
  const parts=[
    'type="button"','class="'+cls.filter(Boolean).join(' ')+'"','data-infer=""',
    'data-infer-backend="'+esc(backend)+'"','data-infer-action="'+esc(action)+'"'
  ];
  if(modelId)parts.push('data-model-id='+JSON.stringify(modelId));
  if(modelKey)parts.push('data-model-key='+JSON.stringify(modelKey));
  if(title)parts.push('data-infer-title='+JSON.stringify(title));
  if(confirmMsg)parts.push('data-confirm='+JSON.stringify(confirmMsg));
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
    el.innerHTML='<span class="pill pill-warn">请先配置管理员密钥</span>';
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
            <button type="button" class="btn sm danger" data-model-delete="ollama" data-model-id=${JSON.stringify(x.id)}>删除</button>
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

function adminKeyStatus(){
  const el=$('adminKeyStatus');
  if(!el)return;
  if(isAuthenticated()){el.innerHTML='<span class="pill pill-success">Web 已登录</span>';return}
  el.innerHTML=getKey()?'<span class="pill pill-success">密钥已保存</span>':'<span class="pill pill-warn">未配置</span>';
}
function saveKey(){
  localStorage.setItem('llm_gateway_admin_key',getKey());
  adminKeyStatus();
  onAdminKeySaved();
  refreshAll();
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
    if(!getKey())return;
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
    if(!getKey()||!$('dashboard')?.classList.contains('active')){this.stop();return;}
    if(this.inFlight){this.schedule();return;}
    this.inFlight=true;
    try{
      await loadSystem();
      this.auxTick++;
      const every=this.intervalMs<=4000?2:3;
      if(this.auxTick%every===0){
        await loadDashboardRecent().catch(()=>{});
      }
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
    $('filesBox').innerHTML=!fs.length?emptyState('无文件'):wrapTable('<tr><th>文件</th><th>大小</th><th>SHA256</th><th></th></tr>'+fs.map(f=>`<tr><td>${esc(f.path)}</td><td>${fmtSize(f.size)}</td><td class="small">${esc(f.sha256||'—')}</td><td><button type="button" class="btn sm" onclick='chooseFile(${JSON.stringify(f.path)},${JSON.stringify(repo)})'>选择</button></td></tr>`).join(''));
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

function llamaCatalogSourceLabel(item,managed){
  const source=String(item?.source||'').split('+').filter(Boolean);
  const labels=[];
  if(source.includes('router'))labels.push('Router 注册');
  if(source.includes('disk'))labels.push('共享磁盘');
  if(source.includes('gateway'))labels.push('Gateway 登记');
  if(source.includes('upstream'))labels.push('当前上游');
  if(!labels.length&&managed)labels.push('Gateway 登记');
  return labels.join(' + ')||'未确认来源';
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
    '<tr><th>模型名称</th><th>来源</th><th>模型 ID / 键</th><th>大小</th><th>状态</th><th>操作</th></tr>'+
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
        ?'<button type="button" class="btn sm danger" data-model-delete="gguf" data-model-key='+JSON.stringify(modelKey)+'>删除</button>'
        :rt?.disk_present&&rt?.path
          ?'<button type="button" class="btn sm danger" data-model-delete="gguf-disk" data-model-path='+JSON.stringify(String(rt.path))+'>删除</button>'
          :'';
      const defaultPill=rt?.active?' <span class="pill pill-active">Gateway 默认</span>':'';
      return '<tr><td>'+esc(name)+(x.quantization?'<div class="small muted">'+esc(x.quantization)+'</div>':'')+defaultPill+'</td>'+
        '<td>'+esc(llamaCatalogSourceLabel(rt,managed))+'</td>'+
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
  if(!getKey()){
    const el=$('keyTable');
    if(el)el.innerHTML=emptyState('请先保存管理员密钥');
    return;
  }
  await loadKeys();
}
async function loadKeys(){
  const j=await api('/api/admin/keys');const rows=j.data||[];
  $('keyTable').innerHTML=!rows.length?emptyState('暂无密钥'):wrapTable('<tr><th>名称</th><th>前缀</th><th>状态</th><th>限流</th><th>允许模型</th><th>过期</th><th>操作</th></tr>'+rows.map(x=>`<tr><td>${esc(x.name)}</td><td><code>${esc(x.prefix)}…</code></td><td>${statusPill(x.status)}</td><td>${x.rpm}/分钟</td><td class="small">${esc((x.allowed_models||[]).join(', ')||'全部')}</td><td class="small">${fmtDate(x.expires_at)}</td><td><div class="td-actions"><button type="button" class="btn sm" onclick="toggleKey(${x.id},'${x.status==='active'?'disable':'enable'}')">${x.status==='active'?'禁用':'启用'}</button><button type="button" class="btn sm" onclick="rotateKey(${x.id})">轮换</button><button type="button" class="btn sm danger" onclick="deleteKey(${x.id})">删除</button></div></td></tr>`).join(''));
}
async function createKey(){
  const models=$('keyModels').value.split(',').map(x=>x.trim()).filter(Boolean);
  try{
    const j=await api('/api/admin/keys',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:$('keyName').value.trim(),rpm:Number($('keyRPM').value)||60,allowed_models:models,expires_at:$('keyExpires').value.trim()||null})});
    $('newKeyBox').style.display='block';$('newKeyBox').innerHTML='<div class="keybox">'+esc(j.key)+'</div><div class="msg muted">请立即复制</div>';loadKeys();
  }catch(e){$('newKeyBox').style.display='block';$('newKeyBox').innerHTML='<span class="err">'+esc(e.message)+'</span>'}
}
async function toggleKey(id,a){try{await api('/api/admin/keys/'+id+'/'+a,{method:'POST'});loadKeys()}catch(e){alert(e.message)}}
async function rotateKey(id){if(!confirm('轮换后旧密钥立即失效，继续？'))return;try{const j=await api('/api/admin/keys/'+id+'/rotate',{method:'POST'});alert('新密钥：\n\n'+j.key);loadKeys()}catch(e){alert(e.message)}}
async function deleteKey(id){if(!confirm('确定删除该密钥？'))return;try{await api('/api/admin/keys/'+id,{method:'DELETE'});loadKeys()}catch(e){alert(e.message)}}

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
function benchPayloadForApi(target){
  const upstream=typeof target==='string'?target:(target?.upstream||'');
  const gatewayKey=(typeof target==='string'?'':(target?.gatewayKey||'')).trim();
  const p=collectBenchParams();
  // 有网关 model_key 时优先用它（小写业务键）；否则 fallback 到上游 loaded 的 model_id。
  // 发往 llama 的 chat body 里 "model" 仍是上游注册名，见 updateBenchReqPreview。
  const body=gatewayKey
    ?{model_key:gatewayKey,prompt:p.prompt,max_tokens:p.max_tokens}
    :{use_active:true,model_id:upstream,prompt:p.prompt,max_tokens:p.max_tokens};
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
  const run=$('benchRunBtn'),stop=$('benchStopBtn');
  if(run)run.disabled=!!running;
  if(stop)stop.disabled=!running;
}
function openBenchParamsDrawer(){
  const root=$('benchParamsDrawer');
  if(!root||root.classList.contains('open'))return;
  root.hidden=false;
  root.classList.add('open');
  root.setAttribute('aria-hidden','false');
  document.body.classList.add('drawer-open');
}
function closeBenchParamsDrawer(){
  const root=$('benchParamsDrawer');
  if(!root||!root.classList.contains('open'))return;
  root.classList.remove('open');
  root.hidden=true;
  root.setAttribute('aria-hidden','true');
  document.body.classList.remove('drawer-open');
}
document.addEventListener('keydown',e=>{
  if(e.key==='Escape')closeBenchParamsDrawer();
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

function updateBenchReqPreview(upstreamModel){
  const p=collectBenchParams();
  // 预览的是网关 → llama.cpp 的 /v1/chat/completions 体；"model" 必须是上游注册名（常与 GGUF 文件名同大小写），不是 model_key。
  const body={
    model:upstreamModel||'（当前上游）',
    messages:[{role:'user',content:p.prompt}],
    stream:true,
    max_tokens:p.max_tokens,
  };
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
  $('benchReqPreview').textContent=JSON.stringify(body,null,2);
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
function renderBenchAnalysis(wrap){
  const box=$('benchAnalysis');if(!box)return;
  const a=wrap.analysis;
  if(!a||!a.phases_ms){box.style.display='none';box.innerHTML='';return}
  const p=a.phases_ms,tp=a.throughput||{};
  box.style.display='block';
  box.innerHTML='<h3 style="margin:0 0 10px;font-size:14px">请求分析</h3><dl>'+
    [['总耗时',p.total_ms!=null?p.total_ms+' ms':null],['Gateway 准备',p.prep_ms+' ms'],['模型加载',p.model_load_ms+' ms'],['上游推理',p.upstream_ms+' ms'],
    ['首 token',p.ttft_ms!=null?p.ttft_ms+' ms':null],['首条正文',p.ttft_content_ms!=null?p.ttft_content_ms+' ms':null],
    ['思考阶段',p.thinking_ms!=null?p.thinking_ms+' ms':null],['正文生成',p.content_generation_ms!=null?p.content_generation_ms+' ms':null],
    ['输出速度',tp.output_tokens_per_sec!=null?tp.output_tokens_per_sec.toFixed(2)+' tok/s':null],
    ].filter(x=>x[1]!=null).map(([k,v])=>`<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('')+'</dl>'+
    (a.summary?.length?`<ul class="req-hints" style="margin:10px 0 0">${a.summary.map(l=>`<li>${esc(l)}</li>`).join('')}</ul>`:'')+
    (tp.note?`<p class="req-note">${esc(tp.note)}</p>`:'');
}
function renderBenchStreamEvents(evs){
  const fold=$('benchEventsFold'),tbl=$('benchEventsTable'),cnt=$('benchEventsCount');
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
  const ans=$('benchAnswer');
  if(ans){
    if(failed)ans.className='bench-answer err',ans.textContent=j.error||(j.status==='cancelled'?'任务已停止':'测试失败');
    else if(running&&!content&&!reasoning)ans.className='bench-answer muted',ans.textContent='推理中…';
    else ans.className='bench-answer',ans.textContent=content||'（尚无正文 content）';
  }
  const rw=$('benchReasoningWrap'),rb=$('benchReasoning');
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
  const met=$('benchMetrics');if(!met)return;
  const tps=wrap.analysis?.throughput?.output_tokens_per_sec;
  met.innerHTML=[
    statusPill(j.status==='completed'?'success':j.status==='failed'?'error':j.status==='cancelled'?'cancelled':j.status),
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
    j.request_id?`<button type="button" class="btn sm ghost" onclick="showDetail('${encodeURIComponent(j.request_id)}')">完整调用记录</button>`:'',
  ].filter(Boolean).join('');
  const rawBody=j.response||'';
  $('benchRaw').textContent=rawBody.trim().startsWith('data:')?rawBody:JSON.stringify({analysis:wrap.analysis,job:j,stream_events:evs,response:tryParse(rawBody)||rawBody},null,2);
  const ef=$('benchEventsFold');if(ef&&evs.length)ef.open=evs.length<=80;
}
function tryParse(s){try{return JSON.parse(s)}catch{return s}}

function renderBenchRunningMetrics(wrap,t0){
  const j=benchJobPayload(wrap);
  const metR=$('benchMetrics');
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
    return wrap;
  }
}

async function resumeBenchmarkSession(){
  const st=readBenchStore();
  if(!st.jobId)return;
  const token=++benchPollToken;
  try{
    const wrap=await api('/api/benchmark/'+encodeURIComponent(st.jobId));
    if(token!==benchPollToken)return;
    const j=benchJobPayload(wrap);
    if(j.status==='running'){
      setBenchRunning(true);
      const t0=Date.parse(j.started_at)||Date.now();
      const ans0=$('benchAnswer');if(ans0){ans0.className='bench-answer muted';ans0.textContent='恢复轮询中…'}
      await pollBenchmarkJob(st.jobId,t0,token);
    }else{
      renderBenchResult(wrap);
    }
  }catch{
    writeBenchStore({jobId:null});
  }finally{
    if(token===benchPollToken)setBenchRunning(false);
  }
}

async function stopBenchmark(){
  const st=readBenchStore();
  if(!st.jobId){setBenchRunning(false);return}
  benchPollToken++;
  setBenchRunning(false);
  try{
    await api('/api/benchmark/'+encodeURIComponent(st.jobId),{method:'DELETE'});
    const wrap=await api('/api/benchmark/'+encodeURIComponent(st.jobId));
    renderBenchResult(wrap);
  }catch(e){
    const ansE=$('benchAnswer');if(ansE){ansE.className='bench-answer err';ansE.textContent=e.message}
  }
}

async function runBenchmark(){
  benchPollToken++;
  const token=benchPollToken;
  setBenchRunning(true);
  const ans0=$('benchAnswer');if(ans0){ans0.className='bench-answer muted';ans0.textContent='正在连接上游…'}
  $('benchReasoningWrap')&&( $('benchReasoningWrap').style.display='none');
  $('benchAnalysis')&&($('benchAnalysis').style.display='none',$('benchAnalysis').innerHTML='');
  $('benchEventsFold')&&($('benchEventsFold').style.display='none');
  const met0=$('benchMetrics');if(met0)met0.innerHTML=statusPill('running');
  try{
    const benchTarget=await loadBenchPanel({resume:false});
    if(token!==benchPollToken)return;
    const upstream=typeof benchTarget==='string'?benchTarget:(benchTarget?.upstream||'');
    if(!upstream){
      throw new Error('当前 API 推理后端无已加载模型，请先在「模型管理」加载');
    }
    updateBenchReqPreview(upstream);
    const start=await api('/api/benchmark',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(benchPayloadForApi(benchTarget))});
    if(token!==benchPollToken)return;
    const jobId=start.job_id,t0=Date.now();
    writeBenchStore({jobId});
    persistBenchParams();
    await pollBenchmarkJob(jobId,t0,token);
  }catch(e){
    if(token!==benchPollToken)return;
    const ansE=$('benchAnswer');if(ansE){ansE.className='bench-answer err';ansE.textContent=e.message}
    const metE=$('benchMetrics');if(metE)metE.innerHTML='';
  }finally{
    if(token===benchPollToken)setBenchRunning(false);
  }
}

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
  if($('requests')?.classList.contains('active'))loadRequests().catch(()=>{});
},REQUESTS_POLL_MS);
setInterval(()=>{
  if(!isAuthenticated())return;
  if($('models-download')?.classList.contains('active'))loadDownloads().catch(()=>{});
},DOWNLOADS_POLL_MS);
if(isAuthenticated()&&$('dashboard')?.classList.contains('active'))dashboardPoll.start();
