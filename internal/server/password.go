package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/unicode/norm"
)

const (
	minPasswordRunes = 16
	maxPasswordRunes = 256
	maxPasswordBytes = 1024

	argonMemoryKiB uint32 = 19 * 1024
	argonTime      uint32 = 2
	argonThreads   uint8  = 1
	argonSaltBytes        = 16
	argonKeyBytes  uint32 = 32
)

func normalizePassword(password string) (string, error) {
	if !utf8.ValidString(password) {
		return "", errors.New("password must be valid UTF-8")
	}
	if len(password) > maxPasswordBytes {
		return "", fmt.Errorf("password must be at most %d bytes", maxPasswordBytes)
	}
	normalized := norm.NFC.String(password)
	if len(normalized) > maxPasswordBytes {
		return "", fmt.Errorf("password must be at most %d bytes after normalization", maxPasswordBytes)
	}
	return normalized, nil
}

func normalizeAndValidatePassword(password string) (string, error) {
	normalized, err := normalizePassword(password)
	if err != nil {
		return "", err
	}
	runes := utf8.RuneCountInString(normalized)
	if runes < minPasswordRunes {
		return "", fmt.Errorf("password must be at least %d characters", minPasswordRunes)
	}
	if runes > maxPasswordRunes {
		return "", fmt.Errorf("password must be at most %d characters", maxPasswordRunes)
	}
	return normalized, nil
}

func hashPassword(password string) (string, error) {
	normalized, err := normalizeAndValidatePassword(password)
	if err != nil {
		return "", err
	}
	return hashNormalizedPassword(normalized)
}

func rehashVerifiedPassword(password string) (string, error) {
	normalized, err := normalizePassword(password)
	if err != nil {
		return "", err
	}
	return hashNormalizedPassword(normalized)
}

func hashNormalizedPassword(normalized string) (string, error) {
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(normalized), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemoryKiB,
		argonTime,
		argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func verifyPasswordHash(encoded, password string) (ok bool, needsRehash bool) {
	if strings.HasPrefix(encoded, "$2a$") || strings.HasPrefix(encoded, "$2b$") || strings.HasPrefix(encoded, "$2y$") {
		if bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)) == nil {
			return true, true
		}
		return false, false
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		return false, false
	}

	normalized, err := normalizePassword(password)
	if err != nil {
		return false, false
	}
	memory, iterations, parallelism, salt, want, err := parseArgon2PHC(encoded)
	if err != nil {
		return false, false
	}
	got := argon2.IDKey([]byte(normalized), salt, iterations, memory, parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false
	}
	needs := memory < argonMemoryKiB || iterations < argonTime || len(salt) < argonSaltBytes || len(want) < int(argonKeyBytes)
	return true, needs
}

func parseArgon2PHC(encoded string) (memory uint32, iterations uint32, parallelism uint8, salt, hash []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return 0, 0, 0, nil, nil, errors.New("invalid argon2id PHC string")
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 || !strings.HasPrefix(params[0], "m=") || !strings.HasPrefix(params[1], "t=") || !strings.HasPrefix(params[2], "p=") {
		return 0, 0, 0, nil, nil, errors.New("invalid argon2id parameters")
	}
	m64, err := strconv.ParseUint(strings.TrimPrefix(params[0], "m="), 10, 32)
	if err != nil {
		return 0, 0, 0, nil, nil, errors.New("invalid argon2id memory parameter")
	}
	t64, err := strconv.ParseUint(strings.TrimPrefix(params[1], "t="), 10, 32)
	if err != nil {
		return 0, 0, 0, nil, nil, errors.New("invalid argon2id time parameter")
	}
	p64, err := strconv.ParseUint(strings.TrimPrefix(params[2], "p="), 10, 8)
	if err != nil {
		return 0, 0, 0, nil, nil, errors.New("invalid argon2id parallelism parameter")
	}
	memory, iterations = uint32(m64), uint32(t64)
	if memory < 7*1024 || memory > 256*1024 || iterations < 1 || iterations > 10 || p64 < 1 || p64 > 16 {
		return 0, 0, 0, nil, nil, errors.New("argon2id parameters out of bounds")
	}
	parallelism = uint8(p64)
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return 0, 0, 0, nil, nil, errors.New("invalid argon2id salt")
	}
	hash, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(hash) < 16 || len(hash) > 64 {
		return 0, 0, 0, nil, nil, errors.New("invalid argon2id hash")
	}
	return memory, iterations, parallelism, salt, hash, nil
}
