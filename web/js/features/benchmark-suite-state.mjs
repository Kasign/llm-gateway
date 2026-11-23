const TERMINAL_JOB_STATUSES = new Set([
  'completed',
  'failed',
  'cancelled',
  'interrupted',
  'timeout',
  'timed_out'
]);

function normalizedStatus(value) {
  return String(value || '').trim().toLowerCase();
}

export function benchmarkSuiteIsTerminalStatus(status) {
  return TERMINAL_JOB_STATUSES.has(normalizedStatus(status));
}

// status 表示可执行/恢复状态；server_status 只记录服务端 GET 真正返回的状态。
export function benchmarkSuiteTaskOutcome({
  jobStatus = '',
  jobID = '',
  cancelled = false,
  lastObservedServerStatus = ''
} = {}) {
  const returnedStatus = normalizedStatus(jobStatus);
  const serverStatus = returnedStatus || normalizedStatus(lastObservedServerStatus) || 'unknown';

  if (benchmarkSuiteIsTerminalStatus(returnedStatus)) {
    return {
      status: returnedStatus,
      server_status: serverStatus,
      poll_status: 'completed',
      result_status: returnedStatus
    };
  }

  if (String(jobID || '').trim()) {
    return {
      status: 'poll_interrupted',
      server_status: serverStatus,
      poll_status: cancelled ? 'cancelled' : 'interrupted',
      result_status: 'unknown'
    };
  }

  if (cancelled) {
    return {
      status: 'cancelled',
      server_status: serverStatus,
      poll_status: 'cancelled',
      result_status: 'not_submitted'
    };
  }

  return {
    status: 'failed',
    server_status: serverStatus,
    poll_status: 'failed',
    result_status: 'not_submitted'
  };
}

// 旧报告只有 status=running 和 job_id，仍可恢复；新报告的 poll_interrupted 也必须继续查询原 Job。
export function benchmarkSuiteCaseHasRecoverableJob(item) {
  if (!item || !String(item.job_id || '').trim()) return false;
  const status = normalizedStatus(item.status);
  const resultStatus = normalizedStatus(item.result_status);
  if (benchmarkSuiteIsTerminalStatus(resultStatus)) return false;
  return status === 'running' || status === 'poll_interrupted' || status === 'unknown';
}

export function benchmarkSuiteStatusForReport(item) {
  if (!item) return 'unknown';
  const pollStatus = normalizedStatus(item.poll_status);
  const serverStatus = normalizedStatus(item.server_status) || 'unknown';
  const resultStatus = normalizedStatus(item.result_status);

  if (resultStatus === 'unknown' || pollStatus === 'interrupted' || (pollStatus === 'cancelled' && resultStatus !== 'not_submitted')) {
    return 'poll_interrupted（最近确认服务端状态：' + serverStatus + '；轮询：' + (pollStatus || 'unknown') + '）';
  }
  if (resultStatus && resultStatus !== 'not_submitted') return resultStatus;
  if (!pollStatus && normalizedStatus(item.status) === 'running') {
    return 'legacy_running_unverified（旧报告未保留服务端轮询回执）';
  }
  return normalizedStatus(item.status) || 'unknown';
}
