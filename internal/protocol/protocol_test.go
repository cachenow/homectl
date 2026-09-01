package protocol

import "testing"

func TestTokenFormats(t *testing.T) {
	validEnrollment := "0123456789abcdef0123456789abcdef0123456789abcdef"
	validDevice := validEnrollment + "0123456789abcdef"
	if !ValidEnrollmentToken(validEnrollment) {
		t.Fatal("valid 48-character enrollment token was rejected")
	}
	if !ValidDeviceToken(validDevice) {
		t.Fatal("valid 64-character device token was rejected")
	}
	for _, token := range []string{
		validEnrollment[:len(validEnrollment)-1],
		validEnrollment + "0",
		"0123456789ABCDEF0123456789abcdef0123456789abcdef",
		"g123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		if ValidEnrollmentToken(token) {
			t.Fatalf("malformed enrollment token was accepted: %q", token)
		}
	}
}

func TestHasCapability(t *testing.T) {
	capabilities := []string{"other", CapabilityFileDownloadCredits, CapabilityFileUploadCredits}
	if !HasCapability(capabilities, CapabilityFileDownloadCredits) {
		t.Fatal("declared capability was not found")
	}
	if !HasCapability(capabilities, CapabilityFileUploadCredits) {
		t.Fatal("upload credit capability was not found")
	}
	if HasCapability([]string{"other"}, CapabilityFileDownloadCredits) {
		t.Fatal("missing capability was reported")
	}
}
