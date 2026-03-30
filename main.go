package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

	"agent-wecom-ws-bridge/internal/bridge"
	"agent-wecom-ws-bridge/internal/config"
	"agent-wecom-ws-bridge/internal/runner"
	"agent-wecom-ws-bridge/internal/ws"

	"github.com/joho/godotenv"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config.load_failed", "error", err)
		os.Exit(1)
	}

	runnerClient := runner.NewClient(cfg.RunnerBaseURL, cfg.BearerToken)
	b := bridge.New(runnerClient, cfg.DefaultAgent)

	gatewayURL := cfg.BuildGatewayURL()
	slog.Info("bridge.starting",
		"gateway", gatewayURL,
		"runner", cfg.RunnerBaseURL,
		"defaultAgent", cfg.DefaultAgent,
	)

	// 健康检查 HTTP 端口（后台）
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		// 主动推送接口：curl -X POST http://127.0.0.1:11970/api/push -d '{"targetId":"userId或chatId","markdown":"你好"}'
		mux.HandleFunc("/api/push", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			var req struct {
				TargetID string `json:"targetId"`
				Markdown string `json:"markdown"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			if err := b.Push(req.TargetID, req.Markdown); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte("ok"))
		})
		if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
			slog.Error("healthz.listen_failed", "error", err)
		}
	}()

	// 主循环：连接对方 gateway，断线自动重连
	ws.DialAndKeepAlive(gatewayURL, func(conn *ws.Conn) {
		b.HandleConn(conn)
	})
}
