package api

import (
	"net/http"

	"github.com/jiayu113/gowatch/internal/cluster"
)

// RequireLeader 是一个中间件，确保只有集群中的leader节点能处理请求。
func RequireLeader(state cluster.LeaderState, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if state.IsLeader() {
			next(w, r)
			return
		}
		// 不是leader，返回503，并在响应头里说明当前节点的角色和ID，方便调试和监控。
		w.Header().Set("X-GoWatch-Role", "follower")
		w.Header().Set("X-GoWatch-Node-ID", state.NodeID())
		http.Error(w, "This node is not the leader", http.StatusServiceUnavailable)
	}
}
