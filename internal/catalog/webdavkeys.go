package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// WebDAVKey is a named, revocable credential a user mints for themselves to
// authenticate a WebDAV client (a phone emulator's "cloud drive" import,
// for instance) against the read-only /webdav/ catalog listing. Unlike an
// APIKey it carries only that one authority, scoped to the user who created
// it, never admin or general API access.
type WebDAVKey struct {
	ID         string
	UserID     string
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// CreateWebDAVKey mints a new key for the given user and returns the record
// plus the raw token. As with API keys and sessions, the token is stored as
// issued and returned here only once.
func (s *Store) CreateWebDAVKey(ctx context.Context, userID, label string) (WebDAVKey, string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return WebDAVKey{}, "", err
	}
	token := "rrd_" + hex.EncodeToString(buf)
	id := "rrd_" + hex.EncodeToString(buf[:8])
	now := time.Now()
	if _, err := s.execCtx(ctx, `INSERT INTO webdav_keys(id,user_id,label,token,created_at) VALUES(?,?,?,?,?)`,
		id, userID, label, token, now.Unix()); err != nil {
		return WebDAVKey{}, "", err
	}
	return WebDAVKey{ID: id, UserID: userID, Label: label, CreatedAt: now}, token, nil
}

// WebDAVKeysForUser lists every key belonging to a user, newest first,
// active and revoked, so they can see what a device is still using.
func (s *Store) WebDAVKeysForUser(ctx context.Context, userID string) ([]WebDAVKey, error) {
	rows, err := s.queryCtx(ctx, `SELECT id,user_id,label,created_at,last_used_at,revoked_at
FROM webdav_keys WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebDAVKey
	for rows.Next() {
		var k WebDAVKey
		var created int64
		var lastUsed, revoked *int64
		if err := rows.Scan(&k.ID, &k.UserID, &k.Label, &created, &lastUsed, &revoked); err != nil {
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

// RevokeWebDAVKey disables a key immediately, scoped to the user it belongs
// to so one user can never revoke another's key. The row is kept for the
// audit trail rather than deleted.
func (s *Store) RevokeWebDAVKey(ctx context.Context, userID, id string) error {
	_, err := s.execCtx(ctx, `UPDATE webdav_keys SET revoked_at=? WHERE id=? AND user_id=? AND revoked_at IS NULL`,
		time.Now().Unix(), id, userID)
	return err
}

// WebDAVKeyUser resolves a token to the user it was issued to, the identity
// a WebDAV-authenticated request acts as. It returns sql.ErrNoRows if the
// token is unknown, revoked, or its owner is disabled, and touches
// last_used_at on success.
func (s *Store) WebDAVKeyUser(ctx context.Context, token string) (User, error) {
	row := s.queryRowCtx(ctx, `SELECT u.id,u.username,u.password_hash,u.role,u.created_at,u.disabled,u.must_change_password
FROM webdav_keys k JOIN users u ON u.id=k.user_id
WHERE k.token=? AND k.revoked_at IS NULL AND u.disabled=FALSE`, token)
	u, err := s.scanUser(row)
	if err != nil {
		return User{}, err
	}
	_, _ = s.execCtx(ctx, `UPDATE webdav_keys SET last_used_at=? WHERE token=?`, time.Now().Unix(), token)
	return u, nil
}
