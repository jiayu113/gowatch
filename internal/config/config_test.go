package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFromFile_DefaultTimeout(t *testing.T) {
	path := writeTempConfig(t, `
targets:
  - name: a
    type: http
    url: http://example.com
  - name: b
    type: tcp
    url: 127.0.0.1:80
    timeout: 2s
`)
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Targets[0].Timeout != defaultTimeout {
		t.Errorf("target a: got %v, want %v", cfg.Targets[0].Timeout, defaultTimeout)
	}
	if cfg.Targets[1].Timeout != 2*time.Second {
		t.Errorf("target b: got %v, want 2s", cfg.Targets[1].Timeout)
	}
}

func TestLoadFromFile_Validation(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string // 期望错误信息里包含的字串
	}{
		{"empty targets", `targets: []`, "no targets"},
		{"missing name", `targets:
  - type: http
    url: http://x.com`, "missing name"},
		{"missing url", `targets:
  - name: a
    type: http`, "missing url"},
		{"invalid type", `targets:
  - name: a
    type: ftp
    url: x`, "invalid type"},
		{"duplicate name", `targets:
  - name: a
    type: http
    url: http://x.com
  - name: a
    type: tcp
    url: 127.0.0.1:80`, "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, tc.content)
			_, err := LoadFromFile(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
