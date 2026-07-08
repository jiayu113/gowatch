package aiops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// LLM 接口化的两个理由:换模型=改配置;单测=mock
type LLM interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

type OpenAICompat struct {
	baseURL, model, key string
	hc                  *http.Client
}

func NewOpenAICompat(cfg *Config) *OpenAICompat {
	return &OpenAICompat{
		baseURL: strings.TrimRight(cfg.LLM.BaseURL, "/"),
		model:   cfg.LLM.Model,
		key:     os.Getenv(cfg.LLM.APIKeyEnv), // 只从环境变量读
		hc:      &http.Client{Timeout: cfg.LLM.Timeout},
	}
}

type chatReq struct {
	Model    string    `json:"model"`
	Messages []chatMsg `json:"messages"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *OpenAICompat) Complete(ctx context.Context, system, user string) (string, error) {
	if c.key == "" {
		return "", fmt.Errorf("api key env empty")
	}

	body, _ := json.Marshal(chatReq{Model: c.model, Messages: []chatMsg{
		{Role: "system", Content: system}, {Role: "user", Content: user},
	}})
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("llm http %d: %s", resp.StatusCode, b)
	}
	var cr chatResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", err
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("llm empty choices")
	}
	return cr.Choices[0].Message.Content, nil
}
