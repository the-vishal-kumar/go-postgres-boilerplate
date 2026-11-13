// Package auth holds password hashing, access tokens and token hashing.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// scrypt parameters match Node's crypto.scrypt defaults (N=16384, r=8, p=1)
// so hashes written by the Express implementation still verify here.
const (
	scryptN   = 16384
	scryptR   = 8
	scryptP   = 1
	keyLength = 64
	saltBytes = 16
)

// HashPassword returns "<hex key>.<hex salt>". Like the Node format, the salt
// is used as the ASCII text of its own hex encoding, not as raw bytes.
func HashPassword(password string) (string, error) {
	raw := make([]byte, saltBytes)
	_, _ = rand.Read(raw)
	salt := hex.EncodeToString(raw)
	key, err := scrypt.Key([]byte(password), []byte(salt), scryptN, scryptR, scryptP, keyLength)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(key) + "." + salt, nil
}

// VerifyPassword reports whether password matches a stored hash. A malformed
// stored value never matches.
func VerifyPassword(stored, password string) bool {
	hashHex, salt, ok := strings.Cut(stored, ".")
	if !ok || salt == "" {
		return false
	}
	want, err := hex.DecodeString(hashHex)
	if err != nil || len(want) != keyLength {
		return false
	}
	got, err := scrypt.Key([]byte(password), []byte(salt), scryptN, scryptR, scryptP, keyLength)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(want, got) == 1
}
