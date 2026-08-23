package utils

import (
	"fmt"

	"github.com/cheggaaa/pb/v3"
)

// ProgressHook 测速进度钩子（可选）。
// 上游进度条每次推进时调用：phase 为阶段标识（loading/ping/download），
// detail 为当前正在处理的地址（可能为空），current/total 为进度计数。
// 由 speedtest 包在测速期间注册，其余时间为 nil。
var ProgressHook func(phase, detail string, current, total int)

// Bar 进度条封装，可选携带阶段标识用于向 ProgressHook 上报。
type Bar struct {
	pb    *pb.ProgressBar
	phase string
	total int
}

func NewBar(count int, MyStrStart, MyStrEnd string) *Bar {
	tmpl := fmt.Sprintf(`{{counters . }} {{ bar . "[" "-" (cycle . "↖" "↗" "↘" "↙" ) "_" "]"}} %s {{string . "MyStr" | green}} %s `, MyStrStart, MyStrEnd)
	bar := pb.ProgressBarTemplate(tmpl).Start(count)
	return &Bar{pb: bar}
}

// NewBarWithPhase 创建带阶段标识的进度条（用于向 ProgressHook 上报）。
func NewBarWithPhase(count int, MyStrStart, MyStrEnd, phase string) *Bar {
	b := NewBar(count, MyStrStart, MyStrEnd)
	b.phase = phase
	b.total = count
	return b
}

func (b *Bar) Grow(num int, MyStrVal string) {
	b.pb.Set("MyStr", MyStrVal).Add(num)
	// 进度钩子：把当前计数与正在处理的地址推给订阅方
	if ProgressHook != nil && b.phase != "" {
		ProgressHook(b.phase, MyStrVal, int(b.pb.Current()), b.total)
	}
}

func (b *Bar) Done() {
	b.pb.Finish()
	if ProgressHook != nil && b.phase != "" {
		ProgressHook(b.phase, "", b.total, b.total)
	}
}
