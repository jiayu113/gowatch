package checker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/jiayu113/gowatch/internal/config"
)

func TestClassifNetErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ErrTypeNone},
		{"deadline", context.DeadlineExceeded, ErrTypeTimeout},
		{"deadline wrapped", fmt.Errorf("get: %w", context.DeadlineExceeded), ErrTypeTimeout},
		{"dns", &net.DNSError{Name: "xxx", IsNotFound: true}, ErrTypeDNS},
		{"refused via errno", &net.OpError{Err: syscall.ECONNREFUSED}, ErrTypeRefused},
		{"refused via string", errors.New("dial tcp: connection refused"), ErrTypeRefused},
		{"random", errors.New("xxx"), ErrTypeOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyNetErr(tt.err); got != tt.want {
				t.Errorf("got=%s want=%s", got, tt.want)
			}
		})
	}
}

func TestClassifyNetErr_RealDNS(t *testing.T) {
	h := NewHTTPChecker(config.Target{Name: "dns", URL: "http://no-such-host-zzz12345.invalid"})
	got := h.Check(context.Background())
	if got.ErrorType != ErrTypeDNS {
		t.Errorf("real DNS failure: got %q want %q (err=%q)", got.ErrorType, ErrTypeDNS, got.Error)
	}
}

// 真实超时: 验证 http.Client.Do 抛出的 context deadline exceeded 被包装后，仍能被归类为 timeout
func TestClassifyNetErr_RealTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer ts.Close()

	h := NewHTTPChecker(config.Target{
		Name: "slow-target",
		URL:  ts.URL,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	got := h.Check(ctx)

	if got.ErrorType != ErrTypeTimeout {
		t.Errorf("real timeout failure: got %q want %q (err=%q)", got.ErrorType, ErrTypeTimeout, got.Error)
	}
}
