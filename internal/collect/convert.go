package collect

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/anon"
	"github.com/datastelio/infrapulse-collector/internal/fields"
	sample "github.com/datastelio/infrapulse-collector/internal/promsample"
	"github.com/datastelio/infrapulse-collector/internal/promtext"
	"github.com/datastelio/infrapulse-collector/internal/sink"
)

// ConvertConfig controls the conversion of metrics exported from a
// Prometheus-compatible store into a gpu_samples file.
type ConvertConfig struct {
	// Inputs are export folders (the files directly inside are read) or files:
	// Prometheus HTTP API results (.json, as infrapulse-export.sh writes) or
	// `promtool tsdb dump` text, plain or .gz.
	Inputs    []string
	OutDir    string
	Format    string // csv or jsonl
	Salt      string
	JobLabels []string
	Version   string
}

// hourSuffix matches the per-hour files infrapulse-export.sh writes
// (<metric>_h000.json.gz); each hour is converted on its own to bound memory.
var hourSuffix = regexp.MustCompile(`_h(\d+)\.`)

// Convert turns the exported samples into gpu_samples rows, one per GPU per
// scrape timestamp, through the same field mapping as a live collection: both
// DCGM name generations, unit conversion, and throttle reasons rebuilt from
// the throttle-seconds counters where the exporter serves no bitmask.
func Convert(cfg ConvertConfig) (*Meta, error) {
	files, err := exportFiles(cfg.Inputs)
	if err != nil {
		return nil, err
	}
	groups := map[string][]string{}
	for _, f := range files {
		k := ""
		if m := hourSuffix.FindStringSubmatch(filepath.Base(f)); m != nil {
			k = fmt.Sprintf("%09s", m[1])
		}
		groups[k] = append(groups[k], f)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	opt := sample.Options{JobLabels: cfg.JobLabels, Hasher: anon.New(cfg.Salt)}
	meta := &Meta{CollectorVersion: cfg.Version, Sources: []string{fmt.Sprintf("prometheus export (%d files)", len(files))},
		Pseudonymised: cfg.Salt != "", Coverage: map[string]bool{}, HostSource: "none"}
	for _, fd := range fields.Catalog {
		meta.Coverage[fd.Column] = false
	}
	var (
		w       sink.Writer
		out     *os.File
		reasons sample.ReasonDeriver
		last    = map[string]time.Time{} // per GPU: the newest row written
		steps   = map[int64]int{}        // milliseconds between a GPU's rows -> how often
		unknown = map[string]struct{}{}
		untimed int
	)
	defer func() {
		if out != nil {
			out.Close()
		}
	}()
	for _, k := range keys {
		var samples []promtext.Sample
		for _, f := range groups[k] {
			ss, err := readExport(f)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			samples = append(samples, ss...)
		}
		normaliseExportLabels(samples)
		byTS := map[int64][]promtext.Sample{}
		for _, s := range samples {
			if s.TimestampMs == 0 {
				untimed++
				continue
			}
			byTS[s.TimestampMs] = append(byTS[s.TimestampMs], s)
		}
		stamps := make([]int64, 0, len(byTS))
		for ts := range byTS {
			stamps = append(stamps, ts)
		}
		sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
		for _, ts := range stamps {
			rows, unk := sample.Build(time.UnixMilli(ts), byTS[ts], opt)
			for _, u := range unk {
				unknown[u] = struct{}{}
			}
			reasons.Apply(rows)
			// Two hourly files can both hold the sample on their shared boundary.
			keep := rows[:0]
			for _, r := range rows {
				g := r.Identity["host"] + "\x00" + r.Identity["gpu_index"] + "\x00" + r.Identity["gpu_uuid"]
				if prev, seen := last[g]; seen {
					if !r.TS.After(prev) {
						continue
					}
					steps[r.TS.Sub(prev).Milliseconds()]++
				}
				last[g] = r.TS
				keep = append(keep, r)
			}
			if len(keep) == 0 {
				continue
			}
			if w == nil {
				if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
					return nil, err
				}
				meta.StartedAt = keep[0].TS
				meta.SamplesFile = fmt.Sprintf("gpu_samples_%s.%s", keep[0].TS.Format("20060102T150405Z"), cfg.Format)
				if out, err = os.Create(filepath.Join(cfg.OutDir, meta.SamplesFile)); err != nil {
					return nil, err
				}
				if w, err = sink.New(cfg.Format, out); err != nil {
					return nil, err
				}
			}
			if err := w.Write(keep); err != nil {
				return nil, err
			}
			meta.Rows += len(keep)
			for _, r := range keep {
				meta.DCGM47 = meta.DCGM47 || r.DCGM47
				if r.TS.After(meta.EndedAt) {
					meta.EndedAt = r.TS
				}
				if v := fields.Vendor(r.Identity["model"]); meta.Vendor == "" {
					meta.Vendor = v
				} else if v != "" && v != meta.Vendor {
					meta.Vendor = "mixed"
				}
				for c := range r.Values {
					meta.Coverage[c] = true
				}
			}
		}
	}
	if w == nil {
		if untimed > 0 {
			return nil, fmt.Errorf("the samples carry no timestamps; export a time range (infrapulse-export.sh or promtool tsdb dump), not a single scrape")
		}
		return nil, fmt.Errorf("no GPU metrics InfraPulse knows in %d files; run `ipcollect fields` for the metric names", len(files))
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}
	err = out.Close()
	out = nil
	if err != nil {
		return nil, err
	}
	meta.GPUs = len(last)
	meta.ReasonsDerived = reasons.Derived > 0
	meta.IntervalSeconds = medianStep(steps)
	for u := range unknown {
		meta.UnknownMetrics = append(meta.UnknownMetrics, u)
	}
	sort.Strings(meta.UnknownMetrics)
	return meta, writeMeta(cfg.OutDir, meta.SamplesFile, meta)
}

// exportFiles lists the files to read: each input itself, or the files directly
// inside it when it is a folder (subfolders may hold another export of the same
// window and are not entered).
func exportFiles(inputs []string) ([]string, error) {
	var out []string
	for _, in := range inputs {
		st, err := os.Stat(in)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			out = append(out, in)
			continue
		}
		entries, err := os.ReadDir(in)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			n := strings.TrimSuffix(e.Name(), ".gz")
			if e.Type().IsRegular() && !strings.HasPrefix(n, ".") && (strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".txt")) {
				out = append(out, filepath.Join(in, e.Name()))
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no .json, .txt, .json.gz or .txt.gz files in %s", strings.Join(inputs, ", "))
	}
	sort.Strings(out)
	return out, nil
}

func readExport(path string) ([]promtext.Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	name := path
	if strings.HasSuffix(name, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r, name = gz, strings.TrimSuffix(name, ".gz")
	}
	if strings.HasSuffix(name, ".json") {
		return promtext.ParseAPI(r)
	}
	return promtext.ParseDump(r)
}

// workloadLabels are the labels dcgm-exporter sets from the pod using a GPU.
var workloadLabels = []string{"namespace", "pod", "container"}

// normaliseExportLabels undoes what the metrics store did to the exporter's
// labels. With honor_labels off (the Kubernetes default), Prometheus keeps the
// scrape target's own namespace, pod and container (the dcgm-exporter pod) and
// renames the exporter's to exported_*; a GPU with no workload then carries
// only the target's. So when any series has an exported_* label, those are the
// workload and the plain ones are dropped. A series with no host label takes
// the node or instance the store recorded.
func normaliseExportLabels(samples []promtext.Sample) {
	renamed := false
	for _, s := range samples {
		for _, l := range workloadLabels {
			if _, ok := s.Labels["exported_"+l]; ok {
				renamed = true
			}
		}
		if renamed {
			break
		}
	}
	// The samples of a series share one label map; every step below gives the
	// same result when repeated.
	for _, s := range samples {
		l := s.Labels
		if renamed {
			for _, w := range workloadLabels {
				if v, ok := l["exported_"+w]; ok {
					l[w] = v
				} else {
					delete(l, w)
				}
			}
		}
		if l[fields.LabelHost] == "" && l[fields.LabelHostV2] == "" {
			for _, k := range []string{"node", "kubernetes_node", "instance"} {
				if l[k] != "" {
					l[fields.LabelHost] = l[k]
					break
				}
			}
		}
	}
}

// medianStep is the median time between a GPU's consecutive rows, in seconds.
func medianStep(steps map[int64]int) float64 {
	total := 0
	keys := make([]int64, 0, len(steps))
	for ms, n := range steps {
		keys = append(keys, ms)
		total += n
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	seen := 0
	for _, ms := range keys {
		if seen += steps[ms]; seen*2 >= total {
			return float64(ms) / 1000
		}
	}
	return 0
}
