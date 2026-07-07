package aiops

import (
	"strings"
	"testing"
	"time"

	"github.com/jiayu113/gowatch/internal/alert"
	"github.com/jiayu113/gowatch/pkg/checker"
)

func TestBuildContext(t *testing.T) {
	// 准备一个基准时间，方便模拟历史记录
	now := time.Now()

	t.Run(" 混合历史(dnsx3 + timeoutx2 + up)", func(t *testing.T) {
		// 模拟数据：先 up，然后 timeout 两次，最后 dns 报错三次
		hist := []checker.Result{
			{Status: "up", Latency: 10 * time.Millisecond, Timestamp: now.Add(-6 * time.Minute)},
			{Status: "down", ErrorType: "timeout", Timestamp: now.Add(-5 * time.Minute)},
			{Status: "down", ErrorType: "timeout", Timestamp: now.Add(-4 * time.Minute)},
			{Status: "up", Latency: 20 * time.Millisecond, Timestamp: now.Add(-3 * time.Minute)}, // 恢复了一次，制造翻转
			{Status: "down", ErrorType: "dns", Timestamp: now.Add(-2 * time.Minute)},
			{Status: "down", ErrorType: "dns", Timestamp: now.Add(-1 * time.Minute)},
			{Status: "down", ErrorType: "dns", Timestamp: now},
		}

		dc := BuildContext(alert.Event{}, hist)

		// 断言分桶
		if dc.ErrBuckets["dns"] != 3 || dc.ErrBuckets["timeout"] != 2 {
			t.Errorf("分桶统计错误: 期望 dns=3, timeout=2, 实际得到 %v", dc.ErrBuckets)
		}

		// 断言翻转次数 (up -> down -> up -> down = 3次翻转)
		if dc.FlapCount != 3 {
			t.Errorf("翻转次数错误: 期望 3, 实际得到 %d", dc.FlapCount)
		}

		// 断言故障起点 (最近一段连续 down 是从 -2 分钟开始的 dns 报错)
		expectedFirstDown := now.Add(-2 * time.Minute)
		if !dc.FirstDownAt.Equal(expectedFirstDown) {
			t.Errorf("故障起点错误: 期望 %v, 实际得到 %v", expectedFirstDown, dc.FirstDownAt)
		}
	})

	t.Run(" 全 up 历史", func(t *testing.T) {
		hist := []checker.Result{
			{Status: "up", Latency: 10 * time.Millisecond, Timestamp: now.Add(-2 * time.Minute)},
			{Status: "up", Latency: 12 * time.Millisecond, Timestamp: now.Add(-1 * time.Minute)},
			{Status: "up", Latency: 11 * time.Millisecond, Timestamp: now},
		}

		dc := BuildContext(alert.Event{}, hist)

		// 断言 FirstDownAt 零值
		if !dc.FirstDownAt.IsZero() {
			t.Errorf("全 up 时故障起点应为空(零值), 实际得到 %v", dc.FirstDownAt)
		}

		// 断言桶空
		if len(dc.ErrBuckets) > 0 {
			t.Errorf("全 up 时分桶应为空, 实际得到 %v", dc.ErrBuckets)
		}
	})

	t.Run(" RenderPrompt 包含关键字段", func(t *testing.T) {
		ev := alert.Event{
			RuleName: "HighErrorRate",
			Target:   "user-service",
			FireAt:   now,
			Reason:   "测试告警触发",
		}
		dc := DiagContext{
			Ev:         ev,
			ErrBuckets: map[string]int{"timeout": 5},
		}

		prompt := dc.RenderPrompt()

		// 验证规则名是否被渲染进去
		if !strings.Contains(prompt, "HighErrorRate") {
			t.Errorf("RenderPrompt 漏掉了关键字段: 规则名。生成的文本: %s", prompt)
		}

		// 验证分桶字符串是否在里面 (打印 map 会有 map[timeout:5] 字样)
		if !strings.Contains(prompt, "timeout:5") {
			t.Errorf("RenderPrompt 漏掉了关键字段: 分桶字符串。生成的文本: %s", prompt)
		}
	})
}
