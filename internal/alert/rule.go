package alert

import (
	"time"

	"github.com/jiayu113/gowatch/internal/checker"
)

// Rule 描述一条告警规则。从 alerts.yaml 加载。
type Rule struct {
	Name      string        `yaml:"name"`                 // 规则名，告警标识
	Target    string        `yaml:"target"`               // 应用的 target 名，"*" 表示所有
	Type      string        `yaml:"type"`                 // consecutive_status / consecutive_error_type / error_rate_window
	Status    string        `yaml:"status,omitempty"`     // consecutive_status 用：down / up
	ErrorType string        `yaml:"error_type,omitempty"` // consecutive_error_type 用：timeout / refused / dns / non_2xx
	Threshold int           `yaml:"threshold"`            // 连续 N 次 / 错误率百分比
	Window    time.Duration `yaml:"window,omitempty"`     // error_rate_window 用
	Cooldown  time.Duration `yaml:"cooldown"`             // 抑制窗口
	Webhook   string        `yaml:"webhook"`              // POST 目标 URL
}

// Event 告警事件，命中规则后产生
type Event struct {
	RuleName string           `json:"rule_name"`
	Target   string           `json:"target"`
	FireAt   time.Time        `json:"fired_at"`
	Reason   string           `json:"reason"`   // 触发原因
	Snapshot []checker.Result `json:"snapshot"` // 触发时的最近 N 次结果快照
}

// Match 判断规则是否在最近结果序列上命中。recent 假定按时间正序（最旧→最新）。
func (r *Rule) Match(recent []checker.Result) (bool, string) {
	switch r.Type {
	case "consecutive_status":
		return matchConsecutiveStatus(r, recent)
	case "consecutive_error_type":
		return matchConsecutiveErrorType(r, recent)
	case "error_rate_window":
		return matchErrorRateWindow(r, recent)
	default:
		return false, ""
	}
}
