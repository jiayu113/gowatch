package checker

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jiayu113/gowatch/internal/config"
)

type HTTPChecker struct {
	Target config.Target
}

func (h *HTTPChecker) result(status, errMsg string, start time.Time) Result {
	return Result{
		Target:    h.Target.Name,
		Status:    status,
		Error:     errMsg,
		Timestamp: time.Now(),
		Latency:   time.Since(start),
	}
}

func (h *HTTPChecker) Check(ctx context.Context) Result {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", h.Target.URL, nil)
	if err != nil {
		return h.result(StatusDown, err.Error(), start)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return h.result(StatusDown, err.Error(), start)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return h.result(StatusUp, "", start)
	}
	return h.result(StatusDown, fmt.Sprintf("unexpected status: %d", resp.StatusCode), start)
}

var _ Checker = (*HTTPChecker)(nil)
