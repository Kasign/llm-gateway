(() => {
  const list=document.getElementById('keyServicesList'), summary=document.getElementById('keyServicesSelectionSummary');
  const hidden=document.getElementById('keyModels'), create=document.getElementById('keyCreateButton');
  if(!list||!summary||!hidden||!create)return;
  const builtins=[
    {id:'llamacpp',name:'llama.cpp',description:'内置本地推理服务'},
    {id:'ollama',name:'Ollama',description:'内置本地推理服务'}
  ];
  let services=[],loading=false;
  const escapeHTML=v=>String(v??'').replace(/[&<>"']/g,ch=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
  function selected(){return [...list.querySelectorAll('input[type="checkbox"]:checked')].map(x=>x.value)}
  function sync(){const ids=selected();hidden.value=ids.join(',');summary.textContent=ids.length?'已限制 '+ids.length+' 项服务：'+ids.join('、'):'当前未限制服务范围：此密钥可访问所有已配置服务。'}
  function render(keep=[]){
    const chosen=new Set(keep);
    if(!services.length){list.innerHTML='<div class="muted small">当前没有可选择的服务。</div>';sync();return}
    list.innerHTML=services.map(s=>'<label class="key-service-option"><input type="checkbox" value="'+escapeHTML(s.id)+'"'+(chosen.has(s.id)?' checked':'')+'><span class="key-service-option-copy"><strong>'+escapeHTML(s.name)+'</strong><small class="muted">'+escapeHTML(s.description)+'</small></span></label>').join('');
    list.querySelectorAll('input[type="checkbox"]').forEach(x=>x.addEventListener('change',sync));sync();
  }
  async function load(){
    if(loading)return;loading=true;create.disabled=true;
    const keep=selected();list.innerHTML='<div class="muted small">正在读取当前配置的服务…</div>';summary.textContent='正在读取服务列表。';
    try{
      if(typeof globalThis.api!=='function')throw new Error('管理台尚未初始化，请重新登录后重试');
      const response=await globalThis.api('/api/admin/providers');
      const remote=(response?.data||[]).filter(x=>x&&x.enabled===true&&String(x.id||'').trim()).map(x=>({id:String(x.id).trim(),name:String(x.id).trim(),description:'远程 Provider · 默认模型：'+(String(x.default_model||'').trim()||'未设置')}));
      const m=new Map(builtins.map(x=>[x.id,x]));for(const x of remote)if(!m.has(x.id))m.set(x.id,x);
      services=[...m.values()];render(keep.filter(id=>m.has(id)));create.disabled=false;
    }catch(e){list.innerHTML='<div class="err small">'+escapeHTML(e?.message||'读取服务列表失败')+'</div>';summary.textContent='服务列表加载失败；为避免意外创建无限制密钥，已禁用生成按钮。';create.disabled=true}
    finally{loading=false}
  }
  document.getElementById('btnSelectAllKeyServices')?.addEventListener('click',()=>{list.querySelectorAll('input[type="checkbox"]').forEach(x=>x.checked=true);sync()});
  document.getElementById('btnClearKeyServices')?.addEventListener('click',()=>{list.querySelectorAll('input[type="checkbox"]').forEach(x=>x.checked=false);sync()});
  document.getElementById('btnRefreshKeyServices')?.addEventListener('click',()=>void load());
  document.querySelector('.nav button[data-tab="keys"]')?.addEventListener('click',()=>void load());
})();