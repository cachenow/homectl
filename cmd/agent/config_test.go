package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadAgentConfigRelativeState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{
  "server": "wss://panel.example.com/agent/ws",
  "enroll_token": "one-time-token",
  "state_file": "state.json",
  "heartbeat_interval": "20s",
  "reconnect_min": "2s",
  "reconnect_max": "10s",
  "dial_timeout": "5s",
  "handshake_timeout": "6s",
  "write_timeout": "7s",
  "command_timeout": "45s",
  "max_command_output_bytes": 65536,
  "shell": "/bin/bash",
  "exec_enabled": false,
  "terminal_enabled": true,
  "file_browser_enabled": true,
  "file_browser_root": "/srv",
  "file_transfer_chunk_bytes": 32768,
  "max_file_transfer_bytes": 10485760
}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateFile != filepath.Join(dir, "state.json") {
		t.Fatalf("unexpected state path: %s", cfg.StateFile)
	}
	if cfg.heartbeatDuration != 20*time.Second || cfg.handshakeTimeoutDur != 6*time.Second || cfg.writeTimeoutDur != 7*time.Second {
		t.Fatal("timeout settings not loaded")
	}
	if cfg.ExecEnabled {
		t.Fatal("exec_enabled false was not preserved")
	}
	if !cfg.FileBrowserEnabled || cfg.FileBrowserRoot != "/srv" {
		t.Fatal("file browser settings not loaded")
	}
}

func TestValidateAgentName(t *testing.T) {
	if err := validateAgentName("客厅-KVM-01"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	if err := validateAgentName("bad\nname"); err == nil {
		t.Fatal("control character in agent name was accepted")
	}
	tooLong := make([]rune, 129)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	if err := validateAgentName(string(tooLong)); err == nil {
		t.Fatal("overlong agent name was accepted")
	}
}

func TestLoadAgentConfigRejectsTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"server":"wss://panel.example.com/agent/ws","shell":"/bin/bash"} {"server":"wss://attacker.invalid/agent/ws"}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAgentConfig(path); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestDeploymentConfigExampleLoads(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", "agent", "config.example.json")
	if _, err := loadAgentConfig(path); err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	requireCompleteAgentJSONConfig(t, path, reflect.TypeOf(agentConfig{}))
}

func requireCompleteAgentJSONConfig(t *testing.T, path string, configType reflect.Type) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(b, &values); err != nil {
		t.Fatal(err)
	}
	requireAgentJSONFields(t, path, values, configType)
}

func requireAgentJSONFields(t *testing.T, path string, values map[string]json.RawMessage, configType reflect.Type) {
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
			requireAgentJSONFields(t, path+"."+name, nested, field.Type)
		}
	}
}
