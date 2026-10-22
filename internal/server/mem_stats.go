// server（mem_stats）：从 /proc/meminfo 读主机内存用量，与管理面板宿主机视图对齐。
package server

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// containerMemory 表示一次内存采样结果（名称沿用容器场景，实际来自 meminfo）。
type containerMemory struct {
	Used     uint64
	Limit    uint64
	Source   string
	UsagePct float64
}

// containerMemoryStats uses host-visible /proc/meminfo (MemTotal − MemAvailable), aligned with 1Panel host memory.
func containerMemoryStats() containerMemory {
	total, avail, ok := readMeminfoBytes()
	if !ok || total == 0 {
		return containerMemory{}
	}
	used := total - avail
	return containerMemory{
		Used: used, Limit: total, Source: "meminfo",
		UsagePct: clampCPUPercent(100 * float64(used) / float64(total)),
	}
}

func readMeminfoBytes() (total, available uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fld := strings.Fields(sc.Text())
		if len(fld) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(fld[1], 10, 64)
		switch fld[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			available = kb * 1024
		}
	}
	return total, available, total > 0
}
