package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // RFC 6238 default; broad authenticator compatibility.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func newTOTPSecret() (string, error) {
	b := make([]byte, 20) // 160-bit secret, matching the RFC 4226 reference size.
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

func validTOTP(secret, code string, now time.Time) bool {
	_, ok := matchTOTP(secret, code, now)
	return ok
}

// matchTOTP returns the accepted 30-second time step. A one-step clock skew in
// either direction is allowed for interoperability; login replay prevention is
// handled separately by recording the last accepted login step in SQLite.
func matchTOTP(secret, code string, now time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	if _, err := strconv.Atoi(code); err != nil {
		return 0, false
	}
	current := now.Unix() / 30
	for _, drift := range []int64{0, -1, 1} {
		step := current + drift
		if hmac.Equal([]byte(totpCode(secret, step)), []byte(code)) {
			return step, true
		}
	}
	return 0, false
}

func totpCode(secret string, counter int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return ""
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])
	return fmt.Sprintf("%06d", bin%1000000)
}
