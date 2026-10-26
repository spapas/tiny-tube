package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"tiny-tube/store"
)

type reviewFaultStore struct {
	store.Store
	listErr bool
}

func (s reviewFaultStore) Get(id string) (store.Meta, error) {
	if !s.listErr {
		return store.Meta{}, errors.New("storage unavailable")
	}
	return s.Store.Get(id)
}
func (s reviewFaultStore) ListAccessLists() ([]store.AccessList, error) {
	if s.listErr {
		return nil, errors.New("lists unavailable")
	}
	return s.Store.ListAccessLists()
}

func TestMediaReadErrorsAndOrphansStayPrivate(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			list, _ := createLockedList(t, a, "Private")
			id := uploadWithAccessList(t, a, "Private meeting", "", list)
			m, _ := a.fs.Get(id)
			original := a.fs
			defer func() { a.fs = original }()
			a.accessCache = map[string]string{}
			a.fs = reviewFaultStore{Store: original}
			if rec := serve(t, a, "GET", "/videos/"+id+"/"+m.Original); rec.Code != 503 || rec.Body.Len() == len(sampleVideo(t)) {
				t.Fatalf("read failure exposed media: %d", rec.Code)
			}
			a.fs = original
			if err := os.MkdirAll(a.fs.Dir("orphan"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(a.fs.Dir("orphan")+"/original.mp4", sampleVideo(t), 0600); err != nil {
				t.Fatal(err)
			}
			if rec := serve(t, a, "GET", "/videos/orphan/original.mp4"); rec.Code != 404 {
				t.Fatalf("orphan served: %d", rec.Code)
			}
		})
	}
}

type blockedMetadataRead struct {
	store.Store
	once             sync.Once
	entered, release chan struct{}
}

func (s *blockedMetadataRead) Get(id string) (store.Meta, error) {
	m, err := s.Store.Get(id)
	s.once.Do(func() { close(s.entered); <-s.release })
	return m, err
}

func TestVisibilityChangeCannotBeOverwrittenByCacheFill(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			list, _ := createLockedList(t, a, "Private")
			id := uploadWithAccessList(t, a, "Move me", "", "")
			m, _ := a.fs.Get(id)
			original := a.fs
			defer func() { a.fs = original }()
			delete(a.accessCache, id)
			wrapped := &blockedMetadataRead{Store: original, entered: make(chan struct{}), release: make(chan struct{})}
			a.fs = wrapped
			readDone := make(chan struct{})
			go func() { a.mediaAccessList(id); close(readDone) }()
			<-wrapped.entered
			m.AccessList = list
			writeDone := make(chan error, 1)
			go func() { writeDone <- a.putMeta(m) }()
			select {
			case err := <-writeDone:
				close(wrapped.release)
				<-readDone
				t.Fatalf("write bypassed in-flight policy read: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(wrapped.release)
			<-readDone
			if err := <-writeDone; err != nil {
				t.Fatal(err)
			}
			if rec := serve(t, a, "GET", "/videos/"+id+"/"+m.Original); rec.Code != 404 {
				t.Fatalf("stale policy exposed media: %d", rec.Code)
			}
		})
	}
}

type blockedSessionRefresh struct {
	store.Store
	entered, release chan struct{}
}

func (s *blockedSessionRefresh) RefreshSession(previous store.Session, expiresAt string) error {
	close(s.entered)
	<-s.release
	return s.Store.RefreshSession(previous, expiresAt)
}

func TestInFlightRefreshCannotResurrectRevokedSession(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			raw := "old-session"
			previous := store.Session{TokenHash: hashSessionToken(raw), Username: "tester", ExpiresAt: time.Now().Add(sessionTTL - 2*sessionRefreshInterval).UTC().Format(time.RFC3339Nano)}
			if err := a.fs.CreateSession(previous); err != nil {
				t.Fatal(err)
			}
			original := a.fs
			defer func() { a.fs = original }()
			wrapped := &blockedSessionRefresh{Store: original, entered: make(chan struct{}), release: make(chan struct{})}
			a.fs = wrapped
			req := httptest.NewRequest("GET", "/", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
			done := make(chan *store.User, 1)
			go func() { done <- a.validateSession(req) }()
			<-wrapped.entered
			if err := original.DeleteUserSessions("tester"); err != nil {
				t.Fatal(err)
			}
			close(wrapped.release)
			if u := <-done; u != nil {
				t.Fatal("revoked session validated")
			}
			if _, err := original.GetSession(previous.TokenHash); !os.IsNotExist(err) {
				t.Fatalf("session recreated: %v", err)
			}
		})
	}
}

func TestSessionRefreshRenewsBrowserCookieAndCoalescesWrites(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			raw := "sliding-session"
			sess := store.Session{TokenHash: hashSessionToken(raw), Username: "tester", ExpiresAt: time.Now().Add(sessionTTL - 2*sessionRefreshInterval).UTC().Format(time.RFC3339Nano)}
			if err := a.fs.CreateSession(sess); err != nil {
				t.Fatal(err)
			}
			cookie := &http.Cookie{Name: sessionCookieName, Value: raw}
			rec := serve(t, a, "GET", "/", cookie)
			found := false
			for _, c := range rec.Result().Cookies() {
				if c.Name == sessionCookieName && c.MaxAge == int(sessionTTL.Seconds()) {
					found = true
				}
			}
			if !found {
				t.Fatal("browser expiry was not renewed")
			}
			updated, _ := a.fs.GetSession(sess.TokenHash)
			if updated.ExpiresAt == sess.ExpiresAt {
				t.Fatal("storage expiry not renewed")
			}
			rec = serve(t, a, "GET", "/", cookie)
			again, _ := a.fs.GetSession(sess.TokenHash)
			if again.ExpiresAt != updated.ExpiresAt || len(rec.Result().Cookies()) != 0 {
				t.Fatal("fresh session refreshed again")
			}
		})
	}
}

func TestLDAPCannotAdoptLocalAdministratorAndLocalSurvivesOutage(t *testing.T) {
	old := newDirectory
	defer func() { newDirectory = old }()
	newDirectory = func(config) (directory, error) {
		return &fakeDirectory{users: map[string]string{"tester": "directory-password"}}, nil
	}
	a := testApplication(t)
	a.cfg.AuthMode = "ldap"
	if _, err := a.authenticate("tester", "directory-password"); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("LDAP adopted local account: %v", err)
	}
	original, _ := a.fs.GetUser("tester")
	if original.AuthSource != store.AuthLocal || !original.IsAdmin() {
		t.Fatal("local account mutated")
	}
	a.cfg.AuthMode = "both"
	if _, err := a.authenticate("tester", "directory-password"); !errors.Is(err, errInvalidCredentials) {
		t.Fatal("LDAP password authenticated a local identity")
	}
	newDirectory = func(config) (directory, error) { return nil, errors.New("LDAP unavailable") }
	if u, err := a.authenticate("tester", "test-password"); err != nil || !u.IsAdmin() {
		t.Fatalf("local emergency login blocked: %v", err)
	}
}

func TestVisibilityFormFailsClosedAndOmittedEditPreservesPolicy(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			list, _ := createLockedList(t, a, "Private")
			id := uploadWithAccessList(t, a, "Private meeting", "", list)
			cookie := testSession(t, a, "tester")
			original := a.fs
			defer func() { a.fs = original }()
			a.fs = reviewFaultStore{Store: original, listErr: true}
			for _, path := range []string{"/edit?id=" + id, "/upload"} {
				rec := serve(t, a, "GET", path, cookie)
				if rec.Code != 500 || strings.Contains(rec.Body.String(), `<option value="">`) {
					t.Fatalf("unsafe visibility form %s: %d", path, rec.Code)
				}
			}
			a.fs = original
			form := url.Values{"id": {id}, "title": {"Still private"}}
			req := httptest.NewRequest("POST", "/api/edit", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(cookie)
			out := httptest.NewRecorder()
			a.routes().ServeHTTP(out, req)
			m, _ := a.fs.Get(id)
			if out.Code != 303 || m.AccessList != list {
				t.Fatalf("omitted policy was cleared: %d %+v", out.Code, m)
			}
		})
	}
}

func TestViewDebounceHasHardMemoryBound(t *testing.T) {
	a := testApplication(t)
	a.viewOrder = make([]viewEntry, maxViewDebounce)
	now := time.Now()
	for i := 0; i < maxViewDebounce; i++ {
		key := fmt.Sprint(i)
		a.viewSeen[key] = now
		a.viewOrder[i] = viewEntry{key, now}
	}
	id := uploadWithAccessList(t, a, "Count me", "", "")
	req := httptest.NewRequest("GET", "/watch?id="+id, nil)
	a.countView(req, id, 0)
	if len(a.viewSeen) > maxViewDebounce {
		t.Fatalf("map exceeded cap: %d", len(a.viewSeen))
	}
	if _, exists := a.viewSeen["0"]; exists {
		t.Fatal("oldest entry not evicted")
	}
	a.countView(req, id, 0)
	m, _ := a.fs.Get(id)
	if m.Views != 1 {
		t.Fatal("same viewer counted twice")
	}
}

func TestPrivateResponsesAreNotCacheable(t *testing.T) {
	a := testApplication(t)
	list, key := createLockedList(t, a, "Private")
	id := uploadWithAccessList(t, a, "Private meeting", "", list)
	cookie := testSession(t, a, "tester")
	for _, path := range []string{"/", "/api/videos?key=" + key, "/api/video?id=" + id + "&key=" + key, "/admin/keys", "/admin/lists"} {
		rec := serve(t, a, "GET", path, cookie)
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("cacheable private response %s: %d %q", path, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestLockedShareRequiresCapabilityAndEditKeepsGreekValues(t *testing.T) {
	a := testApplication(t)
	list, key := createLockedList(t, a, "Private")
	id := uploadWithAccessList(t, a, "Private meeting", "", list)
	cookie := testSession(t, a, "tester")
	rec := serve(t, a, "GET", "/watch?id="+id, cookie)
	if strings.Contains(rec.Body.String(), "data-copy-link") || !strings.Contains(rec.Body.String(), "original locked-list link") {
		t.Fatal("unusable locked URL offered for copying")
	}
	rec = serve(t, a, "GET", "/watch?id="+id+"&key="+key)
	if !strings.Contains(rec.Body.String(), "data-copy-link") || !strings.Contains(rec.Body.String(), key) {
		t.Fatal("valid capability not shareable")
	}
	title := strings.Repeat("Ω", 300)
	form := url.Values{"id": {id}, "title": {title}, "desc": {"Κείμενο που διατηρείται"}, "access_list": {list}}
	req := httptest.NewRequest("POST", "/api/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	out := httptest.NewRecorder()
	a.routes().ServeHTTP(out, req)
	if out.Code != 400 || !strings.Contains(out.Body.String(), title) || !strings.Contains(out.Body.String(), "edit-error") {
		t.Fatal("edit validation discarded form values")
	}
	m, _ := a.fs.Get(id)
	if m.Title != "Private meeting" || m.AccessList != list {
		t.Fatal("invalid edit persisted")
	}
}

func TestLibraryPaginationIsBoundedAndKeepsFilters(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			for i := 0; i < 55; i++ {
				m := store.Meta{ID: fmt.Sprintf("page-%03d", i), Title: fmt.Sprintf("Training %03d", i), Category: "Category", Status: "ready", Original: "original.mp4", Created: time.Date(2026, 10, 24, 10, 0, i, 0, time.UTC).Format(time.RFC3339Nano)}
				if err := a.putMeta(m); err != nil {
					t.Fatal(err)
				}
			}
			rec := serve(t, a, "GET", "/?q=Training&category=Category&sort=views&page=2")
			if rec.Code != 200 || strings.Count(rec.Body.String(), `class="video-card"`) != 24 || !strings.Contains(rec.Body.String(), `name="sort" value="views"`) {
				t.Fatal("HTML pagination/filter state incorrect")
			}
			rec = serve(t, a, "GET", "/api/videos?page=2&page_size=10")
			body := decodeJSON(t, rec)
			if body["total"] != float64(55) || body["count"] != float64(10) || body["page"] != float64(2) {
				t.Fatalf("API pagination: %v", body)
			}
			for _, query := range []string{"page=-1", "page_size=1000", "page=NaN"} {
				if rec := serve(t, a, "GET", "/api/videos?"+query); rec.Code != 400 {
					t.Fatal("invalid page accepted")
				}
			}
		})
	}
}

func TestLibraryPayloadStaysBoundedWithThousandVideos(t *testing.T) {
	a := testApplicationWithStore(t, "sqlite")
	for i := 0; i < 1000; i++ {
		if err := a.putMeta(store.Meta{ID: fmt.Sprintf("scale-%04d", i), Title: fmt.Sprintf("Training %04d", i), Desc: strings.Repeat("Training details. ", 10), Status: "ready", Created: "2026-10-24T10:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	rec := serve(t, a, "GET", "/")
	if rec.Code != 200 || strings.Count(rec.Body.String(), `class="video-card"`) != 24 || rec.Body.Len() > 100000 {
		t.Fatalf("unbounded library: status=%d bytes=%d", rec.Code, rec.Body.Len())
	}
	t.Logf("1,000-video library: %d bytes HTML, 24 cards", rec.Body.Len())
}
