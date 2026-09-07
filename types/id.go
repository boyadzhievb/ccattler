package types

import (
	"crypto/rand"
	"encoding/hex"
)

// NewInstanceID generates a short random hex ID for a new instance.
// It produces 5 random bytes encoded as 10 hex characters (e.g. "a8f31bc904").
func NewInstanceID() string {
	b := make([]byte, 5)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// IDFunc is the signature for ID generator functions. It allows tests to
// inject deterministic ID generators instead of the random default, making
// instance creation reproducible in test scenarios.
type IDFunc func() string
