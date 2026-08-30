package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type fileConfig struct {
	ListenAddr             string `json:"listen_addr"`
	DatabasePath           string `json:"database_path"`
	LegacyDeviceStore      string `json:"legacy_device_store"`
	AdminUsername          string `json:"admin_username"`
	AdminPassword          string `json:"admin_password"`
	CookieSecure           bool   `json:"cookie_secure"`
	SessionTTL             string `json:"session_ttl"`
	AllowExec              bool   `json:"allow_exec"`
	AllowTerminal          bool   `json:"allow_terminal"`
	FileBrowserEnabled     bool   `json:"file_browser_enabled"`
	AgentOfflineTimeout    string `json:"agent_offline_timeout"`
	AgentHandshakeTimeout  string `json:"agent_handshake_timeout"`
	AgentWriteTimeout      string `json:"agent_write_timeout"`
	ActionTimeout          string `json:"action_timeout"`
	ExecResponseTimeout    string `json:"exec_response_timeout"`
	FileTransferTimeout    string `json:"file_transfer_timeout"`
	EnrollmentTokenTTL     string `json:"enrollment_token_ttl"`
	WebRefreshInterval     string `json:"web_refresh_interval"`
	UIResultTTL            string `json:"ui_result_ttl"`
	HTTPReadHeaderTimeout  string `json:"http_read_header_timeout"`
	ShutdownTimeout        string `json:"shutdown_timeout"`
	FileTransferChunkBytes int    `json:"file_transfer_chunk_bytes"`
	MaxFileTransferBytes   int64  `json:"max_file_transfer_bytes"`
	MaxCommandLength       int    `json:"max_command_length"`
}

// LoadConfig reads the server JSON configuration file.
// Unknown fields are rejected so configuration typos fail fast.
func LoadConfig(path string) (Config, error) {
	raw := fileConfig{
		ListenAddr:             ":8080",
		DatabasePath:           "/data/homectl.db",
		AdminUsername:          "admin",
		CookieSecure:           true,
		SessionTTL:             "24h",
		AllowExec:              true,
		AllowTerminal:          true,
		FileBrowserEnabled:     false,
		AgentOfflineTimeout:    "25s",
		AgentHandshakeTimeout:  "15s",
		AgentWriteTimeout:      "10s",
		ActionTimeout:          "8s",
		ExecResponseTimeout:    "40s",
		FileTransferTimeout:    "2m",
		EnrollmentTokenTTL:     "30m",
		WebRefreshInterval:     "5s",
		UIResultTTL:            "20s",
		HTTPReadHeaderTimeout:  "10s",
		ShutdownTimeout:        "10s",
		FileTransferChunkBytes: 64 << 10,
		MaxFileTransferBytes:   1 << 30,
		MaxCommandLength:       4096,
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

	raw.ListenAddr = strings.TrimSpace(raw.ListenAddr)
	raw.DatabasePath = strings.TrimSpace(raw.DatabasePath)
	raw.LegacyDeviceStore = strings.TrimSpace(raw.LegacyDeviceStore)
	raw.AdminUsername = strings.TrimSpace(raw.AdminUsername)
	raw.AdminPassword = strings.TrimSpace(raw.AdminPassword)
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
	if raw.AdminUsername == "" || len(raw.AdminUsername) > 64 {
		return Config{}, fmt.Errorf("admin_username must be 1-64 characters")
	}
	if raw.AdminPassword != "" && len(raw.AdminPassword) < 12 {
		return Config{}, fmt.Errorf("admin_password must be empty or at least 12 characters")
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
		Addr:                   raw.ListenAddr,
		DBPath:                 raw.DatabasePath,
		LegacyDeviceStore:      raw.LegacyDeviceStore,
		AdminUsername:          raw.AdminUsername,
		AdminPassword:          raw.AdminPassword,
		CookieSecure:           raw.CookieSecure,
		SessionTTL:             sessionTTL,
		AllowExec:              raw.AllowExec,
		AllowTerminal:          raw.AllowTerminal,
		FileBrowserEnabled:     raw.FileBrowserEnabled,
		AgentOfflineTimeout:    offlineTimeout,
		AgentHandshakeTimeout:  handshakeTimeout,
		AgentWriteTimeout:      writeTimeout,
		ActionTimeout:          actionTimeout,
		ExecResponseTimeout:    execTimeout,
		FileTransferTimeout:    fileTimeout,
		EnrollmentTokenTTL:     enrollTTL,
		WebRefreshInterval:     webRefresh,
		UIResultTTL:            uiTTL,
		HTTPReadHeaderTimeout:  readHeaderTimeout,
		ShutdownTimeout:        shutdownTimeout,
		FileTransferChunkBytes: raw.FileTransferChunkBytes,
		MaxFileTransferBytes:   raw.MaxFileTransferBytes,
		MaxCommandLength:       raw.MaxCommandLength,
	}, nil
}
