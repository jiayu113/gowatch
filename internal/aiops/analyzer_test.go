package aiops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiayu113/gowatch/internal/alert"
	"github.com/jiayu113/gowatch/pkg/checker"
)

// mockLLM 用于模拟大模型的返回状态
type mockLLM struct {
	fail  bool
	calls int
	mu    sync.Mutex
}

func (m *mockLLM) Complete(_ context.Context, _, _ string) (string, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()

	if m.fail {
		return "", fmt.Errorf("mock llm down")
	}
	return "## 可能根因\n- mock", nil
}

// dummyHistory 模拟历史查询，直接返回空
func dummyHistory(target string, limit int) ([]checker.Result, error) {
	return nil, nil
}

func TestAnalyzer(t *testing.T) {
	// 成功路：测试正常流转与文件写入
	t.Run("成功路", func(t *testing.T) {
		tmpDir := t.TempDir()

		cfg := &Config{}
		cfg.Output.Dir = tmpDir
		cfg.Limits.DailyMax = 10
		cfg.Limits.Cooldown = 1 * time.Hour
		cfg.LLM.Timeout = 5 * time.Second

		llm := &mockLLM{}
		analyzer := New(cfg, llm, dummyHistory, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go analyzer.Run(ctx)

		ev := alert.Event{RuleName: "CPUHigh", Target: "ServerA"}
		analyzer.Submit(ev)

		time.Sleep(100 * time.Millisecond)

		if llm.calls != 1 {
			t.Errorf("成功路 calls 期望 1, 实际 %d", llm.calls)
		}

		today := time.Now().Format("20060102")
		expectedFile := filepath.Join(tmpDir, fmt.Sprintf("diagnosis-%s.jsonl", today))
		data, err := os.ReadFile(expectedFile)
		if err != nil {
			t.Fatalf("文件没生成: %v", err)
		}
		if !strings.Contains(string(data), "CPUHigh") {
			t.Errorf("文件里没找到关键的 RuleName。内容: %s", string(data))
		}
	})

	// 冷却路：测试短时间内同 key 重复告警是否被拦截
	t.Run("冷却路", func(t *testing.T) {
		cfg := &Config{}
		cfg.Limits.DailyMax = 10
		cfg.Limits.Cooldown = 1 * time.Hour

		llm := &mockLLM{}
		analyzer := New(cfg, llm, dummyHistory, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go analyzer.Run(ctx)

		ev1 := alert.Event{RuleName: "OOM", Target: "ServerB"}
		ev2 := alert.Event{RuleName: "OOM", Target: "ServerB"}

		analyzer.Submit(ev1)
		analyzer.Submit(ev2)
		time.Sleep(100 * time.Millisecond)

		if llm.calls != 1 {
			t.Errorf("冷却路 calls 期望 1, 实际 %d", llm.calls)
		}
	})

	// 熔断路：测试多次失败后是否触发断路器
	t.Run("熔断路", func(t *testing.T) {
		cfg := &Config{}
		cfg.Limits.DailyMax = 10
		cfg.Limits.Cooldown = 1 * time.Hour
		cfg.Limits.BreakerFails = 3
		cfg.Limits.BreakerOpen = 1 * time.Hour

		llm := &mockLLM{fail: true}
		analyzer := New(cfg, llm, dummyHistory, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go analyzer.Run(ctx)

		// 提交 3 条不同的告警以触发熔断
		analyzer.Submit(alert.Event{RuleName: "R1", Target: "T1"})
		analyzer.Submit(alert.Event{RuleName: "R2", Target: "T2"})
		analyzer.Submit(alert.Event{RuleName: "R3", Target: "T3"})
		time.Sleep(200 * time.Millisecond)

		// 熔断开启后，第 4 条应被拦截
		analyzer.Submit(alert.Event{RuleName: "R4", Target: "T4"})
		time.Sleep(100 * time.Millisecond)

		if llm.calls != 3 {
			t.Errorf("熔断路 calls 期望 3, 实际 %d", llm.calls)
		}
	})
}
