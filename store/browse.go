package store

import (
	"database/sql/driver"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	sqlite "modernc.org/sqlite"
)

// VideoQuery applies authorization before counts, filtering, ordering or paging.
// PageSize is bounded by the caller; no query can request an unbounded page.
type VideoQuery struct {
	Text, Category, Sort string
	Page, PageSize       int
	All, OnlyLocked      bool
	AccessLists          map[string]bool
}

type VideoPage struct {
	Items                          []Meta
	Total, Matches, Page, PageSize int
	Categories                     map[string]int
}

var titleCollators = sync.Pool{New: func() any {
	return collate.New(language.Und, collate.Numeric, collate.IgnoreCase)
}}

func compareTitles(a, b string) int {
	c := titleCollators.Get().(*collate.Collator)
	result := c.CompareString(a, b)
	titleCollators.Put(c)
	return result
}

func init() {
	sqlite.MustRegisterCollationUtf8("tt_title", compareTitles)
	sqlite.MustRegisterDeterministicScalarFunction("tt_fold", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		v, _ := args[0].(string)
		return strings.ToLower(v), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("tt_date", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		v, _ := args[0].(string)
		t, _ := time.Parse(time.RFC3339Nano, v)
		return t.UTC().Format("2006-01-02T15:04:05.000000000"), nil
	})
}

func normalizeQuery(q VideoQuery) VideoQuery {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Page > 1000000 {
		q.Page = 1000000
	}
	if q.PageSize < 1 {
		q.PageSize = 24
	}
	if q.PageSize > 100 {
		q.PageSize = 100
	}
	q.Text = strings.ToLower(strings.TrimSpace(q.Text))
	return q
}

func pageBounds(p *VideoPage, q VideoQuery) (int, int) {
	pages := (p.Matches + q.PageSize - 1) / q.PageSize
	if pages < 1 {
		pages = 1
	}
	p.Page = q.Page
	if p.Page > pages {
		p.Page = pages
	}
	p.PageSize = q.PageSize
	start := (p.Page - 1) * q.PageSize
	end := start + q.PageSize
	if end > p.Matches {
		end = p.Matches
	}
	return start, end
}

// BrowseVideos uses SQLite's bounded query path when available. The legacy FS
// backend still scans JSON, but HTML/JSON results remain bounded and identical.
func BrowseVideos(s Store, q VideoQuery) (VideoPage, error) {
	q = normalizeQuery(q)
	if fast, ok := s.(interface {
		BrowseVideos(VideoQuery) (VideoPage, error)
	}); ok {
		return fast.BrowseVideos(q)
	}
	p := VideoPage{Items: []Meta{}, Categories: map[string]int{}}
	all, err := s.List("")
	if err != nil {
		return p, err
	}
	filtered := []Meta{}
	for _, m := range all {
		if q.OnlyLocked && m.AccessList == "" {
			continue
		}
		if m.AccessList != "" && !q.All && !q.AccessLists[m.AccessList] {
			continue
		}
		p.Total++
		if m.Category != "" {
			p.Categories[m.Category]++
		}
		if q.Category != "" && m.Category != q.Category {
			continue
		}
		if q.Text != "" && !strings.Contains(strings.ToLower(m.Title+" "+m.Desc+" "+m.Category), q.Text) {
			continue
		}
		filtered = append(filtered, m)
	}
	if q.Sort == "views" {
		SortByViews(filtered)
	} else if q.Sort == "title" {
		// List is already ordered by date and ID, which break equal-title ties.
		sort.SliceStable(filtered, func(i, j int) bool {
			return compareTitles(filtered[i].Title, filtered[j].Title) < 0
		})
	}
	p.Matches = len(filtered)
	start, end := pageBounds(&p, q)
	p.Items = append(p.Items, filtered[start:end]...)
	return p, nil
}

func (s *SQLite) BrowseVideos(q VideoQuery) (VideoPage, error) {
	q = normalizeQuery(q)
	p := VideoPage{Items: []Meta{}, Categories: map[string]int{}}
	where := "1=1"
	args := []any{}
	if !q.All {
		allowed := []string{}
		for id, granted := range q.AccessLists {
			if granted {
				allowed = append(allowed, id)
			}
		}
		sort.Strings(allowed)
		where = "access_list=''"
		if len(allowed) > 0 {
			where = "(" + where + " OR access_list IN (" + strings.TrimSuffix(strings.Repeat("?,", len(allowed)), ",") + "))"
			for _, id := range allowed {
				args = append(args, id)
			}
		}
	}
	if q.OnlyLocked {
		where += " AND access_list<>''"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if err = tx.QueryRow(`SELECT COUNT(*) FROM videos WHERE `+where, args...).Scan(&p.Total); err != nil {
		return p, err
	}
	rows, err := tx.Query(`SELECT category,COUNT(*) FROM videos WHERE `+where+` AND category<>'' GROUP BY category`, args...)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var category string
		var count int
		if err = rows.Scan(&category, &count); err != nil {
			rows.Close()
			return p, err
		}
		p.Categories[category] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if q.Category != "" {
		where += " AND category=?"
		args = append(args, q.Category)
	}
	if q.Text != "" {
		where += " AND instr(tt_fold(title||' '||description||' '||category),?)>0"
		args = append(args, q.Text)
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM videos WHERE `+where, args...).Scan(&p.Matches); err != nil {
		return p, err
	}
	start, _ := pageBounds(&p, q)
	order := `tt_date(created) DESC,id DESC`
	if q.Sort == "views" {
		order = `views DESC,` + order
	} else if q.Sort == "title" {
		order = `title COLLATE tt_title ASC,` + order
	}
	args = append(args, q.PageSize, start)
	rows, err = tx.Query(`SELECT id,title,description,category,created,original,hls,poster,status,error,access_list,views FROM videos WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var m Meta
		if err = rows.Scan(&m.ID, &m.Title, &m.Desc, &m.Category, &m.Created, &m.Original, &m.HLS, &m.Poster, &m.Status, &m.Error, &m.AccessList, &m.Views); err != nil {
			rows.Close()
			return p, err
		}
		p.Items = append(p.Items, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// CheckMetadata avoids loading the complete video table for readiness probes.
func CheckMetadata(s Store) error {
	if db, ok := s.(*SQLite); ok {
		var one int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='videos'`).Scan(&one); err != nil {
			return err
		}
		if one != 1 {
			return os.ErrNotExist
		}
		return nil
	}
	_, err := s.List("")
	return err
}

// PendingVideos loads only queued/interrupted work on SQLite.
func PendingVideos(s Store, recovering bool) ([]Meta, error) {
	if db, ok := s.(*SQLite); ok {
		status := `status='queued'`
		if recovering {
			status = `status IN ('queued','processing')`
		}
		rows, err := db.db.Query(`SELECT id,title,description,category,created,original,hls,poster,status,error,access_list,views FROM videos WHERE ` + status + ` ORDER BY tt_date(created) DESC,id DESC`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var items []Meta
		for rows.Next() {
			var m Meta
			if err := rows.Scan(&m.ID, &m.Title, &m.Desc, &m.Category, &m.Created, &m.Original, &m.HLS, &m.Poster, &m.Status, &m.Error, &m.AccessList, &m.Views); err != nil {
				return nil, err
			}
			items = append(items, m)
		}
		return items, rows.Err()
	}
	return s.List("")
}
