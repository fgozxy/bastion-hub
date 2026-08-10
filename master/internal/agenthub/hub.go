// Package agenthub manages the pool of connected agents and routes messages
// between the master and agents over websockets.
package agenthub

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"nodepanel/shared/proto"
)

// Handlers are callbacks invoked by the hub for agent lifecycle/data events.
type Handlers struct {
	OnConnect    func(nodeID string)
	OnDisconnect func(nodeID string)
	OnHello      func(nodeID string, h proto.HelloData, remoteIP string)
	OnMetrics    func(nodeID string, m proto.MetricsData)
	OnNewKeys    func(nodeID string, k proto.NewKeysData)
	OnContainers func(nodeID string, c proto.ContainersData)
}

// sub is one in-flight RPC waiter. client is the agent connection the request
// was (or will be) sent on; when that connection dies the channel is closed so
// waiters fail immediately instead of sitting until their full timeout.
type sub struct {
	client *Client
	ch     chan *proto.Envelope
}

// Hub keeps track of connected agents and pending request subscriptions.
type Hub struct {
	handlers Handlers

	mu      sync.RWMutex
	clients map[string]*Client // nodeID -> client
	subs    map[string]*sub    // reqID -> waiter
}

// New creates a Hub.
func New(h Handlers) *Hub {
	return &Hub{
		handlers: h,
		clients:  map[string]*Client{},
		subs:     map[string]*sub{},
	}
}

// Online reports whether a node's agent is currently connected.
func (h *Hub) Online(nodeID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.clients[nodeID]
	return ok
}

// Client returns the client for a node, if online.
func (h *Hub) Client(nodeID string) (*Client, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.clients[nodeID]
	return c, ok
}

// Send delivers an envelope to a node's agent. Returns error if offline.
// When env.ID matches a pending Subscribe, the waiter is bound to this
// connection so a subsequent disconnect/reconnect cancels it immediately.
func (h *Hub) Send(nodeID string, env *proto.Envelope) error {
	h.mu.Lock()
	c, ok := h.clients[nodeID]
	if ok && env != nil && env.ID != "" {
		if s, exists := h.subs[env.ID]; exists {
			s.client = c
		}
	}
	h.mu.Unlock()
	if !ok {
		return ErrOffline
	}
	return c.send(env)
}

// Subscribe registers a channel to receive inbound messages matching reqID.
// Returns the channel; caller must Unsubscribe when done. If the agent
// connection the matching Send used dies, the channel is closed so the
// receiver sees !ok (treated as "agent disconnected" by callers).
func (h *Hub) Subscribe(reqID string) chan *proto.Envelope {
	ch := make(chan *proto.Envelope, 64)
	h.mu.Lock()
	h.subs[reqID] = &sub{ch: ch}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscription (idempotent).
func (h *Hub) Unsubscribe(reqID string) {
	h.mu.Lock()
	if s, ok := h.subs[reqID]; ok {
		delete(h.subs, reqID)
		close(s.ch)
	}
	h.mu.Unlock()
}

func (h *Hub) route(env *proto.Envelope) {
	h.mu.RLock()
	s, ok := h.subs[env.ID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case s.ch <- env:
	default:
		// drop if full to avoid blocking the read pump
	}
}

// cancelClientSubs closes every waiter that was bound to c. Called when that
// connection ends (disconnect or replacement) so RPCs sent on it cannot hang
// until their caller's multi-minute timeout after a silent WS drop.
func (h *Hub) cancelClientSubs(c *Client) {
	h.mu.Lock()
	var doomed []string
	for id, s := range h.subs {
		if s.client == c {
			doomed = append(doomed, id)
		}
	}
	for _, id := range doomed {
		close(h.subs[id].ch)
		delete(h.subs, id)
	}
	h.mu.Unlock()
	if len(doomed) > 0 {
		log.Printf("agenthub: cancelled %d in-flight RPC(s) for node %s (connection closed)", len(doomed), c.nodeID)
	}
}

// ServeAgent runs the read/write pumps for an accepted agent connection.
// Blocks until the agent disconnects.
func (h *Hub) ServeAgent(conn *websocket.Conn, nodeID, remoteIP string) {
	c := &Client{
		nodeID:   nodeID,
		conn:     conn,
		out:      make(chan *proto.Envelope, 64),
		remoteIP: remoteIP,
	}
	h.mu.Lock()
	if prev := h.clients[nodeID]; prev != nil {
		_ = prev.conn.Close()
	}
	h.clients[nodeID] = c
	h.mu.Unlock()
	if h.handlers.OnConnect != nil {
		h.handlers.OnConnect(nodeID)
	}

	done := make(chan struct{})
	go c.writePump(done)
	c.readPump(h, nodeID)
	close(done)

	// Always cancel RPCs bound to THIS connection — even when a newer connection
	// has already replaced us. Requests sent on the dead socket will never get a
	// reply from the replacement agent.
	h.cancelClientSubs(c)

	disconnectedCurrent := false
	h.mu.Lock()
	if h.clients[nodeID] == c {
		delete(h.clients, nodeID)
		disconnectedCurrent = true
	}
	h.mu.Unlock()
	if disconnectedCurrent && h.handlers.OnDisconnect != nil {
		h.handlers.OnDisconnect(nodeID)
	}
}

// Client wraps a single agent websocket connection.
type Client struct {
	nodeID   string
	conn     *websocket.Conn
	out      chan *proto.Envelope
	remoteIP string
}

func (c *Client) send(env *proto.Envelope) error {
	select {
	case c.out <- env:
		return nil
	default:
		return ErrSlowConsumer
	}
}

const (
	writeWait  = 10 * time.Second
	// Match agent-side keepalive (2.4.4+): detect a silently dropped Cloudflare
	// edge before the next scheduled job fires into a half-open socket.
	pongWait   = 45 * time.Second
	pingPeriod = 20 * time.Second
)

func (c *Client) writePump(done chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case <-done:
			return
		case env, ok := <-c.out:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			b, err := json.Marshal(env)
			if err != nil {
				continue
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) readPump(h *Hub, nodeID string) {
	defer func() { _ = c.conn.Close() }()
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env proto.Envelope
		if err := json.Unmarshal(payload, &env); err != nil {
			log.Printf("agenthub: bad message from %s: %v", nodeID, err)
			continue
		}
		switch env.Type {
		case proto.MsgHello:
			var d proto.HelloData
			if decode(env.Data, &d) == nil && h.handlers.OnHello != nil {
				h.handlers.OnHello(nodeID, d, c.remoteIP)
			}
		case proto.MsgMetrics:
			var d proto.MetricsData
			if decode(env.Data, &d) == nil && h.handlers.OnMetrics != nil {
				h.handlers.OnMetrics(nodeID, d)
			}
		case proto.MsgNewKeys:
			var d proto.NewKeysData
			if decode(env.Data, &d) == nil && h.handlers.OnNewKeys != nil {
				h.handlers.OnNewKeys(nodeID, d)
			}
		case proto.MsgContainers:
			var d proto.ContainersData
			if decode(env.Data, &d) == nil && h.handlers.OnContainers != nil {
				h.handlers.OnContainers(nodeID, d)
			}
		default:
			// a reply or stream chunk for a pending request
			if env.ID != "" {
				h.route(&env)
			}
		}
	}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}
