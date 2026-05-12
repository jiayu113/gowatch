package alert

import (
	"sync"
	"time"
)

// Suppressor 在 cooldown 内拦截重复触发。in-memory，跨重启清零（本周期不解决）。
type Suppressor struct {
	mu        sync.Mutex
	lastFired map[string]time.Time // key = ruleName + ":" + target
}

func NewSuppressor() *Suppressor {
	return &Suppressor{lastFired: make(map[string]time.Time)}
}

// Allow 返回是否允许触发；如果允许，记录本次触发时间
func (s *Suppressor) Allow(ruleName, target string, cooldown time.Duration) bool {
	if cooldown <= 0 {
		return true
	}
	key := ruleName + ":" + target
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.lastFired[key]
	if !ok || time.Since(last) >= cooldown {
		s.lastFired[key] = time.Now()
		return true
	}
	return false
}
