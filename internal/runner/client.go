package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
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

// UploadParams 用于 multipart 上传给 runner /api/upload
type UploadParams struct {
	RequestID string
	ChatID    string
	FileName  string
	FileData  []byte
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

	url := c.baseURL + "/api/query"
	slog.Debug("runner.query.request", "url", url, "body", string(body))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
		slog.Error("runner.query.http_error", "url", url, "error", err)
		return fmt.Errorf("runner request: %w", err)
	}
	defer resp.Body.Close()

	slog.Debug("runner.query.response", "status", resp.StatusCode, "contentType", resp.Header.Get("Content-Type"))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		slog.Error("runner.query.status_error", "status", resp.StatusCode, "body", string(errBody))
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

	url := c.baseURL + "/api/submit"
	slog.Info("runner.submit", "runId", req.RunID, "toolId", req.ToolID)
	slog.Debug("runner.submit.request", "url", url, "body", string(body))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.bearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		slog.Error("runner.submit.http_error", "url", url, "error", err)
		return nil, fmt.Errorf("runner request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	slog.Debug("runner.submit.response", "status", resp.StatusCode, "body", string(respBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Error("runner.submit.status_error", "status", resp.StatusCode, "body", string(respBody))
		return nil, fmt.Errorf("runner /api/submit status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return json.RawMessage(respBody), nil
}

// Upload 调用 runner /api/upload，multipart 上传文件，返回响应 JSON
func (c *Client) Upload(ctx context.Context, params UploadParams) (json.RawMessage, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// requestId (必填)
	if err := writer.WriteField("requestId", params.RequestID); err != nil {
		return nil, fmt.Errorf("write requestId: %w", err)
	}
	// chatId
	if params.ChatID != "" {
		if err := writer.WriteField("chatId", params.ChatID); err != nil {
			return nil, fmt.Errorf("write chatId: %w", err)
		}
	}
	// file
	part, err := writer.CreateFormFile("file", params.FileName)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(params.FileData); err != nil {
		return nil, fmt.Errorf("write file data: %w", err)
	}
	writer.Close()

	url := c.baseURL + "/api/upload"
	slog.Info("runner.upload", "url", url, "requestId", params.RequestID, "chatId", params.ChatID, "fileName", params.FileName, "fileSize", len(params.FileData))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	if c.bearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		slog.Error("runner.upload.http_error", "url", url, "error", err)
		return nil, fmt.Errorf("runner request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	slog.Debug("runner.upload.response", "status", resp.StatusCode, "body", string(respBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Error("runner.upload.status_error", "status", resp.StatusCode, "body", string(respBody))
		return nil, fmt.Errorf("runner /api/upload status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return json.RawMessage(respBody), nil
}

// BuildURL constructs a full URL from the runner base URL and a relative path.
func (c *Client) BuildURL(relativePath string) string {
	p := strings.TrimSpace(relativePath)
	if strings.HasPrefix(strings.ToLower(p), "http://") || strings.HasPrefix(strings.ToLower(p), "https://") {
		return p
	}
	if strings.HasPrefix(p, "/") {
		return c.baseURL + p
	}
	return c.baseURL + "/" + p
}

// DownloadFile downloads a file from the given URL with runner auth.
func (c *Client) DownloadFile(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download status=%d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return data, nil
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
