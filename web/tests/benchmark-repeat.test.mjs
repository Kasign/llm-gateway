import test from 'node:test';
import assert from 'node:assert/strict';
import { normalizeBenchmarkRepeatCount, summarizeBenchmarkRepeat, buildBenchmarkRepeatMarkdown } from '../js/features/benchmark-repeat.mjs';

test('repeat count defaults to ten and enforces bounds', () => {
  assert.equal(normalizeBenchmarkRepeatCount(''), 10);
  assert.equal(normalizeBenchmarkRepeatCount('1'), 1);
  assert.equal(normalizeBenchmarkRepeatCount('20'), 20);
  for (const value of ['0', '21', '1.5', 'abc']) assert.throws(() => normalizeBenchmarkRepeatCount(value), /1～20/);
});

test('summary only includes completed samples with one identical non-empty condition fingerprint', () => {
  const summary = summarizeBenchmarkRepeat([
    { status: 'completed', condition_status: 'complete', condition_sha256: 'same', latency_ms: 100, ttft_ms: 20, cache_state: 'cold' },
    { status: 'completed', condition_status: 'complete', condition_sha256: 'same', latency_ms: 300, ttft_ms: 40, cache_state: 'partial' },
    { status: 'failed', condition_status: 'complete', condition_sha256: 'same', latency_ms: 999, ttft_ms: 99, cache_state: 'cold' }
  ]);
  assert.equal(summary.completed_count, 2);
  assert.equal(summary.attempted_count, 3);
  assert.equal(summary.comparable_count, 2);
  assert.equal(summary.cache_metrics.cold.metrics.latency_ms.median, 100);
  assert.equal(summary.cache_metrics.cold.metrics.ttft_ms.median, 20);
  assert.equal(summary.cache_metrics.cold.count, 1);
  assert.equal(summary.cache_metrics.partial.count, 1);
});

test('different condition fingerprints are not pooled', () => {
  const summary = summarizeBenchmarkRepeat([
    { status: 'completed', condition_status: 'complete', job_id: 'a', condition_sha256: 'one', latency_ms: 100 },
    { status: 'completed', condition_status: 'complete', job_id: 'b', condition_sha256: 'two', latency_ms: 200 }
  ]);
  assert.equal(summary.comparable_count, 0);
  assert.equal(summary.cache_metrics && Object.keys(summary.cache_metrics).length, 0);
});

test('traceability-only gaps allow limited comparison, but runtime-condition gaps still block', () => {
  const limited = summarizeBenchmarkRepeat([
    { status: 'completed', condition_status: 'partial', condition_sha256: 'same', condition_missing: ['model_sha256_unknown','gateway_revision_unknown','gateway_build_cleanliness_unknown'], latency_ms: 100, cache_state: 'partial' },
    { status: 'completed', condition_status: 'partial', condition_sha256: 'same', condition_missing: ['model_sha256_unknown','gateway_revision_unknown','gateway_build_cleanliness_unknown'], latency_ms: 200, cache_state: 'partial' }
  ]);
  assert.equal(limited.comparable_count, 2);
  assert.equal(limited.comparability_level, 'limited');
  assert.equal(limited.cache_metrics.partial.metrics.latency_ms.median, 150);
  const blocked = summarizeBenchmarkRepeat([
    { status: 'completed', condition_status: 'partial', condition_sha256: 'same', condition_missing: ['model_load_state_unknown','model_sha256_unknown'], latency_ms: 100 }
  ]);
  assert.equal(blocked.comparable_count, 0);
});

test('incomplete conditions and detected condition changes are excluded from pooled metrics', () => {
  const incomplete = summarizeBenchmarkRepeat([
    { status: 'completed', condition_status: 'partial', condition_sha256: 'same', latency_ms: 100 }
  ]);
  assert.equal(incomplete.completed_count, 1);
  assert.equal(incomplete.comparable_count, 0);
  const changed = summarizeBenchmarkRepeat([
    { status: 'completed', condition_status: 'complete', condition_sha256: 'same', latency_ms: 100 },
    { status: 'completed', condition_status: 'complete', condition_sha256: 'same', condition_mismatch: true, latency_ms: 200 }
  ]);
  assert.equal(changed.completed_count, 2);
  assert.equal(changed.comparable_count, 0);
  assert.equal(Object.keys(changed.cache_metrics).length, 0);
});

test('Markdown report retains Job IDs and comparability wording', () => {
  const markdown = buildBenchmarkRepeatMarkdown({
    status: 'completed', requested_count: 1,
    samples: [{ index: 1, status: 'completed', job_id: 'job-1', condition_sha256: 'hash', cache_state: 'cold', condition_status: 'complete', latency_ms: 123, ttft_ms: 45 }]
  });
  assert.match(markdown, /job-1/);
  assert.match(markdown, /条件指纹一致/);
  assert.match(markdown, /冷缓存/);
});

test('Decode research sample qualification requires long output, native timing and comparable conditions', async () => {
  const { isResearchDecodeSampleEligible, researchDecodeSampleFailureReason } = await import('../js/features/benchmark-repeat.mjs');
  const base = {
    status: 'completed', output_tokens: 256, decode_tokens: 255,
    decode_ms: 12000, decode_tokens_per_sec: 21.25,
    cache_state: 'full', condition_status: 'complete', condition_sha256: 'same'
  };
  assert.equal(isResearchDecodeSampleEligible(base), true);
  assert.equal(isResearchDecodeSampleEligible({ ...base, output_tokens: 255 }), false);
  assert.equal(isResearchDecodeSampleEligible({ ...base, output_tokens: undefined }), false);
  assert.equal(isResearchDecodeSampleEligible({ ...base, decode_tokens: null }), false);
  assert.equal(isResearchDecodeSampleEligible({ ...base, decode_tokens: undefined }), false);
  assert.equal(researchDecodeSampleFailureReason({ ...base, decode_tokens_per_sec: null }), 'native_decode_timing_unavailable');
  assert.equal(researchDecodeSampleFailureReason({ ...base, output_tokens: 120 }), 'decode_output_tokens_below_minimum');
  assert.equal(researchDecodeSampleFailureReason({ ...base, cache_state: 'unknown' }), 'cache_state_unknown');
  assert.equal(isResearchDecodeSampleEligible({ ...base, condition_status: 'partial', condition_missing: ['model_load_state_unknown'] }), false);
});

test('Decode research summary excludes short and non-comparable samples from effective count', () => {
  const samples = [
    { status: 'completed', output_tokens: 256, decode_tokens: 255, decode_ms: 12000, decode_tokens_per_sec: 20, cache_state: 'full', condition_status: 'complete', condition_sha256: 'same' },
    { status: 'completed', output_tokens: 120, decode_tokens: 119, decode_ms: 5000, decode_tokens_per_sec: 24, cache_state: 'full', condition_status: 'complete', condition_sha256: 'same' }
  ];
  const summary = summarizeBenchmarkRepeat(samples, { requireDecodeTiming: true });
  assert.equal(summary.completed_count, 2);
  assert.equal(summary.decode_eligible_count, 1);
  assert.equal(summary.comparable_count, 1);
});


test('Decode 报告逐样本展示 Prompt/Decode 速度和排除原因', () => {
  const markdown = buildBenchmarkRepeatMarkdown({
    status: 'sample_not_eligible', requested_count: 5,
    workflow: { version: 1, min_output_tokens: 256 },
    samples: [{ index: 1, status: 'completed', job_id: 'decode-job-1', input_tokens: 1200, output_tokens: 128,
      latency_ms: 9000, ttft_ms: 800, prompt_ms: 500, prompt_tokens_per_sec: 2400,
      decode_tokens: 127, decode_ms: 8200, decode_tokens_per_sec: 15.5,
      output_tokens_per_sec: 15.2, cache_state: 'cold', condition_status: 'complete',
      decode_exclusion_reason: 'decode_output_tokens_below_minimum' }]
  });
  assert.match(markdown, /Prompt ms \/ tok\/s/);
  assert.match(markdown, /Decode Token \/ ms \/ tok\/s/);
  assert.match(markdown, /输出 Token 数不足/);
  assert.match(markdown, /15.50/);
});
