package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// APIKeyPrefix marks a Radar API key. It is part of the secret (it is hashed
// with the rest of it) and makes keys recognizable to secret scanners and
// distinguishable from OIDC access tokens on the Authorization header.
const APIKeyPrefix = "rk_"

// apiKeySecretHexLen is the hex-encoded length of the 32-byte secret minted by
// GenerateAPIKey. Kept beside the generator so the two cannot drift.
const apiKeySecretHexLen = 64

// APIKeyLength is the exact length of a minted key: the prefix plus the
// hex-encoded secret.
const APIKeyLength = len(APIKeyPrefix) + apiKeySecretHexLen

// MaxAPIKeyLength bounds what an APIKeyStore will hash from a caller-supplied
// value, so an arbitrarily large Authorization header can't be turned into
// work. It is a defensive bound inside the store; callers reading credentials
// off a request should use LooksLikeAPIKey first, which is exact.
const MaxAPIKeyLength = 256

// LooksLikeAPIKey reports whether s has the shape of a Radar API key. It is a
// structural test, not authentication — a well-formed key is still unknown
// until an APIKeyStore resolves it.
//
// Checking the shape before consulting the store keeps every other kind of
// bearer token out of the credential path: an OIDC access token on the
// Authorization header is never hashed and probed against the key database,
// and junk from a scanner costs nothing and logs nothing.
func LooksLikeAPIKey(s string) bool {
	if len(s) != APIKeyLength || !strings.HasPrefix(s, APIKeyPrefix) {
		return false
	}
	for i := len(APIKeyPrefix); i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// APIKey is a long-lived credential bound to the identity of the user who
// created it. It carries no secret material: the plaintext is returned exactly
// once at creation and only its SHA-256 is persisted, so this record is safe
// to serialize to clients.
type APIKey struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Username    string    `json:"username"`
	Groups      []string  `json:"groups"`
	CreatedAt   time.Time `json:"createdAt"`
}

// APIKeyStore persists API keys and resolves a presented key to the identity
// it was minted for. Implementations must scope ListForUser and Delete to the
// owning username — one user's key is invisible to another.
//
// Declared here (rather than alongside the SQLite implementation) so Config
// can reference it without pulling a database driver into this module, which
// is deliberately dependency-light for reuse.
type APIKeyStore interface {
	// Create mints a key for the given identity and returns the record plus
	// the plaintext key, which the caller can never obtain again.
	Create(username string, groups []string, description string) (*APIKey, string, error)
	// Lookup resolves a plaintext key to its record, or nil when no key
	// matches. A nil record with a nil error means "not a valid key".
	Lookup(plaintext string) (*APIKey, error)
	// ListForUser returns the keys owned by username.
	ListForUser(username string) ([]APIKey, error)
	// Delete revokes id if it is owned by username, reporting whether a key
	// was removed.
	Delete(id, username string) (bool, error)
}

// GenerateAPIKey mints a new key ID and plaintext key. The ID is not secret;
// the plaintext is 32 bytes of entropy behind APIKeyPrefix.
func GenerateAPIKey() (id, plaintext string, err error) {
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return "", "", fmt.Errorf("generate api key id: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", fmt.Errorf("generate api key: %w", err)
	}
	return hex.EncodeToString(idBytes), APIKeyPrefix + hex.EncodeToString(secret), nil
}

// HashAPIKey returns the hex-encoded SHA-256 of a plaintext key — the only
// form of a key that is ever written to disk.
func HashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
