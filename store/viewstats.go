package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DayViews counts views of one video on one UTC day (YYYY-MM-DD).
type DayViews struct {
	Day   string `json:"day"`
	Views int64  `json:"views"`
}

// ViewStatsStore records per-day view counts next to the totals kept by
// IncrementViews. Reads return newest first.
type ViewStatsStore interface {
	ViewHistory(id string, days int) ([]DayViews, error)
}

const (
	viewStatsDaysMax  = 60
	viewStatsKeepDays = 60
)

func clampStatDays(days int) int {
	if days <= 0 {
		return 14
	}
	if days > viewStatsDaysMax {
		return viewStatsDaysMax
	}
	return days
}

func todayUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

func statCutoff() string {
	return time.Now().UTC().AddDate(0, 0, -viewStatsKeepDays).Format("2006-01-02")
}

func (s *SQLite) recordViewDay(id, day string) error {
	_, err := s.db.Exec(`INSERT INTO video_views(video_id, day, views) VALUES(?,?,1)
		ON CONFLICT(video_id, day) DO UPDATE SET views=views+1`, id, day)
	return err
}

func (s *SQLite) ViewHistory(id string, days int) ([]DayViews, error) {
	if !ValidID(id) {
		return nil, os.ErrInvalid
	}
	rows, err := s.db.Query(`SELECT day, views FROM video_views WHERE video_id=? ORDER BY day DESC LIMIT ?`, id, clampStatDays(days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayViews
	for rows.Next() {
		var dv DayViews
		if err := rows.Scan(&dv.Day, &dv.Views); err != nil {
			return nil, err
		}
		out = append(out, dv)
	}
	return out, rows.Err()
}

func (s *FS) statsPath() string {
	return filepath.Join(s.Root, ".stats", "views.json")
}

func (s *FS) readStatsLocked() (map[string]map[string]int64, error) {
	b, err := os.ReadFile(s.statsPath())
	if os.IsNotExist(err) {
		return map[string]map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	stats := map[string]map[string]int64{}
	if err := json.Unmarshal(b, &stats); err != nil {
		return nil, err
	}
	return stats, nil
}

func (s *FS) recordViewDay(id, day string) error {
	stats, err := s.readStatsLocked()
	if err != nil {
		return err
	}
	byDay := stats[id]
	if byDay == nil {
		byDay = map[string]int64{}
		stats[id] = byDay
	}
	byDay[day]++
	cutoff := statCutoff()
	for d := range byDay {
		if d < cutoff {
			delete(byDay, d)
		}
	}
	return s.writeJSONFile(s.statsPath(), stats)
}

func (s *FS) ViewHistory(id string, days int) ([]DayViews, error) {
	if !ValidID(id) {
		return nil, os.ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats, err := s.readStatsLocked()
	if err != nil {
		return nil, err
	}
	var out []DayViews
	for day, views := range stats[id] {
		out = append(out, DayViews{Day: day, Views: views})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day > out[j].Day })
	if len(out) > clampStatDays(days) {
		out = out[:clampStatDays(days)]
	}
	return out, nil
}

func dropVideoStats(stats map[string]map[string]int64, id string) {
	delete(stats, id)
}
