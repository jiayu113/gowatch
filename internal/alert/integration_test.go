package alert

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jiayu113/gowatch/pkg/checker"
)

func TestEvaluator_FullPipeline(t *testing.T) {
	// 假 webhook 接收端
	received := make(chan Event, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		json.NewDecoder(r.Body).Decode(&ev)
		received <- ev
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 一条规则
	rules := []Rule{
		{
			Name: "test", Target: "x", Type: "consecutive_status",
			Status: "down", Threshold: 3, Cooldown: 100 * time.Millisecond,
			Webhook: srv.URL,
		},
	}
	e := NewEvaluator(rules, nil)

	// 喂 3 条 down，应该触发
	for i := 0; i < 3; i++ {
		e.OnResult(checker.Result{Target: "x", Status: "down", Timestamp: time.Now()})
	}
	select {
	case ev := <-received:
		if ev.RuleName != "test" {
			t.Errorf("wrong rule: %s", ev.RuleName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected event but timed out")
	}

	// 立刻再喂一条 down，cooldown 内应该不触发
	e.OnResult(checker.Result{Target: "x", Status: "down", Timestamp: time.Now()})
	select {
	case <-received:
		t.Fatal("should be suppressed")
	case <-time.After(300 * time.Millisecond):
		// 期望走到这里
	}
}
