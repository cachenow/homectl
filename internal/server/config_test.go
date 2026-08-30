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
  "database_path": "data/store.json",
  "admin_password": "0123456789abcdef",
  "enroll_token": "0123456789abcdef0123456789abcdef",
  "cookie_secure": false,
  "session_ttl": "2h",
  "allow_exec": false,
  "allow_terminal": true
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
	if cfg.DBPath != filepath.Join(dir, "data/store.json") {
		t.Fatalf("unexpected db path: %s", cfg.DBPath)
	}
	if cfg.CookieSecure {
		t.Fatal("cookie_secure false was not preserved")
	}
	if cfg.AllowExec {
		t.Fatal("allow_exec false was not preserved")
	}
	if cfg.SessionTTL != 2*time.Hour {
		t.Fatalf("unexpected session ttl: %s", cfg.SessionTTL)
	}
}
