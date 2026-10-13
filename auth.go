package main

import (
	"crypto/subtle"
	"net/http"
	"os"
)

// Viewers: open. Upload: single password via BasicAuth.
// Nginx mode: same creds in htpasswd, Go re-checks anyway.
func uploadAuth(next http.Handler) http.Handler {
	user := getenv("UPLOAD_USER", "admin")
	pass := getenv("UPLOAD_PASS", "changeme")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="upload"`)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
