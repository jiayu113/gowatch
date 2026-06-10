package alert

import (
	"testing"
	"time"

	"github.com/jiayu113/gowatch/pkg/checker"
)

// ============ 基本读写 ============

func TestWindow_PushAndSnapshot(t *testing.T) {
	w := NewWindow(10)
	w.Push(checker.Result{Target: "api-1", Status: "down", Timestamp: time.Now()})

	snap := w.Snapshot("api-1")
	if len(snap) != 1 {
		t.Fatalf("len=%d want=1", len(snap))
	}
	if snap[0].Status != "down" {
		t.Errorf("status=%s want=down", snap[0].Status)
	}
}

// ============ 多 target 互不干扰 ============
// 验证 buckets map 按 Target 分桶是正确的

func TestWindow_TargetIsolation(t *testing.T) {
	w := NewWindow(10)
	w.Push(checker.Result{Target: "api-1", Status: "down"})
	w.Push(checker.Result{Target: "api-2", Status: "up"})
	w.Push(checker.Result{Target: "api-1", Status: "down"})

	if got := len(w.Snapshot("api-1")); got != 2 {
		t.Errorf("api-1 len=%d want=2", got)
	}
	if got := len(w.Snapshot("api-2")); got != 1 {
		t.Errorf("api-2 len=%d want=1", got)
	}
}

// ============ cap 自动裁剪 ============
// cap=3 时推入 5 条，应只保留最后 3 条（最旧的 a, b 被踢掉）
// 同时验证顺序：保留下来的应该是按 push 顺序的最后 3 条

func TestWindow_CapacityLimit(t *testing.T) {
	w := NewWindow(3)
	tags := []string{"a", "b", "c", "d", "e"}
	for _, tag := range tags {
		w.Push(checker.Result{Target: "api-1", Status: "down", ErrorType: tag})
	}

	snap := w.Snapshot("api-1")
	if len(snap) != 3 {
		t.Fatalf("len=%d want=3 (cap=3)", len(snap))
	}
	expected := []string{"c", "d", "e"}
	for i, exp := range expected {
		if snap[i].ErrorType != exp {
			t.Errorf("snap[%d].ErrorType=%s want=%s", i, snap[i].ErrorType, exp)
		}
	}
}

// ============ 未 push 过的 target ============
// 不应 panic，应返回长度为 0 的 slice

func TestWindow_SnapshotUnknownTarget(t *testing.T) {
	w := NewWindow(10)
	snap := w.Snapshot("never-pushed")
	if len(snap) != 0 {
		t.Errorf("len=%d want=0", len(snap))
	}
}

// ============ Snapshot 是独立副本（关键契约测试）============
// 这个测试是 window.go 里 `copy(out, b)` 那一行的全部价值证明。
// 如果哪天优化代码改成 `return w.buckets[target]` 直接返回，这个测试会失败。
// 测试方法：调用方修改 snap，不应影响 Window 内部，后续 Snapshot 拿到的应该是原值。

func TestWindow_SnapshotIsCopy(t *testing.T) {
	w := NewWindow(10)
	w.Push(checker.Result{Target: "api-1", Status: "down", ErrorType: "original"})

	snap1 := w.Snapshot("api-1")
	if len(snap1) != 1 {
		t.Fatalf("snap1 len=%d want=1", len(snap1))
	}

	// 调用方故意修改 snap1
	snap1[0].Status = "MODIFIED"
	snap1[0].ErrorType = "MODIFIED"

	// 再 Snapshot 一次，应该拿到原值，证明 Window 内部没被污染
	snap2 := w.Snapshot("api-1")
	if snap2[0].Status == "MODIFIED" || snap2[0].ErrorType == "MODIFIED" {
		t.Errorf("Snapshot 必须返回独立副本，调用方修改不应影响内部状态")
	}
}
