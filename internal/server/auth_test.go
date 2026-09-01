package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAdmin("admin", "correct horse battery staple"); err != nil {
		store.Close()
		t.Fatal(err)
	}
	cfg := Config{
		CookieSecure:             false,
		SessionTTL:               time.Hour,
		RememberSessionTTL:       30 * 24 * time.Hour,
		PreAuthTTL:               5 * time.Minute,
		PreAuthMaxAttempts:       5,
		PasswordMaxFailures:      10,
		PasswordFailureWindow:    15 * time.Minute,
		PasswordLockoutDuration:  time.Minute,
		PasswordHashConcurrency:  2,
		PasswordHashQueueTimeout: 50 * time.Millisecond,
		TOTPMaxFailures:          10,
		TOTPFailureWindow:        15 * time.Minute,
		TOTPLockoutDuration:      time.Minute,
		TOTPSetupTTL:             10 * time.Minute,
	}
	return New(cfg, store), store
}

func postJSONFromWithCookies(t *testing.T, h http.Handler, path, remoteAddr string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	r.RemoteAddr = remoteAddr
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-HomeCTL-Request", "1")
	for _, cookie := range cookies {
		if cookie != nil {
			r.AddCookie(cookie)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func postJSONFrom(t *testing.T, h http.Handler, path, remoteAddr string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return postJSONFromWithCookies(t, h, path, remoteAddr, body)
}

func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return postJSONFrom(t, h, path, "192.0.2.10:43210", body)
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name && cookie.Value != "" && cookie.MaxAge >= 0 {
			return cookie
		}
	}
	return nil
}

func loginPreAuthCookie(t *testing.T, h http.Handler, remoteAddr string) *http.Cookie {
	t.Helper()
	w := postJSONFrom(t, h, "/api/login", remoteAddr, map[string]any{
		"username": "admin", "password": "correct horse battery staple", "remember": false,
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("password stage status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "preauth_token") {
		t.Fatalf("raw pre-auth token leaked in response body: %s", w.Body.String())
	}
	cookie := cookieNamed(w, "homectl_preauth")
	if cookie == nil || !cookie.HttpOnly || cookie.Path != "/api/login" {
		t.Fatalf("missing protected pre-auth cookie: %#v", w.Result().Cookies())
	}
	return cookie
}

func TestTwoStageTOTPLoginAndReplayProtection(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminTOTP(secret); err != nil {
		t.Fatal(err)
	}
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))

	w := postJSON(t, h, "/api/login", map[string]any{
		"username": "admin", "password": "correct horse battery staple", "remember": true,
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("password stage status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "preauth_token") {
		t.Fatalf("raw pre-auth token leaked in response body: %s", w.Body.String())
	}
	preAuthCookie := cookieNamed(w, "homectl_preauth")
	if preAuthCookie == nil || !preAuthCookie.HttpOnly || preAuthCookie.Path != "/api/login" {
		t.Fatalf("missing protected pre-auth cookie: %#v", w.Result().Cookies())
	}
	if cookieNamed(w, "homectl_session") != nil {
		t.Fatal("full session cookie issued before TOTP")
	}

	code := totpCode(secret, time.Now().Unix()/30)
	w = postJSONFromWithCookies(t, h, "/api/login/totp", "192.0.2.10:43210", map[string]any{"code": code}, preAuthCookie)
	if w.Code != http.StatusOK {
		t.Fatalf("TOTP stage status=%d body=%s", w.Code, w.Body.String())
	}
	sessionCookie := cookieNamed(w, "homectl_session")
	if sessionCookie == nil || sessionCookie.MaxAge <= 0 {
		t.Fatalf("remembered session cookie missing/persistence wrong: %#v", w.Result().Cookies())
	}

	w = postJSONFromWithCookies(t, h, "/api/login/totp", "192.0.2.10:43210", map[string]any{"code": code}, preAuthCookie)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("reused preauth cookie returned %d", w.Code)
	}

	preAuthCookie = loginPreAuthCookie(t, h, "192.0.2.10:43210")
	w = postJSONFromWithCookies(t, h, "/api/login/totp", "192.0.2.10:43210", map[string]any{"code": code}, preAuthCookie)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "invalid_totp") {
		t.Fatalf("TOTP replay not rejected generically: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestNonRememberedLoginUsesSessionCookie(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	w := postJSON(t, h, "/api/login", map[string]any{
		"username": "admin", "password": "correct horse battery staple", "remember": false,
	})
	if w.Code != http.StatusOK {
		body, _ := io.ReadAll(w.Result().Body)
		t.Fatalf("login status=%d body=%s", w.Code, body)
	}
	cookies := w.Result().Cookies()
	sessionCookie := cookieNamed(w, "homectl_session")
	if sessionCookie == nil || sessionCookie.MaxAge != 0 {
		t.Fatalf("expected non-persistent session cookie: %#v", cookies)
	}
}

func TestPasswordFailuresAreScopedByClient(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	s.cfg.PasswordMaxFailures = 3
	s.cfg.PasswordLockoutDuration = time.Minute
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))

	for i := 0; i < 3; i++ {
		w := postJSONFrom(t, h, "/api/login", "192.0.2.10:1000", map[string]any{
			"username": "admin", "password": "wrong password", "remember": false,
		})
		if i < 2 && w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d returned %d: %s", i, w.Code, w.Body.String())
		}
		if i == 2 && w.Code != http.StatusTooManyRequests {
			t.Fatalf("lockout returned %d: %s", w.Code, w.Body.String())
		}
	}

	w := postJSONFrom(t, h, "/api/login", "198.51.100.20:2000", map[string]any{
		"username": "admin", "password": "correct horse battery staple", "remember": false,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("independent client was locked out: %d %s", w.Code, w.Body.String())
	}

	w = postJSONFrom(t, h, "/api/login", "192.0.2.10:1001", map[string]any{
		"username": "admin", "password": "correct horse battery staple", "remember": false,
	})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("locked client bypassed lockout: %d %s", w.Code, w.Body.String())
	}
}

func TestTOTPErrorsLockTOTPStageAcrossPreAuthTokens(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	s.cfg.TOTPMaxFailures = 3
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminTOTP(secret); err != nil {
		t.Fatal(err)
	}
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))

	badCode := "000000"
	if _, ok := matchTOTP(secret, badCode, time.Now()); ok {
		badCode = "999999"
	}
	if _, ok := matchTOTP(secret, badCode, time.Now()); ok {
		t.Fatal("test could not select an invalid TOTP code")
	}

	for i := 0; i < 3; i++ {
		pre := loginPreAuthCookie(t, h, "192.0.2.10:43210")
		w := postJSONFromWithCookies(t, h, "/api/login/totp", "192.0.2.10:43210", map[string]any{"code": badCode}, pre)
		if i < 2 && w.Code != http.StatusUnauthorized {
			t.Fatalf("invalid TOTP attempt %d returned %d: %s", i, w.Code, w.Body.String())
		}
		if i == 2 && w.Code != http.StatusTooManyRequests {
			t.Fatalf("TOTP lockout returned %d: %s", w.Code, w.Body.String())
		}
	}

	w := postJSON(t, h, "/api/login", map[string]any{
		"username": "admin", "password": "correct horse battery staple", "remember": false,
	})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("password stage bypassed active TOTP lockout: %d %s", w.Code, w.Body.String())
	}
}

func TestInvalidPreAuthDoesNotCreateTOTPLockout(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	s.cfg.TOTPMaxFailures = 3
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminTOTP(secret); err != nil {
		t.Fatal(err)
	}
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))

	fake := &http.Cookie{Name: "homectl_preauth", Value: "not-a-real-token", Path: "/api/login"}
	for i := 0; i < 10; i++ {
		w := postJSONFromWithCookies(t, h, "/api/login/totp", "192.0.2.10:43210", map[string]any{"code": "000000"}, fake)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("invalid preauth returned %d: %s", w.Code, w.Body.String())
		}
	}
	if pre := loginPreAuthCookie(t, h, "192.0.2.10:43210"); pre == nil {
		t.Fatal("valid password stage was unexpectedly blocked")
	}
}

func TestPasswordHashConcurrencyReturnsBusy(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))

	for range cap(s.passwordSlots) {
		s.passwordSlots <- struct{}{}
	}
	defer func() {
		for range cap(s.passwordSlots) {
			<-s.passwordSlots
		}
	}()
	w := postJSON(t, h, "/api/login", map[string]any{
		"username": "admin", "password": "correct horse battery staple", "remember": false,
	})
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "auth_busy") {
		t.Fatalf("busy password verifier returned %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordHashAdmissionHasHardCapacity(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	s.cfg.PasswordHashQueueTimeout = time.Second
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	for range cap(s.passwordAdmission) {
		s.passwordAdmission <- struct{}{}
	}
	defer func() {
		for range cap(s.passwordAdmission) {
			<-s.passwordAdmission
		}
	}()
	started := time.Now()
	w := postJSON(t, h, "/api/login", map[string]any{
		"username": "admin", "password": "correct horse battery staple", "remember": false,
	})
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "auth_busy") {
		t.Fatalf("full admission queue returned %d: %s", w.Code, w.Body.String())
	}
	if time.Since(started) > 250*time.Millisecond {
		t.Fatal("request waited despite the bounded admission queue being full")
	}
}

func TestSameOriginRequestRejectsCrossSiteMutation(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "https://panel.example.test/api/logout", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if sameOriginRequest(r) {
		t.Fatal("cross-site state-changing request was accepted")
	}
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if !sameOriginRequest(r) {
		t.Fatal("same-origin state-changing request was rejected")
	}
}

func TestClientIPHeaderRequiresTrustedProxy(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	s.cfg.ClientIPHeader = "CF-Connecting-IP"
	s.cfg.TrustedProxyPrefixes = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

	r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	r.RemoteAddr = "198.51.100.10:1234"
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	if got := s.clientKey(r); got != "198.51.100.10" {
		t.Fatalf("untrusted peer controlled client IP: %q", got)
	}

	r.RemoteAddr = "127.0.0.1:4321"
	if got := s.clientKey(r); got != "203.0.113.9" {
		t.Fatalf("trusted proxy header not used: %q", got)
	}

	r.Header.Set("CF-Connecting-IP", "203.0.113.9, 198.51.100.2")
	if got := s.clientKey(r); got != "127.0.0.1" {
		t.Fatalf("multi-value client header should be rejected: %q", got)
	}
}

func TestBrowserRequestGuardRejectsUnsafeAPIWithoutMarker(t *testing.T) {
	h := browserRequestGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unsafe API request without marker returned %d", w.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{}`))
	r.Header.Set("X-HomeCTL-Request", "1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("marked API request returned %d", w.Code)
	}
}

func loginSessionCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	w := postJSON(t, h, "/api/login", map[string]any{
		"username": "admin", "password": "correct horse battery staple", "remember": false,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	cookie := cookieNamed(w, "homectl_session")
	if cookie == nil {
		t.Fatalf("missing session cookie: %#v", w.Result().Cookies())
	}
	return cookie
}

func TestTOTPEnableDoesNotConsumeFirstLoginCode(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	session := loginSessionCookie(t, h)

	w := postJSONFromWithCookies(t, h, "/api/account/totp/setup", "192.0.2.10:43210", map[string]any{
		"current_password": "correct horse battery staple",
	}, session)
	if w.Code != http.StatusOK {
		t.Fatalf("TOTP setup status=%d body=%s", w.Code, w.Body.String())
	}
	var setup struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil || setup.Secret == "" {
		t.Fatalf("decode TOTP setup: %v body=%s", err, w.Body.String())
	}
	code := totpCode(setup.Secret, time.Now().Unix()/30)
	w = postJSONFromWithCookies(t, h, "/api/account/totp/enable", "192.0.2.10:43210", map[string]any{
		"code": code, "current_password": "correct horse battery staple",
	}, session)
	if w.Code != http.StatusOK {
		t.Fatalf("TOTP enable status=%d body=%s", w.Code, w.Body.String())
	}
	if valid, err := store.SessionValid(session.Value); err != nil || valid {
		t.Fatalf("TOTP enable did not revoke the authorizing session: valid=%v err=%v", valid, err)
	}

	pre := loginPreAuthCookie(t, h, "192.0.2.10:43210")
	w = postJSONFromWithCookies(t, h, "/api/login/totp", "192.0.2.10:43210", map[string]any{"code": code}, pre)
	if w.Code != http.StatusOK {
		t.Fatalf("first login after TOTP enable rejected current setup code: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPreAuthSurvivesTransientSessionCreationFailure(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminTOTP(secret); err != nil {
		t.Fatal(err)
	}
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	pre := loginPreAuthCookie(t, h, "192.0.2.10:43210")
	if _, err := store.db.Exec(`CREATE TRIGGER fail_session_insert BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	code := totpCode(secret, time.Now().Unix()/30)
	w := postJSONFromWithCookies(t, h, "/api/login/totp", "192.0.2.10:43210", map[string]any{"code": code}, pre)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "session_error") {
		t.Fatalf("forced session failure status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_session_insert`); err != nil {
		t.Fatal(err)
	}
	w = postJSONFromWithCookies(t, h, "/api/login/totp", "192.0.2.10:43210", map[string]any{"code": code}, pre)
	if w.Code != http.StatusOK {
		t.Fatalf("pre-auth was not retryable after transient DB error: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCurrentPasswordFailuresAreScopedToSession(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	s.cfg.PasswordMaxFailures = 3
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	badSession := loginSessionCookie(t, h)

	for i := 0; i < 3; i++ {
		w := postJSONFromWithCookies(t, h, "/api/account/totp/setup", "192.0.2.10:43210", map[string]any{
			"current_password": "wrong password",
		}, badSession)
		if i < 2 && w.Code != http.StatusForbidden {
			t.Fatalf("reauth failure %d returned %d: %s", i, w.Code, w.Body.String())
		}
		if i == 2 && w.Code != http.StatusTooManyRequests {
			t.Fatalf("reauth lockout returned %d: %s", w.Code, w.Body.String())
		}
	}

	goodSession := loginSessionCookie(t, h)
	w := postJSONFromWithCookies(t, h, "/api/account/totp/setup", "192.0.2.10:43210", map[string]any{
		"current_password": "correct horse battery staple",
	}, goodSession)
	if w.Code != http.StatusOK {
		t.Fatalf("different authenticated session inherited reauth lockout: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestTOTPSetupStateIsBoundToSession(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	ownerSession := loginSessionCookie(t, h)

	w := postJSONFromWithCookies(t, h, "/api/account/totp/setup", "192.0.2.10:43210", map[string]any{
		"current_password": "correct horse battery staple",
	}, ownerSession)
	if w.Code != http.StatusOK {
		t.Fatalf("TOTP setup status=%d body=%s", w.Code, w.Body.String())
	}
	var setup struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil || setup.Secret == "" {
		t.Fatalf("decode TOTP setup: %v body=%s", err, w.Body.String())
	}

	otherSession := loginSessionCookie(t, h)
	code := totpCode(setup.Secret, time.Now().Unix()/30)
	w = postJSONFromWithCookies(t, h, "/api/account/totp/enable", "192.0.2.10:43210", map[string]any{"code": code}, otherSession)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "setup expired") {
		t.Fatalf("different session reused TOTP setup state: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUsernameChangeRevokesSessionsAndClosesTerminals(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	session := loginSessionCookie(t, h)
	term := newBrowserTerm(nil, string(hashToken(session.Value)), "device")
	s.mu.Lock()
	s.terms["terminal"] = term
	s.mu.Unlock()

	w := postJSONFromWithCookies(t, h, "/api/account/username", "192.0.2.10:43210", map[string]any{
		"username": "new-owner", "current_password": "correct horse battery staple",
	}, session)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "reauth") {
		t.Fatalf("username change status=%d body=%s", w.Code, w.Body.String())
	}
	admin, err := store.GetAdmin()
	if err != nil || admin == nil || admin.Username != "new-owner" {
		t.Fatalf("administrator=%#v err=%v", admin, err)
	}
	if valid, err := store.SessionValid(session.Value); err != nil || valid {
		t.Fatalf("username change left session valid=%v err=%v", valid, err)
	}
	select {
	case <-term.done:
	default:
		t.Fatal("username change left terminal open")
	}
}

func TestPasswordChangeRevokesSessionsAndClosesTerminals(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	session := loginSessionCookie(t, h)
	term := newBrowserTerm(nil, string(hashToken(session.Value)), "device")
	s.mu.Lock()
	s.terms["terminal"] = term
	s.mu.Unlock()

	w := postJSONFromWithCookies(t, h, "/api/account/password", "192.0.2.10:43210", map[string]any{
		"current_password": "correct horse battery staple", "new_password": "a new correct horse battery staple",
	}, session)
	if w.Code != http.StatusOK {
		t.Fatalf("password change status=%d body=%s", w.Code, w.Body.String())
	}
	if valid, err := store.SessionValid(session.Value); err != nil || valid {
		t.Fatalf("password change left session valid=%v err=%v", valid, err)
	}
	if _, ok, err := store.VerifyAdminPassword("a new correct horse battery staple"); err != nil || !ok {
		t.Fatalf("new password verification ok=%v err=%v", ok, err)
	}
	select {
	case <-term.done:
	default:
		t.Fatal("password change left terminal open")
	}
}

func TestTOTPDisableUsesIndependentVerificationStep(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminTOTP(secret); err != nil {
		t.Fatal(err)
	}
	h := s.Handler(http.FS(os.DirFS(t.TempDir())))
	pre := loginPreAuthCookie(t, h, "192.0.2.10:43210")
	code := totpCode(secret, time.Now().Unix()/30)
	w := postJSONFromWithCookies(t, h, "/api/login/totp", "192.0.2.10:43210", map[string]any{"code": code}, pre)
	if w.Code != http.StatusOK {
		t.Fatalf("TOTP login status=%d body=%s", w.Code, w.Body.String())
	}
	session := cookieNamed(w, "homectl_session")
	if session == nil {
		t.Fatalf("missing session cookie: %#v", w.Result().Cookies())
	}
	w = postJSONFromWithCookies(t, h, "/api/account/totp/disable", "192.0.2.10:43210", map[string]any{
		"current_password": "correct horse battery staple", "code": code,
	}, session)
	if w.Code != http.StatusOK {
		t.Fatalf("TOTP disable rejected an independently valid code: status=%d body=%s", w.Code, w.Body.String())
	}
	admin, err := store.GetAdmin()
	if err != nil || admin == nil || admin.TOTPSecret != "" || admin.LastTOTPLoginStep != -1 {
		t.Fatalf("administrator after disable=%#v err=%v", admin, err)
	}
	if valid, err := store.SessionValid(session.Value); err != nil || valid {
		t.Fatalf("TOTP disable left session valid=%v err=%v", valid, err)
	}
}
