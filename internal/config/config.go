package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultTimeout = 5 * time.Second

type Target struct {
	Name    string        `yaml:"name"`
	Type    string        `yaml:"type"`
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

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
		if t.Type != "http" && t.Type != "tcp" {
			return nil, fmt.Errorf("config %s: target %q has invalid type %q (must be http or tcp)", path, t.Name, t.Type)
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
