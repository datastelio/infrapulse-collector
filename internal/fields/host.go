package fields

// Host-level fields, written to the host_samples table (one row per host,
// NUMA node or GPU process group per sample).
//
//	H  host and NUMA node (CPU, memory, swap, disk, network, PSI); from /proc or node_exporter
//	P  the processes using each GPU (loader workers, D state, reads, NUMA placement); /proc only

const (
	TierH Tier = "H"
	TierP Tier = "P"
)

// Scopes of a host_samples row.
const (
	ScopeHost = "host"
	ScopeNUMA = "numa"
	ScopeGPU  = "gpu"
)

// HostField describes one host_samples column.
type HostField struct {
	Column string
	Tier   Tier
	Scopes []string // scopes in which the column is filled
	Unit   string
	Help   string
}

var hn = []string{ScopeHost, ScopeNUMA}
var h = []string{ScopeHost}
var g = []string{ScopeGPU}

// HostCatalog lists every host_samples field, in canonical column order.
var HostCatalog = []HostField{
	{"cpu_count", TierH, hn, "count", "Logical CPUs."},
	{"cpu_busy_pct", TierH, hn, "%", "CPU time not idle, iowait or steal."},
	{"cpu_user_pct", TierH, hn, "%", "CPU time in user mode (incl. nice)."},
	{"cpu_system_pct", TierH, hn, "%", "CPU time in kernel mode (incl. irq, softirq)."},
	{"cpu_iowait_pct", TierH, hn, "%", "CPU time idle while I/O was outstanding."},
	{"cpu_steal_pct", TierH, hn, "%", "CPU time taken by the hypervisor."},
	{"cpu_cores_saturated", TierH, hn, "count", "Logical CPUs at least 90% busy over the interval."},
	{"mem_total_bytes", TierH, hn, "B", "Memory installed."},
	{"mem_available_bytes", TierH, h, "B", "Memory available for new work without swapping (MemAvailable)."},
	{"mem_free_bytes", TierH, hn, "B", "Memory not used at all (MemFree)."},
	{"swap_used_bytes", TierH, h, "B", "Swap in use."},
	{"swap_in_pages_per_s", TierH, h, "pages/s", "Pages swapped in."},
	{"swap_out_pages_per_s", TierH, h, "pages/s", "Pages swapped out."},
	{"major_faults_per_s", TierH, h, "1/s", "Major page faults (page read from disk)."},
	{"disk_read_bytes_per_s", TierH, h, "B/s", "Bytes read from physical disks."},
	{"disk_write_bytes_per_s", TierH, h, "B/s", "Bytes written to physical disks."},
	{"disk_util_max_pct", TierH, h, "%", "Busiest disk's share of time with I/O in flight."},
	{"net_rx_bytes_per_s", TierH, h, "B/s", "Bytes received on physical interfaces."},
	{"net_tx_bytes_per_s", TierH, h, "B/s", "Bytes sent on physical interfaces."},
	{"psi_cpu_some_pct", TierH, h, "%", "Share of time some task waited for a CPU (PSI)."},
	{"psi_mem_some_pct", TierH, h, "%", "Share of time some task stalled on memory (PSI)."},
	{"psi_mem_full_pct", TierH, h, "%", "Share of time all non-idle tasks stalled on memory (PSI)."},
	{"psi_io_some_pct", TierH, h, "%", "Share of time some task stalled on I/O (PSI)."},
	{"psi_io_full_pct", TierH, h, "%", "Share of time all non-idle tasks stalled on I/O (PSI)."},

	{"gpu_numa_node", TierP, g, "node", "NUMA node the GPU is attached to (-1 unknown)."},
	{"proc_count", TierP, g, "count", "Processes using the GPU, with their descendants."},
	{"proc_cpu_cores", TierP, g, "cores", "CPU-seconds per second used by those processes."},
	{"loader_workers", TierP, g, "count", "Members whose parent is also a member (data-loader workers)."},
	{"loader_workers_saturated", TierP, g, "count", "Loader workers using at least 0.9 of a core."},
	{"proc_d_state", TierP, g, "count", "Members in uninterruptible sleep (usually waiting for I/O) at the sample."},
	{"proc_read_bytes_per_s", TierP, g, "B/s", "Storage reads caused by the members."},
	{"proc_major_faults_per_s", TierP, g, "1/s", "Major page faults of the members."},
	{"proc_rss_bytes", TierP, g, "B", "Sum of members' resident memory (shared pages counted per process)."},
	{"proc_remote_numa_share", TierP, g, "ratio", "CPU-weighted share of members last running outside the GPU's NUMA node."},
	// The job's own cgroup (v2): a container or pod memory limit is invisible in host-wide
	// memory signals; these show it.
	{"proc_cgroup_mem_psi_some_pct", TierP, g, "%", "Share of time some task in the job's cgroup stalled on memory (memory.pressure avg10)."},
	{"proc_cgroup_mem_high_events_per_s", TierP, g, "1/s", "Times per second the job's cgroup was throttled at memory.high (memory.events high)."},
	{"proc_cgroup_mem_used_bytes", TierP, g, "B", "Memory charged to the job's cgroup, page cache included (memory.current)."},
	{"proc_cgroup_mem_limit_bytes", TierP, g, "B", "The job's cgroup memory limit: the lower of memory.high and memory.max; empty if unlimited."},
}

// OptionalHostColumns may be absent on a healthy host: cgroup v1 hosts have no
// memory.pressure, and a job without a memory limit has no limit to report.
var OptionalHostColumns = map[string]bool{
	"proc_cgroup_mem_psi_some_pct": true, "proc_cgroup_mem_high_events_per_s": true,
	"proc_cgroup_mem_used_bytes": true, "proc_cgroup_mem_limit_bytes": true,
}

// HostIdentityColumns precede the field columns in every host_samples row.
var HostIdentityColumns = []string{"ts", "host", "scope", "scope_id"}

// HostColumns returns the full host_samples column order.
func HostColumns() []string {
	cols := append([]string{}, HostIdentityColumns...)
	for _, f := range HostCatalog {
		cols = append(cols, f.Column)
	}
	return cols
}

// HostByTier groups the host catalog by tier, in catalog order.
func HostByTier() map[Tier][]HostField {
	out := map[Tier][]HostField{}
	for _, f := range HostCatalog {
		out[f.Tier] = append(out[f.Tier], f)
	}
	return out
}
