package alert

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const suppressorKeyPrefix = "/gowatch/suppressor/"

type suppressorState struct {
	LastFiredAt time.Time `json:"last_fired_at"`
}

// Suppressor 在 cooldown 内拦截重复触发。in-memory，跨重启清零（本周期不解决）。
type Suppressor struct {
	mu        sync.Mutex
	lastFired map[string]time.Time // key = ruleName + ":" + target
}

func NewSuppressor() *Suppressor {
	return &Suppressor{lastFired: make(map[string]time.Time)}
}

// Allow 返回是否允许触发；如果允许，记录本次触发时间
func (s *Suppressor) Allow(ruleName, target string, cooldown time.Duration) bool {
	if cooldown <= 0 {
		return true
	}
	key := ruleName + ":" + target
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.lastFired[key]
	if !ok || time.Since(last) >= cooldown {
		s.lastFired[key] = time.Now()
		return true
	}
	return false
}

// 把 etcd 里的状态加载到内存
func (s *Suppressor) LoadFromEtcd(ctx context.Context, cli *clientv3.Client) error {
	resp, err := cli.Get(ctx, suppressorKeyPrefix, clientv3.WithPrefix())
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, kv := range resp.Kvs {
		key := string(kv.Key[len(suppressorKeyPrefix):]) // 去掉前缀
		var st suppressorState
		if err := json.Unmarshal(kv.Value, &st); err != nil {
			log.Printf("Failed to unmarshal suppressor state for key %s: %v", key, err)
			continue // 反序列化失败就跳过这个 key
		}
		s.lastFired[key] = st.LastFiredAt
	}
	log.Printf("alert: loaded %d suppressor entries from etcd", len(resp.Kvs))
	return nil
}

// 在 Allow 的基础上, 触发时持久化到 etcd
// cli 可以为 nil(单机模式或集群启动失败时降低为纯内存)
func (s *Suppressor) AllowAndPersist(ctx context.Context, cli *clientv3.Client, ruleName, target string, cooldown time.Duration) bool {
	if !s.Allow(ruleName, target, cooldown) {
		return false
	}
	if cli == nil {
		return true
	}
	// 异步写入 etcd，避免阻塞主流程
	go func() {
		key := suppressorKeyPrefix + ruleName + ":" + target
		st := suppressorState{LastFiredAt: time.Now()}
		data, err := json.Marshal(st)
		if err != nil {
			log.Printf("Failed to marshal suppressor state for key %s: %v", key, err)
			return
		}
		if _, err := cli.Put(ctx, key, string(data)); err != nil {
			log.Printf("Failed to persist suppressor state to etcd for key %s: %v", key, err)
		}
	}()
	return true
}
