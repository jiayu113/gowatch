# GoWatch

> 用 Go 写的轻量探活监控服务 —— 配置驱动、并发探测、错误分类、SQLite 持久化、HTTP API、Web 面板、Prometheus 指标。单二进制、无 CGO、跨平台。

![Web 面板截图](image.png)
![Prometheus 指标截图](image-1.png)

GoWatch 周期性地对一组 HTTP / TCP 目标做健康检查,把结果按**错误类型**分桶并写入 SQLite,通过 HTTP API + Web UI + Prometheus `/metrics` 端点暴露。整个服务是常驻进程,支持优雅关闭,可以放心 Ctrl+C 或 SIGTERM。

---

## ✨ 特性

- **多协议健康检查** — HTTP(S) 状态码 / TCP 端口连通性,通过 `Checker` 接口扩展
- **并发 Worker Pool 调度** — 固定 worker + Ticker 周期触发 + collector 解耦写库
- **错误分类(`error_type`)** — 把网络层错误归类为 `timeout` / `refused` / `dns` / `non_2xx` / `other`,Prometheus 指标按错误类型分桶,**让告警规则能区分"网络抖动"和"服务挂了"**
- **SQLite 持久化** — 纯 Go 驱动 (`modernc.org/sqlite`),无 CGO,跨平台编译零负担
- **REST API + Web UI** — 实时状态、按 target 查历史,前端 5 秒自动刷新
- **Prometheus 集成** — `/metrics` 端点暴露 counter / histogram / gauge,可直接接 Grafana
- **Graceful Shutdown** — `signal.NotifyContext` + `server.Shutdown` + scheduler done channel 三步收尾,不丢数据
- **CLI 工具化** — 同一个二进制支持服务模式、查询历史、查看最新状态三种用法

---

## 🏗️ 架构

```
                 主 goroutine (scheduler.Run)
                          │
                          │ Ticker 每 N 秒触发 dispatch
                          ▼
                    jobs channel ──┬─→ worker 1 ─┐
                                   ├─→ worker 2 ─┤
                                   ├─→ worker 3 ─┼─→ results channel ─→ collector ─→ SQLite
                                   ├─→ worker 4 ─┤                                      │
                                   └─→ worker 5 ─┘                                      └─→ Prometheus 指标更新

ctx.Done() ─→ close(jobs) ─→ workers 退出 ─→ close(results) ─→ collector 退出 ─→ store.Close
```

**三层 goroutine 职责分离:**
- **主 goroutine** — Ticker 派活、监听 ctx 信号、协调关闭顺序
- **Worker Pool** — 并发跑 `Checker.Check(ctx)`,IO 密集场景天然受益于并发,每个 worker 用 per-target 独立 ctx 避免互相拖累
- **Collector** — 单独 goroutine 串行写库 + 同步更新 Prometheus 指标,把 IO 和指标写入与 worker 解耦

---

## 🚀 快速开始

### 安装

```bash
git clone https://github.com/jiayu113/gowatch.git
cd gowatch
go build -o gowatch ./cmd/gowatch
```

或直接运行:

```bash
go run ./cmd/gowatch
```

### 配置

在项目根目录创建 `config.yaml`:

```yaml
targets:
  - name: baidu-home
    type: http
    url: https://www.baidu.com
    timeout: 3s

  - name: local-mysql
    type: tcp
    url: 127.0.0.1:3306
    timeout: 2s

  - name: k8s-api
    type: tcp
    url: 127.0.0.1:6443
    timeout: 1s

  - name: github-home
    type: http
    url: https://www.github.com
    timeout: 3s
```

通过 `type` 字段显式指定探测协议(`http` 或 `tcp`),scheduler 会构造对应的 Checker 实例。要新增 ICMP / gRPC / DNS 等探测方式,只需实现 `Checker` 接口并在 `NewPool` 的 switch 里加一个 case。

### 启动服务

```bash
./gowatch
```

默认行为:
- 加载 `./config.yaml`
- 数据库写入 `./gowatch.db`
- HTTP 服务监听 `:8080`
- Worker 数 5,探测周期 10 秒
- Ctrl+C / SIGTERM 优雅退出

启动后:

```
gowatch started

2026/05/07 11:50:32 scheduler: started workers=5 interval=10s
2026/05/07 11:50:32 服务启动,监听 :8080
2026/05/07 11:50:32 可用端点:
2026/05/07 11:50:32   GET /api/health           - 健康检查
2026/05/07 11:50:32   GET /api/status           - 各 target 最新状态
2026/05/07 11:50:32   GET /api/history?target=x - 某 target 历史
```

### 命令行选项

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `--config` | `config.yaml` | 配置文件路径 |
| `--db` | `gowatch.db` | SQLite 数据库路径 |
| `--port` | `:8080` | HTTP 监听端口 |
| `--query` | `false` | 查询历史模式(查完即退) |
| `--target` | `""` | 配合 `--query`,只查指定 target |
| `--limit` | `20` | 配合 `--query`,返回条数上限 |
| `--latest` | `false` | 查询每个 target 的最新一条状态 |

```bash
# 查最近 50 条历史
./gowatch --query --limit 50

# 查 baidu-home 的最近 20 次记录
./gowatch --query --target baidu-home

# 查每个 target 当前最新状态(命令行版的 /api/status)
./gowatch --latest
```

---

## 📡 API

打开浏览器访问 `http://localhost:8080` 看 Web 面板,或者直接调 API:

### `GET /api/health`

```json
{
  "status": "ok",
  "uptime": "1h2m52.9687301s"
}
```

### `GET /api/status`

返回每个 target 的最新一条记录:

```json
[
  {
    "target": "baidu-home",
    "status": "up",
    "latency_ms": 144.797,
    "error": "",
    "timestamp": "2026-05-07T03:50:42Z"
  },
  {
    "target": "github-home",
    "status": "down",
    "latency_ms": 3000.079,
    "error": "Get \"https://www.github.com\": context deadline exceeded",
    "timestamp": "2026-05-07T03:50:45Z"
  }
]
```

### `GET /api/history?target=<name>&limit=<n>`

返回指定 target 的历史记录(`limit` 默认 20、上限 1000,按时间倒序)。

### `GET /metrics`

Prometheus 兼容的指标端点(详见下一节)。

---

## 📊 Prometheus 指标

| 指标 | 类型 | Labels | 说明 |
|------|------|--------|------|
| `gowatch_check_total` | counter | `target`, `status` | 累计检查次数,按 up/down 分桶 |
| `gowatch_check_errors_total` | counter | `target`, **`error_type`** | 累计错误次数,**按错误类型分桶** |
| `gowatch_check_latency_seconds` | histogram | `target` | 检查耗时分布 |
| `gowatch_target_up` | gauge | `target` | 当前是否 up(1=up, 0=down) |
| 标准 Go runtime 指标 | - | - | goroutine 数、heap、GC 等 |

### 为什么需要 `error_type` 这个维度?

最早 `errors_total` 只有 `target` 一个 label,告警规则只能写"失败次数 > 阈值就报"——分不清是网络抖动、对端服务挂了、还是 DNS 出问题。**所有失败被一锅端,就没法分配 oncall 优先级和处置预案。**

加上 `error_type` 之后:

```promql
# 监控"对端响应慢"——通常是网络抖动或对端慢查询
rate(gowatch_check_errors_total{error_type="timeout"}[5m])

# 监控"端口直接挂了"——通常是进程崩溃或部署期
rate(gowatch_check_errors_total{error_type="refused"}[5m])

# 监控"DNS 解析问题"——通常是基础设施层面问题
rate(gowatch_check_errors_total{error_type="dns"}[5m])

# 监控"应用返回错误码"——通常是应用 bug 或后端 5xx
rate(gowatch_check_errors_total{error_type="non_2xx"}[5m])
```

不同维度对应不同的告警严重等级和值班响应——这是从"看得见服务是否健康"升级到"看得见服务为什么不健康"的关键一步。

---

## 🧪 稳定性验证(1 小时 Soak Test)

5 个真实 target,interval 10s,本机连续运行 **1 小时 2 分钟**,共完成 **1825 次健康检查**(5 × 365,均匀无遗漏)。

### 资源稳定性

| 指标 | 启动 3min57s | 运行 1h2m | 变化 | 结论 |
|------|-------------|-----------|------|------|
| `go_goroutines` | 23 | **23** | **0** | ✅ 无 goroutine 泄漏 |
| `heap_inuse_bytes` | 4.19 MB | 4.09 MB | -2% | ✅ heap 稳定甚至轻微下降 |
| `process_resident_memory` | 23.7 MB | ~25 MB | <5% | ✅ 常驻内存稳定 |
| `gowatch_check_total` (per target) | 23 | 365 | +342 | ✅ 调度公平,5 个 target 完全均匀 |

### 三种失败模式分类

5 个 target 覆盖了不同层的失败,验证 checker 路径和 `error_type` 归类逻辑:

| 失败类型 | 示例 target | Latency | `error_type` | 错误链特征 |
|---------|-----------|---------|-------------|----------|
| **应用层超时** | github-home / google-home(出境网络不稳定) | ~3000ms | `timeout` | `*url.Error: context deadline exceeded` |
| **传输层 RST** | k8s-api(127.0.0.1:6443 端口未监听) | < 1ms | `refused` | `*net.OpError: connection refused` |
| **正常响应** | baidu-home | ~150ms | (无错误) | - |

> 注:1 小时 soak test 是在 `error_type` 维度上线**之前**跑的,数据反映的是底层资源稳定性。`error_type` 维度的长跑验证排在 v1.x backlog,会基于新维度做一次对比 soak,把每种错误类型的分布画成 Grafana 面板。

---

## 🎯 设计决策

### 为什么 Checker 是接口而不是函数?

```go
type Checker interface {
    Check(ctx context.Context) Result
}
```

接口最小化(只一个方法),HTTP 和 TCP 是两个独立实现。新增 ICMP / gRPC / DNS 探测时无需改 scheduler,只要实现接口、注册到 factory 即可。

### 为什么 Check 返回 `Result` 而不是 `(Result, error)`?

业务上"探测失败"不是程序错误,**它就是 Result 的一种合法状态**。把 Error 字段塞进 Result 里,调用方拿到一个值就能知道全部信息,不用 `if err != nil` 判断两次。同时 Result 还带上 `ErrorType` 字段,让上游(metrics / API)能按错误类型分桶,而不需要每个消费者都重写一遍归类逻辑。

### 为什么 `ErrorType` 在 `Checker` 里赋值,而不是在 metrics 里现场归类?

错误归类逻辑(`ClassifyNetErr`)放在 checker 包里,**只走一次**。Result 流过 storage、API、metrics 三个消费者,每个都直接读 `r.ErrorType`,不重复计算。**单一来源**避免归类逻辑在多个地方走偏不一致。

### 为什么 worker pool 用固定数量而不是 per-target 一个 goroutine?

- 假设 1000 个 target,per-target 模式起 1000 个 goroutine 既浪费也不好控制
- 固定 5 个 worker 在 IO 密集场景已经够(每个 worker 90% 时间在 IO 等待)
- 真要扩,改一个数字就行,不用改架构

### 为什么 collector 单独 goroutine 写库?

避免 N 个 worker 同时往 SQLite 写造成锁竞争。worker 把结果丢 channel 就回去探测下一个,collector 串行消费 → SQLite 写入 + Prometheus 指标更新。**IO 与计算解耦**,数据库不会成为并发瓶颈。

### Graceful Shutdown 的关闭顺序

```go
ctx.Done()                   // 1. 收到信号
  → server.Shutdown(ctx)     // 2. HTTP server 不再接新连接,在跑的请求继续
  → close(jobs)              // 3. scheduler 内部:workers 自然退出
  → wg.Wait()                // 4. 等所有 workers 收尾
  → close(results)           // 5. collector 看到 channel 关闭后退出
  → <-poolDone               // 6. main 等 scheduler 完全结束
  → defer store.Close()      // 7. 最后关数据库
```

**关键不变量**:数据库不会在还有数据要写的时候被关闭。

---

## 🔬 联调发现 / 工程反思

GoWatch 各模块单元测试都过,但完成端到端联调时发现了几个 mock 测试遮蔽的问题。这些问题本身已修或已记入 backlog,放在这里是因为**"发现的过程"本身**就是这个项目最值钱的一部分。

### 单元测试覆盖 ≠ 真实生产路径覆盖

`ClassifyNetErr` 的单元测试用 `&net.DNSError{IsNotFound: true}` 这种"干净"的错误验证分类逻辑,PASS。但端到端联调时发现:针对不存在的域名(如 `http://xxx-not-exist-12345.com`)发起请求,在 Windows + 国内 DNS 环境下,实际产生的错误是 `*url.Error: context deadline exceeded`,而**不是** `*net.DNSError`。

**根因**:Go 在 Windows 上默认走 cgo resolver 调系统 DNS API。系统 resolver 对未知域名不一定立刻返回 NXDOMAIN——可能在重试上游、可能 ISP 在拦截、可能在递归 search list——总之"卡住"了,然后被上层 ctx 超时打断,错误链最深处变成了 `context.DeadlineExceeded`,内层 `*net.DNSError` 根本没机会出现。

**启示**:生产监控不能只靠"应用层捕获到 DNS error"来推断 DNS 健康。成熟的 SRE 系统会**单独 probe DNS 解析延迟**(CoreDNS 自身指标、定时 dig 关键域名)。GoWatch 把这种"DNS 卡住超时"归到 `timeout` 桶是有意的——它**如实反映应用视角看到的现象**,不假装是干净的 NXDOMAIN。

### `errors.As` 能穿透,`errors.Is` 不一定能

延伸思考:`errors.As` 沿错误链查找匹配类型的能力是稳定的。但 `errors.Is` 的命中行为依赖每层 `Is()` 方法的实现——`*url.Error` → `*net.OpError` → `*net.DNSError` 这条链上每一层都可能微调 `Is()` 的行为,导致同样的错误在 mock 和真实环境中走不同分类分支。

写错误归类代码时,**真实错误链需要真实拨号产生,mock 永远复刻不全**。

### 联调暴露的 config 默认值缺失

端到端跑的时候发现某些 target 的 latency=0、错误是立刻返回的 `context deadline exceeded`,根因是 config.yaml 里那条 target 漏了 `timeout` 字段,`time.Duration` 零值传给 `context.WithTimeout(ctx, 0)` 让 ctx 立刻过期。配置里漏一个字段不应该让整个 target 无法工作——这条已记入 v1.x backlog,会在 `LoadFromFile` 里加默认值兜底。

---

## 🛠️ 开发

### 目录结构

```
gowatch/
├── cmd/gowatch/main.go              # 入口:flag、模式分派、生命周期管理
├── internal/
│   ├── config/                      # YAML 配置加载
│   ├── checker/                     # Checker 接口 + HTTP/TCP 实现 + 错误分类
│   ├── storage/                     # SQLite 封装 + 单元测试
│   ├── api/                         # HTTP Handler + Web UI(embed.FS)
│   ├── scheduler/                   # Worker Pool + Ticker + Collector
│   └── metrics/                     # Prometheus 指标定义
├── config.yaml                      # 配置示例
├── go.mod
└── README.md
```

### 跑测试

```bash
# 单独跑某个包
go test -v ./internal/checker/      # checker 接口 + 错误分类
go test -v ./internal/storage/      # SQLite(:memory: 模式)

# 全量
go test -v ./...

# 带覆盖率
go test -cover ./...
```

### 接 Prometheus + Grafana(可选)

`prometheus.yml` 加抓取配置:

```yaml
scrape_configs:
  - job_name: 'gowatch'
    static_configs:
      - targets: ['localhost:8080']
```

然后在 Grafana 里画:

- `gowatch_target_up` — 当前每个 target 是否在线
- `sum by (error_type) (rate(gowatch_check_errors_total[5m]))` — 错误类型分布
- `histogram_quantile(0.99, rate(gowatch_check_latency_seconds_bucket[5m]))` — P99 延迟

---

## 📝 路线

### v1 ✅ Done

- [x] config / checker / storage / api / scheduler 五大核心包
- [x] CLI 多模式 + Web UI(embed.FS 单二进制)
- [x] 错误分类 `error_type`(timeout / refused / dns / non_2xx / other)+ Prometheus label 维度
- [x] Graceful shutdown + 关闭顺序保证
- [x] 1 小时 soak test:无 goroutine 泄漏、内存稳定、调度公平

### v1.x 🐛 Backlog(已知 + 计划修复)

- [ ] **config `Timeout` 字段加默认值兜底**——漏配置时 ctx 立刻过期 bug
- [ ] **`ClassifyNetErr` 真实包装链集成测试**——用 `httptest` 触发真实 dial 失败,覆盖 mock 漏掉的路径
- [ ] **`error_type` 维度的长跑验证**——基于新维度重做 soak test,出 Grafana 面板
- [ ] non_2xx 端到端验证(目前依赖 unit test,等 staging 环境补)

### v2 🚧 Next

- [ ] config 热加载(fsnotify)
- [ ] 告警规则引擎(基于 `error_type` + 阈值,触发 webhook / 邮件)
- [ ] 多实例部署 + etcd 协调,避免重复探测

### v3 🔮 长期

- [ ] K8s 集成:从 Service / Endpoints 自动发现 target
- [ ] 接入 OpenTelemetry,支持分布式追踪
- [ ] eBPF 探针(可选),从内核层观测 TCP 状态

---

## 📄 License

[MIT](LICENSE)
