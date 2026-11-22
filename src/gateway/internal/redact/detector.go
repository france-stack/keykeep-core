package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// HTTPDetector 调用本地 PII 检测器（可选）。请求/响应均为 JSON：
//
//	POST {"text":"..."} -> {"spans":[{"start":0,"end":3}]}
//
// 模型文件不随首版交付；未配置 URL 时由 block 预设拒绝请求（ADR-008）。
type HTTPDetector struct {
	URL    string
	Client *http.Client
}

func NewHTTPDetector(url string) *HTTPDetector {
	return &HTTPDetector{URL: url, Client: &http.Client{Timeout: 3 * time.Second}}
}

func (d *HTTPDetector) Detect(ctx context.Context, text string) ([]Span, error) {
	if d == nil || d.URL == "" {
		return nil, fmt.Errorf("detector url is empty")
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("detector status %d", resp.StatusCode)
	}
	var out struct {
		Spans []Span `json:"spans"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Spans, nil
}
