package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// User is a ROM Relay account. PasswordHash is a bcrypt hash, never a plaintext
// password.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
	// Disabled blocks sign-in while keeping every row the account owns.
	Disabled bool
	// MustChangePassword marks a password the admin generated: the user is held
	// at the change-password page until they set their own.
	MustChangePassword bool
}

// UserByUsername looks up a user by username. It returns sql.ErrNoRows when no
// such user exists. Username is passed as a bound parameter, so it cannot be used
// for SQL injection.
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	return s.scanUser(s.queryRowCtx(ctx, `SELECT id,username,password_hash,role,created_at,disabled,must_change_password FROM users WHERE username=?`, username))
}

// UserByID looks up a user by id.
func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return s.scanUser(s.queryRowCtx(ctx, `SELECT id,username,password_hash,role,created_at,disabled,must_change_password FROM users WHERE id=?`, id))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanUser(row rowScanner) (User, error) {
	var u User
	var created int64
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &created, &u.Disabled, &u.MustChangePassword); err != nil {
		return User{}, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, nil
}

// CreateUser inserts a new account and returns its id. mustChangePassword marks
// an admin-generated temporary password. The username is stored via a bound
// parameter.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string, mustChangePassword bool) (string, error) {
	id := hex.EncodeToString(sha256Sum("user\x00" + username)[:12])
	if _, err := s.execCtx(ctx, `INSERT INTO users(id,username,password_hash,role,created_at,must_change_password) VALUES(?,?,?,?,?,?) ON CONFLICT(username) DO NOTHING`,
		id, username, passwordHash, role, time.Now().Unix(), mustChangePassword); err != nil {
		return "", err
	}
	u, err := s.UserByUsername(ctx, username)
	if err != nil {
		return "", err
	}
	return u.ID, nil
}

// SetUserPassword updates a user's bcrypt password hash. mustChangePassword is
// true for an admin-issued temporary password and false once the user has chosen
// their own.
func (s *Store) SetUserPassword(ctx context.Context, userID, passwordHash string, mustChangePassword bool) error {
	_, err := s.execCtx(ctx, `UPDATE users SET password_hash=?,must_change_password=? WHERE id=?`, passwordHash, mustChangePassword, userID)
	return err
}

// CreateSession issues a random opaque session token for a user, valid until
// expiresAt, and returns it.
func (s *Store) CreateSession(ctx context.Context, userID string, expiresAt time.Time) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if _, err := s.execCtx(ctx, `INSERT INTO sessions(id,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		token, userID, time.Now().Unix(), expiresAt.Unix()); err != nil {
		return "", err
	}
	return token, nil
}

// SessionUser resolves the user for a session token, or sql.ErrNoRows if the
// token is unknown or expired.
func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	row := s.queryRowCtx(ctx, `SELECT u.id,u.username,u.password_hash,u.role,u.created_at,u.disabled,u.must_change_password FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id=? AND s.expires_at>? AND u.disabled=FALSE`, token, time.Now().Unix())
	return s.scanUser(row)
}

// DeleteSession revokes a session token (logout).
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.execCtx(ctx, `DELETE FROM sessions WHERE id=?`, token)
	return err
}

// CleanupExpiredSessions removes sessions past their expiry.
func (s *Store) CleanupExpiredSessions(ctx context.Context) error {
	_, err := s.execCtx(ctx, `DELETE FROM sessions WHERE expires_at<=?`, time.Now().Unix())
	return err
}
