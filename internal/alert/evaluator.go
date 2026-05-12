package alert

import (
	"context"
	"log"
	"time"

	"github.com/jiayu113/gowatch/internal/checker"
)

// Evaluator 是评估 + 抑制 + 通知的整合点
type Evaluator struct {
	rules      []Rule
	window     *Window
	suppressor *Suppressor
	notifiers  map[string]Notifier // url -> notifier，复用同一个 http.Client
	emit       chan<- Event
}

func NewEvaluator(rules []Rule, emit chan<- Event) *Evaluator {
	return &Evaluator{
		rules:      rules,
		window:     NewWindow(50),
		suppressor: NewSuppressor(),
		notifiers:  make(map[string]Notifier),
		emit:       emit,
	}
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
		if !e.suppressor.Allow(rule.Name, r.Target, rule.Cooldown) {
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
