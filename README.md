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
- **配置热加载(fsnotify)** — 监听 config.yaml 变化,200ms debounce 防抖,下一轮 dispatch 切换到新 checker;watcher 起不来时降级为静态配置,不阻塞主流程
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

### 配置热加载链路(v2 新增)

```
config.yaml 修改 → fsnotify Event → debounce 200ms → Load + Validate
                                                          │
                                                          ▼
                                                    reloadCh (buffer=1)
                                                          │
                                                          ▼
                                                    scheduler.Reload
                                                          │
                                                          ▼
                                              下一轮 dispatch 应用新 checker
```

**关键设计:**

- **debounce 防抖**: 编辑器一次保存可能触发多次 fsnotify 事件,200ms 内的连续事件合并成一次 reload
- **reloadCh buffer=1 + 覆盖语义**: 如果 reload 比 scheduler 处理快,新 cfg 覆盖旧 cfg,scheduler 永远拿到最新一份
- **加载失败不切换**: LoadFromFile 失败仅打日志,scheduler 继续用旧配置,避免一次错误配置把监控打挂
- **优雅降级**: watcher 启动失败(权限 / inotify 资源等),主流程照常启动,只是失去热加载能力

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

修改后保存,无需重启;watcher 检测到变化后会触发 reload,下一轮 dispatch 自动用新配置。

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

2026/05/07 11:50:32 config: watching for changes...
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

---

## 🛠️ 开发

### 目录结构

```
gowatch/
├── cmd/gowatch/main.go              # 入口:flag、模式分派、生命周期管理
├── internal/
│   ├── config/                      # YAML 配置加载 + fsnotify 热加载
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
- [ ] **config 加载时 URL schema 校验**——type=http 校验 http/https 前缀,type=tcp 校验 host:port;否则配错时 latency=0s 看起来像服务挂,实际是配置错
- [ ] **fsnotify watcher 改为监听父目录**——当前监听文件本身,在 vim / VSCode 等 atomic save 编辑器下首次保存后 watcher 失效;改为监听父目录 + filter base name 可解决

### v2 🚧 In Progress

- [x] **config 热加载(fsnotify)** — debounce 防抖 + 优雅降级 + reload 不停机切换
- [ ] **告警规则引擎** — 基于 `error_type` + 阈值触发 webhook / 邮件,复用 v1 已有的错误分桶维度
- [ ] **多实例部署 + etcd 协调** — 避免重复探测,主备切换

### v3 🔮 长期

- [ ] K8s 集成:从 Service / Endpoints 自动发现 target
- [ ] 接入 OpenTelemetry,支持分布式追踪
- [ ] eBPF 探针(可选),从内核层观测 TCP 状态

---

## 📄 License

[MIT](LICENSE)
