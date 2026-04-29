package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jiayu113/gowatch/internal/checker"
	"github.com/jiayu113/gowatch/internal/config"
	"github.com/jiayu113/gowatch/internal/metrics"
	"github.com/jiayu113/gowatch/internal/storage"
)

// Pool 是检测调度器
type Pool struct {
	store    *storage.Store
	targets  []config.Target
	workers  int
	interval time.Duration
	checkers map[string]checker.Checker // target 名 -> 对应的 Checker 实例
}

func NewPool(store *storage.Store, targets []config.Target, workers int, interval time.Duration) *Pool {
	checkers := make(map[string]checker.Checker)
	for _, t := range targets {
		switch t.Type {
		case "http":
			checkers[t.Name] = &checker.HTTPChecker{Target: t}
		case "tcp":
			checkers[t.Name] = &checker.TCPChecker{Target: t}
		}
	}
	return &Pool{
		store:    store,
		targets:  targets,
		workers:  workers,
		interval: interval,
		checkers: checkers,
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
			// 同时更新 metrics
			// r.Target — target名字，比如"baidu-home"
			// r.Status — 状态，"up"或"down"
			// r.Latency.Seconds() — 把延迟从time.Duration转成秒的float64,Histogram需要float64
			// r.Error != "" — Error字段不为空说明有错误，转成bool传给hasError
			metrics.Record(r.Target, r.Status, r.Latency.Seconds(), r.Error != "")
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
		}
	}
}

func (p *Pool) dispatchJobs(jobs chan<- config.Target) {
	for _, t := range p.targets {
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
		c := p.checkers[target.Name]
		checkCtx, cancel := context.WithTimeout(ctx, target.Timeout)
		result := c.Check(checkCtx)
		cancel()
		log.Printf("worker: target=%s status=%s latency=%s", result.Target, result.Status, result.Latency)
		results <- result
	}
}
