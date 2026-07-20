package catalog

import (
	"context"
	"errors"
)

// Guard rails on account changes that would lock an operator out of their own
// server. They are enforced here, next to the query, because a check that lives
// in a handler is one forgotten call away from being bypassed.
var (
	// ErrLastAdmin reports an attempt to demote, disable, or delete the only
	// remaining enabled admin.
	ErrLastAdmin = errors.New("this is the last enabled admin account")
	// ErrSelfTarget reports an attempt to disable or delete your own account.
	ErrSelfTarget = errors.New("you cannot disable or delete your own account")
)

// Users lists every account, oldest first.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.queryCtx(ctx, `SELECT id,username,password_hash,role,created_at,disabled,must_change_password FROM users ORDER BY created_at ASC,username ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetUserRole promotes or demotes an account. Demoting the last enabled admin is
// refused: it would leave the deployment with no way to administer itself.
func (s *Store) SetUserRole(ctx context.Context, id, role string) error {
	if role != "admin" {
		if err := s.guardLastAdmin(ctx, id); err != nil {
			return err
		}
	}
	_, err := s.execCtx(ctx, `UPDATE users SET role=? WHERE id=?`, role, id)
	return err
}

// SetUserDisabled blocks or restores an account's ability to sign in, keeping all
// of its data. Disabling also revokes the account's live sessions, without that,
// an existing cookie keeps working and "disabled" would mean nothing until it
// expired. actorID is the admin making the change.
func (s *Store) SetUserDisabled(ctx context.Context, id string, disabled bool, actorID string) error {
	if disabled {
		if id == actorID {
			return ErrSelfTarget
		}
		if err := s.guardLastAdmin(ctx, id); err != nil {
			return err
		}
	}
	if _, err := s.execCtx(ctx, `UPDATE users SET disabled=? WHERE id=?`, disabled, id); err != nil {
		return err
	}
	if !disabled {
		return nil
	}
	return s.DeleteSessionsForUser(ctx, id)
}

// DeleteUser removes an account. Its Pokémon, dex, sessions, and save rows go
// with it by cascade; the caller removes the save files, which the database does
// not own. actorID is the admin making the change.
func (s *Store) DeleteUser(ctx context.Context, id, actorID string) error {
	if id == actorID {
		return ErrSelfTarget
	}
	if err := s.guardLastAdmin(ctx, id); err != nil {
		return err
	}
	_, err := s.execCtx(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// DeleteSessionsForUser signs an account out everywhere.
func (s *Store) DeleteSessionsForUser(ctx context.Context, id string) error {
	_, err := s.execCtx(ctx, `DELETE FROM sessions WHERE user_id=?`, id)
	return err
}

// guardLastAdmin refuses a change that would remove the final enabled admin.
// A disabled admin does not count: it cannot sign in, so it cannot administer.
func (s *Store) guardLastAdmin(ctx context.Context, id string) error {
	var isAdmin bool
	if err := s.queryRowCtx(ctx, `SELECT role='admin' AND disabled=FALSE FROM users WHERE id=?`, id).Scan(&isAdmin); err != nil {
		return err
	}
	if !isAdmin {
		return nil
	}
	var remaining int
	if err := s.queryRowCtx(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=FALSE AND id<>?`, id).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		return ErrLastAdmin
	}
	return nil
}
