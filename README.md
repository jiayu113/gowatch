# GoWatch

> 用 Go 写的轻量探活监控服务 —— 配置驱动、并发探测、错误分类、SQLite 持久化、HTTP API、Web 面板、Prometheus 指标、告警规则引擎、**SSL/TLS 证书过期监控**、**多实例 active-standby(集群状态对外可见)**。单二进制、无 CGO、跨平台。

![Web 面板截图](image-2.png)

GoWatch 周期性地对一组 **HTTP / TCP / SSL 证书**目标做健康检查,把结果按**错误类型**分桶并写入 SQLite,通过 HTTP API + Web UI + Prometheus `/metrics` 端点暴露,命中告警规则后走 webhook 通知并落库。**SSL 证书目标**会读叶子证书的 `NotAfter`,剩余天数低于阈值即判定为 down 并暴露到期天数指标,把 HTTP / TCP / 证书三类监控统一进同一套调度 + 告警 + 指标链路。**v2 引入多实例部署**:通过 etcd 协调实现 active-standby 选主,任意时刻只有一个实例在跑 scheduler,Leader 失效后 follower 自动接管;集群身份对 Prometheus(`gowatch_is_leader`)、HTTP API(`/api/cluster/status`)和上游负载均衡(follower 数据接口返回 `503` + 角色 header)**都可见**;告警抑制状态通过 etcd 跨重启 / 跨 leader 切换对齐。**单机模式 100% 向后兼容(不开 `--cluster` flag 时行为与之前完全一致)**。整个服务是常驻进程,支持优雅关闭,可以放心 Ctrl+C 或 SIGTERM。

> **项目线**:本仓是 GoWatch 的独立守护进程形态(v1–v2.x)。K8s Operator 形态见子项目仓 **[gowatch-operator](https://github.com/jiayu113/gowatch-operator)**(产品线 v3)——其进程内探测引擎经 versioned Go module 跨仓复用本仓的公开包 [`pkg/checker`](#pkgchecker-公开包)。

---

## 特性

- **多协议健康检查** — HTTP(S) 状态码 / TCP 端口连通性 / **SSL 证书剩余天数**,通过 `Checker` 接口扩展
- **公开探测内核(`pkg/checker`)** — `Checker` 接口、HTTP/TCP/Cert 三个实现与 `error_type` 错误分类以公开包形式暴露,经 versioned Go module(`v1.2.0`)被子项目 [gowatch-operator](https://github.com/jiayu113/gowatch-operator) 真实跨仓消费(详见 [pkg/checker 公开包](#pkgchecker-公开包))
- **SSL/TLS 证书过期监控** — `cert` 检查类型做 TLS 握手、读叶子证书 `NotAfter`,剩余天数低于 `cert_warn_days`(默认 14)判定为 down,错误类型记为 `cert_expiring`;`gowatch_ssl_cert_expiry_days` 指标暴露到期天数,可直接接 Alertmanager 做提前预警;TLS 握手本身的网络错误(超时 / DNS / 拒绝)走同一套 `error_type` 分类
- **并发 Worker Pool 调度** — 固定 worker + Ticker 周期触发 + collector 解耦写库
- **错误分类(`error_type`)** — 把网络层错误归类为 `timeout` / `refused` / `dns` / `non_2xx` / `cert_expiring` / `other`,Prometheus 指标按错误类型分桶,**让告警规则能区分"网络抖动""服务挂了""证书快过期"**
- **告警规则引擎** — 三种语义的规则匹配 + cooldown 抑制 + webhook 通知(带重试)+ SQLite 持久化;**抑制状态通过 etcd 跨重启 / 跨 leader 对齐(已接入主评估链路)**,`OnResult` 永不阻塞探测主链路
- **多实例 active-standby(v2)** — `internal/cluster` 包封装 etcd Election,**Leader 跑 scheduler,follower 阻塞在 Campaign,session 失效后自动重新参选**;**集群身份对外可见**:`gowatch_is_leader` 指标 + `/api/cluster/status` 端点 + follower 数据接口返回 `503` 并带 `X-GoWatch-Role` 头;scheduler 对 cluster 层完全透明,单机模式不开 `--cluster` 完全向后兼容
- **`LeaderState` 统一抽象** — 单机模式用 always-leader 的 `SingleLeader`,集群模式由 `cluster.Leader` 实现同一接口;API 层 / metric 层 / middleware 都只依赖这个接口,**两条装配路径不散落 if 分支**
- **SQLite 持久化** — 纯 Go 驱动 (`modernc.org/sqlite`),无 CGO,跨平台编译零负担
- **REST API + Web UI** — 实时状态、按 target 查历史、最近告警列表、集群状态查询,前端 5 秒自动刷新
- **Prometheus 集成** — `/metrics` 端点暴露 counter / histogram / gauge(含 `gowatch_is_leader`、`gowatch_ssl_cert_expiry_days`),可直接接 Grafana + Alertmanager
- **Graceful Shutdown** — `signal.NotifyContext` + `server.Shutdown` + scheduler done channel 三步收尾,不丢数据;集群模式额外加 leader 优雅下台(取消 leaderCtx + 5s 超时兜底)
- **配置热加载(fsnotify)** — 监听 config.yaml 变化,200ms debounce 防抖,下一轮 dispatch 切换到新 checker
- **CLI 工具化** — 同一个二进制支持服务模式、查询历史、查看最新状态三种用法

---

## 架构

### 单机模式(默认)

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
                                                                             └─→ emit channel → SaveAlert

ctx.Done() ─→ close(jobs) ─→ workers 退出 ─→ close(results) ─→ collector 退出 ─→ store.Close
```

**三层 goroutine 职责分离:**
- **主 goroutine** — Ticker 派活、监听 ctx 信号、协调关闭顺序
- **Worker Pool** — 并发跑 `Checker.Check(ctx)`,IO 密集场景天然受益于并发,每个 worker 用 per-target 独立 ctx 避免互相拖累;**Prometheus 指标在 worker 里同步更新**(每次探测即时 `metrics.Record`,cert 目标额外 set `gowatch_ssl_cert_expiry_days`),指标反映每一次探测,不依赖后续写库
- **Collector** — 单独 goroutine 串行写库 (`store.Save`) + 调用 `evaluator.OnResult`,把 SQLite IO 与 worker 解耦

### 集群模式(`--cluster`)

scheduler 不变,外面套一层 cluster.Leader 来负责"谁跑"。Leader 用回调式 API,scheduler 不知道自己是不是 leader,只是被传入了一个 ctx:

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
- **`LeaderState` 接口统一身份查询** — `IsLeader()` 用 `atomic.Bool`,Run 里上位前置 1、下位后置 0;API 层 / middleware / metric 都读这个接口,单机模式由 `SingleLeader`(恒为 leader)实现,**避免到处写 `if clusterMode`**
- **scheduler 100% 复用** — 同一份 `pool.Run(ctx)` 代码,单机模式直接调,集群模式被 `leader.Run` 包裹后调;集群上位 = leaderCtx 启动,session 失效 = leaderCtx 取消,scheduler 自然退出
- **fail-fast on startup, backoff at runtime** — 启动期 etcd dial 失败直接报错(配置错应该让人看见);运行期 etcd 抖动用指数退避(1s → 2s → ... ≤ 30s),不让重连风暴打挂 etcd
- **优雅下台 5s 上限** — session 失效或 ctx cancel 时,先取消 leaderCtx 让 scheduler 自己收尾;`select` 等 done channel,5s 后超时强退,**不让一个挂掉的 scheduler 阻塞自身退出**
- **session TTL 默认 15s** — 心跳 / 5s,容忍 1-2 次丢包;权衡了"切换太快导致 false failover"和"切换太慢导致探测停摆"
- **集群健康靠外部监控 + 自身 metric** — `gowatch_is_leader` 让 Alertmanager 能直接表达"无 leader"(`absent(gowatch_is_leader == 1)`)和"脑裂"(`sum(gowatch_is_leader) > 1`);但"集群整体挂掉"这种事 GoWatch 自己监控不到(自己就是被监控对象),仍必须接外部 Alertmanager(详见下文"多实例部署")

### 告警引擎链路

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
                                  └─→ emit ch → store.SaveAlert
```

**关键设计:**

- **`OnResult` 永不阻塞主链路** — Window.Push 与 suppressor 判断是 ms 级内存操作;真正可能慢的 webhook 全部丢进 `go fire(...)`,网络抖动绝对不会拖死 collector
- **复用 `error_type` 维度** — `consecutive_error_type` 规则可以做到"连续 2 次 dns 失败才告警",这是单纯 status 维度做不到的判断;同理 `error_type=cert_expiring` 可以专门为"证书快过期"配规则
- **Suppressor 是 (rule, target) 二元 key** — 同一规则在不同 target 上的 cooldown 互相独立,一个 target 在抑制窗口内不会让别的 target 也被静默
- **Suppressor 跨重启 / 跨 leader 持久化(已接入主链路)** — `OnResult` 统一调用 `AllowAndPersist`:命中并放行时**异步**写 etcd(`/gowatch/suppressor/<rule>:<target>` → JSON `{LastFiredAt}`,不阻塞探测);leader 上位时 `LoadFromEtcd` 把所有 (rule, target) 的最近触发时间回灌内存。**`etcdCli == nil`(单机模式 / 集群启动降级)时自动退回纯内存 `Allow`,无分支侵入**
- **Window cap=50 ring buffer** — 满了从尾部裁剪,保证内存有上界;`Snapshot` 返回独立副本,有专门的 unit test 守住这个契约
- **Webhook 重试策略** — 5xx 视为服务端临时故障,500ms 后重试 1 次;4xx 是客户端语义错误(URL 写错、payload 不合规),重试也没用,直接返回;网络错误按 5xx 处理
- **emit channel buffer=100 + default drop** — `SaveAlert` 慢于产出时丢日志并 drop,不阻塞 fire goroutine

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
  - name: example-home
    type: http
    url: https://example.com
    timeout: 3s

  - name: local-mysql
    type: tcp
    url: 127.0.0.1:3306
    timeout: 2s

  - name: example-ssl
    type: cert
    url: example.com:443
    cert_warn_days: 14

  - name: dns-fail-test
    type: http
    url: http://xxx-not-exist-12345.com
    timeout: 3s
```

字段说明:

- `type` 显式指定探测协议(`http` / `tcp` / `cert`),scheduler 会构造对应的 Checker 实例
- `type: cert` 的 `url` 是 `host:port`(TLS 握手用,不带 scheme),`cert_warn_days` 是剩余天数预警阈值,**省略时默认 14**;剩余天数低于这个值即判定为 down
- `timeout` 省略时默认 5s,三类检查都通过 per-target ctx 强制执行

修改后保存,无需重启;watcher 检测到变化后会触发 reload,下一轮 dispatch 自动用新配置。重名 target 会让 metrics label 冲突,**config 加载时会做重名校验,重名直接 fail-fast**。

### 告警规则(可选)

在根目录创建 `alerts.yaml`,即可启用告警引擎:

```yaml
rules:
  - name: github-flapping
    target: github-home
    type: consecutive_status
    status: down
    threshold: 3
    cooldown: 5m
    webhook: http://localhost:9999/test-webhook

  - name: dns-broken
    target: "*"
    type: consecutive_error_type
    error_type: dns
    threshold: 2
    cooldown: 10m
    webhook: http://localhost:9999/test-webhook

  - name: cert-expiring
    target: "*"
    type: consecutive_error_type
    error_type: cert_expiring
    threshold: 1
    cooldown: 12h
    webhook: http://localhost:9999/test-webhook

  - name: high-error-rate
    target: "*"
    type: error_rate_window
    threshold: 50
    window: 5m
    cooldown: 10m
    webhook: http://localhost:9999/test-webhook
```

**`alerts.yaml` 文件不存在或加载失败,告警引擎自动关闭,不影响主服务启动。**

> 证书快过期会让 cert 目标的 `status` 变成 down、`error_type` 变成 `cert_expiring`,所以**证书告警不需要单独的规则类型**:直接用 `consecutive_status`(status=down)或 `consecutive_error_type`(error_type=cert_expiring)就能命中。想要更直接的天数阈值告警,接 Alertmanager 用 `gowatch_ssl_cert_expiry_days` 更合适(见下文)。

### 启动服务(单机模式)

```bash
./gowatch
```

默认行为:
- 加载 `./config.yaml`(必需) + `./alerts.yaml`(可选)
- 数据库写入 `./gowatch.db`(同一个库,checks 表 + alerts 表)
- HTTP 服务监听 `:8080`
- Worker 数 5,探测周期 10 秒
- `gowatch_is_leader` 恒为 1(单机即 leader)
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
| `--cluster` | `false` | 启用多实例集群模式(需同时指定 `--etcd`) |
| `--etcd` | `""` | etcd endpoints,逗号分隔,如 `etcd1:2379,etcd2:2379` |
| `--node-id` | hostname | 本实例节点 ID,用作 election value、`gowatch_is_leader` 区分和日志标识 |

```bash
# 查最近 50 条历史
./gowatch --query --limit 50

# 查 example-home 的最近 20 次记录
./gowatch --query --target example-home

# 查每个 target 当前最新状态(命令行版的 /api/status)
./gowatch --latest
```

---

## 证书过期监控

`cert` 检查类型把 SSL/TLS 证书纳入和 HTTP/TCP 同一套调度链路,适合盯独立站、API 网关、内部服务的证书有效期。

**检查逻辑(`pkg/checker/cert.go`):**

1. `tls.Dialer.DialContext` 完成 TLS 握手(默认校验证书链 + hostname);握手失败按网络错误处理,`error_type` 走 `ClassifyNetErr`(可能是 `timeout` / `dns` / `refused` / `other`)
2. 取对端叶子证书 `PeerCertificates[0]`,算 `NotAfter` 距今的剩余天数
3. 剩余天数 `< cert_warn_days` → `status=down`、`error_type=cert_expiring`、`error` 带具体天数
4. 否则 → `status=up`;无论 up/down,剩余天数都写进 `Result.ExpiryDays` 并由 worker set 到 `gowatch_ssl_cert_expiry_days` 指标

**两种告警路径:**

- **走 GoWatch 自己的告警引擎** — 用 `consecutive_error_type` + `error_type=cert_expiring`,命中即 webhook
- **走 Alertmanager(推荐)** — 直接对 `gowatch_ssl_cert_expiry_days` 设阈值,能表达"剩余天数 < N"的连续区间,比 up/down 二值更精细:

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

---

## 多实例部署

GoWatch 通过 etcd 协调实现 active-standby,**任意时刻只有一个实例在跑 scheduler,避免重复探测和重复告警**。

### 启动

```bash
# 实例 A
./gowatch --cluster --etcd=etcd1:2379,etcd2:2379 --node-id=node-a \
    --port=:8080 --db=/var/lib/gowatch/a.db --config=/etc/gowatch/config.yaml

# 实例 B(同一份 config.yaml,不同 db / port / node-id)
./gowatch --cluster --etcd=etcd1:2379,etcd2:2379 --node-id=node-b \
    --port=:8081 --db=/var/lib/gowatch/b.db --config=/etc/gowatch/config.yaml
```

不指定 `--node-id` 时默认用 `hostname`。容器化部署建议显式传入 Pod 名或 Deployment 名,避免重启导致 hostname 变化让 leader 身份漂移。

### 行为

- **任意时刻只有一个 instance 在跑 scheduler** — follower 阻塞在 `Election.Campaign`,HTTP server 也是起着的(只是 scheduler 没在跑,且数据接口返回 503)
- **Leader 失效后 follower 在 ~15s 内自动接管** — TTL 15s + lease 心跳 / 5s,理论窗口 5-15s
- **session 抖动自动恢复** — leader session 失效后会自动 demote 并重新参选,不需要人工介入
- **抑制状态跨切换对齐** — 新 leader 上位时从 etcd 回灌 Suppressor 状态,cooldown 不在切换瞬间清零
- **单机模式行为不变** — 不开 `--cluster` flag 时,代码路径与之前完全一致

### follower 的 HTTP 行为

集群模式下,数据接口经 `RequireLeader` 中间件保护,**follower 不直接对外服务陈旧数据**:

| 端点 | leader | follower |
|------|--------|----------|
| `/api/status`、`/api/history`、`/api/alerts` | 正常返回 | `503` + `X-GoWatch-Role: follower` + `X-GoWatch-Node-ID: <id>` |
| `/api/cluster/status` | 正常 | 正常(用来查身份) |
| `/api/health`、`/metrics` | 正常 | 正常 |

上游负载均衡可以据此把读流量只打到 leader,或用 `/api/cluster/status` 主动发现当前 leader。

### 接 Alertmanager(集群健康监控)

GoWatch **自己是被监控对象,无法监控自己的集群整体宕机**,生产部署必须接 Alertmanager。有了 `gowatch_is_leader` 指标后,规则可以写得很直接:

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

### 已知 trade-off

- **极端时序下切换期可能重复告警一次** — 抑制状态已通过 etcd 跨 leader 对齐(老 leader 触发即异步写 etcd,新 leader 上位 `LoadFromEtcd` 回灌)。残留窗口:老 leader 触发后、异步写 etcd 尚未落盘就宕机,新 leader 加载不到这一条,可能重复发一次。**这是有意取舍——异步写不阻塞探测主链路,代价是极端时序下偶发一次重复**
- **切换后新 leader 的 SQLite 是空的** — `/api/history` 看不到上一任的数据,因为 SQLite 是 per-instance 的;接入共享存储(MySQL / TiDB)在后续路线实现
- **etcd 全挂时无 leader** — GoWatch 的探测也会停下来,这就是为什么 `GoWatchNoLeader` 这条 Alertmanager 规则**不能省**
- **failover 窗口期(5-15s)探测有空档** — 这是 active-standby 架构的固有特性,不是 bug;真要消除需要 active-active + 任务分片,超出当前定位

---

## API

打开浏览器访问 `http://localhost:8080` 看 Web 面板(包含目标状态表 + 最近告警表,5 秒自动刷新),或者直接调 API:

### `GET /api/health`

```json
{ "status": "ok", "uptime": "1h2m52.9687301s" }
```

### `GET /api/status`

返回每个 target 的最新一条记录(集群模式下 follower 返回 503):

```json
[
  {
    "target": "example-home",
    "status": "up",
    "latency_ms": 144.797,
    "error": "",
    "timestamp": "2026-05-13T03:50:42Z"
  }
]
```

### `GET /api/history?target=<name>&limit=<n>`

返回指定 target 的历史记录(`limit` 默认 20、上限 1000,按时间倒序;follower 返回 503)。

### `GET /api/alerts?limit=<n>`

返回最近触发的告警事件(默认 50、上限 1000,按 `fired_at` 倒序;follower 返回 503):

```json
[
  {
    "rule_name": "dns-broken",
    "target": "dns-fail-test",
    "fired_at": "2026-05-13T13:55:11Z",
    "reason": "连续2次error_type=dns"
  }
]
```

### `GET /api/cluster/status`

返回当前实例的集群身份(**follower 也能查**,不被 `RequireLeader` 拦截):

```json
{ "mode": "cluster", "node_id": "node-a", "is_leader": true, "uptime": "1h2m52s" }
```

单机模式下 `mode` 为 `standalone`,`is_leader` 恒为 `true`。

### `GET /metrics`

Prometheus 兼容的指标端点(详见下一节)。

---

## Prometheus 指标

| 指标 | 类型 | Labels | 说明 |
|------|------|--------|------|
| `gowatch_check_total` | counter | `target`, `status` | 累计检查次数,按 up/down 分桶 |
| `gowatch_check_errors_total` | counter | `target`, **`error_type`** | 累计错误次数,**按错误类型分桶**(含 `cert_expiring`) |
| `gowatch_check_latency_seconds` | histogram | `target` | 检查耗时分布 |
| `gowatch_target_up` | gauge | `target` | 当前是否 up(1=up, 0=down) |
| `gowatch_ssl_cert_expiry_days` | gauge | `target` | **SSL 证书剩余有效天数**(负数=已过期);仅 cert 类型 target 上报 |
| `gowatch_is_leader` | gauge | (无) | 当前实例是否是 leader(1=leader, 0=follower);**单机模式恒为 1**,多实例靠抓取 `instance` label 区分 |
| 标准 Go runtime 指标 | - | - | goroutine 数、heap、GC 等 |

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
error_type: timeout   # timeout / refused / dns / non_2xx / cert_expiring / other
threshold: 3
```

**语义:** 最近 N 次错误**类型完全一致**才触发。
**用法:** 区分"网络抖动型抖动"和"特定故障模式"。比如:
- 连续 5 次 `timeout` → 大概率链路慢/丢包
- 连续 3 次 `dns` → DNS 服务真挂了
- 连续 3 次 `refused` → 后端服务进程没了
- 连续 1 次 `cert_expiring` → 证书进入预警期(配 `threshold: 1` + 长 cooldown 即可)

### 3. `error_rate_window` — 时间窗口内错误率

```yaml
type: error_rate_window
threshold: 50    # 百分比 0-100
window: 5m
```

**语义:** 在 `window` 时间窗口内,错误次数 / 总检测次数 ≥ `threshold`% 触发。
**用法:** 适合做"部分降级"告警 —— 不是连续挂,但成功率明显跌了。

---

## pkg/checker 公开包

探测内核(`Checker` 接口、HTTP / TCP / Cert 三个实现、`error_type` 错误分类)以**公开包**形式暴露在 `pkg/checker`,任何外部 Go module 都可以直接消费:

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

**对外 API 一览:**

| 标识符 | 说明 |
|--------|------|
| `Checker` 接口 | `Check(ctx) Result`,所有探测器的统一抽象 |
| `Target` | 探测目标描述(名称 / 类型 / URL / 超时 / 证书阈值) |
| `Result` | 单次探测结果(状态 / 延迟 / 错误 / 错误类型 / 证书剩余天数) |
| `NewHTTPChecker` / `TCPChecker` / `CertChecker` | 三个具体实现 |
| `ClassifyNetErr` | 网络错误归类:`timeout` / `refused` / `dns` / `other` |
| `ErrType*` 常量 | 错误类型字符串常量(含 `non_2xx`、`cert_expiring`) |

---

## 开发

### 目录结构

```
gowatch/
├── cmd/gowatch/main.go          # 入口:flag、模式分派、生命周期管理、装配 evaluator + cluster + LeaderState
├── pkg/
│   └── checker/                 # ★ 公开探测内核:Checker 接口 + HTTP/TCP/Cert 实现 + 错误分类
│                                #   被 gowatch-operator 经 versioned Go module(v1.2.0)跨仓复用
├── internal/
│   ├── config/                  # YAML 配置加载(http/tcp/cert 校验)+ fsnotify 热加载
│   │                            #   (Target 定义已下沉至 pkg/checker,此处保留 type Target = checker.Target 别名)
│   ├── storage/                 # SQLite 封装(checks + alerts 两张表)
│   ├── api/                     # HTTP Handler + RequireLeader middleware + Web UI(embed.FS)
│   ├── scheduler/               # Worker Pool + Ticker + Collector(cert 目标额外上报到期天数)
│   ├── metrics/                 # Prometheus 指标定义(含 gowatch_is_leader / gowatch_ssl_cert_expiry_days)
│   ├── alert/                   # 告警引擎:rule / matchers / window /
│   │                            #          suppressor(含 etcd 持久化)/
│   │                            #          notifier / evaluator(WithEtcdClient option)
│   └── cluster/                 # etcd 选主:session / leader / backoff /
│                                #          state(LeaderState + SingleLeader)/ dockertest e2e
├── config.yaml                  # 探测目标配置示例
├── alerts.yaml                  # 告警规则配置示例
├── go.mod
└── README.md
```

### 跑测试

```bash
# 单独跑某个包
go test -v ./pkg/checker/        # checker 接口 + 错误分类 + cert 证书检查(自签证书:valid / 快过期两条路径)
go test -v ./internal/storage/   # SQLite(:memory: 模式)
go test -v ./internal/alert/     # 规则匹配 + Window + Suppressor(含 etcd 持久化)+ Notifier + 集成 + 故障注入
go test -v ./internal/cluster/   # embedded etcd 多场景 + dockertest 真实容器 e2e
go test -v ./internal/api/       # RequireLeader middleware(leader 放行 / follower 503 + header)

# 全量
go test -v ./...

# 带覆盖率 / 竞态检测
go test -cover ./...
go test -race ./...
```

**`pkg/checker` 测试覆盖的重点:**

- `http_test.go` — 2xx success / 5xx → non_2xx / ctx 超时 → timeout / 真实 ECONNREFUSED(起服务后立刻关端口,触发真实拒绝连接)
- `errtype_test.go` — `ClassifyNetErr` 对 nil / deadline(含 wrap)/ DNSError / errno-refused / 字符串兜底-refused / 兜底-other 的分类
- `cert_test.go` — **用 `httptest.NewTLSServer` 验有效证书 days > warn → up;用自签证书(5 天后过期)+ `tls.Listen` 验快过期 → down + `cert_expiring` + ExpiryDays 约 5**;TLSConfig 注入自定义 RootCAs 让测试不依赖真实公网证书

**`internal/cluster` 测试覆盖的重点:**

- **embedded etcd 套件**(基于 `go.etcd.io/etcd/server/v3/embed` 内嵌 etcd,无需外部容器):
  - `TestEmbeddedEtcd` — sanity 测试,验证 embedded etcd 起得来并能 Put/Get,后续测试的地基
  - `TestLeader_CampaignSucceeds` — happy path,实例能成功 Campaign + 接收 ctx cancel + 干净退出
  - `TestLeader_TwoInstancesFailover` — A 上位后 B 必须阻塞;cancel A 的 ctx 后 B 在 10s 内接管
  - `TestLeader_SessionExpires` — 真实故障场景:直接 `Revoke` 掉 leader 的 lease 模拟 etcd 视角的 session 死亡,验证 follower 接管
  - `TestLeader_CtxCancelTriggersExit` — 优雅停机:ctx 被外部取消后,`Run` 必须在 7s 内退出
- **dockertest 真实容器 e2e**(`dockertest_e2e_test.go`,起 `quay.io/coreos/etcd` 容器,**用 `//go:build dockertest` build tag 彻底隔离,不进默认测试套件**,docker daemon 不可用时自动 `t.Skipf`):
  - `TestE2E_LeaderFailover` — 真实 etcd 上 A 上位、B 阻塞,cancel A 后 B 在 10s 内接管
  - `TestE2E_EtcdPauseResume_Backoff` — **冻结 etcd 容器模拟网络黑洞**:A 丢 lease 后主动 demote 进 backoff,**解冻后验证自动重连并抢回 leader**(补 embedded etcd 测不到的网络层场景)

**`internal/alert` 测试覆盖的重点:**

- `matcher_test.go` — 三种规则各自的命中 / 不命中边界,包括 `Threshold=0` / 空 recent / 旧数据被窗口过滤这些容易漏掉的分支
- `window_test.go` — 关键契约:`Snapshot` 必须返回独立副本(调用方修改不影响内部),专门有一个 case 守这一行
- `suppressor_test.go` — cooldown 内拦截、不同 target 独立、`Cooldown=0` 永远放行;**`TestSuppressor_PersistAndLoad` 用 embedded etcd 模拟"持久化 → 进程重启 → 加载状态 → cooldown 仍生效"**
- `notifier_test.go` — 5xx 重试 1 次、4xx 不重试、success 路径数据透传
- `integration_test.go` — 用 `httptest.Server` 模拟 webhook,跑完整 OnResult → fire → webhook 链路 + cooldown 抑制
- `failure_test.go` — webhook 持续 5xx / 持续 timeout 时,**`OnResult` 主调用绝不被阻塞**(异步契约的回归测试)

**`internal/api` 测试覆盖的重点:**

- `middleware_test.go` — `RequireLeader` 在 leader 时放行、在 follower 时返回 503 并带 `X-GoWatch-Role` / `X-GoWatch-Node-ID` 头,用 `mockState` 解耦 cluster 依赖

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
- `gowatch_ssl_cert_expiry_days` — 每个证书 target 的剩余天数(配阈值红线一目了然)
- `gowatch_is_leader` — 哪个实例当前是 leader(集群部署时)
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
- [x] **多实例 active-standby + etcd 选主** — `internal/cluster` 包,回调式 API,scheduler 对 cluster 透明;单机模式 100% 向后兼容;embedded etcd 多场景单元测试(Campaign / Failover / SessionExpire / CtxCancel);fail-fast 启动 + 运行期指数退避 + 优雅下台 5s 兜底
- [x] **Suppressor 跨重启持久化** — etcd 后端(`AllowAndPersist` / `LoadFromEtcd`),异步写入不阻塞主链路;`cli == nil` 时自动降级为纯内存(单机模式无侵入);`PersistAndLoad` 单元测试覆盖
- [x] **集群状态对外可见** — `gowatch_is_leader` 指标 + `/api/cluster/status` 端点 + `RequireLeader` 中间件(follower 数据接口 503 + 角色 header);`LeaderState` 接口统一单机/集群两条装配路径,middleware 单测覆盖 leader 放行 / follower 503
- [x] **告警抑制接入主评估链路** — `NewEvaluator` 支持 `WithEtcdClient` functional option,`OnResult` 统一走 `AllowAndPersist`(单机 nil-safe 降级),leader 上位时 `LoadSuppressorFromEtcd` 回灌,**跨重启 / 跨 leader cooldown 真正对齐**

### v2.x — Done

- [x] **SSL / TLS 证书过期监控** — 新增 `cert` 检查类型(TLS 握手读叶子证书 `NotAfter`)+ `cert_warn_days` 阈值 + `gowatch_ssl_cert_expiry_days` 指标;证书快过期 → `status=down` + `error_type=cert_expiring`,直接复用告警引擎;config 校验扩到 http/tcp/cert,scheduler 装配 `CertChecker`;`cert_test.go` 用自签证书覆盖 valid / 快过期两条路径。**补齐 HTTP / TCP / SSL 三类监控**
- [x] **dockertest 真实容器 e2e** — 起真实 etcd 容器(`quay.io/coreos/etcd`)跑端到端 leader 切换 + 容器冻结/解冻(模拟网络黑洞)验证 lease 丢失 demote → backoff → 网络恢复后重连重选;docker daemon 不可用时运行时 `t.Skipf`,补 embedded etcd 测不到的网络层场景
- [x] **dockertest 用 build tag 彻底隔离** — 当前靠运行时 `t.Skipf` 跳过,仍在默认 `go test` 路径里;改成 build tag(如 `//go:build dockertest`)可彻底不进默认套件,本地/CI 显式开启
- [x] **`fsnotify` watcher 改为监听父目录** — 当前监听文件本身,在 vim / VSCode 等 atomic save 编辑器下首次保存后 watcher 失效;改为监听父目录 + filter base name 可解决
- [x] **config 加载时 URL schema 校验** — type=http 校验 http/https 前缀,type=tcp / cert 校验 host:port;否则配错时 latency=0s 看起来像服务挂,实际是配置错
- [x] **`ClassifyNetErr` 真实包装链集成测试** — `errtype_test` 目前以 mock error 为主;用 `httptest` / 真实 dial 失败覆盖 mock 漏掉的包装路径

### checker 内核公开化(tag `v1.2.0`)— Done

- [x] **`internal/checker` → `pkg/checker` 公开包抽取** — `Target` 下沉至公开包 + `internal/config` 保留类型别名,本仓零改动通过全部测试(详见 [pkg/checker 公开包](#pkgchecker-公开包))
- [x] **打 tag `v1.2.0` 并经 goproxy 可解析** — [gowatch-operator](https://github.com/jiayu113/gowatch-operator) 已经由 versioned Go module 真实消费本包

### v2.x — Backlog(计划中 + 已知待修复)

- [ ] **`alerts.yaml` 热加载** — 复用 config watcher 的 debounce 思路,告警规则也支持运行时修改
- [ ] **告警去重 / 抑制升级** — 当前是 (rule, target) 维度 cooldown;复杂场景可能要"先收敛再发"(N 分钟批量一条)
- [ ] **告警通知通道扩展** — 当前只支持 webhook;`Notifier` 接口已经抽出来,加 Email / 钉钉 / 飞书只是新加一个实现

### v3 — 长期

- [x] **K8s 集成 → 已落地为独立项目 [gowatch-operator](https://github.com/jiayu113/gowatch-operator)**:`Watch` CRD 声明式监控集群内 Service——自动发现 Endpoints、进程内并发探测、leader election 单活、错误分类透传 status condition、自定义 Prometheus 指标、envtest + e2e 双层测试;探测层经 `pkg/checker` 跨仓复用本仓内核
- [ ] 共享存储后端:接 MySQL / TiDB,切换 leader 后 `/api/history` 仍能看到完整历史
- [ ] 接入 OpenTelemetry,支持分布式追踪
- [ ] eBPF 探针,从内核层观测 TCP 状态

---

## 📄 License

[MIT](LICENSE)
