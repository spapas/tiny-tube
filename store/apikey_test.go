package store

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestGenerateAndSplitAPIKey(t *testing.T) {
	raw, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != len("tt-")+32 || !ValidKeyPrefix(prefix) || len(hash) != 64 {
		t.Fatalf("malformed key: %q %q %q", raw, prefix, hash)
	}
	if got, ok := SplitAPIKey(raw); !ok || got != prefix {
		t.Fatalf("split = %q, %v; want %q", got, ok, prefix)
	}
	other, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if other == raw {
		t.Fatal("duplicate keys")
	}
	for _, bad := range []string{"", "tt-short", "Bearer " + raw, raw + "!", "xx-" + raw[3:]} {
		if _, ok := SplitAPIKey(bad); ok {
			t.Fatalf("SplitAPIKey(%q) accepted", bad)
		}
	}
}

func testAPIKey(prefix, user string) APIKey {
	return APIKey{
		Prefix: prefix, Username: user, Name: "script",
		KeyHash:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func TestAuthStoresAgreeOnAPIKeys(t *testing.T) {
	for backend, s := range testAuthStores(t) {
		t.Run(backend, func(t *testing.T) {
			if keys, err := s.ListAPIKeys(); err != nil || len(keys) != 0 {
				t.Fatalf("list = %+v %v", keys, err)
			}
			bad := testAPIKey("a2345678b901", "ed")
			bad.Prefix = "short"
			if err := s.CreateAPIKey(bad); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("bad prefix: %v", err)
			}
			bad = testAPIKey("a2345678b901", "a/b")
			if err := s.CreateAPIKey(bad); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("bad user: %v", err)
			}
			bad = testAPIKey("a2345678b901", "ed")
			bad.KeyHash = "short"
			if err := s.CreateAPIKey(bad); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("bad hash: %v", err)
			}
			bad = testAPIKey("a2345678b901", "ed")
			bad.Name = ""
			if err := s.CreateAPIKey(bad); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("empty name: %v", err)
			}
			if _, err := s.GetAPIKey("nope"); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("bad prefix lookup: %v", err)
			}
			if _, err := s.GetAPIKey("a2345678b901"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing key: %v", err)
			}
			if err := s.PutUser(testUser("ed", RoleEditor)); err != nil {
				t.Fatal(err)
			}
			k1, k2 := testAPIKey("a2345678b901", "ed"), testAPIKey("b2345678b901", "ed")
			if err := s.CreateAPIKey(k1); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateAPIKey(k2); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetAPIKey(k1.Prefix)
			if err != nil || got.Username != "ed" || got.Name != "script" || got.CreatedAt == "" {
				t.Fatalf("round trip: %+v %v", got, err)
			}
			keys, err := s.ListAPIKeys()
			if err != nil || len(keys) != 2 || keys[0].Prefix != k1.Prefix {
				t.Fatalf("list: %+v %v", keys, err)
			}
			before := time.Now().Add(-time.Hour)
			if err := s.TouchAPIKey(k1.Prefix, before); err != nil {
				t.Fatal(err)
			}
			got, err = s.GetAPIKey(k1.Prefix)
			if err != nil || got.LastUsedAt == "" {
				t.Fatalf("touch: %+v %v", got, err)
			}
			if err := s.TouchAPIKey("c2345678b901", time.Now()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("touch missing: %v", err)
			}
			if err := s.DeleteAPIKey("c2345678b901"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("delete missing: %v", err)
			}
			if err := s.DeleteAPIKey(k2.Prefix); err != nil {
				t.Fatal(err)
			}
			// Deleting the user cascades to its keys.
			if err := s.DeleteUser("ed"); err != nil {
				t.Fatal(err)
			}
			if keys, err := s.ListAPIKeys(); err != nil || len(keys) != 0 {
				t.Fatalf("keys survived user deletion: %+v %v", keys, err)
			}
		})
	}
}
