export const RESEARCH_REQUIRED_PROFILE_PARAMETERS = Object.freeze([
  'threads', 'threads_batch', 'batch_size', 'ubatch_size', 'ctx_size',
  'n_gpu_layers', 'flash_attn', 'cache_type_k', 'cache_type_v'
]);

export const RESEARCH_DECODE_MIN_OUTPUT_TOKENS = 256;
export const RESEARCH_DECODE_REQUEST_PARAMS = Object.freeze({
  max_tokens: 512,
  temperature: 0,
  top_p: 1,
  frequency_penalty: 0,
  presence_penalty: 0,
  repeat_penalty: 1,
  seed: 42,
  enable_thinking: false
});

export const RESEARCH_DECODE_PROMPT = [
  '请用中文生成一份完整的技术说明，主题是“高性能本地大语言模型推理服务的工程优化”。',
  '请覆盖 CPU 线程调度、Prompt Prefill、Decode、KV Cache、内存带宽、量化、批处理、上下文长度、性能测量和稳定性。',
  '输出 12 个编号章节，每章都要包含原理、具体实现、常见误区和可验证的优化方法。',
  '每个章节至少写 2 个完整段落。不要写提纲，不要提前总结，不要省略后续章节。',
  '本测试用于测量持续 Token 生成速度，请确保正文足够长，目标生成不少于 300 Token。'
].join('\n\n');

export function parseResearchCandidateValue(parameter, raw) {
  const value = String(raw ?? '').trim();
  if (!value) throw new Error('Candidate 参数值不能为空。');
  if (parameter === 'flash_attn') {
    if (value !== 'true' && value !== 'false') throw new Error('flash_attn 只能填写 true 或 false。');
    return value === 'true';
  }
  if (parameter === 'cache_type_k' || parameter === 'cache_type_v') {
    if (!/^[a-zA-Z0-9_]{1,16}$/.test(value)) throw new Error(parameter + ' 格式无效。');
    return value;
  }
  if (!RESEARCH_REQUIRED_PROFILE_PARAMETERS.includes(parameter)) throw new Error('不支持的 llama.cpp 参数：' + parameter);
  const number = Number(value);
  if (!Number.isInteger(number)) throw new Error(parameter + ' 必须填写整数。');
  return number;
}

export function buildCandidateRuntimeProfile(profile, parameter, candidateValue) {
  if (!profile || !profile.id || !profile.parameters) throw new Error('Baseline 运行参数档案无效。');
  const missing = RESEARCH_REQUIRED_PROFILE_PARAMETERS.filter((key) =>
    !Object.prototype.hasOwnProperty.call(profile.parameters, key) ||
    profile.parameters[key] === null || profile.parameters[key] === undefined || profile.parameters[key] === ''
  );
  if (missing.length) throw new Error('Baseline 档案缺少明确参数：' + missing.join('、') + '。');
  if (!Object.prototype.hasOwnProperty.call(profile.parameters, parameter)) throw new Error('Baseline 档案没有记录变化参数 ' + parameter + '。');
  if (JSON.stringify(profile.parameters[parameter]) === JSON.stringify(candidateValue)) throw new Error('Candidate 值必须与 Baseline 值不同。');
  return {
    name: (String(profile.name || 'Baseline') + ' · ' + parameter + '=' + String(candidateValue)).slice(0, 80),
    backend: profile.backend,
    model_id: profile.model_id,
    parameters: { ...profile.parameters, [parameter]: candidateValue },
    notes: '由 ' + String(profile.name || profile.id) + ' v' + String(profile.version || 1) +
      ' 自动复制；仅声明变更 ' + parameter + '。实际启动参数需在 llama.cpp 重启后由用户确认。'
  };
}
