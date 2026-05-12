package alert

import (
	"testing"
	"time"
)

func TestSuppressor_Allow(t *testing.T) {
	s := NewSuppressor()
	cooldown := 100 * time.Millisecond

	// 第一次必放行
	if !s.Allow("rule1", "github", cooldown) {
		t.Errorf("first call should be allowed")
	}
	// cooldown 内被拦
	if s.Allow("rule1", "github", cooldown) {
		t.Errorf("within cooldown should be blocked")
	}
	// 不同 target 独立
	if !s.Allow("rule1", "baidu", cooldown) {
		t.Errorf("different target should have independent cooldown")
	}
	// 等过 cooldown 后放行
	time.Sleep(cooldown + 20*time.Millisecond)
	if !s.Allow("rule1", "github", cooldown) {
		t.Errorf("after cooldown should be allowed")
	}
}

func TestSuppressor_ZeroCooldown(t *testing.T) {
	s := NewSuppressor()
	for i := 0; i < 5; i++ {
		if !s.Allow("rule", "x", 0) {
			t.Errorf("zero cooldown should always allow")
		}
	}
}
