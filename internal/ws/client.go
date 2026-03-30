package ws

import (
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
)

// DialAndKeepAlive 连接对方 gateway 并保持长连接，断线自动重连
func DialAndKeepAlive(url string, onConnected func(conn *Conn)) {
	for {
		slog.Info("ws.dialing", "url", url)

		raw, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			slog.Error("ws.dial_failed", "url", url, "error", err)
			time.Sleep(5 * time.Second)
			continue
		}

		slog.Info("ws.connected", "url", url)
		conn := &Conn{raw: raw}
		onConnected(conn)

		// onConnected 返回说明连接断了，等一下重连
		slog.Info("ws.disconnected", "url", url)
		time.Sleep(3 * time.Second)
	}
}
