package aiops

import (
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Enabled bool `yaml:"enabled"`
	LLM     struct {
		BaseURL   string        `yaml:"base_url"`
		Model     string        `yaml:"model"`
		APIKeyEnv string        `yaml:"api_key_env"`
		Timeout   time.Duration `yaml:"timeout"`
	} `yaml:"llm"`
	Limits struct {
		Cooldown     time.Duration `yaml:"cooldown"`
		DailyMax     int           `yaml:"daily_max"`
		BreakerFails int           `yaml:"breaker_fails"`
		BreakerOpen  time.Duration `yaml:"breaker_open"`
	} `yaml:"limits"`
	Output struct {
		Dir string `yaml:"dir"`
	} `yaml:"output"`
	HistoryLimit int `yaml:"history_limit"`
}

// LoadFromFile:文件不存在 → 返回 (nil, nil),调用方按"禁用"处理
func LoadFromFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.HistoryLimit <= 0 {
		c.HistoryLimit = 50
	}
	if c.Limits.DailyMax <= 0 {
		c.Limits.DailyMax = 50
	}
	if c.LLM.Timeout <= 0 {
		c.LLM.Timeout = 15 * time.Second
	}
	return &c, nil
}
