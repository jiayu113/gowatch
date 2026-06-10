package config

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/jiayu113/gowatch/pkg/checker"
	"gopkg.in/yaml.v3"
)

const defaultTimeout = 5 * time.Second

type Target = checker.Target

type Config struct {
	Targets []Target
}

func LoadFromFile(path string) (*Config, error) {
	var config Config
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}
	err = yaml.Unmarshal(file, &config)
	if err != nil {
		return nil, fmt.Errorf("file unmarshal %s: %w", path, err)
	}
	if len(config.Targets) == 0 {
		return nil, fmt.Errorf("config %s: no targets defined", path)
	}
	seen := make(map[string]bool)
	for i := range config.Targets {
		t := &config.Targets[i]

		if t.Name == "" {
			return nil, fmt.Errorf("config %s: target #%d missing name", path, i)
		}
		if t.URL == "" {
			return nil, fmt.Errorf("config %s: target %q missing url", path, t.Name)
		}
		if t.Type != "http" && t.Type != "tcp" && t.Type != "cert" {
			return nil, fmt.Errorf("config %s: target %q has invalid type %q (must be http/tcp/cert)", path, t.Name, t.Type)
		}

		switch t.Type {
		case "http":
			if !strings.HasPrefix(t.URL, "http://") && !strings.HasPrefix(t.URL, "https://") {
				return nil, fmt.Errorf("config %s: target %q type=http requires url with http:// or https:// prefix, got %q", path, t.Name, t.URL)
			}
		case "tcp", "cert":
			if strings.Contains(t.URL, "://") {
				return nil, fmt.Errorf("config %s: target %q type=%s expects host:port (no scheme), got %q", path, t.Name, t.Type, t.URL)
			}
			if _, _, err := net.SplitHostPort(t.URL); err != nil {
				return nil, fmt.Errorf("config %s: target %q type=%s url must be host:port: %v", path, t.Name, t.Type, err)
			}
		}

		// 重名检查（重名会让 metrics label 冲突、让 status API 返回怪结果）
		if seen[t.Name] {
			return nil, fmt.Errorf("config %s: duplicate target name %q", path, t.Name)
		}
		seen[t.Name] = true

		// 默认值兜底
		if t.Timeout == 0 {
			t.Timeout = defaultTimeout
		}
	}
	return &config, nil
}
