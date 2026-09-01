package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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
	state.DeviceToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	state.PendingEnrollment = true
	if err := saveState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeviceID != state.DeviceID || loaded.DeviceToken != state.DeviceToken || !loaded.PendingEnrollment {
		t.Fatalf("unexpected state: %#v", loaded)
	}
}

func TestStateRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"device_id":"abc"}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateState(path); err == nil {
		t.Fatal("symlink state file was accepted")
	}
}

func TestStateRejectsInvalidIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"device_id":"../../bad","device_token":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateState(path); err == nil {
		t.Fatal("invalid device id was accepted")
	}
}

func TestSaveStateDoesNotUsePredictableTempPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep-me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	state := agentState{DeviceID: "device-1", DeviceToken: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := saveState(path, state); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep-me" {
		t.Fatalf("predictable temp path modified victim: %q", got)
	}
}

func TestStateRejectsMalformedDeviceToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"device_id":"device-1","device_token":"0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateState(path); err == nil {
		t.Fatal("non-lowercase device token was accepted")
	}
}

func TestStateRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, make([]byte, maxStateFileBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateState(path); err == nil {
		t.Fatal("oversized state file was accepted")
	}
}

func TestStateRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := loadOrCreateState(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO state file was accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("loading a FIFO state file blocked")
	}
}
