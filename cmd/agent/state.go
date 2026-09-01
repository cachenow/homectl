package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"homectl/internal/protocol"

	"golang.org/x/sys/unix"
)

const maxStateFileBytes = 64 << 10

type agentState struct {
	DeviceID          string `json:"device_id"`
	DeviceToken       string `json:"device_token,omitempty"`
	PendingEnrollment bool   `json:"pending_enrollment,omitempty"`
}

func loadOrCreateState(path string) (agentState, error) {
	var state agentState
	// O_NONBLOCK prevents a hostile FIFO at the configured path from hanging
	// startup before the descriptor can be checked with Stat.
	f, openErr := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if openErr == nil {
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return state, fmt.Errorf("inspect state %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return state, fmt.Errorf("state %s must be a regular file, not a symlink or special file", path)
		}
		if info.Size() > maxStateFileBytes {
			return state, fmt.Errorf("state %s exceeds %d bytes", path, maxStateFileBytes)
		}
		b, err := io.ReadAll(io.LimitReader(f, maxStateFileBytes+1))
		if err != nil {
			return state, fmt.Errorf("read state %s: %w", path, err)
		}
		if len(b) > maxStateFileBytes {
			return state, fmt.Errorf("state %s exceeds %d bytes", path, maxStateFileBytes)
		}
		if err := json.Unmarshal(b, &state); err != nil {
			return state, fmt.Errorf("decode state %s: %w", path, err)
		}
		if err := f.Chmod(0600); err != nil {
			return state, fmt.Errorf("secure state %s: %w", path, err)
		}
	} else if !os.IsNotExist(openErr) {
		return state, fmt.Errorf("open state %s without following symlinks: %w", path, openErr)
	}
	if err := validateAgentState(state); err != nil {
		return state, fmt.Errorf("state %s: %w", path, err)
	}
	if state.DeviceID == "" {
		id, err := randomHex(16)
		if err != nil {
			return state, err
		}
		state.DeviceID = id
		if err := saveState(path, state); err != nil {
			return state, err
		}
	}
	return state, nil
}

func validateAgentState(state agentState) error {
	if state.DeviceID != "" && !validAgentDeviceID(state.DeviceID) {
		return fmt.Errorf("contains an invalid device_id")
	}
	if state.DeviceToken != "" && !protocol.ValidDeviceToken(state.DeviceToken) {
		return fmt.Errorf("contains an invalid device_token")
	}
	if state.PendingEnrollment && state.DeviceToken == "" {
		return fmt.Errorf("pending enrollment requires a device_token")
	}
	return nil
}

func validAgentDeviceID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, ch := range id {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return false
	}
	return true
}

func saveState(path string, state agentState) error {
	if err := validateAgentState(state); err != nil {
		return fmt.Errorf("refusing to save invalid state: %w", err)
	}
	dirPath := filepath.Dir(path)
	if err := os.MkdirAll(dirPath, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(dirPath, ".homectl-state-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	if err := f.Chmod(0600); err != nil {
		cleanup()
		return err
	}
	if _, err := f.Write(b); err != nil {
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if dir, err := os.Open(dirPath); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
