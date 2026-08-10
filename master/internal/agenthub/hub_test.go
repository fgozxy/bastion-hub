package agenthub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"nodepanel/shared/proto"
)

func TestReplacingConnectionKeepsNodeOnline(t *testing.T) {
	var disconnects int32
	hub := New(Handlers{
		OnDisconnect: func(string) { atomic.AddInt32(&disconnects, 1) },
	})

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		hub.ServeAgent(conn, "node-1", "127.0.0.1")
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	if !eventually(func() bool { return hub.Online("node-1") }) {
		t.Fatal("node did not come online after first connection")
	}

	c2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	if !eventually(func() bool { return hub.Online("node-1") }) {
		t.Fatal("node went offline after replacement connection")
	}
	if got := atomic.LoadInt32(&disconnects); got != 0 {
		t.Fatalf("stale connection fired disconnect handler: got %d", got)
	}

	_ = c2.Close()
	if !eventually(func() bool { return !hub.Online("node-1") }) {
		t.Fatal("node stayed online after current connection closed")
	}
	if got := atomic.LoadInt32(&disconnects); got != 1 {
		t.Fatalf("current disconnect count = %d, want 1", got)
	}
}

func TestDisconnectCancelsInFlightRPC(t *testing.T) {
	hub := New(Handlers{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		hub.ServeAgent(conn, "node-1", "127.0.0.1")
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { return hub.Online("node-1") }) {
		t.Fatal("node did not come online")
	}

	// Drain the outbound request so writePump isn't the one that notices the
	// close — we want the readPump/disconnect path to cancel the waiter.
	go func() {
		_, _, _ = c1.ReadMessage()
	}()

	ch := hub.Subscribe("req-1")
	if err := hub.Send("node-1", &proto.Envelope{Type: "ping", ID: "req-1"}); err != nil {
		t.Fatal(err)
	}

	// Drop the agent connection. The in-flight waiter must be cancelled
	// immediately (channel closed), not left hanging until the caller's timeout.
	_ = c1.Close()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel close on disconnect, got a message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight RPC was not cancelled after agent disconnect")
	}
}

func TestReplacementCancelsRPCsOnOldConnection(t *testing.T) {
	hub := New(Handlers{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		hub.ServeAgent(conn, "node-1", "127.0.0.1")
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { return hub.Online("node-1") }) {
		t.Fatal("node did not come online")
	}
	go func() { _, _, _ = c1.ReadMessage() }()

	ch := hub.Subscribe("req-old")
	if err := hub.Send("node-1", &proto.Envelope{Type: "ping", ID: "req-old"}); err != nil {
		t.Fatal(err)
	}

	// Replacement connection — old socket is closed; its in-flight RPCs must die
	// even though the node stays Online on the new socket.
	c2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if !eventually(func() bool { return hub.Online("node-1") }) {
		t.Fatal("node offline after replacement")
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected cancel of old-connection RPC, got a message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old-connection RPC survived agent reconnect")
	}
}

func eventually(ok func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ok()
}
