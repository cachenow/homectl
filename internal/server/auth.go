package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	admin, ok, err := s.store.VerifyAdminPassword(in.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if !ok || admin == nil || in.Username != admin.Username {
		time.Sleep(350 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credentials"})
		return
	}
	if admin.TOTPSecret != "" && !validTOTP(admin.TOTPSecret, in.TOTP, time.Now()) {
		time.Sleep(350 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "totp_required"})
		return
	}

	token := randomToken(32)
	expires := time.Now().Add(s.cfg.SessionTTL)
	s.mu.Lock()
	for k, exp := range s.sessions {
		if time.Now().After(exp) {
			delete(s.sessions, k)
		}
	}
	s.sessions[token] = expires
	s.mu.Unlock()
	maxAge := int(s.cfg.SessionTTL / time.Second)
	http.SetCookie(w, &http.Cookie{
		Name: "homectl_session", Value: token, Path: "/", HttpOnly: true,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge,
	})
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

func (s *Server) handleSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"allow_exec":              s.cfg.AllowExec,
		"allow_terminal":          s.cfg.AllowTerminal,
		"file_browser_enabled":    s.cfg.FileBrowserEnabled,
		"web_refresh_ms":          s.cfg.WebRefreshInterval.Milliseconds(),
		"ui_result_ttl_ms":        s.cfg.UIResultTTL.Milliseconds(),
		"max_file_transfer_bytes": s.cfg.MaxFileTransferBytes,
	})
}

func (s *Server) handleAccount(w http.ResponseWriter, _ *http.Request) {
	a, err := s.store.GetAdmin()
	if err != nil || a == nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": a.Username, "totp_enabled": a.TOTPSecret != ""})
}

func (s *Server) verifyCurrentPassword(password string) bool {
	_, ok, err := s.store.VerifyAdminPassword(password)
	return err == nil && ok
}

func (s *Server) handleChangeUsername(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		Username        string `json:"username"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if len(in.Username) < 1 || len(in.Username) > 64 {
		http.Error(w, "username must be 1-64 characters", http.StatusBadRequest)
		return
	}
	if !s.verifyCurrentPassword(in.CurrentPassword) {
		http.Error(w, "current password incorrect", http.StatusForbidden)
		return
	}
	if err := s.store.UpdateAdminUsername(in.Username); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(in.NewPassword) < 12 {
		http.Error(w, "new password must be at least 12 characters", http.StatusBadRequest)
		return
	}
	if !s.verifyCurrentPassword(in.CurrentPassword) {
		http.Error(w, "current password incorrect", http.StatusForbidden)
		return
	}
	if err := s.store.UpdateAdminPassword(in.NewPassword); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.sessions = make(map[string]time.Time)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "homectl_session", Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "reauth": true})
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.verifyCurrentPassword(in.CurrentPassword) {
		http.Error(w, "current password incorrect", http.StatusForbidden)
		return
	}
	a, err := s.store.GetAdmin()
	if err != nil || a == nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	secret, err := newTOTPSecret()
	if err != nil {
		http.Error(w, "secret generation failed", http.StatusInternalServerError)
		return
	}
	issuer := "HomeCTL"
	label := issuer + ":" + a.Username
	uri := fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30",
		url.PathEscape(label), url.QueryEscape(secret), url.QueryEscape(issuer))
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "otpauth_uri": uri})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		Secret          string `json:"secret"`
		Code            string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.verifyCurrentPassword(in.CurrentPassword) {
		http.Error(w, "current password incorrect", http.StatusForbidden)
		return
	}
	in.Secret = strings.TrimSpace(strings.ToUpper(in.Secret))
	if !validTOTP(in.Secret, in.Code, time.Now()) {
		http.Error(w, "invalid verification code", http.StatusBadRequest)
		return
	}
	if err := s.store.SetAdminTOTP(in.Secret); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		Code            string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.verifyCurrentPassword(in.CurrentPassword) {
		http.Error(w, "current password incorrect", http.StatusForbidden)
		return
	}
	a, err := s.store.GetAdmin()
	if err != nil || a == nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if a.TOTPSecret != "" && !validTOTP(a.TOTPSecret, in.Code, time.Now()) {
		http.Error(w, "invalid verification code", http.StatusBadRequest)
		return
	}
	if err := s.store.SetAdminTOTP(""); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCreateEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	in.Label = strings.TrimSpace(in.Label)
	if len(in.Label) > 100 {
		http.Error(w, "label too long", http.StatusBadRequest)
		return
	}
	_ = s.store.CleanupEnrollmentTokens()
	id := randomToken(8)
	token := randomToken(24)
	expiresAt := time.Now().Add(s.cfg.EnrollmentTokenTTL).Unix()
	if err := s.store.CreateEnrollmentToken(id, in.Label, token, expiresAt); err != nil {
		http.Error(w, "create token failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "token": token, "expires_at": expiresAt})
}
