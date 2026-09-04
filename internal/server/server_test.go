package server

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestDevicesAPIUsesStoredOrderAndClearsOfflineDynamicMetrics(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	temperature := 48.0
	for _, record := range []*DeviceRecord{
		{ID: "second", Name: "Alpha", TokenHash: hashToken("two"), LastSeen: time.Now().Unix(), Info: &protocol.SystemInfo{Hostname: "second-host", CPUUsage: 72, CPUTempC: &temperature, NetRXBPS: 100}},
		{ID: "first", Name: "Zulu", TokenHash: hashToken("one"), LastSeen: time.Now().Unix(), Info: &protocol.SystemInfo{Hostname: "first-host", CPUUsage: 31, ProcessZombie: 2}},
	} {
		if err := store.Put(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReorderDevices([]string{"first", "second"}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{AgentOfflineTimeout: time.Minute}, store)
	w := httptest.NewRecorder()
	s.handleDevices(w, httptest.NewRequest(http.MethodGet, "/api/devices", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("devices status=%d body=%s", w.Code, w.Body.String())
	}
	var response []deviceView
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response) != 2 || response[0].ID != "first" || response[1].ID != "second" {
		t.Fatalf("device API order=%#v", response)
	}
	if response[0].Online || response[0].Info.Hostname != "first-host" || response[0].Info.CPUUsage != 0 || response[0].Info.ProcessZombie != 0 {
		t.Fatalf("offline response retained dynamic values or lost inventory: %#v", response[0])
	}
	if response[1].Info.CPUTempC != nil || response[1].Info.NetRXBPS != 0 {
		t.Fatalf("offline response retained dynamic values: %#v", response[1])
	}
}

func TestMetricCardsHandlerPersistsValidatedPolicy(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Put(&DeviceRecord{ID: "device", Name: "Device", TokenHash: hashToken("token")}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{AgentWriteTimeout: time.Second}, store)
	req := httptest.NewRequest(http.MethodPut, "/api/device/device/metric-cards", bytes.NewBufferString(`{"metric_cards":["network","memory","cpu"]}`))
	req.SetPathValue("id", "device")
	w := httptest.NewRecorder()
	s.handleMetricCards(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("metric policy status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		AgentOnline     bool `json:"agent_online"`
		PolicySupported bool `json:"policy_supported"`
		AppliedOnline   bool `json:"applied_online"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AgentOnline || result.PolicySupported || result.AppliedOnline {
		t.Fatalf("offline policy result=%#v", result)
	}
	record, err := store.Get("device")
	if err != nil || record == nil {
		t.Fatalf("stored device=%#v err=%v", record, err)
	}
	want := []string{protocol.MetricCPU, protocol.MetricMemory, protocol.MetricNetwork}
	for index := range want {
		if record.MetricCards[index] != want[index] {
			t.Fatalf("stored metric cards=%v want=%v", record.MetricCards, want)
		}
	}

	bad := httptest.NewRequest(http.MethodPut, "/api/device/device/metric-cards", bytes.NewBufferString(`{"metric_cards":["cpu","memory"]}`))
	bad.SetPathValue("id", "device")
	badResponse := httptest.NewRecorder()
	s.handleMetricCards(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("two-card policy status=%d", badResponse.Code)
	}
}

func TestMetricCardsHandlerReportsOnlineLegacyAgentWithoutSending(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Put(&DeviceRecord{ID: "legacy", Name: "Legacy", TokenHash: hashToken("token")}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{AgentWriteTimeout: time.Second}, store)
	s.agents["legacy"] = &AgentConn{done: make(chan struct{})}
	req := httptest.NewRequest(http.MethodPut, "/api/device/legacy/metric-cards", bytes.NewBufferString(`{"metric_cards":["cpu","memory","disk"]}`))
	req.SetPathValue("id", "legacy")
	w := httptest.NewRecorder()
	s.handleMetricCards(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("legacy policy status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		AgentOnline     bool `json:"agent_online"`
		PolicySupported bool `json:"policy_supported"`
		AppliedOnline   bool `json:"applied_online"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.AgentOnline || result.PolicySupported || result.AppliedOnline {
		t.Fatalf("legacy policy result=%#v", result)
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
	if len(ack.MetricCards) != len(protocol.DefaultMetricCards()) {
		c.CloseNow()
		t.Fatalf("default metric policy missing from acknowledgement: %#v", ack)
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
func TestWebManifestAndIconAssetsAreServed(t *testing.T) {
	webDir := t.TempDir()

	if err := os.WriteFile(
		filepath.Join(webDir, "site.webmanifest"),
		[]byte(`{"name":"HomeCTL"}`),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	iconDir := filepath.Join(webDir, "assets", "icons")
	if err := os.MkdirAll(iconDir, 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(iconDir, "favicon.svg"),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	s := New(Config{}, nil)
	handler := s.Handler(http.FS(os.DirFS(webDir)))

	t.Run("manifest", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/site.webmanifest", nil)
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("manifest status=%d body=%s", w.Code, w.Body.String())
		}

		if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/manifest+json") {
			t.Fatalf("manifest content-type=%q", got)
		}
	})

	t.Run("nested icon", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/icons/favicon.svg", nil)
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("favicon status=%d body=%s", w.Code, w.Body.String())
		}
	})
}
