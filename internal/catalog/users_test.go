package catalog

import (
	"context"
	"errors"
	"testing"
	"time"
)

// admins creates an enabled admin account and returns its id.
func testAdmin(t *testing.T, s *Store, username string) string {
	t.Helper()
	id, err := s.CreateUser(context.Background(), username, "hash", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTheLastEnabledAdminCannotBeDemoted(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	only := testAdmin(t, s, "owner")

	if err := s.SetUserRole(ctx, only, "user"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoting the only admin locks everyone out of admin forever: got %v", err)
	}
	got, err := s.UserByID(ctx, only)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != "admin" {
		t.Fatalf("the refused demotion must not have applied: role=%q", got.Role)
	}
}

func TestTheLastEnabledAdminCannotBeDisabledOrDeleted(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	only := testAdmin(t, s, "owner")
	other := testAdmin(t, s, "helper")
	if err := s.SetUserRole(ctx, other, "user"); err != nil {
		t.Fatal(err)
	}

	if err := s.SetUserDisabled(ctx, only, true, other); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disabling the only admin locks the owner out: got %v", err)
	}
	if err := s.DeleteUser(ctx, only, other); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the only admin locks the owner out: got %v", err)
	}
}

func TestASecondAdminMakesDemotingTheFirstLegal(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	first := testAdmin(t, s, "owner")
	testAdmin(t, s, "cohost")

	if err := s.SetUserRole(ctx, first, "user"); err != nil {
		t.Fatalf("with another admin present, demotion is safe: %v", err)
	}
}

func TestAUserCannotDisableOrDeleteThemselves(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	me := testAdmin(t, s, "owner")
	testAdmin(t, s, "cohost") // so the last-admin rule is not what refuses

	if err := s.SetUserDisabled(ctx, me, true, me); !errors.Is(err, ErrSelfTarget) {
		t.Fatalf("disabling yourself is always a mistake: got %v", err)
	}
	if err := s.DeleteUser(ctx, me, me); !errors.Is(err, ErrSelfTarget) {
		t.Fatalf("deleting yourself is always a mistake: got %v", err)
	}
}

func TestDisablingAUserRevokesTheirLiveSessions(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := testAdmin(t, s, "owner")
	victim, err := s.CreateUser(ctx, "guest", "hash", "user", false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.CreateSession(ctx, victim, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, token); err != nil {
		t.Fatalf("the session should start valid: %v", err)
	}

	if err := s.SetUserDisabled(ctx, victim, true, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, token); err == nil {
		t.Fatal("a disabled user's existing session must stop working, or 'disabled' means nothing until it expires")
	}
}

// Re-enabling an account must not bring its old sessions back to life. Blocking
// a disabled user by filtering alone would do exactly that: the cookie an
// intruder held before the disable would start working again the moment the
// account was restored.
func TestReEnablingAUserDoesNotRestoreOldSessions(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := testAdmin(t, s, "owner")
	victim, err := s.CreateUser(ctx, "guest", "hash", "user", false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.CreateSession(ctx, victim, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetUserDisabled(ctx, victim, true, admin); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDisabled(ctx, victim, false, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, token); err == nil {
		t.Fatal("a session held during a disable must stay dead after the account is re-enabled")
	}
}

func TestUsersListsEveryAccount(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	testAdmin(t, s, "owner")
	if _, err := s.CreateUser(ctx, "guest", "hash", "user", true); err != nil {
		t.Fatal(err)
	}

	list, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("both accounts must be listed: got %d", len(list))
	}
	byName := map[string]User{}
	for _, u := range list {
		byName[u.Username] = u
	}
	if !byName["guest"].MustChangePassword {
		t.Fatal("a user created with a temporary password must be flagged to change it")
	}
	if byName["owner"].Role != "admin" {
		t.Fatalf("roles must be listed: got %q", byName["owner"].Role)
	}
}
