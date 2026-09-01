package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
  "remember_session_ttl": "48h",
  "preauth_ttl": "4m",
  "preauth_max_attempts": 4,
  "password_max_failures": 8,
  "password_failure_window": "12m",
  "password_lockout_duration": "45s",
  "password_hash_concurrency": 3,
  "password_hash_queue_timeout": "6s",
  "totp_max_failures": 7,
  "totp_failure_window": "9m",
  "totp_lockout_duration": "35s",
  "client_ip_header": "CF-Connecting-IP",
  "trusted_proxy_cidrs": ["127.0.0.1/32", "::1/128"],
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
	if cfg.AdminUsername != "owner" || cfg.AdminPassword != "0123456789abcdef" {
		t.Fatal("bootstrap credentials not loaded")
	}
	if cfg.CookieSecure || cfg.AllowExec || !cfg.FileBrowserEnabled {
		t.Fatal("boolean settings not loaded")
	}
	if cfg.SessionTTL != 2*time.Hour || cfg.RememberSessionTTL != 48*time.Hour || cfg.PreAuthTTL != 4*time.Minute {
		t.Fatal("session/pre-auth durations not loaded")
	}
	if cfg.PasswordMaxFailures != 8 || cfg.PasswordFailureWindow != 12*time.Minute || cfg.PasswordLockoutDuration != 45*time.Second || cfg.PasswordHashConcurrency != 3 || cfg.PasswordHashQueueTimeout != 6*time.Second {
		t.Fatal("password authentication limits not loaded")
	}
	if cfg.TOTPMaxFailures != 7 || cfg.TOTPFailureWindow != 9*time.Minute || cfg.TOTPLockoutDuration != 35*time.Second {
		t.Fatal("TOTP authentication limits not loaded")
	}
	if cfg.ClientIPHeader != "CF-Connecting-IP" || len(cfg.TrustedProxyPrefixes) != 2 {
		t.Fatal("trusted proxy settings not loaded")
	}
	if cfg.AgentOfflineTimeout != 30*time.Second || cfg.WebRefreshInterval != 4*time.Second || cfg.UIResultTTL != 0 {
		t.Fatal("runtime durations not loaded")
	}
	if cfg.FileTransferChunkBytes != 32768 || cfg.MaxFileTransferBytes != 10485760 || cfg.MaxCommandLength != 8192 {
		t.Fatal("file/command settings not loaded")
	}
}

func TestLoadConfigAllowsEmptyBootstrapCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
  "listen_addr":"127.0.0.1:8080",
  "database_path":"homectl.db",
  "admin_username":"",
  "admin_password":""
}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("bootstrap credentials should be validated only when an administrator must be created: %v", err)
	}
	if cfg.AdminUsername != "" || cfg.AdminPassword != "" {
		t.Fatal("empty bootstrap credentials were not preserved")
	}
}

func TestLoadConfigRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen_addr":":8080","database_path":"homectl.db","typo_option":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field was not rejected: %v", err)
	}
}

func TestLoadConfigRejectsTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"listen_addr":":8080","database_path":"homectl.db"} {"listen_addr":"127.0.0.1:1"}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestDeploymentConfigExamplesLoad(t *testing.T) {
	for _, name := range []string{"config.example.json", "config.binary.example.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "deploy", "server", name)
			if _, err := LoadConfig(path); err != nil {
				t.Fatalf("load %s: %v", path, err)
			}
			requireCompleteJSONConfig(t, path, reflect.TypeOf(fileConfig{}))
		})
	}
}

func requireCompleteJSONConfig(t *testing.T, path string, configType reflect.Type) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(b, &values); err != nil {
		t.Fatal(err)
	}
	requireJSONFields(t, path, values, configType)
}

func requireJSONFields(t *testing.T, path string, values map[string]json.RawMessage, configType reflect.Type) {
	t.Helper()
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		raw, ok := values[name]
		if !ok {
			t.Errorf("%s is missing configurable field %q", path, name)
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(raw, &nested); err != nil {
				t.Errorf("%s field %q is not an object: %v", path, name, err)
				continue
			}
			requireJSONFields(t, path+"."+name, nested, field.Type)
		}
	}
}
