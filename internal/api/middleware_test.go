package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockState struct {
	isLeader bool
}

func (m mockState) IsLeader() bool {
	return m.isLeader
}

func (m mockState) NodeID() string {
	return "test-node"
}

func (m mockState) Mode() string {
	return "cluster"
}

func TestRequireLeader_LeaderPassesThrough(t *testing.T) {
	state := &mockState{isLeader: true}
	next := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
	handler := RequireLeader(state, next)

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestRequireLeader_FollowerGets503(t *testing.T) {
	state := &mockState{isLeader: false}
	next := func(w http.ResponseWriter, r *http.Request) {
		t.Error("next should not be called for follower")
	}
	handler := RequireLeader(state, next)

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}

	if rec.Header().Get("X-GoWatch-Role") != "follower" {
		t.Errorf("expected X-GoWatch-Role header to be 'follower', got '%s'", rec.Header().Get("X-GoWatch-Role"))
	}

	if rec.Header().Get("X-GoWatch-Node-ID") != "test-node" {
		t.Errorf("expected X-GoWatch-Node-ID header to be 'test-node', got '%s'", rec.Header().Get("X-GoWatch-Node-ID"))
	}
}
