package auth

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestAPIKeyStore(t *testing.T) (*SQLiteAPIKeyStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-keys.db")
	store, err := NewAPIKeyStore(path)
	if err != nil {
		t.Fatalf("NewAPIKeyStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

func TestAPIKeyStore_CreateReturnsPlaintextOnceAndStoresOnlyHash(t *testing.T) {
	store, path := newTestAPIKeyStore(t)

	key, plaintext, err := store.Create("alice", []string{"devs"}, "MCP tool")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if plaintext == "" {
		t.Fatal("Create returned an empty plaintext key")
	}
	if key.ID == "" {
		t.Error("Create returned an empty key ID")
	}
	if key.Username != "alice" {
		t.Errorf("Username = %q, want alice", key.Username)
	}
	if key.Description != "MCP tool" {
		t.Errorf("Description = %q, want %q", key.Description, "MCP tool")
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var storedHash string
	if err := db.QueryRow(`SELECT hash FROM api_keys WHERE id = ?`, key.ID).Scan(&storedHash); err != nil {
		t.Fatalf("read row: %v", err)
	}
	sum := sha256.Sum256([]byte(plaintext))
	if want := hex.EncodeToString(sum[:]); storedHash != want {
		t.Errorf("stored hash = %q, want hex(sha256(plaintext)) %q", storedHash, want)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read db file: %v", err)
	}
	if strings.Contains(string(raw), plaintext) {
		t.Error("plaintext key found in the database file")
	}
}

func TestAPIKeyStore_LookupResolvesCreatorIdentity(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)

	_, plaintext, err := store.Create("alice", []string{"devs", "sre"}, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Lookup(plaintext)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("Lookup returned nil for a valid key")
	}
	if got.Username != "alice" {
		t.Errorf("Username = %q, want alice", got.Username)
	}
	if len(got.Groups) != 2 || got.Groups[0] != "devs" || got.Groups[1] != "sre" {
		t.Errorf("Groups = %v, want [devs sre]", got.Groups)
	}
}

func TestAPIKeyStore_LookupRejectsUnknownAndEmpty(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)
	if _, _, err := store.Create("alice", nil, "ci"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, candidate := range []string{"", "rk_deadbeef", "not-a-key", strings.Repeat("x", 5000)} {
		got, err := store.Lookup(candidate)
		if err != nil {
			t.Fatalf("Lookup(len %d): unexpected error %v", len(candidate), err)
		}
		if got != nil {
			t.Errorf("Lookup(len %d) = %+v, want nil", len(candidate), got)
		}
	}
}

// A comma inside an OIDC group name must survive the round-trip. Comma-joined
// encoding splits it into two groups and hands the key memberships the creator
// never had.
func TestAPIKeyStore_GroupsRoundTripLosslessly(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)
	groups := []string{"Engineering, US", `weird"group`, "plain"}

	_, plaintext, err := store.Create("alice", groups, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.Lookup(plaintext)
	if err != nil || got == nil {
		t.Fatalf("Lookup: %v (key=%v)", err, got)
	}
	if len(got.Groups) != len(groups) {
		t.Fatalf("Groups = %v, want %v", got.Groups, groups)
	}
	for i := range groups {
		if got.Groups[i] != groups[i] {
			t.Errorf("Groups[%d] = %q, want %q", i, got.Groups[i], groups[i])
		}
	}
}

func TestAPIKeyStore_DeleteRevokesTheKey(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)

	key, plaintext, err := store.Create("alice", []string{"devs"}, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deleted, err := store.Delete(key.ID, "alice")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted {
		t.Fatal("Delete reported no rows removed")
	}

	got, err := store.Lookup(plaintext)
	if err != nil {
		t.Fatalf("Lookup after delete: %v", err)
	}
	if got != nil {
		t.Error("revoked key still resolves")
	}
}

func TestAPIKeyStore_DeleteIsScopedToOwner(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)

	key, plaintext, err := store.Create("alice", nil, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deleted, err := store.Delete(key.ID, "mallory")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if deleted {
		t.Fatal("another user's key was deleted")
	}
	got, err := store.Lookup(plaintext)
	if err != nil || got == nil {
		t.Fatalf("key stopped working after a foreign delete attempt: %v (key=%v)", err, got)
	}
}

func TestAPIKeyStore_ListForUserIsScoped(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)

	if _, _, err := store.Create("alice", []string{"devs"}, "alice key"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := store.Create("bob", []string{"devs"}, "bob key"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	keys, err := store.ListForUser("alice")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("ListForUser returned %d keys, want 1", len(keys))
	}
	if keys[0].Username != "alice" {
		t.Errorf("Username = %q, want alice", keys[0].Username)
	}
	if keys[0].Description != "alice key" {
		t.Errorf("Description = %q, want %q", keys[0].Description, "alice key")
	}
}

func TestAPIKeyStore_DatabaseFileIsOwnerOnly(t *testing.T) {
	_, path := newTestAPIKeyStore(t)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("db file mode = %o, want 600", perm)
	}
}

// The admin surface below backs `radar api-keys` and is intentionally absent
// from the APIKeyStore interface, so no HTTP handler can reach across owners.

func TestAPIKeyStore_ListAllSpansOwners(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)

	for _, user := range []string{"alice", "bob", "alice"} {
		if _, _, err := store.Create(user, nil, "k"); err != nil {
			t.Fatalf("Create(%q): %v", user, err)
		}
	}

	keys, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("ListAll returned %d keys, want 3", len(keys))
	}
	owners := map[string]int{}
	for _, key := range keys {
		owners[key.Username]++
	}
	if owners["alice"] != 2 || owners["bob"] != 1 {
		t.Errorf("owners = %v, want alice:2 bob:1", owners)
	}
}

func TestAPIKeyStore_DeleteByIDIgnoresOwner(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)

	key, plaintext, err := store.Create("dana", []string{"devs"}, "offboarded")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Delete (owner-scoped, HTTP path) refuses under another name; DeleteByID
	// is what an operator uses once the account is gone.
	deleted, err := store.Delete(key.ID, "admin")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if deleted {
		t.Fatal("owner-scoped Delete revoked another user's key")
	}

	deleted, err = store.DeleteByID(key.ID)
	if err != nil {
		t.Fatalf("DeleteByID: %v", err)
	}
	if !deleted {
		t.Fatal("DeleteByID did not revoke the key")
	}

	resolved, err := store.Lookup(plaintext)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if resolved != nil {
		t.Error("revoked key still authenticates")
	}
}

func TestAPIKeyStore_DeleteAllForUserSparesOthers(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)

	for _, user := range []string{"dana", "dana", "bob"} {
		if _, _, err := store.Create(user, nil, "k"); err != nil {
			t.Fatalf("Create(%q): %v", user, err)
		}
	}

	n, err := store.DeleteAllForUser("dana")
	if err != nil {
		t.Fatalf("DeleteAllForUser: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d, want 2", n)
	}

	remaining, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Username != "bob" {
		t.Errorf("remaining = %+v, want bob's single key", remaining)
	}

	// An unknown user is a no-op, not an error: offboarding scripts run it
	// for everyone who leaves.
	n, err = store.DeleteAllForUser("nobody")
	if err != nil {
		t.Fatalf("DeleteAllForUser(unknown): %v", err)
	}
	if n != 0 {
		t.Errorf("deleted %d for an unknown user, want 0", n)
	}
}

func TestAPIKeyStore_DeleteAllEmptiesTheStore(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)

	for _, user := range []string{"alice", "bob"} {
		if _, _, err := store.Create(user, nil, "k"); err != nil {
			t.Fatalf("Create(%q): %v", user, err)
		}
	}

	n, err := store.DeleteAll()
	if err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d, want 2", n)
	}

	remaining, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("remaining = %+v, want none", remaining)
	}
}

// OpenExistingAPIKeyStore must never conjure a database: the admin CLI would
// otherwise answer "no keys" to a mistyped path, which reads as "nothing to
// revoke" rather than "wrong file".
func TestOpenExistingAPIKeyStore_DoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")

	if _, err := OpenExistingAPIKeyStore(path); !os.IsNotExist(err) {
		t.Fatalf("err = %v, want a not-exist error", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("OpenExistingAPIKeyStore created the database")
	}
}

// The admin CLI is a second process on the server's database. Without WAL and
// busy_timeout it would fail with SQLITE_BUSY instead of revoking, and the
// server's own lookups would surface that as a spurious 401.
func TestAPIKeyStore_SecondProcessCanRevokeWhileServerHoldsTheStore(t *testing.T) {
	server, path := newTestAPIKeyStore(t)

	key, plaintext, err := server.Create("dana", []string{"devs"}, "offboarded")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	admin, err := OpenExistingAPIKeyStore(path)
	if err != nil {
		t.Fatalf("OpenExistingAPIKeyStore: %v", err)
	}
	defer admin.Close()

	if _, err := admin.DeleteByID(key.ID); err != nil {
		t.Fatalf("DeleteByID from a second handle: %v", err)
	}

	// The running server sees the revocation immediately — Lookup reads the
	// database on every request, with no cache to invalidate.
	resolved, err := server.Lookup(plaintext)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if resolved != nil {
		t.Error("server still authenticates a key the admin CLI revoked")
	}
}

func TestAPIKeyStore_WALPragmaIsSet(t *testing.T) {
	store, _ := newTestAPIKeyStore(t)

	var mode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}
