package checker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
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
