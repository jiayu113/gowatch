package storage

import (
	"testing"
	"time"

	"github.com/jiayu113/gowatch/internal/checker"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(":memory:")
	if err != nil {
		t.Fatalf("创建测试 store 失败: %v", err)
	}
	return store
}

func TestStore_SaveAndGetRecent(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	targets := []string{"a.com", "b.com", "c.com"}
	for i, target := range targets {
		err := store.Save(checker.Result{
			Target:  target,
			Status:  checker.StatusUp,
			Latency: time.Duration(100+i*10) * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("Save 失败: %v", err)
		}
	}

	results, err := store.GetRecent(10)
	if err != nil {
		t.Fatalf("GetRecent 失败: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("期望 3 条记录,得到 %d 条", len(results))
	}
}

func TestStore_GetByTarget(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	for i := 0; i < 3; i++ {
		if err := store.Save(checker.Result{
			Target: "baidu.com",
			Status: checker.StatusUp,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := store.Save(checker.Result{
			Target: "github.com",
			Status: checker.StatusDown,
		}); err != nil {
			t.Fatal(err)
		}
	}

	results, err := store.GetByTarget("baidu.com", 10)
	if err != nil {
		t.Fatalf("GetByTarget 失败: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("期望 baidu.com 3 条,得到 %d 条", len(results))
	}
	for _, r := range results {
		if r.Target != "baidu.com" {
			t.Errorf("不应该出现非 baidu.com 的记录: %v", r)
		}
	}
}

func TestStore_GetByTarget_NotFound(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	results, err := store.GetByTarget("nonexistent.com", 10)
	if err != nil {
		t.Errorf("查不存在的 target 不该报错: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("期望返回空 slice,得到 %d 条", len(results))
	}
}

func TestStore_SaveBatch(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	batch := []checker.Result{
		{Target: "x1", Status: checker.StatusUp, Latency: 10 * time.Millisecond},
		{Target: "x2", Status: checker.StatusUp, Latency: 20 * time.Millisecond},
		{Target: "x3", Status: checker.StatusDown, Error: "timeout"},
	}
	if err := store.SaveBatch(batch); err != nil {
		t.Fatalf("SaveBatch 失败: %v", err)
	}

	results, err := store.GetRecent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("期望 3 条, 得到 %d 条", len(results))
	}
}

func TestStore_SaveBatch_Empty(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	if err := store.SaveBatch(nil); err != nil {
		t.Errorf("空 batch 不该报错: %v", err)
	}
	if err := store.SaveBatch([]checker.Result{}); err != nil {
		t.Errorf("空 batch 不该报错: %v", err)
	}
}

func TestStore_GetLatestPerTarget(t *testing.T) {
	store := newTestStore(t)
	defer store.Close()

	// baidu.com 存 3 次,github.com 存 2 次,example.com 存 1 次
	for i := 0; i < 3; i++ {
		if err := store.Save(checker.Result{
			Target:  "baidu.com",
			Status:  checker.StatusUp,
			Latency: time.Duration(i+1) * 10 * time.Millisecond,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := store.Save(checker.Result{
			Target: "github.com",
			Status: checker.StatusDown,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(checker.Result{
		Target: "example.com",
		Status: checker.StatusUp,
	}); err != nil {
		t.Fatal(err)
	}

	results, err := store.GetLatestPerTarget()
	if err != nil {
		t.Fatalf("GetLatestPerTarget 失败: %v", err)
	}

	// 3 个 target 各一条
	if len(results) != 3 {
		t.Errorf("期望 3 条(每个 target 最新一条),得到 %d 条", len(results))
	}

	// 每个 target 只能出现一次
	seen := make(map[string]bool)
	for _, r := range results {
		if seen[r.Target] {
			t.Errorf("target %s 出现多次", r.Target)
		}
		seen[r.Target] = true
	}
}
