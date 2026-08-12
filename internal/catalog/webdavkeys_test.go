package catalog

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func testUser(t *testing.T, s *Store, username string) string {
	t.Helper()
	id, err := s.CreateUser(context.Background(), username, "hash", "user", false)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCreateWebDAVKeyRoundTripsThroughWebDAVKeyUser(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	userID := testUser(t, s, "ash")

	rec, token, err := s.CreateWebDAVKey(ctx, userID, "my phone")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Label != "my phone" || rec.UserID != userID {
		t.Fatalf("unexpected record: %+v", rec)
	}

	got, err := s.WebDAVKeyUser(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != userID {
		t.Fatalf("token resolved to the wrong user: got %s want %s", got.ID, userID)
	}
}

func TestWebDAVKeyUserRejectsUnknownRevokedOrDisabled(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := testAdmin(t, s, "owner")
	userID := testUser(t, s, "misty")

	if _, err := s.WebDAVKeyUser(ctx, "not-a-real-token"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("an unknown token must be refused: got %v", err)
	}

	rec, token, err := s.CreateWebDAVKey(ctx, userID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeWebDAVKey(ctx, userID, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebDAVKeyUser(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a revoked key must be refused: got %v", err)
	}

	_, token2, err := s.CreateWebDAVKey(ctx, userID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDisabled(ctx, userID, true, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebDAVKeyUser(ctx, token2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a key belonging to a disabled user must be refused: got %v", err)
	}
}

func TestRevokeWebDAVKeyIsScopedToItsOwner(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testUser(t, s, "brock")
	intruder := testUser(t, s, "intruder")

	rec, token, err := s.CreateWebDAVKey(ctx, owner, "owner's key")
	if err != nil {
		t.Fatal(err)
	}

	// Revoking with the wrong user_id must be a no-op: it must not touch a
	// key belonging to someone else, even if the caller knows its ID.
	if err := s.RevokeWebDAVKey(ctx, intruder, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebDAVKeyUser(ctx, token); err != nil {
		t.Fatalf("a key must survive a revoke attempt from a non-owner: %v", err)
	}

	if err := s.RevokeWebDAVKey(ctx, owner, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebDAVKeyUser(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("the owner's own revoke must take effect")
	}
}

func TestWebDAVKeysForUserListsOnlyThatUsersKeys(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := testUser(t, s, "a")
	b := testUser(t, s, "b")

	if _, _, err := s.CreateWebDAVKey(ctx, a, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateWebDAVKey(ctx, a, "a2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateWebDAVKey(ctx, b, "b1"); err != nil {
		t.Fatal(err)
	}

	keys, err := s.WebDAVKeysForUser(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys for user a, got %d: %+v", len(keys), keys)
	}
	for _, k := range keys {
		if k.UserID != a {
			t.Fatalf("leaked another user's key: %+v", k)
		}
	}
}

func TestDeletingAUserCascadesItsWebDAVKeys(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := testAdmin(t, s, "owner")
	userID := testUser(t, s, "gone-soon")

	if _, _, err := s.CreateWebDAVKey(ctx, userID, "soon gone"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, userID, admin); err != nil {
		t.Fatal(err)
	}
	keys, err := s.WebDAVKeysForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("cascade delete must remove the user's WebDAV keys: %+v", keys)
	}
}
