package cluster

import (
	"context"
	"net/url"
	"os"
	"sync"
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

// 第一个 sanity test, 确保我们能成功启动一个内嵌的 etcd 实例, 并且能用 client 连接上它
func TestEmbeddedEtcd(t *testing.T) {
	cli, cleanup := newEmbeddedEtcd(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Put(ctx, "/test/key", "hello"); err != nil {
		t.Fatal(err)
	}
	resp, err := cli.Get(ctx, "/test/key")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Kvs) != 1 || string(resp.Kvs[0].Value) != "hello" {
		t.Fatalf("got %v, want hello", resp.Kvs)
	}
}

func TestLeader_CampaignSucceeds(t *testing.T) {
	cli, cleanup := newEmbeddedEtcd(t)
	defer cleanup()
	ldr := NewLeader(cli, "instance-A", Config{SessionTTL: 5})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	elected := make(chan struct{})
	runDone := make(chan error, 1)
	go func() {
		runDone <- ldr.Run(ctx, func(leaderCtx context.Context) {
			close(elected)     // 成功当选了
			<-leaderCtx.Done() // 等被取消
		})
	}()

	select {
	case <-elected:
		// 成功当选了, 现在就等着它被取消
	case <-time.After(3 * time.Second):
		t.Fatal("LeaderManager 没在 3 秒内当选")
	}

	cancel() // 主动取消领导权

	select {
	case err := <-runDone:
		if err != nil && err != context.Canceled {
			t.Errorf("Run 应该返回 nil 或 context.Canceled, 得到 %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("Run 没在取消后 7 秒内退出")
	}
}

// 主备切换
func TestLeader_TwoInstancesFailover(t *testing.T) {
	cli, cleanup := newEmbeddedEtcd(t)
	defer cleanup()
	ldrA := NewLeader(cli, "instance-A", Config{SessionTTL: 3})
	ldrB := NewLeader(cli, "instance-B", Config{SessionTTL: 3})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	aElected := make(chan struct{})
	bElected := make(chan struct{})

	ctxA, cancelA := context.WithCancel(ctx)

	go ldrA.Run(ctxA, func(leaderCtx context.Context) {
		close(aElected)
		<-leaderCtx.Done()
	})
	<-aElected // 等 A 当选

	go ldrB.Run(ctx, func(leaderCtx context.Context) {
		close(bElected)
		<-leaderCtx.Done()
	})

	// B应该在A还在active时阻塞
	select {
	case <-bElected:
		t.Fatal("B 不应该在 A 还在 active 时当选")
	case <-time.After(3 * time.Second):
		// 正常, B 没有当选
	}

	// 现在让 A 失效
	cancelA()

	select {
	case <-bElected:
		// B 成功当选了
	case <-time.After(10 * time.Second):
		t.Fatal("B 没有在 A 失效后 10 秒内当选")
	}
}

// 模拟 leader 的 session 失效
func TestLeader_SessionExpires(t *testing.T) {
	cli, cleanup := newEmbeddedEtcd(t)
	defer cleanup()

	IdrA := NewLeader(cli, "instance-A", Config{SessionTTL: 3})
	IdrB := NewLeader(cli, "instance-B", Config{SessionTTL: 3})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	aElected := make(chan struct{})
	bElected := make(chan struct{})

	// A 被 revoke 后会重新尝试参选, B 也可能上下台多次
	// 用sync.Once 守护 B 的当选通知, 确保它只被通知一次
	var bOnce sync.Once
	go IdrA.Run(ctx, func(leaderCtx context.Context) {
		close(aElected)
		<-leaderCtx.Done()
	})
	<-aElected // 等 A 当选

	go IdrB.Run(ctx, func(leaderCtx context.Context) {
		bOnce.Do(func() { close(bElected) })
		<-leaderCtx.Done()
	})

	// 给 B 时间进入 Campaign 阻塞，确保 B 已经在 ectd 里写下 Election Key
	// (否则 A 被 revoke 时如果 B 还没注册, A 新一轮 Campaign 可能比 B 更快)
	time.Sleep(500 * time.Millisecond)

	// 模拟 A 的 session 失效
	aSess := IdrA.currentSession()
	if aSess == nil {
		t.Fatal("A 当前已经持有 session")
	}
	if _, err := cli.Revoke(context.Background(), aSess.Lease()); err != nil {
		t.Fatalf("revoke A 的 lease 失败: %v", err)
	}

	// B 应该立即接管
	select {
	case <-bElected:
		// B 成功当选了
	case <-time.After(10 * time.Second):
		t.Fatal("B 没有在 A session 失效后 10 秒内当选")
	}

}

// CtxCancel 服务的优雅停机测试
func TestLeader_CtxCancelTriggersExit(t *testing.T) {
	cli, cleanup := newEmbeddedEtcd(t)
	defer cleanup()
	ldr := NewLeader(cli, "instance-A", Config{SessionTTL: 5})
	ctx, cancel := context.WithCancel(context.Background())

	elected := make(chan struct{})
	runDone := make(chan error, 1)
	go func() {
		runDone <- ldr.Run(ctx, func(leaderCtx context.Context) {
			close(elected)
			<-leaderCtx.Done()
		})
	}()
	<-elected
	cancel()
	select {
	case err := <-runDone:
		if err != nil && err != context.Canceled {
			t.Errorf("Run 应该返回 nil 或 context.Canceled, 得到 %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("Run 没在取消后 7 秒内退出")
	}
}
