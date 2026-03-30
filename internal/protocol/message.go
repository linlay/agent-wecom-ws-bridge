package protocol

import "encoding/json"

// WS 消息顶层结构
type WSMessage struct {
	Cmd      string          `json:"cmd"`
	TargetID string          `json:"targetId,omitempty"` // agentPush 用
	Body     json.RawMessage `json:"body"`
}

// cmd 常量
const (
	CmdUserMessage   = "userMessage"
	CmdAgentResponse = "agentResponse"
	CmdAgentPush     = "agentPush"
)

// userMessage body: query 类型
type QueryBody struct {
	ChatID   string `json:"chatId"`
	AgentKey string `json:"agentKey,omitempty"`
	Role     string `json:"role,omitempty"`
	Message  string `json:"message"`
	Stream   *bool  `json:"stream,omitempty"`
}

// userMessage body: submit 类型
type SubmitBody struct {
	RunID  string      `json:"runId"`
	ToolID string      `json:"toolId"`
	Params interface{} `json:"params"`
}

// agentResponse body: 直接透传 runner SSE 事件
type AgentResponseBody struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"` // 原始 SSE JSON，直接作为 body 透传
}

// agentPush body
type AgentPushBody struct {
	Markdown string `json:"markdown"`
}

// IsSubmit 判断 raw body 是否为 submit 请求（有 runId + toolId）
func IsSubmit(raw json.RawMessage) bool {
	var probe struct {
		RunID  string `json:"runId"`
		ToolID string `json:"toolId"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.RunID != "" && probe.ToolID != ""
}
