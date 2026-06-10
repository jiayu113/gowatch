package checker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jiayu113/gowatch/internal/config"
)

// 有效证书
func TestCertChecker_ValidCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	c := &CertChecker{
		Target:    config.Target{Name: "valid", URL: host},
		WarnDays:  14,
		TLSConfig: &tls.Config{RootCAs: pool},
	}
	got := c.Check(context.Background())
	if got.Status != StatusUp {
		t.Errorf("status=%s want up (err=%s)", got.Status, got.Error)
	}
	if got.ExpiryDays <= 14 {
		t.Errorf("expiry days=%.1f, want > 14", got.ExpiryDays)
	}
}

// 快过期的证书
func TestCertChecker_ExpiringSoon(t *testing.T) {
	cert, pool := getCert(t, 5*24*time.Hour) // 5 天后过期
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.(*tls.Conn).Handshake() // 完成 TLS 握手，才能让 checker 拿到证书信息
		conn.Close()
	}()
	c := &CertChecker{
		Target:    config.Target{Name: "expiring", URL: ln.Addr().String()},
		WarnDays:  14,
		TLSConfig: &tls.Config{RootCAs: pool},
	}
	got := c.Check(context.Background())
	if got.Status != StatusDown {
		t.Errorf("status=%s want down(应该触发 14 天预警)", got.Status)
	}
	if got.ErrorType != ErrTypeCertExpiring {
		t.Errorf("errType=%s want %s", got.ErrorType, ErrTypeCertExpiring)
	}
	if got.ExpiryDays > 14 || got.ExpiryDays <= 0 {
		t.Errorf("expiry days=%.1f, want 约 5", got.ExpiryDays)
	}
}

// 造自签证书
func getCert(t *testing.T, validFor time.Duration) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(validFor),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}
