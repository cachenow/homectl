package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homectl/internal/protocol"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestPendingRequestOverflowIsBoundedAndNonBlocking(t *testing.T) {
	p := &pendingRequest{ch: make(chan protocol.Message, 1), done: make(chan struct{})}
	if delivered, overflow := p.deliver(protocol.Message{Type: "file_chunk"}); !delivered || overflow {
		t.Fatalf("first delivery=(%v,%v)", delivered, overflow)
	}
	started := time.Now()
	if delivered, overflow := p.deliver(protocol.Message{Type: "file_chunk"}); delivered || !overflow {
		t.Fatalf("overflow delivery=(%v,%v)", delivered, overflow)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("overflow delivery blocked the Agent reader")
	}
	if !errors.Is(p.failure(), errPendingBackpressure) {
		t.Fatalf("overflow error=%v", p.failure())
	}
}

func TestBrowserTerminalQueueOverflowClosesOnlyTerminal(t *testing.T) {
	term := newBrowserTerm(nil, "session", "device")
	for i := 0; i < browserTermQueueCapacity; i++ {
		if !term.enqueue(protocol.Message{Type: "term_data", Data: "x"}) {
			t.Fatalf("message %d was rejected before queue filled", i)
		}
	}
	if term.enqueue(protocol.Message{Type: "term_data", Data: "overflow"}) {
		t.Fatal("overflowing terminal message was accepted")
	}
	select {
	case <-term.done:
	default:
		t.Fatal("overflowing terminal was not closed")
	}
}

func TestHeartbeatQueueKeepsLatestUpdate(t *testing.T) {
	a := &AgentConn{heartbeats: make(chan heartbeatUpdate, 1), done: make(chan struct{})}
	a.queueHeartbeat(heartbeatUpdate{seenAt: 1})
	a.queueHeartbeat(heartbeatUpdate{seenAt: 2})
	if got := <-a.heartbeats; got.seenAt != 2 {
		t.Fatalf("queued heartbeat=%d want=2", got.seenAt)
	}
}

func TestShutdownClosesLongLivedConnectionsAndPendingRequests(t *testing.T) {
	agent := &AgentConn{done: make(chan struct{})}
	term := newBrowserTerm(nil, "session", "device")
	pending := &pendingRequest{ch: make(chan protocol.Message, 1), done: make(chan struct{})}
	s := &Server{
		agents:  map[string]*AgentConn{"device": agent},
		terms:   map[string]*BrowserTerm{"terminal": term},
		pending: map[string]*pendingRequest{"request": pending},
	}
	s.Shutdown()
	for name, done := range map[string]<-chan struct{}{
		"agent": agent.done, "terminal": term.done, "pending": pending.done,
	} {
		select {
		case <-done:
		default:
			t.Fatalf("%s remained open after shutdown", name)
		}
	}
	if !errors.Is(pending.failure(), http.ErrServerClosed) {
		t.Fatalf("pending shutdown error=%v", pending.failure())
	}
	if len(s.agents) != 0 || len(s.terms) != 0 || len(s.pending) != 0 {
		t.Fatal("shutdown retained live connection state")
	}
}

func TestSupersededAgentCannotDeliverMessages(t *testing.T) {
	current := &AgentConn{id: "device"}
	stale := &AgentConn{id: "device"}
	p := &pendingRequest{ch: make(chan protocol.Message, 1), done: make(chan struct{})}
	s := &Server{
		agents:  map[string]*AgentConn{"device": current},
		pending: map[string]*pendingRequest{"request": p},
		terms:   make(map[string]*BrowserTerm),
	}
	s.handleAgentMessage(stale, protocol.Message{Type: "command_result", RequestID: "request"})
	select {
	case m := <-p.ch:
		t.Fatalf("superseded Agent delivered %#v", m)
	default:
	}
}

func newAgentProtocolTestServer(t *testing.T) (*Server, *Store, *httptest.Server) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{
		AgentHandshakeTimeout: time.Second,
		AgentWriteTimeout:     time.Second,
		AgentOfflineTimeout:   5 * time.Second,
	}, store)
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	ts := httptest.NewServer(h)
	t.Cleanup(func() {
		ts.Close()
		_ = store.Close()
	})
	return s, store, ts
}

func dialAgentHello(t *testing.T, serverURL string, hello protocol.Message) (*websocket.Conn, protocol.Message) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/agent/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, c, hello); err != nil {
		c.CloseNow()
		t.Fatal(err)
	}
	var ack protocol.Message
	if err := wsjson.Read(ctx, c, &ack); err != nil {
		c.CloseNow()
		t.Fatal(err)
	}
	if ack.Type != "hello_ack" {
		c.CloseNow()
		t.Fatalf("handshake response=%#v", ack)
	}
	return c, ack
}

func TestEnrollmentCompatibilityAndRegisteredReconnect(t *testing.T) {
	_, store, ts := newAgentProtocolTestServer(t)
	enrollmentToken := strings.Repeat("a", protocol.EnrollmentTokenLength)
	if err := store.CreateEnrollmentToken("old-agent", "compatibility", enrollmentToken, time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	hello := protocol.Message{Type: "hello", DeviceID: "old-agent", Name: "old agent", Token: enrollmentToken}
	c, ack := dialAgentHello(t, ts.URL, hello)
	if !protocol.ValidDeviceToken(ack.DeviceToken) {
		c.CloseNow()
		t.Fatalf("generated device token=%q", ack.DeviceToken)
	}
	c.CloseNow()
	rec, err := store.Get("old-agent")
	if err != nil || rec == nil || !secureEqualBytes(rec.TokenHash, hashToken(ack.DeviceToken)) {
		t.Fatalf("enrolled record=%#v err=%v", rec, err)
	}

	hello.Token = ack.DeviceToken
	c, reconnectAck := dialAgentHello(t, ts.URL, hello)
	defer c.CloseNow()
	if reconnectAck.DeviceToken != "" {
		t.Fatalf("registered Agent received replacement token %q", reconnectAck.DeviceToken)
	}
}

func TestPreGeneratedDeviceTokenEnrollmentRecoversAfterLostAck(t *testing.T) {
	_, store, ts := newAgentProtocolTestServer(t)
	enrollmentToken := strings.Repeat("b", protocol.EnrollmentTokenLength)
	deviceToken := strings.Repeat("c", protocol.DeviceTokenLength)
	if err := store.CreateEnrollmentToken("new-agent", "ack recovery", enrollmentToken, time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	hello := protocol.Message{
		Type:            "hello",
		DeviceID:        "new-agent",
		Name:            "new agent",
		Token:           deviceToken,
		EnrollmentToken: enrollmentToken,
		Capabilities: []string{
			protocol.CapabilityFileDownloadCredits,
			protocol.CapabilityFileUploadCredits,
		},
	}
	c, ack := dialAgentHello(t, ts.URL, hello)
	if ack.DeviceToken != "" ||
		!protocol.HasCapability(ack.Capabilities, protocol.CapabilityFileDownloadCredits) ||
		!protocol.HasCapability(ack.Capabilities, protocol.CapabilityFileUploadCredits) {
		c.CloseNow()
		t.Fatalf("new protocol ack=%#v", ack)
	}
	// Simulate losing the acknowledgement by reconnecting with the locally
	// generated identity still marked pending. The one-time enrollment token is
	// already consumed, so recovery must authenticate with the Device Token.
	c.CloseNow()
	c, ack = dialAgentHello(t, ts.URL, hello)
	defer c.CloseNow()
	if ack.DeviceToken != "" {
		t.Fatalf("ACK-loss recovery replaced device token: %#v", ack)
	}
	rec, err := store.Get("new-agent")
	if err != nil || rec == nil || !secureEqualBytes(rec.TokenHash, hashToken(deviceToken)) {
		t.Fatalf("recovered record=%#v err=%v", rec, err)
	}
}
