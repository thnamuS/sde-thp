package platform

import "testing"

func TestHashesDoNotExposeSecrets(t *testing.T) {
	secret := "nx_live_super-secret"
	hash := SHA256Hex(secret)
	if hash == secret || len(hash) != 64 {
		t.Fatalf("unexpected hash %q", hash)
	}
	if hash != SHA256Hex(secret) {
		t.Fatal("hash must be deterministic")
	}
}
func TestWebhookSignature(t *testing.T) {
	payload := []byte(`{"event":"operation.succeeded"}`)
	signature := HMACSignature("secret", payload)
	if !ValidHMAC("secret", payload, signature) {
		t.Fatal("valid signature rejected")
	}
	if ValidHMAC("wrong", payload, signature) {
		t.Fatal("invalid signature accepted")
	}
	if ValidHMAC("secret", []byte("tampered"), signature) {
		t.Fatal("tampered payload accepted")
	}
}
