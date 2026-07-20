package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// APIKey is a named, revocable credential for programmatic access to the
// internal API, external to a browser login session. It carries the same
// authority as the admin account that created it, so revoking one never
// touches that account, and disabling or deleting the account revokes every
// key it created.
type APIKey struct {
	ID                string
	Name              string
	CreatedBy         string
	CreatedByUsername string
	CreatedAt         time.Time
	LastUsedAt        *time.Time
	RevokedAt         *time.Time
}

// CreateAPIKey mints a new key for the given admin and returns the record
// plus the raw token. The token is stored as issued (the same convention as
// session tokens) and is returned here only once: the caller must show it to
// the admin now, since it cannot be recovered afterward.
func (s *Store) CreateAPIKey(ctx context.Context, name, createdBy string) (APIKey, string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return APIKey{}, "", err
	}
	token := "rr_" + hex.EncodeToString(buf)
	id := "rr_" + hex.EncodeToString(buf[:8])
	now := time.Now()
	if _, err := s.execCtx(ctx, `INSERT INTO api_keys(id,name,token,created_by,created_at) VALUES(?,?,?,?,?)`,
		id, name, token, createdBy, now.Unix()); err != nil {
		return APIKey{}, "", err
	}
	return APIKey{ID: id, Name: name, CreatedBy: createdBy, CreatedAt: now}, token, nil
}

// APIKeys lists every key, newest first, with the creating admin's username
// for display.
func (s *Store) APIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.queryCtx(ctx, `SELECT k.id,k.name,k.created_by,u.username,k.created_at,k.last_used_at,k.revoked_at
FROM api_keys k JOIN users u ON u.id=k.created_by ORDER BY k.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var created int64
		var lastUsed, revoked *int64
		if err := rows.Scan(&k.ID, &k.Name, &k.CreatedBy, &k.CreatedByUsername, &created, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		k.CreatedAt = time.Unix(created, 0)
		if lastUsed != nil {
			t := time.Unix(*lastUsed, 0)
			k.LastUsedAt = &t
		}
		if revoked != nil {
			t := time.Unix(*revoked, 0)
			k.RevokedAt = &t
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeAPIKey disables a key immediately. The row is kept for the audit
// trail rather than deleted.
func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	_, err := s.execCtx(ctx, `UPDATE api_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, time.Now().Unix(), id)
	return err
}

// APIKeyUser resolves a bearer token to the admin account that created it,
// the identity an API-key-authenticated request acts as. It returns
// sql.ErrNoRows if the token is unknown, revoked, or its creator is disabled
// or no longer an admin, and touches last_used_at on success for the Admin
// suite's key list.
func (s *Store) APIKeyUser(ctx context.Context, token string) (User, error) {
	row := s.queryRowCtx(ctx, `SELECT u.id,u.username,u.password_hash,u.role,u.created_at,u.disabled,u.must_change_password
FROM api_keys k JOIN users u ON u.id=k.created_by
WHERE k.token=? AND k.revoked_at IS NULL AND u.disabled=FALSE AND u.role='admin'`, token)
	u, err := s.scanUser(row)
	if err != nil {
		return User{}, err
	}
	_, _ = s.execCtx(ctx, `UPDATE api_keys SET last_used_at=? WHERE token=?`, time.Now().Unix(), token)
	return u, nil
}
