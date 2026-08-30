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
	ListenAddr    string `json:"listen_addr"`
	DatabasePath  string `json:"database_path"`
	AdminPassword string `json:"admin_password"`
	EnrollToken   string `json:"enroll_token"`
	CookieSecure  bool   `json:"cookie_secure"`
	SessionTTL    string `json:"session_ttl"`
	AllowExec     bool   `json:"allow_exec"`
	AllowTerminal bool   `json:"allow_terminal"`
}

// LoadConfig reads the server JSON configuration file.
// Unknown fields are rejected so configuration typos fail fast.
func LoadConfig(path string) (Config, error) {
	raw := fileConfig{
		ListenAddr:    ":8080",
		DatabasePath:  "/data/homectl.db",
		CookieSecure:  true,
		SessionTTL:    "24h",
		AllowExec:     true,
		AllowTerminal: true,
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
	raw.AdminPassword = strings.TrimSpace(raw.AdminPassword)
	raw.EnrollToken = strings.TrimSpace(raw.EnrollToken)
	if raw.ListenAddr == "" {
		return Config{}, fmt.Errorf("listen_addr is required")
	}
	if raw.DatabasePath == "" {
		return Config{}, fmt.Errorf("database_path is required")
	}
	if !filepath.IsAbs(raw.DatabasePath) {
		raw.DatabasePath = filepath.Join(filepath.Dir(path), raw.DatabasePath)
	}
	if len(raw.AdminPassword) < 12 {
		return Config{}, fmt.Errorf("admin_password must be at least 12 characters")
	}
	if len(raw.EnrollToken) < 20 {
		return Config{}, fmt.Errorf("enroll_token must be at least 20 characters")
	}

	ttl, err := time.ParseDuration(raw.SessionTTL)
	if err != nil || ttl <= 0 {
		return Config{}, fmt.Errorf("invalid session_ttl %q", raw.SessionTTL)
	}

	return Config{
		Addr:          raw.ListenAddr,
		DBPath:        raw.DatabasePath,
		AdminPassword: raw.AdminPassword,
		EnrollToken:   raw.EnrollToken,
		CookieSecure:  raw.CookieSecure,
		SessionTTL:    ttl,
		AllowExec:     raw.AllowExec,
		AllowTerminal: raw.AllowTerminal,
	}, nil
}
