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
	client *http.Client
}

func NewHTTPChecker(t config.Target) *HTTPChecker {
	return &HTTPChecker{
		Target: t,
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (h *HTTPChecker) result(status, errMsg, errType string, start time.Time) Result {
	return Result{
		Target:    h.Target.Name,
		Status:    status,
		Error:     errMsg,
		ErrorType: errType,
		Timestamp: time.Now(),
		Latency:   time.Since(start),
	}
}

func (h *HTTPChecker) Check(ctx context.Context) Result {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", h.Target.URL, nil)
	if err != nil {
		return h.result(StatusDown, err.Error(), ClassifyNetErr(err), start)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return h.result(StatusDown, err.Error(), ClassifyNetErr(err), start)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return h.result(StatusUp, "", ErrTypeNone, start)
	}
	return h.result(StatusDown, fmt.Sprintf("unexpected status: %d", resp.StatusCode), ErrTypeNon2xx, start)
}

var _ Checker = (*HTTPChecker)(nil)
