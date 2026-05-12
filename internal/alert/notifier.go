package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Notifier 通用通知接口（明天接邮件就实现一个 EmailNotifier）
type Notifier interface {
	Notify(ctx context.Context, ev Event) error
}

// WebhookNotifier 走 JSON POST，5xx 重试 1 次，4xx 直接返回
type WebhookNotifier struct {
	url    string
	client *http.Client
}

type httpError struct {
	code      int
	retriable bool
}

func NewWebhookNotifier(url string) *WebhookNotifier {
	return &WebhookNotifier{
		url: url,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (n *WebhookNotifier) Notify(ctx context.Context, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	// 第一次尝试
	if err := n.post(ctx, body); err == nil {
		return nil
	} else if !isRetriable(err) {
		return err
	}

	// 5xx 重试 1 次，间隔 500ms
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(500 * time.Millisecond):
		return n.post(ctx, body)
	}
}

func (n *WebhookNotifier) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, "POST", n.url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err // 网络错误，会被 isRetriable 判断
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return &httpError{code: resp.StatusCode, retriable: true}
	}
	if resp.StatusCode >= 400 {
		return &httpError{code: resp.StatusCode, retriable: false}
	}
	return nil
}

func (e *httpError) Error() string { return fmt.Sprintf("webhook returned %d", e.code) }

func isRetriable(err error) bool {
	if he, ok := err.(*httpError); ok {
		return he.retriable
	}
	// 网络错误也重试
	return true
}
