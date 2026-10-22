// applog 提供可开关的运行日志：与标准 log 分离，便于在生产关闭冗长输出。
package applog

import "log"

var (
	enabled = true
	verbose = false
)

// Init 在 main 启动早期调用，决定是否打印网关内部 Info/Debug 日志。
func Init(enabledFlag, verboseFlag bool) {
	enabled = enabledFlag
	verbose = verboseFlag
	if enabled {
		log.Printf("applog: enabled verbose=%v", verbose)
	} else {
		log.Printf("applog: disabled (startup messages still use std log)")
	}
}

func Enabled() bool { return enabled }

func Printf(format string, args ...interface{}) {
	if enabled {
		log.Printf(format, args...)
	}
}

func Infof(format string, args ...interface{}) {
	Printf(format, args...)
}

func Debugf(format string, args ...interface{}) {
	if enabled && verbose {
		log.Printf("[debug] "+format, args...)
	}
}
