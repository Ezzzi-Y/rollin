// Package token implements the opaque-token scheme (04-api-contract.md / 执行计划 3.3):
// raw values are 32 bytes from crypto/rand (>= the required 32), base64url-encoded with
// an optional type prefix (import tokens use "rt_"). The database stores ONLY the
// SHA-256 hash in a BINARY(32) unique key — no ciphertext, no key material. Hash
// comparison goes through a constant-time compare.
//
// The legacy AES-encrypted token ciphertext scheme is abolished: tokens cannot be
// re-mailed from the database, they are regenerated at each send attempt (88.6.1).
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"io"
)

// RandomBytes is the entropy of one raw token, in bytes (>= 32 required).
const RandomBytes = 32

// ImportTokenPrefix is the recognizable prefix of import tokens (04-api-contract.md §5.14).
const ImportTokenPrefix = "rt_"

// Manager is stateless; it exists so future key-dependent token kinds have a home and so
// callers can inject the generator in tests.
type Manager struct{}

func New() *Manager { return &Manager{} }

// Generate returns a fresh raw token (prefix + 43 base64url chars) and its SHA-256 hash.
// The raw value leaves the process exactly once (the creation response / the mail body).
func (m *Manager) Generate(prefix string) (raw string, hash []byte, err error) {
	buf := make([]byte, RandomBytes)
	if _, err = io.ReadFull(rand.Reader, buf); err != nil {
		return "", nil, err
	}
	raw = prefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, Hash(raw), nil
}

// NewSessionID mints an opaque session identifier (same entropy budget as tokens).
func NewSessionID() (string, error) {
	buf := make([]byte, RandomBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash returns the SHA-256 digest stored in BINARY(32) columns.
func Hash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// EqualHashes compares two hashes in constant time. Lookups use the unique hash index;
// this helper is for paths that compare a supplied digest against a stored one directly.
func EqualHashes(a, b []byte) bool {
	if len(a) != sha256.Size || len(b) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}
