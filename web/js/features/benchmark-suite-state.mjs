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

export function benchmarkSuiteIsActiveStatus(status) {
  return normalizedStatus(status) === 'running' || normalizedStatus(status) === 'cancelling';
}

// status 表示可执行/恢复状态；server_status 只记录服务端 GET 真正返回的状态。
export function benchmarkSuiteTaskOutcome({
  jobStatus = '',
  jobID = '',
  cancelled = false,
  lastObservedServerStatus = ''
} = {}) {
  const returnedStatus = normalizedStatus(jobStatus);
  const observedStatus = normalizedStatus(lastObservedServerStatus);
  const serverStatus = returnedStatus || observedStatus || 'unknown';
  const terminalStatus = benchmarkSuiteIsTerminalStatus(returnedStatus)
    ? returnedStatus
    : (!returnedStatus && benchmarkSuiteIsTerminalStatus(observedStatus) ? observedStatus : '');

  if (terminalStatus) {
    return {
      status: terminalStatus,
      server_status: serverStatus,
      poll_status: 'completed',
      result_status: terminalStatus
    };
  }

  if (String(jobID || '').trim()) {
    return {
      status: cancelled ? 'cancelled' : 'running',
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

// 旧报告的 running + job_id 和所有服务端结果未决的任务都保留原 Job，恢复时只查询、不重复提交。
export function benchmarkSuiteCaseHasRecoverableJob(item) {
  if (!item || !String(item.job_id || '').trim()) return false;
  const status = normalizedStatus(item.status);
  const resultStatus = normalizedStatus(item.result_status);
  if (benchmarkSuiteIsTerminalStatus(resultStatus)) return false;
  return status === 'running' || status === 'poll_interrupted' || status === 'unknown' || (status === 'cancelled' && resultStatus === 'unknown');
}

export function benchmarkSuiteStatusForReport(item) {
  if (!item) return 'unknown';
  const pollStatus = normalizedStatus(item.poll_status);
  const serverStatus = normalizedStatus(item.server_status) || 'unknown';
  const resultStatus = normalizedStatus(item.result_status);

  if (pollStatus === 'polling') {
    return 'polling（最近确认服务端状态：' + (serverStatus === 'unknown' ? '服务端状态未确认' : serverStatus) + '）';
  }
  if (resultStatus === 'unknown' || pollStatus === 'interrupted' || (pollStatus === 'cancelled' && resultStatus !== 'not_submitted')) {
    const displayStatus = pollStatus === 'cancelled' ? 'client_cancelled_unverified' : 'poll_interrupted';
    return displayStatus + '（最近确认服务端状态：' + serverStatus + '；轮询：' + (pollStatus || 'unknown') + '）';
  }
  if (resultStatus && resultStatus !== 'not_submitted') return resultStatus;
  if (!pollStatus && normalizedStatus(item.status) === 'running') {
    return 'legacy_running_unverified（旧报告未保留服务端轮询回执）';
  }
  return normalizedStatus(item.status) || 'unknown';
}
