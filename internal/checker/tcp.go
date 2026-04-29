package checker

import (
	"context"
	"net"
	"time"

	"github.com/jiayu113/gowatch/internal/config"
)

type TCPChecker struct {
	Target config.Target
}

func (t *TCPChecker) result(status, errMsg string, start time.Time) Result {
	return Result{
		Target:    t.Target.Name,
		Status:    status,
		Error:     errMsg,
		Timestamp: time.Now(),
		Latency:   time.Since(start),
	}
}

func (t *TCPChecker) Check(ctx context.Context) Result {
	start := time.Now()
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", t.Target.URL)
	if err != nil {
		return t.result(StatusDown, err.Error(), start)
	}
	defer conn.Close()
	return t.result(StatusUp, "", start)
}

var _ Checker = (*TCPChecker)(nil)
