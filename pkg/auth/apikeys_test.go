package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGenerateAPIKey_ShapeAndUniqueness(t *testing.T) {
	id, plaintext, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	if !strings.HasPrefix(plaintext, APIKeyPrefix) {
		t.Errorf("plaintext = %q, want prefix %q", plaintext, APIKeyPrefix)
	}
	// 32 random bytes, hex-encoded, after the prefix.
	if got := len(plaintext) - len(APIKeyPrefix); got != 64 {
		t.Errorf("secret length = %d hex chars, want 64", got)
	}

	id2, plaintext2, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if id == id2 || plaintext == plaintext2 {
		t.Error("GenerateAPIKey produced a duplicate id or key")
	}
}

func TestHashAPIKey(t *testing.T) {
	sum := sha256.Sum256([]byte("rk_abc"))
	if got, want := HashAPIKey("rk_abc"), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("HashAPIKey = %q, want %q", got, want)
	}
	if HashAPIKey("rk_abc") == HashAPIKey("rk_abd") {
		t.Error("distinct keys hashed to the same value")
	}
}

// The APIKey record is what the API returns to clients, so it must have no
// place to put the secret or its hash in the first place.
func TestAPIKey_SerializesNoSecretMaterial(t *testing.T) {
	key := APIKey{
		ID:          "abc123",
		Description: "MCP tool",
		Username:    "alice",
		Groups:      []string{"devs"},
		CreatedAt:   time.Now().UTC(),
	}
	blob, err := json.Marshal(key)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"hash", "\"key\"", "secret", "plaintext"} {
		if strings.Contains(strings.ToLower(string(blob)), forbidden) {
			t.Errorf("APIKey JSON contains %q: %s", forbidden, blob)
		}
	}
}

func TestLooksLikeAPIKey(t *testing.T) {
	_, minted, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !LooksLikeAPIKey(minted) {
		t.Errorf("a freshly minted key %q was rejected", minted)
	}
	if len(minted) != APIKeyLength {
		t.Errorf("minted length = %d, want APIKeyLength %d", len(minted), APIKeyLength)
	}

	for _, tc := range []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"prefix only", APIKeyPrefix},
		{"too short", APIKeyPrefix + "deadbeef"},
		{"too long", APIKeyPrefix + strings.Repeat("a", 65)},
		{"missing prefix", strings.Repeat("a", APIKeyLength)},
		{"uppercase hex", APIKeyPrefix + strings.Repeat("A", 64)},
		{"non-hex", APIKeyPrefix + strings.Repeat("z", 64)},
		{"jwt", "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.e30.sig"},
		{"whitespace padded", " " + APIKeyPrefix + strings.Repeat("a", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if LooksLikeAPIKey(tc.value) {
				t.Errorf("LooksLikeAPIKey(%q) = true, want false", tc.value)
			}
		})
	}
}

// Every key the generator produces must satisfy the shape check — the two
// definitions live together precisely so they cannot drift.
func TestLooksLikeAPIKeyAcceptsEveryGeneratedKey(t *testing.T) {
	for i := 0; i < 200; i++ {
		_, plaintext, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("GenerateAPIKey: %v", err)
		}
		if !LooksLikeAPIKey(plaintext) {
			t.Fatalf("generated key %q failed the shape check", plaintext)
		}
	}
}
