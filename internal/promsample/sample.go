// Package promsample turns one scrape of dcgm-exporter or the AMD Device Metrics
// Exporter (many per-metric series) into rows of the canonical gpu_samples schema (one row per GPU per scrape).
package promsample

import (
	"math"
	"sort"
	"strconv"
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
	// DCGM47 is set when the GPU's metrics came under the DCGM 4.7 names
	// (dcgm-exporter 4.9.0 and later). Not written to the samples file.
	DCGM47 bool
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

// xidAcc tracks the XID code with the highest count in the exporter's window
// for one row (ties go to the higher code, so the result does not depend on the
// order of the series).
type xidAcc struct{ code, count float64 }

// Build groups samples by GPU and returns rows sorted by host and GPU index.
// It also returns the names of metrics that were not in the catalog.
func Build(ts time.Time, samples []promtext.Sample, opt Options) ([]Row, []string) {
	jobLabels := opt.JobLabels
	if len(jobLabels) == 0 {
		jobLabels = DefaultJobLabels
	}
	rows := map[key]*Row{}
	amd := map[*Row]map[string]*amdAcc{}
	xids := map[*Row]xidAcc{}
	unknown := map[string]struct{}{}
	for _, s := range samples {
		f, scale, v2, ok := fields.LookupScaled(s.Name)
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
		if amdFields == nil && f.Column == fields.XIDCountColumn {
			// One series per XID code: the column is the total.
			r.Values[f.Column] += s.Value
			if code, err := strconv.ParseFloat(s.Labels[fields.XIDCodeLabel], 64); err == nil && s.Value > 0 {
				if a := xids[r]; s.Value > a.count || (s.Value == a.count && code > a.code) {
					xids[r] = xidAcc{code: code, count: s.Value}
				}
			}
			continue
		}
		if amdFields == nil {
			v := s.Value
			if scale != 1 {
				v = math.Round(v*scale*1e6) / 1e6 // scaling leaves float noise (8.077822 s x 1e6)
			}
			r.Values[f.Column] = v
			r.DCGM47 = r.DCGM47 || v2
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
	// The last-XID field has no value until DCGM reports one; when only the count
	// arrived, the xid column takes the most frequent code in the window.
	for r, a := range xids {
		if _, has := r.Values["xid"]; !has {
			r.Values["xid"] = a.code
		}
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
		// dcgm-exporter 4.8.3 renamed Hostname to hostname; 4.9.0 renamed the driver label.
		host, driver := l[fields.LabelHost], l[fields.LabelDriver]
		if host == "" {
			host = l[fields.LabelHostV2]
		}
		if driver == "" {
			driver = l[fields.LabelDriverV2]
		}
		return identity{host: host, gpu: gpu, uuid: l["UUID"], model: l["modelName"], driver: driver}
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

// ThrottleShare is the share of the interval between two scrapes a GPU must
// have spent throttled, by a counter's own account, for that reason to be set.
// NVML's reason bits are instantaneous; a counter covers the whole interval, so
// a sample is flagged when the limit held for most of it (the rule the AMD
// mapping uses, fields.AMDThrottleShare).
const ThrottleShare = fields.AMDThrottleShare / 100

// reasonCounters maps the throttle-time counters to the NVML reason bit each
// stands for. Board-limit and reliability throttling have no NVML reason bit.
var reasonCounters = []struct {
	column string
	bit    uint64
}{
	{"power_violation_us", fields.ReasonSWPowerCap},
	{"thermal_violation_us", fields.ReasonSWThermalSlowdown},
	{"sync_boost_violation_us", fields.ReasonSyncBoost},
}

// ReasonDeriver fills clock_event_reasons for GPUs whose exporter cannot serve
// it. dcgm-exporter 4.9.0 rejects bitmask fields, so the throttle reasons are
// rebuilt from how much each throttle-seconds counter grew since the previous
// scrape. It needs two scrapes: the first leaves the column empty. A GPU that
// reports the bitmask itself, or whose counters came under the classic names
// (whose unit differs between DCGM versions), is left alone.
type ReasonDeriver struct {
	prev map[string]reasonState
	// Derived counts the rows whose reasons were derived.
	Derived int
}

type reasonState struct {
	ts time.Time
	v  map[string]float64
}

// Apply derives the reasons for one scrape's rows, in place.
func (d *ReasonDeriver) Apply(rows []Row) {
	if d.prev == nil {
		d.prev = map[string]reasonState{}
	}
	for i := range rows {
		r := &rows[i]
		if _, has := r.Values["clock_event_reasons"]; has || !r.DCGM47 {
			continue
		}
		cur := reasonState{ts: r.TS, v: map[string]float64{}}
		for _, c := range reasonCounters {
			if v, ok := r.Values[c.column]; ok {
				cur.v[c.column] = v
			}
		}
		if len(cur.v) == 0 {
			continue
		}
		k := r.Identity["host"] + "\x00" + r.Identity["gpu_index"] + "\x00" + r.Identity["gpu_uuid"]
		p, ok := d.prev[k]
		d.prev[k] = cur
		dt := cur.ts.Sub(p.ts).Seconds()
		if !ok || dt <= 0 {
			continue
		}
		var mask uint64
		known := false
		for _, c := range reasonCounters {
			a, okA := p.v[c.column]
			b, okB := cur.v[c.column]
			if !okA || !okB || b < a { // b < a: the counter restarted (driver reload)
				continue
			}
			known = true
			if (b-a)/1e6/dt >= ThrottleShare {
				mask |= c.bit
			}
		}
		if known {
			r.Values["clock_event_reasons"] = float64(mask)
			d.Derived++
		}
	}
}
