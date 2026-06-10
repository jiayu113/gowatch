package checker

import "time"

type Target struct {
	Name         string        `yaml:"name"`
	Type         string        `yaml:"type"`
	URL          string        `yaml:"url"`
	Timeout      time.Duration `yaml:"timeout"`
	CertWarnDays int           `yaml:"cert_warn_days,omitempty"`
}
