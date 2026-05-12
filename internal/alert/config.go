package alert

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Rules []Rule `yaml:"rules"`
}

func LoadFromFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read alerts %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("unmarshal alerts %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) Validate() error {
	seen := make(map[string]bool)
	validTypes := map[string]bool{
		"consecutive_status": true, "consecutive_error_type": true, "error_rate_window": true,
	}
	for i, r := range c.Rules {
		if r.Name == "" {
			return fmt.Errorf("rule #%d missing name", i)
		}
		if seen[r.Name] {
			return fmt.Errorf("duplicate rule name %q", r.Name)
		}
		seen[r.Name] = true
		if r.Target == "" {
			return fmt.Errorf("rule %q missing target (use \"*\" for all)", r.Name)
		}
		if !validTypes[r.Type] {
			return fmt.Errorf("rule %q invalid type %q", r.Name, r.Type)
		}
		if r.Threshold <= 0 {
			return fmt.Errorf("rule %q threshold must be >0 ", r.Name)
		}
		if r.Webhook == "" {
			return fmt.Errorf("rule %q missing webhook", r.Name)
		}
		// type 特定的字段校验
		switch r.Type {
		case "consecutive_status":
			if r.Status != "up" && r.Status != "down" {
				return fmt.Errorf("rule %q: consecutive_status requires status=up|down", r.Name)
			}
		case "consecutive_error_type":
			if r.ErrorType == "" {
				return fmt.Errorf("rule %q: consecutive_error_type requires error_type", r.Name)
			}
		case "error_rate_window":
			if r.Window <= 0 {
				return fmt.Errorf("rule %q: error_rate_window requires window > 0", r.Name)
			}
			if r.Threshold > 100 {
				return fmt.Errorf("rule %q: threshold (%d) is percentage, must be <=100", r.Name, r.Threshold)
			}

		}
	}
	return nil
}
