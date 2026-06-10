package checker

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"
)

const ErrTypeCertExpiring = "cert_expiring"

type CertChecker struct {
	Target    Target      // 目标
	WarnDays  int         // 剩余天数低于这个值算 down, 默认 14
	TLSConfig *tls.Config // nil = 默认安全校验; 测试时注入自定义 RootCAs 或 InsecureSkipVerify
}

func (c *CertChecker) result(status, errMsg, errType string, days float64, start time.Time) Result {
	return Result{
		Target:     c.Target.Name,
		Status:     status,
		Error:      errMsg,
		ErrorType:  errType,
		Timestamp:  time.Now(),
		Latency:    time.Since(start),
		ExpiryDays: days,
	}
}

func (c *CertChecker) Check(ctx context.Context) Result {
	start := time.Now()
	cfg := c.TLSConfig
	if cfg == nil {
		cfg = &tls.Config{} // 默认会校验证书链 + hostname
	}
	dialer := &tls.Dialer{Config: cfg}
	conn, err := dialer.DialContext(ctx, "tcp", c.Target.URL)
	if err != nil {
		return c.result(StatusDown, err.Error(), ClassifyNetErr(err), 0, start)
	}
	defer conn.Close()

	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return c.result(StatusDown, "no peer certificate", ErrTypeOther, 0, start)
	}
	left := certs[0]
	days := time.Until(left.NotAfter).Hours() / 24

	if days < float64(c.WarnDays) {
		return c.result(StatusDown, fmt.Sprintf("cert expires in %.1f days (warn=%d)", days, c.WarnDays), ErrTypeCertExpiring, days, start)
	}
	return c.result(StatusUp, "", "", days, start)
}

var _ Checker = (*CertChecker)(nil) // 编译时检查 certChecker 是否实现了 Checker 接口
