package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"homectl/internal/protocol"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type Config struct {
	Addr                     string
	DBPath                   string
	LegacyDeviceStore        string
	AdminUsername            string
	AdminPassword            string
	CookieSecure             bool
	SessionTTL               time.Duration
	RememberSessionTTL       time.Duration
	PreAuthTTL               time.Duration
	PreAuthMaxAttempts       int
	PasswordMaxFailures      int
	PasswordFailureWindow    time.Duration
	PasswordLockoutDuration  time.Duration
	PasswordHashConcurrency  int
	PasswordHashQueueTimeout time.Duration
	TOTPMaxFailures          int
	TOTPFailureWindow        time.Duration
	TOTPLockoutDuration      time.Duration
	TOTPSetupTTL             time.Duration
	ClientIPHeader           string
	TrustedProxyPrefixes     []netip.Prefix
	AllowExec                bool
	AllowTerminal            bool
	FileBrowserEnabled       bool
	AgentOfflineTimeout      time.Duration
	AgentHandshakeTimeout    time.Duration
	AgentWriteTimeout        time.Duration
	ActionTimeout            time.Duration
	ExecResponseTimeout      time.Duration
	FileTransferTimeout      time.Duration
	EnrollmentTokenTTL       time.Duration
	WebRefreshInterval       time.Duration
	UIResultTTL              time.Duration
	HTTPReadHeaderTimeout    time.Duration
	ShutdownTimeout          time.Duration
	FileTransferChunkBytes   int
	MaxFileTransferBytes     int64
	MaxCommandLength         int
}

type Server struct {
	cfg   Config
	store *Store

	mu      sync.RWMutex
	agents  map[string]*AgentConn
	pending map[string]*pendingRequest
	terms   map[string]*BrowserTerm

	authMu            sync.Mutex
	preAuth           map[string]*preAuthState
	totpSetups        map[string]*totpSetupState
	passwordFailures  map[string]*authFailureState
	reauthFailures    map[string]*authFailureState
	totpFailures      authFailureState
	passwordSlots     chan struct{}
	passwordAdmission chan struct{}
}

type AgentConn struct {
	id                  string
	conn                *websocket.Conn
	mu                  sync.Mutex
	fileDownloadCredits bool
	fileUploadCredits   bool
	heartbeats          chan heartbeatUpdate
	done                chan struct{}
	stopOnce            sync.Once
}

type pendingRequest struct {
	ch    chan protocol.Message
	done  chan struct{}
	once  sync.Once
	errMu sync.Mutex
	err   error
}

type BrowserTerm struct {
	conn       *websocket.Conn
	sessionKey string
	deviceID   string
	output     chan protocol.Message
	done       chan struct{}
	closeOnce  sync.Once
}

type heartbeatUpdate struct {
	seenAt int64
	info   *protocol.SystemInfo
}

var errPendingBackpressure = errors.New("device response exceeded the bounded receiver queue")

const (
	passwordHashQueueCapacity  = 2
	browserTermQueueCapacity   = 16
	pendingQueueCapacity       = 8
	initialFileDownloadCredits = 4
	initialFileUploadCredits   = 4
)

type deviceView struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	LastSeen int64                `json:"last_seen"`
	Info     *protocol.SystemInfo `json:"info,omitempty"`
	Online   bool                 `json:"online"`
}

func New(cfg Config, store *Store) *Server {
	passwordConcurrency := max(1, cfg.PasswordHashConcurrency)
	return &Server{
		cfg:               cfg,
		store:             store,
		agents:            make(map[string]*AgentConn),
		preAuth:           make(map[string]*preAuthState),
		totpSetups:        make(map[string]*totpSetupState),
		passwordFailures:  make(map[string]*authFailureState),
		reauthFailures:    make(map[string]*authFailureState),
		passwordSlots:     make(chan struct{}, passwordConcurrency),
		passwordAdmission: make(chan struct{}, passwordConcurrency+passwordHashQueueCapacity),
		pending:           make(map[string]*pendingRequest),
		terms:             make(map[string]*BrowserTerm),
	}
}

func (s *Server) Handler(webFS http.FileSystem) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /agent/ws", s.handleAgentWS)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/login/totp", s.handleLoginTOTP)
	mux.Handle("POST /api/logout", s.requireAuth(http.HandlerFunc(s.handleLogout)))
	mux.Handle("GET /api/settings", s.requireAuth(http.HandlerFunc(s.handleSettings)))
	mux.Handle("GET /api/account", s.requireAuth(http.HandlerFunc(s.handleAccount)))
	mux.Handle("POST /api/account/username", s.requireAuth(http.HandlerFunc(s.handleChangeUsername)))
	mux.Handle("POST /api/account/password", s.requireAuth(http.HandlerFunc(s.handleChangePassword)))
	mux.Handle("POST /api/account/totp/setup", s.requireAuth(http.HandlerFunc(s.handleTOTPSetup)))
	mux.Handle("POST /api/account/totp/enable", s.requireAuth(http.HandlerFunc(s.handleTOTPEnable)))
	mux.Handle("POST /api/account/totp/disable", s.requireAuth(http.HandlerFunc(s.handleTOTPDisable)))
	mux.Handle("POST /api/enrollment-tokens", s.requireAuth(http.HandlerFunc(s.handleCreateEnrollmentToken)))
	mux.Handle("GET /api/devices", s.requireAuth(http.HandlerFunc(s.handleDevices)))
	mux.Handle("PATCH /api/device/{id}", s.requireAuth(http.HandlerFunc(s.handleUpdateDevice)))
	mux.Handle("DELETE /api/device/{id}", s.requireAuth(http.HandlerFunc(s.handleDeleteDevice)))
	mux.Handle("POST /api/device/{id}/action", s.requireAuth(http.HandlerFunc(s.handleAction)))
	mux.Handle("POST /api/device/{id}/exec", s.requireAuth(http.HandlerFunc(s.handleExec)))
	mux.Handle("GET /api/device/{id}/files", s.requireAuth(http.HandlerFunc(s.handleFileList)))
	mux.Handle("POST /api/device/{id}/files/mkdir", s.requireAuth(http.HandlerFunc(s.handleFileMkdir)))
	mux.Handle("POST /api/device/{id}/files/delete", s.requireAuth(http.HandlerFunc(s.handleFileDelete)))
	mux.Handle("POST /api/device/{id}/files/rename", s.requireAuth(http.HandlerFunc(s.handleFileRename)))
	mux.Handle("GET /api/device/{id}/files/download", s.requireAuth(http.HandlerFunc(s.handleFileDownload)))
	mux.Handle("POST /api/device/{id}/files/upload", s.requireAuth(http.HandlerFunc(s.handleFileUpload)))
	mux.HandleFunc("GET /ws/terminal/{id}", s.handleTerminalWS)
	mux.Handle("/", http.FileServer(webFS))
	return securityHeaders(browserRequestGuard(mux))
}

func browserRequestGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if r.Header.Get("X-HomeCTL-Request") != "1" {
				http.Error(w, "request rejected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; connect-src 'self' ws: wss:; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sendAgent(a *AgentConn, m protocol.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.AgentWriteTimeout)
	defer cancel()
	return wsjson.Write(ctx, a.conn, m)
}

func (a *AgentConn) stop() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		close(a.done)
		if a.conn != nil {
			_ = a.conn.CloseNow()
		}
	})
}

// Shutdown closes upgraded WebSocket connections and releases pending
// requests. net/http Shutdown alone does not close hijacked connections.
func (s *Server) Shutdown() {
	s.mu.Lock()
	agents := make([]*AgentConn, 0, len(s.agents))
	for _, agent := range s.agents {
		agents = append(agents, agent)
	}
	terms := make([]*BrowserTerm, 0, len(s.terms))
	for _, term := range s.terms {
		terms = append(terms, term)
	}
	pending := make([]*pendingRequest, 0, len(s.pending))
	for _, request := range s.pending {
		pending = append(pending, request)
	}
	clear(s.agents)
	clear(s.terms)
	clear(s.pending)
	s.mu.Unlock()

	for _, request := range pending {
		request.finish(http.ErrServerClosed)
	}
	for _, term := range terms {
		term.forceClose()
	}
	for _, agent := range agents {
		agent.stop()
	}
}

func newBrowserTerm(conn *websocket.Conn, sessionKey, deviceID string) *BrowserTerm {
	return &BrowserTerm{
		conn:       conn,
		sessionKey: sessionKey,
		deviceID:   deviceID,
		output:     make(chan protocol.Message, browserTermQueueCapacity),
		done:       make(chan struct{}),
	}
}

func (t *BrowserTerm) enqueue(m protocol.Message) bool {
	select {
	case <-t.done:
		return false
	default:
	}
	select {
	case t.output <- m:
		return true
	case <-t.done:
		return false
	default:
		t.forceClose()
		return false
	}
}

func (t *BrowserTerm) runWriter(timeout time.Duration) {
	for {
		select {
		case <-t.done:
			return
		case m := <-t.output:
			typ := "data"
			if m.Type == "term_exit" {
				typ = "exit"
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			err := wsjson.Write(ctx, t.conn, map[string]string{"type": typ, "data": m.Data, "error": m.Error})
			cancel()
			if err != nil || m.Type == "term_exit" {
				t.forceClose()
				return
			}
		}
	}
}

func (t *BrowserTerm) forceClose() {
	if t == nil {
		return
	}
	t.closeOnce.Do(func() {
		close(t.done)
		if t.conn != nil {
			_ = t.conn.CloseNow()
		}
	})
}

func (s *Server) closeBrowserTermsForSession(sessionKey string) {
	if sessionKey == "" {
		return
	}
	s.mu.RLock()
	terms := make([]*BrowserTerm, 0)
	for _, term := range s.terms {
		if term != nil && term.sessionKey == sessionKey {
			terms = append(terms, term)
		}
	}
	s.mu.RUnlock()
	for _, term := range terms {
		term.forceClose()
	}
}

func (s *Server) closeAllBrowserTerms() {
	s.mu.RLock()
	terms := make([]*BrowserTerm, 0, len(s.terms))
	for _, term := range s.terms {
		if term != nil {
			terms = append(terms, term)
		}
	}
	s.mu.RUnlock()
	for _, term := range terms {
		term.forceClose()
	}
}

func (s *Server) closeBrowserTermsForDevice(deviceID string) {
	if deviceID == "" {
		return
	}
	s.mu.RLock()
	terms := make([]*BrowserTerm, 0)
	for _, term := range s.terms {
		if term != nil && term.deviceID == deviceID {
			terms = append(terms, term)
		}
	}
	s.mu.RUnlock()
	for _, term := range terms {
		term.forceClose()
	}
}

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(2 << 20)

	handshakeCtx, cancel := context.WithTimeout(r.Context(), s.cfg.AgentHandshakeTimeout)
	var hello protocol.Message
	err = wsjson.Read(handshakeCtx, c, &hello)
	cancel()
	if err != nil || hello.Type != "hello" || !validDeviceID(hello.DeviceID) {
		_ = c.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}
	hello.Name = strings.TrimSpace(hello.Name)
	if hello.Name == "" {
		hello.Name = hello.DeviceID
	}
	if hello.Name, err = normalizeDeviceName(hello.Name); err != nil || !validCapabilities(hello.Capabilities) {
		_ = c.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}

	rec, err := s.store.Get(hello.DeviceID)
	if err != nil {
		_ = c.Close(websocket.StatusInternalError, "db error")
		return
	}
	newToken := ""
	if rec == nil {
		enrollmentToken := hello.EnrollmentToken
		deviceToken := hello.Token
		if enrollmentToken == "" {
			if !protocol.ValidEnrollmentToken(hello.Token) {
				_ = c.Close(websocket.StatusPolicyViolation, "bad hello")
				return
			}
			enrollmentToken = hello.Token
			newToken = randomToken(32)
			deviceToken = newToken
		} else if !protocol.ValidEnrollmentToken(enrollmentToken) || !protocol.ValidDeviceToken(deviceToken) {
			_ = c.Close(websocket.StatusPolicyViolation, "bad hello")
			return
		}
		rec = &DeviceRecord{
			ID:        hello.DeviceID,
			Name:      hello.Name,
			TokenHash: hashToken(deviceToken),
			LastSeen:  time.Now().Unix(),
		}
		ok, err := s.store.EnrollDevice(enrollmentToken, rec)
		if err != nil {
			_ = c.Close(websocket.StatusInternalError, "db error")
			return
		}
		if !ok {
			_ = c.Close(websocket.StatusPolicyViolation, "enrollment denied")
			return
		}
	} else if !protocol.ValidDeviceToken(hello.Token) || (hello.EnrollmentToken != "" && !protocol.ValidEnrollmentToken(hello.EnrollmentToken)) || !secureEqualBytes(hashToken(hello.Token), rec.TokenHash) {
		_ = c.Close(websocket.StatusPolicyViolation, "authentication denied")
		return
	}

	agent := &AgentConn{
		id:                  hello.DeviceID,
		conn:                c,
		fileDownloadCredits: protocol.HasCapability(hello.Capabilities, protocol.CapabilityFileDownloadCredits),
		fileUploadCredits:   protocol.HasCapability(hello.Capabilities, protocol.CapabilityFileUploadCredits),
		heartbeats:          make(chan heartbeatUpdate, 1),
		done:                make(chan struct{}),
	}
	s.mu.Lock()
	old := s.agents[hello.DeviceID]
	s.agents[hello.DeviceID] = agent
	s.mu.Unlock()
	if old != nil {
		s.closeBrowserTermsForDevice(hello.DeviceID)
		old.stop()
	}
	go s.runHeartbeatWriter(agent)
	defer func() {
		removed := false
		s.mu.Lock()
		if s.agents[hello.DeviceID] == agent {
			delete(s.agents, hello.DeviceID)
			removed = true
		}
		s.mu.Unlock()
		if removed {
			s.closeBrowserTermsForDevice(hello.DeviceID)
		}
		agent.stop()
	}()

	ack := protocol.Message{Type: "hello_ack", DeviceToken: newToken}
	if agent.fileDownloadCredits {
		ack.Capabilities = append(ack.Capabilities, protocol.CapabilityFileDownloadCredits)
	}
	if agent.fileUploadCredits {
		ack.Capabilities = append(ack.Capabilities, protocol.CapabilityFileUploadCredits)
	}
	if err := s.sendAgent(agent, ack); err != nil {
		return
	}
	log.Printf("agent online: %s (%q)", hello.DeviceID, hello.Name)

	for {
		readCtx, cancel := context.WithTimeout(context.Background(), s.cfg.AgentOfflineTimeout)
		var m protocol.Message
		err := wsjson.Read(readCtx, c, &m)
		cancel()
		if err != nil {
			log.Printf("agent offline: %s: %v", hello.DeviceID, err)
			return
		}
		s.handleAgentMessage(agent, m)
	}
}

func validCapabilities(capabilities []string) bool {
	if len(capabilities) > 16 {
		return false
	}
	for _, capability := range capabilities {
		if len(capability) < 1 || len(capability) > 64 {
			return false
		}
	}
	return true
}

func (s *Server) runHeartbeatWriter(agent *AgentConn) {
	var lastErrorLog time.Time
	for {
		select {
		case <-agent.done:
			return
		case heartbeat := <-agent.heartbeats:
			if err := s.store.UpdateHeartbeat(agent.id, heartbeat.seenAt, heartbeat.info); err != nil && time.Since(lastErrorLog) >= time.Minute {
				log.Printf("heartbeat persistence failed for %s: %v", agent.id, err)
				lastErrorLog = time.Now()
			}
		}
	}
}

func (a *AgentConn) queueHeartbeat(heartbeat heartbeatUpdate) {
	select {
	case <-a.done:
		return
	default:
	}
	select {
	case a.heartbeats <- heartbeat:
		return
	default:
	}
	select {
	case <-a.heartbeats:
	default:
	}
	select {
	case a.heartbeats <- heartbeat:
	case <-a.done:
	default:
	}
}

func (s *Server) handleAgentMessage(agent *AgentConn, m protocol.Message) {
	s.mu.RLock()
	current := s.agents[agent.id]
	s.mu.RUnlock()
	if current != agent {
		return
	}
	if m.RequestID != "" {
		s.mu.RLock()
		p := s.pending[m.RequestID]
		s.mu.RUnlock()
		if p != nil {
			_, overflow := p.deliver(m)
			if overflow && strings.HasPrefix(m.Type, "file_") {
				go func() {
					_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: m.RequestID})
				}()
			}
			return
		}
	}

	switch m.Type {
	case "heartbeat":
		agent.queueHeartbeat(heartbeatUpdate{seenAt: time.Now().Unix(), info: m.Info})
	case "term_data", "term_exit":
		s.mu.RLock()
		term := s.terms[m.SessionID]
		s.mu.RUnlock()
		if term != nil {
			term.enqueue(m)
		}
	}
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !sameOriginRequest(r) {
			http.Error(w, "cross-site request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOriginRequest(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site")
}

func (s *Server) sessionToken(r *http.Request) string {
	for _, name := range []string{s.sessionCookieName(), "homectl_session"} {
		c, err := r.Cookie(name)
		if err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func (s *Server) authenticated(r *http.Request) bool {
	token := s.sessionToken(r)
	if token == "" {
		return false
	}
	ok, err := s.store.SessionValid(token)
	return err == nil && ok
}

func (s *Server) deviceOnline(d DeviceRecord) bool {
	if d.LastSeen == 0 || time.Since(time.Unix(d.LastSeen, 0)) > s.cfg.AgentOfflineTimeout {
		return false
	}
	s.mu.RLock()
	_, connected := s.agents[d.ID]
	s.mu.RUnlock()
	return connected
}

func (s *Server) handleDevices(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.List()
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	out := make([]deviceView, 0, len(list))
	for _, d := range list {
		out = append(out, deviceView{ID: d.ID, Name: d.Name, LastSeen: d.LastSeen, Info: d.Info, Online: s.deviceOnline(d)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "invalid device", http.StatusBadRequest)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	in.Name, err = normalizeDeviceName(in.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rec, err := s.store.Get(id)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if rec == nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	if err := s.store.UpdateDeviceName(id, in.Name); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "name": in.Name})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "invalid device", http.StatusBadRequest)
		return
	}
	rec, err := s.store.Get(id)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if rec == nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}

	if err := s.store.DeleteDevice(id); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	agent := s.agents[id]
	if agent != nil {
		delete(s.agents, id)
	}
	s.mu.Unlock()
	if agent != nil {
		s.closeBrowserTermsForDevice(id)
		agent.stop()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if in.Action != "reboot" && in.Action != "poweroff" {
		http.Error(w, "unsupported action", http.StatusBadRequest)
		return
	}
	res, err := s.request(r.PathValue("id"), protocol.Message{Type: "action", Action: in.Action}, s.cfg.ActionTimeout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowExec {
		http.Error(w, "exec disabled", http.StatusForbidden)
		return
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in.Command = strings.TrimSpace(in.Command)
	if in.Command == "" || len(in.Command) > s.cfg.MaxCommandLength {
		http.Error(w, "invalid command", http.StatusBadRequest)
		return
	}
	res, err := s.request(r.PathValue("id"), protocol.Message{Type: "exec", Command: in.Command}, s.cfg.ExecResponseTimeout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) newPending() (string, *pendingRequest) {
	id := randomToken(12)
	p := &pendingRequest{ch: make(chan protocol.Message, pendingQueueCapacity), done: make(chan struct{})}
	s.mu.Lock()
	s.pending[id] = p
	s.mu.Unlock()
	return id, p
}

func (s *Server) removePending(id string, p *pendingRequest) {
	s.mu.Lock()
	if s.pending[id] == p {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	p.finish(nil)
}

func (p *pendingRequest) finish(err error) {
	p.once.Do(func() {
		p.errMu.Lock()
		p.err = err
		p.errMu.Unlock()
		close(p.done)
	})
}

func (p *pendingRequest) failure() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	if p.err != nil {
		return p.err
	}
	return errors.New("device response stream ended")
}

func (p *pendingRequest) deliver(m protocol.Message) (delivered, overflow bool) {
	select {
	case <-p.done:
		return false, false
	default:
	}
	select {
	case p.ch <- m:
		return true, false
	case <-p.done:
		return false, false
	default:
		p.finish(errPendingBackpressure)
		return false, true
	}
}

func (s *Server) getOnlineAgent(deviceID string) (*AgentConn, error) {
	rec, err := s.store.Get(deviceID)
	if err != nil {
		return nil, errors.New("db error")
	}
	if rec == nil || !s.deviceOnline(*rec) {
		return nil, errors.New("device offline")
	}
	s.mu.RLock()
	agent := s.agents[deviceID]
	s.mu.RUnlock()
	if agent == nil {
		return nil, errors.New("device offline")
	}
	return agent, nil
}

func (s *Server) request(deviceID string, m protocol.Message, timeout time.Duration) (protocol.Message, error) {
	agent, err := s.getOnlineAgent(deviceID)
	if err != nil {
		return protocol.Message{}, err
	}
	requestID, p := s.newPending()
	defer s.removePending(requestID, p)
	m.RequestID = requestID
	if err := s.sendAgent(agent, m); err != nil {
		return protocol.Message{}, err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-p.ch:
		return res, nil
	case <-p.done:
		return protocol.Message{}, p.failure()
	case <-timer.C:
		return protocol.Message{}, errors.New("device response timeout")
	}
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	token := s.sessionToken(r)
	expiresAt, validSession, err := s.store.SessionExpiry(token)
	if err != nil {
		http.Error(w, "session check failed", http.StatusInternalServerError)
		return
	}
	if !validSession {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.cfg.AllowTerminal {
		http.Error(w, "terminal disabled", http.StatusForbidden)
		return
	}
	deviceID := r.PathValue("id")
	agent, err := s.getOnlineAgent(deviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 10)

	sid := randomToken(12)
	term := newBrowserTerm(c, string(hashToken(token)), deviceID)
	s.mu.Lock()
	s.terms[sid] = term
	s.mu.Unlock()
	go term.runWriter(s.cfg.AgentWriteTimeout)
	defer func() {
		s.mu.Lock()
		delete(s.terms, sid)
		s.mu.Unlock()
		term.forceClose()
		_ = s.sendAgent(agent, protocol.Message{Type: "term_close", SessionID: sid})
	}()

	// Revalidate after registering the terminal so a concurrent logout or
	// credential change cannot leave a newly opened terminal detached from
	// the session that authorized it.
	if ok, err := s.store.SessionValid(token); err != nil || !ok {
		term.forceClose()
		return
	}

	expiryDelay := time.Until(time.Unix(expiresAt, 0))
	if expiryDelay <= 0 {
		term.forceClose()
		return
	}
	expiryTimer := time.AfterFunc(expiryDelay, func() {
		term.forceClose()
	})
	defer expiryTimer.Stop()

	cols := parseTerminalDimension(r.URL.Query().Get("cols"), 100)
	rows := parseTerminalDimension(r.URL.Query().Get("rows"), 30)
	if err := s.sendAgent(agent, protocol.Message{Type: "term_open", SessionID: sid, Cols: cols, Rows: rows}); err != nil {
		return
	}
	for {
		var in struct {
			Type string `json:"type"`
			Data string `json:"data,omitempty"`
			Cols uint16 `json:"cols,omitempty"`
			Rows uint16 `json:"rows,omitempty"`
		}
		if err := wsjson.Read(context.Background(), c, &in); err != nil {
			return
		}
		switch in.Type {
		case "input":
			_ = s.sendAgent(agent, protocol.Message{Type: "term_input", SessionID: sid, Data: in.Data})
		case "resize":
			_ = s.sendAgent(agent, protocol.Message{Type: "term_resize", SessionID: sid, Cols: in.Cols, Rows: in.Rows})
		}
	}
}

func parseTerminalDimension(raw string, fallback uint16) uint16 {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 8 || n > 1000 {
		return fallback
	}
	return uint16(n)
}

func validDeviceID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, ch := range id {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return false
	}
	return true
}

func secureEqualBytes(a, b []byte) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
