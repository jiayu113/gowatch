package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebhookNotifier_Success(t *testing.T) {
	var got Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL)
	ev := Event{RuleName: "test-rule", Target: "github", Reason: "test"}
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	if got.RuleName != "test-rule" {
		t.Errorf("rule name not transmitted: got=%s", got.RuleName)
	}
}

func TestWebhookNotifier_5xxRetries(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL)
	err := n.Notify(context.Background(), Event{})
	if err == nil {
		t.Errorf("should fail after retries")
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 + 1 retry), got %d", calls)
	}
}

func TestWebhookNotifier_4xxNoRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL)
	_ = n.Notify(context.Background(), Event{})
	if calls != 1 {
		t.Errorf("4xx should not retry, got %d calls", calls)
	}
}
