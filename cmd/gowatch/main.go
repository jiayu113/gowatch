package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jiayu113/gowatch/internal/aiops"
	"github.com/jiayu113/gowatch/internal/alert"
	"github.com/jiayu113/gowatch/internal/api"
	"github.com/jiayu113/gowatch/internal/cluster"
	"github.com/jiayu113/gowatch/internal/config"
	"github.com/jiayu113/gowatch/internal/metrics"
	"github.com/jiayu113/gowatch/internal/scheduler"
	"github.com/jiayu113/gowatch/internal/storage"
	"github.com/jiayu113/gowatch/pkg/checker"
)

func main() {

	configPath := flag.String("config", "config.yaml", "配置文件路径")
	dbPath := flag.String("db", "gowatch.db", "SQLite 数据库路径")
	queryMode := flag.Bool("query", false, "查询历史记录并退出")
	queryTarget := flag.String("target", "", "查询指定target,留空则查看所有")
	queryLimit := flag.Int("limit", 20, "查询返回条数")
	queryLatest := flag.Bool("latest", false, "查询每个target的最新状态")
	servePort := flag.String("port", ":8080", "HTTP服务监听端口")
	clusterMode := flag.Bool("cluster", false, "启用多实例集群模式(需要 --etcd)")
	etcdEndpoints := flag.String("etcd", "", "etcd endpoints,逗号分隔 e.g.localhost:2379")
	nodeID := flag.String("node-id", "", "本实例的node ID, 默认用hostname")
	flag.Parse()

	if *clusterMode {
		if *etcdEndpoints == "" {
			log.Fatal("--cluster 启用时必须指定 --etcd")
		}
		if *nodeID == "" {
			h, err := os.Hostname()
			if err != nil {
				log.Fatalf("获取hostname失败:%v", err)
			}
			*nodeID = h
		}
	}

	store, err := storage.New(*dbPath)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer store.Close()

	modeCount := 0
	if *queryLatest {
		modeCount++
	}
	if *queryMode {
		modeCount++
	}
	if modeCount > 1 {
		log.Fatal("--latest / --query 只能选其一")
	}

	// 查询每个target的最新状态
	if *queryLatest {
		results, err := store.GetLatestPerTarget()
		if err != nil {
			log.Fatal(err)
		}
		for _, r := range results {
			fmt.Printf("%s %-20s %-6s %-12s %s\n", r.Timestamp.Format("2006-01-02 15:04:05"), r.Target, r.Status, r.Latency, r.Error)
		}
		return
	}

	// 查询模式
	if *queryMode {
		var results []checker.Result
		if *queryTarget != "" {
			results, err = store.GetByTarget(*queryTarget, *queryLimit)
		} else {
			results, err = store.GetRecent(*queryLimit)
		}
		if err != nil {
			log.Fatalf("查询失败:%v", err)
		}

		for _, r := range results {
			fmt.Printf("%s %-20s %-6s %-12s %s\n", r.Timestamp.Format("2006-01-02 15:04:05"), r.Target, r.Status, r.Latency, r.Error)
		}
		return
	}

	// scheduler后台 + HTTP server前台
	fmt.Println("gowatch started")
	fmt.Println()

	cfg, err := config.LoadFromFile(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	alertCfg, err := alert.LoadFromFile("alerts.yaml")
	if err != nil {
		log.Printf("alert: load failed, alerting disabled: %v", err)
		alertCfg = &alert.Config{}
	}

	// 监听SIGINT/SIGTERM的ctx
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	aiCfg, err := aiops.LoadFromFile("aiops.yaml")
	if err != nil {
		log.Printf("aiops: config error, disabled: %v", err)
	}
	var analyzer *aiops.Analyzer
	if aiCfg != nil && aiCfg.Enabled {
		llm := aiops.NewOpenAICompat(aiCfg)
		if llm == nil {
			log.Printf("aiops: llm 初始化失败，AI 分析禁用（主链路正常运行）")
		} else {
			analyzer = aiops.New(aiCfg, llm, store.GetByTarget)
			go analyzer.Run(ctx)
			log.Printf("aiops: enabled model=%s", aiCfg.LLM.Model)
		}
	}

	emitCh := make(chan alert.Event, 100)
	go func() {
		for ev := range emitCh {
			// 主链路：正常存数据库
			if err := store.SaveAlert(ev); err != nil {
				log.Printf("alert: save failed: %v", err)
			}
			// 旁路：如果开启了 AI，把同一份告警塞给 AI 处理
			if analyzer != nil {
				analyzer.Submit(ev)
			}
		}
	}()

	// 启动scheduler，用done channel等它真的退出
	pool := scheduler.NewPool(store, cfg, 5, 10*time.Second)
	poolDone := make(chan struct{})
	var leaderState cluster.LeaderState
	if *clusterMode {
		// 集群模式: leader.Run 包裹 pool.Run
		endpoints := strings.Split(*etcdEndpoints, ",")
		clusterCil, err := cluster.NewClient(ctx, cluster.Config{Endpoints: endpoints})
		if err != nil {
			log.Fatalf("cluster: %v", err)
		}
		defer clusterCil.Close()

		evaluator := alert.NewEvaluator(alertCfg.Rules, emitCh, alert.WithEtcdClient(clusterCil))
		pool.SetEvaluator(evaluator)

		ldr := cluster.NewLeader(clusterCil, *nodeID, cluster.Config{Endpoints: endpoints})
		leaderState = ldr

		go func() {
			wrappedOnLeader := func(leaderCtx context.Context) {
				loadCtx, cancel := context.WithTimeout(leaderCtx, 5*time.Second)
				if err := evaluator.LoadSuppressorFromEtcd(loadCtx, clusterCil); err != nil {
					log.Printf("alert: load suppressor from etcd failed: %v", err)
				}
				cancel()
				pool.Run(leaderCtx)
			}
			if err := ldr.Run(ctx, wrappedOnLeader); err != nil && !errors.Is(err, context.Canceled) {
				log.Fatalf("cluster: leader exited: %v", err)
			}
			close(poolDone)
		}()
	} else {
		// 单机模式
		leaderState = cluster.NewSingleLeader(*nodeID)
		metrics.IsLeader.Set(1) // 单机模式恒为 leader

		evaluator := alert.NewEvaluator(alertCfg.Rules, emitCh)
		pool.SetEvaluator(evaluator)

		go func() {
			pool.Run(ctx)
			close(poolDone)
		}()
	}

	// 启动 config watcher（可选，watcher 起不来不影响主流程）
	reloadCh := make(chan *config.Config, 1)
	if err := config.Watch(ctx, *configPath, reloadCh); err != nil {
		log.Printf("config: watcher disabled:%v", err)
	} else {
		log.Println("config: watching for changes...")
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case cfg := <-reloadCh:
					pool.Reload(cfg)
				}
			}
		}()
	}

	// 启动HTTP server，goroutine不阻塞
	handler := api.NewHandler(store, leaderState)
	actualPort := *servePort
	if !strings.Contains(actualPort, ":") {
		actualPort = ":" + actualPort
	}
	server := &http.Server{
		Addr:    actualPort,
		Handler: handler.Routes(),
	}
	go func() {
		log.Printf("服务启动，监听%s", *servePort)
		log.Println("可用端点:")
		log.Println("  GET /api/health           - 健康检查")
		log.Println("  GET /api/status           - 各 target 最新状态")
		log.Println("  GET /api/history?target=x - 某 target 历史")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("服务异常退出：%v", err)
		}
	}()

	// 阻塞等信号
	<-ctx.Done()
	log.Println("收到退出信号，开始优雅关闭...")

	// 关闭HTTP server，最多等10s
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	log.Println("HTTP server 已关闭")

	// 等scheduler真的退完
	<-poolDone
	log.Println("scheduler 已退出")

	// defer store.Close() 会自动跑
	log.Println("gowatch 退出完成")

}

// // 1. 成功路径
// c1 := &checker.HTTPChecker{Target: config.Target{Name: "baidu", URL: "https://www.baidu.com"}}
// fmt.Printf("%+v\n", c1.Check(context.Background()))
// fmt.Println()

// // 2. 非 2xx 路径
// c2 := &checker.HTTPChecker{Target: config.Target{Name: "404-test", URL: "https://httpbin.org/status/500"}}
// fmt.Printf("%+v\n", c2.Check(context.Background()))
// fmt.Println()

// // 3. DNS 失败路径
// c3 := &checker.HTTPChecker{Target: config.Target{Name: "fake", URL: "https://this-domain-does-not-exist-12345.com"}}
// fmt.Printf("%+v\n", c3.Check(context.Background()))
// fmt.Println()

// // 4. ctx 超时路径
// ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
// defer cancel()
// c4 := &checker.HTTPChecker{Target: config.Target{Name: "slow", URL: "https://www.baidu.com"}}
// fmt.Printf("%+v\n", c4.Check(ctx))
// fmt.Println()

// // TCP 成功(连一个肯定开着的端口,比如你本机浏览器打着的 80 或 443)
// c5 := &checker.TCPChecker{Target: config.Target{Name: "local-web", URL: "www.baidu.com:443"}}
// fmt.Printf("%+v\n", c5.Check(context.Background()))
// fmt.Println()

// // TCP 失败(一个没开的端口)
// c6 := &checker.TCPChecker{Target: config.Target{Name: "closed-port", URL: "127.0.0.1:59999"}}
// fmt.Printf("%+v\n", c6.Check(context.Background()))
// fmt.Println()

// // TCP ctx 超时(故意超快,测 DialContext 真的响应 ctx)
// ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
// defer cancel2()
// c7 := &checker.TCPChecker{Target: config.Target{Name: "tcp-timeout", URL: "www.baidu.com:443"}}
// fmt.Printf("%+v\n", c7.Check(ctx2))
// fmt.Println()
