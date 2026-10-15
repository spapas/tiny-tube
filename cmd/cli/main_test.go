package main

import (
	"testing"

	"tiny-tube/store"
)

func TestNormalizeCLIArgs(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		command string
	}{
		{[]string{"users", "list"}, "user-list"},
		{[]string{"users", "set-role", "--username", "a"}, "user-set-role"},
		{[]string{"users", "api-key", "--username", "a"}, "user-api-key"},
		{[]string{"users", "api-keys"}, "user-api-keys"},
		{[]string{"users", "api-key-revoke", "--prefix", "a2345678b901"}, "user-api-key-revoke"},
		{[]string{"user-list"}, "user-list"},
		{[]string{"user-api-keys"}, "user-api-keys"},
	} {
		command, _, err := normalizeCLIArgs(tc.args)
		if err != nil || command != tc.command {
			t.Fatalf("normalizeCLIArgs(%v) = %q, %v; want %q", tc.args, command, err, tc.command)
		}
	}
	if _, _, err := normalizeCLIArgs([]string{"users", "delete"}); err == nil {
		t.Fatal("unknown users subcommand must fail")
	}
	if _, _, err := normalizeCLIArgs([]string{"videos"}); err == nil {
		t.Fatal("unknown command must fail")
	}
}

func TestParseSetRoleArgsRejectsBadRole(t *testing.T) {
	if _, _, err := parseSetRoleArgs([]string{"--username", "a", "--role", "boss"}); err == nil {
		t.Fatal("unknown role must fail")
	}
	if _, _, err := parseSetRoleArgs([]string{"--username", "a"}); err == nil {
		t.Fatal("missing role must fail")
	}
	u, r, err := parseSetRoleArgs([]string{"--username", "a", "--role", "admin"})
	if err != nil || u != "a" || r != store.RoleAdmin {
		t.Fatalf("parse = %q %q %v", u, r, err)
	}
}

func TestLastAdminStanding(t *testing.T) {
	users := []store.User{
		{Username: "root", Role: store.RoleAdmin, Active: true},
		{Username: "ed", Role: store.RoleEditor, Active: true},
	}
	if !lastAdminStanding(users, "root") {
		t.Fatal("demoting the only admin must be refused")
	}
	if lastAdminStanding(users, "ed") {
		t.Fatal("demoting a non-admin must be allowed")
	}
	users = append(users, store.User{Username: "root2", Role: store.RoleAdmin, Active: true})
	if lastAdminStanding(users, "root") {
		t.Fatal("a second active admin must allow demotion")
	}
}
