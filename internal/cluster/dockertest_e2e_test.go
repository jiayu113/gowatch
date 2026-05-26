package cluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// startEtcdContainer 起一个真实 etcd 容器,返回 client、容器资源句柄、cleanup
func startEtcdContainer(t *testing.T) (*clientv3.Client, *dockertest.Resource, func()) {
	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("连不上 docker: %v", err)
	}
	if err := pool.Client.Ping(); err != nil {
		t.Skipf("docker daemon 不可用,skip: %v", err)
	}

	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "quay.io/coreos/etcd",
		Tag:        "v3.5.17",
		Cmd:        []string{"/usr/local/bin/etcd", "--advertise-client-urls=http://0.0.0.0:2379", "--listen-client-urls=http://0.0.0.0:2379"},
	})
	if err != nil {
		t.Fatalf("启动 etcd 容器失败: %v", err)
	}
	_ = resource.Expire(120) // 2分钟后自动销毁容器

	endpoint := fmt.Sprintf("localhost:%s", resource.GetPort("2379/tcp"))

	var cli *clientv3.Client
	if err := pool.Retry(func() error {
		c, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{endpoint},
			DialTimeout: 2 * time.Second,
		})
		if err != nil {
			return err
		}
		ctx, cancel := contextTimeout(2 * time.Second)
		defer cancel()
		if _, err := c.Status(ctx, endpoint); err != nil {
			c.Close()
			return err
		}
		cli = c
		return nil
	}); err != nil {
		pool.Purge(resource)
		t.Fatalf("等 etcd ready 超时: %v", err)
	}

	return cli, resource, func() {
		cli.Close()
		pool.Purge(resource)
	}
}

func contextTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func TestE2E_LeaderFailover(t *testing.T) {
	cli, _, cleanup := startEtcdContainer(t)
	defer cleanup()

	ldrA := NewLeader(cli, "node-A", Config{SessionTTL: 3})
	ldrB := NewLeader(cli, "node-B", Config{SessionTTL: 3})

	ctx, cancel := contextTimeout(30 * time.Second)
	defer cancel()
	ctxA, cancelA := context.WithCancel(ctx)

	aElected := make(chan struct{})
	bElected := make(chan struct{})

	go ldrA.Run(ctxA, func(lc context.Context) { close(aElected); <-lc.Done() })
	<-aElected

	go ldrB.Run(ctx, func(lc context.Context) { close(bElected); <-lc.Done() })

	select {
	case <-bElected:
		t.Fatal("A 在位时 B 不该当选")
	case <-time.After(3 * time.Second):
	}

	cancelA() // A 退出
	select {
	case <-bElected:
	case <-time.After(10 * time.Second):
		t.Fatal("A 退出后 B 没在 10s 内接管")
	}
}

func TestE2E_EtcdPauseResume_Backoff(t *testing.T) {
	cli, resource, cleanup := startEtcdContainer(t)
	defer cleanup()

	ldrA := NewLeader(cli, "node-A", Config{SessionTTL: 3})

	ctx, cancel := contextTimeout(40 * time.Second)
	defer cancel()

	firstElected := make(chan struct{})
	leaseLost := make(chan struct{})
	reElected := make(chan struct{})

	isFirstTime := true

	go ldrA.Run(ctx, func(lc context.Context) {
		if isFirstTime {
			close(firstElected)
			isFirstTime = false

			<-lc.Done()
			close(leaseLost)
		} else {
			close(reElected)
			<-lc.Done()
		}
	})

	<-firstElected
	t.Log("节点首次当选 Leader, 准备拔网线...")

	pool, _ := dockertest.NewPool("")

	if err := pool.Client.PauseContainer(resource.Container.ID); err != nil {
		t.Fatalf("冻结容器失败: %v", err)
	}
	t.Log("etcd 已被冻结 (模拟网络黑洞/进程挂起)")

	<-leaseLost
	t.Log("节点发现 etcd 连不上，已主动放弃 Leader 职位，开始 Backoff 重试...")

	if err := pool.Client.UnpauseContainer(resource.Container.ID); err != nil {
		t.Fatalf("恢复容器失败: %v", err)
	}
	t.Log("etcd 已解冻 (网络恢复)")

	select {
	case <-reElected:
		t.Log("完美！节点成功自动重连，并抢回了 Leader! ")
	case <-time.After(15 * time.Second):
		t.Fatal("网络恢复后 15 秒都没能重连成功, Backoff 机制可能失效了")
	}
}
