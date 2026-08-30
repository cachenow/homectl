package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type agentConfig struct {
	Server                    string                 `json:"server"`
	Name                      string                 `json:"name"`
	EnrollToken               string                 `json:"enroll_token"`
	StateFile                 string                 `json:"state_file"`
	HeartbeatInterval         string                 `json:"heartbeat_interval"`
	ReconnectMin              string                 `json:"reconnect_min"`
	ReconnectMax              string                 `json:"reconnect_max"`
	DialTimeout               string                 `json:"dial_timeout"`
	HandshakeTimeout          string                 `json:"handshake_timeout"`
	WriteTimeout              string                 `json:"write_timeout"`
	CommandTimeout            string                 `json:"command_timeout"`
	MaxCommandOutputBytes     int                    `json:"max_command_output_bytes"`
	Shell                     string                 `json:"shell"`
	ExecEnabled               bool                   `json:"exec_enabled"`
	TerminalEnabled           bool                   `json:"terminal_enabled"`
	FileBrowserEnabled        bool                   `json:"file_browser_enabled"`
	FileBrowserRoot           string                 `json:"file_browser_root"`
	FileTransferChunkBytes    int                    `json:"file_transfer_chunk_bytes"`
	MaxFileTransferBytes      int64                  `json:"max_file_transfer_bytes"`
	DiskExcludeDevicePrefixes []string               `json:"disk_exclude_device_prefixes"`
	CloudflareAccess          cloudflareAccessConfig `json:"cloudflare_access"`
	TLS                       tlsConfig              `json:"tls"`

	heartbeatDuration   time.Duration
	reconnectMinDur     time.Duration
	reconnectMaxDur     time.Duration
	dialTimeoutDur      time.Duration
	handshakeTimeoutDur time.Duration
	writeTimeoutDur     time.Duration
	commandTimeoutDur   time.Duration
}

type cloudflareAccessConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type tlsConfig struct {
	InsecureSkipVerify bool `json:"insecure_skip_verify"`
}

func loadAgentConfig(path string) (agentConfig, error) {
	cfg := agentConfig{
		StateFile:                 "state.json",
		HeartbeatInterval:         "10s",
		ReconnectMin:              "1s",
		ReconnectMax:              "30s",
		DialTimeout:               "15s",
		HandshakeTimeout:          "15s",
		WriteTimeout:              "10s",
		CommandTimeout:            "30s",
		MaxCommandOutputBytes:     512 << 10,
		Shell:                     "/bin/bash",
		ExecEnabled:               true,
		TerminalEnabled:           true,
		FileBrowserEnabled:        false,
		FileBrowserRoot:           "/",
		FileTransferChunkBytes:    64 << 10,
		MaxFileTransferBytes:      1 << 30,
		DiskExcludeDevicePrefixes: []string{"/dev/loop", "/dev/zram", "/dev/ram"},
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode config %s: %w", path, err)
	}

	cfg.Server = strings.TrimSpace(cfg.Server)
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.EnrollToken = strings.TrimSpace(cfg.EnrollToken)
	cfg.StateFile = strings.TrimSpace(cfg.StateFile)
	cfg.Shell = strings.TrimSpace(cfg.Shell)
	cfg.FileBrowserRoot = strings.TrimSpace(cfg.FileBrowserRoot)
	for i := range cfg.DiskExcludeDevicePrefixes {
		cfg.DiskExcludeDevicePrefixes[i] = strings.TrimSpace(cfg.DiskExcludeDevicePrefixes[i])
	}
	if cfg.Server == "" {
		return cfg, fmt.Errorf("server is required")
	}
	if cfg.StateFile == "" {
		return cfg, fmt.Errorf("state_file is required")
	}
	if !filepath.IsAbs(cfg.StateFile) {
		cfg.StateFile = filepath.Join(filepath.Dir(path), cfg.StateFile)
	}
	if cfg.Shell == "" {
		return cfg, fmt.Errorf("shell is required")
	}
	if cfg.FileBrowserRoot == "" {
		cfg.FileBrowserRoot = "/"
	}
	if !filepath.IsAbs(cfg.FileBrowserRoot) {
		return cfg, fmt.Errorf("file_browser_root must be an absolute path")
	}
	cfg.FileBrowserRoot = filepath.Clean(cfg.FileBrowserRoot)
	if cfg.MaxCommandOutputBytes < 4096 {
		return cfg, fmt.Errorf("max_command_output_bytes must be at least 4096")
	}
	if cfg.FileTransferChunkBytes < 4096 || cfg.FileTransferChunkBytes > 512<<10 {
		return cfg, fmt.Errorf("file_transfer_chunk_bytes must be between 4096 and 524288")
	}
	if cfg.MaxFileTransferBytes < 0 {
		return cfg, fmt.Errorf("max_file_transfer_bytes must be >= 0; use 0 for unlimited")
	}

	if cfg.heartbeatDuration, err = positiveDuration("heartbeat_interval", cfg.HeartbeatInterval); err != nil {
		return cfg, err
	}
	if cfg.reconnectMinDur, err = positiveDuration("reconnect_min", cfg.ReconnectMin); err != nil {
		return cfg, err
	}
	if cfg.reconnectMaxDur, err = positiveDuration("reconnect_max", cfg.ReconnectMax); err != nil {
		return cfg, err
	}
	if cfg.reconnectMaxDur < cfg.reconnectMinDur {
		return cfg, fmt.Errorf("reconnect_max must be >= reconnect_min")
	}
	if cfg.dialTimeoutDur, err = positiveDuration("dial_timeout", cfg.DialTimeout); err != nil {
		return cfg, err
	}
	if cfg.handshakeTimeoutDur, err = positiveDuration("handshake_timeout", cfg.HandshakeTimeout); err != nil {
		return cfg, err
	}
	if cfg.writeTimeoutDur, err = positiveDuration("write_timeout", cfg.WriteTimeout); err != nil {
		return cfg, err
	}
	if cfg.commandTimeoutDur, err = positiveDuration("command_timeout", cfg.CommandTimeout); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, value)
	}
	return d, nil
}
