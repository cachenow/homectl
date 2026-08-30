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
	"strings"
	"sync"
	"time"

	"homectl/internal/protocol"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type Config struct {
	Addr          string
	DBPath        string
	AdminPassword string
	EnrollToken   string
	CookieSecure  bool
	SessionTTL    time.Duration
	AllowExec     bool
	AllowTerminal bool
}

type Server struct {
	cfg   Config
	store *Store

	mu       sync.RWMutex
	agents   map[string]*AgentConn
	sessions map[string]time.Time
	pending  map[string]chan protocol.Message
	terms    map[string]*BrowserTerm
}

type AgentConn struct {
	id   string
	conn *websocket.Conn
	mu   sync.Mutex
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
		cfg: cfg, store: store,
		agents: make(map[string]*AgentConn), sessions: make(map[string]time.Time),
		pending: make(map[string]chan protocol.Message), terms: make(map[string]*BrowserTerm),
	}
}

func (s *Server) Handler(webFS http.FileSystem) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /agent/ws", s.handleAgentWS)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.Handle("POST /api/logout", s.requireAuth(http.HandlerFunc(s.handleLogout)))
	mux.Handle("GET /api/devices", s.requireAuth(http.HandlerFunc(s.handleDevices)))
	mux.Handle("POST /api/device/{id}/action", s.requireAuth(http.HandlerFunc(s.handleAction)))
	mux.Handle("POST /api/device/{id}/exec", s.requireAuth(http.HandlerFunc(s.handleExec)))
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

func (a *AgentConn) send(ctx context.Context, m protocol.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
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

	ctx := r.Context()
	var hello protocol.Message
	if err := wsjson.Read(ctx, c, &hello); err != nil || hello.Type != "hello" || hello.DeviceID == "" {
		c.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}

	rec, err := s.store.Get(hello.DeviceID)
	if err != nil {
		c.Close(websocket.StatusInternalError, "db error")
		return
	}
	newToken := ""
	if rec == nil {
		if !secureEqual(hello.Token, s.cfg.EnrollToken) {
			c.Close(websocket.StatusPolicyViolation, "enrollment denied")
			return
		}
		newToken = randomToken(32)
		rec = &DeviceRecord{ID: hello.DeviceID, Name: hello.Name, Token: newToken, LastSeen: time.Now().Unix()}
		if err := s.store.Put(rec); err != nil {
			c.Close(websocket.StatusInternalError, "db error")
			return
		}
	} else if !secureEqual(hello.Token, rec.Token) {
		c.Close(websocket.StatusPolicyViolation, "authentication denied")
		return
	}

	agent := &AgentConn{id: hello.DeviceID, conn: c}
	s.mu.Lock()
	if old := s.agents[hello.DeviceID]; old != nil {
		old.conn.Close(websocket.StatusNormalClosure, "replaced")
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

	_ = agent.send(context.Background(), protocol.Message{Type: "hello_ack", DeviceToken: newToken})
	log.Printf("agent online: %s (%s)", hello.DeviceID, hello.Name)

	for {
		var m protocol.Message
		if err := wsjson.Read(context.Background(), c, &m); err != nil {
			log.Printf("agent offline: %s: %v", hello.DeviceID, err)
			return
		}
		s.handleAgentMessage(hello.DeviceID, m)
	}
}

func (s *Server) handleAgentMessage(id string, m protocol.Message) {
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
	case "command_result":
		s.mu.RLock()
		ch := s.pending[m.RequestID]
		s.mu.RUnlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
	case "term_data", "term_exit":
		s.mu.RLock()
		term := s.terms[m.SessionID]
		s.mu.RUnlock()
		if term != nil {
			typ := "data"
			if m.Type == "term_exit" {
				typ = "exit"
			}
			_ = term.send(context.Background(), map[string]string{"type": typ, "data": m.Data, "error": m.Error})
		}
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !secureEqual(in.Password, s.cfg.AdminPassword) {
		time.Sleep(350 * time.Millisecond)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := randomToken(32)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(s.cfg.SessionTTL)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "homectl_session", Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("homectl_session"); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "homectl_session", Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

func (s *Server) handleDevices(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.List()
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	s.mu.RLock()
	out := make([]deviceView, 0, len(list))
	for _, d := range list {
		_, online := s.agents[d.ID]
		out = append(out, deviceView{ID: d.ID, Name: d.Name, LastSeen: d.LastSeen, Info: d.Info, Online: online})
	}
	s.mu.RUnlock()
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
	res, err := s.request(r.PathValue("id"), protocol.Message{Type: "action", Action: in.Action}, 8*time.Second)
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
	if in.Command == "" || len(in.Command) > 4096 {
		http.Error(w, "invalid command", http.StatusBadRequest)
		return
	}
	res, err := s.request(r.PathValue("id"), protocol.Message{Type: "exec", Command: in.Command}, 40*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) request(deviceID string, m protocol.Message, timeout time.Duration) (protocol.Message, error) {
	s.mu.RLock()
	agent := s.agents[deviceID]
	s.mu.RUnlock()
	if agent == nil {
		return protocol.Message{}, errors.New("device offline")
	}
	m.RequestID = randomToken(12)
	ch := make(chan protocol.Message, 1)
	s.mu.Lock()
	s.pending[m.RequestID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, m.RequestID)
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := agent.send(ctx, m); err != nil {
		return protocol.Message{}, err
	}
	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
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
	s.mu.RLock()
	agent := s.agents[deviceID]
	s.mu.RUnlock()
	if agent == nil {
		http.Error(w, "device offline", http.StatusBadGateway)
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
		_ = agent.send(context.Background(), protocol.Message{Type: "term_close", SessionID: sid})
	}()

	if err := agent.send(context.Background(), protocol.Message{Type: "term_open", SessionID: sid, Cols: 100, Rows: 30}); err != nil {
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
			_ = agent.send(context.Background(), protocol.Message{Type: "term_input", SessionID: sid, Data: in.Data})
		case "resize":
			_ = agent.send(context.Background(), protocol.Message{Type: "term_resize", SessionID: sid, Cols: in.Cols, Rows: in.Rows})
		}
	}
}

func secureEqual(a, b string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
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
