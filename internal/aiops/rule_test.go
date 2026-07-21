package aiops

import (
	"testing"
	"time"

	"github.com/jiayu113/gowatch/internal/alert"
	"github.com/jiayu113/gowatch/pkg/checker"
)

// TestRule_ConsecutiveStatus 验证"连续状态"规则
// 同一 target 连续 N 次 down,达到/超过 threshold 就应命中
func TestRule_ConsecutiveStatus(t *testing.T) {
	rule := alert.Rule{
		Name:      "gh-flapping",
		Target:    "github-home",
		Type:      "consecutive_status",
		Status:    "down",
		Threshold: 3,
	}

	tests := []struct {
		name    string
		downN   int  // 连续塞多少条 down
		wantHit bool // 期望是否命中
	}{
		{"连续5次down_超过阈值3_命中", 5, true},
		{"连续3次down_刚好达阈值_命中", 3, true},
		{"连续2次down_不足阈值_不命中", 2, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := alert.NewWindow(50)
			for i := 0; i < tt.downN; i++ {
				w.Push(checker.Result{
					Target:    "github-home",
					Status:    "down",
					ErrorType: "timeout",
					Timestamp: time.Now().Add(-time.Duration(tt.downN-i) * time.Second),
				})
			}

			hit, reason := rule.Match(w.Snapshot("github-home"))
			if hit != tt.wantHit {
				t.Errorf("hit 期望 %v, 实际 %v (reason=%s)", tt.wantHit, hit, reason)
			}
		})
	}
}
