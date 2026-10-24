package store

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestSessionRefreshNeverInsertsAndPreservesNewerExpiry(t *testing.T) {
	for name, s := range testAuthStores(t) {
		t.Run(name, func(t *testing.T) {
			previous := testSession("ed", time.Now().Add(time.Hour))
			next := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
			if err := s.RefreshSession(previous, next); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing session refreshed: %v", err)
			}
			if err := s.CreateSession(previous); err != nil {
				t.Fatal(err)
			}
			if err := s.RefreshSession(previous, next); err != nil {
				t.Fatal(err)
			}
			later := time.Now().Add(25 * time.Hour).UTC().Format(time.RFC3339Nano)
			if err := s.RefreshSession(previous, later); err != nil {
				t.Fatal(err)
			}
			got, _ := s.GetSession(previous.TokenHash)
			if got.ExpiresAt != next {
				t.Fatal("stale refresh changed newer expiry")
			}
			if err := s.DeleteSession(previous.TokenHash); err != nil {
				t.Fatal(err)
			}
			if err := s.RefreshSession(previous, next); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("revoked token recreated: %v", err)
			}
		})
	}
}

func TestLoginTimestampCannotOverwriteAccountChanges(t *testing.T) {
	for name, s := range testAuthStores(t) {
		t.Run(name, func(t *testing.T) {
			current := testUser("ed", RoleViewer)
			current.Active = false
			current.PasswordHash = "replacement hash"
			if err := s.PutUser(current); err != nil {
				t.Fatal(err)
			}
			if err := s.TouchUserLogin("ed", time.Now()); err != nil {
				t.Fatal(err)
			}
			got, _ := s.GetUser("ed")
			if got.Role != RoleViewer || got.Active || got.PasswordHash != current.PasswordHash || got.LastLoginAt == "" {
				t.Fatal("login bookkeeping overwrote account policy")
			}
		})
	}
}

func TestBrowseAgreesAcrossStoresWithVisibilityAndUnicode(t *testing.T) {
	fs := NewFS(t.TempDir())
	db := newTestSQLite(t)
	for _, s := range []Store{fs, db} {
		for i := 0; i < 31; i++ {
			m := Meta{ID: fmt.Sprintf("video-%02d", i), Title: fmt.Sprintf("Εκπαίδευση %02d", i), Desc: "literal % and _", Category: "Ομάδα", Created: time.Date(2026, 10, 24, 10, 0, 0, i, time.FixedZone("UTC+3", 3*3600)).Format(time.RFC3339Nano), Status: "ready", Views: int64(i % 4)}
			if i%3 == 0 {
				m.AccessList = "private"
			}
			if i%5 == 0 {
				m.AccessList = "other"
			}
			if err := s.Put(m); err != nil {
				t.Fatal(err)
			}
		}
	}
	queries := []VideoQuery{
		{Page: 1, PageSize: 7},
		{Page: 2, PageSize: 7, Text: "ΕΚΠΑΊΔΕΥΣΗ", Category: "Ομάδα"},
		{Page: 3, PageSize: 4, Sort: "views", AccessLists: map[string]bool{"private": true}},
		{Page: 999, PageSize: 3, All: true},
		{Page: 1, PageSize: 5, OnlyLocked: true, AccessLists: map[string]bool{"private": true}},
		{Page: 1, PageSize: 5, Text: "%", All: true},
		{Page: 1, PageSize: 5, Category: "' OR 1=1 --", All: true},
	}
	for _, q := range queries {
		want, err := BrowseVideos(fs, q)
		if err != nil {
			t.Fatal(err)
		}
		got, err := BrowseVideos(db, q)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("query %+v:\nSQLite=%+v\nFS=%+v", q, got, want)
		}
		if len(got.Items) > q.PageSize {
			t.Fatal("unbounded page")
		}
	}
	if err := CheckMetadata(db); err != nil {
		t.Fatal(err)
	}
	queued := Meta{ID: "queued", Status: "queued", Title: "Pending", Created: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := db.Put(queued); err != nil {
		t.Fatal(err)
	}
	pending, err := PendingVideos(db, false)
	if err != nil || len(pending) != 1 || pending[0].ID != "queued" {
		t.Fatalf("queue query: %+v %v", pending, err)
	}
}
