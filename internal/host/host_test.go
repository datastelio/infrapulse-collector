package host

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/anon"
	"github.com/datastelio/infrapulse-collector/internal/fields"
	"github.com/datastelio/infrapulse-collector/internal/promtext"
)

// fakeRoot writes a minimal /proc and /sys tree. tick advances the counters.
func fakeRoot(t *testing.T, root string, tick int) {
	t.Helper()
	k := float64(tick)
	w := func(path, body string) {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		p := filepath.Join(root, path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.Remove(p)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	// 4 CPUs, 2 NUMA nodes. Over one tick (10 s = 1000 ticks per CPU):
	// cpu0 fully busy, cpu1 idle, cpu2 half iowait, cpu3 idle.
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	itoa := strconv.Itoa
	w("proc/stat", "cpu  0 0 0 0 0 0 0 0 0 0\n"+
		"cpu0 "+f(1000*k)+" 0 0 100 0 0 0 0 0 0\n"+
		"cpu1 0 0 0 "+f(100+1000*k)+" 0 0 0 0 0 0\n"+
		"cpu2 0 0 0 "+f(100+500*k)+" "+f(500*k)+" 0 0 0 0 0\n"+
		"cpu3 0 0 0 "+f(100+1000*k)+" 0 0 0 0 0 0\nintr 1\n")
	w("sys/devices/system/node/node0/cpulist", "0-1\n")
	w("sys/devices/system/node/node1/cpulist", "2-3\n")
	w("sys/devices/system/node/node0/meminfo", "Node 0 MemTotal:  1000 kB\nNode 0 MemFree:  400 kB\n")
	w("sys/devices/system/node/node1/meminfo", "Node 1 MemTotal:  1000 kB\nNode 1 MemFree:  100 kB\n")
	w("proc/meminfo", "MemTotal:  2000 kB\nMemFree:  500 kB\nMemAvailable:  800 kB\nSwapTotal: 100 kB\nSwapFree: 40 kB\n")
	w("proc/vmstat", "pswpin "+f(100*k)+"\npswpout 0\npgmajfault "+f(50*k)+"\n")
	w("proc/diskstats",
		"   8       0 sda 1 0 "+f(2000*k)+" 0 1 0 "+f(1000*k)+" 0 0 "+f(5000*k)+" 0 0 0 0 0 0 0\n"+
			"   8       1 sda1 1 0 "+f(2000*k)+" 0 1 0 "+f(1000*k)+" 0 0 "+f(5000*k)+" 0 0 0 0 0 0 0\n"+
			"   7       0 loop0 1 0 999999 0 1 0 999999 0 0 999999 0 0 0 0 0 0 0\n")
	w("proc/net/dev", "Inter-|   Receive  |  Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes\n"+
		"    lo: 99999 0 0 0 0 0 0 0 99999 0 0 0 0 0 0 0\n"+
		"  eth0: "+f(1e6*k)+" 0 0 0 0 0 0 0 "+f(5e5*k)+" 0 0 0 0 0 0 0\n")
	w("proc/pressure/cpu", "some avg10=0 avg60=0 avg300=0 total="+f(1e6*k)+"\nfull avg10=0 avg60=0 avg300=0 total=0\n")
	w("proc/pressure/memory", "some avg10=0 avg60=0 avg300=0 total=0\nfull avg10=0 avg60=0 avg300=0 total=0\n")
	w("proc/pressure/io", "some avg10=0 avg60=0 avg300=0 total="+f(5e6*k)+"\nfull avg10=0 avg60=0 avg300=0 total="+f(2e6*k)+"\n")
	// GPU 0 on NUMA node 1.
	w("proc/driver/nvidia/gpus/0000:07:00.0/information", "Model: NVIDIA A100\nDevice Minor: 0\nBus Location: 0000:07:00.0\n")
	w("sys/bus/pci/devices/0000:07:00.0/numa_node", "1\n")
	// pid 100: trainer holding /dev/nvidia0, on cpu 3 (local). pid 101: its loader
	// worker, saturated, on cpu 0 (remote), in D state. pid 200: dcgm, excluded.
	// pid 300: unrelated.
	stat := func(pid, comm, state string, ppid int, majflt, utime float64, cpu int) string {
		fs := make([]string, 37)
		for i := range fs {
			fs[i] = "0"
		}
		fs[0], fs[1] = state, itoa(ppid)
		fs[12-3], fs[14-3], fs[24-3], fs[39-3] = f(majflt), f(utime), "10", itoa(cpu)
		return pid + " (" + comm + ") " + strings.Join(fs, " ") + "\n"
	}
	w("proc/100/stat", stat("100", "python (main)", "R", 1, 0, 300*k, 3))
	w("proc/100/io", "rchar: 1\nread_bytes: "+f(1e6*k)+"\n")
	link("/dev/nvidia0", "proc/100/fd/5")
	link("/dev/nvidiactl", "proc/100/fd/4")
	w("proc/101/stat", stat("101", "pt_data_worker", "D", 100, 20*k, 950*k, 0))
	w("proc/101/io", "read_bytes: "+f(4e6*k)+"\n")
	link("/dev/nvidia0", "proc/101/fd/7")
	// pid 102: an idle torch.compile worker, also a child of the trainer: not a loader worker.
	w("proc/102/stat", stat("102", "python3", "S", 100, 0, 1, 3))
	// The job runs in its own cgroup with a memory.high limit; it hits the limit 30 times per 10 s.
	for _, pid := range []string{"100", "101", "102"} {
		w("proc/"+pid+"/cgroup", "0::/system.slice/job.scope\n")
	}
	cg := "sys/fs/cgroup/system.slice/job.scope/"
	w(cg+"memory.current", "9000000000\n")
	w(cg+"memory.high", "10000000000\n")
	w(cg+"memory.max", "max\n")
	w(cg+"memory.pressure", "some avg10=42.50 avg60=30.00 avg300=10.00 total=1\nfull avg10=5.00 avg60=1.00 avg300=0.00 total=1\n")
	w(cg+"memory.events", "low 0\nhigh "+f(30*k)+"\nmax 0\noom 0\noom_kill 0\n")
	w("proc/200/stat", stat("200", "nv-hostengine", "S", 1, 0, 50*k, 1))
	link("/dev/nvidia0", "proc/200/fd/3")
	w("proc/300/stat", stat("300", "sshd", "S", 1, 0, 1*k, 1))
}

func TestReadProcAndRates(t *testing.T) {
	root := t.TempDir()
	opt := ProcOptions{Root: root, HostName: "gpu-host", Procs: true, PageSize: 4096, ClkTck: 100}
	fakeRoot(t, root, 1)
	a, err := ReadProc(opt)
	if err != nil {
		t.Fatal(err)
	}
	fakeRoot(t, root, 2)
	b, err := ReadProc(opt)
	if err != nil {
		t.Fatal(err)
	}
	a.TS = time.Unix(1000, 0)
	b.TS = a.TS.Add(10 * time.Second)
	rows := Rates(a, b, nil)
	by := map[string]map[string]float64{}
	for _, r := range rows {
		by[r.Scope+"/"+r.ScopeID] = r.Values
		if r.Host != "gpu-host" {
			t.Fatalf("host = %q", r.Host)
		}
	}
	want := map[string]map[string]float64{
		"host/": {"cpu_count": 4, "cpu_busy_pct": 25, "cpu_iowait_pct": 12.5, "cpu_cores_saturated": 1,
			"mem_available_bytes": 800 * 1024, "swap_used_bytes": 60 * 1024, "swap_in_pages_per_s": 10, "major_faults_per_s": 5,
			"disk_read_bytes_per_s": 2000 * 512 / 10, "disk_write_bytes_per_s": 1000 * 512 / 10, "disk_util_max_pct": 50,
			"net_rx_bytes_per_s": 1e5, "net_tx_bytes_per_s": 5e4, "psi_cpu_some_pct": 10, "psi_io_some_pct": 50, "psi_io_full_pct": 20,
			"psi_mem_some_pct": 0},
		"numa/0": {"cpu_count": 2, "cpu_busy_pct": 50, "cpu_cores_saturated": 1, "mem_total_bytes": 1000 * 1024, "mem_free_bytes": 400 * 1024},
		"numa/1": {"cpu_count": 2, "cpu_iowait_pct": 25, "cpu_busy_pct": 0},
		"gpu/0": {"gpu_numa_node": 1, "proc_count": 3, "loader_workers": 1, "loader_workers_saturated": 1, "proc_d_state": 1,
			"proc_cpu_cores": 1.25, "proc_read_bytes_per_s": 5e5, "proc_major_faults_per_s": 2, "proc_rss_bytes": 3 * 10 * 4096,
			"proc_remote_numa_share": 0.76, "proc_cgroup_mem_psi_some_pct": 42.5, "proc_cgroup_mem_high_events_per_s": 3,
			"proc_cgroup_mem_used_bytes": 9e9, "proc_cgroup_mem_limit_bytes": 1e10},
	}
	for scope, fs := range want {
		got, ok := by[scope]
		if !ok {
			t.Fatalf("no %s row in %v", scope, by)
		}
		for k, v := range fs {
			if math.Abs(got[k]-v) > 1e-6 {
				t.Errorf("%s %s = %v, want %v", scope, k, got[k], v)
			}
		}
	}
	if len(by) != 4 {
		t.Errorf("want rows host, numa/0, numa/1, gpu/0; got %v", len(by))
	}
}

func TestRatesPseudonymiseHost(t *testing.T) {
	a := &Snapshot{TS: time.Unix(0, 0), Host: "node-a", HasMem: true, MemTotal: 1}
	b := &Snapshot{TS: time.Unix(10, 0), Host: "node-a", HasMem: true, MemTotal: 1}
	h := anon.New("s3cret")
	rows := Rates(a, b, h)
	if len(rows) != 1 || rows[0].Host != h.Hash("host", "node-a") {
		t.Fatalf("host not hashed like gpu_samples: %+v", rows)
	}
}

func TestNodeExporterRoundTrip(t *testing.T) {
	root := t.TempDir()
	opt := ProcOptions{Root: root, HostName: "gpu-host", ClkTck: 100}
	fakeRoot(t, root, 1)
	a, _ := ReadProc(opt)
	fakeRoot(t, root, 2)
	b, _ := ReadProc(opt)
	parse := func(s *Snapshot, ts time.Time) *Snapshot {
		ss, err := promtext.Parse(strings.NewReader(NodeExporterText(s)))
		if err != nil {
			t.Fatal(err)
		}
		return FromNodeExporter(ss, "", ts)
	}
	na, nb := parse(a, time.Unix(1000, 0)), parse(b, time.Unix(1010, 0))
	if nb.Host != "gpu-host" {
		t.Fatalf("nodename not read: %q", nb.Host)
	}
	rows := Rates(na, nb, nil)
	cov := Coverage(rows)
	for _, f := range fields.HostCatalog {
		if f.Tier == fields.TierH && !cov[f.Column] {
			t.Errorf("node_exporter path lost %s", f.Column)
		}
	}
	if rows[0].Values["cpu_busy_pct"] != 25 || rows[0].Values["disk_read_bytes_per_s"] != 102400 {
		t.Errorf("unexpected values %v", rows[0].Values)
	}
}

func TestDeviceFilters(t *testing.T) {
	for _, d := range []string{"sda1", "nvme0n1p2", "loop3", "dm-0", "md127", "vda1", "ram0"} {
		if !skipDisk.MatchString(d) {
			t.Errorf("%s should be skipped", d)
		}
	}
	for _, d := range []string{"sda", "nvme0n1", "vdb", "xvda"} {
		if skipDisk.MatchString(d) {
			t.Errorf("%s should count", d)
		}
	}
	for _, n := range []string{"lo", "veth12ab", "docker0", "br-1234", "cali9"} {
		if !skipNet.MatchString(n) {
			t.Errorf("%s should be skipped", n)
		}
	}
	if skipNet.MatchString("eth0") || skipNet.MatchString("ens5") || skipNet.MatchString("ib0") {
		t.Error("physical interface skipped")
	}
}

func TestCounterResetGivesZero(t *testing.T) {
	if rate(100, 50, 10) != 0 {
		t.Fatal("counter reset should not give a negative rate")
	}
}

// Without named DataLoader workers, only children doing work count: an idle
// helper process must not dilute the saturated share (real A100 run, Oct 2026).
func TestLoaderWorkersWithoutNamedWorkers(t *testing.T) {
	procs := func(busy, idle float64) *GPUProcs {
		return &GPUProcs{NUMANode: -1, Procs: map[int]Proc{
			1: {Comm: "python", PPID: 0, CPUSec: 100},
			2: {Comm: "python", PPID: 1, CPUSec: busy},
			3: {Comm: "python", PPID: 1, CPUSec: idle},
		}}
	}
	r := gpuRow(Row{Values: map[string]float64{}}, procs(0, 0), procs(10, 0.01), nil, 10)
	if r.Values["loader_workers"] != 1 || r.Values["loader_workers_saturated"] != 1 {
		t.Fatalf("workers = %v, saturated = %v; want 1, 1", r.Values["loader_workers"], r.Values["loader_workers_saturated"])
	}
}
