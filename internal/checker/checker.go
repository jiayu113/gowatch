package checker

import (
	"context"
	"time"
)

const (
	StatusUp   = "up"
	StatusDown = "down"
)

type Checker interface {
	Check(ctx context.Context) Result
}

type Result struct {
	Target    string
	Status    string
	Latency   time.Duration
	Error     string
	ErrorType string
	Timestamp time.Time
}
