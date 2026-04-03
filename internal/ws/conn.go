package ws

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/gorilla/websocket"
)

// Conn 封装 websocket 连接，提供并发安全的写操作
type Conn struct {
	raw    *websocket.Conn
	mu     sync.Mutex
	closed bool
}

// WriteJSON 线程安全地写 JSON 消息
func (c *Conn) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return websocket.ErrCloseSent
	}
	if slog.Default().Enabled(nil, slog.LevelDebug) {
		if b, err := json.Marshal(v); err == nil {
			slog.Debug("ws.send", "data", string(b))
		}
	}
	return c.raw.WriteJSON(v)
}

// ReadJSON 读取一条 JSON 消息（阻塞）
func (c *Conn) ReadJSON(v interface{}) error {
	return c.raw.ReadJSON(v)
}

// Close 关闭连接
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return c.raw.Close()
}
