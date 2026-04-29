package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

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
	return &config, nil
}
