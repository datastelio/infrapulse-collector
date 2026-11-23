// Package promsample turns one scrape of dcgm-exporter or the AMD Device Metrics
// Exporter (many per-metric series) into rows of the canonical gpu_samples schema (one row per GPU per scrape).
package promsample

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/anon"
	"github.com/datastelio/infrapulse-collector/internal/fields"
	"github.com/datastelio/infrapulse-collector/internal/promtext"
)

// Row is one GPU at one point in time.
type Row struct {
	TS       time.Time
	Identity map[string]string  // identity columns (host, gpu_index, ...)
	Values   map[string]float64 // field columns; absent key = not reported
}

// Options control how labels map to identity columns.
type Options struct {
	// DefaultHost is used when the exporter does not send a Hostname label.
	DefaultHost string
	// JobLabels are label names checked, in order, for a scheduler job ID
	// (for example Slurm or a custom HPC job label).
	JobLabels []string
	// Hasher pseudonymises host, UUID, pod, namespace, container and job IDs.
	Hasher *anon.Hasher
}

// DefaultJobLabels are common label names for a scheduler job ID.
var DefaultJobLabels = []string{"job_id", "hpc_job", "slurm_job_id", "SLURM_JOB_ID"}

type key struct{ host, gpu, uuid string }

// amdAcc collects the AMD exporter series feeding one column of one row: the
// best-priority metric wins and its series are averaged (gpu_clock reports one
// system clock per XCD on MI300).
type amdAcc struct {
	prio int
	sum  float64
	n    int
}

// Build groups samples by GPU and returns rows sorted by host and GPU index.
// It also returns the names of metrics that were not in the catalog.
func Build(ts time.Time, samples []promtext.Sample, opt Options) ([]Row, []string) {
	jobLabels := opt.JobLabels
	if len(jobLabels) == 0 {
		jobLabels = DefaultJobLabels
	}
	rows := map[key]*Row{}
	amd := map[*Row]map[string]*amdAcc{}
	unknown := map[string]struct{}{}
	for _, s := range samples {
		f, ok := fields.Lookup(s.Name)
		var amdFields []fields.AMDField
		if !ok {
			if !fields.IsAMDMetric(s.Name) {
				// The AMD exporter serves 100+ metrics (per-block ECC, XGMI, profiler
				// counters); the ones not in the catalog are skipped quietly.
				if _, amd := s.Labels["gpu_id"]; !amd {
					unknown[s.Name] = struct{}{}
				}
				continue
			}
			if amdFields = fields.LookupAMD(s.Name, s.Labels); len(amdFields) == 0 {
				continue // a series of a known metric we don't read (gpu_clock{clock_type="video"})
			}
		}
		id := identityOf(s.Labels, amdFields != nil)
		if id.host == "" {
			id.host = opt.DefaultHost
		}
		k := key{host: id.host, gpu: id.gpu, uuid: id.uuid}
		r, ok := rows[k]
		if !ok {
			r = &Row{TS: ts.UTC(), Identity: map[string]string{
				"host":           opt.Hasher.Hash("host", id.host),
				"gpu_index":      id.gpu,
				"gpu_uuid":       opt.Hasher.Hash("gpu", id.uuid),
				"model":          id.model,
				"driver_version": id.driver,
			}, Values: map[string]float64{}}
			rows[k] = r
		}
		setIfEmpty(r.Identity, "namespace", opt.Hasher.Hash("ns", s.Labels["namespace"]))
		setIfEmpty(r.Identity, "pod", opt.Hasher.Hash("pod", s.Labels["pod"]))
		setIfEmpty(r.Identity, "container", opt.Hasher.Hash("ctr", s.Labels["container"]))
		for _, l := range jobLabels {
			if v := s.Labels[l]; v != "" {
				setIfEmpty(r.Identity, "job_id", opt.Hasher.Hash("job", v))
				break
			}
		}
		if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			continue
		}
		if amdFields == nil {
			r.Values[f.Column] = s.Value * f.NameScale()
			continue
		}
		acc := amd[r]
		if acc == nil {
			acc = map[string]*amdAcc{}
			amd[r] = acc
		}
		for _, af := range amdFields {
			a := acc[af.Column]
			switch {
			case a == nil || af.Priority < a.prio:
				acc[af.Column] = &amdAcc{prio: af.Priority, sum: s.Value * af.Scale, n: 1}
			case af.Priority == a.prio:
				a.sum += s.Value * af.Scale
				a.n++
			}
		}
	}
	for r, acc := range amd {
		finishAMD(r, acc)
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Identity["host"] != out[j].Identity["host"] {
			return out[i].Identity["host"] < out[j].Identity["host"]
		}
		return gpuLess(out[i].Identity["gpu_index"], out[j].Identity["gpu_index"])
	})
	names := make([]string, 0, len(unknown))
	for n := range unknown {
		names = append(names, n)
	}
	sort.Strings(names)
	return out, names
}

type identity struct{ host, gpu, uuid, model, driver string }

// identityOf reads the GPU's identity from dcgm-exporter labels, or from the
// AMD Device Metrics Exporter's (gpu_id, hostname, serial_number, card_model).
func identityOf(l map[string]string, amd bool) identity {
	if !amd {
		gpu := l["gpu"]
		if gi := l["GPU_I_ID"]; gi != "" { // MIG instance
			gpu = gpu + "/" + gi
		}
		return identity{host: l["Hostname"], gpu: gpu, uuid: l["UUID"], model: l["modelName"], driver: l["DCGM_FI_DRIVER_VERSION"]}
	}
	gpu := l["gpu_id"]
	if p := l["gpu_partition_id"]; p != "" && p != "0" && !strings.EqualFold(p, "NA") { // compute partition (CPX)
		gpu = gpu + "/" + p
	}
	return identity{host: l["hostname"], gpu: gpu, uuid: l["serial_number"], model: fields.AMDModelName(l["card_model"]), driver: l["driver_version"]}
}

// finishAMD writes the averaged AMD values into the row and derives the
// throttle-reason bitmask from the violation residency percentages.
func finishAMD(r *Row, acc map[string]*amdAcc) {
	get := func(col string) (float64, bool) {
		a := acc[col]
		if a == nil || a.n == 0 {
			return 0, false
		}
		return a.sum / float64(a.n), true
	}
	for col := range acc {
		if strings.HasPrefix(col, "_amd_") {
			continue
		}
		if _, set := r.Values[col]; !set {
			v, _ := get(col)
			r.Values[col] = math.Round(v*1e6) / 1e6 // scaling leaves float noise (35 x 0.01)
		}
	}
	ppt, hp := get(fields.AMDPowerViolationPct)
	thm, ht := get(fields.AMDThermalViolationPct)
	hbm, hh := get(fields.AMDHBMViolationPct)
	if mask, ok := fields.AMDReasons(ppt, thm, hbm, hp, ht, hh); ok {
		r.Values["clock_event_reasons"] = float64(mask)
	}
}

func setIfEmpty(m map[string]string, k, v string) {
	if v != "" && m[k] == "" {
		m[k] = v
	}
}

// gpuLess orders "2" before "10" and keeps MIG suffixes together.
func gpuLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// Coverage reports, for every catalog column, whether at least one GPU in the
// rows reported a value.
func Coverage(rows []Row) map[string]bool {
	cov := map[string]bool{}
	for _, f := range fields.Catalog {
		cov[f.Column] = false
	}
	for _, r := range rows {
		for c := range r.Values {
			cov[c] = true
		}
	}
	return cov
}
