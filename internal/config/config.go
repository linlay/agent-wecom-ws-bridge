package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	// 对方 gateway 的 WSS 地址
	GatewayWSURL string // 如 wss://gateway.example.com/ws/agent
	UserID       string
	Ticket       string
	AgentKey     string
	Channel      string

	// runner
	RunnerBaseURL string
	BearerToken   string
	DefaultAgent  string

	// 本地 HTTP 端口（健康检查用）
	ListenAddr string
}

func Load() (*Config, error) {
	cfg := &Config{
		GatewayWSURL:  os.Getenv("GATEWAY_WS_URL"),
		UserID:        os.Getenv("GATEWAY_USER_ID"),
		Ticket:        os.Getenv("GATEWAY_TICKET"),
		AgentKey:      os.Getenv("GATEWAY_AGENT_KEY"),
		Channel:       envOr("GATEWAY_CHANNEL", "wecom"),
		RunnerBaseURL: envOr("RUNNER_BASE_URL", "http://127.0.0.1:11949"),
		BearerToken:   os.Getenv("RUNNER_BEARER_TOKEN"),
		DefaultAgent:  envOr("DEFAULT_AGENT_KEY", "default"),
		ListenAddr:    envOr("LISTEN_ADDR", ":11970"),
	}
	cfg.RunnerBaseURL = strings.TrimRight(cfg.RunnerBaseURL, "/")
	if cfg.GatewayWSURL == "" {
		return nil, fmt.Errorf("GATEWAY_WS_URL is required")
	}
	if cfg.RunnerBaseURL == "" {
		return nil, fmt.Errorf("RUNNER_BASE_URL is required")
	}
	return cfg, nil
}

// BuildGatewayURL 拼接完整的 WSS 连接地址
func (c *Config) BuildGatewayURL() string {
	url := strings.TrimRight(c.GatewayWSURL, "/")
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	params := ""
	if c.UserID != "" {
		params += sep + "userId=" + c.UserID
		sep = "&"
	}
	if c.Ticket != "" {
		params += sep + "ticket=" + c.Ticket
		sep = "&"
	}
	if c.AgentKey != "" {
		params += sep + "agentKey=" + c.AgentKey
		sep = "&"
	}
	if c.Channel != "" {
		params += sep + "channel=" + c.Channel
	}
	return url + params
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
