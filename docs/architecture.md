# GoWatch 架构与设计取舍

> 本文承接 README 的架构章节,记录完整数据流、集群选主细节与各项设计取舍。快速上手请回 [README](../README.md)。

## 单机模式数据流

```
                 主 goroutine (scheduler.Run)
                          │
                          │ Ticker 每 N 秒触发 dispatch
                          ▼
                    jobs channel ──┬─→ worker 1 ─┐  每个 worker:
                                   ├─→ worker 2 ─┤   1. c.Check(ctx) (per-target 超时)
                                   ├─→ worker 3 ─┼─  2. metrics.Record(...)
                                   ├─→ worker 4 ─┤   3. cert 类型额外 set SSLCertExpiryDays
                                   └─→ worker 5 ─┘   4. result → results channel
                                                              │
                                                              ▼
                                                          collector ─┬─→ SQLite (store.Save)
                                                                     └─→ alert.Evaluator.OnResult
                                                                             │
                                                                             ▼
                                                                  Window (per-target ring) → Rule.Match
                                                                             │
                                                                             ▼ 命中且未在 cooldown
                                                                  go fire(rule, event)
                                                                             │
                                                                             ├─→ Webhook(POST,5xx 重试 1 次)
                                                                             └─→ emit channel → SaveAlert / AIOps 旁路

ctx.Done() ─→ close(jobs) ─→ workers 退出 ─→ close(results) ─→ collector 退出 ─→ store.Close
```

**三层 goroutine 职责分离:**

- **主 goroutine** — Ticker 派活、监听 ctx 信号、协调关闭顺序
- **Worker Pool** — 并发跑 `Checker.Check(ctx)`,IO 密集场景天然受益于并发,每个 worker 用 per-target 独立 ctx 避免互相拖累;Prometheus 指标在 worker 里同步更新(每次探测即时 `metrics.Record`,cert 目标额外 set `gowatch_ssl_cert_expiry_days`),指标反映每一次探测,不依赖后续写库
- **Collector** — 单独 goroutine 串行写库(`store.Save`)+ 调用 `evaluator.OnResult`,把 SQLite IO 与 worker 解耦

## 集群模式(`--cluster`)

scheduler 不变,外面套一层 `cluster.Leader` 来负责"谁跑"。Leader 用回调式 API,scheduler 不知道自己是不是 leader,只是被传入了一个 ctx:

```
                ┌─────────────────────────────┐
                │           etcd              │
                │  /gowatch/leader (election) │
                │  /gowatch/suppressor/...    │
                └──────────────┬──────────────┘
                               │
              ┌────────────────┴────────────────┐
              │                                 │
         Instance A                        Instance B
   leader.Run(ctx, onLeader)         leader.Run(ctx, onLeader)
   is_leader = 1                     is_leader = 0
   /api/* 正常服务                    /api/status → 503 (follower)
              │                                 │
              ▼                                 ▼
         Election wins                   Blocked on Campaign
              │                                 │
              ▼                                 │
   LoadSuppressorFromEtcd                       │ wait...
   pool.Run(leaderCtx)                          │
              │                                 │
              │ session.Done()                  │
              │ or ctx.Done()                   │
              ▼                                 ▼
         DEMOTE (cancel leaderCtx,     Election wins (≤15s)
         wait scheduler done ≤5s)              │
         is_leader = 0                          ▼
              │                       LoadSuppressorFromEtcd
              └─→ backoff → CAMPAIGN     pool.Run(leaderCtx)
                  (1s→2s→...→30s)        is_leader = 1
```

**关键设计:**

- **回调式 API:`leader.Run(ctx, onLeader func(ctx))`** — cluster 对 scheduler 完全透明,scheduler 看到的只是"一个 ctx,跑就行,取消就退";上位时 `onLeader` 先 `LoadSuppressorFromEtcd` 回灌抑制状态,再 `pool.Run(leaderCtx)`
- **`LeaderState` 接口统一身份查询** — `IsLeader()` 用 `atomic.Bool`,Run 里上位前置 1、下位后置 0;API 层 / middleware / metric 都读这个接口,单机模式由 `SingleLeader`(恒为 leader)实现,避免到处写 `if clusterMode`
- **scheduler 100% 复用** — 同一份 `pool.Run(ctx)` 代码,单机模式直接调,集群模式被 `leader.Run` 包裹后调;集群上位 = leaderCtx 启动,session 失效 = leaderCtx 取消,scheduler 自然退出
- **fail-fast on startup, backoff at runtime** — 启动期 etcd dial 失败直接报错(配置错应该让人看见);运行期 etcd 抖动用指数退避(1s → 2s → ... ≤ 30s),不让重连风暴打挂 etcd
- **优雅下台 5s 上限** — session 失效或 ctx cancel 时,先取消 leaderCtx 让 scheduler 自己收尾;`select` 等 done channel,5s 后超时强退,不让一个挂掉的 scheduler 阻塞自身退出
- **session TTL 默认 15s** — 心跳 / 5s,容忍 1-2 次丢包;权衡了"切换太快导致 false failover"和"切换太慢导致探测停摆"

### follower 的 HTTP 行为

集群模式下,数据接口经 `RequireLeader` 中间件保护,follower 不直接对外服务陈旧数据:

| 端点 | leader | follower |
|------|--------|----------|
| `/api/status`、`/api/history`、`/api/alerts` | 正常返回 | `503` + `X-GoWatch-Role: follower` + `X-GoWatch-Node-ID: <id>` |
| `/api/cluster/status` | 正常 | 正常(用来查身份) |
| `/api/health`、`/metrics` | 正常 | 正常 |

上游负载均衡可以据此把读流量只打到 leader,或用 `/api/cluster/status` 主动发现当前 leader。

### 接 Alertmanager

GoWatch 自己是被监控对象,无法监控自己的集群整体宕机,生产部署必须接外部 Alertmanager:

```yaml
groups:
  - name: gowatch-cluster
    rules:
      - alert: GoWatchNoLeader
        expr: absent(gowatch_is_leader == 1)
        for: 1m
        annotations:
          summary: "GoWatch 集群无 leader(或全部实例宕机),探测可能已停止"

      - alert: GoWatchSplitBrain
        expr: sum(gowatch_is_leader) > 1
        for: 30s
        annotations:
          summary: "GoWatch 出现多个 leader(疑似脑裂)"

      - alert: GoWatchLeaderFlapping
        expr: sum(changes(gowatch_is_leader[10m])) > 6
        annotations:
          summary: "GoWatch leader 在 10 分钟内频繁切换"
```

> `gowatch_is_leader` 是不带 label 的 gauge,多实例靠 Prometheus 抓取时的 `instance` label 区分。`absent(gowatch_is_leader == 1)` 同时覆盖"实例都在但无 leader"和"实例全挂"两种情况。

## 告警引擎链路

```
collector ──→ evaluator.OnResult(r)
                  │
                  ├─→ window.Push(r)              // per-target ring buffer,cap=50
                  │
                  └─→ for rule in rules:
                          if rule.Target != "*" && rule.Target != r.Target: continue
                          snap = window.Snapshot(r.Target)         // 独立副本,避免锁外修改
                          hit, reason = rule.Match(snap)
                          if hit && suppressor.AllowAndPersist(ctx, etcdCli, rule, target, cooldown):
                              go fire(rule, event)                 // 异步,不阻塞 collector
                                  ├─→ webhook POST(5s timeout, 5xx 重试 1 次, 4xx 不重试)
                                  └─→ emit ch → store.SaveAlert / AIOps analyzer.Submit
```

**关键设计:**

- **`OnResult` 永不阻塞主链路** — Window.Push 与 suppressor 判断是 ms 级内存操作;真正可能慢的 webhook 全部丢进 `go fire(...)`,网络抖动绝对不会拖死 collector
- **复用 `error_type` 维度** — `consecutive_error_type` 规则可以做到"连续 2 次 dns 失败才告警",这是单纯 status 维度做不到的判断;同理 `error_type=cert_expiring` 可以专门为"证书快过期"配规则
- **Suppressor 是 (rule, target) 二元 key** — 同一规则在不同 target 上的 cooldown 互相独立,一个 target 在抑制窗口内不会让别的 target 也被静默
- **Suppressor 跨重启 / 跨 leader 持久化** — `OnResult` 统一调用 `AllowAndPersist`:命中并放行时异步写 etcd(`/gowatch/suppressor/<rule>:<target>` → JSON `{LastFiredAt}`,不阻塞探测);leader 上位时 `LoadFromEtcd` 把所有 (rule, target) 的最近触发时间回灌内存。`etcdCli == nil`(单机模式 / 集群启动降级)时自动退回纯内存 `Allow`,无分支侵入
- **Window cap=50 ring buffer** — 满了从尾部裁剪,保证内存有上界;`Snapshot` 返回独立副本,有专门的 unit test 守住这个契约
- **Webhook 重试策略** — 5xx 视为服务端临时故障,500ms 后重试 1 次;4xx 是客户端语义错误(URL 写错、payload 不合规),重试也没用,直接返回;网络错误按 5xx 处理
- **emit channel buffer=100 + default drop** — `SaveAlert` 慢于产出时丢日志并 drop,不阻塞 fire goroutine

## 告警规则类型详解

### 1. `consecutive_status` — 连续 N 次 status 命中

```yaml
type: consecutive_status
status: down        # 或 up
threshold: 3
```

**语义:** 最近 N 次结果全部是指定 status 才触发。
**用法:** 最朴素的"3 次都挂了再叫"规则,避免单次网络抖动误报。
**注意:** 中间夹一次正常就清零,不是滑动窗口。

### 2. `consecutive_error_type` — 连续 N 次相同错误类型

```yaml
type: consecutive_error_type
error_type: timeout   # timeout / refused / dns / non_2xx / cert_expiring / other
threshold: 3
```

**语义:** 最近 N 次错误类型完全一致才触发。
**用法:** 区分"网络抖动"和"特定故障模式":连续 5 次 `timeout` → 大概率链路慢/丢包;连续 3 次 `dns` → DNS 服务真挂了;连续 3 次 `refused` → 后端进程没了;连续 1 次 `cert_expiring` → 证书进入预警期(配 `threshold: 1` + 长 cooldown 即可)。

### 3. `error_rate_window` — 时间窗口内错误率

```yaml
type: error_rate_window
threshold: 50    # 百分比 0-100
window: 5m
```

**语义:** 在 `window` 时间窗口内,错误次数 / 总检测次数 ≥ `threshold`% 触发。
**用法:** 适合做"部分降级"告警——不是连续挂,但成功率明显跌了。

## 证书过期监控

`cert` 检查类型把 SSL/TLS 证书纳入和 HTTP/TCP 同一套调度链路。检查逻辑(`pkg/checker/cert.go`):

1. `tls.Dialer.DialContext` 完成 TLS 握手(默认校验证书链 + hostname);握手失败按网络错误处理,`error_type` 走 `ClassifyNetErr`
2. 取对端叶子证书 `PeerCertificates[0]`,算 `NotAfter` 距今的剩余天数
3. 剩余天数 `< cert_warn_days` → `status=down`、`error_type=cert_expiring`
4. 否则 → `status=up`;无论 up/down,剩余天数都写进 `Result.ExpiryDays` 并上报到 `gowatch_ssl_cert_expiry_days` 指标

两种告警路径:走 GoWatch 自己的告警引擎(`consecutive_error_type` + `cert_expiring`),或推荐直接在 Alertmanager 对 `gowatch_ssl_cert_expiry_days` 设阈值——能表达"剩余天数 < N"的连续区间,比 up/down 二值更精细:

```yaml
groups:
  - name: gowatch-ssl
    rules:
      - alert: SSLCertExpiringSoon
        expr: gowatch_ssl_cert_expiry_days < 14
        for: 10m
        annotations:
          summary: "{{ $labels.target }} 证书剩余 {{ $value }} 天,即将过期"
      - alert: SSLCertExpired
        expr: gowatch_ssl_cert_expiry_days < 0
        annotations:
          summary: "{{ $labels.target }} 证书已过期"
```

## pkg/checker 公开包

探测内核(`Checker` 接口、HTTP / TCP / Cert 三个实现、`error_type` 错误分类)以公开包形式暴露在 `pkg/checker`,任何外部 Go module 都可以直接消费;[gowatch-operator](https://github.com/jiayu113/gowatch-operator) 经 versioned Go module(`v1.2.0`)真实跨仓复用本包:

```go
import "github.com/jiayu113/gowatch/pkg/checker"

c := checker.NewHTTPChecker(checker.Target{
    Name:    "demo",
    Type:    "http",
    URL:     "http://10.0.0.1:80/healthz",
    Timeout: 3 * time.Second,
})
r := c.Check(ctx) // Result{Status, Latency, Error, ErrorType, ...}
```

| 标识符 | 说明 |
|--------|------|
| `Checker` 接口 | `Check(ctx) Result`,所有探测器的统一抽象 |
| `Target` | 探测目标描述(名称 / 类型 / URL / 超时 / 证书阈值) |
| `Result` | 单次探测结果(状态 / 延迟 / 错误 / 错误类型 / 证书剩余天数) |
| `NewHTTPChecker` / `TCPChecker` / `CertChecker` | 三个具体实现 |
| `ClassifyNetErr` | 网络错误归类:`timeout` / `refused` / `dns` / `other` |
| `ErrType*` 常量 | 错误类型字符串常量(含 `non_2xx`、`cert_expiring`) |

## 目录结构

```
gowatch/
├── cmd/gowatch/main.go          # 入口:flag、模式分派、生命周期管理、装配 evaluator + cluster + AIOps
├── pkg/
│   └── checker/                 # ★ 公开探测内核:Checker 接口 + HTTP/TCP/Cert 实现 + 错误分类
├── internal/
│   ├── config/                  # YAML 配置加载(http/tcp/cert 校验)+ fsnotify 热加载
│   ├── storage/                 # SQLite 封装(checks + alerts 两张表)
│   ├── api/                     # HTTP Handler + RequireLeader middleware + Web UI(embed.FS)
│   ├── scheduler/               # Worker Pool + Ticker + Collector
│   ├── metrics/                 # Prometheus 指标定义
│   ├── alert/                   # 告警引擎:rule / matchers / window / suppressor / notifier / evaluator
│   ├── cluster/                 # etcd 选主:session / leader / backoff / LeaderState
│   └── aiops/                   # AIOps 旁路诊断:config / context / llm / analyzer(设计见 docs/aiops.md)
├── docs/                        # aiops.md 设计文档 + 本文
├── config.yaml                  # 探测目标配置示例
└── alerts.yaml                  # 告警规则配置示例
```

## 测试覆盖重点

**`pkg/checker`:**

- `http_test.go` — 2xx success / 5xx → non_2xx / ctx 超时 → timeout / 真实 ECONNREFUSED(起服务后立刻关端口,触发真实拒绝连接)
- `errtype_test.go` — `ClassifyNetErr` 对 nil / deadline(含 wrap)/ DNSError / errno-refused / 字符串兜底的分类
- `cert_test.go` — 用 `httptest.NewTLSServer` 验有效证书 → up;用自签证书(5 天后过期)+ `tls.Listen` 验快过期 → down + `cert_expiring`;TLSConfig 注入自定义 RootCAs,测试不依赖真实公网证书

**`internal/cluster`:**

- embedded etcd 套件(`go.etcd.io/etcd/server/v3/embed`,无需外部容器):Campaign happy path / 双实例 failover / `Revoke` lease 模拟 session 死亡 / ctx cancel 优雅退出
- dockertest 真实容器 e2e(`//go:build dockertest` 隔离,不进默认套件,docker 不可用时 `t.Skipf`):真实 etcd 上的 leader 切换;冻结容器模拟网络黑洞,验证 demote → backoff → 解冻后自动重连并抢回 leader

**`internal/alert`:**

- `matcher_test.go` — 三种规则的命中 / 不命中边界,含 `Threshold=0`、空 recent、旧数据被窗口过滤等易漏分支
- `window_test.go` — `Snapshot` 必须返回独立副本的契约测试
- `suppressor_test.go` — cooldown 拦截、不同 target 独立;embedded etcd 模拟"持久化 → 重启 → 加载 → cooldown 仍生效"
- `failure_test.go` — webhook 持续 5xx / 持续 timeout 时,`OnResult` 主调用绝不被阻塞(异步契约回归测试)

**`internal/aiops`:**

- `context_test.go` — 诊断上下文构建与 prompt 渲染
- `analyzer_test.go` — 三闸门(冷却 / 日上限 / 熔断)与 JSONL 落盘,mock LLM 驱动

## Grafana 常用查询

- `gowatch_target_up` — 每个 target 是否在线
- `gowatch_ssl_cert_expiry_days` — 证书剩余天数(配阈值红线)
- `gowatch_is_leader` — 当前 leader 是谁
- `sum by (error_type) (rate(gowatch_check_errors_total[5m]))` — 错误类型分布
- `histogram_quantile(0.99, rate(gowatch_check_latency_seconds_bucket[5m]))` — P99 延迟
