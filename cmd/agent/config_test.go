package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAgentConfigRelativeState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{
  "server": "wss://panel.example.com/agent/ws",
  "enroll_token": "0123456789abcdef0123456789abcdef",
  "state_file": "state.json",
  "heartbeat_interval": "20s",
  "reconnect_min": "2s",
  "reconnect_max": "10s",
  "dial_timeout": "5s",
  "command_timeout": "45s",
  "max_command_output_bytes": 65536,
  "shell": "/bin/bash",
  "exec_enabled": false,
  "terminal_enabled": true
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
	if cfg.heartbeatDuration != 20*time.Second {
		t.Fatalf("unexpected heartbeat: %s", cfg.heartbeatDuration)
	}
	if cfg.ExecEnabled {
		t.Fatal("exec_enabled false was not preserved")
	}
}
