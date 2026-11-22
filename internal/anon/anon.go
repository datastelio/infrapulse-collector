// Package anon pseudonymises identifiers (hostnames, UUIDs, pod and job names)
// before they leave the operator's network. The same input and salt always
// give the same output, so joins across files still work, but the original
// value cannot be read back without the salt.
package anon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Hasher pseudonymises values with a secret salt. A nil Hasher passes values
// through unchanged.
type Hasher struct {
	salt []byte
}

// New returns a Hasher. An empty salt returns nil (no hashing).
func New(salt string) *Hasher {
	if salt == "" {
		return nil
	}
	return &Hasher{salt: []byte(salt)}
}

// Hash returns prefix + "_" + the first 12 hex characters of HMAC-SHA256(salt, value).
// Empty values stay empty.
func (h *Hasher) Hash(prefix, value string) string {
	if h == nil || value == "" {
		return value
	}
	m := hmac.New(sha256.New, h.salt)
	m.Write([]byte(prefix))
	m.Write([]byte{0})
	m.Write([]byte(value))
	return prefix + "_" + hex.EncodeToString(m.Sum(nil))[:12]
}
