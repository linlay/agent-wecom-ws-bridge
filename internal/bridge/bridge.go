package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"agent-wecom-ws-bridge/internal/protocol"
	"agent-wecom-ws-bridge/internal/runner"
	"agent-wecom-ws-bridge/internal/ws"
)

type Bridge struct {
	runner       *runner.Client
	defaultAgent string

	// 当前活跃的 gateway 连接（用于调试接口推送）
	connMu   sync.Mutex
	activeConn *ws.Conn
}

func New(runnerClient *runner.Client, defaultAgent string) *Bridge {
	return &Bridge{
		runner:       runnerClient,
		defaultAgent: defaultAgent,
	}
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

		switch msg.Cmd {
		case protocol.CmdUserMessage:
			b.handleUserMessage(conn, msg.Body)
		default:
			slog.Warn("ws.unknown_cmd", "cmd", msg.Cmd)
		}
	}
}

func (b *Bridge) handleUserMessage(conn *ws.Conn, body json.RawMessage) {
	if protocol.IsSubmit(body) {
		b.handleSubmit(conn, body)
	} else {
		b.handleQuery(conn, body)
	}
}

func (b *Bridge) handleQuery(conn *ws.Conn, body json.RawMessage) {
	var qb protocol.QueryBody
	if err := json.Unmarshal(body, &qb); err != nil {
		slog.Error("bridge.query.parse_failed", "error", err)
		b.sendError(conn, "invalid query body")
		return
	}

	agentKey := qb.AgentKey
	if agentKey == "" {
		agentKey = b.defaultAgent
	}

	req := runner.QueryRequest{
		ChatID:   qb.ChatID,
		AgentKey: agentKey,
		Role:     qb.Role,
		Message:  qb.Message,
	}

	slog.Info("bridge.query.start", "chatId", req.ChatID, "agentKey", agentKey)

	ctx := context.Background()
	err := b.runner.StreamQuery(ctx, req, func(event runner.StreamEvent) error {
		return conn.WriteJSON(protocol.WSMessage{
			Cmd:  protocol.CmdAgentResponse,
			Body: event.Raw,
		})
	})
	if err != nil {
		slog.Error("bridge.query.failed", "chatId", req.ChatID, "error", err)
		b.sendError(conn, "query failed: "+err.Error())
	}
}

func (b *Bridge) handleSubmit(conn *ws.Conn, body json.RawMessage) {
	var sb protocol.SubmitBody
	if err := json.Unmarshal(body, &sb); err != nil {
		slog.Error("bridge.submit.parse_failed", "error", err)
		b.sendError(conn, "invalid submit body")
		return
	}

	slog.Info("bridge.submit.start", "runId", sb.RunID, "toolId", sb.ToolID)

	ctx := context.Background()
	result, err := b.runner.Submit(ctx, runner.SubmitRequest{
		RunID:  sb.RunID,
		ToolID: sb.ToolID,
		Params: sb.Params,
	})
	if err != nil {
		slog.Error("bridge.submit.failed", "runId", sb.RunID, "error", err)
		b.sendError(conn, "submit failed: "+err.Error())
		return
	}

	conn.WriteJSON(protocol.WSMessage{
		Cmd:  protocol.CmdAgentResponse,
		Body: result,
	})
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

func (b *Bridge) sendError(conn *ws.Conn, message string) {
	errBody, _ := json.Marshal(map[string]string{
		"type":  "bridge.error",
		"error": message,
	})
	conn.WriteJSON(protocol.WSMessage{
		Cmd:  protocol.CmdAgentResponse,
		Body: errBody,
	})
}
