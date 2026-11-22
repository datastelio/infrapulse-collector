package host

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ProcOptions control ReadProc.
type ProcOptions struct {
	Root     string // filesystem root holding proc/ and sys/; "" = "/"
	HostName string
	Procs    bool    // scan processes for the GPU process groups (Tier P)
	PageSize float64 // bytes; 0 = os.Getpagesize()
	ClkTck   float64 // USER_HZ; 0 = 100
	// Exclude lists process names (comm) never counted as GPU users.
	Exclude map[string]bool
}

// DefaultExclude are processes that hold GPU device files without running work on them.
var DefaultExclude = map[string]bool{
	"nv-hostengine": true, "dcgm-exporter": true, "dcgmi": true, "nvidia-persistenced": true,
	"nvidia-smi": true, "Xorg": true, "Xwayland": true, "ipcollect": true, "nvidia-cuda-mps": true,
}

// Devices and interfaces that would double-count bytes or are not physical.
var (
	skipDisk = regexp.MustCompile(`^(z?ram|loop|fd|sr|dm-|md)\d*|^((h|s|v|xv)d[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+)$`)
	skipNet  = regexp.MustCompile(`^(lo|veth|docker|br-|virbr|cni|flannel|cali|tunl|kube-|vxlan|weave|cilium|tailscale|tun|tap)`)
	nvDevice = regexp.MustCompile(`^/dev/nvidia(\d+)$`)
)

// ReadProc reads one snapshot from /proc and /sys. Parts that cannot be read
// (no PSI on old kernels, no NUMA directory in some VMs) are left empty; only a
// missing /proc/stat is an error.
func ReadProc(opt ProcOptions) (*Snapshot, error) {
	root := opt.Root
	if root == "" {
		root = "/"
	}
	if opt.PageSize == 0 {
		opt.PageSize = float64(os.Getpagesize())
	}
	if opt.ClkTck == 0 {
		opt.ClkTck = 100
	}
	if opt.Exclude == nil {
		opt.Exclude = DefaultExclude
	}
	p := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	s := &Snapshot{TS: time.Now(), Host: opt.HostName}

	b, err := os.ReadFile(p("proc", "stat"))
	if err != nil {
		return nil, fmt.Errorf("read /proc/stat: %w", err)
	}
	s.CPUs = parseStat(b, opt.ClkTck)
	s.NUMACPUs, s.NUMAMem = readNUMA(p("sys", "devices", "system", "node"))

	if b, err := os.ReadFile(p("proc", "meminfo")); err == nil {
		m := parseKV(b, 1024)
		s.HasMem = true
		s.MemTotal, s.MemAvailable, s.MemFree = m["MemTotal"], m["MemAvailable"], m["MemFree"]
		s.SwapTotal, s.SwapFree = m["SwapTotal"], m["SwapFree"]
	}
	if b, err := os.ReadFile(p("proc", "vmstat")); err == nil {
		m := parseKV(b, 1)
		s.HasVM = true
		s.PswpIn, s.PswpOut, s.PgMajFault = m["pswpin"], m["pswpout"], m["pgmajfault"]
	}
	if b, err := os.ReadFile(p("proc", "diskstats")); err == nil {
		s.HasDisk = true
		s.DiskIOTime = map[string]float64{}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 14 {
				continue
			}
			dev := f[2]
			if strings.HasPrefix(dev, "loop") || strings.HasPrefix(dev, "ram") || strings.HasPrefix(dev, "zram") {
				continue
			}
			s.DiskIOTime[dev] = num(f[12]) / 1000
			if skipDisk.MatchString(dev) {
				continue
			}
			s.DiskRead += num(f[5]) * 512
			s.DiskWrite += num(f[9]) * 512
		}
	}
	if b, err := os.ReadFile(p("proc", "net", "dev")); err == nil {
		s.HasNet = true
		for _, line := range strings.Split(string(b), "\n") {
			name, rest, ok := strings.Cut(line, ":")
			name = strings.TrimSpace(name)
			if !ok || skipNet.MatchString(name) {
				continue
			}
			f := strings.Fields(rest)
			if len(f) < 9 {
				continue
			}
			s.NetRx += num(f[0])
			s.NetTx += num(f[8])
		}
	}
	s.PSI = readPSI(p("proc", "pressure"))
	if opt.Procs {
		s.GPUs = readGPUProcs(root, s.NUMACPUs, opt)
	}
	return s, nil
}

func num(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// parseStat reads per-CPU lines ("cpu0 user nice system idle iowait irq softirq steal ...").
func parseStat(b []byte, hz float64) map[int]CPUTimes {
	out := map[int]CPUTimes{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 9 || !strings.HasPrefix(f[0], "cpu") || f[0] == "cpu" {
			continue
		}
		id, err := strconv.Atoi(f[0][3:])
		if err != nil {
			continue
		}
		v := func(i int) float64 { return num(f[i]) / hz }
		out[id] = CPUTimes{User: v(1), Nice: v(2), System: v(3), Idle: v(4), IOWait: v(5), IRQ: v(6), SoftIRQ: v(7), Steal: v(8)}
	}
	return out
}

// parseKV reads "Name: value [kB]" or "name value" lines, scaled by mult.
func parseKV(b []byte, mult float64) map[string]float64 {
	out := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.Replace(line, ":", " ", 1))
		if len(f) >= 2 {
			out[f[0]] = num(f[1]) * mult
		}
	}
	return out
}

// parseCPUList expands "0-3,8,10-11".
func parseCPUList(s string) []int {
	var out []int
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		if part == "" {
			continue
		}
		lo, hi, rng := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		b := a
		if rng {
			if b, err = strconv.Atoi(hi); err != nil {
				continue
			}
		}
		for i := a; i <= b; i++ {
			out = append(out, i)
		}
	}
	return out
}

func readNUMA(dir string) (map[int][]int, map[int]NUMAMem) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	cpus, mem := map[int][]int{}, map[int]NUMAMem{}
	for _, e := range ents {
		n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "node"))
		if err != nil || !strings.HasPrefix(e.Name(), "node") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, e.Name(), "cpulist")); err == nil {
			if l := parseCPUList(string(b)); len(l) > 0 {
				cpus[n] = l
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, e.Name(), "meminfo")); err == nil {
			// "Node 0 MemTotal:       263842816 kB"
			var m NUMAMem
			for _, line := range strings.Split(string(b), "\n") {
				f := strings.Fields(line)
				if len(f) >= 4 {
					switch strings.TrimSuffix(f[2], ":") {
					case "MemTotal":
						m.Total = num(f[3]) * 1024
					case "MemFree":
						m.Free = num(f[3]) * 1024
					}
				}
			}
			mem[n] = m
		}
	}
	if len(cpus) == 0 {
		cpus = nil
	}
	return cpus, mem
}

// readPSI reads /proc/pressure/{cpu,memory,io}; total= is in microseconds.
func readPSI(dir string) *PSI {
	read := func(name string) (some, full float64, ok bool) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return 0, 0, false
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			for _, kv := range f[1:] {
				if v, ok := strings.CutPrefix(kv, "total="); ok {
					if f[0] == "some" {
						some = num(v) / 1e6
					} else if f[0] == "full" {
						full = num(v) / 1e6
					}
				}
			}
		}
		return some, full, true
	}
	cs, _, ok1 := read("cpu")
	ms, mf, ok2 := read("memory")
	is, iF, ok3 := read("io")
	if !ok1 && !ok2 && !ok3 {
		return nil
	}
	return &PSI{CPUSome: cs, MemSome: ms, MemFull: mf, IOSome: is, IOFull: iF}
}

// gpuNUMA maps each NVIDIA device minor to its NUMA node via the driver's
// /proc/driver/nvidia/gpus/<bus id>/information and sysfs.
func gpuNUMA(root string) map[int]int {
	out := map[int]int{}
	dir := filepath.Join(root, "proc", "driver", "nvidia", "gpus")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "information"))
		if err != nil {
			continue
		}
		minor := -1
		for _, line := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "Device Minor" {
				minor, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		if minor < 0 {
			continue
		}
		node := -1
		if nb, err := os.ReadFile(filepath.Join(root, "sys", "bus", "pci", "devices", strings.ToLower(e.Name()), "numa_node")); err == nil {
			node, _ = strconv.Atoi(strings.TrimSpace(string(nb)))
		}
		out[minor] = node
	}
	return out
}

type procInfo struct {
	pid  int
	comm string
	Proc
}

// parsePidStat parses /proc/<pid>/stat. Fields after the "(comm)" are counted
// from 3 (state), as in proc(5).
func parsePidStat(b []byte, hz, page float64) (procInfo, bool) {
	lp, rp := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if lp < 0 || rp < lp {
		return procInfo{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b[:lp])))
	if err != nil {
		return procInfo{}, false
	}
	f := strings.Fields(string(b[rp+1:]))
	if len(f) < 37 {
		return procInfo{}, false
	}
	at := func(n int) float64 { return num(f[n-3]) }
	pi := procInfo{pid: pid, comm: string(b[lp+1 : rp])}
	pi.Comm = pi.comm
	pi.State = f[0][0]
	pi.PPID = int(at(4))
	pi.MajFlt = at(12)
	pi.CPUSec = (at(14) + at(15)) / hz
	pi.RSS = at(24) * page
	pi.LastCPU = int(at(39))
	pi.ReadBytes = -1
	return pi, true
}

// readGPUProcs finds processes holding /dev/nvidiaN open, adds their
// descendants, and groups them by device minor.
func readGPUProcs(root string, numa map[int][]int, opt ProcOptions) map[string]*GPUProcs {
	procDir := filepath.Join(root, "proc")
	ents, err := os.ReadDir(procDir)
	if err != nil {
		return nil
	}
	all := map[int]procInfo{}
	holds := map[int][]int{} // pid -> device minors
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procDir, e.Name(), "stat"))
		if err != nil {
			continue
		}
		pi, ok := parsePidStat(b, opt.ClkTck, opt.PageSize)
		if !ok {
			continue
		}
		all[pid] = pi
		if opt.Exclude[pi.comm] {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(procDir, e.Name(), "fd"))
		if err != nil {
			continue // another user's process without root
		}
		seen := map[int]bool{}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(procDir, e.Name(), "fd", fd.Name()))
			if err != nil {
				continue
			}
			if m := nvDevice.FindStringSubmatch(target); m != nil {
				minor, _ := strconv.Atoi(m[1])
				if !seen[minor] {
					seen[minor] = true
					holds[pid] = append(holds[pid], minor)
				}
			}
		}
	}
	if len(holds) == 0 {
		return map[string]*GPUProcs{}
	}
	// Each process belongs to the GPUs held by itself or its nearest holding ancestor chain.
	groupsOf := func(pid int) []int {
		seen := map[int]bool{}
		var out []int
		for p := pid; p > 1 && !seen[p]; p = all[p].PPID {
			seen[p] = true
			if _, ok := all[p]; !ok {
				break
			}
			out = append(out, holds[p]...)
		}
		return out
	}
	numaOf := gpuNUMA(root)
	out := map[string]*GPUProcs{}
	for pid, pi := range all {
		if opt.Exclude[pi.comm] {
			continue
		}
		minors := groupsOf(pid)
		if len(minors) == 0 {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "io")); err == nil {
			if v, ok := parseKV(b, 1)["read_bytes"]; ok {
				pi.ReadBytes = v
			}
		}
		done := map[int]bool{}
		for _, m := range minors {
			if done[m] {
				continue
			}
			done[m] = true
			k := strconv.Itoa(m)
			gp, ok := out[k]
			if !ok {
				node, known := numaOf[m]
				if !known {
					node = -1
				}
				gp = &GPUProcs{NUMANode: node, Procs: map[int]Proc{}}
				out[k] = gp
			}
			gp.Procs[pid] = pi.Proc
		}
	}
	for _, gp := range out {
		gp.Cgroup = readGroupCgroup(root, gp)
	}
	return out
}

// readGroupCgroup returns the cgroup v2 memory reading of the cgroup holding most
// of the group's members (the job's container, pod or systemd scope).
func readGroupCgroup(root string, gp *GPUProcs) *CgroupMem {
	count := map[string]int{}
	for pid := range gp.Procs {
		b, err := os.ReadFile(filepath.Join(root, "proc", strconv.Itoa(pid), "cgroup"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "0::") {
				count[strings.TrimSpace(line[3:])]++
			}
		}
	}
	best, n := "", 0
	for p, c := range count {
		if c > n || (c == n && p > best) {
			best, n = p, c
		}
	}
	if best == "" {
		return nil
	}
	dir := filepath.Join(root, "sys", "fs", "cgroup", best)
	cg := &CgroupMem{Path: best, Limit: -1}
	b, err := os.ReadFile(filepath.Join(dir, "memory.current"))
	if err != nil {
		return nil // no memory controller here
	}
	cg.Current = num(strings.TrimSpace(string(b)))
	for _, f := range []string{"memory.high", "memory.max"} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			if s := strings.TrimSpace(string(b)); s != "max" {
				if v := num(s); cg.Limit < 0 || v < cg.Limit {
					cg.Limit = v
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "memory.pressure")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "some ") {
				for _, kv := range strings.Fields(line)[1:] {
					if strings.HasPrefix(kv, "avg10=") {
						cg.PSISome = num(kv[6:])
					}
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "memory.events")); err == nil {
		cg.HighEvents = parseKV(b, 1)["high"]
	}
	return cg
}

// HostName returns the name used for the host column: override if set, else os.Hostname.
func HostName(override string) string {
	if override != "" {
		return override
	}
	n, _ := os.Hostname()
	return n
}
