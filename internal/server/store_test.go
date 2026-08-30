package server

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteStoreDevicesAndAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homectl.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnsureAdmin("admin", "0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	admin, ok, err := s.VerifyAdminPassword("0123456789abcdef")
	if err != nil || !ok || admin.Username != "admin" {
		t.Fatalf("admin verification failed: %#v %v %v", admin, ok, err)
	}

	want := &DeviceRecord{ID: "device-1", Name: "lab", TokenHash: hashToken("secret"), LastSeen: 123}
	if err := s.Put(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("device-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != want.ID || got.Name != want.Name || !secureEqualBytes(got.TokenHash, want.TokenHash) {
		t.Fatalf("unexpected record: %#v", got)
	}
}

func TestEnrollmentTokenIsOneTime(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "homectl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateEnrollmentToken("id1", "test", "one-time-secret", time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ConsumeEnrollmentToken("one-time-secret")
	if err != nil || !ok {
		t.Fatalf("first consume failed: %v %v", ok, err)
	}
	ok, err = s.ConsumeEnrollmentToken("one-time-secret")
	if err != nil || ok {
		t.Fatalf("token reused: %v %v", ok, err)
	}
}
