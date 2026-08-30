package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{
  "listen_addr": "127.0.0.1:9090",
  "database_path": "data/homectl.db",
  "admin_username": "owner",
  "admin_password": "0123456789abcdef",
  "cookie_secure": false,
  "session_ttl": "2h",
  "allow_exec": false,
  "allow_terminal": true,
  "file_browser_enabled": true,
  "agent_offline_timeout": "30s",
  "agent_handshake_timeout": "12s",
  "agent_write_timeout": "7s",
  "action_timeout": "9s",
  "exec_response_timeout": "45s",
  "file_transfer_timeout": "3m",
  "enrollment_token_ttl": "20m",
  "web_refresh_interval": "4s",
  "ui_result_ttl": "0s",
  "http_read_header_timeout": "11s",
  "shutdown_timeout": "13s",
  "file_transfer_chunk_bytes": 32768,
  "max_file_transfer_bytes": 10485760,
  "max_command_length": 8192
}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9090" {
		t.Fatalf("unexpected addr: %s", cfg.Addr)
	}
	if cfg.DBPath != filepath.Join(dir, "data/homectl.db") {
		t.Fatalf("unexpected db path: %s", cfg.DBPath)
	}
	if cfg.AdminUsername != "owner" {
		t.Fatalf("unexpected username: %s", cfg.AdminUsername)
	}
	if cfg.CookieSecure {
		t.Fatal("cookie_secure false was not preserved")
	}
	if cfg.AllowExec {
		t.Fatal("allow_exec false was not preserved")
	}
	if !cfg.FileBrowserEnabled {
		t.Fatal("file_browser_enabled true was not preserved")
	}
	if cfg.SessionTTL != 2*time.Hour || cfg.AgentOfflineTimeout != 30*time.Second || cfg.WebRefreshInterval != 4*time.Second {
		t.Fatal("duration settings not loaded")
	}
	if cfg.UIResultTTL != 0 {
		t.Fatalf("unexpected ui ttl: %s", cfg.UIResultTTL)
	}
	if cfg.FileTransferChunkBytes != 32768 || cfg.MaxFileTransferBytes != 10485760 || cfg.MaxCommandLength != 8192 {
		t.Fatal("file transfer settings not loaded")
	}
}
