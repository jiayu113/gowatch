package alert

import (
	"sync"

	"github.com/jiayu113/gowatch/internal/checker"
)

type Window struct {
	mu      sync.RWMutex
	cap     int
	buckets map[string][]checker.Result
}

func NewWindow(cap int) *Window {
	return &Window{
		cap:     cap,
		buckets: make(map[string][]checker.Result),
	}
}

// 存入新记录
func (w *Window) Push(r checker.Result) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.buckets[r.Target]
	b = append(b, r)
	if len(b) > w.cap {
		b = b[len(b)-w.cap:]
	}
	w.buckets[r.Target] = b
}

// 快照
func (w *Window) Snapshot(target string) []checker.Result {
	w.mu.RLock()
	defer w.mu.RUnlock()
	b := w.buckets[target]
	out := make([]checker.Result, len(b))
	copy(out, b)
	return out
}
