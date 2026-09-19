package platform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func SHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func HMACSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
func ValidHMAC(secret string, payload []byte, signature string) bool {
	return hmac.Equal([]byte(HMACSignature(secret, payload)), []byte(signature))
}
