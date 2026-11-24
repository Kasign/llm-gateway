import test from 'node:test';
import assert from 'node:assert/strict';
import {
  RESEARCH_DECODE_MIN_OUTPUT_TOKENS,
  RESEARCH_DECODE_REQUEST_PARAMS,
  RESEARCH_REQUIRED_PROFILE_PARAMETERS,
  buildCandidateRuntimeProfile,
  parseResearchCandidateValue
} from '../js/features/research-workflow.mjs';

const profile = {
  id: 'baseline-1', name: 'NUC Baseline', version: 3, backend: 'llamacpp', model_id: 'qwen',
  parameters: {
    threads: 4, threads_batch: 4, batch_size: 512, ubatch_size: 128,
    ctx_size: 8192, n_gpu_layers: 0, flash_attn: false, cache_type_k: 'f16', cache_type_v: 'f16'
  }
};

test('Decode 实验固定输出规模与推理条件', () => {
  assert.equal(RESEARCH_DECODE_MIN_OUTPUT_TOKENS, 256);
  assert.equal(RESEARCH_DECODE_REQUEST_PARAMS.max_tokens, 512);
  assert.equal(RESEARCH_DECODE_REQUEST_PARAMS.temperature, 0);
  assert.equal(RESEARCH_DECODE_REQUEST_PARAMS.seed, 42);
});

test('Candidate 档案只改变声明参数并保留其他实际条件', () => {
  const candidate = buildCandidateRuntimeProfile(profile, 'threads', 8);
  assert.equal(candidate.parameters.threads, 8);
  for (const key of RESEARCH_REQUIRED_PROFILE_PARAMETERS.filter(key => key !== 'threads')) {
    assert.deepEqual(candidate.parameters[key], profile.parameters[key], key);
  }
  assert.equal(candidate.model_id, profile.model_id);
  assert.equal(candidate.backend, profile.backend);
});

test('Candidate 参数按参数类型解析', () => {
  assert.equal(parseResearchCandidateValue('threads', '8'), 8);
  assert.equal(parseResearchCandidateValue('flash_attn', 'true'), true);
  assert.equal(parseResearchCandidateValue('cache_type_k', 'q8_0'), 'q8_0');
  assert.throws(() => parseResearchCandidateValue('threads', '4.5'));
  assert.throws(() => parseResearchCandidateValue('flash_attn', 'on'));
});

test('Baseline 档案缺少参数时拒绝创建 Candidate', () => {
  const incomplete = { ...profile, parameters: { ...profile.parameters } };
  delete incomplete.parameters.n_gpu_layers;
  assert.throws(() => buildCandidateRuntimeProfile(incomplete, 'threads', 8), /n_gpu_layers/);
});
