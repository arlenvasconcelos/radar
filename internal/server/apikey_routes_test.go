package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// newAPIKeyTestServer builds a proxy-auth server with an API key store, the
// way `--auth-mode=proxy --auth-api-keys-file=...` does.
func newAPIKeyTestServer(t *testing.T, devMode bool) *authTestEnv {
	t.Helper()
	store, err := auth.NewAPIKeyStore(filepath.Join(t.TempDir(), "api-keys.db"))
	if err != nil {
		t.Fatalf("NewAPIKeyStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(Config{
		DevMode: devMode,
		AuthConfig: auth.Config{
			Mode:         "proxy",
			Secret:       "test-secret",
			UserHeader:   "X-Forwarded-User",
			GroupsHeader: "X-Forwarded-Groups",
			APIKeys:      store,
		},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Stop()
	})
	return &authTestEnv{ts: ts, srv: srv}
}

func decodeBody(t *testing.T, resp *http.Response, into any) {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

// Full lifecycle over the real router: mint a key as a proxy-authenticated
// user, use it on an ordinary endpoint, revoke it, and confirm it stops working.
func TestAPIKeyRoutes_Lifecycle(t *testing.T) {
	env := newAPIKeyTestServer(t, true)

	resp := env.authPost(t, "/api/auth/api-keys", "alice", "devs,sre", `{"description":"MCP tool"}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create: status = %d, want 201: %s", resp.StatusCode, body)
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	decodeBody(t, resp, &created)
	if created.Key == "" || created.ID == "" {
		t.Fatalf("create returned id=%q key=%q", created.ID, created.Key)
	}

	// The key alone authenticates, resolving to the creator's identity.
	for _, header := range []struct{ name, value string }{
		{"Authorization", "Bearer " + created.Key},
		{"X-Api-Key", created.Key},
	} {
		req, _ := http.NewRequest("GET", env.ts.URL+"/api/auth/me", nil)
		req.Header.Set(header.name, header.value)
		meResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /api/auth/me: %v", err)
		}
		var me struct {
			Username string   `json:"username"`
			Groups   []string `json:"groups"`
		}
		decodeBody(t, meResp, &me)
		if me.Username != "alice" {
			t.Errorf("%s: username = %q, want alice", header.name, me.Username)
		}
		if len(me.Groups) != 2 || me.Groups[0] != "devs" || me.Groups[1] != "sre" {
			t.Errorf("%s: groups = %v, want [devs sre]", header.name, me.Groups)
		}
	}

	// The key is listed, without its plaintext.
	listReq, _ := http.NewRequest("GET", env.ts.URL+"/api/auth/api-keys", nil)
	listReq.Header.Set("X-Forwarded-User", "alice")
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("GET /api/auth/api-keys: %v", err)
	}
	listBody, _ := io.ReadAll(listResp.Body)
	listResp.Body.Close()
	if !strings.Contains(string(listBody), created.ID) {
		t.Errorf("list does not contain the created key: %s", listBody)
	}
	if strings.Contains(string(listBody), created.Key) {
		t.Error("list leaked the plaintext key")
	}

	// Revoke it.
	delReq, _ := http.NewRequest("DELETE", env.ts.URL+"/api/auth/api-keys/"+created.ID, nil)
	delReq.Header.Set("X-Forwarded-User", "alice")
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", delResp.StatusCode)
	}

	// The revoked key no longer authenticates.
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/topology", nil)
	req.Header.Set("Authorization", "Bearer "+created.Key)
	afterResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/topology: %v", err)
	}
	afterResp.Body.Close()
	if afterResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked key: status = %d, want 401", afterResp.StatusCode)
	}
}

// Mint and revoke are CSRF-guarded: the CORS allowlist plus SameSite=Lax make
// any localhost page a credentialed cross-origin caller, and the create
// response carries the plaintext key.
func TestAPIKeyRoutes_CrossOriginRefused(t *testing.T) {
	env := newAPIKeyTestServer(t, false)

	send := func(method, path, origin string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, env.ts.URL+path, strings.NewReader(`{"description":"x"}`))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("X-Forwarded-User", "alice")
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// A local page on another port is same-site to the browser and on the
	// CORS allowlist — the guard is all that stops it.
	resp := send("POST", "/api/auth/api-keys", "http://localhost:3000")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin create: status = %d, want 403", resp.StatusCode)
	}

	resp = send("DELETE", "/api/auth/api-keys/someid", "http://localhost:3000")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin delete: status = %d, want 403", resp.StatusCode)
	}

	// Radar's own UI presents the Origin it was served from.
	resp = send("POST", "/api/auth/api-keys", env.ts.URL)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("same-origin create: status = %d, want 201", resp.StatusCode)
	}

	// Non-browser clients (curl, CI) send no Origin at all.
	resp = send("POST", "/api/auth/api-keys", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("no-origin create: status = %d, want 201", resp.StatusCode)
	}

	// List is deliberately unguarded — metadata only, no plaintext.
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/auth/api-keys", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("Origin", "http://localhost:3000")
	listResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Errorf("cross-origin list: status = %d, want 200", listResp.StatusCode)
	}
}

// Under --dev the Vite proxy rewrites Host (changeOrigin: true), so the UI's
// loopback origin never string-matches; the loopback-pair allowance must hold
// there or key minting breaks in `make watch-frontend`.
func TestAPIKeyRoutes_DevModeAllowsLoopbackPairOrigin(t *testing.T) {
	env := newAPIKeyTestServer(t, true)

	req, err := http.NewRequest("POST", env.ts.URL+"/api/auth/api-keys", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("Origin", "http://localhost:9273")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("dev-mode loopback-pair create: status = %d, want 201", resp.StatusCode)
	}
}

func TestAPIKeyRoutes_NotRegisteredWithoutStore(t *testing.T) {
	env := newAuthTestServer(t)

	resp := env.authPost(t, "/api/auth/api-keys", "alice", "devs", `{}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404/405 when no API key store is configured", resp.StatusCode)
	}
}

// The frontend gates its settings section on this flag rather than probing for
// a 404 — without it, Radar Hub (which embeds this frontend) would render a
// dead credential page beside its own tokens.
func TestAuthMe_ReportsAPIKeyAvailability(t *testing.T) {
	withStore := newAPIKeyTestServer(t, true)
	resp := withStore.authGet(t, "/api/auth/me", "alice", "devs")
	var me struct {
		APIKeysEnabled bool `json:"apiKeysEnabled"`
	}
	decodeBody(t, resp, &me)
	if !me.APIKeysEnabled {
		t.Error("apiKeysEnabled = false with a key store configured")
	}

	withoutStore := newAuthTestServer(t)
	resp = withoutStore.authGet(t, "/api/auth/me", "alice", "devs")
	decodeBody(t, resp, &me)
	if me.APIKeysEnabled {
		t.Error("apiKeysEnabled = true without a key store")
	}
}
