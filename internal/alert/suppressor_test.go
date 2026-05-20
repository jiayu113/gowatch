package alert

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

// newEmbeddedEtcd 起内嵌 etcd, 返回 client 和 cleanup 函数
func newEmbeddedEtcd(t *testing.T) (*clientv3.Client, func()) {
	t.Helper()

	dir, err := os.MkdirTemp("", "etcd-test-")
	if err != nil {
		t.Fatal(err)
	}
	cfg := embed.NewConfig()
	cfg.Dir = dir
	cfg.LogLevel = "error"

	// 随机端口避免冲突
	lpurl, _ := url.Parse("http://127.0.0.1:0")
	lcurl, _ := url.Parse("http://127.0.0.1:0")
	cfg.ListenPeerUrls = []url.URL{*lpurl}
	cfg.ListenClientUrls = []url.URL{*lcurl}
	cfg.AdvertisePeerUrls = []url.URL{*lpurl}
	cfg.AdvertiseClientUrls = []url.URL{*lcurl}

	cfg.InitialCluster = cfg.Name + "=http://127.0.0.1:0"

	e, err := embed.StartEtcd(cfg)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}

	select {
	case <-e.Server.ReadyNotify():
		// etcd 已经准备好了
	case <-e.Err():
		e.Close()
		os.RemoveAll(dir)
		t.Fatal("embedded etcd start failed")
	case <-time.After(10 * time.Second):
		e.Close()
		os.RemoveAll(dir)
		t.Fatal("embedded etcd start timed out")
	}

	clientURL := e.Clients[0].Addr().String()
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{clientURL},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		e.Close()
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	return cli, func() {
		cli.Close()
		e.Close()
		os.RemoveAll(dir)
	}
}

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

func TestSuppressor_PersistAndLoad(t *testing.T) {
	cli, cleanup := newEmbeddedEtcd(t)
	defer cleanup()

	ctx := context.Background()
	s1 := NewSuppressor()
	// 触发并持久化
	if !s1.AllowAndPersist(ctx, cli, "rule-1", "target-A", 60*time.Second) {
		t.Fatalf("first trigger should be allowed")
	}
	// 立即再触发, cooldown 还没到, 应该被拦
	if s1.AllowAndPersist(ctx, cli, "rule-1", "target-A", 60*time.Second) {
		t.Fatalf("second trigger within cooldown should be blocked")
	}
	time.Sleep(200 * time.Millisecond) // 确保上次触发的时间戳写入 etcd

	// 模拟重启，创建一个新的 Suppressor 实例, 从 etcd 加载状态
	s2 := NewSuppressor()
	if err := s2.LoadFromEtcd(ctx, cli); err != nil {
		t.Fatalf("LoadFromEtcd failed: %v", err)
	}
	// s2 应该知道 "rule-1:target-A" 上次触发的时间, 仍然在 cooldown 内, 所以被拦
	if s2.Allow("rule-1", "target-A", 60*time.Second) {
		t.Fatalf("after loading from etcd, trigger within cooldown should still be blocked")
	}
	// 不同的 rule 或 target 不受影响
	if !s2.Allow("rule-2", "target-A", 60*time.Second) {
		t.Fatalf("different rule should not be blocked")
	}
}
