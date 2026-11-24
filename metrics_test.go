package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tiny-tube/store"
)

func scrapeMetrics(t *testing.T, a *application) string {
	t.Helper()
	w := httptest.NewRecorder()
	a.writeMetrics(w, httptest.NewRequest(http.MethodGet, metricsPath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("scrape: %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != metricsContentType {
		t.Fatalf("content type: %q", got)
	}
	return w.Body.String()
}

func assertMetric(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("missing %q in:\n%s", want, body)
	}
}

// metricSample finds a series line, ignoring # HELP/# TYPE headers that also
// contain the metric name.
func metricSample(body, name string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name) {
			return line, true
		}
	}
	return "", false
}

// Route labels come from ServeMux patterns. Capability keys and per-video
// paths must never reach the exposition.
func TestMetricsUsePatternsNotURLs(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing?key=lt-secret", nil))

	body := scrapeMetrics(t, a)
	assertMetric(t, body, `tiny_tube_http_requests_total{route="GET /healthz",method="GET",code="200"} 1`)
	assertMetric(t, body, `tiny_tube_http_requests_total{route="unmatched",method="GET",code="404"} 1`)
	assertMetric(t, body, `tiny_tube_http_request_duration_seconds_count{route="GET /healthz"} 1`)
	assertMetric(t, body, "# TYPE tiny_tube_http_request_duration_seconds histogram")
	// healthz writes "ok".
	assertMetric(t, body, `tiny_tube_http_response_bytes_total{route="GET /healthz"} 2`)
	if strings.Contains(body, "lt-secret") {
		t.Fatalf("URL leaked into metrics:\n%s", body)
	}
}

// The metrics endpoint lives only on its own listener.
func TestMetricsNotServedOnPublicMux(t *testing.T) {
	a := testApplication(t)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, metricsPath, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("public %s: %d", metricsPath, w.Code)
	}
}

func TestMetricsServerNeedsAddress(t *testing.T) {
	a := testApplication(t)
	if a.metricsServer() != nil {
		t.Fatal("metrics listener configured without METRICS_ADDR")
	}
	a.cfg.MetricsAddr = "127.0.0.1:0"
	srv := a.metricsServer()
	if srv == nil {
		t.Fatal("no metrics server")
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, metricsPath, http.StatusOK},
		{http.MethodGet, "/other", http.StatusNotFound},
		{http.MethodPost, metricsPath, http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

func TestMetricsCountThrottledRequests(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthRequestsPerMinute = 1
	a, err := newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.close() })
	h := a.routes()
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=x&password=y"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if i == 1 && w.Code != http.StatusTooManyRequests {
			t.Fatalf("second login: %d", w.Code)
		}
	}
	assertMetric(t, scrapeMetrics(t, a), "tiny_tube_auth_throttled_total 1")
}

func TestMetricsCountUploadBytes(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	admin := testSession(t, a, "tester")
	id := startChunkedUpload(t, h, admin, "clip.mp4", 4)

	r := httptest.NewRequest(http.MethodPost, "/api/uploads/"+id+"/chunk?offset=0", bytes.NewReader([]byte("data")))
	r.AddCookie(admin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("chunk: %d %s", w.Code, w.Body.String())
	}
	assertMetric(t, scrapeMetrics(t, a), "tiny_tube_upload_bytes_total 4")
}

func TestMetricsCountViews(t *testing.T) {
	a := testApplication(t)
	id := uploadWithAccessList(t, a, "Counted", "Cat", "")
	if rec := watchAs(t, a, "/watch?id="+id, "10.0.0.1:1234"); rec.Code != http.StatusOK {
		t.Fatalf("watch: %d", rec.Code)
	}
	assertMetric(t, scrapeMetrics(t, a), "tiny_tube_views_total 1")
}

// Conversion outcomes come from the persisted status: ready, failed and the
// canceled/queued case that resumes after restart.
func TestMetricsCountConversionOutcomes(t *testing.T) {
	a := testApplication(t)
	a.ffmpegBin = "fake"
	a.runFFmpeg = func(ctx context.Context, bin string, args []string) error {
		for _, arg := range args {
			if strings.Contains(arg, "bad-") {
				return errors.New("encoder failed")
			}
		}
		return fakeHLS(ctx, bin, args)
	}
	queueVideo(t, a, "good-1")
	queueVideo(t, a, "bad-1")
	startTestWorkers(t, a)
	waitStatus(t, a, "good-1", "ready")
	waitStatus(t, a, "bad-1", "failed")

	body := scrapeMetrics(t, a)
	assertMetric(t, body, `tiny_tube_transcode_jobs_total{result="ready"} 1`)
	assertMetric(t, body, `tiny_tube_transcode_jobs_total{result="failed"} 1`)
	assertMetric(t, body, "tiny_tube_transcode_jobs_active 0")
}

func TestMetricsReportActiveConversions(t *testing.T) {
	a := testApplication(t)
	a.ffmpegBin = "fake"
	release := make(chan struct{})
	a.runFFmpeg = func(ctx context.Context, bin string, args []string) error {
		<-release
		return fakeHLS(ctx, bin, args)
	}
	queueVideo(t, a, "busy")
	startTestWorkers(t, a)
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.claimMu.Lock()
		active := len(a.activeJobs)
		a.claimMu.Unlock()
		if active == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("conversion was not claimed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertMetric(t, scrapeMetrics(t, a), "tiny_tube_transcode_jobs_active 1")
	close(release)
	waitStatus(t, a, "busy", "ready")
	assertMetric(t, scrapeMetrics(t, a), "tiny_tube_transcode_jobs_active 0")
}

func TestMetricsBackupHealth(t *testing.T) {
	a := testApplication(t)
	a.cfg.BackupDir = t.TempDir()
	if err := a.runBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := scrapeMetrics(t, a)
	assertMetric(t, body, "tiny_tube_backup_last_success_timestamp_seconds ")
	if _, ok := metricSample(body, "tiny_tube_backup_failures_total"); ok {
		t.Fatalf("failure counter present without failures:\n%s", body)
	}

	// A file where the backup root must be fails the walk.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.cfg.BackupDir = filepath.Join(blocker, "backups")
	if err := a.runBackup(context.Background()); err == nil {
		t.Fatal("backup into a file path succeeded")
	}
	assertMetric(t, scrapeMetrics(t, a), "tiny_tube_backup_failures_total 1")
}

func TestMetricsStorageAndRuntimeGauges(t *testing.T) {
	a := testApplication(t)
	a.refreshStorageMetrics()
	body := scrapeMetrics(t, a)
	for _, status := range []string{"ready", "queued", "processing", "failed"} {
		assertMetric(t, body, `tiny_tube_videos{status="`+status+`"} `)
	}
	for _, want := range []string{
		"tiny_tube_storage_bytes ",
		"tiny_tube_disk_free_bytes ",
		"tiny_tube_goroutines ",
		`tiny_tube_heap_bytes{kind="alloc"} `,
		`tiny_tube_heap_bytes{kind="sys"} `,
		"tiny_tube_start_time_seconds ",
		`tiny_tube_build_info{version="` + appVersion + `"} 1`,
	} {
		assertMetric(t, body, want)
	}
}

func TestMetricsHistogramBucketsCumulative(t *testing.T) {
	m := newMetrics()
	hist := m.histogram("test_seconds", "test", []float64{0.5, 1}, "kind")
	hist.observe(0.5, "a")
	hist.observe(1, "a")
	hist.observe(2, "a")

	var b strings.Builder
	if err := m.write(&b); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{
		`test_seconds_bucket{kind="a",le="0.5"} 1`,
		`test_seconds_bucket{kind="a",le="1"} 2`,
		`test_seconds_bucket{kind="a",le="+Inf"} 3`,
		`test_seconds_count{kind="a"} 3`,
		`test_seconds_sum{kind="a"} 3.5`,
	} {
		assertMetric(t, body, want)
	}
}

func TestMetricsRegistryRejectsMisuse(t *testing.T) {
	assertPanics := func(t *testing.T, name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		fn()
	}
	m := newMetrics()
	assertPanics(t, "duplicate name", func() { m.counter("tiny_tube_views_total", "dup") })
	assertPanics(t, "label count", func() { m.views.inc("nope") })
}

// The wrapper must stay transparent to http.ResponseController; upload read
// deadlines depend on reaching the real writer through Unwrap.
func TestStatusWriterPassesDeadlinesThrough(t *testing.T) {
	base := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	tracked := &statusWriter{ResponseWriter: base}
	want := time.Unix(1234, 0)
	if err := http.NewResponseController(tracked).SetReadDeadline(want); err != nil {
		t.Fatal(err)
	}
	if !base.deadline.Equal(want) {
		t.Fatalf("deadline not forwarded: %v", base.deadline)
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	d.deadline = t
	return nil
}

// io.Copy through the recorder uses ReadFrom, which must still mark the
// status, count bytes and reach the original writer (sendfile for media).
func TestStatusWriterReadFrom(t *testing.T) {
	rec := httptest.NewRecorder()
	tracked := &statusWriter{ResponseWriter: rec}
	if _, err := io.Copy(tracked, strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	if tracked.status != http.StatusOK {
		t.Fatalf("status: %d", tracked.status)
	}
	if tracked.written != int64(len("payload")) {
		t.Fatalf("written: %d", tracked.written)
	}
	if rec.Body.String() != "payload" {
		t.Fatalf("body: %q", rec.Body.String())
	}
	if tracked.Unwrap() != http.ResponseWriter(rec) {
		t.Fatal("Unwrap does not return the original writer")
	}
}

func TestMetricsConcurrentUse(t *testing.T) {
	m := newMetrics()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.httpRequests.add(1, "GET /x", "GET", "200")
				m.httpDuration.observe(0.001, "GET /x")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		var b strings.Builder
		for i := 0; i < 10; i++ {
			if err := m.write(&b); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()

	var b strings.Builder
	if err := m.write(&b); err != nil {
		t.Fatal(err)
	}
	assertMetric(t, b.String(), `tiny_tube_http_requests_total{route="GET /x",method="GET",code="200"} 800`)
}

// Store failures surface only in logs on background paths (conversion status
// writes, view counting), so they get their own counter.
func TestMetricsCountStoreFailures(t *testing.T) {
	a := testApplication(t)
	good := a.fs
	defer func() { a.fs = good }()

	a.fs = putFails{Store: good}
	if err := a.putMeta(store.Meta{ID: "x", Title: "t"}); err == nil {
		t.Fatal("put succeeded")
	}
	a.fs = deleteFails{Store: good}
	if err := a.deleteMeta("x"); err == nil {
		t.Fatal("delete succeeded")
	}
	a.fs = viewsFail{Store: good}
	a.countView(httptest.NewRequest(http.MethodGet, "/watch?id=x", nil), "x", 0)
	a.fs = listFails{Store: good}
	a.refreshStorageMetrics()

	body := scrapeMetrics(t, a)
	for _, op := range []string{"put", "delete", "views", "list"} {
		assertMetric(t, body, `tiny_tube_store_errors_total{op="`+op+`"} 1`)
	}
}

var errBrokenStore = errors.New("store unavailable")

type putFails struct{ store.Store }

func (putFails) Put(store.Meta) error { return errBrokenStore }

type deleteFails struct{ store.Store }

func (deleteFails) Delete(string) error { return errBrokenStore }

type viewsFail struct{ store.Store }

func (viewsFail) IncrementViews(string) (int64, error) { return 0, errBrokenStore }

type listFails struct{ store.Store }

func (listFails) List(string) ([]store.Meta, error) { return nil, errBrokenStore }

func TestMetricsEscapeLabelValues(t *testing.T) {
	got := sampleLine("x", []labelPair{{"route", "a\"b\\c\nd"}}, labelPair{}, 1)
	want := `x{route="a\"b\\c\nd"} 1`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
