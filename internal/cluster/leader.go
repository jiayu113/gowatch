package cluster

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/jiayu113/gowatch/internal/metrics"
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
	cli      *clientv3.Client
	nodeID   string
	cfg      Config
	isLeader atomic.Bool // 记住自己是不是 leader, 供 IsLeader() 快速返回用, 不需要每次都访问 etcd

	// 仅供测试：暴露当前持有的 Session, 测试可以 Revoke 它的 Lease 来模拟 session 死掉
	curSess atomic.Pointer[concurrency.Session]
}

func NewLeader(cli *clientv3.Client, nodeID string, cfg Config) *Leader {
	return &Leader{cli: cli, nodeID: nodeID, cfg: cfg}
}

func (l *Leader) IsLeader() bool {
	return l.isLeader.Load()
}

func (l *Leader) NodeID() string {
	return l.nodeID
}
func (l *Leader) Mode() string {
	return "cluster"
}

// currentSession 仅供测试用, 返回 nil 表示当前未持有 session
func (l *Leader) currentSession() *concurrency.Session {
	return l.curSess.Load()
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
		l.curSess.Store(sess) // 仅供测试用, session 成功创建了才存, 现在有个 session 在手
		e := concurrency.NewElection(sess, electionPrefix)
		// 阻塞直到上位或 ctx 取消
		log.Printf("cluster: campaigning as %s", l.nodeID)
		if err := e.Campaign(ctx, l.nodeID); err != nil {
			sess.Close()
			l.curSess.Store(nil) // 仅供测试用, session 还没上位就失败了, 现在没有 session 在手
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("cluster: campaign failed: %v", err)
			bo.Wait(ctx)
			continue
		}
		// 上位
		log.Printf("cluster: !!!ELECTED AS LEADER (node=%s)!!!", l.nodeID)
		l.isLeader.Store(true)  // 记住自己是 leader 了
		metrics.IsLeader.Set(1) // 更新 metrics

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
		l.isLeader.Store(false) // 记住自己不再是 leader 了
		metrics.IsLeader.Set(0) // 更新 metrics
		l.curSess.Store(nil)    // 仅供测试用, session 失效后清除
		sess.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		bo.Reset()
	}
}
