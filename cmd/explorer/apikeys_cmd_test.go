package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// seedKeyStore builds a key database with the given username→description keys
// and returns its path plus the minted ids, in creation order.
func seedKeyStore(t *testing.T, keys ...[2]string) (string, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-keys.db")
	store, err := auth.NewAPIKeyStore(path)
	if err != nil {
		t.Fatalf("NewAPIKeyStore: %v", err)
	}
	defer store.Close()

	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		key, _, err := store.Create(k[0], []string{"devs"}, k[1])
		if err != nil {
			t.Fatalf("Create(%q): %v", k[0], err)
		}
		ids = append(ids, key.ID)
	}
	return path, ids
}

func runAPIKeys(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runAPIKeysSubcommand(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestAPIKeysList(t *testing.T) {
	path, ids := seedKeyStore(t, [2]string{"alice", "mcp"}, [2]string{"bob", "ci"})

	code, out, errOut := runAPIKeys(t, "list", "--file", path)
	if code != 0 {
		t.Fatalf("list: code = %d, stderr = %s", code, errOut)
	}
	for _, want := range []string{ids[0], ids[1], "alice", "bob", "mcp", "ci"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}

	// --user narrows to one owner.
	code, out, _ = runAPIKeys(t, "list", "--file", path, "--user", "alice")
	if code != 0 {
		t.Fatalf("list --user: code = %d", code)
	}
	if !strings.Contains(out, "alice") || strings.Contains(out, "bob") {
		t.Errorf("list --user alice leaked another owner:\n%s", out)
	}
}

// A mistyped path must not silently produce an empty store: "no keys" would
// read as "nothing to revoke" when the real database is elsewhere.
func TestAPIKeysMissingDatabaseIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.db")

	code, _, errOut := runAPIKeys(t, "list", "--file", missing)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "no API key database") {
		t.Errorf("stderr = %q, want a missing-database explanation", errOut)
	}
	if _, err := auth.OpenExistingAPIKeyStore(missing); err == nil {
		t.Error("the CLI created the database it was asked to read")
	}
}

func TestAPIKeysRevokeByID(t *testing.T) {
	path, ids := seedKeyStore(t, [2]string{"alice", "mcp"}, [2]string{"alice", "ci"})

	code, out, errOut := runAPIKeys(t, "revoke", "--file", path, "--id", ids[0], "--yes")
	if code != 0 {
		t.Fatalf("revoke: code = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, ids[0]) {
		t.Errorf("output = %q, want the revoked id", out)
	}

	_, listOut, _ := runAPIKeys(t, "list", "--file", path)
	if strings.Contains(listOut, ids[0]) {
		t.Error("revoked key still listed")
	}
	if !strings.Contains(listOut, ids[1]) {
		t.Error("revoke --id removed a sibling key")
	}
}

// Offboarding: every key of one user goes, everyone else's stays.
func TestAPIKeysRevokeByUser(t *testing.T) {
	path, ids := seedKeyStore(t,
		[2]string{"dana", "mcp"}, [2]string{"dana", "ci"}, [2]string{"bob", "mcp"})

	code, out, errOut := runAPIKeys(t, "revoke", "--file", path, "--user", "dana", "--yes")
	if code != 0 {
		t.Fatalf("revoke --user: code = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "2 API keys") {
		t.Errorf("output = %q, want a count of 2", out)
	}

	_, listOut, _ := runAPIKeys(t, "list", "--file", path)
	for _, id := range ids[:2] {
		if strings.Contains(listOut, id) {
			t.Errorf("dana's key %s survived", id)
		}
	}
	if !strings.Contains(listOut, ids[2]) {
		t.Error("revoke --user removed another user's key")
	}
}

func TestAPIKeysRevokeAll(t *testing.T) {
	path, _ := seedKeyStore(t, [2]string{"alice", "mcp"}, [2]string{"bob", "ci"})

	code, out, errOut := runAPIKeys(t, "revoke", "--file", path, "--all", "--yes")
	if code != 0 {
		t.Fatalf("revoke --all: code = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "2 API keys") {
		t.Errorf("output = %q, want a count of 2", out)
	}

	_, listOut, _ := runAPIKeys(t, "list", "--file", path)
	if !strings.Contains(listOut, "No API keys") {
		t.Errorf("store not empty after --all:\n%s", listOut)
	}
}

// Selector discipline: guessing between --all and a narrower flag could revoke
// the whole database when one key was meant.
func TestAPIKeysRevokeSelectorValidation(t *testing.T) {
	path, ids := seedKeyStore(t, [2]string{"alice", "mcp"})

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no selector", []string{"revoke", "--file", path, "--yes"}, "is required"},
		{"user and all", []string{"revoke", "--file", path, "--user", "alice", "--all", "--yes"}, "mutually exclusive"},
		{"id and all", []string{"revoke", "--file", path, "--id", ids[0], "--all", "--yes"}, "mutually exclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := runAPIKeys(t, tc.args...)
			if code != 2 {
				t.Errorf("code = %d, want 2", code)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Errorf("stderr = %q, want %q", errOut, tc.want)
			}
		})
	}

	// Nothing was revoked by any rejected invocation.
	_, listOut, _ := runAPIKeys(t, "list", "--file", path)
	if !strings.Contains(listOut, ids[0]) {
		t.Error("a rejected revoke still deleted a key")
	}
}

// kubectl exec without -it gives no terminal; an unattended --all must not be
// confirmable by an empty read.
func TestAPIKeysRevokeRefusesWithoutTTYOrYes(t *testing.T) {
	path, ids := seedKeyStore(t, [2]string{"alice", "mcp"})

	code, _, errOut := runAPIKeys(t, "revoke", "--file", path, "--all")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "--yes") {
		t.Errorf("stderr = %q, want guidance to pass --yes", errOut)
	}

	_, listOut, _ := runAPIKeys(t, "list", "--file", path)
	if !strings.Contains(listOut, ids[0]) {
		t.Error("unconfirmed revoke deleted a key")
	}
}

func TestAPIKeysRevokeUnknownIDReportsFailure(t *testing.T) {
	path, _ := seedKeyStore(t, [2]string{"alice", "mcp"})

	code, _, errOut := runAPIKeys(t, "revoke", "--file", path, "--id", "deadbeef", "--yes")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "no key with id") {
		t.Errorf("stderr = %q, want a not-found message", errOut)
	}
}

// Revoking a user with no keys is a no-op, not an error — offboarding scripts
// run it for everyone who leaves.
func TestAPIKeysRevokeUserWithNoKeys(t *testing.T) {
	path, ids := seedKeyStore(t, [2]string{"alice", "mcp"})

	code, out, _ := runAPIKeys(t, "revoke", "--file", path, "--user", "nobody", "--yes")
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if !strings.Contains(out, "No API keys for") {
		t.Errorf("output = %q", out)
	}

	_, listOut, _ := runAPIKeys(t, "list", "--file", path)
	if !strings.Contains(listOut, ids[0]) {
		t.Error("revoking an unknown user touched another key")
	}
}

func TestAPIKeysUnknownSubcommand(t *testing.T) {
	code, _, errOut := runAPIKeys(t, "rotate")
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown subcommand") {
		t.Errorf("stderr = %q", errOut)
	}
}

// Under Cloud the Hub owns identity and the auth middleware ignores keys on
// tunneled requests. Opening a store there would register routes that mint
// credentials which can never authenticate, so it must stay closed — and no
// database file may be created on the pod's ephemeral volume.
func TestOpenAPIKeyStore_DisabledUnderCloudMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "api-keys.db")
	t.Setenv("RADAR_CLOUD_MODE", "true")

	// Cloud forces --auth-mode=proxy, which is what main() passes here.
	if store := openAPIKeyStore("proxy", dbPath); store != nil {
		store.Close()
		t.Error("a key store was opened under Radar Cloud")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Error("a key database was created under Radar Cloud")
	}
}

func TestOpenAPIKeyStore_EnabledOutsideCloudMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "api-keys.db")
	t.Setenv("RADAR_CLOUD_MODE", "false")

	store := openAPIKeyStore("proxy", dbPath)
	if store == nil {
		t.Fatal("no key store opened for --auth-mode=proxy outside Cloud")
	}
	defer store.Close()
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("key database not created: %v", err)
	}
}

func TestOpenAPIKeyStore_DisabledWithoutAuth(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "api-keys.db")
	t.Setenv("RADAR_CLOUD_MODE", "false")

	if store := openAPIKeyStore("none", dbPath); store != nil {
		store.Close()
		t.Error("a key store was opened with --auth-mode=none")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Error("a key database was created with --auth-mode=none")
	}
}
