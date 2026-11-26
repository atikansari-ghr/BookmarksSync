package identity

import (
	"bytes"
	"testing"
)

// RFC 6238 Appendix B test vectors (SHA-1, 8 digits, 30 s period, T0 = 0).
func TestTOTPMatchesRFC6238Vectors(t *testing.T) {
	seed := []byte("12345678901234567890")
	for unix, want := range map[int64]string{
		59:          "94287082",
		1111111109:  "07081804",
		1111111111:  "14050471",
		1234567890:  "89005924",
		2000000000:  "69279037",
		20000000000: "65353130",
	} {
		if got := hotp(seed, uint64(unix/30), 8); got != want {
			t.Errorf("T=%d: %s, want %s", unix, got, want)
		}
	}
	// The service's own parameters: 6 digits truncate the same value.
	if got := hotp(seed, 59/30, TOTPDigits); got != "287082" {
		t.Errorf("6-digit code = %s", got)
	}
	if TOTPDigits != 6 || TOTPPeriod.Seconds() != 30 || TOTPSkewSteps != 1 || TOTPAlgorithm != "SHA1" {
		t.Errorf("TOTP parameters changed: %d digits, %v, skew %d, %s", TOTPDigits, TOTPPeriod, TOTPSkewSteps, TOTPAlgorithm)
	}
}

func TestSecretBoxRoundTripAndBinding(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	box, err := newSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("seed material 1234")
	ct, err := box.seal(plain, "totp-seed/1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatal("ciphertext contains the plaintext")
	}
	ct2, _ := box.seal(plain, "totp-seed/1")
	if bytes.Equal(ct, ct2) {
		t.Fatal("nonce reused: identical ciphertexts")
	}
	got, err := box.open(ct, "totp-seed/1")
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := box.open(ct, "totp-seed/2"); err == nil {
		t.Fatal("ciphertext opened under another user's binding")
	}
	tampered := append([]byte{}, ct...)
	tampered[len(tampered)-1] ^= 1
	if _, err := box.open(tampered, "totp-seed/1"); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
	other, _ := newSecretBox(bytes.Repeat([]byte{0x43}, 32))
	if _, err := other.open(ct, "totp-seed/1"); err == nil {
		t.Fatal("opened with another instance key")
	}
	if _, err := box.open(ct[:5], "totp-seed/1"); err == nil {
		t.Fatal("truncated ciphertext opened")
	}
	if _, err := newSecretBox(make([]byte, 16)); err == nil {
		t.Fatal("short key accepted")
	}
}
