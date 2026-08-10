package agenthub

import (
	"context"
	"time"

	"nodepanel/shared/proto"
)

// RequestOne sends env to a node's agent and waits for the first matching reply
// (by env.ID) within timeout. Used for one-shot ops (scan_ssh, backup, etc.).
func (h *Hub) RequestOne(nodeID string, env *proto.Envelope, timeout time.Duration) (*proto.Envelope, error) {
	ch := h.Subscribe(env.ID)
	defer h.Unsubscribe(env.ID)
	if err := h.Send(nodeID, env); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case msg, ok := <-ch:
		if !ok {
			// Channel closed by Unsubscribe or by cancelClientSubs when the
			// agent connection the request was sent on died mid-flight.
			return nil, ErrOffline
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ErrTimeout
	}
}
