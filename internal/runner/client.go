package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

type Client struct {
	baseURL     string
	bearerToken string
	http        *http.Client
}

func NewClient(baseURL, bearerToken string) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		bearerToken: strings.TrimSpace(bearerToken),
		http:        &http.Client{Timeout: 0},
	}
}

// QueryRequest 对应 runner POST /api/query
type QueryRequest struct {
	ChatID   string `json:"chatId"`
	AgentKey string `json:"agentKey,omitempty"`
	Role     string `json:"role,omitempty"`
	Message  string `json:"message"`
	Stream   bool   `json:"stream"`
}

// SubmitRequest 对应 runner POST /api/submit
type SubmitRequest struct {
	RunID  string      `json:"runId"`
	ToolID string      `json:"toolId"`
	Params interface{} `json:"params"`
}

// StreamEvent 表示 runner 返回的一个 SSE 事件
type StreamEvent struct {
	Raw json.RawMessage // 原始 JSON，直接透传给 WS 客户端
}

// StreamQuery 调用 runner /api/query，流式读取 SSE 并逐事件回调
func (c *Client) StreamQuery(ctx context.Context, req QueryRequest, onEvent func(StreamEvent) error) error {
	req.Stream = true
	if req.Role == "" {
		req.Role = "user"
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal query: %w", err)
	}

	slog.Info("runner.query", "chatId", req.ChatID, "agentKey", req.AgentKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/query", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.bearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("runner request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("runner /api/query status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	return parseSSE(resp.Body, onEvent)
}

// Submit 调用 runner /api/submit，返回响应 body
func (c *Client) Submit(ctx context.Context, req SubmitRequest) (json.RawMessage, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal submit: %w", err)
	}

	slog.Info("runner.submit", "runId", req.RunID, "toolId", req.ToolID)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/submit", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.bearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("runner request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("runner /api/submit status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return json.RawMessage(respBody), nil
}

func parseSSE(r io.Reader, onEvent func(StreamEvent) error) error {
	reader := bufio.NewReaderSize(r, 128*1024)
	var dataLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			if len(dataLines) > 0 {
				payload := strings.Join(dataLines, "\n")
				dataLines = dataLines[:0]
				if payload != "[DONE]" {
					if err := onEvent(StreamEvent{Raw: json.RawMessage(payload)}); err != nil {
						return err
					}
				}
			}
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}

		if err == io.EOF {
			if len(dataLines) > 0 {
				payload := strings.Join(dataLines, "\n")
				if payload != "[DONE]" {
					if err := onEvent(StreamEvent{Raw: json.RawMessage(payload)}); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
}
