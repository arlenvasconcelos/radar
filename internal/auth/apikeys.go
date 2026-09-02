package auth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// SQLiteAPIKeyStore persists API keys in a SQLite database, storing only
// hex(sha256(plaintext)). The database holds credentials for every user, so
// the file is owner-only.
type SQLiteAPIKeyStore struct {
	db   *sql.DB
	path string
}

var _ pkgauth.APIKeyStore = (*SQLiteAPIKeyStore)(nil)

// NewAPIKeyStore opens (creating if needed) the API key database at path.
func NewAPIKeyStore(path string) (*SQLiteAPIKeyStore, error) {
	if path == "" {
		return nil, errors.New("api key store path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create api key directory: %w", err)
		}
	}
	// Create the file with owner-only permissions before SQLite does, so the
	// key hashes are never briefly world-readable. An existing file is
	// tightened to the same mode.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create api key database: %w", err)
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure api key database: %w", err)
	}
	return openAPIKeyDB(path)
}

// OpenExistingAPIKeyStore opens the database at path but never creates it.
// The admin CLI runs alongside a server that owns the real store: conjuring an
// empty database from a mistyped path would answer "no keys" to a revocation
// request, which reads as "nothing to revoke" rather than "wrong file".
// Returns an error satisfying errors.Is(err, fs.ErrNotExist) when absent.
func OpenExistingAPIKeyStore(path string) (*SQLiteAPIKeyStore, error) {
	if path == "" {
		return nil, errors.New("api key store path is empty")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return openAPIKeyDB(path)
}

func openAPIKeyDB(path string) (*SQLiteAPIKeyStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open api key database: %w", err)
	}
	// SQLite takes one writer at a time.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &SQLiteAPIKeyStore{db: db, path: path}
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteAPIKeyStore) initSchema() error {
	stmts := []string{
		// WAL lets the admin CLI read and revoke while the server holds the
		// database; busy_timeout absorbs the write overlap. Without these a
		// second process fails immediately with SQLITE_BUSY, which the auth
		// path would surface as a spurious 401 on a valid key.
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=10000",
		"PRAGMA synchronous=NORMAL",
		`CREATE TABLE IF NOT EXISTS api_keys (
			id          TEXT PRIMARY KEY,
			hash        TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			username    TEXT NOT NULL,
			groups      TEXT NOT NULL DEFAULT '[]',
			created_at  TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_username ON api_keys(username)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("initialize api key schema: %w", err)
		}
	}
	return nil
}

// Path returns the database file path.
func (s *SQLiteAPIKeyStore) Path() string { return s.path }

// Close releases the database handle.
func (s *SQLiteAPIKeyStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Create mints a key bound to username+groups and returns the plaintext, which
// is not recoverable afterwards.
func (s *SQLiteAPIKeyStore) Create(username string, groups []string, description string) (*pkgauth.APIKey, string, error) {
	if strings.TrimSpace(username) == "" {
		return nil, "", errors.New("api key requires a username")
	}
	id, plaintext, err := pkgauth.GenerateAPIKey()
	if err != nil {
		return nil, "", err
	}
	// JSON, not a delimiter-joined string: an OIDC group name may contain any
	// character, and a lossy round-trip would grant memberships the creator
	// never had.
	encodedGroups, err := json.Marshal(nonNilGroups(groups))
	if err != nil {
		return nil, "", fmt.Errorf("encode api key groups: %w", err)
	}
	createdAt := time.Now().UTC().Truncate(time.Second)

	_, err = s.db.Exec(
		`INSERT INTO api_keys (id, hash, description, username, groups, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, pkgauth.HashAPIKey(plaintext), description, username, string(encodedGroups), createdAt.Format(time.RFC3339),
	)
	if err != nil {
		return nil, "", fmt.Errorf("store api key: %w", err)
	}

	return &pkgauth.APIKey{
		ID:          id,
		Description: description,
		Username:    username,
		// nonNilGroups, matching what the row will read back as: a groupless
		// user must not get "groups": null from create and "groups": [] from
		// list, or a client indexing the field breaks on one and not the other.
		Groups:    nonNilGroups(groups),
		CreatedAt: createdAt,
	}, plaintext, nil
}

// Lookup resolves a presented plaintext key to the identity it was minted for.
// Returns (nil, nil) when the key is unknown or revoked.
func (s *SQLiteAPIKeyStore) Lookup(plaintext string) (*pkgauth.APIKey, error) {
	if plaintext == "" || len(plaintext) > pkgauth.MaxAPIKeyLength {
		return nil, nil
	}
	// Indexed probe on the hash — never a scan over every stored key. The
	// hash is of a value the caller already knows, so there is no secret to
	// leak through comparison timing.
	row := s.db.QueryRow(
		`SELECT id, description, username, groups, created_at FROM api_keys WHERE hash = ?`,
		pkgauth.HashAPIKey(plaintext),
	)
	key, err := scanAPIKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return key, nil
}

// ListForUser returns the keys owned by username, newest first.
func (s *SQLiteAPIKeyStore) ListForUser(username string) ([]pkgauth.APIKey, error) {
	rows, err := s.db.Query(
		`SELECT id, description, username, groups, created_at FROM api_keys WHERE username = ? ORDER BY created_at DESC, id`,
		username,
	)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	keys := []pkgauth.APIKey{}
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, *key)
	}
	return keys, rows.Err()
}

// Delete revokes id when it belongs to username. The username predicate is in
// the statement itself, so a caller can never revoke someone else's key.
func (s *SQLiteAPIKeyStore) Delete(id, username string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ? AND username = ?`, id, username)
	if err != nil {
		return false, fmt.Errorf("delete api key: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete api key: %w", err)
	}
	return affected > 0, nil
}

// The methods below back `radar api-keys` and are deliberately NOT on the
// pkgauth.APIKeyStore interface: nothing reachable over HTTP may enumerate or
// revoke across owners. Their authorization is filesystem access to the
// database — see cmd/explorer/apikeys_cmd.go.

// ListAll returns every key in the store, newest first.
func (s *SQLiteAPIKeyStore) ListAll() ([]pkgauth.APIKey, error) {
	rows, err := s.db.Query(
		`SELECT id, description, username, groups, created_at FROM api_keys ORDER BY username, created_at DESC, id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	keys := []pkgauth.APIKey{}
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, *key)
	}
	return keys, rows.Err()
}

// DeleteByID revokes a key whoever owns it. Unlike Delete, this is how an
// operator revokes a departed user's key: the HTTP routes are owner-scoped, so
// once the IdP account is gone nobody can reach them for that user.
func (s *SQLiteAPIKeyStore) DeleteByID(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete api key: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete api key: %w", err)
	}
	return affected > 0, nil
}

// DeleteAllForUser revokes every key owned by username and reports how many.
func (s *SQLiteAPIKeyStore) DeleteAllForUser(username string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE username = ?`, username)
	if err != nil {
		return 0, fmt.Errorf("delete api keys for %q: %w", username, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete api keys for %q: %w", username, err)
	}
	return affected, nil
}

// DeleteAll revokes every key in the store and reports how many.
func (s *SQLiteAPIKeyStore) DeleteAll() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM api_keys`)
	if err != nil {
		return 0, fmt.Errorf("delete all api keys: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete all api keys: %w", err)
	}
	return affected, nil
}

// rowScanner covers both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAPIKey(row rowScanner) (*pkgauth.APIKey, error) {
	var (
		key           pkgauth.APIKey
		encodedGroups string
		createdAt     string
	)
	if err := row.Scan(&key.ID, &key.Description, &key.Username, &encodedGroups, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read api key: %w", err)
	}
	if encodedGroups != "" {
		if err := json.Unmarshal([]byte(encodedGroups), &key.Groups); err != nil {
			return nil, fmt.Errorf("decode api key groups: %w", err)
		}
	}
	if ts, err := time.Parse(time.RFC3339, createdAt); err == nil {
		key.CreatedAt = ts
	}
	return &key, nil
}

func nonNilGroups(groups []string) []string {
	if groups == nil {
		return []string{}
	}
	return groups
}
