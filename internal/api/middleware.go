package api

import (
	"net/http"

	"github.com/jiayu113/gowatch/internal/cluster"
)

func RequireLeader(state cluster.LeaderState, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if state.IsLeader() {
			next(w, r)
			return
		}
		w.Header().Set("X-GoWatch-Role", "follower")
		w.Header().Set("X-GoWatch-Node-ID", state.NodeID())
		http.Error(w, "This node is not the leader", http.StatusServiceUnavailable)
	}
}
