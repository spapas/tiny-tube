package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAuditLogRecordsMutations(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	admin := loginRequest(t, h, "tester", "test-password")

	r := uploadRequest(t, a, "Audit me", "original.mp4", sampleVideo(t))
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("upload response: %v %s", err, w.Body.String())
	}

	form := url.Values{"id": {created.ID}, "title": {"Audited"}, "desc": {""}, "category": {""}}
	er := httptest.NewRequest(http.MethodPost, "/api/edit", strings.NewReader(form.Encode()))
	er.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	er.AddCookie(admin)
	ew := httptest.NewRecorder()
	h.ServeHTTP(ew, er)
	if ew.Code != http.StatusSeeOther && ew.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", ew.Code, ew.Body.String())
	}

	entries, err := a.fs.ListAudit(50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, e := range entries {
		seen[e.Action] = e.Actor
	}
	if seen["auth.login"] != "tester" || seen["video.upload"] != "tester" || seen["video.edit"] != "tester" {
		t.Fatalf("audit log missed mutations: %+v", seen)
	}

	pw := httptest.NewRecorder()
	pr := httptest.NewRequest(http.MethodGet, "/admin/audit", nil)
	pr.AddCookie(admin)
	h.ServeHTTP(pw, pr)
	if pw.Code != http.StatusOK {
		t.Fatalf("audit page: %d %s", pw.Code, pw.Body.String())
	}
	body := pw.Body.String()
	for _, want := range []string{"video.upload", "video.edit", "tester", "Audited"} {
		if !strings.Contains(body, want) {
			t.Fatalf("audit page missing %q", want)
		}
	}

	uw := httptest.NewRecorder()
	h.ServeHTTP(uw, httptest.NewRequest(http.MethodGet, "/admin/audit", nil))
	if uw.Code != http.StatusSeeOther {
		t.Fatalf("anonymous audit page: %d", uw.Code)
	}
}
