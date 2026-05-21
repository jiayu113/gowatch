package cluster

type LeaderState interface {
	IsLeader() bool // 是否是 leader
	NodeID() string // 当前节点 ID
	Mode() string   // "standalone" / "cluster"
}

// SingleLeader 是单节点模式的 always-leader 实现
type SingleLeader struct {
	nodeID string
}

func NewSingleLeader(nodeID string) *SingleLeader {
	return &SingleLeader{nodeID: nodeID}
}

func (s *SingleLeader) IsLeader() bool {
	return true
}

func (s *SingleLeader) NodeID() string {
	return s.nodeID
}

func (s *SingleLeader) Mode() string {
	return "standalone"
}
