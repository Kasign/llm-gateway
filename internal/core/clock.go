package core

import "time"

// timeNow 单独放出来，测试可以替换它。生产路径使用真实时间。
var timeNow = time.Now
