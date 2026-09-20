package secretbox

import (
	"bytes"
	"testing"
)

func testKey() []byte { return bytes.Repeat([]byte{7}, KeyLen) }

func TestSealOpenRoundtrip(t *testing.T) {
	key := testKey()
	plaintext := []byte("smtp-password-实例-123")
	box, err := Seal(key, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(box, plaintext) {
		t.Fatal("ciphertext must not contain the plaintext")
	}
	opened, err := Open(key, box)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("roundtrip mismatch: %q", opened)
	}
}

func TestSealIsRandomized(t *testing.T) {
	key := testKey()
	a, _ := Seal(key, []byte("same"))
	b, _ := Seal(key, []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext must differ (random nonce)")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	key := testKey()
	box, err := Seal(key, []byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	box[len(box)-1] ^= 0xFF
	if _, err := Open(key, box); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
	if _, err := Open(testKey2(), box); err == nil {
		t.Fatal("wrong key must fail")
	}
	if _, err := Open(key, []byte("tiny")); err == nil {
		t.Fatal("truncated ciphertext must fail")
	}
}

func TestKeyLengthEnforced(t *testing.T) {
	if _, err := Seal([]byte("short"), []byte("x")); err == nil {
		t.Fatal("keys shorter than 32 bytes must be rejected")
	}
	if _, err := Seal(make([]byte, 31), []byte("x")); err == nil {
		t.Fatal("31-byte keys must be rejected")
	}
}

func testKey2() []byte { return bytes.Repeat([]byte{8}, KeyLen) }
