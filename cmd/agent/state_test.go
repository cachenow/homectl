package main

import (
	"path/filepath"
	"testing"
)

func TestStateCreateAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := loadOrCreateState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.DeviceID == "" {
		t.Fatal("device id was not generated")
	}
	state.DeviceToken = "device-token"
	if err := saveState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeviceID != state.DeviceID || loaded.DeviceToken != "device-token" {
		t.Fatalf("unexpected state: %#v", loaded)
	}
}
