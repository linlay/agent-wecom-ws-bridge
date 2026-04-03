package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	// Peer gateway websocket endpoint.
	GatewayWSURL string // e.g. wss://gateway.example.com/ws/agent
	UserID       string
	Ticket       string
	AgentKey     string
	Channel      string

	// Runner
	RunnerBaseURL      string
	BearerToken        string
	DefaultAgent       string
	OverrideAgentKey   string // if set, override agentKey sent to runner

	// Gateway HTTP related
	GatewayBaseURL      string // e.g. http://10.76.32.255:8080
	GatewayDownloadPath string // e.g. /api/download
	GatewayUploadPath   string // e.g. /api/upload
	GatewayAuthToken    string // Bearer token for gateway upload

	// Local health check listen address.
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
		DefaultAgent:     envOr("DEFAULT_AGENT_KEY", "default"),
		OverrideAgentKey: os.Getenv("OVERRIDE_AGENT_KEY"),
		ListenAddr:    envOr("LISTEN_ADDR", ":11970"),
	}

	cfg.RunnerBaseURL = strings.TrimRight(cfg.RunnerBaseURL, "/")
	cfg.GatewayBaseURL = strings.TrimRight(envOr("AGENT_GATEWAY_BASE_URL", ""), "/")
	if cfg.GatewayBaseURL == "" {
		// Auto derive from websocket URL: ws://host:port/path -> http://host:port.
		if parsed, err := url.Parse(cfg.GatewayWSURL); err == nil {
			scheme := "http"
			if parsed.Scheme == "wss" {
				scheme = "https"
			}
			cfg.GatewayBaseURL = scheme + "://" + parsed.Host
		}
	}
	cfg.GatewayDownloadPath = envOr("AGENT_GATEWAY_DOWNLOAD_PATH", "/api/download")
	cfg.GatewayUploadPath = envOr("AGENT_GATEWAY_UPLOAD_PATH", "/api/upload")
	cfg.GatewayAuthToken = os.Getenv("AGENT_GATEWAY_AUTH_TOKEN")

	if cfg.GatewayWSURL == "" {
		return nil, fmt.Errorf("GATEWAY_WS_URL is required")
	}
	if cfg.RunnerBaseURL == "" {
		return nil, fmt.Errorf("RUNNER_BASE_URL is required")
	}
	return cfg, nil
}

// BuildGatewayURL builds full websocket URL with query params.
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
