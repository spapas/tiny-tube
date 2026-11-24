package main

// Prometheus exposition without the client library: Tiny Tube stays a single
// CGO-free binary with no extra module. Instruments are created once at
// startup, so label sets stay bounded, and scrapes read a snapshot.
//
//	METRICS_ADDR=127.0.0.1:9090   ->   GET http://127.0.0.1:9090/metrics
//
// The endpoint has its own listener. The public mux (and the Nginx sample,
// which proxies every path) must never expose it. Labels use ServeMux
// patterns, never request URLs, so locked-list capability keys (?key=lt-...)
// and per-video HLS paths cannot leak into time series.

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	metricsPath        = "/metrics"
	metricsContentType = "text/plain; version=0.0.4; charset=utf-8"
	// storageWalkInterval keeps a full VIDEO_DIR walk off the scrape path.
	storageWalkInterval = time.Minute
	// unmatchedRoute labels requests that no ServeMux pattern handled.
	unmatchedRoute = "unmatched"
)

// Latencies span HLS segment reads and long original downloads.
var httpBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 2.5, 5, 15, 60, 300}

type metricKind string

const (
	kindCounter   metricKind = "counter"
	kindGauge     metricKind = "gauge"
	kindHistogram metricKind = "histogram"
)

type metrics struct {
	all    []*metric
	byName map[string]*metric

	httpRequests      *metric
	httpDuration      *metric
	httpBytes         *metric
	authThrottled     *metric
	transcodes        *metric
	transcodeActive   *metric
	uploadBytes       *metric
	views             *metric
	backupFailures    *metric
	backupLastSuccess *metric
	videos            *metric
	storageBytes      *metric
	diskFreeBytes     *metric
	storeErrors       *metric
	buildInfo         *metric
	startTime         *metric
	goroutines        *metric
	heapBytes         *metric
}

type metric struct {
	name, help string
	kind       metricKind
	labels     []string
	buckets    []float64

	mu     sync.Mutex
	series map[string]*metricSeries
}

type metricSeries struct {
	labelValues []string
	value       float64  // counter/gauge value, or histogram sum
	hits        []uint64 // histogram bucket counts, cumulative
	count       uint64   // histogram observations
}

func newMetrics() *metrics {
	m := &metrics{byName: map[string]*metric{}}
	m.httpRequests = m.counter("tiny_tube_http_requests_total", "HTTP requests served, by route pattern and response status.", "route", "method", "code")
	m.httpDuration = m.histogram("tiny_tube_http_request_duration_seconds", "HTTP request latency, by route pattern.", httpBuckets, "route")
	m.httpBytes = m.counter("tiny_tube_http_response_bytes_total", "Response body bytes written, by route pattern.", "route")
	m.authThrottled = m.counter("tiny_tube_auth_throttled_total", "Protected requests rejected by the per-IP limiter.")
	m.transcodes = m.counter("tiny_tube_transcode_jobs_total", "Finished conversion jobs, by outcome.", "result")
	m.transcodeActive = m.gauge("tiny_tube_transcode_jobs_active", "Conversions currently running.")
	m.uploadBytes = m.counter("tiny_tube_upload_bytes_total", "Upload payload bytes accepted.")
	m.views = m.counter("tiny_tube_views_total", "Video views counted after the per-viewer debounce.")
	m.backupFailures = m.counter("tiny_tube_backup_failures_total", "Automatic backup runs that failed.")
	m.backupLastSuccess = m.gauge("tiny_tube_backup_last_success_timestamp_seconds", "Unix time of the last successful automatic backup.")
	m.videos = m.gauge("tiny_tube_videos", "Stored videos, by conversion status.", "status")
	m.storageBytes = m.gauge("tiny_tube_storage_bytes", "Bytes used under VIDEO_DIR.")
	m.diskFreeBytes = m.gauge("tiny_tube_disk_free_bytes", "Free bytes on the filesystem holding VIDEO_DIR.")
	m.storeErrors = m.counter("tiny_tube_store_errors_total", "Metadata store failures, by operation.", "op")
	m.buildInfo = m.gauge("tiny_tube_build_info", "Build version; always 1.", "version")
	m.startTime = m.gauge("tiny_tube_start_time_seconds", "Unix time the process started.")
	m.goroutines = m.gauge("tiny_tube_goroutines", "Live goroutines.")
	m.heapBytes = m.gauge("tiny_tube_heap_bytes", "Go heap bytes, by kind.", "kind")
	m.buildInfo.set(1, appVersion)
	m.startTime.set(float64(time.Now().Unix()))
	return m
}

func (m *metrics) counter(name, help string, labels ...string) *metric {
	return m.register(name, help, kindCounter, nil, labels)
}

func (m *metrics) gauge(name, help string, labels ...string) *metric {
	return m.register(name, help, kindGauge, nil, labels)
}

func (m *metrics) histogram(name, help string, buckets []float64, labels ...string) *metric {
	return m.register(name, help, kindHistogram, buckets, labels)
}

func (m *metrics) register(name, help string, kind metricKind, buckets []float64, labels []string) *metric {
	if _, exists := m.byName[name]; exists {
		panic("duplicate metric " + name)
	}
	inst := &metric{name: name, help: help, kind: kind, labels: labels, buckets: buckets, series: map[string]*metricSeries{}}
	m.byName[name] = inst
	m.all = append(m.all, inst)
	return inst
}

func (m *metric) inc(labelValues ...string) {
	m.add(1, labelValues...)
}

func (m *metric) add(delta float64, labelValues ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seriesFor(labelValues).value += delta
}

func (m *metric) set(value float64, labelValues ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seriesFor(labelValues).value = value
}

func (m *metric) observe(value float64, labelValues ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.seriesFor(labelValues)
	s.value += value
	s.count++
	for i, bound := range m.buckets {
		if value <= bound {
			s.hits[i]++
		}
	}
}

// seriesFor must be called with m.mu held. The keys are label values joined,
// which cannot collide because no label value contains the separator.
func (m *metric) seriesFor(labelValues []string) *metricSeries {
	if len(labelValues) != len(m.labels) {
		panic(fmt.Sprintf("metric %s: got %d label values, want %d", m.name, len(labelValues), len(m.labels)))
	}
	key := strings.Join(labelValues, "\x1f")
	if s, ok := m.series[key]; ok {
		return s
	}
	s := &metricSeries{labelValues: append([]string(nil), labelValues...)}
	if m.kind == kindHistogram {
		s.hits = make([]uint64, len(m.buckets))
	}
	m.series[key] = s
	return s
}

func (m *metrics) write(w io.Writer) error {
	var b strings.Builder
	for _, inst := range m.all {
		b.WriteString("# HELP " + inst.name + " " + inst.help + "\n")
		b.WriteString("# TYPE " + inst.name + " " + string(inst.kind) + "\n")
		for _, line := range inst.lines() {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// lines renders one instrument. Series are sorted so repeated scrapes and
// tests see a stable order.
func (m *metric) lines() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.series))
	for key := range m.series {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		s := m.series[key]
		base := make([]labelPair, 0, len(m.labels))
		for i, name := range m.labels {
			base = append(base, labelPair{name, s.labelValues[i]})
		}
		if m.kind != kindHistogram {
			out = append(out, sampleLine(m.name, base, labelPair{}, s.value))
			continue
		}
		for i, bound := range m.buckets {
			out = append(out, sampleLine(m.name+"_bucket", base, labelPair{"le", formatMetricValue(bound)}, float64(s.hits[i])))
		}
		out = append(out, sampleLine(m.name+"_bucket", base, labelPair{"le", "+Inf"}, float64(s.count)))
		out = append(out, sampleLine(m.name+"_sum", base, labelPair{}, s.value))
		out = append(out, sampleLine(m.name+"_count", base, labelPair{}, float64(s.count)))
	}
	return out
}

type labelPair struct{ name, value string }

// escapeLabel covers the three sequences the exposition format requires.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func sampleLine(name string, labels []labelPair, extra labelPair, value float64) string {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 || extra.name != "" {
		b.WriteString("{")
		for i, pair := range labels {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(pair.name + `="` + labelEscaper.Replace(pair.value) + `"`)
		}
		if extra.name != "" {
			if len(labels) > 0 {
				b.WriteString(",")
			}
			b.WriteString(extra.name + `="` + labelEscaper.Replace(extra.value) + `"`)
		}
		b.WriteString("}")
	}
	b.WriteString(" ")
	b.WriteString(formatMetricValue(value))
	return b.String()
}

func formatMetricValue(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// statusWriter records the response status for the request counter while
// staying transparent to the handler.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController and http.ServeContent reach the
// original writer: upload read deadlines and media streaming depend on it.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// ReadFrom keeps zero-copy media serving. Without it io.Copy falls back to
// buffered Write calls and sendfile is lost.
func (w *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	var n int64
	var err error
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(r)
	} else {
		n, err = io.Copy(w.ResponseWriter, r)
	}
	w.written += n
	return n, err
}

// withMetrics sits above the mux but below language negotiation, which
// replaces the request. That is still outside authentication, so throttled,
// rejected and unmatched requests are counted.
func (a *application) withMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		tracked := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(tracked, r)
		route := r.Pattern
		if route == "" {
			route = unmatchedRoute
		}
		status := tracked.status
		if status == 0 {
			status = http.StatusOK
		}
		a.metrics.httpRequests.add(1, route, r.Method, strconv.Itoa(status))
		a.metrics.httpDuration.observe(time.Since(start).Seconds(), route)
		a.metrics.httpBytes.add(float64(tracked.written), route)
	})
}

// writeMetrics serves the text exposition on the metrics listener.
func (a *application) writeMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != metricsPath {
		http.NotFound(w, r)
		return
	}
	a.snapshotRuntimeMetrics()
	w.Header().Set("Content-Type", metricsContentType)
	w.Header().Set("Cache-Control", "no-store")
	if err := a.metrics.write(w); err != nil {
		log.Printf("write metrics: %v", err)
	}
}

// snapshotRuntimeMetrics reads live state at scrape time, so no separate
// bookkeeping can drift from the source of truth.
func (a *application) snapshotRuntimeMetrics() {
	a.claimMu.Lock()
	active := len(a.activeJobs)
	a.claimMu.Unlock()
	a.metrics.transcodeActive.set(float64(active))
	a.metrics.goroutines.set(float64(runtime.NumGoroutine()))
	// ReadMemStats stops the world briefly; scrapes are infrequent.
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	a.metrics.heapBytes.set(float64(mem.HeapAlloc), "alloc")
	a.metrics.heapBytes.set(float64(mem.HeapSys), "sys")
}

func (a *application) metricsServer() *http.Server {
	if a.cfg.MetricsAddr == "" {
		return nil
	}
	return &http.Server{Addr: a.cfg.MetricsAddr, Handler: http.HandlerFunc(a.writeMetrics),
		ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
}

// startMetricsRefresh keeps storage gauges current without walking the video
// tree on every scrape.
func (a *application) startMetricsRefresh(ctx context.Context) {
	a.refreshStorageMetrics()
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		ticker := time.NewTicker(storageWalkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.refreshStorageMetrics()
			}
		}
	}()
}

func (a *application) refreshStorageMetrics() {
	if used, err := dirUsage(a.videoDir); err != nil {
		log.Printf("measure storage for metrics: %v", err)
	} else {
		a.metrics.storageBytes.set(float64(used))
	}
	if free, err := availableDiskBytes(a.videoDir); err == nil {
		a.metrics.diskFreeBytes.set(float64(free))
	}
	items, err := a.fs.List("")
	if err != nil {
		a.metrics.storeErrors.inc("list")
		log.Printf("list videos for metrics: %v", err)
		return
	}
	counts := map[string]int{}
	for _, m := range items {
		counts[m.Status]++
	}
	for _, status := range []string{"ready", "queued", "processing", "failed"} {
		a.metrics.videos.set(float64(counts[status]), status)
	}
}
