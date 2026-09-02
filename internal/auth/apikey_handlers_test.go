package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func newAPIKeyRouter(t *testing.T, mode string) (chi.Router, *SQLiteAPIKeyStore) {
	t.Helper()
	store, _ := newTestAPIKeyStore(t)
	h := NewAPIKeyHandler(Config{Mode: mode, APIKeys: store})

	r := chi.NewRouter()
	r.Get("/api/auth/api-keys", h.HandleList)
	r.Post("/api/auth/api-keys", h.HandleCreate)
	r.Delete("/api/auth/api-keys/{id}", h.HandleDelete)
	return r, store
}

// asUser injects an authenticated identity the way the auth middleware would.
func asUser(req *http.Request, username string, groups ...string) *http.Request {
	return req.WithContext(ContextWithUser(req.Context(), &User{Username: username, Groups: groups}))
}

func TestAPIKeyHandler_CreateReturnsPlaintextOnce(t *testing.T) {
	r, store := newAPIKeyRouter(t, "oidc")

	req := asUser(httptest.NewRequest("POST", "/api/auth/api-keys", strings.NewReader(`{"description":"MCP tool"}`)), "alice", "devs")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID          string   `json:"id"`
		Key         string   `json:"key"`
		Description string   `json:"description"`
		Username    string   `json:"username"`
		Groups      []string `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(created.Key, pkgauth.APIKeyPrefix) {
		t.Errorf("key = %q, want prefix %q", created.Key, pkgauth.APIKeyPrefix)
	}
	if created.Username != "alice" {
		t.Errorf("username = %q, want alice", created.Username)
	}
	if len(created.Groups) != 1 || created.Groups[0] != "devs" {
		t.Errorf("groups = %v, want [devs]", created.Groups)
	}

	// The plaintext is never retrievable again.
	listReq := asUser(httptest.NewRequest("GET", "/api/auth/api-keys", nil), "alice", "devs")
	listRec := httptest.NewRecorder()
	r.ServeHTTP(listRec, listReq)
	if strings.Contains(listRec.Body.String(), created.Key) {
		t.Error("list response leaked the plaintext key")
	}
	if strings.Contains(strings.ToLower(listRec.Body.String()), "hash") {
		t.Error("list response leaked the key hash")
	}

	if got, err := store.Lookup(created.Key); err != nil || got == nil {
		t.Fatalf("created key does not resolve: %v (key=%v)", err, got)
	}
}

func TestAPIKeyHandler_CreateRefusedWhenAuthModeNone(t *testing.T) {
	r, store := newAPIKeyRouter(t, "none")

	req := asUser(httptest.NewRequest("POST", "/api/auth/api-keys", strings.NewReader(`{"description":"ci"}`)), "alice")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	keys, err := store.ListForUser("alice")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("a key was stored despite auth-mode=none: %+v", keys)
	}
}

func TestAPIKeyHandler_CreateRequiresAuthenticatedUser(t *testing.T) {
	r, _ := newAPIKeyRouter(t, "oidc")

	req := httptest.NewRequest("POST", "/api/auth/api-keys", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// A leaked key must not be able to mint replacements that survive its own
// revocation — minting requires an interactive session identity.
func TestAPIKeyHandler_CreateRefusedWhenAuthenticatedByAPIKey(t *testing.T) {
	r, store := newAPIKeyRouter(t, "oidc")

	req := httptest.NewRequest("POST", "/api/auth/api-keys", strings.NewReader(`{"description":"chained"}`))
	req = asUser(req, "alice", "devs")
	req = req.WithContext(contextWithAPIKeyAuth(req.Context()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	keys, err := store.ListForUser("alice")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("a key was minted by an API-key-authenticated request: %+v", keys)
	}
}

// A leaked key must not be able to enumerate its owner's remaining keys, nor
// revoke them — which would break their other automations and destroy the
// credential they would have rotated with.
func TestAPIKeyHandler_ListAndDeleteRefusedWhenAuthenticatedByAPIKey(t *testing.T) {
	r, store := newAPIKeyRouter(t, "oidc")

	victim, _, err := store.Create("alice", []string{"devs"}, "prod-deploy")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, tc := range []struct {
		name   string
		method string
		target string
	}{
		{"list", "GET", "/api/auth/api-keys"},
		{"delete", "DELETE", "/api/auth/api-keys/" + victim.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := asUser(httptest.NewRequest(tc.method, tc.target, nil), "alice", "devs")
			req = req.WithContext(contextWithAPIKeyAuth(req.Context()))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), victim.ID) {
				t.Errorf("the refusal leaked a key id: %s", rec.Body.String())
			}
		})
	}

	// The key survived both attempts.
	keys, err := store.ListForUser("alice")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("alice has %d keys, want 1 — an API-key-authenticated request revoked one", len(keys))
	}
}

// The browser manages keys with a session cookie, so the guard must not touch
// ordinary interactive traffic.
func TestAPIKeyHandler_ListAndDeleteAllowedForSessionAuth(t *testing.T) {
	r, store := newAPIKeyRouter(t, "oidc")

	key, _, err := store.Create("alice", []string{"devs"}, "mcp")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, asUser(httptest.NewRequest("GET", "/api/auth/api-keys", nil), "alice", "devs"))
	if rec.Code != http.StatusOK {
		t.Errorf("list: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, asUser(httptest.NewRequest("DELETE", "/api/auth/api-keys/"+key.ID, nil), "alice", "devs"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

func TestAPIKeyHandler_ListIsScopedToCaller(t *testing.T) {
	r, store := newAPIKeyRouter(t, "oidc")

	if _, _, err := store.Create("alice", []string{"devs"}, "alice key"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := store.Create("bob", []string{"devs"}, "bob key"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := asUser(httptest.NewRequest("GET", "/api/auth/api-keys", nil), "alice", "devs")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Keys []pkgauth.APIKey `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Keys) != 1 {
		t.Fatalf("got %d keys, want 1 (%s)", len(resp.Keys), rec.Body.String())
	}
	if resp.Keys[0].Username != "alice" {
		t.Errorf("username = %q, want alice", resp.Keys[0].Username)
	}
	if strings.Contains(rec.Body.String(), "bob") {
		t.Error("list leaked another user's key")
	}
}

func TestAPIKeyHandler_DeleteOwnKey(t *testing.T) {
	r, store := newAPIKeyRouter(t, "oidc")

	key, plaintext, err := store.Create("alice", []string{"devs"}, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := asUser(httptest.NewRequest("DELETE", "/api/auth/api-keys/"+key.ID, nil), "alice", "devs")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	got, err := store.Lookup(plaintext)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Error("revoked key still resolves")
	}
}

func TestAPIKeyHandler_DeleteAnotherUsersKeyIs404(t *testing.T) {
	r, store := newAPIKeyRouter(t, "oidc")

	key, plaintext, err := store.Create("alice", []string{"devs"}, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := asUser(httptest.NewRequest("DELETE", "/api/auth/api-keys/"+key.ID, nil), "mallory", "devs")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	// 404, not 403: the caller must not learn that the key exists.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	got, err := store.Lookup(plaintext)
	if err != nil || got == nil {
		t.Fatalf("victim's key stopped working: %v (key=%v)", err, got)
	}
}

func TestAPIKeyHandler_NoStoreConfigured(t *testing.T) {
	h := NewAPIKeyHandler(Config{Mode: "oidc"})
	r := chi.NewRouter()
	r.Get("/api/auth/api-keys", h.HandleList)

	req := asUser(httptest.NewRequest("GET", "/api/auth/api-keys", nil), "alice")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// A groupless user is common (proxy mode without a groups header, or an OIDC
// provider that emits no groups claim). Create and list must agree on the
// shape, or a client indexing `groups` breaks on one response and not the
// other.
func TestAPIKeyHandler_GroupsSerializeAsEmptyArrayNotNull(t *testing.T) {
	r, _ := newAPIKeyRouter(t, "oidc")

	req := asUser(httptest.NewRequest("POST", "/api/auth/api-keys", strings.NewReader(`{}`)), "alice")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	createBody := rec.Body.String()
	if strings.Contains(createBody, `"groups":null`) {
		t.Errorf("create returned a null groups field: %s", createBody)
	}
	if !strings.Contains(createBody, `"groups":[]`) {
		t.Errorf("create groups = %s, want []", createBody)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, asUser(httptest.NewRequest("GET", "/api/auth/api-keys", nil), "alice"))
	if listBody := rec.Body.String(); !strings.Contains(listBody, `"groups":[]`) {
		t.Errorf("list groups = %s, want [] to match create", listBody)
	}
}
