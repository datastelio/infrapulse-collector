package host

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/promtext"
)

// FromNodeExporter builds a snapshot from one node_exporter scrape. It fills
// Tier H only: node_exporter has no per-process data and no CPU-to-NUMA map.
// The host name is node_uname_info's nodename unless hostName is set.
func FromNodeExporter(samples []promtext.Sample, hostName string, ts time.Time) *Snapshot {
	s := &Snapshot{TS: ts, Host: hostName}
	cpus := map[int]CPUTimes{}
	io := map[string]float64{}
	var psi PSI
	hasPSI := false
	for _, x := range samples {
		v := x.Value
		switch x.Name {
		case "node_uname_info":
			if s.Host == "" {
				s.Host = x.Labels["nodename"]
			}
		case "node_cpu_seconds_total":
			id, err := strconv.Atoi(x.Labels["cpu"])
			if err != nil {
				continue
			}
			c := cpus[id]
			switch x.Labels["mode"] {
			case "user":
				c.User = v
			case "nice":
				c.Nice = v
			case "system":
				c.System = v
			case "idle":
				c.Idle = v
			case "iowait":
				c.IOWait = v
			case "irq":
				c.IRQ = v
			case "softirq":
				c.SoftIRQ = v
			case "steal":
				c.Steal = v
			}
			cpus[id] = c
		case "node_memory_MemTotal_bytes":
			s.HasMem, s.MemTotal = true, v
		case "node_memory_MemAvailable_bytes":
			s.MemAvailable = v
		case "node_memory_MemFree_bytes":
			s.MemFree = v
		case "node_memory_SwapTotal_bytes":
			s.SwapTotal = v
		case "node_memory_SwapFree_bytes":
			s.SwapFree = v
		case "node_memory_numa_MemTotal", "node_memory_numa_MemFree":
			n, err := strconv.Atoi(x.Labels["node"])
			if err != nil {
				continue
			}
			if s.NUMAMem == nil {
				s.NUMAMem = map[int]NUMAMem{}
			}
			m := s.NUMAMem[n]
			if x.Name == "node_memory_numa_MemTotal" {
				m.Total = v
			} else {
				m.Free = v
			}
			s.NUMAMem[n] = m
		case "node_vmstat_pswpin":
			s.HasVM, s.PswpIn = true, v
		case "node_vmstat_pswpout":
			s.HasVM, s.PswpOut = true, v
		case "node_vmstat_pgmajfault":
			s.HasVM, s.PgMajFault = true, v
		case "node_disk_read_bytes_total", "node_disk_written_bytes_total", "node_disk_io_time_seconds_total":
			dev := x.Labels["device"]
			if x.Name == "node_disk_io_time_seconds_total" {
				io[dev] = v
				continue
			}
			s.HasDisk = true
			if skipDisk.MatchString(dev) {
				continue
			}
			if x.Name == "node_disk_read_bytes_total" {
				s.DiskRead += v
			} else {
				s.DiskWrite += v
			}
		case "node_network_receive_bytes_total", "node_network_transmit_bytes_total":
			if skipNet.MatchString(x.Labels["device"]) {
				continue
			}
			s.HasNet = true
			if x.Name == "node_network_receive_bytes_total" {
				s.NetRx += v
			} else {
				s.NetTx += v
			}
		case "node_pressure_cpu_waiting_seconds_total":
			hasPSI, psi.CPUSome = true, v
		case "node_pressure_memory_waiting_seconds_total":
			hasPSI, psi.MemSome = true, v
		case "node_pressure_memory_stalled_seconds_total":
			hasPSI, psi.MemFull = true, v
		case "node_pressure_io_waiting_seconds_total":
			hasPSI, psi.IOSome = true, v
		case "node_pressure_io_stalled_seconds_total":
			hasPSI, psi.IOFull = true, v
		}
	}
	if len(cpus) > 0 {
		s.CPUs = cpus
	}
	if len(io) > 0 {
		s.HasDisk = true
		s.DiskIOTime = io
	}
	if hasPSI {
		s.PSI = &psi
	}
	return s
}

// NodeExporterText renders the Tier H part of a snapshot as node_exporter
// exposition text (used by tests and simulators to serve /node/metrics).
func NodeExporterText(s *Snapshot) string {
	var b strings.Builder
	w := func(name, labels string, v float64) {
		if labels != "" {
			labels = "{" + labels + "}"
		}
		fmt.Fprintf(&b, "%s%s %s\n", name, labels, strconv.FormatFloat(v, 'g', -1, 64))
	}
	w("node_uname_info", fmt.Sprintf(`nodename=%q,sysname="Linux"`, s.Host), 1)
	ids := make([]int, 0, len(s.CPUs))
	for id := range s.CPUs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		c := s.CPUs[id]
		for _, m := range []struct {
			mode string
			v    float64
		}{{"user", c.User}, {"nice", c.Nice}, {"system", c.System}, {"idle", c.Idle}, {"iowait", c.IOWait},
			{"irq", c.IRQ}, {"softirq", c.SoftIRQ}, {"steal", c.Steal}} {
			w("node_cpu_seconds_total", fmt.Sprintf(`cpu="%d",mode=%q`, id, m.mode), m.v)
		}
	}
	if s.HasMem {
		w("node_memory_MemTotal_bytes", "", s.MemTotal)
		w("node_memory_MemAvailable_bytes", "", s.MemAvailable)
		w("node_memory_MemFree_bytes", "", s.MemFree)
		w("node_memory_SwapTotal_bytes", "", s.SwapTotal)
		w("node_memory_SwapFree_bytes", "", s.SwapFree)
	}
	nodes := make([]int, 0, len(s.NUMAMem))
	for n := range s.NUMAMem {
		nodes = append(nodes, n)
	}
	sort.Ints(nodes)
	for _, n := range nodes {
		w("node_memory_numa_MemTotal", fmt.Sprintf(`node="%d"`, n), s.NUMAMem[n].Total)
		w("node_memory_numa_MemFree", fmt.Sprintf(`node="%d"`, n), s.NUMAMem[n].Free)
	}
	if s.HasVM {
		w("node_vmstat_pswpin", "", s.PswpIn)
		w("node_vmstat_pswpout", "", s.PswpOut)
		w("node_vmstat_pgmajfault", "", s.PgMajFault)
	}
	if s.HasDisk {
		// The simulator keeps one physical disk; byte totals go on it.
		w("node_disk_read_bytes_total", `device="nvme0n1"`, s.DiskRead)
		w("node_disk_written_bytes_total", `device="nvme0n1"`, s.DiskWrite)
		devs := make([]string, 0, len(s.DiskIOTime))
		for d := range s.DiskIOTime {
			devs = append(devs, d)
		}
		sort.Strings(devs)
		for _, d := range devs {
			w("node_disk_io_time_seconds_total", fmt.Sprintf(`device=%q`, d), s.DiskIOTime[d])
		}
	}
	if s.HasNet {
		w("node_network_receive_bytes_total", `device="eth0"`, s.NetRx)
		w("node_network_transmit_bytes_total", `device="eth0"`, s.NetTx)
	}
	if s.PSI != nil {
		w("node_pressure_cpu_waiting_seconds_total", "", s.PSI.CPUSome)
		w("node_pressure_memory_waiting_seconds_total", "", s.PSI.MemSome)
		w("node_pressure_memory_stalled_seconds_total", "", s.PSI.MemFull)
		w("node_pressure_io_waiting_seconds_total", "", s.PSI.IOSome)
		w("node_pressure_io_stalled_seconds_total", "", s.PSI.IOFull)
	}
	return b.String()
}
