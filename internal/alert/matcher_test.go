package alert

import (
	"testing"
	"time"

	"github.com/jiayu113/gowatch/pkg/checker"
)

// ============ matchConsecutiveStatus 测试 ============

func TestMatchConsecutiveStatus(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		recent  []checker.Result
		wantHit bool
	}{
		{
			name:    "连续3次down命中",
			rule:    Rule{Type: "consecutive_status", Status: "down", Threshold: 3},
			recent:  results("up", "down", "down", "down"),
			wantHit: true,
		},
		{
			name:    "中间夹一次up不命中",
			rule:    Rule{Type: "consecutive_status", Status: "down", Threshold: 3},
			recent:  results("down", "up", "down", "down"),
			wantHit: false,
		},
		{
			name:    "样本不足不命中",
			rule:    Rule{Type: "consecutive_status", Status: "down", Threshold: 3},
			recent:  results("down", "down"),
			wantHit: false,
		},
		{
			name:    "Threshold=0不命中",
			rule:    Rule{Type: "consecutive_status", Status: "down", Threshold: 0},
			recent:  results("down", "down"),
			wantHit: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hit, _ := tt.rule.Match(tt.recent)
			if hit != tt.wantHit {
				t.Errorf("got=%v want=%v", hit, tt.wantHit)
			}
		})
	}
}

// ============ matchConsecutiveErrorType 测试 ============
// 改成 case-level 的 rule，跟上面那个测试函数风格对齐，顺便补两个边界 case

func TestMatchConsecutiveErrorType(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		recent  []checker.Result
		wantHit bool
	}{
		{
			name:    "连续3次timeout命中",
			rule:    Rule{Type: "consecutive_error_type", ErrorType: "timeout", Threshold: 3},
			recent:  resultsWithErrType("timeout", "timeout", "timeout"),
			wantHit: true,
		},
		{
			name:    "中间一次dns不命中",
			rule:    Rule{Type: "consecutive_error_type", ErrorType: "timeout", Threshold: 3},
			recent:  resultsWithErrType("timeout", "dns", "timeout"),
			wantHit: false,
		},
		{
			name:    "成功结果混入不命中",
			rule:    Rule{Type: "consecutive_error_type", ErrorType: "timeout", Threshold: 3},
			recent:  resultsWithErrType("timeout", "", "timeout"),
			wantHit: false,
		},
		{
			name:    "样本不足不命中",
			rule:    Rule{Type: "consecutive_error_type", ErrorType: "timeout", Threshold: 3},
			recent:  resultsWithErrType("timeout", "timeout"),
			wantHit: false,
		},
		{
			name:    "Threshold=0不命中",
			rule:    Rule{Type: "consecutive_error_type", ErrorType: "timeout", Threshold: 0},
			recent:  resultsWithErrType("timeout", "timeout", "timeout"),
			wantHit: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hit, _ := tt.rule.Match(tt.recent)
			if hit != tt.wantHit {
				t.Errorf("got=%v want=%v", hit, tt.wantHit)
			}
		})
	}
}

// ============ matchErrorRateWindow 测试 ============
// 关键点：这个函数引入了"时间维度"，要造带 Timestamp 的结果，用 resultAt 工具

func TestMatchErrorRateWindow(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		recent  []checker.Result
		wantHit bool
	}{
		{
			name: "窗口内100%错误命中",
			rule: Rule{Type: "error_rate_window", Threshold: 50, Window: 5 * time.Minute},
			recent: []checker.Result{
				resultAt(-2*time.Minute, "down"),
				resultAt(-1*time.Minute, "down"),
				resultAt(-30*time.Second, "down"),
			},
			wantHit: true,
		},
		{
			name: "错误率低于阈值不命中",
			rule: Rule{Type: "error_rate_window", Threshold: 50, Window: 5 * time.Minute},
			recent: []checker.Result{
				resultAt(-2*time.Minute, "down"),
				resultAt(-1*time.Minute, "up"),
				resultAt(-50*time.Second, "up"),
				resultAt(-30*time.Second, "up"),
				resultAt(-10*time.Second, "up"),
			},
			wantHit: false, // errs=1, total=5, rate=20% < 50%
		},
		{
			// 关键边界：测的是 rate >= r.Threshold 这个 ">=" 的等号
			// 如果哪天有人误改成 ">", 这个 case 会失败暴露问题
			name: "错误率刚好等于阈值命中",
			rule: Rule{Type: "error_rate_window", Threshold: 50, Window: 5 * time.Minute},
			recent: []checker.Result{
				resultAt(-2*time.Minute, "down"),
				resultAt(-1*time.Minute, "down"),
				resultAt(-30*time.Second, "up"),
				resultAt(-10*time.Second, "up"),
			},
			wantHit: true, // errs=2, total=4, rate=50% == 50%
		},
		{
			// 测的是 cutoff 过滤分支：旧数据全被踢出窗口，total==0 → 不命中
			name: "旧数据被窗口过滤掉不命中",
			rule: Rule{Type: "error_rate_window", Threshold: 50, Window: 5 * time.Minute},
			recent: []checker.Result{
				resultAt(-20*time.Minute, "down"),
				resultAt(-15*time.Minute, "down"),
				resultAt(-10*time.Minute, "down"),
			},
			wantHit: false,
		},
		{
			// 综合：老的全错被过滤，新的全对 → 实际窗口内错误率 0%
			name: "窗口外全错+窗口内全对不命中",
			rule: Rule{Type: "error_rate_window", Threshold: 50, Window: 5 * time.Minute},
			recent: []checker.Result{
				resultAt(-20*time.Minute, "down"),
				resultAt(-15*time.Minute, "down"),
				resultAt(-2*time.Minute, "up"),
				resultAt(-1*time.Minute, "up"),
			},
			wantHit: false,
		},
		{
			name:    "Threshold=0不命中",
			rule:    Rule{Type: "error_rate_window", Threshold: 0, Window: 5 * time.Minute},
			recent:  []checker.Result{resultAt(-1*time.Minute, "down")},
			wantHit: false,
		},
		{
			name:    "Window=0不命中",
			rule:    Rule{Type: "error_rate_window", Threshold: 50, Window: 0},
			recent:  []checker.Result{resultAt(-1*time.Minute, "down")},
			wantHit: false,
		},
		{
			name:    "空recent不命中",
			rule:    Rule{Type: "error_rate_window", Threshold: 50, Window: 5 * time.Minute},
			recent:  []checker.Result{},
			wantHit: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hit, _ := tt.rule.Match(tt.recent)
			if hit != tt.wantHit {
				t.Errorf("got=%v want=%v", hit, tt.wantHit)
			}
		})
	}
}

// ============ Rule.Match dispatcher 测试 ============
// 前面三个测试都打到了 dispatcher 的具体 case，但 default 分支还没覆盖
// 这个测试专门测 switch 的路由能力

func TestRuleMatch_Dispatch(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		recent  []checker.Result
		wantHit bool
	}{
		{
			name:    "error_rate_window路由正确",
			rule:    Rule{Type: "error_rate_window", Threshold: 50, Window: 5 * time.Minute},
			recent:  []checker.Result{resultAt(-1*time.Minute, "down"), resultAt(-30*time.Second, "down")},
			wantHit: true,
		},
		{
			name:    "未知type走default返回false",
			rule:    Rule{Type: "unknown_xxx", Threshold: 3},
			recent:  results("down", "down", "down"),
			wantHit: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hit, _ := tt.rule.Match(tt.recent)
			if hit != tt.wantHit {
				t.Errorf("got=%v want=%v", hit, tt.wantHit)
			}
		})
	}
}

// ============ helper 函数 ============

// results 造一组带 Status 的结果，时间戳都是当前时间
func results(statuses ...string) []checker.Result {
	r := make([]checker.Result, len(statuses))
	for i, s := range statuses {
		r[i] = checker.Result{Status: s, Timestamp: time.Now()}
	}
	return r
}

// resultsWithErrType 造一组带 ErrorType 的结果。
// 约定：et=="" 表示成功（Status=up），其他都是 down
func resultsWithErrType(types ...string) []checker.Result {
	r := make([]checker.Result, len(types))
	for i, et := range types {
		r[i] = checker.Result{ErrorType: et, Status: "down"}
		if et == "" {
			r[i].Status = "up"
		}
	}
	return r
}

// resultAt 造一条带特定相对时间的结果。
// offset 通常传负值表示过去（比如 -1*time.Minute 表示 1 分钟前）
func resultAt(offset time.Duration, status string) checker.Result {
	return checker.Result{
		Status:    status,
		Timestamp: time.Now().Add(offset),
	}
}
