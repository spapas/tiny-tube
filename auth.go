package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

// Viewers: open. Upload: single password via BasicAuth.
// Nginx mode: same creds in htpasswd, Go re-checks anyway.
func uploadAuth(user, pass string, next http.Handler) http.Handler {
	wantUser, wantPass := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		gotUser, gotPass := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
		userOK := subtle.ConstantTimeCompare(gotUser[:], wantUser[:])
		passOK := subtle.ConstantTimeCompare(gotPass[:], wantPass[:])
		w.Header().Set("Cache-Control", "no-store")
		if !ok || userOK&passOK != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="upload"`)
			if strings.Contains(r.Header.Get("Accept"), "application/json") || r.URL.Query().Get("format") == "json" {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"auth required"}` + "\n"))
				return
			}
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
