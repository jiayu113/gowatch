package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jiayu113/gowatch/internal/alert"
	"github.com/jiayu113/gowatch/internal/config"
	"github.com/jiayu113/gowatch/internal/metrics"
	"github.com/jiayu113/gowatch/internal/storage"
	"github.com/jiayu113/gowatch/pkg/checker"
)

// Pool 是检测调度器
type Pool struct {
	store     *storage.Store
	mu        sync.RWMutex
	targets   []config.Target
	workers   int
	interval  time.Duration
	checkers  map[string]checker.Checker // target 名 -> 对应的 Checker 实例
	reloadCh  chan *config.Config
	evaluator *alert.Evaluator
}

func NewPool(store *storage.Store, cfg *config.Config, workers int, interval time.Duration) *Pool {
	p := &Pool{
		store:    store,
		workers:  workers,
		interval: interval,
		reloadCh: make(chan *config.Config, 1),
	}
	p.applyConfig(cfg)
	return p
}

func (p *Pool) SetEvaluator(e *alert.Evaluator) {
	p.evaluator = e
}

// applyConfig 写入 targets + checkers,加锁
func (p *Pool) applyConfig(cfg *config.Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.targets = cfg.Targets
	p.checkers = make(map[string]checker.Checker, len(cfg.Targets))
	for _, t := range cfg.Targets {
		switch t.Type {
		case "http":
			p.checkers[t.Name] = checker.NewHTTPChecker(t)
		case "tcp":
			p.checkers[t.Name] = &checker.TCPChecker{Target: t}
		case "cert":
			warn := t.CertWarnDays
			if warn == 0 {
				warn = 14 // 默认 14 天
			}
			p.checkers[t.Name] = &checker.CertChecker{Target: t, WarnDays: warn}
		default:
			log.Printf("scheduler: unknown type=%q for target=%q, skipped", t.Type, t.Name)
		}
	}
}

// Reload 把新配置塞进 channel，不阻塞；buffer=1 保证最新一份覆盖旧的
func (p *Pool) Reload(cfg *config.Config) {
	select {
	case p.reloadCh <- cfg:
	default:
		select {
		case <-p.reloadCh:
		default:
		}
		p.reloadCh <- cfg
	}
}

// Run 启动调度器。会阻塞，直到 ctx 被取消
func (p *Pool) Run(ctx context.Context) {
	jobs := make(chan config.Target, len(p.targets))
	results := make(chan checker.Result, len(p.targets))

	// 启动 N 个 worker
	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go p.worker(ctx, &wg, jobs, results)
	}

	// 启动 collector：单独 goroutine 写库，避免 IO 阻塞 worker
	var collectorWg sync.WaitGroup
	collectorWg.Add(1)
	go func() {
		defer collectorWg.Done()
		for r := range results {
			if err := p.store.Save(r); err != nil {
				log.Printf("scheduler: save failed:%v", err)
			}
			if p.evaluator != nil {
				p.evaluator.OnResult(r)
			}
		}
	}()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	log.Printf("scheduler: started workers=%d interval=%s", p.workers, p.interval)
	p.dispatchJobs(jobs) // 立刻派一轮，不等第一个 tick

	for {
		select {
		case <-ctx.Done():
			close(jobs)        //  关闭 jobs，让 worker 做完手上的活退出
			wg.Wait()          //  等所有 worker 退出
			close(results)     //  关闭 results，让 collector 退出
			collectorWg.Wait() //  等 collector 写完最后几条
			log.Println("scheduler: stopped")
			return
		case <-ticker.C:
			p.dispatchJobs(jobs)
		case cfg := <-p.reloadCh:
			log.Println("scheduler: reloading config")
			p.applyConfig(cfg)
			log.Printf("scheduler: reloaded, targets=%d", len(cfg.Targets))
		}
	}
}

func (p *Pool) dispatchJobs(jobs chan<- config.Target) {
	p.mu.RLock()
	targets := p.targets
	p.mu.RUnlock()
	for _, t := range targets {
		select {
		case jobs <- t:
		default:
			// jobs 满了说明 worker 还没处理完上一轮，跳过避免雪崩
			log.Printf("scheduler:jobs full,skip target=%s", t.Name)
		}
	}
}

func (p *Pool) worker(ctx context.Context, wg *sync.WaitGroup, jobs <-chan config.Target, results chan<- checker.Result) {
	defer wg.Done()
	for target := range jobs {
		p.mu.RLock()
		c := p.checkers[target.Name]
		p.mu.RUnlock()
		if c == nil {
			// reload 删掉了这个 target，但 jobs channel 里还有它的旧 job
			log.Printf("worker: checker not found for target=%s, skip", target.Name)
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, target.Timeout)
		result := c.Check(checkCtx)
		cancel()
		log.Printf("worker: target=%s status=%s latency=%s", result.Target, result.Status, result.Latency)
		metrics.Record(result.Target, result.Status, result.ErrorType, result.Latency.Seconds(), result.Error != "")
		if target.Type == "cert" {
			metrics.SSLCertExpiryDays.WithLabelValues(result.Target).Set(result.ExpiryDays)
		}
		results <- result
	}
}
