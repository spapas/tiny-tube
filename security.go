package main

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const hlsScriptURL = "https://cdn.jsdelivr.net/npm/hls.js@1.6.19/dist/hls.min.js"

func secureHTTP(next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeAPIError(w, r, http.StatusForbidden, "cross-origin mutation forbidden")
	}))
	protected := csrf.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' "+hlsScriptURL+"; style-src 'self'; img-src 'self'; media-src 'self' blob:; connect-src 'self'; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		protected.ServeHTTP(w, r)
	})
}

type authWindow struct {
	start time.Time
	count int
}
type authLimiter struct {
	mu       sync.Mutex
	limit    int
	clients  map[string]authWindow
	overflow authWindow
	now      func() time.Time
}

const maxAuthClients = 4096

func newAuthLimiter(limit int) *authLimiter {
	return &authLimiter{limit: limit, clients: make(map[string]authWindow), now: time.Now}
}

func (l *authLimiter) allow(remoteAddr string) (bool, int) {
	// Forwarding headers are untrusted; use the actual connection's source IP.
	client, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		client = remoteAddr
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	window, exists := l.clients[client]
	if !exists && len(l.clients) >= maxAuthClients {
		for key, entry := range l.clients {
			if now.Sub(entry.start) >= time.Minute {
				delete(l.clients, key)
			}
		}
	}
	overflow := !exists && len(l.clients) >= maxAuthClients
	if overflow {
		window = l.overflow
	}
	if window.start.IsZero() || now.Sub(window.start) >= time.Minute {
		window = authWindow{start: now}
	}
	allowed := window.count < l.limit
	if allowed {
		window.count++
	}
	if overflow {
		l.overflow = window
	} else {
		l.clients[client] = window
	}
	retry := int((time.Minute - now.Sub(window.start) + time.Second - 1) / time.Second)
	if retry < 1 {
		retry = 1
	}
	return allowed, retry
}

func (a *application) protect(next http.Handler) http.Handler {
	authed := uploadAuth(a.cfg.UploadUser, a.cfg.UploadPass, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check the limit before credentials, so 429 cannot become a password oracle.
		if allowed, retry := a.authRequests.allow(r.RemoteAddr); !allowed {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeAPIError(w, r, http.StatusTooManyRequests, "too many authentication requests; retry later")
			return
		}
		authed.ServeHTTP(w, r)
	})
}

func (a *application) httpServer() *http.Server {
	writeTimeout := time.Duration(a.cfg.UploadTimeoutSeconds+60) * time.Second
	if writeTimeout < 30*time.Minute {
		writeTimeout = 30 * time.Minute
	}
	return &http.Server{Addr: a.cfg.address(), Handler: a.routes(), MaxHeaderBytes: 64 << 10,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: writeTimeout, IdleTimeout: 60 * time.Second}
}
