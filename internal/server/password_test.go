package server

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestArgon2PasswordHashAndVerify(t *testing.T) {
	password := "correct horse battery staple"
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("unexpected hash format: %q", hash)
	}
	ok, rehash := verifyPasswordHash(hash, password)
	if !ok || rehash {
		t.Fatalf("valid password rejected or unexpectedly marked for rehash: ok=%v rehash=%v", ok, rehash)
	}
	if ok, _ := verifyPasswordHash(hash, "wrong password value"); ok {
		t.Fatal("wrong password accepted")
	}
}

func TestPasswordNFCNormalization(t *testing.T) {
	// Same visual password with composed and decomposed e-acute forms.
	composed := "abcdefghijklmnop-é"
	decomposed := "abcdefghijklmnop-e\u0301"
	hash, err := hashPassword(composed)
	if err != nil {
		t.Fatal(err)
	}
	ok, _ := verifyPasswordHash(hash, decomposed)
	if !ok {
		t.Fatal("NFC-equivalent password was rejected")
	}
}

func TestPasswordPolicy(t *testing.T) {
	if _, err := hashPassword("short-password"); err == nil {
		t.Fatal("short password accepted")
	}
	// No composition rule: a long lowercase passphrase is valid.
	if _, err := hashPassword("this is a long password phrase"); err != nil {
		t.Fatalf("valid passphrase rejected: %v", err)
	}
}

func TestLegacyBcryptVerification(t *testing.T) {
	password := "legacy-password-1234"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	ok, rehash := verifyPasswordHash(string(hash), password)
	if !ok || !rehash {
		t.Fatalf("legacy bcrypt not accepted for migration: ok=%v rehash=%v", ok, rehash)
	}
}
