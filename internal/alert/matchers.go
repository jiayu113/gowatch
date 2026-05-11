package alert

import (
	"fmt"
	"time"

	"github.com/jiayu113/gowatch/internal/checker"
)

// 连续 N 次 status 匹配（典型用例：连续 3 次 down 触发告警）
func matchConsecutiveStatus(r *Rule, recent []checker.Result) (bool, string) {
	if r.Threshold <= 0 || len(recent) < r.Threshold {
		return false, ""
	}
	tail := recent[len(recent)-r.Threshold:]
	for _, res := range tail {
		if res.Status != r.Status {
			return false, ""
		}
	}
	return true, fmt.Sprintf("连续%d次status=%s", r.Threshold, r.Status)
}

// 连续 N 次相同 error_type（典型用例：连续 5 次 timeout 说明网络真挂了）
func matchConsecutiveErrorType(r *Rule, recent []checker.Result) (bool, string) {
	if r.Threshold <= 0 || len(recent) < r.Threshold {
		return false, ""
	}
	tail := recent[len(recent)-r.Threshold:]
	for _, res := range tail {
		if res.ErrorType != r.ErrorType {
			return false, ""
		}
	}
	return true, fmt.Sprintf("连续%d次error_type=%s", r.Threshold, r.ErrorType)
}

// 时间窗口内错误率超过阈值（典型用例：5 分钟内错误率 > 50%）
// Threshold 此处是百分比（0-100），Window 是窗口时长
func matchErrorRateWindow(r *Rule, recent []checker.Result) (bool, string) {
	if r.Threshold <= 0 || r.Window <= 0 || len(recent) == 0 {
		return false, ""
	}
	cutoff := time.Now().Add(-r.Window)
	var total, errs int
	for _, res := range recent {
		if res.Timestamp.Before(cutoff) {
			continue
		}
		total++
		if res.Status != "up" {
			errs++
		}
	}
	if total == 0 {
		return false, ""
	}
	rate := errs * 100 / total
	if rate >= r.Threshold {
		return true, fmt.Sprintf("最近%s错误率%d%%(errs=%d/total=%d)超过阈值%d%%", r.Window, rate, errs, total, r.Threshold)
	}
	return false, ""
}
