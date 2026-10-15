// Command cli is the small Tiny Tube operator tool for user administration.
//
// It opens the same VIDEO_DIR/STORE/DB_PATH storage as the server and offers
// the read-only list plus the role grant used to promote someone to admin:
//
//	go run ./cmd/cli users list
//	go run ./cmd/cli users set-role --username alice --role admin
//
// Roles are admin, editor or viewer. The tool refuses to demote the last
// active administrator, mirroring the /admin web guard.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tiny-tube/store"
)

const cliUsage = `Usage: cli <command> [flags]

Users (same storage as the server: VIDEO_DIR/STORE/DB_PATH from .env or env):
  users list                                    # list username, role, active, source
  users set-role --username U --role R          # R is admin, editor or viewer

API keys (automation without cookies; send as 'Authorization: Bearer <key>'):
  users api-key --username U [--name N]         # create; prints the key once
  users api-keys [--username U]                 # list prefix, user, name, last use
  users api-key-revoke --prefix P               # revoke

Aliases: user-list, user-set-role, user-api-key, user-api-keys, user-api-key-revoke
(and the 'users <sub>' spelling above).
Making someone admin: users set-role --username U --role admin`

func main() {
	loadDotEnv(".env")
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, cliUsage)
		os.Exit(2)
	}
	command, commandArgs, err := normalizeCLIArgs(os.Args[1:])
	if err != nil {
		fatal(err)
	}
	switch command {
	case "users":
		fmt.Fprintln(os.Stderr, cliUsage)
		os.Exit(2)
	case "user-list":
		if err := cmdList(commandArgs); err != nil {
			fatal(err)
		}
	case "user-set-role":
		if err := cmdSetRole(commandArgs); err != nil {
			fatal(err)
		}
	case "user-api-key":
		if err := cmdCreateAPIKey(commandArgs); err != nil {
			fatal(err)
		}
	case "user-api-keys":
		if err := cmdListAPIKeys(commandArgs); err != nil {
			fatal(err)
		}
	case "user-api-key-revoke":
		if err := cmdRevokeAPIKey(commandArgs); err != nil {
			fatal(err)
		}
	default:
		fatal(fmt.Errorf("unknown command: %q\n%s", command, cliUsage))
	}
}

// normalizeCLIArgs accepts the grouped `users <sub>` spelling (like SHDE)
// plus the flat `user-list` / `user-set-role` aliases.
func normalizeCLIArgs(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, errors.New(cliUsage)
	}
	if args[0] != "users" && args[0] != "user" {
		switch args[0] {
		case "user-list", "user-set-role", "user-api-key", "user-api-keys", "user-api-key-revoke":
			return args[0], args[1:], nil
		}
		return "", nil, fmt.Errorf("unknown command: %q\n%s", args[0], cliUsage)
	}
	if len(args) < 2 {
		return "users", nil, nil
	}
	switch args[1] {
	case "list":
		return "user-list", args[2:], nil
	case "set-role":
		return "user-set-role", args[2:], nil
	case "api-key":
		return "user-api-key", args[2:], nil
	case "api-keys":
		return "user-api-keys", args[2:], nil
	case "api-key-revoke":
		return "user-api-key-revoke", args[2:], nil
	}
	return "", nil, fmt.Errorf("users: unknown subcommand %q (want list, set-role, api-key, api-keys or api-key-revoke)", args[1])
}

func parseSetRoleArgs(args []string) (username, role string, err error) {
	fs := flag.NewFlagSet("users set-role", flag.ContinueOnError)
	u := fs.String("username", "", "login name (required)")
	r := fs.String("role", "", "admin, editor or viewer (required)")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if *u == "" || *r == "" {
		return "", "", errors.New("users set-role: --username and --role are required")
	}
	if !store.ValidRole(*r) {
		return "", "", fmt.Errorf("users set-role: unknown role %q (want admin, editor or viewer)", *r)
	}
	return *u, *r, nil
}

// lastAdminStanding reports whether no active administrator other than
// except would remain, mirroring application.lastAdminStanding.
func lastAdminStanding(users []store.User, except string) bool {
	for _, u := range users {
		if u.IsAdmin() && u.Active && u.Username != except {
			return false
		}
	}
	return true
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("users list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("users list takes no arguments")
	}
	s, closeStore, err := openAuthStore()
	if err != nil {
		return err
	}
	defer closeStore()
	users, err := s.ListUsers()
	if err != nil {
		return err
	}
	for _, u := range users {
		display := u.DisplayName
		if display == "" {
			display = u.Username
		}
		fmt.Printf("%-20s %-8s active=%-5v src=%-5s %s\n",
			u.Username, u.Role, u.Active, u.AuthSource, display)
	}
	return nil
}

func cmdSetRole(args []string) error {
	username, role, err := parseSetRoleArgs(args)
	if err != nil {
		return err
	}
	if !store.ValidUsername(username) {
		return fmt.Errorf("invalid username %q", username)
	}
	s, closeStore, err := openAuthStore()
	if err != nil {
		return err
	}
	defer closeStore()
	u, err := s.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("user not found: %q", username)
		}
		return err
	}
	if u.IsAdmin() && role != store.RoleAdmin {
		users, err := s.ListUsers()
		if err != nil {
			return err
		}
		if lastAdminStanding(users, username) {
			return errors.New("at least one active administrator is required")
		}
	}
	u.Role = role
	if err := s.PutUser(u); err != nil {
		return err
	}
	fmt.Printf("role updated: %s -> %s (src=%s active=%v)\n", username, role, u.AuthSource, u.Active)
	return nil
}

func cmdCreateAPIKey(args []string) error {
	fs := flag.NewFlagSet("users api-key", flag.ContinueOnError)
	u := fs.String("username", "", "login name (required)")
	n := fs.String("name", "", "key label, e.g. upload script (default: username)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *u == "" {
		return errors.New("users api-key: --username required")
	}
	if !store.ValidUsername(*u) {
		return fmt.Errorf("invalid username %q", *u)
	}
	name := *n
	if name == "" {
		name = *u
	}
	s, closeStore, err := openAuthStore()
	if err != nil {
		return err
	}
	defer closeStore()
	if _, err := s.GetUser(*u); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("user not found: %q", *u)
		}
		return err
	}
	raw, prefix, hash, err := store.GenerateAPIKey()
	if err != nil {
		return err
	}
	if err := s.CreateAPIKey(store.APIKey{Prefix: prefix, Username: *u, Name: name, KeyHash: hash}); err != nil {
		return err
	}
	// Plaintext is shown once; only its hash is stored.
	fmt.Printf("api key for %s (%s), prefix %s:\n%s\n", *u, name, prefix, raw)
	return nil
}

func cmdListAPIKeys(args []string) error {
	fs := flag.NewFlagSet("users api-keys", flag.ContinueOnError)
	u := fs.String("username", "", "only keys of this user (default: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, closeStore, err := openAuthStore()
	if err != nil {
		return err
	}
	defer closeStore()
	keys, err := s.ListAPIKeys()
	if err != nil {
		return err
	}
	for _, k := range keys {
		if *u != "" && k.Username != *u {
			continue
		}
		last := k.LastUsedAt
		if last == "" {
			last = "never"
		}
		fmt.Printf("%-14s %-20s %-30s last=%s\n", k.Prefix, k.Username, k.Name, last)
	}
	return nil
}

func cmdRevokeAPIKey(args []string) error {
	fs := flag.NewFlagSet("users api-key-revoke", flag.ContinueOnError)
	p := fs.String("prefix", "", "public key prefix shown by users api-keys (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !store.ValidKeyPrefix(*p) {
		return errors.New("users api-key-revoke: --prefix must be the 12-character public prefix")
	}
	s, closeStore, err := openAuthStore()
	if err != nil {
		return err
	}
	defer closeStore()
	if err := s.DeleteAPIKey(*p); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("key not found: %q", *p)
		}
		return err
	}
	fmt.Printf("api key revoked: %s\n", *p)
	return nil
}

func openAuthStore() (store.AuthStore, func(), error) {
	videoDir := getenv("VIDEO_DIR", "videos")
	root, err := filepath.Abs(videoDir)
	if err != nil {
		return nil, nil, err
	}
	switch getenv("STORE", "sqlite") {
	case "fs":
		return store.NewFS(root), func() {}, nil
	case "sqlite", "":
		dbPath := os.Getenv("DB_PATH")
		if dbPath == "" {
			dbPath = filepath.Join(root, "tiny-tube.db")
		}
		abs, err := filepath.Abs(dbPath)
		if err != nil {
			return nil, nil, err
		}
		db, err := store.NewSQLite(root, abs)
		if err != nil {
			return nil, nil, err
		}
		return db, func() { _ = db.Close() }, nil
	default:
		return nil, nil, errors.New("STORE must be sqlite or fs")
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loadDotEnv applies KEY=VALUE lines from path only for vars not already in
// the environment, mirroring the server startup behavior.
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for k, v := range parseDotEnv(string(b)) {
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}

func parseDotEnv(data string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, " \t") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		out[k] = v
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
