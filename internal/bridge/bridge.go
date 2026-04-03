package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-wecom-ws-bridge/internal/protocol"
	"agent-wecom-ws-bridge/internal/runner"
	"agent-wecom-ws-bridge/internal/ws"
)

type Bridge struct {
	runner              *runner.Client
	defaultAgent        string
	channel             string
	gatewayBaseURL      string
	gatewayDownloadPath string
	gatewayUploadPath   string
	gatewayAuthToken    string
	overrideAgentKey    string

	// 当前活跃的 gateway 连接（用于调试接口推送）
	connMu     sync.Mutex
	activeConn *ws.Conn

	// requestId 去重
	seenMu sync.Mutex
	seen   map[string]time.Time
}

func New(
	runnerClient *runner.Client,
	defaultAgent string,
	channel string,
	gatewayBaseURL string,
	gatewayDownloadPath string,
	gatewayUploadPath string,
	gatewayAuthToken string,
	overrideAgentKey string,
) *Bridge {
	return &Bridge{
		runner:              runnerClient,
		defaultAgent:        defaultAgent,
		channel:             strings.TrimSpace(channel),
		gatewayBaseURL:      gatewayBaseURL,
		gatewayDownloadPath: gatewayDownloadPath,
		gatewayUploadPath:   gatewayUploadPath,
		gatewayAuthToken:    strings.TrimSpace(gatewayAuthToken),
		overrideAgentKey:    strings.TrimSpace(overrideAgentKey),
		seen:                make(map[string]time.Time),
	}
}

// dedup 返回 true 表示此 requestId 是重复的，应跳过
func (b *Bridge) dedup(requestID string) bool {
	if requestID == "" {
		return false
	}
	b.seenMu.Lock()
	defer b.seenMu.Unlock()
	if _, ok := b.seen[requestID]; ok {
		return true
	}
	b.seen[requestID] = time.Now()
	// 清理超过 5 分钟的旧记录
	for k, t := range b.seen {
		if time.Since(t) > 5*time.Minute {
			delete(b.seen, k)
		}
	}
	return false
}

// HandleConn 处理一个 WS 连接的完整生命周期（阻塞直到断开）
func (b *Bridge) HandleConn(conn *ws.Conn) {
	b.connMu.Lock()
	b.activeConn = conn
	b.connMu.Unlock()

	defer func() {
		b.connMu.Lock()
		b.activeConn = nil
		b.connMu.Unlock()
		conn.Close()
	}()

	for {
		var msg protocol.WSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			slog.Info("ws.read_closed", "error", err)
			return
		}

		slog.Debug("ws.recv", "cmd", msg.Cmd, "targetId", msg.TargetID, "bodyLen", len(msg.Body), "body", string(msg.Body))

		switch msg.Cmd {
		case protocol.CmdUserMessage:
			b.handleUserMessage(conn, msg.Body, msg.RequestID)
		case protocol.CmdUserUpload:
			b.handleUpload(conn, msg.Body)
		default:
			slog.Warn("ws.unknown_cmd", "cmd", msg.Cmd, "body", string(msg.Body))
		}
	}
}

func (b *Bridge) handleUserMessage(conn *ws.Conn, body json.RawMessage, envelopeRequestID string) {
	isSubmit := protocol.IsSubmit(body)
	slog.Debug("bridge.route", "isSubmit", isSubmit, "body", string(body))
	if isSubmit {
		b.handleSubmit(conn, body, envelopeRequestID)
	} else {
		b.handleQuery(conn, body, envelopeRequestID)
	}
}

func (b *Bridge) handleQuery(conn *ws.Conn, body json.RawMessage, envelopeRequestID string) {
	var qb protocol.QueryBody
	if err := json.Unmarshal(body, &qb); err != nil {
		slog.Error("bridge.query.parse_failed", "error", err)
		b.sendError(conn, "invalid query body")
		return
	}

	agentKey := qb.AgentKey
	if b.overrideAgentKey != "" {
		agentKey = b.overrideAgentKey
	} else if agentKey == "" {
		agentKey = b.defaultAgent
	}

	req := runner.QueryRequest{
		ChatID:   qb.ChatID,
		AgentKey: agentKey,
		Role:     qb.Role,
		Message:  qb.Message,
	}

	requestID := strings.TrimSpace(envelopeRequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(qb.RequestID)
	}
	if requestID == "" {
		requestID = strings.TrimSpace(qb.RunID)
	}
	slog.Info("bridge.query.start", "chatId", req.ChatID, "agentKey", agentKey, "runId", qb.RunID, "requestId", requestID, "role", req.Role, "message", req.Message)

	ctx := context.Background()
	eventCount := 0
	err := b.runner.StreamQuery(ctx, req, func(event runner.StreamEvent) error {
		eventCount++
		forwardBody := b.normalizeRunnerEvent(req.ChatID, event.Raw)
		slog.Debug("bridge.query.sse_event", "chatId", req.ChatID, "eventNum", eventCount, "raw", string(forwardBody))
		return conn.WriteJSON(protocol.WSMessage{
			Cmd:       protocol.CmdAgentResponse,
			RequestID: requestID,
			Body:      forwardBody,
		})
	})
	if err != nil {
		slog.Error("bridge.query.failed", "chatId", req.ChatID, "error", err, "eventsReceived", eventCount)
		b.sendError(conn, "query failed: "+err.Error(), requestID)
	} else {
		slog.Info("bridge.query.done", "chatId", req.ChatID, "totalEvents", eventCount)
	}
}

func (b *Bridge) normalizeRunnerEvent(chatID string, raw json.RawMessage) json.RawMessage {
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Warn("bridge.query.event_parse_failed", "chatId", chatID, "error", err)
		return raw
	}

	eventType, _ := payload["type"].(string)
	if eventType != "artifact.publish" {
		return raw
	}

	artifact, ok := payload["artifact"].(map[string]interface{})
	if !ok {
		slog.Warn("bridge.query.artifact_publish_missing_artifact", "chatId", chatID)
		return raw
	}

	originalURL, _ := artifact["url"].(string)
	rewrittenURL := b.rewriteArtifactURL(originalURL)
	artifact["url"] = rewrittenURL
	payload["artifact"] = artifact

	normalized, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("bridge.query.artifact_publish_marshal_failed", "chatId", chatID, "error", err)
		return raw
	}

	slog.Info(
		"bridge.query.artifact_publish",
		"chatId", chatID,
		"originalUrl", originalURL,
		"rewrittenUrl", rewrittenURL,
		"urlChanged", originalURL != rewrittenURL,
	)

	// Best-effort chain: download runner artifact then upload to gateway.
	// Use original (runner-relative) URL for downloading, not the rewritten gateway URL.
	go b.forwardArtifactToGateway(payload, originalURL)
	return json.RawMessage(normalized)
}

func (b *Bridge) rewriteArtifactURL(rawURL string) string {
	url := strings.TrimSpace(rawURL)
	if url == "" {
		return rawURL
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return url
	}
	if b.gatewayBaseURL == "" {
		return url
	}
	if strings.HasPrefix(url, "/") {
		return b.gatewayBaseURL + url
	}
	return b.gatewayBaseURL + "/" + strings.TrimLeft(url, "/")
}

func (b *Bridge) forwardArtifactToGateway(event map[string]interface{}, originalArtifactURL string) {
	if strings.TrimSpace(b.gatewayBaseURL) == "" || strings.TrimSpace(b.gatewayUploadPath) == "" {
		slog.Debug("bridge.artifact.upload.skip", "reason", "gateway upload endpoint not configured")
		return
	}

	chatID, _ := event["chatId"].(string)
	artifactID, _ := event["artifactId"].(string)
	artifact, ok := event["artifact"].(map[string]interface{})
	if !ok {
		slog.Warn("bridge.artifact.upload.skip", "reason", "missing artifact object", "chatId", chatID, "artifactId", artifactID)
		return
	}

	// Use the original runner-relative URL to build the download URL from runner.
	artifactURL := originalArtifactURL
	fileName, _ := artifact["name"].(string)
	fileType, _ := artifact["type"].(string)
	if fileType == "" {
		fileType = "file"
	}
	if artifactID == "" {
		artifactID = fmt.Sprintf("%s-%d", fileName, time.Now().UnixMilli())
	}
	if fileName == "" {
		fileName = "artifact.bin"
	}
	if strings.TrimSpace(artifactURL) == "" {
		slog.Warn("bridge.artifact.upload.skip", "reason", "empty artifact url", "chatId", chatID, "artifactId", artifactID)
		return
	}

	// Build full download URL from runner base URL + original relative path.
	downloadURL := b.runner.BuildURL(artifactURL)
	fileData, err := b.runner.DownloadFile(downloadURL)
	if err != nil {
		slog.Error("bridge.artifact.upload.download_failed", "chatId", chatID, "artifactId", artifactID, "url", artifactURL, "error", err)
		return
	}
	uploadURL := b.gatewayBaseURL + "/" + strings.TrimLeft(path.Clean("/"+b.gatewayUploadPath), "/")
	respBody, err := b.uploadToGateway(uploadURL, chatID, fileName, fileType, artifactID, fileData)
	if err != nil {
		slog.Error("bridge.artifact.upload_failed", "chatId", chatID, "artifactId", artifactID, "url", uploadURL, "error", err)
		return
	}
	slog.Info("bridge.artifact.upload_done", "chatId", chatID, "artifactId", artifactID, "fileName", fileName, "sizeBytes", len(fileData), "uploadUrl", uploadURL, "response", string(respBody))
}

func (b *Bridge) uploadToGateway(uploadURL string, chatID string, fileName string, fileType string, requestID string, fileData []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	_ = writer.WriteField("chatId", chatID)
	if fileName != "" {
		_ = writer.WriteField("name", fileName)
	}
	if fileType != "" {
		_ = writer.WriteField("type", fileType)
	}
	if requestID != "" {
		_ = writer.WriteField("requestId", requestID)
	}

	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return nil, fmt.Errorf("write form file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close writer: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, uploadURL, &buf)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if b.gatewayAuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.gatewayAuthToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, fmt.Errorf("gateway upload status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func toInt64(value interface{}) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case int32:
		return int64(v)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func (b *Bridge) handleSubmit(conn *ws.Conn, body json.RawMessage, envelopeRequestID string) {
	var sb protocol.SubmitBody
	if err := json.Unmarshal(body, &sb); err != nil {
		slog.Error("bridge.submit.parse_failed", "error", err)
		b.sendError(conn, "invalid submit body")
		return
	}

	slog.Info("bridge.submit.start", "runId", sb.RunID, "toolId", sb.ToolID, "params", fmt.Sprintf("%v", sb.Params))

	ctx := context.Background()
	result, err := b.runner.Submit(ctx, runner.SubmitRequest{
		RunID:  sb.RunID,
		ToolID: sb.ToolID,
		Params: sb.Params,
	})
	if err != nil {
		slog.Error("bridge.submit.failed", "runId", sb.RunID, "toolId", sb.ToolID, "error", err)
		b.sendError(conn, "submit failed: "+err.Error(), sb.RunID)
		return
	}

	slog.Info("bridge.submit.done", "runId", sb.RunID, "toolId", sb.ToolID, "resultLen", len(result))
	slog.Debug("bridge.submit.result", "runId", sb.RunID, "result", string(result))
	requestID := strings.TrimSpace(envelopeRequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(sb.RunID)
	}
	conn.WriteJSON(protocol.WSMessage{
		Cmd:       protocol.CmdAgentResponse,
		RequestID: requestID,
		Body:      result,
	})
}

func (b *Bridge) handleUpload(conn *ws.Conn, body json.RawMessage) {
	var ub protocol.UserUploadBody
	if err := json.Unmarshal(body, &ub); err != nil {
		slog.Error("bridge.upload.parse_failed", "error", err)
		b.sendError(conn, "invalid upload body")
		return
	}

	if b.dedup(ub.RequestID) {
		slog.Debug("bridge.upload.dedup", "requestId", ub.RequestID)
		return
	}

	slog.Info("bridge.upload.start",
		"requestId", ub.RequestID,
		"chatId", ub.ChatID,
		"fileId", ub.Upload.ID,
		"fileName", ub.Upload.Name,
		"mimeType", ub.Upload.MimeType,
		"sizeBytes", ub.Upload.SizeBytes,
		"url", ub.Upload.URL,
	)

	// 1. 拼接完整下载 URL
	downloadURL := ub.Upload.URL
	if strings.HasPrefix(downloadURL, "/") {
		downloadURL = b.gatewayBaseURL + downloadURL
	} else if !strings.HasPrefix(downloadURL, "http") {
		// 相对路径（如 objectPath），用 base + downloadPath + url 拼接
		downloadURL = b.gatewayBaseURL + b.gatewayDownloadPath + "/" + downloadURL
	}
	slog.Debug("bridge.upload.download", "url", downloadURL)

	// 2. 从 gateway 下载文件
	fileData, err := b.downloadFile(downloadURL)
	if err != nil {
		slog.Error("bridge.upload.download_failed", "url", downloadURL, "error", err)
		b.sendError(conn, "download failed: "+err.Error(), ub.RequestID)
		return
	}
	slog.Info("bridge.upload.downloaded", "fileName", ub.Upload.Name, "bytes", len(fileData))

	// 3. multipart 上传给 runner
	ctx := context.Background()
	result, err := b.runner.Upload(ctx, runner.UploadParams{
		RequestID: ub.RequestID,
		ChatID:    ub.ChatID,
		FileName:  ub.Upload.Name,
		FileData:  fileData,
	})
	if err != nil {
		slog.Error("bridge.upload.runner_failed", "chatId", ub.ChatID, "error", err)
		b.sendError(conn, "upload to runner failed: "+err.Error(), ub.RequestID)
		return
	}

	slog.Info("bridge.upload.done", "chatId", ub.ChatID, "requestId", ub.RequestID, "resultLen", len(result))
	slog.Debug("bridge.upload.result", "result", string(result))

	// 4. 把 runner 响应透传回 gateway（带 requestId）
	conn.WriteJSON(protocol.WSMessage{
		Cmd:       protocol.CmdAgentResponse,
		RequestID: ub.RequestID,
		Body:      result,
	})
}

// downloadFile 从指定 URL 下载文件内容
func (b *Bridge) downloadFile(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download status=%d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024)) // 最大 100MB
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return data, nil
}

// GetActiveConn 返回当前 gateway 连接（调试用）
func (b *Bridge) GetActiveConn() *ws.Conn {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	return b.activeConn
}

// Push 通过 WSS 发送 agentPush 给 gateway
func (b *Bridge) Push(targetID string, markdown string) error {
	conn := b.GetActiveConn()
	if conn == nil {
		return fmt.Errorf("no active gateway connection")
	}
	body, _ := json.Marshal(protocol.AgentPushBody{
		Markdown: markdown,
	})
	slog.Info("bridge.push", "targetId", targetID, "markdownLen", len(markdown))
	return conn.WriteJSON(protocol.WSMessage{
		Cmd:      protocol.CmdAgentPush,
		TargetID: targetID,
		Body:     body,
	})
}

func (b *Bridge) sendError(conn *ws.Conn, message string, requestID ...string) {
	slog.Warn("bridge.send_error", "message", message)
	errBody, _ := json.Marshal(map[string]string{
		"type":  "bridge.error",
		"error": message,
	})
	msg := protocol.WSMessage{
		Cmd:  protocol.CmdAgentResponse,
		Body: errBody,
	}
	if len(requestID) > 0 && requestID[0] != "" {
		msg.RequestID = requestID[0]
	}
	conn.WriteJSON(msg)
}
