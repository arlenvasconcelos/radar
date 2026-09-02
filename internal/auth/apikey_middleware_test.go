package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// wellFormedKey has the exact shape of a minted key but was never stored, so
// it reaches the store lookup — which malformed values deliberately do not.
const wellFormedKey = pkgauth.APIKeyPrefix + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func apiKeyConfig(t *testing.T) (Config, *SQLiteAPIKeyStore) {
	t.Helper()
	store, _ := newTestAPIKeyStore(t)
	return Config{
		Mode:         "oidc",
		Secret:       "test-secret",
		CookieTTL:    1 * time.Hour,
		UserHeader:   "X-Forwarded-User",
		GroupsHeader: "X-Forwarded-Groups",
		APIKeys:      store,
	}, store
}

func decodeUser(t *testing.T, rec *httptest.ResponseRecorder) User {
	t.Helper()
	var u User
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode user: %v (body %s)", err, rec.Body.String())
	}
	return u
}

func TestMiddleware_APIKeyBearerHeader(t *testing.T) {
	cfg, store := apiKeyConfig(t)
	_, plaintext, err := store.Create("alice", []string{"devs", "sre"}, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	handler := Authenticate(cfg)(http.HandlerFunc(echoUser))
	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	user := decodeUser(t, rec)
	if user.Username != "alice" {
		t.Errorf("username = %q, want alice", user.Username)
	}
	if len(user.Groups) != 2 || user.Groups[0] != "devs" || user.Groups[1] != "sre" {
		t.Errorf("groups = %v, want [devs sre]", user.Groups)
	}
	// Headless clients must not be handed a session cookie.
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("API key auth set %d cookies, want 0", len(cookies))
	}
}

func TestMiddleware_APIKeyXApiKeyHeader(t *testing.T) {
	cfg, store := apiKeyConfig(t)
	_, plaintext, err := store.Create("bob", []string{"ops"}, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	handler := Authenticate(cfg)(http.HandlerFunc(echoUser))
	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	req.Header.Set("X-Api-Key", plaintext)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if user := decodeUser(t, rec); user.Username != "bob" {
		t.Errorf("username = %q, want bob", user.Username)
	}
}

func TestMiddleware_APIKeyMarksRequestAsKeyAuthenticated(t *testing.T) {
	cfg, store := apiKeyConfig(t)
	_, plaintext, err := store.Create("alice", nil, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var keyAuthed bool
	handler := Authenticate(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyAuthed = isAPIKeyAuthenticated(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !keyAuthed {
		t.Error("API-key-authenticated request was not marked as such")
	}
}

func TestMiddleware_APIKeyRejected(t *testing.T) {
	cfg, store := apiKeyConfig(t)
	key, plaintext, err := store.Create("alice", []string{"devs"}, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Delete(key.ID, "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	handler := Authenticate(cfg)(http.HandlerFunc(echoUser))

	cases := []struct {
		name   string
		header string
		value  string
	}{
		{"revoked key", "Authorization", "Bearer " + plaintext},
		{"revoked key via X-Api-Key", "X-Api-Key", plaintext},
		{"unknown but well-formed key", "Authorization", "Bearer " + wellFormedKey},
		{"too short for a key", "Authorization", "Bearer rk_deadbeef"},
		{"right length, not hex", "Authorization", "Bearer " + pkgauth.APIKeyPrefix + strings.Repeat("z", 64)},
		{"missing the rk_ prefix", "Authorization", "Bearer " + strings.Repeat("a", 67)},
		{"an OIDC access token", "Authorization", "Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.e30.sig"},
		{"empty bearer", "Authorization", "Bearer "},
		{"non-bearer scheme", "Authorization", "Basic dXNlcjpwYXNz"},
		{"empty api key header", "X-Api-Key", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/resources/pods", nil)
			req.Header.Set(tc.header, tc.value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// A valid session cookie stays authoritative; the API key branch must not
// change identity for browser traffic.
func TestMiddleware_SessionCookieWinsOverAPIKey(t *testing.T) {
	cfg, store := apiKeyConfig(t)
	_, plaintext, err := store.Create("alice", []string{"devs"}, "ci")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	handler := Authenticate(cfg)(http.HandlerFunc(echoUser))
	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	for _, c := range CreateSessionCookie(&User{Username: "bob", Groups: []string{"ops"}}, NewSessionID(), "", cfg.Secret, cfg.CookieTTL, false) {
		req.AddCookie(c)
	}
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if user := decodeUser(t, rec); user.Username != "bob" {
		t.Errorf("username = %q, want bob (session cookie must win)", user.Username)
	}
}

func TestMiddleware_NoAPIKeyStoreConfigured(t *testing.T) {
	cfg := proxyConfig()
	handler := Authenticate(cfg)(http.HandlerFunc(echoUser))

	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	req.Header.Set("Authorization", "Bearer "+wellFormedKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// erroringAPIKeyStore simulates a store outage: a locked or corrupt database,
// a full disk, an exhausted connection pool.
type erroringAPIKeyStore struct{ err error }

func (s *erroringAPIKeyStore) Create(string, []string, string) (*pkgauth.APIKey, string, error) {
	return nil, "", s.err
}
func (s *erroringAPIKeyStore) Lookup(string) (*pkgauth.APIKey, error)       { return nil, s.err }
func (s *erroringAPIKeyStore) ListForUser(string) ([]pkgauth.APIKey, error) { return nil, s.err }
func (s *erroringAPIKeyStore) Delete(string, string) (bool, error)          { return false, s.err }

// A store outage must fail closed. Falling through would 401 in oidc mode —
// indistinguishable from revocation — so the operator re-mints keys instead of
// fixing the database.
func TestMiddleware_APIKeyLookupErrorFailsClosed(t *testing.T) {
	cfg, _ := apiKeyConfig(t)
	cfg.APIKeys = &erroringAPIKeyStore{err: errors.New("database is locked")}

	handler := Authenticate(cfg)(http.HandlerFunc(echoUser))
	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	req.Header.Set("Authorization", "Bearer "+wellFormedKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unavailable") {
		t.Errorf("body = %s, want an availability message distinct from 401", rec.Body.String())
	}
}

// The proxy-header block sits after the key branch, so a fall-through on error
// would silently serve the request as the header identity instead of the key's
// — a store outage must never swap who the request runs as.
func TestMiddleware_APIKeyLookupErrorDoesNotFallBackToProxyIdentity(t *testing.T) {
	cfg, _ := apiKeyConfig(t)
	cfg.Mode = "proxy"
	cfg.APIKeys = &erroringAPIKeyStore{err: errors.New("database is locked")}

	handler := Authenticate(cfg)(http.HandlerFunc(echoUser))
	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	req.Header.Set("Authorization", "Bearer "+wellFormedKey)
	req.Header.Set("X-Forwarded-User", "someone-else")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "someone-else") {
		t.Error("a store outage served the request as the proxy-header identity")
	}
}

// A request carrying no key at all is unaffected by an unhealthy store: the
// outage must not take down cookie and proxy-header traffic.
func TestMiddleware_StoreOutageDoesNotAffectRequestsWithoutAKey(t *testing.T) {
	cfg, _ := apiKeyConfig(t)
	cfg.Mode = "proxy"
	cfg.APIKeys = &erroringAPIKeyStore{err: errors.New("database is locked")}

	handler := Authenticate(cfg)(http.HandlerFunc(echoUser))
	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if user := decodeUser(t, rec); user.Username != "alice" {
		t.Errorf("username = %q, want alice", user.Username)
	}
}

// The credential path must not be entered by tokens that aren't Radar keys.
// An OIDC access token on the Authorization header would otherwise be hashed
// and probed against the key database on every request, and every scanner
// would write a rejection line to the log.
func TestMiddleware_NonRadarBearerNeverReachesTheStore(t *testing.T) {
	cfg, _ := apiKeyConfig(t)
	// Any store hit fails the test loudly: an error would surface as 503.
	cfg.APIKeys = &erroringAPIKeyStore{err: errors.New("store must not be consulted")}

	handler := Authenticate(cfg)(http.HandlerFunc(echoUser))
	for _, tc := range []struct{ name, header, value string }{
		{"OIDC access token", "Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig"},
		{"opaque token", "Authorization", "Bearer " + strings.Repeat("a", 300)},
		{"malformed X-Api-Key", "X-Api-Key", "not-a-radar-key"},
		{"prefix but wrong length", "Authorization", "Bearer " + pkgauth.APIKeyPrefix + "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/resources/pods", nil)
			req.Header.Set(tc.header, tc.value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code == http.StatusServiceUnavailable {
				t.Fatalf("the store was consulted for a non-Radar credential: %s", rec.Body.String())
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}
