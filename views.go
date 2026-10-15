package main

import (
	"log"
	"net"
	"net/http"
	"time"
)

// View counting: an authorized /watch render adds one view, debounced per
// viewer and video so refreshes do not inflate the total. Debounce state is
// in-memory and bounded; a restart may count one extra view per active
// viewer. Only client IPs are hashed into the key — nothing is persisted.
//
// Locked videos count like public ones once access is granted; denied (404)
// requests never count.

// viewDebounceTTL is how long one viewer IP counts once per video.
const viewDebounceTTL = 24 * time.Hour

// maxViewDebounce bounds debounce memory; the oldest entries are swept first.
const maxViewDebounce = 8192

// clientIP extracts the connection source IP. Forwarding headers are
// untrusted and ignored, mirroring the auth limiter.
func clientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return host
}

// countView records one debounced view and returns the total to display.
func (a *application) countView(r *http.Request, id string, current int64) int64 {
	key := id + "\x00" + clientIP(r.RemoteAddr)
	now := time.Now()
	a.viewMu.Lock()
	if last, ok := a.viewSeen[key]; ok && now.Sub(last) < viewDebounceTTL {
		a.viewMu.Unlock()
		return current
	}
	a.viewSeen[key] = now
	if len(a.viewSeen) > maxViewDebounce {
		for k, last := range a.viewSeen {
			if now.Sub(last) >= viewDebounceTTL {
				delete(a.viewSeen, k)
			}
		}
	}
	a.viewMu.Unlock()
	total, err := a.fs.IncrementViews(id)
	if err != nil {
		log.Printf("count view %s: %v", id, err)
		return current
	}
	return total
}
