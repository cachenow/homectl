package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type agentState struct {
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token,omitempty"`
}

func loadOrCreateState(path string) (agentState, error) {
	var state agentState
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &state); err != nil {
			return state, fmt.Errorf("decode state %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return state, fmt.Errorf("read state %s: %w", path, err)
	}
	state.DeviceID = strings.TrimSpace(state.DeviceID)
	state.DeviceToken = strings.TrimSpace(state.DeviceToken)
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

func saveState(path string, state agentState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(path, 0600)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
