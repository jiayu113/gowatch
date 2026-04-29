package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jiayu113/gowatch/internal/checker"
	"github.com/jiayu113/gowatch/internal/storage"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler 是所有 HTTP 处理器的聚合。
// 持有 store 的引用，这样 handler 就能读数据库。
type Handler struct {
	store     *storage.Store
	startedAt time.Time // 服务启动时间，用于计算 uptime
}

func NewHandler(store *storage.Store) *Handler {
	return &Handler{
		store:     store,
		startedAt: time.Now(),
	}
}

// Routes 返回一个 ServeMux，把所有路由注册好。
// 把"定义路由"和"启动服务"分开，方便后面测试。
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", h.Health)
	mux.HandleFunc("/api/status", h.Status)
	mux.HandleFunc("/api/history", h.History)

	// 访问/metrics时会自动把所有注册的metric序列化成Prometheus能抓取的文本格式输出出来
	mux.Handle("/metrics", promhttp.Handler())

	// 托管 Web UI 目录
	mux.Handle("/", http.FileServer(http.Dir("./internal/api/static")))
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
	if results == nil {
		results = []checker.Result{}
	}
	writeJSON(w, http.StatusOK, results)
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
	if results == nil {
		results = []checker.Result{}
	}
	writeJSON(w, http.StatusOK, results)
}

// writeJSON 抽出的小工具：统一设置响应头 + 状态码 + 编码。
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode error: %v", err)
	}
}
