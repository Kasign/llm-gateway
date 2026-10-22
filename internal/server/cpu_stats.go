// server（cpu_stats）：读取 /proc/stat 估算主机 CPU 占用，供 bootstrap host 指标展示。
package server

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// cpuUsagePercent 用两次 /proc/stat 采样差值算整机 CPU  busy 比例，并做短期缓存。
// cpuUsagePercent returns host-wide CPU busy % from /proc/stat deltas (same view as top/1Panel host).
func (s *Server) cpuUsagePercent() float64 {
	total, idle, ok := readProcStatJiffies()
	if !ok {
		return s.cpuCachedPercent
	}
	now := time.Now()

	s.cpuSampleMu.Lock()
	defer s.cpuSampleMu.Unlock()

	if s.cpuProcLastSample.IsZero() {
		s.cpuLastTotalJiffies = total
		s.cpuLastIdleJiffies = idle
		s.cpuProcLastSample = now
		time.Sleep(200 * time.Millisecond)
		t2, i2, ok2 := readProcStatJiffies()
		if ok2 && t2 > total {
			td := t2 - total
			if td > 0 {
				s.cpuCachedPercent = clampCPUPercent(100 * float64(td-(i2-idle)) / float64(td))
			}
			s.cpuLastTotalJiffies = t2
			s.cpuLastIdleJiffies = i2
			s.cpuProcLastSample = time.Now()
		}
		return s.cpuCachedPercent
	}

	elapsed := now.Sub(s.cpuProcLastSample).Seconds()
	if elapsed < 0.05 {
		return s.cpuCachedPercent
	}

	totalDelta := total - s.cpuLastTotalJiffies
	idleDelta := idle - s.cpuLastIdleJiffies
	s.cpuLastTotalJiffies = total
	s.cpuLastIdleJiffies = idle
	s.cpuProcLastSample = now

	if totalDelta == 0 {
		return s.cpuCachedPercent
	}
	s.cpuCachedPercent = clampCPUPercent(100 * float64(totalDelta-idleDelta) / float64(totalDelta))
	return s.cpuCachedPercent
}

func clampCPUPercent(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func readProcStatJiffies() (total, idle uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			return 0, 0, false
		}
		for i := 1; i < len(f); i++ {
			v, _ := strconv.ParseUint(f[i], 10, 64)
			total += v
			if i == 4 {
				idle = v
			}
		}
		return total, idle, true
	}
	return 0, 0, false
}
