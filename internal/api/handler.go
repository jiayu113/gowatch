package api

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jiayu113/gowatch/internal/cluster"
	"github.com/jiayu113/gowatch/internal/storage"
	"github.com/jiayu113/gowatch/pkg/checker"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ResultDTO 是 API 对外输出的 Result 表示。
// 内部用 time.Duration 存纳秒精度,API 层转成毫秒 float 给前端,
// 字段名走 camelCase + 单位后缀,自我说明。
type ResultDTO struct {
	Target    string    `json:"target"`
	Status    string    `json:"status"`
	LatencyMs float64   `json:"latency_ms"`
	Error     string    `json:"error"`
	Timestamp time.Time `json:"timestamp"`
}

type ClusterStatusDTO struct {
	Mode     string `json:"mode"`      // cluster or standalone
	NodeID   string `json:"node_id"`   // 当前节点 ID
	IsLeader bool   `json:"is_leader"` // 是否是 leader
	Uptime   string `json:"uptime"`    // 服务启动时间
}

func (h *Handler) ClusterStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, ClusterStatusDTO{
		Mode:     h.leaderState.Mode(),
		NodeID:   h.leaderState.NodeID(),
		IsLeader: h.leaderState.IsLeader(),
		Uptime:   time.Since(h.startedAt).String(),
	})
}

func toDTO(r checker.Result) ResultDTO {
	return ResultDTO{
		Target:    r.Target,
		Status:    r.Status,
		LatencyMs: float64(r.Latency) / float64(time.Millisecond),
		Error:     r.Error,
		Timestamp: r.Timestamp,
	}
}

func toDTOs(results []checker.Result) []ResultDTO {
	dtos := make([]ResultDTO, len(results))
	for i, r := range results {
		dtos[i] = toDTO(r)
	}
	return dtos
}

// Handler 是所有 HTTP 处理器的聚合。
// 持有 store 的引用，这样 handler 就能读数据库。
type Handler struct {
	store       *storage.Store
	leaderState cluster.LeaderState
	startedAt   time.Time // 服务启动时间，用于计算
}

func NewHandler(store *storage.Store, state cluster.LeaderState) *Handler {
	return &Handler{
		store:       store,
		leaderState: state,
		startedAt:   time.Now(),
	}
}

// Routes 返回一个 ServeMux，把所有路由注册好。
// 把"定义路由"和"启动服务"分开，方便后面测试。
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", h.Health)
	mux.HandleFunc("/api/status", RequireLeader(h.leaderState, h.Status))
	mux.HandleFunc("/api/history", RequireLeader(h.leaderState, h.History))
	mux.HandleFunc("/api/alerts", RequireLeader(h.leaderState, h.Alerts))
	mux.HandleFunc("/api/diagnoses", RequireLeader(h.leaderState, h.Diagnoses))
	mux.HandleFunc("/api/cluster/status", h.ClusterStatus)

	// 访问/metrics时会自动把所有注册的metric序列化成Prometheus能抓取的文本格式输出出来
	mux.Handle("/metrics", promhttp.Handler())

	// embed.FS 内部路径是 "static/index.html",
	// fs.Sub 剥掉 "static" 前缀,让 mux "/" 路由直接对应 index.html
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// 路径写错了,启动期就 panic,fail fast
		panic(err)
	}
	// 托管 Web UI 目录
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
}

// Health 返回服务自身健康状态
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"uptime": time.Since(h.startedAt).String(),
	})
}

// Status 返回每个目标的最新状态
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	results, err := h.store.GetLatestPerTarget()
	if err != nil {
		log.Printf("GetLatestPerTarget error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toDTOs(results))
}

// History 返回某个目标的历史记录
// 例如 GET /api/history?target=baidu.com&limit=50
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "missing target parameter", http.StatusBadRequest)
		return
	}

	limit := 20 // 默认值
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	results, err := h.store.GetByTarget(target, limit)
	if err != nil {
		log.Printf("GetByTarget error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toDTOs(results))
}

// writeJSON 抽出的小工具：统一设置响应头 + 状态码 + 编码。
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode error: %v", err)
	}
}

func (h *Handler) Alerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	events, err := h.store.GetRecentAlerts(limit)
	if err != nil {
		log.Printf("GetRecentAlerts error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// Diagnoses 返回最近的 AI 诊断记录
func (h *Handler) Diagnoses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 20
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	diags, err := h.store.GetRecentDiagnoses(limit)
	if err != nil {
		log.Printf("GetRecentDiagnoses error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, diags)
}
