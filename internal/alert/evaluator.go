package alert

import (
	"context"
	"log"
	"time"

	"github.com/jiayu113/gowatch/internal/checker"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Evaluator 是评估 + 抑制 + 通知的整合点
type Evaluator struct {
	rules      []Rule
	window     *Window
	suppressor *Suppressor
	notifiers  map[string]Notifier // url -> notifier，复用同一个 http.Client
	emit       chan<- Event
	etcdCli    *clientv3.Client // 可选的 etcd 客户端，用于分布式抑制
}

type EvaluatorOption func(*Evaluator)

func WithEtcdClient(cli *clientv3.Client) EvaluatorOption {
	return func(e *Evaluator) {
		e.etcdCli = cli
	}
}

func NewEvaluator(rules []Rule, emit chan<- Event, opts ...EvaluatorOption) *Evaluator {
	e := &Evaluator{
		rules:      rules,
		window:     NewWindow(50),
		suppressor: NewSuppressor(),
		notifiers:  make(map[string]Notifier),
		emit:       emit,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// OnResult 由 collector 在每次保存后调用。永远不阻塞，错误吞日志。
func (e *Evaluator) OnResult(r checker.Result) {
	e.window.Push(r)
	for _, rule := range e.rules {
		if rule.Target != "*" && rule.Target != r.Target {
			continue
		}
		recent := e.window.Snapshot(r.Target)
		hit, reason := rule.Match(recent)
		if !hit {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		allowed := e.suppressor.AllowAndPersist(ctx, e.etcdCli, rule.Name, r.Target, rule.Cooldown)
		cancel()
		if !allowed {
			continue
		}
		ev := Event{
			RuleName: rule.Name,
			Target:   r.Target,
			FireAt:   time.Now(),
			Reason:   reason,
			Snapshot: recent,
		}
		// 异步发，不阻塞 collector
		go e.fire(rule, ev)
	}
}

func (e *Evaluator) fire(rule Rule, ev Event) {
	n, ok := e.notifiers[rule.Webhook]
	if !ok {
		n = NewWebhookNotifier(rule.Webhook)
		e.notifiers[rule.Webhook] = n
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.Notify(ctx, ev); err != nil {
		log.Printf("alert: notify failed rule=%s target=%s err=%v", rule.Name, ev.Target, err)
	}
	// emit 给持久化订阅者
	if e.emit != nil {
		select {
		case e.emit <- ev:
		default:
			log.Printf("alert: emit channel full, dropping event")
		}
	}
}

// LoadSuppressorFromEtcd 从 etcd 加载抑制状态，适用于重启时恢复。
func (e *Evaluator) LoadSuppressorFromEtcd(ctx context.Context, cli *clientv3.Client) error {
	if cli == nil {
		return nil // 单机模式没有 etcd，不需要加载抑制状态
	}
	return e.suppressor.LoadFromEtcd(ctx, cli)
}
