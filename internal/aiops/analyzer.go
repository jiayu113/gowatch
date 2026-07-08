package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jiayu113/gowatch/internal/alert"
	"github.com/jiayu113/gowatch/internal/metrics"
	"github.com/jiayu113/gowatch/pkg/checker"
)

type historyFn func(target string, limit int) ([]checker.Result, error)

type Analyzer struct {
	cfg     *Config
	llm     LLM
	history historyFn // 注入 store.GetByTarget,不直接依赖 storage 包(也躲开潜在 import 环)

	ch chan alert.Event

	mu        sync.Mutex
	lastRun   map[string]time.Time // target|rule → 上次分析时间(冷却)
	failStrk  int                  // 连续失败(熔断)
	openUntil time.Time            // 熔断开到何时
	dayKey    string               // 日上限:当天日期
	dayCount  int
}

func New(cfg *Config, llm LLM, history historyFn) *Analyzer {
	return &Analyzer{
		cfg: cfg, llm: llm, history: history,
		ch:      make(chan alert.Event, 16),
		lastRun: map[string]time.Time{},
	}
}

// Submit 投入永不阻塞:满了丢新保旧并计数
func (a *Analyzer) Submit(ev alert.Event) {
	select {
	case a.ch <- ev:
	default:
		metrics.AIOpsTotal.WithLabelValues("dropped").Inc()
		log.Printf("aiops: buffer full, dropped rule=%s target=%s", ev.RuleName, ev.Target)
	}
}

// Run 发送事件
func (a *Analyzer) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-a.ch:
			a.handle(ctx, ev)
		}
	}
}

func (a *Analyzer) handle(ctx context.Context, ev alert.Event) {
	if a.llm == nil {
		log.Printf("aiops: llm 未配置，跳过 target=%s", ev.Target)
		return
	}
	if reason, ok := a.gate(ev); !ok {
		metrics.AIOpsTotal.WithLabelValues(reason).Inc()
		log.Printf("aiops: %s target=%s", reason, ev.Target)
		return
	}
	hist, err := a.history(ev.Target, a.cfg.HistoryLimit)
	if err != nil {
		log.Printf("aiops: history query failed: %v", err)
		return
	}
	dc := BuildContext(ev, hist)

	cctx, cancel := context.WithTimeout(ctx, a.cfg.LLM.Timeout)
	defer cancel()
	start := time.Now()
	advice, err := a.llm.Complete(cctx, SystemPrompt, dc.RenderPrompt())
	if err != nil {
		a.onFail()
		metrics.AIOpsTotal.WithLabelValues("error").Inc()
		log.Printf("aiops: llm failed rule=%s target=%s err=%v(告警主链路不受影响)", ev.RuleName, ev.Target, err)
		return
	}
	a.onOK()
	metrics.AIOpsTotal.WithLabelValues("ok").Inc()
	a.persist(ev, advice, time.Since(start))
	log.Printf("aiops: diagnosis ready rule=%s target=%s (%.1fs)\n%s",
		ev.RuleName, ev.Target, time.Since(start).Seconds(), advice)
}

func (a *Analyzer) gate(ev alert.Event) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	// 熔断
	if now.Before(a.openUntil) {
		return "breaker_open", false
	}
	if day := now.Format("2006-01-02"); day != a.dayKey {
		a.dayKey, a.dayCount = day, 0
	}
	// 限流
	if a.dayCount >= a.cfg.Limits.DailyMax {
		return "daily_max", false
	}
	// 冷却
	key := ev.Target + "|" + ev.RuleName
	if last, ok := a.lastRun[key]; ok && now.Sub(last) < a.cfg.Limits.Cooldown {
		return "cooldown", false
	}
	a.lastRun[key] = now
	a.dayCount++
	return "", true
}

func (a *Analyzer) onFail() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failStrk++
	if a.failStrk >= a.cfg.Limits.BreakerFails {
		a.openUntil = time.Now().Add(a.cfg.Limits.BreakerOpen)
		a.failStrk = 0
		log.Printf("aiops: breaker OPEN for %s", a.cfg.Limits.BreakerOpen)
	}
}

func (a *Analyzer) onOK() { a.mu.Lock(); a.failStrk = 0; a.mu.Unlock() }

type record struct {
	Time      time.Time `json:"time"`
	Rule      string    `json:"rule"`
	Target    string    `json:"target"`
	Reason    string    `json:"reason"`
	Advice    string    `json:"advice"`
	Model     string    `json:"model"`
	LatencyMS int64     `json:"latency_ms"`
}

func (a *Analyzer) persist(ev alert.Event, advice string, took time.Duration) {
	_ = os.MkdirAll(a.cfg.Output.Dir, 0o755)
	path := filepath.Join(a.cfg.Output.Dir,
		fmt.Sprintf("diagnosis-%s.jsonl", time.Now().Format("20060102")))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("aiops: persist open failed: %v", err)
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(record{
		Time: time.Now(), Rule: ev.RuleName, Target: ev.Target, Reason: ev.Reason,
		Advice: advice, Model: a.cfg.LLM.Model, LatencyMS: took.Milliseconds(),
	})
}
