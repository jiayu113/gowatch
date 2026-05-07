package checker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jiayu113/gowatch/internal/config"
)

func TestHTTPChecker_Check(t *testing.T) {
	tests := []struct {
		name        string
		handler     http.HandlerFunc
		ctxTimeout  time.Duration // 0表示不设超时
		wantStatus  string
		wantErrType string
	}{
		{
			name: "2xx success",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			wantStatus:  StatusUp,
			wantErrType: ErrTypeNone,
		},
		{
			name: "5xx returns non_2xx",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantStatus:  StatusDown,
			wantErrType: ErrTypeNon2xx,
		},
		{
			name: "slow response triggers ctx timeout",
			handler: func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(500 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
			},
			ctxTimeout:  100 * time.Millisecond,
			wantStatus:  StatusDown,
			wantErrType: ErrTypeTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			h := &HTTPChecker{
				Target: config.Target{
					Name: tt.name,
					URL:  srv.URL,
				},
			}

			ctx := context.Background()
			if tt.ctxTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.ctxTimeout)
				defer cancel()
			}

			got := h.Check(ctx)

			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q ,want %q (err=%q)", got.Status, tt.wantStatus, got.Error)
			}
			if got.ErrorType != tt.wantErrType {
				t.Errorf("ErrorType = %q ,want %q (err=%q)", got.ErrorType, tt.wantErrType, got.Error)
			}
			if got.Target != tt.name {
				t.Errorf("Target = %q ,want %q", got.Target, tt.name)
			}
			if got.Latency <= 0 {
				t.Errorf("Latency should be > 0,got %v", got.Latency)
			}
		})
	}
}

func TestHTTPChecker_Check_ConnectionRefused(t *testing.T) {
	// 起服务拿到 URL，然后立刻关。端口不再监听 → 后续连接得到 ECONNREFUSED。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	h := &HTTPChecker{
		Target: config.Target{Name: "refused", URL: url},
	}

	got := h.Check(context.Background())

	if got.Status != StatusDown {
		t.Errorf("Status = %q, want %q (err=%q)", got.Status, StatusDown, got.Error)
	}
	if got.ErrorType != ErrTypeRefused {
		t.Errorf("ErrorType = %q, want %q (err=%q)", got.ErrorType, ErrTypeRefused, got.Error)
	}
}
