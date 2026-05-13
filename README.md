# GoWatch

> 用 Go 写的轻量探活监控服务 —— 配置驱动、并发探测、错误分类、SQLite 持久化、HTTP API、Web 面板、Prometheus 指标、**告警规则引擎**。单二进制、无 CGO、跨平台。

![Web 面板截图](image-2.png)

GoWatch 周期性地对一组 HTTP / TCP 目标做健康检查，把结果按**错误类型**分桶并写入 SQLite，通过 HTTP API + Web UI + Prometheus `/metrics` 端点暴露。**v2 引入告警规则引擎**：基于 v1 已有的 `error_type` 维度做多种语义的规则匹配，命中后走 webhook 通知并落库。整个服务是常驻进程，支持优雅关闭，可以放心 Ctrl+C 或 SIGTERM。

---

## 特性

- **多协议健康检查** — HTTP(S) 状态码 / TCP 端口连通性，通过 `Checker` 接口扩展
- **并发 Worker Pool 调度** — 固定 worker + Ticker 周期触发 + collector 解耦写库
- **错误分类(`error_type`)** — 把网络层错误归类为 `timeout` / `refused` / `dns` / `non_2xx` / `other`,Prometheus 指标按错误类型分桶,**让告警规则能区分"网络抖动"和"服务挂了"**
- **告警规则引擎(v2 新增)** — 三种语义的规则匹配 + cooldown 抑制 + webhook 通知 + 持久化,**异步发起、永不阻塞探测主链路**
- **SQLite 持久化** — 纯 Go 驱动 (`modernc.org/sqlite`),无 CGO,跨平台编译零负担
- **REST API + Web UI** — 实时状态、按 target 查历史、最近告警列表,前端 5 秒自动刷新
- **Prometheus 集成** — `/metrics` 端点暴露 counter / histogram / gauge,可直接接 Grafana
- **Graceful Shutdown** — `signal.NotifyContext` + `server.Shutdown` + scheduler done channel 三步收尾,不丢数据
- **配置热加载(fsnotify)** — 监听 config.yaml 变化,200ms debounce 防抖,下一轮 dispatch 切换到新 checker
- **CLI 工具化** — 同一个二进制支持服务模式、查询历史、查看最新状态三种用法

---

## 架构

```
                 主 goroutine (scheduler.Run)
                          │
                          │ Ticker 每 N 秒触发 dispatch
                          ▼
                    jobs channel ──┬─→ worker 1 ─┐
                                   ├─→ worker 2 ─┤
                                   ├─→ worker 3 ─┼─→ results channel ─→ collector ─┬─→ SQLite
                                   ├─→ worker 4 ─┤                                 ├─→ Prometheus 指标更新
                                   └─→ worker 5 ─┘                                 └─→ alert.Evaluator.OnResult
                                                                                       │
                                                                                       ▼
                                                                            Window (per-target ring) → Rule.Match
                                                                                       │
                                                                                       ▼ 命中且未在 cooldown
                                                                            go fire(rule, event)
                                                                                       │
                                                                                       ├─→ Webhook(POST,5xx 重试 1 次)
                                                                                       └─→ emit channel → SaveAlert

ctx.Done() ─→ close(jobs) ─→ workers 退出 ─→ close(results) ─→ collector 退出 ─→ store.Close
```

**三层 goroutine 职责分离:**
- **主 goroutine** — Ticker 派活、监听 ctx 信号、协调关闭顺序
- **Worker Pool** — 并发跑 `Checker.Check(ctx)`,IO 密集场景天然受益于并发,每个 worker 用 per-target 独立 ctx 避免互相拖累
- **Collector** — 单独 goroutine 串行写库 + 同步更新 Prometheus 指标 + 调用 `evaluator.OnResult`,把 IO 与指标写入和 worker 解耦

### 告警引擎链路(v2 新增)

```
collector ──→ evaluator.OnResult(r)
                  │
                  ├─→ window.Push(r)              // per-target ring buffer,cap=50
                  │
                  └─→ for rule in rules:
                          if rule.Target != "*" && rule.Target != r.Target: continue
                          snap = window.Snapshot(r.Target)         // 独立副本,避免锁外修改
                          hit, reason = rule.Match(snap)
                          if hit && suppressor.Allow(rule,target,cooldown):
                              go fire(rule, event)                 // 异步,不阻塞 collector
                                  ├─→ webhook POST(5s timeout, 5xx 重试 1 次, 4xx 不重试)
                                  └─→ emit ch → store.SaveAlert
```

**关键设计:**

- **`OnResult` 永不阻塞主链路** — Window.Push 与 suppressor.Allow 是 ms 级内存操作;真正可能慢的 webhook 全部丢进 `go fire(...)`,网络抖动绝对不会拖死 collector
- **复用 v1 的 `error_type` 维度** — `consecutive_error_type` 规则可以做到"连续 2 次 dns 失败才告警",这是单纯 status 维度做不到的判断:DNS 抖动 vs 服务真挂应该是两种告警,严重程度也不同
- **Suppressor 是 (rule, target) 二元 key** — 同一规则在不同 target 上的 cooldown 互相独立,一个 target 在抑制窗口内不会让别的 target 也被静默
- **Window cap=50 ring buffer** — 满了从尾部裁剪,保证内存有上界;`Snapshot` 返回独立副本,有专门的 unit test 守住这个契约,防止后续优化误改成共享 slice
- **Webhook 重试策略** — 5xx 视为服务端临时故障,500ms 后重试 1 次;4xx 是客户端语义错误(URL 写错、payload 不合规),重试也没用,直接返回;网络错误按 5xx 处理
- **emit channel buffer=100 + default drop** — `SaveAlert` 慢于产出时丢日志并 drop,不阻塞 fire goroutine;告警是辅助信号,不能让告警写库拖垮探测
- **跨重启 Suppressor 清零(已知)** — 进程重启后第一次命中会立刻发出,即使上一进程刚刚发过;v2.x 计划落到 SQLite

---

## 快速开始

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

在项目根目录创建 `config.yaml`(探测目标):

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

  - name: dns-fail-test
    type: http
    url: http://xxx-not-exist-12345.com
    timeout: 3s
```

通过 `type` 字段显式指定探测协议(`http` 或 `tcp`),scheduler 会构造对应的 Checker 实例。要新增 ICMP / gRPC / DNS 等探测方式,只需实现 `Checker` 接口并在 `applyConfig` 的 switch 里加一个 case。

修改后保存,无需重启;watcher 检测到变化后会触发 reload,下一轮 dispatch 自动用新配置。

### 告警规则(可选)

在根目录创建 `alerts.yaml`,即可启用告警引擎:

```yaml
rules:
  # 单 target 规则:GitHub 连续 3 次 down 才告警,避免单次网络抖动误报
  - name: github-flapping
    target: github-home
    type: consecutive_status
    status: down
    threshold: 3
    cooldown: 5m
    webhook: http://localhost:9999/test-webhook

  # 通配规则:任何 target 连续 2 次 DNS 错误,说明大概率是 DNS 真挂了
  - name: dns-broken
    target: "*"
    type: consecutive_error_type
    error_type: dns
    threshold: 2
    cooldown: 10m
    webhook: http://localhost:9999/test-webhook

  # 错误率规则:最近 5 分钟错误率 ≥50% 触发,适合容量告警 / 部分降级场景
  - name: high-error-rate
    target: "*"
    type: error_rate_window
    threshold: 50
    window: 5m
    cooldown: 10m
    webhook: http://localhost:9999/test-webhook
```

**`alerts.yaml` 文件不存在或加载失败,告警引擎自动关闭,不影响主服务启动。**

### 启动服务

```bash
./gowatch
```

默认行为:
- 加载 `./config.yaml`(必需) + `./alerts.yaml`(可选)
- 数据库写入 `./gowatch.db`(同一个库,checks 表 + alerts 表)
- HTTP 服务监听 `:8080`
- Worker 数 5,探测周期 10 秒
- Ctrl+C / SIGTERM 优雅退出

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

## API

打开浏览器访问 `http://localhost:8080` 看 Web 面板(包含目标状态表 + 最近告警表,5 秒自动刷新),或者直接调 API:

### `GET /api/health`

```json
{ "status": "ok", "uptime": "1h2m52.9687301s" }
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
    "timestamp": "2026-05-13T03:50:42Z"
  },
  {
    "target": "github-home",
    "status": "down",
    "latency_ms": 3000.079,
    "error": "Get \"https://www.github.com\": context deadline exceeded",
    "timestamp": "2026-05-13T03:50:45Z"
  }
]
```

### `GET /api/history?target=<name>&limit=<n>`

返回指定 target 的历史记录(`limit` 默认 20、上限 1000,按时间倒序)。

### `GET /api/alerts?limit=<n>`

返回最近触发的告警事件(默认 50、上限 1000,按 `fired_at` 倒序):

```json
[
  {
    "rule_name": "dns-broken",
    "target": "dns-fail-test",
    "fired_at": "2026-05-13T13:55:11Z",
    "reason": "连续2次error_type=dns"
  },
  {
    "rule_name": "high-error-rate",
    "target": "non2xx-test",
    "fired_at": "2026-05-13T13:55:02Z",
    "reason": "最近5m0s错误率100%(errs=1/total=1)超过阈值50%"
  }
]
```

### `GET /metrics`

Prometheus 兼容的指标端点(详见下一节)。

---

## Prometheus 指标

| 指标 | 类型 | Labels | 说明 |
|------|------|--------|------|
| `gowatch_check_total` | counter | `target`, `status` | 累计检查次数,按 up/down 分桶 |
| `gowatch_check_errors_total` | counter | `target`, **`error_type`** | 累计错误次数,**按错误类型分桶** |
| `gowatch_check_latency_seconds` | histogram | `target` | 检查耗时分布 |
| `gowatch_target_up` | gauge | `target` | 当前是否 up(1=up, 0=down) |
| 标准 Go runtime 指标 | - | - | goroutine 数、heap、GC 等 |

> 告警事件目前只持久化到 SQLite,没有对应的 Prometheus 指标。如需配 Alertmanager 双路告警,可以基于 `gowatch_check_errors_total{error_type="..."}` 自行写 PromQL,效果与内置规则等价。

---

## 告警规则类型详解

### 1. `consecutive_status` — 连续 N 次 status 命中

```yaml
type: consecutive_status
status: down        # 或 up
threshold: 3
```

**语义:** 最近 N 次结果**全部**是指定 status 才触发。
**用法:** 最朴素的"3 次都挂了再叫"规则,避免单次网络抖动误报。
**注意:** 中间夹一次正常就清零,不是滑动窗口。

### 2. `consecutive_error_type` — 连续 N 次相同错误类型

```yaml
type: consecutive_error_type
error_type: timeout   # timeout / refused / dns / non_2xx / other
threshold: 3
```

**语义:** 最近 N 次错误**类型完全一致**才触发。
**用法:** 区分"网络抖动型抖动"和"特定故障模式"。比如:
- 连续 5 次 `timeout` → 大概率链路慢/丢包
- 连续 3 次 `dns` → DNS 服务真挂了
- 连续 3 次 `refused` → 后端服务进程没了

这是单纯看 status 做不到的细分,**告警噪音可以根据故障类型分通道分级**。

### 3. `error_rate_window` — 时间窗口内错误率

```yaml
type: error_rate_window
threshold: 50    # 百分比 0-100
window: 5m
```

**语义:** 在 `window` 时间窗口内,错误次数 / 总检测次数 ≥ `threshold`% 触发。
**用法:** 适合做"部分降级"告警 —— 不是连续挂,但成功率明显跌了。
**注意:**
- `threshold` 是百分比(0-100),不是 0-1 的小数
- 窗口外的记录不计入分母
- 窗口内一条记录都没有(数据稀疏 / 新启动)直接判定为不命中

---

## 开发

### 目录结构

```
gowatch/
├── cmd/gowatch/main.go          # 入口:flag、模式分派、生命周期管理、装配 evaluator
├── internal/
│   ├── config/                  # YAML 配置加载 + fsnotify 热加载
│   ├── checker/                 # Checker 接口 + HTTP/TCP 实现 + 错误分类
│   ├── storage/                 # SQLite 封装(checks + alerts 两张表)
│   ├── api/                     # HTTP Handler + Web UI(embed.FS)
│   ├── scheduler/               # Worker Pool + Ticker + Collector
│   ├── metrics/                 # Prometheus 指标定义
│   └── alert/                   # 告警引擎:rule / matchers / window / suppressor /
│                                #          notifier / evaluator
├── config.yaml                  # 探测目标配置示例
├── alerts.yaml                  # 告警规则配置示例
├── go.mod
└── README.md
```

### 跑测试

```bash
# 单独跑某个包
go test -v ./internal/checker/   # checker 接口 + 错误分类
go test -v ./internal/storage/   # SQLite(:memory: 模式)
go test -v ./internal/alert/     # 规则匹配 + Window + Suppressor + Notifier + 集成 + 故障注入

# 全量
go test -v ./...

# 带覆盖率
go test -cover ./...
```

**`internal/alert` 测试覆盖的重点:**

- `matcher_test.go` — 三种规则各自的命中 / 不命中边界,包括 `Threshold=0` / 空 recent / 旧数据被窗口过滤这些容易漏掉的分支
- `window_test.go` — 关键契约:`Snapshot` 必须返回独立副本(调用方修改不影响内部),专门有一个 case 守这一行
- `suppressor_test.go` — cooldown 内拦截、不同 target 独立、`Cooldown=0` 永远放行
- `notifier_test.go` — 5xx 重试 1 次、4xx 不重试、success 路径数据透传
- `integration_test.go` — 用 `httptest.Server` 模拟 webhook,跑完整 OnResult → fire → webhook 链路 + cooldown 抑制
- `failure_test.go` — webhook 持续 5xx / 持续 timeout 时,**`OnResult` 主调用绝不被阻塞**(异步契约的回归测试)

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

## 路线

### v1 — Done

- [x] config / checker / storage / api / scheduler 五大核心包
- [x] CLI 多模式 + Web UI(embed.FS 单二进制)
- [x] 错误分类 `error_type`(timeout / refused / dns / non_2xx / other)+ Prometheus label 维度
- [x] Graceful shutdown + 关闭顺序保证
- [x] 1 小时 soak test:无 goroutine 泄漏、内存稳定、调度公平

### v2 — Done

- [x] **config 热加载(fsnotify)** — debounce 防抖 + 优雅降级 + reload 不停机切换
- [x] **告警规则引擎** — 三种规则语义 + Window + Suppressor + Webhook(含重试) + SQLite 持久化 + Web UI 展示
- [x] **告警系统的异步契约** — `OnResult` 永不阻塞 collector,webhook 故障(5xx/timeout)有专门的回归测试守护

### v2.x — Backlog(已知 + 计划修复)

- [ ] **Suppressor 状态持久化** — 当前是 in-memory,跨重启清零;落 SQLite 后能避免重启风暴
- [ ] **`alerts.yaml` 热加载** — 复用 config watcher 的 debounce 思路,告警规则也支持运行时修改
- [ ] **`fsnotify` watcher 改为监听父目录** — 当前监听文件本身,在 vim / VSCode 等 atomic save 编辑器下首次保存后 watcher 失效;改为监听父目录 + filter base name 可解决
- [ ] **config 加载时 URL schema 校验** — type=http 校验 http/https 前缀,type=tcp 校验 host:port;否则配错时 latency=0s 看起来像服务挂,实际是配置错
- [ ] **`ClassifyNetErr` 真实包装链集成测试** — 用 `httptest` 触发真实 dial 失败,覆盖 mock 漏掉的路径
- [ ] **告警去重 / 抑制升级** — 当前是 (rule, target) 维度 cooldown;复杂场景可能要"先收敛再发"(N 分钟批量一条)
- [ ] **告警通知通道扩展** — 当前只支持 webhook;`Notifier` 接口已经抽出来,加 Email / 钉钉 / 飞书只是新加一个实现

### v3 — 长期

- [ ] 多实例部署 + etcd 协调,避免重复探测,主备切换
- [ ] K8s 集成:从 Service / Endpoints 自动发现 target
- [ ] 接入 OpenTelemetry,支持分布式追踪
- [ ] eBPF 探针(可选),从内核层观测 TCP 状态

---

## 📄 License

[MIT](LICENSE)
