package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type fileConfig struct {
	ListenAddr               string   `json:"listen_addr"`
	DatabasePath             string   `json:"database_path"`
	LegacyDeviceStore        string   `json:"legacy_device_store"`
	AdminUsername            string   `json:"admin_username"`
	AdminPassword            string   `json:"admin_password"`
	CookieSecure             bool     `json:"cookie_secure"`
	SessionTTL               string   `json:"session_ttl"`
	RememberSessionTTL       string   `json:"remember_session_ttl"`
	PreAuthTTL               string   `json:"preauth_ttl"`
	PreAuthMaxAttempts       int      `json:"preauth_max_attempts"`
	PasswordMaxFailures      int      `json:"password_max_failures"`
	PasswordFailureWindow    string   `json:"password_failure_window"`
	PasswordLockoutDuration  string   `json:"password_lockout_duration"`
	PasswordHashConcurrency  int      `json:"password_hash_concurrency"`
	PasswordHashQueueTimeout string   `json:"password_hash_queue_timeout"`
	TOTPMaxFailures          int      `json:"totp_max_failures"`
	TOTPFailureWindow        string   `json:"totp_failure_window"`
	TOTPLockoutDuration      string   `json:"totp_lockout_duration"`
	TOTPSetupTTL             string   `json:"totp_setup_ttl"`
	ClientIPHeader           string   `json:"client_ip_header"`
	TrustedProxyCIDRs        []string `json:"trusted_proxy_cidrs"`
	AllowExec                bool     `json:"allow_exec"`
	AllowTerminal            bool     `json:"allow_terminal"`
	FileBrowserEnabled       bool     `json:"file_browser_enabled"`
	AgentOfflineTimeout      string   `json:"agent_offline_timeout"`
	AgentHandshakeTimeout    string   `json:"agent_handshake_timeout"`
	AgentWriteTimeout        string   `json:"agent_write_timeout"`
	ActionTimeout            string   `json:"action_timeout"`
	ExecResponseTimeout      string   `json:"exec_response_timeout"`
	FileTransferTimeout      string   `json:"file_transfer_timeout"`
	EnrollmentTokenTTL       string   `json:"enrollment_token_ttl"`
	WebRefreshInterval       string   `json:"web_refresh_interval"`
	UIResultTTL              string   `json:"ui_result_ttl"`
	HTTPReadHeaderTimeout    string   `json:"http_read_header_timeout"`
	ShutdownTimeout          string   `json:"shutdown_timeout"`
	FileTransferChunkBytes   int      `json:"file_transfer_chunk_bytes"`
	MaxFileTransferBytes     int64    `json:"max_file_transfer_bytes"`
	MaxCommandLength         int      `json:"max_command_length"`
}

// LoadConfig reads a JSON configuration file and rejects unknown fields.
func LoadConfig(path string) (Config, error) {
	raw := fileConfig{
		ListenAddr:               ":8080",
		DatabasePath:             "/data/homectl.db",
		AdminUsername:            "admin",
		CookieSecure:             true,
		SessionTTL:               "24h",
		RememberSessionTTL:       "168h",
		PreAuthTTL:               "5m",
		PreAuthMaxAttempts:       5,
		PasswordMaxFailures:      10,
		PasswordFailureWindow:    "15m",
		PasswordLockoutDuration:  "1m",
		PasswordHashConcurrency:  1,
		PasswordHashQueueTimeout: "5s",
		TOTPMaxFailures:          10,
		TOTPFailureWindow:        "15m",
		TOTPLockoutDuration:      "1m",
		TOTPSetupTTL:             "10m",
		TrustedProxyCIDRs:        []string{"127.0.0.1/32", "::1/128"},
		AllowExec:                true,
		AllowTerminal:            true,
		FileBrowserEnabled:       false,
		AgentOfflineTimeout:      "25s",
		AgentHandshakeTimeout:    "15s",
		AgentWriteTimeout:        "10s",
		ActionTimeout:            "8s",
		ExecResponseTimeout:      "40s",
		FileTransferTimeout:      "2m",
		EnrollmentTokenTTL:       "30m",
		WebRefreshInterval:       "5s",
		UIResultTTL:              "20s",
		HTTPReadHeaderTimeout:    "10s",
		ShutdownTimeout:          "10s",
		FileTransferChunkBytes:   64 << 10,
		MaxFileTransferBytes:     1 << 30,
		MaxCommandLength:         4096,
	}

	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return Config{}, fmt.Errorf("decode config %s: trailing content: %w", path, err)
	}

	raw.ListenAddr = strings.TrimSpace(raw.ListenAddr)
	raw.DatabasePath = strings.TrimSpace(raw.DatabasePath)
	raw.LegacyDeviceStore = strings.TrimSpace(raw.LegacyDeviceStore)
	raw.AdminUsername = strings.TrimSpace(raw.AdminUsername)
	raw.ClientIPHeader = strings.TrimSpace(raw.ClientIPHeader)
	if raw.ListenAddr == "" {
		return Config{}, fmt.Errorf("listen_addr is required")
	}
	if raw.DatabasePath == "" {
		return Config{}, fmt.Errorf("database_path is required")
	}
	if !filepath.IsAbs(raw.DatabasePath) {
		raw.DatabasePath = filepath.Join(filepath.Dir(path), raw.DatabasePath)
	}
	if raw.LegacyDeviceStore != "" && !filepath.IsAbs(raw.LegacyDeviceStore) {
		raw.LegacyDeviceStore = filepath.Join(filepath.Dir(path), raw.LegacyDeviceStore)
	}

	parsePositive := func(name, value string) (time.Duration, error) {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("invalid %s %q", name, value)
		}
		return d, nil
	}
	parseNonNegative := func(name, value string) (time.Duration, error) {
		d, err := time.ParseDuration(value)
		if err != nil || d < 0 {
			return 0, fmt.Errorf("invalid %s %q", name, value)
		}
		return d, nil
	}

	sessionTTL, err := parsePositive("session_ttl", raw.SessionTTL)
	if err != nil {
		return Config{}, err
	}
	rememberSessionTTL, err := parsePositive("remember_session_ttl", raw.RememberSessionTTL)
	if err != nil {
		return Config{}, err
	}
	if rememberSessionTTL < sessionTTL {
		return Config{}, fmt.Errorf("remember_session_ttl must be >= session_ttl")
	}
	preAuthTTL, err := parsePositive("preauth_ttl", raw.PreAuthTTL)
	if err != nil {
		return Config{}, err
	}
	if preAuthTTL < 30*time.Second || preAuthTTL > 15*time.Minute {
		return Config{}, fmt.Errorf("preauth_ttl must be between 30s and 15m")
	}
	passwordFailureWindow, err := parsePositive("password_failure_window", raw.PasswordFailureWindow)
	if err != nil {
		return Config{}, err
	}
	passwordLockout, err := parsePositive("password_lockout_duration", raw.PasswordLockoutDuration)
	if err != nil {
		return Config{}, err
	}
	passwordQueueTimeout, err := parsePositive("password_hash_queue_timeout", raw.PasswordHashQueueTimeout)
	if err != nil {
		return Config{}, err
	}
	totpFailureWindow, err := parsePositive("totp_failure_window", raw.TOTPFailureWindow)
	if err != nil {
		return Config{}, err
	}
	totpLockout, err := parsePositive("totp_lockout_duration", raw.TOTPLockoutDuration)
	if err != nil {
		return Config{}, err
	}
	totpSetupTTL, err := parsePositive("totp_setup_ttl", raw.TOTPSetupTTL)
	if err != nil {
		return Config{}, err
	}
	if totpSetupTTL < time.Minute || totpSetupTTL > 30*time.Minute {
		return Config{}, fmt.Errorf("totp_setup_ttl must be between 1m and 30m")
	}
	if raw.PreAuthMaxAttempts < 3 || raw.PreAuthMaxAttempts > 20 {
		return Config{}, fmt.Errorf("preauth_max_attempts must be between 3 and 20")
	}
	if raw.PasswordMaxFailures < 3 || raw.PasswordMaxFailures > 100 {
		return Config{}, fmt.Errorf("password_max_failures must be between 3 and 100")
	}
	if raw.PasswordHashConcurrency < 1 || raw.PasswordHashConcurrency > 16 {
		return Config{}, fmt.Errorf("password_hash_concurrency must be between 1 and 16")
	}
	if raw.TOTPMaxFailures < 3 || raw.TOTPMaxFailures > 100 {
		return Config{}, fmt.Errorf("totp_max_failures must be between 3 and 100")
	}
	if raw.ClientIPHeader != "" {
		for _, ch := range raw.ClientIPHeader {
			if !(ch == '-' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z') {
				return Config{}, fmt.Errorf("client_ip_header contains invalid characters")
			}
		}
	}
	trustedProxyPrefixes := make([]netip.Prefix, 0, len(raw.TrustedProxyCIDRs))
	for _, value := range raw.TrustedProxyCIDRs {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid trusted_proxy_cidrs entry %q", value)
		}
		trustedProxyPrefixes = append(trustedProxyPrefixes, prefix.Masked())
	}

	offlineTimeout, err := parsePositive("agent_offline_timeout", raw.AgentOfflineTimeout)
	if err != nil {
		return Config{}, err
	}
	handshakeTimeout, err := parsePositive("agent_handshake_timeout", raw.AgentHandshakeTimeout)
	if err != nil {
		return Config{}, err
	}
	writeTimeout, err := parsePositive("agent_write_timeout", raw.AgentWriteTimeout)
	if err != nil {
		return Config{}, err
	}
	actionTimeout, err := parsePositive("action_timeout", raw.ActionTimeout)
	if err != nil {
		return Config{}, err
	}
	execTimeout, err := parsePositive("exec_response_timeout", raw.ExecResponseTimeout)
	if err != nil {
		return Config{}, err
	}
	fileTimeout, err := parsePositive("file_transfer_timeout", raw.FileTransferTimeout)
	if err != nil {
		return Config{}, err
	}
	enrollTTL, err := parsePositive("enrollment_token_ttl", raw.EnrollmentTokenTTL)
	if err != nil {
		return Config{}, err
	}
	webRefresh, err := parsePositive("web_refresh_interval", raw.WebRefreshInterval)
	if err != nil {
		return Config{}, err
	}
	uiTTL, err := parseNonNegative("ui_result_ttl", raw.UIResultTTL)
	if err != nil {
		return Config{}, err
	}
	readHeaderTimeout, err := parsePositive("http_read_header_timeout", raw.HTTPReadHeaderTimeout)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := parsePositive("shutdown_timeout", raw.ShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	if raw.FileTransferChunkBytes < 4096 || raw.FileTransferChunkBytes > 512<<10 {
		return Config{}, fmt.Errorf("file_transfer_chunk_bytes must be between 4096 and 524288")
	}
	if raw.MaxFileTransferBytes < 0 {
		return Config{}, fmt.Errorf("max_file_transfer_bytes must be >= 0; use 0 for unlimited")
	}
	if raw.MaxCommandLength < 256 || raw.MaxCommandLength > 1<<20 {
		return Config{}, fmt.Errorf("max_command_length must be between 256 and 1048576")
	}

	return Config{
		Addr:                     raw.ListenAddr,
		DBPath:                   raw.DatabasePath,
		LegacyDeviceStore:        raw.LegacyDeviceStore,
		AdminUsername:            raw.AdminUsername,
		AdminPassword:            raw.AdminPassword,
		CookieSecure:             raw.CookieSecure,
		SessionTTL:               sessionTTL,
		RememberSessionTTL:       rememberSessionTTL,
		PreAuthTTL:               preAuthTTL,
		PreAuthMaxAttempts:       raw.PreAuthMaxAttempts,
		PasswordMaxFailures:      raw.PasswordMaxFailures,
		PasswordFailureWindow:    passwordFailureWindow,
		PasswordLockoutDuration:  passwordLockout,
		PasswordHashConcurrency:  raw.PasswordHashConcurrency,
		PasswordHashQueueTimeout: passwordQueueTimeout,
		TOTPMaxFailures:          raw.TOTPMaxFailures,
		TOTPFailureWindow:        totpFailureWindow,
		TOTPLockoutDuration:      totpLockout,
		TOTPSetupTTL:             totpSetupTTL,
		ClientIPHeader:           raw.ClientIPHeader,
		TrustedProxyPrefixes:     trustedProxyPrefixes,
		AllowExec:                raw.AllowExec,
		AllowTerminal:            raw.AllowTerminal,
		FileBrowserEnabled:       raw.FileBrowserEnabled,
		AgentOfflineTimeout:      offlineTimeout,
		AgentHandshakeTimeout:    handshakeTimeout,
		AgentWriteTimeout:        writeTimeout,
		ActionTimeout:            actionTimeout,
		ExecResponseTimeout:      execTimeout,
		FileTransferTimeout:      fileTimeout,
		EnrollmentTokenTTL:       enrollTTL,
		WebRefreshInterval:       webRefresh,
		UIResultTTL:              uiTTL,
		HTTPReadHeaderTimeout:    readHeaderTimeout,
		ShutdownTimeout:          shutdownTimeout,
		FileTransferChunkBytes:   raw.FileTransferChunkBytes,
		MaxFileTransferBytes:     raw.MaxFileTransferBytes,
		MaxCommandLength:         raw.MaxCommandLength,
	}, nil
}
