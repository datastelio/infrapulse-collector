package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Help text. `ipcollect --help` prints the overview and every command's flags;
// `ipcollect help <command>` or `ipcollect <command> -h` prints one command.

const overview = `ipcollect %s: InfraPulse GPU telemetry collector

Reads GPU metrics from NVIDIA dcgm-exporter (or the AMD Device Metrics Exporter)
and host metrics from /proc and /sys, and writes them to local CSV or JSONL files.
It is read-only: it changes no GPU, clock, power-limit or scheduler setting, and it
makes no network connections other than the scrapes you point it at.

Usage:
  ipcollect <command> [flags]
  ipcollect help <command>

Flags take one or two dashes (-out or --out). Durations: 10s, 5m, 24h.

Commands:
  check     scrape once and report which fields arrive, by tier; writes nothing
  run       scrape on an interval and write gpu_samples and host_samples files
  fields    list the fields InfraPulse collects, or print the dcgm-exporter field file
  convert   turn metrics exported from Prometheus into a gpu_samples file
  version   print the version
  help      print this help, or one command's help
`

const examples = `Examples:
  # 1. Is everything there? (run as root to see other users' GPU processes)
  sudo ipcollect check --source http://127.0.0.1:9400/metrics --require A

  # 2. Collect for 24 hours, pseudonymized, into a per-host folder
  sudo ipcollect run --source http://127.0.0.1:9400/metrics --interval 10s \
    --duration 24h --out /var/lib/infrapulse/$(hostname) --salt "$IPCOLLECT_SALT"

  # 3. GPU metrics only, from many hosts' dcgm-exporters, on one machine
  ipcollect run --source http://gpu01:9400/metrics,http://gpu02:9400/metrics \
    --host-source none --duration 24h --out ./fleet

  # 4. The field lines to add to your dcgm-exporter collectors file
  ipcollect fields --dcgm-exporter-csv > infrapulse-dcgm-fields.csv                      # 4.8.x and earlier
  ipcollect fields --dcgm-exporter-csv --exporter 4.9 > infrapulse-dcgm-fields-4.9.csv   # 4.9.0 and later

  # 5. Metrics you already keep in Prometheus: export a window, then convert it
  PROM=http://prometheus:9090 START=2026-12-01T00:00:00Z HOURS=24 ./infrapulse-export.sh
  ipcollect convert --in ./infrapulse-export --out ./converted
`

const reference = `Tiers (what each group of fields is for):
  A  activity: utilization, memory used/free, power, energy, temperature
  B  clocks and health: SM/memory clocks, throttle reasons, violation counters,
     XID, ECC, remapped rows, PCIe replays
  C  profiling: SM, tensor, DRAM and FP pipe activity, PCIe and NVLink bytes
     (dcgm-exporter needs --cap-add SYS_ADMIN; bare-metal or passthrough GPUs)
  S  static limits: enforced power limit, maximum clocks
  D  job labels: Kubernetes pod/namespace/container, or a scheduler job label
  H  host and NUMA node: CPU, memory, swap, PSI, disk, network (host_samples)
  P  processes using each GPU: loader workers, D state, reads, faults, NUMA
     placement, cgroup memory (host_samples; --host-source proc, as root)

Output (in --out):
  gpu_samples_<start>.csv        one row per GPU per scrape
  host_samples_<start>.csv       one row per host, NUMA node and GPU per scrape
  gpu_samples_<start>.meta.json  version, interval, sources, field coverage
  (.jsonl instead of .csv with --format jsonl)

Exit codes:
  0  success
  1  collection failed, no GPUs found, or required tiers incomplete (check)
  2  bad command or flag
`

type cmdHelpEntry struct {
	name, synopsis, about string
	flags                 func() *flag.FlagSet
}

var commands = []cmdHelpEntry{
	{"check", "ipcollect check [flags]",
		"Scrapes the sources once and lists every field by tier as ok or MISSING, plus job labels\n" +
			"and host metrics. Writes no files. Exits 1 if a tier named in --require is incomplete.",
		func() *flag.FlagSet { return newCheckFlags().fs }},
	{"run", "ipcollect run [flags]",
		"Scrapes the sources every --interval and appends rows to files in --out until --duration\n" +
			"has passed or it receives Ctrl-C / SIGTERM; the files are flushed on exit.",
		func() *flag.FlagSet { return newRunFlags().fs }},
	{"fields", "ipcollect fields [flags]",
		"Prints the InfraPulse field catalog (tier, DCGM field ID and name, column, unit) for\n" +
			"GPU and host metrics, or the dcgm-exporter field file.",
		func() *flag.FlagSet { return newFieldsFlags().fs }},
	{"convert", "ipcollect convert --in <export> [flags]",
		"Reads dcgm-exporter metrics exported from Prometheus or a compatible store and writes\n" +
			"one gpu_samples file, one row per GPU per scrape time. Classic and DCGM 4.7 metric\n" +
			"names (dcgm-exporter 4.9.0 and later) are both read and converted to the same units.\n" +
			"Export the samples as scraped (infrapulse-export.sh, or promtool tsdb dump): a\n" +
			"query_range export repeats the last value across missed scrapes.",
		func() *flag.FlagSet { return newConvertFlags().fs }},
}

func usage(w io.Writer) {
	fmt.Fprintf(w, overview, version)
	fmt.Fprintf(w, "\nRun \"ipcollect --help\" for every flag, or \"ipcollect help <command>\".\n")
}

// fullHelp is `ipcollect --help`: the overview, every command with its flags, examples and reference.
func fullHelp(w io.Writer) {
	fmt.Fprintf(w, overview, version)
	for _, c := range commands {
		fmt.Fprintln(w)
		commandHelp(w, c)
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, examples)
	fmt.Fprintln(w)
	fmt.Fprint(w, reference)
}

func commandHelp(w io.Writer, c cmdHelpEntry) {
	fmt.Fprintf(w, "%s\n%s\n\nFlags for %s:\n", c.synopsis, indent(c.about, "  "), c.name)
	fs := c.flags()
	fs.SetOutput(w)
	fs.PrintDefaults()
}

func cmdHelp(args []string) int {
	if len(args) == 0 {
		fullHelp(os.Stdout)
		return 0
	}
	for _, c := range commands {
		if c.name == args[0] {
			commandHelp(os.Stdout, c)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
	usage(os.Stderr)
	return 2
}

// parse parses a command's flags. -h / --help prints that command's help to
// stdout (exit 0); a bad flag prints the error and a pointer to help (exit 2).
func parse(fs *flag.FlagSet, args []string) (code int, ok bool) {
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {}
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		for _, c := range commands {
			if c.name == fs.Name() {
				commandHelp(os.Stdout, c)
			}
		}
		return 0, false
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Run \"ipcollect help %s\" for its flags.\n", fs.Name())
		return 2, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q\nRun \"ipcollect help %s\" for its flags.\n", fs.Arg(0), fs.Name())
		return 2, false
	}
	return 0, true
}

func indent(s, pre string) string {
	return pre + strings.ReplaceAll(s, "\n", "\n"+pre)
}
