package main

import (
	"os"
)

// Viewing is public. Upload/edit/delete and administration require a logged-in
// user with the matching role; see authn.go.
func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
