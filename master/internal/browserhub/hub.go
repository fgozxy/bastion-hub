// Package browserhub fans out live events to connected browser clients over
// a websocket (dashboard updates, command output streaming, backup progress).
package browserhub

import (
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Out is a message pushed to browsers. Type is a string the frontend switches on.
type Out struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

func NewOut(typ string, v any) Out {
	var raw json.RawMessage
	if v != nil {
		b, err := json.Marshal(v)
		if err == nil {
			raw = b
		}
	}
	return Out{Type: typ, Data: raw}
}

// Hub manages connected browser clients.
type Hub struct {
	mu      sync.RWMutex
	clients map[int64]*client
	nextID  int64
}

type client struct {
	id   int64
	conn *websocket.Conn
	send chan []byte
}

func New() *Hub {
	return &Hub{clients: map[int64]*client{}}
}

func (h *Hub) ServeBrowser(conn *websocket.Conn) {
	id := atomic.AddInt64(&h.nextID, 1)
	c := &client{id: id, conn: conn, send: make(chan []byte, 128)}
	h.mu.Lock()
	h.clients[id] = c
	h.mu.Unlock()

	done := make(chan struct{})
	go c.writePump(done)
	c.readPump(done)

	h.mu.Lock()
	delete(h.clients, id)
	h.mu.Unlock()
	close(c.send)
}

// Broadcast sends a message to all connected browsers.
func (h *Hub) Broadcast(msg Out) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	clients := make([]*client, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		select {
		case c.send <- b:
		default:
			// drop slow clients
		}
	}
}

func (c *client) writePump(done chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() { ticker.Stop(); _ = c.conn.Close() }()
	for {
		select {
		case <-done:
			return
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *client) readPump(done chan struct{}) {
	defer func() { close(done) }()
	_ = c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			if websocket.IsUnexpectedCloseError(err) {
				log.Printf("browserhub: client %d disconnected: %v", c.id, err)
			}
			return
		}
	}
}
