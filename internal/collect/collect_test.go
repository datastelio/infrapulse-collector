package collect

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/anon"
	"github.com/datastelio/infrapulse-collector/internal/fields"
	"github.com/datastelio/infrapulse-collector/internal/host"
)

// The fixtures in testdata are four consecutive scrapes, 10 s apart, of a
// simulated 8-GPU host: dcgm-exporter text (dcgm_mixed_N.prom), the same host in
// node_exporter format (node_mixed_N.prom), and host snapshots with GPU processes
// (host_snapshots.json). They are synthetic test input, not measurements.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rotating serves prefix_0.prom, prefix_1.prom, … in turn, one per request.
func rotating(t *testing.T, prefix string) func() []byte {
	t.Helper()
	var bodies [][]byte
	for k := 0; k < 4; k++ {
		bodies = append(bodies, fixture(t, fmt.Sprintf("%s_%d.prom", prefix, k)))
	}
	var mu sync.Mutex
	next := 0
	return func() []byte {
		mu.Lock()
		defer mu.Unlock()
		b := bodies[next%len(bodies)]
		next++
		return b
	}
}

func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	dcgm := rotating(t, "dcgm_mixed")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(dcgm())
	}))
}

func TestRunWritesCanonicalCSV(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	meta, err := Run(context.Background(), Config{
		Sources: []string{srv.URL}, Interval: 100 * time.Millisecond, Duration: 350 * time.Millisecond,
		OutDir: dir, Format: "csv", Salt: "s3cret", Version: "test",
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	if meta.GPUs != 8 || meta.Rows < 16 || meta.ScrapesFailed != 0 {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	f, err := os.Open(filepath.Join(dir, meta.SamplesFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	header := recs[0]
	if len(header) != len(fields.Columns()) || header[0] != "ts" {
		t.Fatalf("bad header: %v", header)
	}
	if recs[1][1] == "sim-node" {
		t.Error("host was not pseudonymised")
	}
	if _, err := os.Stat(filepath.Join(dir, "gpu_samples_"+meta.StartedAt.Format("20060102T150405Z")+".meta.json")); err != nil {
		t.Errorf("meta file missing: %v", err)
	}
}

func TestCheckCoverage(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	res := Check(context.Background(), []string{srv.URL}, nil, time.Second)
	if res.GPUs != 8 || !res.HasJobLabel || !res.HasPodLabel {
		t.Fatalf("unexpected check result: %+v", res)
	}
	for col, ok := range res.Coverage {
		if !ok {
			t.Errorf("missing %s", col)
		}
	}
}

func TestScrapeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := Scrape(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("expected error on HTTP 503")
	}
}

func TestRunWritesHostSamplesThatJoin(t *testing.T) {
	dcgm, node := rotating(t, "dcgm_mixed"), rotating(t, "node_mixed")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/node/metrics" {
			w.Write(node())
			return
		}
		w.Write(dcgm())
	}))
	defer srv.Close()
	src, err := NewHostSource(srv.URL+"/node/metrics", "", false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	meta, err := Run(context.Background(), Config{
		Sources: []string{srv.URL}, Interval: 100 * time.Millisecond, Duration: 350 * time.Millisecond,
		OutDir: dir, Format: "csv", Salt: "s3cret", Version: "test", Host: src, HostDesc: "node_exporter",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if meta.HostRows < 2 || meta.HostReadsFailed != 0 || meta.HostNameMismatch {
		t.Fatalf("unexpected host meta: %+v", meta)
	}
	for _, c := range []string{"cpu_busy_pct", "mem_available_bytes", "psi_io_some_pct", "disk_read_bytes_per_s"} {
		if !meta.HostCoverage[c] {
			t.Errorf("host field %s never arrived", c)
		}
	}
	read := func(name string) [][]string {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		recs, err := csv.NewReader(f).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		return recs
	}
	g, h := read(meta.SamplesFile), read(meta.HostSamplesFile)
	if strings.Join(h[0], ",") != strings.Join(fields.HostColumns(), ",") {
		t.Fatalf("bad host header: %v", h[0])
	}
	if h[1][1] == "sim-node" || h[1][1] != g[1][1] {
		t.Errorf("host column must be pseudonymised like gpu_samples: host=%q gpu=%q", h[1][1], g[1][1])
	}
}

func TestHostSourceSpecs(t *testing.T) {
	if s, err := NewHostSource("none", "", true, time.Second); s != nil || err != nil {
		t.Error("none should give no source")
	}
	if _, err := NewHostSource("bogus", "", true, time.Second); err == nil {
		t.Error("expected error for unknown source")
	}
	if s, err := NewHostSource("proc", "x", true, time.Second); s == nil || err != nil {
		t.Error("proc source not created")
	}
}

func TestCheckHost(t *testing.T) {
	var snaps []*host.Snapshot
	if err := json.Unmarshal(fixture(t, "host_snapshots.json"), &snaps); err != nil {
		t.Fatal(err)
	}
	tick := 0
	src := func(context.Context) (*host.Snapshot, error) {
		s := snaps[tick%len(snaps)]
		tick++
		return s, nil
	}
	res := CheckHost(context.Background(), src, 0)
	if res.Err != nil || res.HostName != "sim-node" {
		t.Fatalf("check failed: %+v", res)
	}
	for col, ok := range res.Coverage {
		if !ok {
			t.Errorf("missing %s", col)
		}
	}
}

func TestRedactURL(t *testing.T) {
	h := anon.New("s3cret")
	cases := map[string]bool{ // URL -> host should be replaced
		"http://gpu01.example.net:9400/metrics": true,
		"http://10.1.2.3:9100/metrics":          true,
		"http://127.0.0.1:9400/metrics":         false,
		"http://localhost:9400/metrics":         false,
		"proc":                                  false,
		"none":                                  false,
	}
	for in, replaced := range cases {
		got := RedactURL(h, in)
		if replaced {
			if strings.Contains(got, "gpu01") || strings.Contains(got, "10.1.2.3") || !strings.Contains(got, "src_") {
				t.Errorf("RedactURL(%q) = %q, want the host pseudonymized", in, got)
			}
			if got != RedactURL(h, in) {
				t.Errorf("RedactURL(%q) not stable", in)
			}
		} else if got != in {
			t.Errorf("RedactURL(%q) = %q, want unchanged", in, got)
		}
	}
	if got := RedactURL(nil, "http://gpu01:9400/metrics"); got != "http://gpu01:9400/metrics" {
		t.Errorf("unsalted run changed the URL: %q", got)
	}
	if got := RedactURL(h, "http://gpu01:9400/metrics"); !strings.HasSuffix(got, ":9400/metrics") {
		t.Errorf("port or path lost: %q", got)
	}
}
