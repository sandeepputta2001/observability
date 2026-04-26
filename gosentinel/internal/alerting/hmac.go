package alerting

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// hmacSHA256Hex returns the HMAC-SHA256 hex digest of body using secret.
func hmacSHA256Hex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
