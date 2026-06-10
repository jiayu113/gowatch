package checker

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
)

const (
	ErrTypeNone    = "" // 成功无错误
	ErrTypeTimeout = "timeout"
	ErrTypeRefused = "refused"
	ErrTypeDNS     = "dns"
	ErrTypeNon2xx  = "non_2xx"
	ErrTypeOther   = "other"
)

// ClassifyNetErr 把网络层错误归类。HTTP 的 non_2xx 不走这里，由 HTTPChecker 直接传。
func ClassifyNetErr(err error) string {
	if err == nil {
		return ErrTypeNone
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTypeTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ErrTypeDNS
	}
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) && sysErr == syscall.ECONNREFUSED {
		return ErrTypeRefused
	}
	// 字符串兜底：不同平台 errno 行为可能不一致
	if strings.Contains(strings.ToLower(err.Error()), "refused") {
		return ErrTypeRefused
	}
	return ErrTypeOther
}
