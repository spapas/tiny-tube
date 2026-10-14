package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testAuthStores(t *testing.T) map[string]AuthStore {
	t.Helper()
	root := t.TempDir()
	db, err := NewSQLite(root, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return map[string]AuthStore{
		"fs":     NewFS(filepath.Join(root, "fsroot")),
		"sqlite": db,
	}
}

func testUser(name, role string) User {
	return User{Username: name, DisplayName: name, PasswordHash: "hash", AuthSource: AuthLocal, Role: role, Active: true}
}

func TestAuthStoresAgreeOnUsers(t *testing.T) {
	for backend, s := range testAuthStores(t) {
		t.Run(backend, func(t *testing.T) {
			if n, err := s.UserCount(); err != nil || n != 0 {
				t.Fatalf("count = %d, %v", n, err)
			}
			if _, err := s.GetUser("ghost"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing user: %v", err)
			}
			for _, name := range []string{"", "a/b", "..", ".hidden", "has space", "uniçode"} {
				if _, err := s.GetUser(name); !errors.Is(err, os.ErrInvalid) {
					t.Fatalf("GetUser(%q) = %v", name, err)
				}
				if err := s.PutUser(User{Username: name, Role: RoleViewer, AuthSource: AuthLocal}); !errors.Is(err, os.ErrInvalid) {
					t.Fatalf("PutUser(%q) = %v", name, err)
				}
			}
			if err := s.PutUser(User{Username: "ed", Role: "super", AuthSource: AuthLocal}); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("bad role: %v", err)
			}
			if err := s.PutUser(testUser("ed", RoleEditor)); err != nil {
				t.Fatal(err)
			}
			if err := s.PutUser(testUser("admin", RoleAdmin)); err != nil {
				t.Fatal(err)
			}
			u, err := s.GetUser("ed")
			if err != nil || u.Role != RoleEditor || !u.Active || u.CreatedAt == "" {
				t.Fatalf("round trip: %+v %v", u, err)
			}
			users, err := s.ListUsers()
			if err != nil || len(users) != 2 || users[0].Username != "admin" || users[1].Username != "ed" {
				t.Fatalf("list: %+v %v", users, err)
			}
			if n, err := s.UserCount(); err != nil || n != 2 {
				t.Fatalf("count = %d, %v", n, err)
			}
			if err := s.DeleteUser("ghost"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("delete missing: %v", err)
			}
			if err := s.DeleteUser("ed"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetUser("ed"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deleted user readable: %v", err)
			}
		})
	}
}

func testSession(user string, expires time.Time) Session {
	return Session{
		TokenHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Username:  user,
		ExpiresAt: expires.UTC().Format(time.RFC3339Nano),
	}
}

func TestAuthStoresAgreeOnSessions(t *testing.T) {
	for backend, s := range testAuthStores(t) {
		t.Run(backend, func(t *testing.T) {
			if err := s.CreateSession(testSession("ed", time.Now().Add(time.Hour))); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetSession(testSession("ed", time.Now()).TokenHash)
			if err != nil || got.Username != "ed" {
				t.Fatalf("round trip: %+v %v", got, err)
			}
			if _, err := s.GetSession("short"); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("bad token: %v", err)
			}
			if _, err := s.GetSession("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing session: %v", err)
			}
			// Expired sessions behave as missing.
			expired := testSession("ed", time.Now().Add(-time.Minute))
			expired.TokenHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if err := s.CreateSession(expired); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetSession(expired.TokenHash); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expired session readable: %v", err)
			}
			if err := s.DeleteUserSessions("nobody"); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteUserSessions("ed"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetSession(testSession("ed", time.Now()).TokenHash); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("user sessions not revoked: %v", err)
			}
			if err := s.DeleteSession(testSession("ed", time.Now()).TokenHash); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("delete missing session: %v", err)
			}
			// Prune removes only expired rows.
			live := testSession("ed", time.Now().Add(time.Hour))
			live.TokenHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			stale := testSession("ed", time.Now().Add(-time.Hour))
			stale.TokenHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
			if err := s.CreateSession(live); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateSession(stale); err != nil {
				t.Fatal(err)
			}
			if err := s.PruneSessions(time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetSession(live.TokenHash); err != nil {
				t.Fatalf("live session pruned: %v", err)
			}
			if _, err := s.GetSession(stale.TokenHash); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale session kept: %v", err)
			}
		})
	}
}

func TestFSAuthRecordsStayOutOfVideoListing(t *testing.T) {
	s := NewFS(t.TempDir())
	if err := s.PutUser(testUser("ed", RoleEditor)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(testSession("ed", time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	metas, err := s.List("")
	if err != nil || len(metas) != 0 {
		t.Fatalf("auth records leaked into listing: %+v %v", metas, err)
	}
	if _, err := s.Get(".auth"); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf(".auth reachable as video: %v", err)
	}
}
