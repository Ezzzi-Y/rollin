// Package secretbox encrypts small server-side secrets (currently: SMTP passwords) with
// AES-256-GCM under the dedicated SMTP_ENC_KEY from the environment — deliberately
// separate from any token material (05-data-model.md §12; 需求 13 章). Output layout is
// nonce || ciphertext||tag; authentication failures surface as errors, never as garbage.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// KeyLen is the required key size (AES-256).
const KeyLen = 32

var errShortKey = errors.New("secretbox: key must be 32 bytes (AES-256)")

// Seal encrypts plaintext with AES-256-GCM. A fresh random nonce is prepended.
func Seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := gcm(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secretbox: nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func Open(key, box []byte) ([]byte, error) {
	gcm, err := gcm(key)
	if err != nil {
		return nil, err
	}
	if len(box) < gcm.NonceSize() {
		return nil, errors.New("secretbox: ciphertext too short")
	}
	nonce, body := box[:gcm.NonceSize()], box[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, errors.New("secretbox: authentication failed")
	}
	return plaintext, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != KeyLen {
		return nil, errShortKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
