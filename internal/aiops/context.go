package aiops

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jiayu113/gowatch/internal/alert"
	"github.com/jiayu113/gowatch/pkg/checker"
)

// DiagContext 是喂给 LLM 的全部信息
type DiagContext struct {
	Ev          alert.Event      // 告警事件
	History     []checker.Result // 时间正序,最多 HistoryLimit 条
	ErrBuckets  map[string]int   // 错误类型 → 次数(仅 down)
	LatencyP50  time.Duration
	LatencyMax  time.Duration
	FirstDownAt time.Time // 本轮故障最早 down(从最新往回连续段)
	FlapCount   int       // 历史窗口内 up/down 翻转次数(闪断指标)
}

func BuildContext(ev alert.Event, hist []checker.Result) DiagContext {
	dc := DiagContext{Ev: ev, History: hist, ErrBuckets: map[string]int{}}
	var lats []time.Duration
	prev := ""
	for _, r := range hist {
		if r.Status == "down" && r.ErrorType != "" {
			dc.ErrBuckets[r.ErrorType]++
		}
		if r.Latency > 0 {
			lats = append(lats, r.Latency)
			if r.Latency > dc.LatencyMax {
				dc.LatencyMax = r.Latency
			}
		}
		// 闪断
		if prev != "" && r.Status != prev {
			dc.FlapCount++
		}
		prev = r.Status
	}
	if len(lats) > 0 {
		sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
		dc.LatencyP50 = lats[len(lats)/2]
	}
	// 从最新往回找连续 down 段的起点
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Status != "down" {
			break
		}
		dc.FirstDownAt = hist[i].Timestamp
	}
	return dc
}

// RenderPrompt 渲染为 user 消息。
const SystemPrompt = `你是一名资深 SRE 助手,协助分析监控告警的可能根因。规则:
1. 只提供排查建议,绝不使用任何"自动执行/我已修复"类口吻;
2. 按固定结构输出:## 可能根因(按可能性排序,每条附依据) / ## 下一步排查(命令级动作) / ## 置信度说明;
3. 依据不足就明说不确定,不编造。回答用中文,精炼。`

func (dc DiagContext) RenderPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 告警\n规则: %s\n目标: %s\n触发时间: %s\n触发原因: %s\n\n",
		dc.Ev.RuleName, dc.Ev.Target, dc.Ev.FireAt.Format(time.RFC3339), dc.Ev.Reason)
	fmt.Fprintf(&b, "## 触发快照(最近 %d 次)\n", len(dc.Ev.Snapshot))
	for _, r := range dc.Ev.Snapshot {
		fmt.Fprintf(&b, "- %s status=%s latency=%s err_type=%s err=%q\n",
			r.Timestamp.Format("15:04:05"), r.Status, r.Latency, r.ErrorType, r.Error)
	}
	fmt.Fprintf(&b, "\n## 历史统计(近 %d 次探测)\n错误分桶: %v\n延迟 p50=%s max=%s\n状态翻转次数: %d\n",
		len(dc.History), dc.ErrBuckets, dc.LatencyP50, dc.LatencyMax, dc.FlapCount)
	if !dc.FirstDownAt.IsZero() {
		fmt.Fprintf(&b, "本轮故障起点: %s(已持续 %s)\n",
			dc.FirstDownAt.Format(time.RFC3339), time.Since(dc.FirstDownAt).Round(time.Second))
	}
	return b.String()
}
