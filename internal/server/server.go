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
	Addr                   string
	DBPath                 string
	LegacyDeviceStore      string
	AdminUsername          string
	AdminPassword          string
	CookieSecure           bool
	SessionTTL             time.Duration
	AllowExec              bool
	AllowTerminal          bool
	FileBrowserEnabled     bool
	AgentOfflineTimeout    time.Duration
	AgentHandshakeTimeout  time.Duration
	AgentWriteTimeout      time.Duration
	ActionTimeout          time.Duration
	ExecResponseTimeout    time.Duration
	FileTransferTimeout    time.Duration
	EnrollmentTokenTTL     time.Duration
	WebRefreshInterval     time.Duration
	UIResultTTL            time.Duration
	HTTPReadHeaderTimeout  time.Duration
	ShutdownTimeout        time.Duration
	FileTransferChunkBytes int
	MaxFileTransferBytes   int64
	MaxCommandLength       int
}

type Server struct {
	cfg   Config
	store *Store

	mu       sync.RWMutex
	agents   map[string]*AgentConn
	sessions map[string]time.Time
	pending  map[string]*pendingRequest
	terms    map[string]*BrowserTerm
}

type AgentConn struct {
	id   string
	conn *websocket.Conn
	mu   sync.Mutex
}

type pendingRequest struct {
	ch   chan protocol.Message
	done chan struct{}
}

type BrowserTerm struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

type deviceView struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	LastSeen int64                `json:"last_seen"`
	Info     *protocol.SystemInfo `json:"info,omitempty"`
	Online   bool                 `json:"online"`
}

func New(cfg Config, store *Store) *Server {
	return &Server{
		cfg:      cfg,
		store:    store,
		agents:   make(map[string]*AgentConn),
		sessions: make(map[string]time.Time),
		pending:  make(map[string]*pendingRequest),
		terms:    make(map[string]*BrowserTerm),
	}
}

func (s *Server) Handler(webFS http.FileSystem) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /agent/ws", s.handleAgentWS)
	mux.HandleFunc("POST /api/login", s.handleLogin)
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
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
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

func (t *BrowserTerm) send(ctx context.Context, v any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return wsjson.Write(ctx, t.conn, v)
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
	if err != nil || hello.Type != "hello" || hello.DeviceID == "" {
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
		ok, err := s.store.ConsumeEnrollmentToken(hello.Token)
		if err != nil || !ok {
			_ = c.Close(websocket.StatusPolicyViolation, "enrollment denied")
			return
		}
		newToken = randomToken(32)
		rec = &DeviceRecord{
			ID:        hello.DeviceID,
			Name:      hello.Name,
			TokenHash: hashToken(newToken),
			LastSeen:  time.Now().Unix(),
		}
		if err := s.store.Put(rec); err != nil {
			_ = c.Close(websocket.StatusInternalError, "db error")
			return
		}
	} else if !secureEqualBytes(hashToken(hello.Token), rec.TokenHash) {
		_ = c.Close(websocket.StatusPolicyViolation, "authentication denied")
		return
	}

	agent := &AgentConn{id: hello.DeviceID, conn: c}
	s.mu.Lock()
	if old := s.agents[hello.DeviceID]; old != nil {
		_ = old.conn.Close(websocket.StatusNormalClosure, "replaced")
	}
	s.agents[hello.DeviceID] = agent
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.agents[hello.DeviceID] == agent {
			delete(s.agents, hello.DeviceID)
		}
		s.mu.Unlock()
	}()

	if err := s.sendAgent(agent, protocol.Message{Type: "hello_ack", DeviceToken: newToken}); err != nil {
		return
	}
	log.Printf("agent online: %s (%s)", hello.DeviceID, hello.Name)

	for {
		readCtx, cancel := context.WithTimeout(context.Background(), s.cfg.AgentOfflineTimeout)
		var m protocol.Message
		err := wsjson.Read(readCtx, c, &m)
		cancel()
		if err != nil {
			log.Printf("agent offline: %s: %v", hello.DeviceID, err)
			return
		}
		s.handleAgentMessage(hello.DeviceID, m)
	}
}

func (s *Server) handleAgentMessage(id string, m protocol.Message) {
	if m.RequestID != "" {
		s.mu.RLock()
		p := s.pending[m.RequestID]
		s.mu.RUnlock()
		if p != nil {
			select {
			case p.ch <- m:
			case <-p.done:
			}
			return
		}
	}

	switch m.Type {
	case "heartbeat":
		rec, _ := s.store.Get(id)
		if rec == nil {
			return
		}
		rec.LastSeen = time.Now().Unix()
		if m.Name != "" {
			rec.Name = m.Name
		}
		rec.Info = m.Info
		_ = s.store.Put(rec)
	case "term_data", "term_exit":
		s.mu.RLock()
		term := s.terms[m.SessionID]
		s.mu.RUnlock()
		if term != nil {
			typ := "data"
			if m.Type == "term_exit" {
				typ = "exit"
			}
			ctx, cancel := context.WithTimeout(context.Background(), s.cfg.AgentWriteTimeout)
			_ = term.send(ctx, map[string]string{"type": typ, "data": m.Data, "error": m.Error})
			cancel()
		}
	}
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticated(r *http.Request) bool {
	c, err := r.Cookie("homectl_session")
	if err != nil || c.Value == "" {
		return false
	}
	s.mu.RLock()
	exp, ok := s.sessions[c.Value]
	s.mu.RUnlock()
	return ok && time.Now().Before(exp)
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
	p := &pendingRequest{ch: make(chan protocol.Message, 4), done: make(chan struct{})}
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
	close(p.done)
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
	case <-timer.C:
		return protocol.Message{}, errors.New("device response timeout")
	}
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
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
	term := &BrowserTerm{conn: c}
	s.mu.Lock()
	s.terms[sid] = term
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.terms, sid)
		s.mu.Unlock()
		_ = s.sendAgent(agent, protocol.Message{Type: "term_close", SessionID: sid})
	}()

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
