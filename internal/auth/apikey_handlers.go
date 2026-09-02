package auth

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// maxAPIKeyDescription bounds what a caller can store as a key label.
const maxAPIKeyDescription = 256

// APIKeyHandler serves the per-user API key endpoints. Every operation is
// scoped to the calling identity: a user sees, mints, and revokes only their
// own keys.
type APIKeyHandler struct {
	store pkgauth.APIKeyStore
	mode  string
}

// NewAPIKeyHandler builds the handler from the auth configuration.
func NewAPIKeyHandler(cfg Config) *APIKeyHandler {
	return &APIKeyHandler{store: cfg.APIKeys, mode: cfg.Mode}
}

type createAPIKeyRequest struct {
	Description string `json:"description"`
}

// createAPIKeyResponse carries the plaintext key. It is the only response that
// ever does — the key cannot be retrieved again.
type createAPIKeyResponse struct {
	pkgauth.APIKey
	Key string `json:"key"`
}

// HandleList returns the caller's keys. No plaintext, no hash.
func (h *APIKeyHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if !h.requireInteractiveSession(w, r, "be listed") {
		return
	}
	if !h.requireStore(w) {
		return
	}

	keys, err := h.store.ListForUser(user.Username)
	if err != nil {
		log.Printf("[auth] Failed to list API keys for %q: %v", user.Username, err)
		writeAPIKeyError(w, http.StatusInternalServerError, "failed to list API keys")
		return
	}
	writeAPIKeyJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// HandleCreate mints a key bound to the caller's username and groups.
func (h *APIKeyHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	// With auth disabled there is no stable identity to bind a key to, and a
	// key would grant more than the (unauthenticated) caller already has.
	if h.mode == "" || h.mode == "none" {
		writeAPIKeyError(w, http.StatusForbidden, "API keys require --auth-mode=proxy or --auth-mode=oidc")
		return
	}
	if !h.requireInteractiveSession(w, r, "be created") {
		return
	}
	if !h.requireStore(w) {
		return
	}

	var req createAPIKeyRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeAPIKeyError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeAPIKeyError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	description := strings.TrimSpace(req.Description)
	if len(description) > maxAPIKeyDescription {
		description = description[:maxAPIKeyDescription]
	}

	key, plaintext, err := h.store.Create(user.Username, user.Groups, description)
	if err != nil {
		log.Printf("[auth] Failed to create API key for %q: %v", user.Username, err)
		writeAPIKeyError(w, http.StatusInternalServerError, "failed to create API key")
		return
	}
	log.Printf("[audit] user=%q groups=%q action=%q id=%q", user.Username, user.Groups, "create-api-key", key.ID)
	writeAPIKeyJSON(w, http.StatusCreated, createAPIKeyResponse{APIKey: *key, Key: plaintext})
}

// HandleDelete revokes one of the caller's keys.
func (h *APIKeyHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if !h.requireInteractiveSession(w, r, "be revoked") {
		return
	}
	if !h.requireStore(w) {
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		writeAPIKeyError(w, http.StatusBadRequest, "missing key id")
		return
	}

	deleted, err := h.store.Delete(id, user.Username)
	if err != nil {
		log.Printf("[auth] Failed to delete API key %q for %q: %v", id, user.Username, err)
		writeAPIKeyError(w, http.StatusInternalServerError, "failed to delete API key")
		return
	}
	if !deleted {
		// 404 rather than 403 for someone else's key: the caller must not
		// learn that a key id exists.
		writeAPIKeyError(w, http.StatusNotFound, "API key not found")
		return
	}
	log.Printf("[audit] user=%q groups=%q action=%q id=%q", user.Username, user.Groups, "delete-api-key", id)
	w.WriteHeader(http.StatusNoContent)
}

func (h *APIKeyHandler) requireUser(w http.ResponseWriter, r *http.Request) (*User, bool) {
	user := UserFromContext(r.Context())
	if user == nil || user.Username == "" {
		writeAPIKeyError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return user, true
}

// requireInteractiveSession refuses requests whose identity came from an API
// key. The whole key-management plane needs a session (cookie or proxy
// identity), not a key: a leaked key must not be able to mint replacements
// that outlive its revocation, nor enumerate its owner's remaining keys and
// revoke them — which would break every one of their automations and destroy
// the credential they would have used to rotate, leaving the stolen key as the
// only one still working.
//
// No legitimate client is affected: keys are for headless clients doing
// cluster work, and the browser manages keys with its session cookie.
//
// `action` completes "API keys cannot ___".
func (h *APIKeyHandler) requireInteractiveSession(w http.ResponseWriter, r *http.Request, action string) bool {
	if !isAPIKeyAuthenticated(r.Context()) {
		return true
	}
	writeAPIKeyError(w, http.StatusForbidden,
		"API keys cannot "+action+" from an API-key-authenticated request; sign in first")
	return false
}

func (h *APIKeyHandler) requireStore(w http.ResponseWriter) bool {
	if h.store == nil {
		writeAPIKeyError(w, http.StatusServiceUnavailable, "API key store is not configured")
		return false
	}
	return true
}

func writeAPIKeyJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("[auth] Failed to write API key response: %v", err)
	}
}

func writeAPIKeyError(w http.ResponseWriter, status int, msg string) {
	writeAPIKeyJSON(w, status, map[string]string{"error": msg})
}
