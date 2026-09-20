package token

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateProducesEntropyAndHash(t *testing.T) {
	m := New()
	raw, hash, err := m.Generate(ImportTokenPrefix)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(raw, ImportTokenPrefix) {
		t.Fatalf("raw token missing prefix: %q", raw)
	}
	body := strings.TrimPrefix(raw, ImportTokenPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("raw token is not base64url: %v", err)
	}
	// Contract: at least 32 bytes of crypto/rand entropy.
	if len(decoded) < 32 {
		t.Fatalf("token entropy too small: got %d bytes, want >= 32", len(decoded))
	}
	if len(hash) != 32 {
		t.Fatalf("hash must be SHA-256 (32 bytes), got %d", len(hash))
	}
	if !bytes.Equal(hash, Hash(raw)) {
		t.Fatal("returned hash does not match Hash(raw)")
	}
}

func TestGenerateUnique(t *testing.T) {
	m := New()
	seen := make(map[string]bool, 200)
	for i := 0; i < 200; i++ {
		raw, _, err := m.Generate("")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if seen[raw] {
			t.Fatalf("duplicate token generated at iteration %d", i)
		}
		seen[raw] = true
	}
}

func TestEqualHashes(t *testing.T) {
	m := New()
	raw, hash, err := m.Generate("")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !EqualHashes(hash, Hash(raw)) {
		t.Fatal("equal hashes compared unequal")
	}
	if EqualHashes(hash, Hash(raw+"x")) {
		t.Fatal("different raw values compared equal")
	}
	// Length-mismatched inputs must be rejected, not panic.
	if EqualHashes(hash, []byte("short")) {
		t.Fatal("length-mismatched hashes compared equal")
	}
}

func TestHashIsSHA256AndDeterministic(t *testing.T) {
	if !bytes.Equal(Hash("abc"), Hash("abc")) {
		t.Fatal("hash not deterministic")
	}
	if bytes.Equal(Hash("abc"), Hash("abd")) {
		t.Fatal("hash collision for distinct inputs")
	}
	if len(Hash("abc")) != 32 {
		t.Fatal("hash length is not 32 bytes")
	}
}

func TestNewSessionID(t *testing.T) {
	a, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	b, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil || len(decoded) < 32 {
		t.Fatalf("session id entropy too small or malformed: %v (%d bytes)", err, len(decoded))
	}
	if a == b {
		t.Fatal("session ids must be unique")
	}
}
