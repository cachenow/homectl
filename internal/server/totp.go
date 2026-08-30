package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // RFC 6238 default used by Google Authenticator.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func newTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

func validTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	if _, err := strconv.Atoi(code); err != nil {
		return false
	}
	for drift := int64(-1); drift <= 1; drift++ {
		if totpCode(secret, now.Unix()/30+drift) == code {
			return true
		}
	}
	return false
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
