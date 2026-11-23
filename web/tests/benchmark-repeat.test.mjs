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
    { status: 'completed', condition_sha256: 'same', latency_ms: 100, ttft_ms: 20, cache_state: 'cold' },
    { status: 'completed', condition_sha256: 'same', latency_ms: 300, ttft_ms: 40, cache_state: 'partial' },
    { status: 'failed', condition_sha256: 'same', latency_ms: 999, ttft_ms: 99, cache_state: 'cold' }
  ]);
  assert.equal(summary.completed_count, 2);
  assert.equal(summary.attempted_count, 3);
  assert.equal(summary.comparable_count, 2);
  assert.equal(summary.metrics.latency_ms.median, 200);
  assert.equal(summary.metrics.ttft_ms.median, 30);
  assert.equal(summary.cache_metrics.cold.count, 1);
  assert.equal(summary.cache_metrics.partial.count, 1);
});

test('different condition fingerprints are not pooled', () => {
  const summary = summarizeBenchmarkRepeat([
    { status: 'completed', job_id: 'a', condition_sha256: 'one', latency_ms: 100 },
    { status: 'completed', job_id: 'b', condition_sha256: 'two', latency_ms: 200 }
  ]);
  assert.equal(summary.comparable_count, 0);
  assert.equal(summary.metrics.latency_ms.count, 0);
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
