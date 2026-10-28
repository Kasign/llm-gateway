(() => {
  const list=document.getElementById('keyServicesList'), summary=document.getElementById('keyServicesSelectionSummary');
  const hidden=document.getElementById('keyModels'), create=document.getElementById('keyCreateButton');
  if(!list||!summary||!hidden||!create)return;
  const editModal=document.getElementById('keyEditModal'), editForm=document.getElementById('keyEditForm');
  const editList=document.getElementById('keyEditServicesList'), editSummary=document.getElementById('keyEditServiceSummary');
  const editLegacyPanel=document.getElementById('keyEditLegacyPanel'), editLegacyMessage=document.getElementById('keyEditLegacyMessage');
  const preserveLegacy=document.getElementById('keyEditPreserveLegacy'), editError=document.getElementById('keyEditError');
  const editSave=document.getElementById('keyEditSave');
  const editRefresh=document.getElementById('btnRefreshEditServices');
  const builtins=[
    {id:'llamacpp',name:'llama.cpp',description:'内置本地推理服务'},
    {id:'ollama',name:'Ollama',description:'内置本地推理服务'}
  ];
  let services=[],loading=false,catalogPromise=null,editingKey=null,editingLegacyModels=[],saving=false,editTrigger=null,editTriggerKeyId='',editEpoch=0;
  const escapeHTML=v=>String(v??'').replace(/[&<>"']/g,ch=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
  function selectedIds(target){return [...target.querySelectorAll('input[type="checkbox"]:checked')].map(x=>x.value)}
  async function loadServiceCatalog(){
    if(catalogPromise)return catalogPromise;
    catalogPromise=(async()=>{
      if(typeof globalThis.api!=='function')throw new Error('管理台尚未初始化，请重新登录后重试');
      const response=await globalThis.api('/api/admin/providers');
      const remote=(response?.data||[]).filter(x=>x&&x.enabled===true&&String(x.id||'').trim())
        .map(x=>({id:String(x.id).trim(),name:String(x.id).trim(),description:'远程 Provider · 默认模型：'+(String(x.default_model||'').trim()||'未设置')}));
      const merged=new Map(builtins.map(x=>[x.id,x]));for(const item of remote)if(!merged.has(item.id))merged.set(item.id,item);
      services=[...merged.values()];return services;
    })();
    try{return await catalogPromise}finally{catalogPromise=null}
  }
  function renderServiceOptions(target,selected){
    const chosen=new Set(selected||[]);
    if(!services.length){target.innerHTML='<div class="muted small">当前没有可选择的服务。</div>';return}
    target.innerHTML=services.map(s=>'<label class="key-service-option"><input type="checkbox" value="'+escapeHTML(s.id)+'"'+(chosen.has(s.id)?' checked':'')+'><span class="key-service-option-copy"><strong>'+escapeHTML(s.name)+'</strong><small class="muted">'+escapeHTML(s.description)+'</small></span></label>').join('');
  }
  function syncCreateSelection(){
    const ids=selectedIds(list);hidden.value=ids.join(',');
    summary.textContent=ids.length?'已限制 '+ids.length+' 项服务：'+ids.join('、'):'当前未限制服务范围：此密钥可访问所有已配置服务。';
  }
  async function loadCreateServices(){
    if(loading)return;loading=true;create.disabled=true;
    const keep=selectedIds(list);list.innerHTML='<div class="muted small">正在读取当前配置的服务…</div>';summary.textContent='正在读取服务列表。';
    try{await loadServiceCatalog();renderServiceOptions(list,keep.filter(id=>services.some(s=>s.id===id)));syncCreateSelection();create.disabled=false}
    catch(e){list.innerHTML='<div class="err small">'+escapeHTML(e?.message||'读取服务列表失败')+'</div>';summary.textContent='服务列表加载失败；为避免意外创建无限制密钥，已禁用生成按钮。';create.disabled=true}
    finally{loading=false}
  }
  async function serviceKeyRequest(url,options={}){
    const path=new URL(url,window.location.origin).pathname;
    if(path!=='/api/admin/keys'&&!path.startsWith('/api/admin/keys/'))throw new Error('管理员 API Key 仅允许用于服务密钥管理接口');
    const key=localStorage.getItem('llm_gateway_admin_key')||'';if(!key)throw new Error('请先在“服务密钥”页面输入并验证管理员 API Key');
    const headers={...(options.headers||{})};delete headers.Authorization;delete headers.authorization;headers['X-Admin-Key']=key;
    const response=await fetch(url,{...options,headers});let body=null;try{body=await response.json()}catch{/* 空响应 */}
    if(!response.ok)throw new Error(body?.error?.message||body?.message||('HTTP '+response.status));return body;
  }
  function syncEditSelection(){
    if(!editList||!editSummary)return;const selected=selectedIds(editList);
    const keepLegacy=editingLegacyModels.length>0&&preserveLegacy?.checked;
    if(keepLegacy&&selected.length)editSummary.textContent='已限制 '+selected.length+' 项服务，并保留 '+editingLegacyModels.length+' 条旧模型 ID 权限。旧模型 ID 仍可能允许未勾选服务按模型名匹配。';
    else if(keepLegacy)editSummary.textContent='未选择服务，但仍保留 '+editingLegacyModels.length+' 条旧模型 ID 限制；当前并非不限制访问。';
    else if(selected.length)editSummary.textContent='仅保留所选服务范围：'+selected.join('、')+'。';
    else editSummary.textContent='当前未限制访问服务：该密钥可访问所有已配置服务。';
  }
  function showEditModal(trigger){
    if(!editModal)return;
    editTrigger=trigger||document.activeElement;
    editTriggerKeyId=String(editTrigger?.dataset?.keyId||'');
    editModal.classList.add('show');
    editModal.setAttribute('aria-hidden','false');
    editError.textContent='';
    document.getElementById('keyEditClose')?.focus();
  }
  function restoreEditFocus(){
    const trigger=editTrigger;
    const id=editTriggerKeyId;
    editTrigger=null;
    editTriggerKeyId='';
    const refreshedTrigger=id?document.querySelector('#keyTable .key-edit-button[data-key-id="'+id+'"]'):null;
    const target=trigger?.isConnected?trigger:refreshedTrigger;
    if(target){
      if(target.disabled)target.disabled=false;
      target.focus();
    }
  }
  function hideEditModal(){
    if(!editModal||saving)return;
    editEpoch+=1;
    if(editRefresh)editRefresh.disabled=false;
    editModal.classList.remove('show');
    editModal.setAttribute('aria-hidden','true');
    editingKey=null;
    editingLegacyModels=[];
    restoreEditFocus();
  }
  function hideEditModalAfterSave(){
    if(!editModal)return;
    editEpoch+=1;
    if(editRefresh)editRefresh.disabled=false;
    editModal.classList.remove('show');
    editModal.setAttribute('aria-hidden','true');
    editingKey=null;
    editingLegacyModels=[];
    restoreEditFocus();
  }
  async function openEditKey(id,button){
    if(!editModal||!editForm||!editList||saving)return;
    const epoch=++editEpoch;
    if(button)button.disabled=true;
    if(editRefresh)editRefresh.disabled=true;
    showEditModal(button);
    editError.textContent='正在读取密钥和服务列表…';
    editSave.disabled=true;
    editList.innerHTML='<div class="muted small">正在读取服务列表…</div>';
    try{
      const [keyResponse]=await Promise.all([
        serviceKeyRequest('/api/admin/keys',{cache:'no-store'}),
        loadServiceCatalog()
      ]);
      if(epoch!==editEpoch||!editModal.classList.contains('show'))return;
      const key=(keyResponse?.data||[]).find(item=>Number(item.id)===Number(id));
      if(!key)throw new Error('未找到这把密钥，刷新列表后重试');
      editingKey=key;
      const current=[...new Set((key.allowed_models||[]).map(v=>String(v).trim()).filter(Boolean))];
      const known=new Set(services.map(s=>s.id)),selected=current.filter(v=>known.has(v));
      editingLegacyModels=current.filter(v=>!known.has(v));
      document.getElementById('keyEditId').value=String(key.id);
      document.getElementById('keyEditName').value=String(key.name||'');
      document.getElementById('keyEditRPM').value=String(Number(key.rpm)||60);
      document.getElementById('keyEditExpires').value=key.expires_at?String(key.expires_at):'';
      renderServiceOptions(editList,selected);
      if(editingLegacyModels.length){
        editLegacyPanel.hidden=false;
        editLegacyMessage.textContent='检测到旧模型 ID 权限：'+editingLegacyModels.join('、')+'。默认保留以避免意外改变现有权限；保留时，模型 ID 规则仍可能允许未勾选的服务。';
        preserveLegacy.checked=true;
      }else{
        editLegacyPanel.hidden=true;
        editLegacyMessage.textContent='';
        preserveLegacy.checked=false;
      }
      editError.textContent='';
      editSave.disabled=false;
      syncEditSelection();
      document.getElementById('keyEditName')?.focus();
    }catch(e){
      if(epoch!==editEpoch||!editModal.classList.contains('show'))return;
      editError.textContent=e?.message||'读取密钥信息失败';
      editSave.disabled=true;
    }finally{
      if(epoch===editEpoch&&button)button.disabled=false;
      if(epoch===editEpoch&&editRefresh)editRefresh.disabled=false;
    }
  }
  async function saveEditKey(event){
    event?.preventDefault?.();if(!editingKey||saving||!editForm.reportValidity())return;
    const name=document.getElementById('keyEditName').value.trim(),rpm=Number(document.getElementById('keyEditRPM').value);
    if(!name){editError.textContent='请填写密钥名称。';return}
    if(!Number.isInteger(rpm)||rpm<1){editError.textContent='每分钟请求数必须是大于 0 的整数。';return}
    const allowedModels=selectedIds(editList);
    if(editingLegacyModels.length&&preserveLegacy.checked)allowedModels.push(...editingLegacyModels);
    const expires=document.getElementById('keyEditExpires').value.trim();
    const payload={name,rpm,allowed_models:[...new Set(allowedModels)],expires_at:expires||null};
    saving=true;editSave.disabled=true;document.getElementById('keyEditCancel').disabled=true;editError.textContent='正在保存修改…';
    try{
      await serviceKeyRequest('/api/admin/keys/'+encodeURIComponent(String(editingKey.id)),{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});
      if(typeof globalThis.loadKeys==='function')await globalThis.loadKeys();
      hideEditModalAfterSave();
      if(typeof globalThis.setGlobalMsg==='function')globalThis.setGlobalMsg('<span class="ok">服务密钥修改成功，列表已更新。</span>');
    }catch(e){editError.textContent=e?.message||'保存修改失败'}
    finally{saving=false;editSave.disabled=false;document.getElementById('keyEditCancel').disabled=false}
  }
  document.getElementById('btnSelectAllKeyServices')?.addEventListener('click',()=>{list.querySelectorAll('input[type="checkbox"]').forEach(x=>x.checked=true);syncCreateSelection()});
  document.getElementById('btnClearKeyServices')?.addEventListener('click',()=>{list.querySelectorAll('input[type="checkbox"]').forEach(x=>x.checked=false);syncCreateSelection()});
  document.getElementById('btnRefreshKeyServices')?.addEventListener('click',()=>void loadCreateServices());
  document.querySelector('.nav button[data-tab="keys"]')?.addEventListener('click',()=>void loadCreateServices());
  list.addEventListener('change',syncCreateSelection);
  document.getElementById('btnSelectAllEditServices')?.addEventListener('click',()=>{editList?.querySelectorAll('input[type="checkbox"]').forEach(x=>x.checked=true);syncEditSelection()});
  document.getElementById('btnClearEditServices')?.addEventListener('click',()=>{editList?.querySelectorAll('input[type="checkbox"]').forEach(x=>x.checked=false);if(preserveLegacy)preserveLegacy.checked=false;syncEditSelection()});
  editRefresh?.addEventListener('click',async()=>{
    const epoch=editEpoch;
    const keep=selectedIds(editList);
    try{
      await loadServiceCatalog();
      if(epoch!==editEpoch||!editModal?.classList.contains('show'))return;
      renderServiceOptions(editList,keep.filter(id=>services.some(s=>s.id===id)));
      syncEditSelection();
      editError.textContent='';
      editSave.disabled=!editingKey;
    }catch(e){
      if(epoch!==editEpoch||!editModal?.classList.contains('show'))return;
      editError.textContent=e?.message||'刷新服务列表失败';
      editSave.disabled=true;
    }
  });
  editList?.addEventListener('change',syncEditSelection);preserveLegacy?.addEventListener('change',syncEditSelection);
  editForm?.addEventListener('submit',saveEditKey);
  document.getElementById('keyEditCancel')?.addEventListener('click',hideEditModal);
  document.getElementById('keyEditClose')?.addEventListener('click',hideEditModal);
  editModal?.addEventListener('click',event=>{if(event.target===editModal)hideEditModal()});
  document.addEventListener('keydown',event=>{
    if(!editModal?.classList.contains('show'))return;
    if(event.key==='Escape'){event.preventDefault();hideEditModal();return}
    if(event.key!=='Tab')return;
    const focusable=[...editModal.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])')]
      .filter(el=>!el.hidden&&el.getClientRects().length>0);
    if(!focusable.length)return;
    const first=focusable[0],last=focusable[focusable.length-1],active=document.activeElement;
    if(event.shiftKey&&(active===first||!editModal.contains(active))){event.preventDefault();last.focus()}
    else if(!event.shiftKey&&(active===last||!editModal.contains(active))){event.preventDefault();first.focus()}
  });
  const keyTable=document.getElementById('keyTable');
  function enhanceKeyTable(){
    const table=keyTable?.querySelector('table');if(!table)return;
    table.querySelectorAll('th').forEach(th=>{if(th.textContent.trim()==='允许模型')th.textContent='访问范围'});
    table.querySelectorAll('tr').forEach(row=>{
      const actions=row.querySelector('.td-actions');if(!actions||actions.querySelector('.key-edit-button'))return;
      const toggle=actions.querySelector('button[onclick^="toggleKey("]'),match=String(toggle?.getAttribute('onclick')||'').match(/toggleKey\((\d+)/);
      if(!match)return;
      const button=document.createElement('button');button.type='button';button.className='btn sm key-edit-button';button.dataset.keyId=match[1];button.textContent='编辑';
      button.addEventListener('click',()=>void openEditKey(match[1],button));actions.prepend(button);
    });
  }
  if(keyTable){new MutationObserver(enhanceKeyTable).observe(keyTable,{childList:true,subtree:true});enhanceKeyTable()}
  syncCreateSelection();void loadCreateServices();
})();