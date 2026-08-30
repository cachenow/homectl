package server

import (
	"path/filepath"
	"testing"
)

func TestStorePersistsDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := &DeviceRecord{ID: "device-1", Name: "lab", Token: "secret", LastSeen: 123}
	if err := s.Put(want); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get("device-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != want.ID || got.Token != want.Token || got.Name != want.Name {
		t.Fatalf("unexpected record: %#v", got)
	}
}
