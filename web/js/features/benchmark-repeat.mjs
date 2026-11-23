export const BENCHMARK_REPEAT_MIN=1;
export const BENCHMARK_REPEAT_MAX=20;
export const BENCHMARK_REPEAT_DEFAULT=10;
export function normalizeBenchmarkRepeatCount(value){
  const raw=value==null||String(value).trim()===''?BENCHMARK_REPEAT_DEFAULT:Number(value);
  if(!Number.isInteger(raw)||raw<BENCHMARK_REPEAT_MIN||raw>BENCHMARK_REPEAT_MAX)throw new RangeError('重复次数必须是 1～20 之间的整数。');
  return raw;
}
function numericValues(samples,key){return(samples||[]).map(sample=>sample?.[key]).filter(value=>value!==null&&value!==undefined&&value!=='').map(Number).filter(Number.isFinite);}
function median(values){
  if(!values.length)return null;
  const sorted=[...values].sort((a,b)=>a-b),middle=Math.floor(sorted.length/2);
  return sorted.length%2?sorted[middle]:(sorted[middle-1]+sorted[middle])/2;
}
function traceabilityOnlyMissing(missing=[]){
  if(!Array.isArray(missing)||missing.length===0)return false;
  const allowed=new Set(['model_sha256_unknown','gateway_revision_unknown','gateway_build_cleanliness_unknown']);
  return missing.every(item=>allowed.has(item));
}
export function summarizeBenchmarkRepeat(samples=[]){
  const completed=samples.filter(sample=>sample?.status==='completed'),groups=new Map();
  const conditionMismatch=completed.some(sample=>sample?.condition_mismatch===true);
  const eligible=completed.filter(sample=>(sample?.condition_status==='complete'||(sample?.condition_status==='partial'&&traceabilityOnlyMissing(sample.condition_missing)))&&String(sample.condition_sha256||'').trim()!=='');
  for(const sample of eligible){
    const key=String(sample.condition_sha256).trim();
    if(!groups.has(key))groups.set(key,[]);
    groups.get(key).push(sample);
  }
  const ranked=[...groups.entries()].sort((a,b)=>b[1].length-a[1].length);
  const comparable=!conditionMismatch&&ranked.length===1?ranked[0][1]:[];
  const metricKeys=['latency_ms','ttft_ms','output_tokens_per_sec','prompt_tokens_per_sec','decode_tokens_per_sec'];
  const cacheGroups={};
  for(const sample of comparable){
    const cacheKey=sample.cache_state||'unknown';
    if(!cacheGroups[cacheKey])cacheGroups[cacheKey]=[];
    cacheGroups[cacheKey].push(sample);
  }
  const cacheMetrics={};
  for(const [cacheKey,items] of Object.entries(cacheGroups)){
    cacheMetrics[cacheKey]={count:items.length,metrics:{}};
    for(const key of metricKeys){
      const values=numericValues(items,key);
      cacheMetrics[cacheKey].metrics[key]={count:values.length,min:values.length?Math.min(...values):null,median:median(values),max:values.length?Math.max(...values):null};
    }
  }
  const limited=comparable.some(sample=>Array.isArray(sample.condition_missing)&&sample.condition_missing.length>0);
  return{completed_count:completed.length,attempted_count:samples.length,comparable_count:comparable.length,condition_group_count:groups.size,comparability_level:comparable.length?(limited?'limited':'strict'):'not_comparable',cache_metrics:cacheMetrics};
}
function display(value){return value==null||!Number.isFinite(Number(value))?'—':Number(value).toFixed(2);}
export function buildBenchmarkRepeatMarkdown(report){
  if(!report)return'尚无重复采样报告。';
  const samples=Array.isArray(report.samples)?report.samples:[],summary=summarizeBenchmarkRepeat(samples);
  const metricLabels=[['latency_ms','总耗时','ms'],['ttft_ms','TTFT','ms'],['output_tokens_per_sec','输出吞吐','tok/s'],['prompt_tokens_per_sec','Prompt 吞吐','tok/s'],['decode_tokens_per_sec','Decode 吞吐','tok/s']];
  const levelLabel={strict:'严格可比',limited:'有限可比（追溯信息不完整）',not_comparable:'不可比较'};
  const lines=['# Benchmark 串行重复采样报告','','- 状态：'+(report.status||'unknown'),'- 计划次数：'+(report.requested_count??'unknown'),'- 实际尝试：'+summary.attempted_count,'- 服务端完成：'+summary.completed_count,'- 同条件可比样本：'+summary.comparable_count,'- 可比等级：'+(levelLabel[summary.comparability_level]||'不可比较'),'- 条件指纹分组：'+summary.condition_group_count,'- 开始时间：'+(report.started_at||'unknown'),'- 结束时间：'+(report.finished_at||'进行中'),report.error?'- 执行错误：'+report.error:'','','## 指标汇总（按缓存状态分组；仅统计条件指纹一致且运行条件满足要求的 completed 样本）','','| 缓存状态 | 指标 | 有效样本数 | 最小值 | 中位数 | 最大值 |','|---|---|---:|---:|---:|---:|'];
  const cacheLabels={cold:'冷缓存',partial:'部分命中',full:'全命中',unknown:'未知',inconsistent:'证据不一致'};
  for(const [cacheKey,group] of Object.entries(summary.cache_metrics)){
    for(const [key,label,unit] of metricLabels){
      const item=group.metrics[key];
      lines.push('| '+(cacheLabels[cacheKey]||cacheKey)+' | '+label+'（'+unit+'） | '+item.count+' | '+display(item.min)+' | '+display(item.median)+' | '+display(item.max)+' |');
    }
  }
  if(!Object.keys(summary.cache_metrics).length)lines.push('| — | 暂无可比样本 | 0 | — | — | — |');
  lines.push('','## 单次任务','','| 次数 | 状态 | Job ID | 运行档案 | 缓存 | 条件状态 | 输入/输出 Token | 总耗时 ms | TTFT ms | Prompt ms | Decode ms | 输出 tok/s | 错误 |','|---:|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---|');
  for(const sample of samples){
    const error=String(sample.error||'').replace(/\|/g,'\\|');
    const runtimeProfile=sample.runtime_profile_id?(sample.runtime_profile_id+'@v'+(sample.runtime_profile_version??'?')):'未知';
    lines.push('| '+sample.index+' | '+(sample.condition_mismatch?'completed（条件变化）':(sample.status||'unknown'))+' | '+(sample.job_id||'—')+' | '+runtimeProfile+' | '+(cacheLabels[sample.cache_state]||sample.cache_state||'未知')+' | '+(sample.condition_status||'unknown')+' | '+(sample.input_tokens??'—')+'/'+(sample.output_tokens??'—')+' | '+display(sample.latency_ms)+' | '+display(sample.ttft_ms)+' | '+display(sample.prompt_ms)+' | '+display(sample.decode_ms)+' | '+display(sample.output_tokens_per_sec)+' | '+error+' |');
  }
  lines.push('','说明：每条记录对应独立 Benchmark Job。指标汇总只纳入服务端状态 completed、运行条件完整或仅缺少模型 SHA / Gateway Revision / 构建洁净状态等追溯信息，且 condition_sha256 相同、非空的样本；不同缓存状态分开列示。模型加载状态、Backend 版本、硬件、模型标识或完整 Backend 运行参数档案缺失时仍不纳入比较。');
  return lines.join('\n');
}
