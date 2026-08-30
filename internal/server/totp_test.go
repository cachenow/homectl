package server

import (
	"testing"
	"time"
)

func TestTOTP(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	code := totpCode(secret, now.Unix()/30)
	if !validTOTP(secret, code, now) {
		t.Fatal("valid TOTP was rejected")
	}
	if validTOTP(secret, "000000", now) && code != "000000" {
		t.Fatal("invalid TOTP was accepted")
	}
}
