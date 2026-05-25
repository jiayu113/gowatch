package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// 总检测次数（按 target × status 分类）
	CheckTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{ // 第一个参数：metric的描述信息
			Name: "gowatch_check_total",    // metric的名字，Prometheus里查询用这个名字
			Help: "Total number of checks", // 说明文字
		},
		[]string{"target", "status"}, // 第二个参数：这个metric有哪些标签
	)

	// 检测耗时分布
	CheckLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gowatch_check_latency_seconds",
			Help:    "Check latency in seconds",
			Buckets: prometheus.DefBuckets, // Prometheus默认提供的边界,延迟落在哪个区间就往哪个桶里计数
		},
		[]string{"target"},
	)

	// 当前是否 up（1/0）
	TargetUp = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gowatch_target_up",
			Help: "1 if target is up,0 otherwise",
		},
		[]string{"target"},
	)

	// 检测失败累计次数
	CheckErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gowatch_check_errors_total",
			Help: "Total number of check errors",
		},
		[]string{"target", "error_type"},
	)

	// IsLeader 标记当前实例是否是 leader(单机模式恒为 1)
	IsLeader = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "gowatch_is_leader",
			Help: "1 if this node is leader, 0 otherwise",
		},
	)

	// SSL 证书剩余有效天数
	SSLCertExpiryDays = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gowatch_ssl_cert_expiry_days",
			Help: "Days until SSL certificate expiry (negative = expired)",
		},
		[]string{"target"},
	)
)

func Record(target, status, errType string, latencySeconds float64, hasError bool) {
	// 总检测次数+1（按target和status分）
	CheckTotal.WithLabelValues(target, status).Inc()
	// 记录这次延迟
	CheckLatency.WithLabelValues(target).Observe(latencySeconds)
	// 更新当前是否up
	if status == "up" {
		TargetUp.WithLabelValues(target).Set(1)
	} else {
		TargetUp.WithLabelValues(target).Set(0)
		// 失败才记 errors，按 type 分桶
		CheckErrors.WithLabelValues(target, errType).Inc()
	}
}
