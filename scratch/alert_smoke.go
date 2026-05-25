package main

import (
	"fmt"
	"time"

	"github.com/jiayu113/gowatch/internal/alert"
	"github.com/jiayu113/gowatch/internal/checker"
)

func main() {
	w := alert.NewWindow(50)
	for i := 0; i < 5; i++ {
		w.Push(checker.Result{
			Target:    "github-home",
			Status:    "down",
			ErrorType: "timeout",
			Timestamp: time.Now().Add(-time.Duration(5-i) * time.Second),
		})
	}
	rule := alert.Rule{
		Name:      "gh-flapping",
		Target:    "github-home",
		Type:      "consecutive_status",
		Status:    "down",
		Threshold: 3,
	}
	hit, reason := rule.Match(w.Snapshot("github-home"))
	fmt.Printf("[consecutive_status] hit=%v reason=%s\n", hit, reason)
}
