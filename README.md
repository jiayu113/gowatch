# GoWatch

> 用 Go 写的轻量探活监控服务:HTTP / TCP / SSL 证书探测、告警规则引擎、etcd 选主的多实例 active-standby、Prometheus 指标、AIOps 旁路诊断(实验性)。单二进制、无 CGO、跨平台。

![Web 面板截图](image-2.png)

GoWatch 周期性对一组 HTTP / TCP / SSL 证书目标做健康检查,结果按错误类型分类后写入 SQLite,通过 Web 面板、REST API 和 Prometheus `/metrics` 暴露,命中告警规则后走 webhook 通知。支持多实例部署:通过 etcd 选主实现 active-standby,leader 失效后 follower 自动接管;不开 `--cluster` 时行为与单机完全一致。

> K8s Operator 形态见独立仓 [gowatch-operator](https://github.com/jiayu113/gowatch-operator),其探测层经 versioned Go module(`v1.2.0`)跨仓复用本仓的公开包 `pkg/checker`。

## 特性

- **三类探测**:HTTP(S) 状态码 / TCP 连通性 / SSL 证书剩余天数,`Checker` 接口可扩展
- **错误分类**:网络错误归为 `timeout` / `refused` / `dns` / `non_2xx` / `cert_expiring` / `other`,告警规则和指标都按类型分桶,能区分"网络抖动""服务挂了""证书快过期"
- **告警引擎**:三种规则语义 + cooldown 抑制 + webhook 重试;抑制状态经 etcd 持久化,跨重启 / 跨 leader 切换不清零
- **多实例 active-standby**:etcd 选主,任意时刻单活探测;集群身份对指标、API 和上游负载均衡可见
- **AIOps 旁路诊断(实验性)**:告警触发后生成 LLM 根因分析与排查建议,见[下文](#aiops-诊断层实验性)
- **配置热加载**:fsnotify 监听 + debounce 防抖,改 `config.yaml` 不用重启
- **可观测**:Prometheus 指标(含证书剩余天数、leader 身份)+ Web 面板 + REST API + CLI 查询
- **SQLite 持久化**:纯 Go 驱动无 CGO;优雅关闭,不丢数据

## 快速开始

```bash
git clone https://github.com/jiayu113/gowatch.git
cd gowatch
go build -o gowatch ./cmd/gowatch
```

创建 `config.yaml`:

```yaml
targets:
  - name: example-home
    type: http
    url: https://example.com
    timeout: 3s

  - name: local-mysql
    type: tcp
    url: 127.0.0.1:3306

  - name: example-ssl
    type: cert
    url: example.com:443
    cert_warn_days: 14   # 剩余天数低于该值判为 down,默认 14
```

启动:

```bash
./gowatch
```

浏览器打开 `http://localhost:8080` 看面板。默认行为:5 个 worker、10 秒探测周期、数据写 `./gowatch.db`、Ctrl+C / SIGTERM 优雅退出。修改 `config.yaml` 后无需重启,下一轮探测自动生效。

### 告警(可选)

创建 `alerts.yaml` 即启用告警引擎;文件不存在则自动关闭,不影响主服务:

```yaml
rules:
  - name: site-down
    target: "*"                    # * 匹配所有 target
    type: consecutive_status       # 连续 N 次 status 命中
    status: down
    threshold: 3
    cooldown: 5m
    webhook: http://localhost:9999/hook

  - name: cert-expiring
    target: "*"
    type: consecutive_error_type   # 连续 N 次同一错误类型
    error_type: cert_expiring
    threshold: 1
    cooldown: 12h
    webhook: http://localhost:9999/hook
```

三种规则类型:`consecutive_status`(连续 N 次同状态)、`consecutive_error_type`(连续 N 次同错误类型)、`error_rate_window`(时间窗口内错误率)。语义细节与设计取舍见 [docs/architecture.md](docs/architecture.md)。

### 命令行选项

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `--config` | `config.yaml` | 探测目标配置 |
| `--db` | `gowatch.db` | SQLite 路径 |
| `--port` | `:8080` | HTTP 监听端口 |
| `--cluster` | `false` | 启用集群模式(需 `--etcd`) |
| `--etcd` | `""` | etcd endpoints,逗号分隔 |
| `--node-id` | hostname | 本实例节点 ID |
| `--query` / `--target` / `--limit` | - | 命令行查询历史 |
| `--latest` | `false` | 查每个 target 最新状态 |

## 架构

```
主 goroutine ──Ticker──→ jobs channel ──→ Worker Pool(并发探测 + 上报指标)
                                              │
                                              ▼
                                       results channel
                                              │
                                              ▼
                                          collector ──┬─→ SQLite
                                                      └─→ 告警引擎(Window → Rule.Match → 抑制判断)
                                                              │ 命中且未在 cooldown
                                                              ▼
                                                      go fire ──┬─→ webhook(5xx 重试 1 次)
                                                                ├─→ 落库(alerts 表)
                                                                └─→ AIOps 旁路(可选,见下文)
```

- 三层 goroutine 职责分离:主 goroutine 派活 / Worker Pool 并发探测 / collector 单独写库,SQLite IO 不拖累探测
- 告警评估永不阻塞主链路:内存操作 ms 级,可能慢的 webhook 全部异步
- 集群模式在 scheduler 外面套一层 etcd 选主,scheduler 本身对集群无感知

完整数据流图、集群选主细节、各项设计取舍见 **[docs/architecture.md](docs/architecture.md)**。

## 多实例部署

```bash
# 实例 A / B 用同一份 config.yaml,不同 db / port / node-id
./gowatch --cluster --etcd=etcd1:2379,etcd2:2379 --node-id=node-a --port=:8080 --db=a.db
./gowatch --cluster --etcd=etcd1:2379,etcd2:2379 --node-id=node-b --port=:8081 --db=b.db
```

- 任意时刻只有 leader 在跑探测,避免重复探测和重复告警;follower 阻塞等待选举,数据接口返回 `503` + 角色 header,负载均衡可据此只把读流量打给 leader
- leader 失效后 follower 约 15 秒内自动接管(session TTL 15s / 心跳 5s);新 leader 上位时从 etcd 回灌告警抑制状态,cooldown 不在切换瞬间清零
- `gowatch_is_leader` 指标可直接表达"无 leader"(`absent(gowatch_is_leader == 1)`)与"脑裂"(`sum(gowatch_is_leader) > 1`);GoWatch 自己是被监控对象,集群整体健康需接外部 Alertmanager(规则示例见 [docs/architecture.md](docs/architecture.md#接-alertmanager))

## AIOps 诊断层

告警触发后,旁路生成 LLM 根因分析与排查建议(不影响告警主链路,可整体禁用)。

- **旁路异步**:emit 订阅侧 fan-out,LLM 任何故障对告警零影响
- **成本三闸门**:同目标冷却 / 每日上限 / 连续失败熔断
- **建议不执行**:输出仅落日志与 JSONL,不触发任何动作(设计立场)
- **配置见 `aiops.example.yaml`;未配置即禁用**

启用:

```bash
cp aiops.example.yaml aiops.yaml     # aiops.yaml 已 gitignore,不进仓
export GOWATCH_AIOPS_API_KEY=...     # key 只走环境变量,配置里只写变量名
./gowatch
```

诊断结果落 `data/aiops/diagnosis-<日期>.jsonl` 与日志。任意 OpenAI 兼容端点均可(默认配置指向 DeepSeek)。设计文档与取舍见 [docs/aiops.md](docs/aiops.md)。

### 局限

- LLM 可能给出错误或过泛的判断;输出附置信度声明,最终判断永远在人
- 依赖外部 API 的可用性与成本;三闸门约束上界但不为质量兜底
- 当前仅覆盖"告警级"诊断,不做跨目标关联分析

## API 与指标

| 端点 | 说明 |
|------|------|
| `GET /api/status` | 每个 target 的最新状态 |
| `GET /api/history?target=&limit=` | 单 target 历史记录 |
| `GET /api/alerts?limit=` | 最近告警事件 |
| `GET /api/cluster/status` | 本实例集群身份(follower 也能查) |
| `GET /api/health` | 存活检查 |
| `GET /metrics` | Prometheus 指标 |

集群模式下,前三个数据接口在 follower 上返回 `503` + `X-GoWatch-Role` 头。

| 指标 | 类型 | 说明 |
|------|------|------|
| `gowatch_target_up` | gauge | target 当前是否在线 |
| `gowatch_check_total` | counter | 检查次数,按 up/down 分桶 |
| `gowatch_check_errors_total` | counter | 错误次数,按 `error_type` 分桶 |
| `gowatch_check_latency_seconds` | histogram | 探测耗时分布 |
| `gowatch_ssl_cert_expiry_days` | gauge | 证书剩余天数(负数=已过期) |
| `gowatch_is_leader` | gauge | 本实例是否 leader(单机恒为 1) |

## 测试

```bash
go test ./...          # 全量
go test -race ./...    # 竞态检测
```

值得一提的几处:

- 集群选主用 embedded etcd 覆盖 Campaign / Failover / Session 过期 / 优雅退出;另有 dockertest 真容器 e2e(冻结容器模拟网络黑洞),用 `//go:build dockertest` 隔离,不进默认套件
- 证书检查用自签证书覆盖"有效"与"快过期"两条路径,不依赖公网
- 告警异步契约有专门回归测试:webhook 持续 5xx / 超时时,探测主链路绝不被阻塞

## 版本

- **v1**:核心探测链路(config / checker / storage / api / scheduler)+ Web UI + 错误分类
- **v2**:告警引擎、配置热加载、etcd 选主 active-standby、抑制状态跨重启持久化
- **v2.x**:SSL 证书监控;`pkg/checker` 公开化并打 tag `v1.2.0`,被 [gowatch-operator](https://github.com/jiayu113/gowatch-operator) 跨仓消费;dockertest e2e
- **当前**:AIOps 旁路诊断层(实验性)
- **Backlog**:`alerts.yaml` 热加载 / 告警收敛批量发送 / 通知通道扩展(`Notifier` 接口已抽出)/ 共享存储后端

## License

[MIT](LICENSE)
