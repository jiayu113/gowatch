package alert

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jiayu113/gowatch/pkg/checker"
)

// webhook 5xx 持续，verify evaluator 不阻塞主 OnResult 调用
func TestEvaluator_WebhookAlwaysFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rules := []Rule{{
		Name: "t", Target: "x", Type: "consecutive_status",
		Status: "down", Threshold: 1, Cooldown: 0, Webhook: srv.URL,
	}}
	e := NewEvaluator(rules, nil)

	start := time.Now()
	for i := 0; i < 50; i++ {
		e.OnResult(checker.Result{Target: "x", Status: "down", Timestamp: time.Now()})
	}
	elapsed := time.Since(start)

	// 50 次 OnResult 应该几乎瞬完成（异步 fire，主调用不阻塞）
	if elapsed > 100*time.Millisecond {
		t.Errorf("OnResult 不应该被 webhook 阻塞，实际耗时 %v", elapsed)
	}
	// 等 fire 都完成
	time.Sleep(7 * time.Second)
}

// webhook 超时
func TestEvaluator_WebhookTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second) // 比 client.Timeout(5s) 长
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rules := []Rule{{
		Name: "t", Target: "x", Type: "consecutive_status",
		Status: "down", Threshold: 1, Cooldown: 0, Webhook: srv.URL,
	}}
	e := NewEvaluator(rules, nil)

	// 主调用瞬完成，fire 在 goroutine 里慢慢 timeout
	start := time.Now()
	e.OnResult(checker.Result{Target: "x", Status: "down", Timestamp: time.Now()})
	if time.Since(start) > 50*time.Millisecond {
		t.Errorf("主调用不该被超时阻塞")
	}
}
