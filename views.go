package main

import (
	"crypto/sha256"
	"log"
	"net"
	"net/http"
	"time"

	"tiny-tube/store"
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

// maxViewDebounce is a hard cap; the oldest ring slot is evicted in O(1).
const maxViewDebounce = 8192

type viewEntry struct {
	key string
	at  time.Time
}

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
	digest := sha256.Sum256([]byte(id + "\x00" + clientIP(r.RemoteAddr)))
	key := string(digest[:])
	now := time.Now()
	a.viewMu.Lock()
	if last, ok := a.viewSeen[key]; ok && now.Sub(last) < viewDebounceTTL {
		a.viewMu.Unlock()
		return current
	}
	if len(a.viewOrder) == 0 {
		a.viewOrder = make([]viewEntry, maxViewDebounce)
	}
	old := a.viewOrder[a.viewNext]
	if last, ok := a.viewSeen[old.key]; ok && last.Equal(old.at) {
		delete(a.viewSeen, old.key)
	}
	a.viewOrder[a.viewNext] = viewEntry{key, now}
	a.viewNext = (a.viewNext + 1) % maxViewDebounce
	a.viewSeen[key] = now
	a.viewMu.Unlock()
	total, err := a.fs.IncrementViews(id)
	if err != nil {
		log.Printf("count view %s: %v", id, err)
		return current
	}
	return total
}

// dayStat pairs one UTC day with its view count for the watch-page table.
type dayStat struct {
	Day   string
	Views int64
}

// fillStatDays builds an oldest-first series of the last days days,
// filling missing days with zero so the table always covers the window.
func fillStatDays(history []store.DayViews, days int) []dayStat {
	byDay := map[string]int64{}
	for _, h := range history {
		byDay[h.Day] = h.Views
	}
	today := time.Now().UTC()
	out := make([]dayStat, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := today.AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, dayStat{Day: day, Views: byDay[day]})
	}
	return out
}
