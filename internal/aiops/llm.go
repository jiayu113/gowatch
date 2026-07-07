package aiops

import "context"

// LLM 接口化的两个理由:换模型=改配置;单测=mock。
type LLM interface {
	Complete(ctx context.Context, system, user string) (string, error)
}
