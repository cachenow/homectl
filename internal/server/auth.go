package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type preAuthState struct {
	Expires   time.Time
	Remember  bool
	Attempts  int
	Busy      bool
	ClientKey string
}

type totpSetupState struct {
	Secret  string
	Expires time.Time
}

type authFailureState struct {
	Failures    int
	WindowStart time.Time
	LockedUntil time.Time
	LastSeen    time.Time
}

var errAuthBusy = errors.New("authentication worker limit reached")

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	s.deletePreAuthToken(s.preAuthToken(r))
	s.clearPreAuthCookies(w)
	clientKey := s.clientKey(r)
	if retry, locked := s.clientPasswordLocked(clientKey); locked {
		s.writeAuthThrottle(w, retry)
		return
	}

	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	in.Username = strings.TrimSpace(in.Username)

	admin, ok, err := s.verifyPasswordLimited(r.Context(), in.Password)
	if err != nil {
		if errors.Is(err, errAuthBusy) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth_busy"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if !ok || admin == nil || in.Username != admin.Username {
		if retry := s.recordClientPasswordFailure(clientKey); retry > 0 {
			s.writeAuthThrottle(w, retry)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credentials"})
		return
	}

	s.clearClientPasswordFailures(clientKey)
	if admin.TOTPSecret != "" {
		if retry, locked := s.totpAuthLocked(); locked {
			s.writeAuthThrottle(w, retry)
			return
		}
		token := s.createPreAuth(in.Remember, clientKey)
		s.setPreAuthCookie(w, token)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"totp_required": true,
			"expires_in":    int(s.cfg.PreAuthTTL.Seconds()),
		})
		return
	}

	if err := s.createWebSession(w, in.Remember); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	if retry, locked := s.totpAuthLocked(); locked {
		s.clearPreAuthCookies(w)
		s.writeAuthThrottle(w, retry)
		return
	}

	var in struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	preAuthToken := s.preAuthToken(r)
	if preAuthToken == "" {
		s.clearPreAuthCookies(w)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "preauth_invalid"})
		return
	}

	key := string(hashToken(preAuthToken))
	now := time.Now()
	s.authMu.Lock()
	s.cleanupPreAuthLocked(now)
	p := s.preAuth[key]
	if p == nil {
		s.authMu.Unlock()
		s.clearPreAuthCookies(w)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "preauth_expired"})
		return
	}
	if p.Attempts >= s.cfg.PreAuthMaxAttempts {
		delete(s.preAuth, key)
		s.authMu.Unlock()
		s.clearPreAuthCookies(w)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "preauth_attempts_exceeded"})
		return
	}
	remember := p.Remember
	clientKey := p.ClientKey
	s.authMu.Unlock()

	admin, err := s.store.GetAdmin()
	if err != nil || admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	step, valid := matchTOTP(admin.TOTPSecret, in.Code, now)
	if !valid {
		exhausted := s.bumpPreAuthAttempt(key)
		if retry := s.recordTOTPAuthFailure(); retry > 0 {
			s.clearPreAuthCookies(w)
			s.writeAuthThrottle(w, retry)
			return
		}
		if exhausted {
			s.clearPreAuthCookies(w)
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "preauth_attempts_exceeded"})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_totp"})
		return
	}

	if !s.claimPreAuth(key) {
		s.clearPreAuthCookies(w)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "preauth_invalid"})
		return
	}

	consumed, err := s.createWebSessionWithTOTP(w, remember, step)
	if err != nil {
		s.releasePreAuth(key)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session_error"})
		return
	}
	s.deletePreAuthKey(key)
	s.clearPreAuthCookies(w)
	if !consumed {
		if retry := s.recordTOTPAuthFailure(); retry > 0 {
			s.writeAuthThrottle(w, retry)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_totp"})
		return
	}
	s.clearTOTPAuthFailures()
	s.clearClientPasswordFailures(clientKey)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) verifyPasswordLimited(ctx context.Context, password string) (*AdminRecord, bool, error) {
	select {
	case s.passwordAdmission <- struct{}{}:
		defer func() { <-s.passwordAdmission }()
	default:
		return nil, false, errAuthBusy
	}
	timer := time.NewTimer(s.cfg.PasswordHashQueueTimeout)
	defer timer.Stop()
	select {
	case s.passwordSlots <- struct{}{}:
		defer func() { <-s.passwordSlots }()
		return s.store.VerifyAdminPassword(password)
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-timer.C:
		return nil, false, errAuthBusy
	}
}

func (s *Server) createPreAuth(remember bool, clientKey string) string {
	token := randomToken(32)
	key := string(hashToken(token))
	now := time.Now()
	s.authMu.Lock()
	defer s.authMu.Unlock()
	s.cleanupPreAuthLocked(now)
	if len(s.preAuth) >= 32 {
		var oldestKey string
		var oldest time.Time
		for k, v := range s.preAuth {
			if oldestKey == "" || v.Expires.Before(oldest) {
				oldestKey, oldest = k, v.Expires
			}
		}
		delete(s.preAuth, oldestKey)
	}
	s.preAuth[key] = &preAuthState{Expires: now.Add(s.cfg.PreAuthTTL), Remember: remember, ClientKey: clientKey}
	return token
}

func (s *Server) cleanupPreAuthLocked(now time.Time) {
	for k, v := range s.preAuth {
		if v == nil || !now.Before(v.Expires) {
			delete(s.preAuth, k)
		}
	}
}

func (s *Server) bumpPreAuthAttempt(key string) bool {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	p := s.preAuth[key]
	if p == nil {
		return true
	}
	p.Attempts++
	if p.Attempts >= s.cfg.PreAuthMaxAttempts {
		delete(s.preAuth, key)
		return true
	}
	return false
}

func (s *Server) claimPreAuth(key string) bool {
	now := time.Now()
	s.authMu.Lock()
	defer s.authMu.Unlock()
	p := s.preAuth[key]
	if p == nil || !now.Before(p.Expires) || p.Busy || p.Attempts >= s.cfg.PreAuthMaxAttempts {
		if p != nil && !now.Before(p.Expires) {
			delete(s.preAuth, key)
		}
		return false
	}
	p.Busy = true
	return true
}

func (s *Server) releasePreAuth(key string) {
	s.authMu.Lock()
	if p := s.preAuth[key]; p != nil {
		p.Busy = false
	}
	s.authMu.Unlock()
}

func (s *Server) deletePreAuthKey(key string) {
	s.authMu.Lock()
	delete(s.preAuth, key)
	s.authMu.Unlock()
}

func (s *Server) deletePreAuthToken(token string) {
	if token == "" {
		return
	}
	s.authMu.Lock()
	delete(s.preAuth, string(hashToken(token)))
	s.authMu.Unlock()
}

func (s *Server) clientKey(r *http.Request) string {
	remote := remoteIP(r.RemoteAddr)
	if remote.IsValid() && s.cfg.ClientIPHeader != "" && s.isTrustedProxy(remote) {
		if forwarded := singleIPHeader(r.Header.Get(s.cfg.ClientIPHeader)); forwarded.IsValid() {
			return forwarded.Unmap().String()
		}
	}
	if remote.IsValid() {
		return remote.Unmap().String()
	}
	return "unknown"
}

func remoteIP(remoteAddr string) netip.Addr {
	host := strings.TrimSpace(remoteAddr)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}

func singleIPHeader(value string) netip.Addr {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, ",") {
		return netip.Addr{}
	}
	ip, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}

func (s *Server) isTrustedProxy(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, prefix := range s.cfg.TrustedProxyPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func resetExpiredFailureState(st *authFailureState, now time.Time, window time.Duration) {
	if !st.LockedUntil.IsZero() && !now.Before(st.LockedUntil) {
		*st = authFailureState{}
		return
	}
	if st.LockedUntil.IsZero() && !st.WindowStart.IsZero() && now.Sub(st.WindowStart) >= window {
		*st = authFailureState{}
	}
}

func recordFailure(st *authFailureState, now time.Time, maxFailures int, window, lockout time.Duration) time.Duration {
	resetExpiredFailureState(st, now, window)
	st.LastSeen = now
	if !st.LockedUntil.IsZero() && now.Before(st.LockedUntil) {
		return time.Until(st.LockedUntil)
	}
	if st.WindowStart.IsZero() {
		st.WindowStart = now
	}
	st.Failures++
	if st.Failures >= maxFailures {
		st.LockedUntil = now.Add(lockout)
		return lockout
	}
	return 0
}

func (s *Server) clientPasswordLocked(key string) (time.Duration, bool) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	st := s.passwordFailures[key]
	if st == nil {
		return 0, false
	}
	now := time.Now()
	resetExpiredFailureState(st, now, s.cfg.PasswordFailureWindow)
	if !st.LockedUntil.IsZero() && now.Before(st.LockedUntil) {
		return time.Until(st.LockedUntil), true
	}
	if st.Failures == 0 {
		delete(s.passwordFailures, key)
	}
	return 0, false
}

func (s *Server) recordClientPasswordFailure(key string) time.Duration {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	now := time.Now()
	for k, st := range s.passwordFailures {
		resetExpiredFailureState(st, now, s.cfg.PasswordFailureWindow)
		if st.Failures == 0 {
			delete(s.passwordFailures, k)
		}
	}
	if _, exists := s.passwordFailures[key]; !exists && len(s.passwordFailures) >= 4096 {
		var oldestKey string
		var oldest time.Time
		for k, st := range s.passwordFailures {
			stamp := st.LastSeen
			if stamp.IsZero() {
				stamp = st.WindowStart
			}
			if oldestKey == "" || stamp.Before(oldest) {
				oldestKey, oldest = k, stamp
			}
		}
		if oldestKey != "" {
			delete(s.passwordFailures, oldestKey)
		}
	}
	st := s.passwordFailures[key]
	if st == nil {
		st = &authFailureState{}
		s.passwordFailures[key] = st
	}
	return recordFailure(st, now, s.cfg.PasswordMaxFailures, s.cfg.PasswordFailureWindow, s.cfg.PasswordLockoutDuration)
}

func (s *Server) clearClientPasswordFailures(key string) {
	s.authMu.Lock()
	delete(s.passwordFailures, key)
	s.authMu.Unlock()
}

func (s *Server) reauthLocked(key string) (time.Duration, bool) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	st := s.reauthFailures[key]
	if st == nil {
		return 0, false
	}
	now := time.Now()
	resetExpiredFailureState(st, now, s.cfg.PasswordFailureWindow)
	if !st.LockedUntil.IsZero() && now.Before(st.LockedUntil) {
		return time.Until(st.LockedUntil), true
	}
	if st.Failures == 0 {
		delete(s.reauthFailures, key)
	}
	return 0, false
}

func (s *Server) recordReauthFailure(key string) time.Duration {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	now := time.Now()
	st := s.reauthFailures[key]
	if st == nil {
		st = &authFailureState{}
		s.reauthFailures[key] = st
	}
	return recordFailure(st, now, s.cfg.PasswordMaxFailures, s.cfg.PasswordFailureWindow, s.cfg.PasswordLockoutDuration)
}

func (s *Server) clearReauthFailures(key string) {
	s.authMu.Lock()
	delete(s.reauthFailures, key)
	s.authMu.Unlock()
}

func (s *Server) clearAllReauthFailures() {
	s.authMu.Lock()
	clear(s.reauthFailures)
	s.authMu.Unlock()
}

func (s *Server) totpAuthLocked() (time.Duration, bool) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	now := time.Now()
	resetExpiredFailureState(&s.totpFailures, now, s.cfg.TOTPFailureWindow)
	if !s.totpFailures.LockedUntil.IsZero() && now.Before(s.totpFailures.LockedUntil) {
		return time.Until(s.totpFailures.LockedUntil), true
	}
	return 0, false
}

func (s *Server) recordTOTPAuthFailure() time.Duration {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	retry := recordFailure(&s.totpFailures, time.Now(), s.cfg.TOTPMaxFailures, s.cfg.TOTPFailureWindow, s.cfg.TOTPLockoutDuration)
	if retry > 0 {
		for k := range s.preAuth {
			delete(s.preAuth, k)
		}
	}
	return retry
}

func (s *Server) clearTOTPAuthFailures() {
	s.authMu.Lock()
	s.totpFailures = authFailureState{}
	s.authMu.Unlock()
}

func (s *Server) clearPreAuth() {
	s.authMu.Lock()
	clear(s.preAuth)
	s.authMu.Unlock()
}

func (s *Server) writeAuthThrottle(w http.ResponseWriter, retry time.Duration) {
	seconds := max(1, int(retry.Round(time.Second)/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too_many_attempts", "retry_after": seconds})
}

func (s *Server) newSession(remember bool) (token string, expires time.Time, ttl time.Duration) {
	ttl = s.cfg.SessionTTL
	if remember {
		ttl = s.cfg.RememberSessionTTL
	}
	return randomToken(32), time.Now().Add(ttl), ttl
}

func (s *Server) createWebSession(w http.ResponseWriter, remember bool) error {
	_ = s.store.CleanupSessions()
	token, expires, ttl := s.newSession(remember)
	if err := s.store.CreateSession(token, expires.Unix(), remember); err != nil {
		return err
	}
	s.setSessionCookie(w, token, expires, ttl, remember)
	return nil
}

func (s *Server) createWebSessionWithTOTP(w http.ResponseWriter, remember bool, step int64) (bool, error) {
	_ = s.store.CleanupSessions()
	token, expires, ttl := s.newSession(remember)
	consumed, err := s.store.ConsumeTOTPLoginStepAndCreateSession(step, token, expires.Unix(), remember)
	if err != nil || !consumed {
		return consumed, err
	}
	s.setSessionCookie(w, token, expires, ttl, remember)
	return true, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time, ttl time.Duration, remember bool) {
	cookie := &http.Cookie{
		Name:     s.sessionCookieName(),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
	if remember {
		cookie.MaxAge = int(ttl / time.Second)
		cookie.Expires = expires
	}
	http.SetCookie(w, cookie)
	if s.cfg.CookieSecure {
		s.clearCookie(w, "homectl_session", "/")
	}
}

func (s *Server) sessionCookieName() string {
	if s.cfg.CookieSecure {
		return "__Host-homectl_session"
	}
	return "homectl_session"
}

func (s *Server) preAuthCookieName() string {
	if s.cfg.CookieSecure {
		return "__Secure-homectl_preauth"
	}
	return "homectl_preauth"
}

func (s *Server) setPreAuthCookie(w http.ResponseWriter, token string) {
	ttl := s.cfg.PreAuthTTL
	http.SetCookie(w, &http.Cookie{
		Name:     s.preAuthCookieName(),
		Value:    token,
		Path:     "/api/login",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   max(1, int(ttl/time.Second)),
		Expires:  time.Now().Add(ttl),
	})
	if s.cfg.CookieSecure {
		s.clearCookie(w, "homectl_preauth", "/api/login")
	}
}

func (s *Server) preAuthToken(r *http.Request) string {
	for _, name := range []string{s.preAuthCookieName(), "homectl_preauth"} {
		c, err := r.Cookie(name)
		if err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func (s *Server) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: path, HttpOnly: true,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1,
		Expires: time.Unix(1, 0),
	})
}

func (s *Server) clearSessionCookies(w http.ResponseWriter) {
	s.clearCookie(w, s.sessionCookieName(), "/")
	if s.sessionCookieName() != "homectl_session" {
		s.clearCookie(w, "homectl_session", "/")
	}
}

func (s *Server) clearPreAuthCookies(w http.ResponseWriter) {
	s.clearCookie(w, s.preAuthCookieName(), "/api/login")
	if s.preAuthCookieName() != "homectl_preauth" {
		s.clearCookie(w, "homectl_preauth", "/api/login")
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := s.sessionToken(r); token != "" {
		key := string(hashToken(token))
		_ = s.store.DeleteSession(token)
		s.clearTOTPSetupForSessionKey(key)
		s.clearReauthFailures(key)
		s.closeBrowserTermsForSession(key)
	}
	s.clearPreAuthCookies(w)
	s.clearSessionCookies(w)
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
		"password_min_chars":      minPasswordRunes,
		"password_max_chars":      maxPasswordRunes,
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

func (s *Server) verifyCurrentPassword(ctx context.Context, password string) (bool, error) {
	admin, ok, err := s.verifyPasswordLimited(ctx, password)
	if err != nil {
		return false, err
	}
	return admin != nil && ok, nil
}

func (s *Server) requireCurrentPassword(w http.ResponseWriter, r *http.Request, password string) bool {
	key := s.sessionKey(r)
	if key == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if retry, locked := s.reauthLocked(key); locked {
		s.writeAuthThrottle(w, retry)
		return false
	}
	ok, err := s.verifyCurrentPassword(r.Context(), password)
	if err != nil {
		if errors.Is(err, errAuthBusy) {
			http.Error(w, "authentication busy; try again", http.StatusServiceUnavailable)
			return false
		}
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return false
	}
	if !ok {
		if retry := s.recordReauthFailure(key); retry > 0 {
			s.writeAuthThrottle(w, retry)
			return false
		}
		http.Error(w, "current password incorrect", http.StatusForbidden)
		return false
	}
	s.clearReauthFailures(key)
	return true
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
	var err error
	in.Username, err = normalizeUsername(in.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.requireCurrentPassword(w, r, in.CurrentPassword) {
		return
	}
	if err := s.store.UpdateAdminUsernameAndRevokeSessions(in.Username); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	s.clearPreAuth()
	s.clearTOTPSetups()
	s.clearTOTPAuthFailures()
	s.clearAllReauthFailures()
	s.closeAllBrowserTerms()
	s.clearPreAuthCookies(w)
	s.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "reauth": true})
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
	if _, err := normalizeAndValidatePassword(in.NewPassword); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.requireCurrentPassword(w, r, in.CurrentPassword) {
		return
	}
	if err := s.store.UpdateAdminPasswordAndRevokeSessions(in.NewPassword); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	s.clearPreAuth()
	s.clearTOTPSetups()
	s.clearTOTPAuthFailures()
	s.clearAllReauthFailures()
	s.closeAllBrowserTerms()
	s.clearPreAuthCookies(w)
	s.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "reauth": true})
}

func (s *Server) sessionKey(r *http.Request) string {
	token := s.sessionToken(r)
	if token == "" {
		return ""
	}
	return string(hashToken(token))
}

func (s *Server) storeTOTPSetup(r *http.Request, secret string) bool {
	key := s.sessionKey(r)
	if key == "" {
		return false
	}
	now := time.Now()
	s.authMu.Lock()
	defer s.authMu.Unlock()
	for k, setup := range s.totpSetups {
		if setup == nil || !now.Before(setup.Expires) {
			delete(s.totpSetups, k)
		}
	}
	s.totpSetups[key] = &totpSetupState{Secret: secret, Expires: now.Add(s.cfg.TOTPSetupTTL)}
	return true
}

func (s *Server) loadTOTPSetup(r *http.Request) (string, bool) {
	key := s.sessionKey(r)
	if key == "" {
		return "", false
	}
	now := time.Now()
	s.authMu.Lock()
	defer s.authMu.Unlock()
	setup := s.totpSetups[key]
	if setup == nil || !now.Before(setup.Expires) {
		delete(s.totpSetups, key)
		return "", false
	}
	return setup.Secret, true
}

func (s *Server) clearTOTPSetupForSessionKey(key string) {
	if key == "" {
		return
	}
	s.authMu.Lock()
	delete(s.totpSetups, key)
	s.authMu.Unlock()
}

func (s *Server) clearTOTPSetups() {
	s.authMu.Lock()
	clear(s.totpSetups)
	s.authMu.Unlock()
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.requireCurrentPassword(w, r, in.CurrentPassword) {
		return
	}
	a, err := s.store.GetAdmin()
	if err != nil || a == nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if a.TOTPSecret != "" {
		http.Error(w, "two-step verification is already enabled", http.StatusConflict)
		return
	}
	secret, err := newTOTPSecret()
	if err != nil {
		http.Error(w, "secret generation failed", http.StatusInternalServerError)
		return
	}
	if !s.storeTOTPSetup(r, secret) {
		http.Error(w, "session unavailable", http.StatusUnauthorized)
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
		Code            string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	secret, ok := s.loadTOTPSetup(r)
	if !ok {
		http.Error(w, "TOTP setup expired; start setup again", http.StatusConflict)
		return
	}
	if !s.requireCurrentPassword(w, r, in.CurrentPassword) {
		return
	}
	a, err := s.store.GetAdmin()
	if err != nil || a == nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if a.TOTPSecret != "" {
		http.Error(w, "two-step verification is already enabled", http.StatusConflict)
		return
	}
	_, ok = matchTOTP(secret, in.Code, time.Now())
	if !ok {
		http.Error(w, "invalid verification code", http.StatusBadRequest)
		return
	}
	if err := s.store.EnableAdminTOTPAndRevokeSessions(secret); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	s.clearPreAuth()
	s.clearTOTPSetups()
	s.clearTOTPAuthFailures()
	s.clearAllReauthFailures()
	s.closeAllBrowserTerms()
	s.clearPreAuthCookies(w)
	s.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "reauth": true})
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
	if !s.requireCurrentPassword(w, r, in.CurrentPassword) {
		return
	}
	a, err := s.store.GetAdmin()
	if err != nil || a == nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if a.TOTPSecret == "" {
		http.Error(w, "two-step verification is not enabled", http.StatusConflict)
		return
	}
	_, ok := matchTOTP(a.TOTPSecret, in.Code, time.Now())
	if !ok {
		http.Error(w, "invalid verification code", http.StatusBadRequest)
		return
	}
	disabled, err := s.store.DisableAdminTOTPAndRevokeSessions()
	if err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	if !disabled {
		http.Error(w, "two-step verification is not enabled", http.StatusConflict)
		return
	}
	s.clearPreAuth()
	s.clearTOTPSetups()
	s.clearTOTPAuthFailures()
	s.clearAllReauthFailures()
	s.closeAllBrowserTerms()
	s.clearPreAuthCookies(w)
	s.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "reauth": true})
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
