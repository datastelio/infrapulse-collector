// Command ipcollect scrapes dcgm-exporter and writes InfraPulse gpu_samples files.
//
//	ipcollect check  --source http://localhost:9400/metrics
//	ipcollect run    --source http://localhost:9400/metrics --interval 10s --duration 24h --out ./export
//
// Host metrics (CPU, memory, disk, network, PSI, the processes using each GPU)
// are read from /proc on Linux by default (--host-source proc), or scraped from
// node_exporter (--host-source http://host:9100/metrics), into host_samples files.
//
//	ipcollect fields [--dcgm-exporter-csv]
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/collect"
	"github.com/datastelio/infrapulse-collector/internal/fields"
)

var version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "run":
		code = cmdRun(os.Args[2:])
	case "check":
		code = cmdCheck(os.Args[2:])
	case "fields":
		code = cmdFields(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("ipcollect", version)
	case "help", "-h", "-help", "--help":
		code = cmdHelp(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		code = 2
	}
	os.Exit(code)
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

type hostFlags struct {
	source, name *string
	procs        *bool
}

func addHostFlags(fs *flag.FlagSet) hostFlags {
	def := "none"
	if runtime.GOOS == "linux" {
		def = "proc"
	}
	return hostFlags{
		source: fs.String("host-source", def, "host metrics: proc (read /proc and /sys here), a node_exporter URL, or none"),
		name:   fs.String("host-name", "", "host column for host metrics; must match dcgm-exporter's Hostname label (default: this machine's hostname, or node_exporter's nodename)"),
		procs:  fs.Bool("host-procs", true, "with --host-source proc: also record the processes using each GPU (Tier P; run as root to see other users' processes)"),
	}
}

func (h hostFlags) open(timeout time.Duration) (collect.HostSource, error) {
	return collect.NewHostSource(*h.source, *h.name, *h.procs, timeout)
}

type runOpts struct {
	fs                          *flag.FlagSet
	sources, jobLabels          listFlag
	interval, duration, timeout *time.Duration
	out, format, salt           *string
	host                        hostFlags
}

func newRunFlags() *runOpts {
	o := &runOpts{fs: flag.NewFlagSet("run", flag.ContinueOnError)}
	fs := o.fs
	fs.Var(&o.sources, "source", "dcgm-exporter or AMD Device Metrics Exporter metrics URL; repeat it (or comma-separate) to scrape several hosts from one machine (default http://localhost:9400/metrics)")
	fs.Var(&o.jobLabels, "job-label", "label name holding a scheduler job ID; repeatable (default: job_id,hpc_job,slurm_job_id)")
	o.interval = fs.Duration("interval", 10*time.Second, "time between scrapes; 10s to 30s is recommended")
	o.duration = fs.Duration("duration", 0, "stop after this long, e.g. 24h (0 = until Ctrl-C or SIGTERM)")
	o.out = fs.String("out", "./ipcollect-out", "output directory; created if missing")
	o.format = fs.String("format", "csv", "output format: csv or jsonl")
	o.salt = fs.String("salt", os.Getenv("IPCOLLECT_SALT"), "secret salt; when set, hostnames, GPU UUIDs, pods and job IDs are pseudonymized with HMAC-SHA256, the same salt giving the same IDs (default: env IPCOLLECT_SALT)")
	o.timeout = fs.Duration("timeout", 5*time.Second, "per-scrape timeout")
	o.host = addHostFlags(fs)
	return o
}

func cmdRun(args []string) int {
	o := newRunFlags()
	if code, ok := parse(o.fs, args); !ok {
		return code
	}
	sources, jobLabels, hf := o.sources, o.jobLabels, o.host
	interval, duration, out, format, salt, timeout := o.interval, o.duration, o.out, o.format, o.salt, o.timeout
	if len(sources) == 0 {
		sources = listFlag{"http://localhost:9400/metrics"}
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	hsrc, err := hf.open(*timeout)
	if err != nil {
		log.Error("bad --host-source", "err", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	meta, err := collect.Run(ctx, collect.Config{
		Sources: sources, Interval: *interval, Duration: *duration, OutDir: *out, Format: *format,
		Salt: *salt, JobLabels: jobLabels, Timeout: *timeout, Version: version,
		Host: hsrc, HostDesc: *hf.source,
	}, log)
	if err != nil {
		log.Error("collection failed", "err", err)
		return 1
	}
	eventOnly := map[string]bool{}
	for _, f := range fields.Catalog {
		eventOnly[f.Column] = f.EventOnly
	}
	missing := 0
	for col, ok := range meta.Coverage {
		if meta.Vendor == "amd" && len(fields.AMDNamesFor(col)) == 0 {
			continue // NVIDIA-only column
		}
		if !ok && !eventOnly[col] {
			missing++
		}
	}
	if missing > 0 {
		log.Warn("some fields never arrived; run `ipcollect check` for details", "missing", missing)
	}
	return 0
}

type checkOpts struct {
	fs                 *flag.FlagSet
	sources, jobLabels listFlag
	require            *string
	timeout            *time.Duration
	host               hostFlags
}

func newCheckFlags() *checkOpts {
	o := &checkOpts{fs: flag.NewFlagSet("check", flag.ContinueOnError)}
	fs := o.fs
	fs.Var(&o.sources, "source", "dcgm-exporter or AMD Device Metrics Exporter metrics URL; repeatable or comma-separated (default http://localhost:9400/metrics)")
	fs.Var(&o.jobLabels, "job-label", "label name holding a scheduler job ID; repeatable (default: job_id,hpc_job,slurm_job_id)")
	o.require = fs.String("require", "A", "tiers that must be complete for exit code 0, e.g. A, ABC, ABCS or ABCH (H host, P GPU processes)")
	o.timeout = fs.Duration("timeout", 5*time.Second, "scrape timeout")
	o.host = addHostFlags(fs)
	return o
}

func cmdCheck(args []string) int {
	o := newCheckFlags()
	if code, ok := parse(o.fs, args); !ok {
		return code
	}
	sources, jobLabels, require, timeout, hf := o.sources, o.jobLabels, o.require, o.timeout, o.host
	hsrc, err := hf.open(*timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	if len(sources) == 0 {
		sources = listFlag{"http://localhost:9400/metrics"}
	}
	res := collect.Check(context.Background(), sources, jobLabels, *timeout)
	for _, e := range res.Errors {
		fmt.Fprintln(os.Stderr, "error:", e)
	}
	if res.GPUs == 0 {
		fmt.Println("No GPUs found. Is dcgm-exporter (NVIDIA, port 9400) or the AMD Device Metrics Exporter (port 5000) running and reachable?")
		return 1
	}
	amd := res.Vendor == "amd"
	if amd {
		fmt.Printf("GPUs reporting: %d (AMD Device Metrics Exporter)\n\n", res.GPUs)
	} else {
		fmt.Printf("GPUs reporting: %d\n\n", res.GPUs)
	}

	tierNames := map[fields.Tier]string{fields.TierA: "activity", fields.TierB: "clocks & health", fields.TierC: "profiling", fields.TierS: "static"}
	byTier := fields.ByTier()
	failed := false
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, t := range []fields.Tier{fields.TierA, fields.TierB, fields.TierC, fields.TierS} {
		got, total := 0, 0
		for _, f := range byTier[t] {
			if amd && len(fields.AMDNamesFor(f.Column)) == 0 {
				continue
			}
			total++
			if res.Coverage[f.Column] || f.EventOnly {
				got++
			}
		}
		fmt.Fprintf(tw, "Tier %s (%s)\t%d/%d fields\n", t, tierNames[t], got, total)
		for _, f := range byTier[t] {
			if amd {
				// AMD: the exporter metrics that feed this column; NVIDIA-only columns have none.
				names := fields.AMDNamesFor(f.Column)
				status := "ok"
				switch {
				case len(names) == 0:
					status = "not on AMD"
					names = []string{"-"}
				case !res.Coverage[f.Column]:
					status = "MISSING"
					if f.Column == "clock_event_reasons" {
						status = "MISSING (violation metrics are served on bare metal only)"
					} else if t == fields.TierC {
						status = "MISSING (turn on the exporter's profiler metrics)"
					}
					if strings.Contains(*require, string(t)) {
						failed = true
					}
				}
				fmt.Fprintf(tw, "  %s\t%s\t%s\n", f.Column, strings.Join(names, " / "), status)
				continue
			}
			status := "ok"
			if !res.Coverage[f.Column] && f.EventOnly {
				status = "none yet (reported only after one occurs)"
			} else if !res.Coverage[f.Column] {
				status = "MISSING"
				if strings.Contains(*require, string(t)) {
					failed = true
				}
			}
			fmt.Fprintf(tw, "  %d\t%s\t%s\n", f.ID, f.Name, status)
		}
	}
	tw.Flush()
	fmt.Println()
	fmt.Printf("Kubernetes pod labels: %v   Scheduler job labels: %v\n", res.HasPodLabel, res.HasJobLabel)
	if !res.HasPodLabel && !res.HasJobLabel && amd {
		fmt.Println("  Tier D (job labels) missing: enable the exporter's Kubernetes or Slurm job labels, or send a job table alongside the export.")
	} else if !res.HasPodLabel && !res.HasJobLabel {
		fmt.Println("  Tier D (job labels) missing: enable dcgm-exporter pod mapping, or send a job table alongside the export.")
	}
	if len(res.Unknown) > 0 {
		fmt.Printf("Metrics outside the InfraPulse catalog (ignored): %d\n", len(res.Unknown))
	}
	if hsrc == nil {
		fmt.Printf("\nHost metrics: off (--host-source none).")
		if strings.ContainsAny(*require, "HP") {
			failed = true
		}
		fmt.Println()
	} else if !checkHost(hsrc, *hf.source, res.Hosts, *require) {
		failed = true
	}
	if failed {
		fmt.Printf("\nRequired tiers (%s) incomplete.\n", *require)
		return 1
	}
	return 0
}

// checkHost prints Tier H and P coverage and reports whether the required ones are complete.
func checkHost(src collect.HostSource, desc string, gpuHosts []string, require string) bool {
	fmt.Printf("\nHost metrics from %s\n", desc)
	res := collect.CheckHost(context.Background(), src, time.Second)
	if res.Err != nil {
		fmt.Println("  error:", res.Err)
		return !strings.ContainsAny(require, "HP")
	}
	ok := true
	byTier := fields.HostByTier()
	names := map[fields.Tier]string{fields.TierH: "host & NUMA", fields.TierP: "GPU processes"}
	hasGPURows := false
	for _, r := range res.Rows {
		if r.Scope == fields.ScopeGPU {
			hasGPURows = true
		}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, t := range []fields.Tier{fields.TierH, fields.TierP} {
		got := 0
		for _, f := range byTier[t] {
			if res.Coverage[f.Column] {
				got++
			}
		}
		fmt.Fprintf(tw, "Tier %s (%s)\t%d/%d fields\n", t, names[t], got, len(byTier[t]))
		if t == fields.TierP && !hasGPURows {
			if desc != "proc" {
				fmt.Fprintf(tw, "  not available from node_exporter (needs --host-source proc on the GPU host)\n")
			} else {
				fmt.Fprintf(tw, "  no process is using a GPU right now (start a GPU job; run as root to see other users' processes)\n")
			}
			if strings.Contains(require, "P") {
				ok = false
			}
			continue
		}
		for _, f := range byTier[t] {
			status := "ok"
			if !res.Coverage[f.Column] && fields.OptionalHostColumns[f.Column] {
				status = "not reported (optional: needs cgroup v2; limit only when the job has one)"
			} else if !res.Coverage[f.Column] {
				status = "MISSING"
				if strings.Contains(require, string(t)) {
					ok = false
				}
			}
			fmt.Fprintf(tw, "  %s\t%s\n", f.Column, status)
		}
	}
	tw.Flush()
	if !res.Coverage["psi_cpu_some_pct"] {
		fmt.Println("  PSI missing: kernel < 4.20 or booted with psi=0; stall shares are then inferred from iowait only.")
	}
	if !res.Coverage["cpu_cores_saturated"] {
		fmt.Println("  No per-CPU data: per-NUMA CPU and saturated-core counts need --host-source proc.")
	}
	if len(gpuHosts) > 0 && res.HostName != "" {
		match := false
		for _, h := range gpuHosts {
			if h == res.HostName {
				match = true
			}
		}
		if !match {
			fmt.Printf("  WARNING: host name %q does not match dcgm-exporter's Hostname %v; pass --host-name so the files join.\n", res.HostName, gpuHosts)
		}
	}
	return ok
}

type fieldsOpts struct {
	fs  *flag.FlagSet
	csv *bool
}

func newFieldsFlags() *fieldsOpts {
	o := &fieldsOpts{fs: flag.NewFlagSet("fields", flag.ContinueOnError)}
	o.csv = o.fs.Bool("dcgm-exporter-csv", false, "print the dcgm-exporter field file (load it with dcgm-exporter -f) instead of the table")
	return o
}

func cmdFields(args []string) int {
	o := newFieldsFlags()
	if code, ok := parse(o.fs, args); !ok {
		return code
	}
	csv := o.csv
	if *csv {
		fmt.Print(fields.DCGMExporterCSV())
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIER\tID\tDCGM FIELD\tCOLUMN\tUNIT")
	for _, f := range fields.Catalog {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", f.Tier, f.ID, f.Name, f.Column, f.Unit)
	}
	fmt.Fprintln(tw, "\nTIER\tSCOPES\tHOST COLUMN\tUNIT")
	for _, f := range fields.HostCatalog {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Tier, strings.Join(f.Scopes, ","), f.Column, f.Unit)
	}
	tw.Flush()
	return 0
}
