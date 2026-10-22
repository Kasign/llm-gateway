// server（system_host_cache）：宿主机内存与模型盘容量短期缓存，供 bootstrap 首页轮询降频。
package server

import (
	"syscall"
	"time"
)

const systemHostStatsCacheTTL = 5 * time.Second

type systemHostStats struct {
	at       time.Time
	mem      containerMemory
	disk     diskUsage
}

type diskUsage struct {
	Total     uint64
	Available uint64 // free to unprivileged (Bavail), matches df "Avail"
	Used      uint64 // (Blocks-Bfree)*Bsize, matches df/1Panel "Used"
}

func (s *Server) systemHostStatsCached() (containerMemory, diskUsage) {
	s.systemHostMu.Lock()
	if s.systemHostCache != nil && time.Since(s.systemHostCache.at) < systemHostStatsCacheTTL {
		out := *s.systemHostCache
		s.systemHostMu.Unlock()
		return out.mem, out.disk
	}
	s.systemHostMu.Unlock()

	mem := containerMemoryStats()
	var disk diskUsage
	var fs syscall.Statfs_t
	if e := syscall.Statfs(s.cfg.Storage.ModelsDir, &fs); e == nil {
		bsize := uint64(fs.Bsize)
		disk.Total = fs.Blocks * bsize
		disk.Available = fs.Bavail * bsize
		if fs.Blocks >= fs.Bfree {
			disk.Used = (fs.Blocks - fs.Bfree) * bsize
		}
	}

	s.systemHostMu.Lock()
	s.systemHostCache = &systemHostStats{at: time.Now(), mem: mem, disk: disk}
	s.systemHostMu.Unlock()
	return mem, disk
}
