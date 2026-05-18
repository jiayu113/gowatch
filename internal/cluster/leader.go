package cluster

import (
	"context"
	"log"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

const electionPrefix = "/gowatch/leader"

// backoff 简单的指数退避, 给运行期 etcd 临时不可用用
type backoff struct {
	cur, max time.Duration
}

func newBackoff(initial, max time.Duration) *backoff {
	return &backoff{cur: initial, max: max}
}

func (b *backoff) Wait(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(b.cur):
	}
	b.cur *= 2
	if b.cur > b.max {
		b.cur = b.max
	}
}

func (b *backoff) Reset() {
	b.cur = time.Second
}

// Leader 封装 etcd Election 逻辑, 对 scheduler 透明
type Leader struct {
	cli    *clientv3.Client
	nodeID string
	cfg    Config
}

func NewLeader(cli *clientv3.Client, nodeID string, cfg Config) *Leader {
	return &Leader{cli: cli, nodeID: nodeID, cfg: cfg}
}

// Run 阻塞直到 ctx 取消
func (l *Leader) Run(ctx context.Context, onLeader func(context.Context)) error {
	bo := newBackoff(time.Second, 30*time.Second)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sess, err := NewSession(l.cli, l.cfg.SessionTTL)
		if err != nil {
			log.Printf("cluster: session failed: %v", err)
			bo.Wait(ctx)
			continue
		}
		e := concurrency.NewElection(sess, electionPrefix)
		// 阻塞直到上位或 ctx 取消
		log.Printf("cluster: campaigning as %s", l.nodeID)
		if err := e.Campaign(ctx, l.nodeID); err != nil {
			sess.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("cluster: campaign failed: %v", err)
			bo.Wait(ctx)
			continue
		}
		// 上位
		log.Printf("cluster: !!!ELECTED AS LEADER (node=%s)!!!", l.nodeID)

		leaderCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			onLeader(leaderCtx)
			close(done)
		}()
		// 等下台信号
		select {
		case <-sess.Done():
			log.Println("cluster: session lost, demoting")
		case <-ctx.Done():
			log.Println("cluster: shutdown signal received")
		}
		// 下台
		cancel()
		select {
		case <-done:
			log.Println("cluster: scheduler exited cleanly")
		case <-time.After(5 * time.Second):
			log.Println("cluster: scheduler exit TIMEOUT")
		}
		sess.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		bo.Reset()
	}
}
