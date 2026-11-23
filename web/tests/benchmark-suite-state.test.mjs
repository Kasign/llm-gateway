import test from 'node:test';
import assert from 'node:assert/strict';

import {
  benchmarkSuiteCaseHasRecoverableJob,
  benchmarkSuiteIsActiveStatus,
  benchmarkSuiteIsTerminalStatus,
  benchmarkSuiteStatusForReport,
  benchmarkSuiteTaskOutcome
} from '../js/features/benchmark-suite-state.mjs';

test('completed server response is authoritative, even when the client requested cancellation', () => {
  const outcome = benchmarkSuiteTaskOutcome({
    jobStatus: 'completed',
    jobID: 'job-completed',
    cancelled: true,
    lastObservedServerStatus: 'running'
  });

  assert.deepEqual(outcome, {
    status: 'completed',
    server_status: 'completed',
    poll_status: 'completed',
    result_status: 'completed'
  });
});

test('poll interruption does not turn a job ID into a server-confirmed running status', () => {
  const outcome = benchmarkSuiteTaskOutcome({
    jobID: 'job-1',
    lastObservedServerStatus: 'running'
  });

  assert.deepEqual(outcome, {
    status: 'running',
    server_status: 'running',
    poll_status: 'interrupted',
    result_status: 'unknown'
  });
  assert.equal(benchmarkSuiteCaseHasRecoverableJob({ ...outcome, job_id: 'job-1' }), true);
});

test('first poll failure preserves unknown server status and the original job for recovery', () => {
  const outcome = benchmarkSuiteTaskOutcome({ jobID: 'job-2' });

  assert.equal(outcome.status, 'running');
  assert.equal(outcome.server_status, 'unknown');
  assert.equal(outcome.poll_status, 'interrupted');
  assert.equal(outcome.result_status, 'unknown');
  assert.equal(benchmarkSuiteCaseHasRecoverableJob({ ...outcome, job_id: 'job-2' }), true);
});

test('client cancellation with a submitted job remains recoverable until server terminal state is observed', () => {
  const outcome = benchmarkSuiteTaskOutcome({
    jobID: 'job-3',
    cancelled: true,
    lastObservedServerStatus: 'running'
  });

  assert.equal(outcome.status, 'cancelled');
  assert.equal(outcome.server_status, 'running');
  assert.equal(outcome.poll_status, 'cancelled');
  assert.equal(outcome.result_status, 'unknown');
  assert.equal(benchmarkSuiteCaseHasRecoverableJob({ ...outcome, job_id: 'job-3' }), true);
});

test('terminal outcomes and legacy running records keep distinct recovery behavior', () => {
  assert.equal(benchmarkSuiteIsTerminalStatus('failed'), true);
  assert.equal(benchmarkSuiteIsTerminalStatus('running'), false);
  assert.equal(benchmarkSuiteIsTerminalStatus('cancelling'), false);
  assert.equal(benchmarkSuiteIsActiveStatus('running'), true);
  assert.equal(benchmarkSuiteIsActiveStatus('cancelling'), true);
  assert.equal(benchmarkSuiteIsActiveStatus('completed'), false);
  assert.equal(benchmarkSuiteCaseHasRecoverableJob({ status: 'running', job_id: 'legacy-job' }), true);
  assert.equal(benchmarkSuiteCaseHasRecoverableJob({ status: 'completed', job_id: 'done-job' }), false);
  assert.equal(benchmarkSuiteCaseHasRecoverableJob({ status: 'failed', job_id: 'failed-job' }), false);
  assert.equal(benchmarkSuiteCaseHasRecoverableJob({ status: 'poll_interrupted', job_id: '' }), false);
});

test('a client cancellation before submission remains cancelled, not a poll interruption', () => {
  const outcome = benchmarkSuiteTaskOutcome({ cancelled: true });
  assert.equal(outcome.status, 'cancelled');
  assert.equal(outcome.result_status, 'not_submitted');
  assert.equal(
    benchmarkSuiteStatusForReport({ ...outcome }),
    'cancelled'
  );
});

test('Markdown status for a legacy running record does not claim a server observation', () => {
  assert.equal(
    benchmarkSuiteStatusForReport({ status: 'running', job_id: 'legacy-job' }),
    'legacy_running_unverified（旧报告未保留服务端轮询回执）'
  );
});

test('active polling is not reported as a poll interruption', () => {
  assert.equal(
    benchmarkSuiteStatusForReport({ status: 'running', server_status: 'running', poll_status: 'polling', result_status: 'unknown' }),
    'polling（最近确认服务端状态：running）'
  );
  assert.equal(
    benchmarkSuiteStatusForReport({ status: 'running', server_status: 'unknown', poll_status: 'polling', result_status: 'unknown' }),
    'polling（最近确认服务端状态：服务端状态未确认）'
  );
});

test('a client cancellation preserves its operation state without claiming server completion', () => {
  const outcome = benchmarkSuiteTaskOutcome({ jobID: 'job-cancelled', cancelled: true, lastObservedServerStatus: 'running' });
  assert.equal(outcome.status, 'cancelled');
  assert.equal(outcome.server_status, 'running');
  assert.equal(outcome.poll_status, 'cancelled');
  assert.equal(outcome.result_status, 'unknown');
  assert.equal(benchmarkSuiteCaseHasRecoverableJob({ ...outcome, job_id: 'job-cancelled' }), true);
  assert.equal(benchmarkSuiteStatusForReport({ ...outcome }), 'client_cancelled_unverified（最近确认服务端状态：running；轮询：cancelled）');
});

test('Markdown status exposes last server observation and poll outcome', () => {
  assert.equal(
    benchmarkSuiteStatusForReport({
      status: 'poll_interrupted',
      server_status: 'running',
      poll_status: 'interrupted',
      result_status: 'unknown'
    }),
    'poll_interrupted（最近确认服务端状态：running；轮询：interrupted）'
  );
});
