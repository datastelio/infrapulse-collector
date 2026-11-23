// Package collect scrapes one or more dcgm-exporter endpoints on an interval
// and writes canonical gpu_samples rows.
package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/anon"
	"github.com/datastelio/infrapulse-collector/internal/fields"
	"github.com/datastelio/infrapulse-collector/internal/host"
	sample "github.com/datastelio/infrapulse-collector/internal/promsample"
	"github.com/datastelio/infrapulse-collector/internal/promtext"
	"github.com/datastelio/infrapulse-collector/internal/sink"
)

// Config controls a collection run.
type Config struct {
	Sources   []string      // dcgm-exporter /metrics URLs
	Interval  time.Duration // time between scrapes
	Duration  time.Duration // 0 = until cancelled
	OutDir    string
	Format    string // csv or jsonl
	Salt      string // non-empty = pseudonymise identifiers
	JobLabels []string
	Timeout   time.Duration
	Version   string
	// Host metrics: nil = not collected (see NewHostSource).
	Host     HostSource
	HostDesc string // for the meta file: "proc", "none" or the node_exporter URL
}

// Meta is written next to the samples file and describes the run.
type Meta struct {
	CollectorVersion string          `json:"collector_version"`
	StartedAt        time.Time       `json:"started_at"`
	EndedAt          time.Time       `json:"ended_at"`
	Sources          []string        `json:"sources"`
	IntervalSeconds  float64         `json:"interval_seconds"`
	Pseudonymised    bool            `json:"pseudonymised"`
	ScrapesOK        int             `json:"scrapes_ok"`
	ScrapesFailed    int             `json:"scrapes_failed"`
	Rows             int             `json:"rows"`
	GPUs             int             `json:"gpus"`
	Coverage         map[string]bool `json:"coverage"`
	Vendor           string          `json:"vendor,omitempty"` // "amd", "nvidia" or "mixed", from the models seen
	// DCGM47: dcgm-exporter served the DCGM 4.7 metric names (4.9.0 and later);
	// values were converted to the units of the columns. ReasonsDerived:
	// clock_event_reasons was rebuilt from the throttle-seconds counters.
	DCGM47         bool     `json:"dcgm_4_7_names,omitempty"`
	ReasonsDerived bool     `json:"clock_event_reasons_derived,omitempty"`
	UnknownMetrics []string `json:"unknown_metrics"`
	SamplesFile    string   `json:"samples_file"`

	HostSource       string          `json:"host_source"`
	HostSamplesFile  string          `json:"host_samples_file,omitempty"`
	HostRows         int             `json:"host_rows"`
	HostReadsFailed  int             `json:"host_reads_failed"`
	HostCoverage     map[string]bool `json:"host_coverage,omitempty"`
	HostNameMismatch bool            `json:"host_name_mismatch,omitempty"`
}

// HostSource reads one host snapshot.
type HostSource func(ctx context.Context) (*host.Snapshot, error)

// NewHostSource returns a reader for spec: "proc" (read /proc and /sys on this
// machine; procs adds the per-GPU process groups), a node_exporter /metrics URL,
// or "none"/"" (nil source). hostName overrides the host column.
func NewHostSource(spec, hostName string, procs bool, timeout time.Duration) (HostSource, error) {
	switch {
	case spec == "" || spec == "none":
		return nil, nil
	case spec == "proc":
		opt := host.ProcOptions{HostName: host.HostName(hostName), Procs: procs}
		return func(context.Context) (*host.Snapshot, error) { return host.ReadProc(opt) }, nil
	case strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://"):
		client := &http.Client{Timeout: timeout}
		return func(ctx context.Context) (*host.Snapshot, error) {
			ss, err := Scrape(ctx, client, spec)
			if err != nil {
				return nil, err
			}
			return host.FromNodeExporter(ss, hostName, time.Now()), nil
		}, nil
	}
	return nil, fmt.Errorf("unknown host source %q (want proc, none or a node_exporter URL)", spec)
}

// Scrape fetches and parses one exporter endpoint.
func Scrape(ctx context.Context, client *http.Client, src string) ([]promtext.Sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("%s: HTTP %d", src, resp.StatusCode)
	}
	return promtext.Parse(io.LimitReader(resp.Body, 64<<20))
}

func hostOf(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return src
	}
	return u.Hostname()
}

// ScrapeAll scrapes every source once and returns the combined rows.
func ScrapeAll(ctx context.Context, client *http.Client, sources []string, opt sample.Options, now time.Time) ([]sample.Row, []string, []error) {
	var rows []sample.Row
	unknownSet := map[string]struct{}{}
	var errs []error
	for _, src := range sources {
		ss, err := Scrape(ctx, client, src)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		o := opt
		o.DefaultHost = hostOf(src)
		r, unk := sample.Build(now, ss, o)
		rows = append(rows, r...)
		for _, u := range unk {
			unknownSet[u] = struct{}{}
		}
	}
	unknown := make([]string, 0, len(unknownSet))
	for u := range unknownSet {
		unknown = append(unknown, u)
	}
	sort.Strings(unknown)
	return rows, unknown, errs
}

// RedactURL pseudonymizes the host of a source URL for the meta file, so a
// salted collection doesn't name the scraped machines (http://gpu01:9400/metrics
// becomes http://src_<hash>:9400/metrics). Loopback hosts, values that are not
// URLs ("proc", "none") and unsalted runs are returned unchanged.
func RedactURL(h *anon.Hasher, raw string) string {
	if h == nil {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	name := u.Hostname()
	if name == "localhost" || name == "127.0.0.1" || name == "::1" {
		return raw
	}
	u.User = nil
	if port := u.Port(); port != "" {
		u.Host = h.Hash("src", name) + ":" + port
	} else {
		u.Host = h.Hash("src", name)
	}
	return u.String()
}

// Run collects until the duration elapses or ctx is cancelled.
func Run(ctx context.Context, cfg Config, log *slog.Logger) (*Meta, error) {
	if len(cfg.Sources) == 0 {
		return nil, fmt.Errorf("no --source given")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return nil, err
	}
	start := time.Now().UTC()
	name := fmt.Sprintf("gpu_samples_%s.%s", start.Format("20060102T150405Z"), cfg.Format)
	path := filepath.Join(cfg.OutDir, name)
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	w, err := sink.New(cfg.Format, f)
	if err != nil {
		return nil, err
	}
	var hw sink.HostWriter
	hostName := ""
	if cfg.Host != nil {
		hostName = "host_samples_" + strings.TrimPrefix(name, "gpu_samples_")
		hf, err := os.Create(filepath.Join(cfg.OutDir, hostName))
		if err != nil {
			return nil, err
		}
		defer hf.Close()
		if hw, err = sink.NewHost(cfg.Format, hf); err != nil {
			return nil, err
		}
	}
	opt := sample.Options{JobLabels: cfg.JobLabels, Hasher: anon.New(cfg.Salt)}
	client := &http.Client{Timeout: cfg.Timeout}
	metaSources := make([]string, len(cfg.Sources))
	for i, s := range cfg.Sources {
		metaSources[i] = RedactURL(opt.Hasher, s)
	}
	meta := &Meta{CollectorVersion: cfg.Version, StartedAt: start, Sources: metaSources,
		IntervalSeconds: cfg.Interval.Seconds(), Pseudonymised: cfg.Salt != "", SamplesFile: name,
		Coverage: map[string]bool{}, HostSource: RedactURL(opt.Hasher, cfg.HostDesc), HostSamplesFile: hostName}
	for _, fd := range fields.Catalog {
		meta.Coverage[fd.Column] = false
	}
	if cfg.Host != nil {
		meta.HostCoverage = map[string]bool{}
		for _, fd := range fields.HostCatalog {
			meta.HostCoverage[fd.Column] = false
		}
	}
	var prevHost *host.Snapshot
	gpuHosts, hostHosts := map[string]bool{}, map[string]bool{}
	gpus := map[string]struct{}{}
	unknown := map[string]struct{}{}

	if cfg.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Duration)
		defer cancel()
	}
	tick := time.NewTicker(cfg.Interval)
	defer tick.Stop()

	var reasons sample.ReasonDeriver
	scrape := func(now time.Time) {
		sctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		defer cancel()
		rows, unk, errs := ScrapeAll(sctx, client, cfg.Sources, opt, now)
		reasons.Apply(rows)
		for _, r := range rows {
			meta.DCGM47 = meta.DCGM47 || r.DCGM47
		}
		meta.ReasonsDerived = reasons.Derived > 0
		for _, e := range errs {
			meta.ScrapesFailed++
			log.Warn("scrape failed", "err", e)
		}
		meta.ScrapesOK += len(cfg.Sources) - len(errs)
		if cfg.Host != nil {
			collectHost(sctx, cfg.Host, &prevHost, hw, opt.Hasher, meta, hostHosts, log)
		}
		for _, r := range rows {
			gpuHosts[r.Identity["host"]] = true
		}
		if len(rows) == 0 {
			return
		}
		if err := w.Write(rows); err != nil {
			log.Error("write failed", "err", err)
			return
		}
		if err := w.Flush(); err != nil {
			log.Error("flush failed", "err", err)
		}
		meta.Rows += len(rows)
		for _, r := range rows {
			gpus[r.Identity["host"]+"/"+r.Identity["gpu_index"]] = struct{}{}
			if v := fields.Vendor(r.Identity["model"]); meta.Vendor == "" {
				meta.Vendor = v
			} else if v != "" && v != meta.Vendor {
				meta.Vendor = "mixed"
			}
			for c := range r.Values {
				meta.Coverage[c] = true
			}
		}
		for _, u := range unk {
			unknown[u] = struct{}{}
		}
	}

	log.Info("collecting", "sources", strings.Join(cfg.Sources, ","), "interval", cfg.Interval, "out", path)
	scrape(time.Now())
	for {
		select {
		case <-ctx.Done():
			meta.EndedAt = time.Now().UTC()
			meta.GPUs = len(gpus)
			for u := range unknown {
				meta.UnknownMetrics = append(meta.UnknownMetrics, u)
			}
			sort.Strings(meta.UnknownMetrics)
			meta.HostNameMismatch = hostMismatch(gpuHosts, hostHosts)
			if meta.HostNameMismatch {
				log.Warn("host_samples host name matches no dcgm-exporter Hostname; set --host-name so the files join",
					"gpu_hosts", keys(gpuHosts), "host_hosts", keys(hostHosts))
			}
			if err := writeMeta(cfg.OutDir, name, meta); err != nil {
				return meta, err
			}
			log.Info("done", "rows", meta.Rows, "gpus", meta.GPUs, "scrapes_ok", meta.ScrapesOK, "scrapes_failed", meta.ScrapesFailed)
			return meta, nil
		case now := <-tick.C:
			scrape(now)
		}
	}
}

func collectHost(ctx context.Context, src HostSource, prev **host.Snapshot, w sink.HostWriter, h *anon.Hasher,
	meta *Meta, seen map[string]bool, log *slog.Logger) {
	snap, err := src(ctx)
	if err != nil {
		meta.HostReadsFailed++
		log.Warn("host read failed", "err", err)
		return
	}
	if *prev != nil {
		rows := host.Rates(*prev, snap, h)
		if err := w.Write(rows); err != nil {
			log.Error("host write failed", "err", err)
		} else if err := w.Flush(); err != nil {
			log.Error("host flush failed", "err", err)
		}
		meta.HostRows += len(rows)
		for _, r := range rows {
			seen[r.Host] = true
			for c := range r.Values {
				meta.HostCoverage[c] = true
			}
		}
	}
	*prev = snap
}

func hostMismatch(gpuHosts, hostHosts map[string]bool) bool {
	if len(gpuHosts) == 0 || len(hostHosts) == 0 {
		return false
	}
	for h := range hostHosts {
		if gpuHosts[h] {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeMeta(dir, samplesName string, m *Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	base := strings.TrimSuffix(samplesName, filepath.Ext(samplesName))
	return os.WriteFile(filepath.Join(dir, base+".meta.json"), append(b, '\n'), 0o644)
}

// CheckResult reports field coverage for one scrape.
type CheckResult struct {
	GPUs        int
	Coverage    map[string]bool
	Unknown     []string
	HasPodLabel bool
	HasJobLabel bool
	Hosts       []string // Hostname labels seen
	// DCGM47 is set when dcgm-exporter served the DCGM 4.7 metric names (4.9.0
	// and later). It cannot serve the throttle-reasons bitmask; ReasonsDerivable
	// says the throttle-seconds counters that replace it arrived.
	DCGM47           bool
	ReasonsDerivable bool
	// Vendor is "amd" when every GPU came from the AMD Device Metrics Exporter,
	// "nvidia" when every one came from dcgm-exporter, else "mixed" or "".
	Vendor string
	Errors []error
}

// Check scrapes every source once and reports which catalog fields arrived.
func Check(ctx context.Context, sources []string, jobLabels []string, timeout time.Duration) CheckResult {
	client := &http.Client{Timeout: timeout}
	rows, unk, errs := ScrapeAll(ctx, client, sources, sample.Options{JobLabels: jobLabels}, time.Now())
	res := CheckResult{GPUs: len(rows), Coverage: sample.Coverage(rows), Unknown: unk, Errors: errs}
	res.ReasonsDerivable = res.Coverage["power_violation_us"] || res.Coverage["thermal_violation_us"]
	hosts := map[string]bool{}
	vendors := map[string]bool{}
	for _, r := range rows {
		res.DCGM47 = res.DCGM47 || r.DCGM47
		vendors[fields.Vendor(r.Identity["model"])] = true
		hosts[r.Identity["host"]] = true
		if r.Identity["pod"] != "" {
			res.HasPodLabel = true
		}
		if r.Identity["job_id"] != "" {
			res.HasJobLabel = true
		}
	}
	res.Hosts = keys(hosts)
	if len(vendors) == 1 {
		res.Vendor = keys(vendors)[0]
	} else if len(vendors) > 1 {
		res.Vendor = "mixed"
	}
	return res
}

// CheckHostResult reports host field coverage from two readings.
type CheckHostResult struct {
	Rows     []host.Row
	Coverage map[string]bool
	HostName string // as reported by the source
	Err      error
}

// CheckHost reads the host twice, gap apart (rates need two readings).
func CheckHost(ctx context.Context, src HostSource, gap time.Duration) CheckHostResult {
	a, err := src(ctx)
	if err != nil {
		return CheckHostResult{Err: err}
	}
	time.Sleep(gap)
	b, err := src(ctx)
	if err != nil {
		return CheckHostResult{Err: err}
	}
	rows := host.Rates(a, b, nil)
	return CheckHostResult{Rows: rows, Coverage: host.Coverage(rows), HostName: b.Host}
}
