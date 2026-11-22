// Package host reads server-level metrics (CPU, memory, swap, disk, network,
// pressure stall information, and the processes using each GPU) and turns two
// consecutive readings into host_samples rows.
//
// A Snapshot holds cumulative counters and gauges as read at one instant. It is
// filled from /proc and /sys (ReadProc), from node_exporter text
// (FromNodeExporter) or by the simulator. Rates divides the difference between
// two snapshots by the time between them, so every source shares the maths.
package host

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/anon"
	"github.com/datastelio/infrapulse-collector/internal/fields"
)

// CPUTimes are cumulative seconds in each mode for one logical CPU.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal float64
}

func (c CPUTimes) total() float64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

func (c CPUTimes) sub(o CPUTimes) CPUTimes {
	return CPUTimes{c.User - o.User, c.Nice - o.Nice, c.System - o.System, c.Idle - o.Idle,
		c.IOWait - o.IOWait, c.IRQ - o.IRQ, c.SoftIRQ - o.SoftIRQ, c.Steal - o.Steal}
}

func (c CPUTimes) add(o CPUTimes) CPUTimes {
	return CPUTimes{c.User + o.User, c.Nice + o.Nice, c.System + o.System, c.Idle + o.Idle,
		c.IOWait + o.IOWait, c.IRQ + o.IRQ, c.SoftIRQ + o.SoftIRQ, c.Steal + o.Steal}
}

// PSI holds cumulative pressure stall seconds.
type PSI struct {
	CPUSome, MemSome, MemFull, IOSome, IOFull float64
}

// NUMAMem is one NUMA node's memory, in bytes.
type NUMAMem struct{ Total, Free float64 }

// Proc is one process in a GPU's process group.
type Proc struct {
	Comm      string // process name; PyTorch names its DataLoader workers pt_data_worker
	PPID      int
	CPUSec    float64 // cumulative user+system seconds
	ReadBytes float64 // cumulative; -1 if unreadable
	MajFlt    float64 // cumulative
	RSS       float64 // bytes
	State     byte    // R, S, D, ...
	LastCPU   int
}

// GPUProcs is the process group using one GPU.
type GPUProcs struct {
	NUMANode int // -1 unknown
	Procs    map[int]Proc
	Cgroup   *CgroupMem // the cgroup most members belong to; nil if unknown (cgroup v1, unreadable)
}

// CgroupMem is a cgroup v2 memory reading.
type CgroupMem struct {
	Path       string
	PSISome    float64 // memory.pressure some avg10, %
	HighEvents float64 // memory.events "high", cumulative
	Current    float64 // memory.current, bytes
	Limit      float64 // min(memory.high, memory.max), bytes; -1 if unlimited
}

// Snapshot is one reading of the host. Missing parts are nil or flagged.
type Snapshot struct {
	TS   time.Time
	Host string

	CPUs     map[int]CPUTimes // per logical CPU; nil if unavailable
	CPUTotal *CPUTimes        // whole-host total (used when CPUs is nil)
	NUMACPUs map[int][]int    // NUMA node -> logical CPUs; nil if unknown

	HasMem                                               bool
	MemTotal, MemAvailable, MemFree, SwapTotal, SwapFree float64 // bytes
	NUMAMem                                              map[int]NUMAMem

	HasVM                       bool
	PswpIn, PswpOut, PgMajFault float64 // cumulative

	HasDisk             bool
	DiskRead, DiskWrite float64            // cumulative bytes, physical disks
	DiskIOTime          map[string]float64 // cumulative busy seconds per device

	HasNet       bool
	NetRx, NetTx float64 // cumulative bytes, physical interfaces

	PSI *PSI

	GPUs map[string]*GPUProcs // by GPU index (device minor)
}

// Row is one host_samples row.
type Row struct {
	TS      time.Time
	Host    string
	Scope   string
	ScopeID string
	Values  map[string]float64
}

// SaturatedCore is the busy share at which a CPU or a worker counts as saturated.
const SaturatedCore = 0.9

// DataLoaderComm is the process name PyTorch gives DataLoader worker processes.
const DataLoaderComm = "pt_data_worker"

// minWorkerCore is the CPU use (cores) at which an unnamed child counts as a loader worker.
const minWorkerCore = 0.1

// Rates turns two snapshots of the same host into rows stamped with cur.TS:
// one host row, one per NUMA node and one per GPU process group. The host name
// is pseudonymised with h (nil = unchanged), the same way gpu_samples does.
func Rates(prev, cur *Snapshot, h *anon.Hasher) []Row {
	dt := cur.TS.Sub(prev.TS).Seconds()
	if dt <= 0 {
		return nil
	}
	hostID := h.Hash("host", cur.Host)
	mk := func(scope, id string) Row {
		return Row{TS: cur.TS.UTC(), Host: hostID, Scope: scope, ScopeID: id, Values: map[string]float64{}}
	}
	hr := mk(fields.ScopeHost, "")
	rows := []Row{}

	// CPU, host-wide and per NUMA node.
	var cpuRows []Row
	if cur.CPUs != nil && prev.CPUs != nil {
		all := make([]int, 0, len(cur.CPUs))
		for id := range cur.CPUs {
			if _, ok := prev.CPUs[id]; ok {
				all = append(all, id)
			}
		}
		sort.Ints(all)
		cpuStats(hr.Values, prev.CPUs, cur.CPUs, all)
		nodes := make([]int, 0, len(cur.NUMACPUs))
		for n := range cur.NUMACPUs {
			nodes = append(nodes, n)
		}
		sort.Ints(nodes)
		for _, n := range nodes {
			r := mk(fields.ScopeNUMA, strconv.Itoa(n))
			cpuStats(r.Values, prev.CPUs, cur.CPUs, cur.NUMACPUs[n])
			if m, ok := cur.NUMAMem[n]; ok {
				r.Values["mem_total_bytes"] = m.Total
				r.Values["mem_free_bytes"] = m.Free
			}
			cpuRows = append(cpuRows, r)
		}
	} else if cur.CPUTotal != nil && prev.CPUTotal != nil {
		d := cur.CPUTotal.sub(*prev.CPUTotal)
		pct(hr.Values, d)
	}
	if cur.CPUs == nil && len(cur.NUMAMem) > 0 { // node_exporter: NUMA memory without CPU split
		nodes := make([]int, 0, len(cur.NUMAMem))
		for n := range cur.NUMAMem {
			nodes = append(nodes, n)
		}
		sort.Ints(nodes)
		for _, n := range nodes {
			r := mk(fields.ScopeNUMA, strconv.Itoa(n))
			r.Values["mem_total_bytes"] = cur.NUMAMem[n].Total
			r.Values["mem_free_bytes"] = cur.NUMAMem[n].Free
			cpuRows = append(cpuRows, r)
		}
	}

	if cur.HasMem {
		hr.Values["mem_total_bytes"] = cur.MemTotal
		hr.Values["mem_available_bytes"] = cur.MemAvailable
		hr.Values["mem_free_bytes"] = cur.MemFree
		hr.Values["swap_used_bytes"] = math.Max(0, cur.SwapTotal-cur.SwapFree)
	}
	if cur.HasVM && prev.HasVM {
		hr.Values["swap_in_pages_per_s"] = rate(prev.PswpIn, cur.PswpIn, dt)
		hr.Values["swap_out_pages_per_s"] = rate(prev.PswpOut, cur.PswpOut, dt)
		hr.Values["major_faults_per_s"] = rate(prev.PgMajFault, cur.PgMajFault, dt)
	}
	if cur.HasDisk && prev.HasDisk {
		hr.Values["disk_read_bytes_per_s"] = rate(prev.DiskRead, cur.DiskRead, dt)
		hr.Values["disk_write_bytes_per_s"] = rate(prev.DiskWrite, cur.DiskWrite, dt)
		util := 0.0
		for dev, t := range cur.DiskIOTime {
			if p, ok := prev.DiskIOTime[dev]; ok {
				util = math.Max(util, math.Min(100, rate(p, t, dt)*100))
			}
		}
		hr.Values["disk_util_max_pct"] = round(util, 2)
	}
	if cur.HasNet && prev.HasNet {
		hr.Values["net_rx_bytes_per_s"] = rate(prev.NetRx, cur.NetRx, dt)
		hr.Values["net_tx_bytes_per_s"] = rate(prev.NetTx, cur.NetTx, dt)
	}
	if cur.PSI != nil && prev.PSI != nil {
		p, c := prev.PSI, cur.PSI
		hr.Values["psi_cpu_some_pct"] = share(p.CPUSome, c.CPUSome, dt)
		hr.Values["psi_mem_some_pct"] = share(p.MemSome, c.MemSome, dt)
		hr.Values["psi_mem_full_pct"] = share(p.MemFull, c.MemFull, dt)
		hr.Values["psi_io_some_pct"] = share(p.IOSome, c.IOSome, dt)
		hr.Values["psi_io_full_pct"] = share(p.IOFull, c.IOFull, dt)
	}
	if len(hr.Values) > 0 {
		rows = append(rows, hr)
	}
	rows = append(rows, cpuRows...)

	// Process groups per GPU.
	gpus := make([]string, 0, len(cur.GPUs))
	for k := range cur.GPUs {
		gpus = append(gpus, k)
	}
	sort.Slice(gpus, func(i, j int) bool {
		if len(gpus[i]) != len(gpus[j]) {
			return len(gpus[i]) < len(gpus[j])
		}
		return gpus[i] < gpus[j]
	})
	for _, k := range gpus {
		var pg *GPUProcs
		if prev.GPUs != nil {
			pg = prev.GPUs[k]
		}
		rows = append(rows, gpuRow(mk(fields.ScopeGPU, k), pg, cur.GPUs[k], cur.NUMACPUs, dt))
	}
	return rows
}

func gpuRow(r Row, prev, cur *GPUProcs, numa map[int][]int, dt float64) Row {
	v := r.Values
	v["gpu_numa_node"] = float64(cur.NUMANode)
	v["proc_count"] = float64(len(cur.Procs))
	var cores, reads, majflt, rss, remote, dstate, workers, saturated float64
	readOK := false
	// Loader workers: children of a GPU process. When PyTorch's named DataLoader
	// workers are present, count only those; otherwise children of a GPU process
	// such as torch.compile's compile workers, which sit idle, would dilute
	// the saturated share. Without named workers, count children that are doing
	// work (>= minWorkerCore), or every child on the first reading.
	named := false
	for _, p := range cur.Procs {
		if _, child := cur.Procs[p.PPID]; child && p.Comm == DataLoaderComm {
			named = true
			break
		}
	}
	local := map[int]bool{}
	for _, c := range numa[cur.NUMANode] {
		local[c] = true
	}
	for pid, p := range cur.Procs {
		rss += p.RSS
		if p.State == 'D' {
			dstate++
		}
		_, isChild := cur.Procs[p.PPID]
		q, seen := Proc{}, false
		if prev != nil {
			q, seen = prev.Procs[pid]
		}
		c := 0.0
		if seen {
			c = rate(q.CPUSec, p.CPUSec, dt)
		}
		isWorker := isChild && (named && p.Comm == DataLoaderComm || !named && (prev == nil || c >= minWorkerCore))
		if isWorker {
			workers++
		}
		if !seen {
			continue
		}
		cores += c
		if isWorker && c >= SaturatedCore {
			saturated++
		}
		if len(numa) > 1 && cur.NUMANode >= 0 && !local[p.LastCPU] {
			remote += c
		}
		majflt += rate(q.MajFlt, p.MajFlt, dt)
		if p.ReadBytes >= 0 && q.ReadBytes >= 0 {
			reads += rate(q.ReadBytes, p.ReadBytes, dt)
			readOK = true
		}
	}
	v["loader_workers"] = workers
	v["proc_d_state"] = dstate
	v["proc_rss_bytes"] = rss
	if prev != nil {
		v["proc_cpu_cores"] = round(cores, 3)
		v["loader_workers_saturated"] = saturated
		v["proc_major_faults_per_s"] = round(majflt, 3)
		if readOK {
			v["proc_read_bytes_per_s"] = math.Round(reads)
		}
		if len(numa) > 1 && cur.NUMANode >= 0 && cores > 0 {
			v["proc_remote_numa_share"] = round(remote/cores, 3)
		}
	}
	if cg := cur.Cgroup; cg != nil {
		v["proc_cgroup_mem_psi_some_pct"] = cg.PSISome
		v["proc_cgroup_mem_used_bytes"] = cg.Current
		if cg.Limit >= 0 {
			v["proc_cgroup_mem_limit_bytes"] = cg.Limit
		}
		if prev != nil && prev.Cgroup != nil && prev.Cgroup.Path == cg.Path {
			v["proc_cgroup_mem_high_events_per_s"] = round(rate(prev.Cgroup.HighEvents, cg.HighEvents, dt), 3)
		}
	}
	return r
}

func cpuStats(v map[string]float64, prev, cur map[int]CPUTimes, ids []int) {
	var sum CPUTimes
	n, sat := 0, 0
	for _, id := range ids {
		c, ok1 := cur[id]
		p, ok2 := prev[id]
		if !ok1 || !ok2 {
			continue
		}
		d := c.sub(p)
		sum = sum.add(d)
		n++
		if tot := d.total(); tot > 0 && (tot-d.Idle-d.IOWait-d.Steal)/tot >= SaturatedCore {
			sat++
		}
	}
	if n == 0 {
		return
	}
	v["cpu_count"] = float64(n)
	v["cpu_cores_saturated"] = float64(sat)
	pct(v, sum)
}

func pct(v map[string]float64, d CPUTimes) {
	tot := d.total()
	if tot <= 0 {
		return
	}
	p := func(x float64) float64 { return round(math.Max(0, x)/tot*100, 2) }
	v["cpu_busy_pct"] = p(tot - d.Idle - d.IOWait - d.Steal)
	v["cpu_user_pct"] = p(d.User + d.Nice)
	v["cpu_system_pct"] = p(d.System + d.IRQ + d.SoftIRQ)
	v["cpu_iowait_pct"] = p(d.IOWait)
	v["cpu_steal_pct"] = p(d.Steal)
}

// rate is a per-second rate of a counter; a counter reset gives 0.
func rate(prev, cur, dt float64) float64 {
	if cur < prev {
		return 0
	}
	return round((cur-prev)/dt, 3)
}

func share(prev, cur, dt float64) float64 {
	return round(math.Min(100, rate(prev, cur, dt)*100), 2)
}

func round(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}

// Coverage reports, for every host catalog column, whether any row had a value.
func Coverage(rows []Row) map[string]bool {
	cov := map[string]bool{}
	for _, f := range fields.HostCatalog {
		cov[f.Column] = false
	}
	for _, r := range rows {
		for c := range r.Values {
			cov[c] = true
		}
	}
	return cov
}
