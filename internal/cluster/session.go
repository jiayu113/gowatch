package cluster

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

const (
	defaultDialTimeout = 5 * time.Second
	defaultSessionTTL  = 15
)

// Config 是创建 Session 所需的配置
type Config struct {
	Endpoints  []string
	SessionTTL int
}

// NewClient 创建 etcd client, 启动期 dial 失败直接报错
func NewClient(ctx context.Context, cfg Config) (*clientv3.Client, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, errors.New("cluster: no etcd endpoints provided")
	}
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: defaultDialTimeout,
		Context:     ctx,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: dial etcd failed: %w", err)
	}
	// 主动 sanity check, 真发一条请求, 确认 etcd 可用
	ctxPing, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()
	if _, err := cli.Status(ctxPing, cfg.Endpoints[0]); err != nil {
		cli.Close()
		return nil, fmt.Errorf("cluster:etcd status check failed: %w", err)
	}
	log.Printf("cluster: etcd connected, endpoints=%v", cfg.Endpoints)
	return cli, nil
}

// NewSession 创建一个 etcd Session, 用于 Election
func NewSession(cli *clientv3.Client, ttl int) (*concurrency.Session, error) {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	sess, err := concurrency.NewSession(cli, concurrency.WithTTL(ttl))
	if err != nil {
		return nil, fmt.Errorf("cluster: new session failed: %w", err)
	}
	log.Printf("cluster: session created, ttl=%ds lease=%x", ttl, sess.Lease())
	return sess, nil
}
