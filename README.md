# infrapulse-collector

Read-only collectors for GPU telemetry, written in Go with the standard library only.

| Tool | Reads | Collects | Use it when |
| --- | --- | --- | --- |
| **`ipcollect`** | NVIDIA dcgm-exporter (or the AMD Device Metrics Exporter), plus `/proc` and `/sys` | everything InfraPulse analyzes: activity, clocks and throttling, errors, profiling (tensor, DRAM, SM activity), limits, job labels, host and per-GPU-process metrics | you want the full picture; this is the collector InfraPulse asks for |
| `ipcollect-nvidia`, `ipcollect-amd`, `ipview` | the local `nvidia-smi` or `amd-smi` | utilization, memory, temperature, power, clocks, throttle state | you want a quick look with nothing but the GPU driver installed |

All of them change no GPU settings, need no daemon, and write only local files. `ipcollect` makes no network connections other than the scrapes you point it at; the lightweight tools make none at all.

`ipcollect` lists every command and flag itself:

```bash
ipcollect --help             # overview, every command with its flags, examples, tiers, output files
ipcollect help run           # one command
```

## ipcollect

### Requirements

| Component | Needed for | Notes |
| --- | --- | --- |
| Linux, amd64 or arm64 | everything | any distribution; the binary is statically linked |
| NVIDIA driver | GPU metrics | already installed on GPU hosts |
| dcgm-exporter | GPU metrics (Tiers A, B, C, S) | often already running; the Kubernetes GPU Operator deploys it by default |
| root (or sudo) | Tier P only | to see other users' GPU processes; everything else works without root |
| ~50 MB free disk per 8-GPU host per day | output files | about 32 MB of raw CSV per 8-GPU host per 24 h at 10 s; gzip shrinks it to about 4–10 MB |

### Install

From a release: download `ipcollect-linux-amd64` (or `-arm64`) with `SHA256SUMS`, then

```bash
sha256sum -c --ignore-missing SHA256SUMS        # every line must say OK
sudo install -D -m 0755 ipcollect-linux-amd64 /opt/infrapulse/ipcollect   # arm64 file on Arm hosts
/opt/infrapulse/ipcollect version
```

From source (Go 1.27 or later):

```bash
go install github.com/datastelio/infrapulse-collector/cmd/ipcollect@latest
# or
git clone https://github.com/datastelio/infrapulse-collector && cd infrapulse-collector
make dist      # static binaries in dist/linux-amd64/ and dist/linux-arm64/
```

To remove it, delete `/opt/infrapulse/ipcollect` and the output folder. Nothing else is installed.

### Run it

#### 1. Start dcgm-exporter with the InfraPulse field file

The field file is [`fields/infrapulse-dcgm-fields.csv`](fields/infrapulse-dcgm-fields.csv) (or `ipcollect fields --dcgm-exporter-csv`). Skip this step if dcgm-exporter is already running with it.

```bash
docker run -d --name dcgm-exporter --gpus all --cap-add SYS_ADMIN -p 9400:9400 \
  -v $PWD/infrapulse-dcgm-fields.csv:/etc/dcgm-exporter/infrapulse.csv \
  nvcr.io/nvidia/k8s/dcgm-exporter:<version> -f /etc/dcgm-exporter/infrapulse.csv -c 10000
```

- `-c 10000` samples every 10 seconds.
- `--cap-add SYS_ADMIN` turns on the profiling fields (Tier C). Without it, Tier C stays empty.
- On Kubernetes, load the same file through the GPU Operator's custom metrics ConfigMap instead.
- If your DCGM version rejects a field name, comment that line out of the file.

#### 2. Check coverage

```bash
sudo /opt/infrapulse/ipcollect check --source http://127.0.0.1:9400/metrics --require A
```

This lists every field by tier, marking each `ok` or `MISSING`. It writes nothing and takes a few seconds.

- Tier A must be complete.
- Fix `MISSING` lines in Tiers B and C before you start if you can, or send the output to InfraPulse.
- A `MISSING` field is one your GPU, driver, virtualisation or exporter configuration doesn't expose.

#### 3. Collect for 24 hours

```bash
export IPCOLLECT_SALT='<the same secret on every host>'
sudo systemd-run --unit=infrapulse-collect --setenv=IPCOLLECT_SALT="$IPCOLLECT_SALT" /opt/infrapulse/ipcollect run \
  --source http://127.0.0.1:9400/metrics --host-source proc \
  --interval 10s --duration 24h --out /var/lib/infrapulse/$(hostname)
```

It runs in the background, survives logout and stops by itself after 24 hours.

- **Progress:** `journalctl -u infrapulse-collect -f`
- **Stop early:** `sudo systemctl stop infrapulse-collect`. The files are flushed on stop.

Without systemd, run the same `ipcollect run ...` command under `nohup`, `tmux` or `screen`.

To roll it out across the fleet, run the same command through Ansible, pdsh, a Slurm job per node, or a Kubernetes DaemonSet.

#### 4. Compress and upload

```bash
gzip /var/lib/infrapulse/$(hostname)/*.csv
aws s3 cp --recursive /var/lib/infrapulse/$(hostname) \
  s3://<your-bucket>/<prefix>/$(date -u +%F)/$(hostname)/ --sse AES256
```

### Commands

| Command | What it does |
| --- | --- |
| `check` | scrapes once and reports which fields arrive, by tier; writes nothing |
| `run` | scrapes every `--interval` and writes `gpu_samples` and `host_samples` files to `--out` |
| `fields` | lists the fields InfraPulse collects, or prints the dcgm-exporter field file |
| `version` | prints the version |
| `help [command]` | prints all help, or one command's help |

Flags take one or two dashes (`-out` or `--out`). Durations are written `10s`, `5m`, `24h`.

#### `ipcollect run`

| Flag | Default | Meaning |
| --- | --- | --- |
| `--source URL` | `http://localhost:9400/metrics` | dcgm-exporter metrics URL. Repeat it, or comma-separate URLs, to scrape several hosts from one machine |
| `--interval D` | `10s` | time between scrapes; 10–30 s is recommended |
| `--duration D` | `0` (until stopped) | stop after this long, e.g. `24h`; `0` runs until Ctrl-C or SIGTERM |
| `--out DIR` | `./ipcollect-out` | output folder; created if missing |
| `--format F` | `csv` | `csv` or `jsonl` |
| `--salt S` | env `IPCOLLECT_SALT` | secret salt. When set, hostnames, GPU UUIDs, pods and job IDs are replaced by HMAC-SHA256 pseudonyms. The same salt gives the same IDs, so files from different hosts still join. Prefer the environment variable, which keeps the salt out of the process list |
| `--host-source S` | `proc` | host metrics: `proc` (read `/proc` and `/sys` on this machine), a node_exporter URL (`http://host:9100/metrics`), or `none` |
| `--host-name N` | this machine's hostname | value of the `host` column in the host metrics. It must match dcgm-exporter's `Hostname` label so GPU and host files join; `check` warns if it doesn't |
| `--host-procs` | `true` | with `--host-source proc`, also record the processes using each GPU (Tier P). Run as root to see other users' processes. `--host-procs=false` turns it off |
| `--job-label L` | `job_id,hpc_job,slurm_job_id` | label name holding a scheduler job ID; repeatable |
| `--timeout D` | `5s` | per-scrape timeout |

#### `ipcollect check`

| Flag | Default | Meaning |
| --- | --- | --- |
| `--source URL` | `http://localhost:9400/metrics` | dcgm-exporter metrics URL; repeatable or comma-separated |
| `--require T` | `A` | tiers that must be complete for exit code 0, e.g. `A`, `ABC`, `ABCS`, `ABCH` |
| `--host-source S` | `proc` | as for `run` |
| `--host-name N` | this machine's hostname | as for `run` |
| `--host-procs` | `true` | as for `run` |
| `--job-label L` | `job_id,hpc_job,slurm_job_id` | as for `run` |
| `--timeout D` | `5s` | scrape timeout |

#### `ipcollect fields`

| Flag | Default | Meaning |
| --- | --- | --- |
| (none) | | print the field catalog: tier, DCGM field ID and name, output column, unit; then the host columns |
| `--dcgm-exporter-csv` | off | print the dcgm-exporter field file (the same as `fields/infrapulse-dcgm-fields.csv`) |

#### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | collection failed, no GPUs found, or (for `check`) a required tier is incomplete |
| 2 | unknown command or bad flag |

### Other ways to run it

**GPU metrics only, from many hosts, on one machine.** No `ipcollect` on the GPU hosts, only dcgm-exporter. This gives Tiers A, B, C and S, but not host causes (H, P):

```bash
ipcollect run --source http://gpu01:9400/metrics,http://gpu02:9400/metrics,http://gpu03:9400/metrics \
  --host-source none --interval 10s --duration 24h --out ./fleet
```

**Host metrics from an existing node_exporter** instead of `/proc` (Tier H only, no per-process data):

```bash
ipcollect run --source http://127.0.0.1:9400/metrics --host-source http://127.0.0.1:9100/metrics \
  --duration 24h --out ./export
```

### Output

Each `run` writes three files to `--out`, named by the start time (UTC):

| File | Contents |
| --- | --- |
| `gpu_samples_<start>.csv` | one row per GPU per scrape: `ts, host, gpu_index, gpu_uuid, model, driver_version, namespace, pod, container, job_id`, then one column per field (`ipcollect fields` lists them). An empty cell means the field wasn't reported |
| `host_samples_<start>.csv` | one row per host, NUMA node and GPU per scrape: `ts, host, scope, scope_id`, then the host fields |
| `gpu_samples_<start>.meta.json` | collector version, interval, sources, scrape counts, field coverage |

With `--format jsonl`, the sample files are `.jsonl` instead of `.csv`. Nothing is written anywhere else.

### Tiers

| Tier | Contents | Source |
| --- | --- | --- |
| A | utilization, memory used/free, power, energy, GPU temperature | dcgm-exporter |
| B | SM and memory clocks, HBM temperature, P-state, throttle reasons, violation counters, XID, ECC, remapped rows, PCIe replays | dcgm-exporter |
| C | SM, tensor, DRAM and FP pipe activity, PCIe and NVLink bytes | dcgm-exporter with `SYS_ADMIN`, bare-metal or passthrough GPUs |
| S | enforced power limit, maximum SM and memory clocks | dcgm-exporter |
| D | job labels: Kubernetes `namespace`/`pod`/`container`, or a scheduler job label | dcgm-exporter pod mapping or HPC job mapping |
| H | CPU busy/user/system/iowait/steal, memory, swap, major faults, disk and network throughput, pressure stall (PSI), per NUMA node | `/proc` and `/sys`, or node_exporter |
| P | processes using each GPU: loader workers, CPU cores, D state, reads, faults, RSS, far-NUMA share, cgroup memory | `/proc`, as root |

### What it records and what it doesn't

**Recorded:** the metric values above, with the host name, GPU index and UUID, model, driver version, and pod or job labels. The meta file also records the `--source` and `--host-source` URLs. With `--salt`, all identifiers and the host part of each non-local URL are pseudonymized. Without `--salt`, they are recorded as they are.

**Not recorded:** command lines, environment variables, file names or paths, user names, model weights, training data or any workload content. For Tier P, it records only counts and rates for the processes holding `/dev/nvidiaN` open.

## Lightweight collectors: ipcollect-nvidia, ipcollect-amd, ipview

These read the vendor CLI directly, so they need no exporter, but they cannot see profiling counters, error counters or host metrics, and they write their own, smaller set of columns.

### Requirements

- Linux (the binaries are static; amd64 and arm64).
- NVIDIA: the NVIDIA driver, which ships `nvidia-smi`.
- AMD: ROCm with `amd-smi` (ROCm 6+), or `rocm-smi` as a fallback.
- Go 1.27 or later, only if you build from source.

### Install

```bash
go install github.com/datastelio/infrapulse-collector/cmd/ipcollect-nvidia@latest
go install github.com/datastelio/infrapulse-collector/cmd/ipcollect-amd@latest
go install github.com/datastelio/infrapulse-collector/cmd/ipview@latest
```

Or build from source:

```bash
git clone https://github.com/datastelio/infrapulse-collector
cd infrapulse-collector
make build   # bin/ipcollect-nvidia, bin/ipcollect-amd, bin/ipview for this machine
make dist    # static binaries in dist/linux-amd64/ and dist/linux-arm64/
make test
```

Copy the binary for your host's architecture from `dist/` to the GPU host;
it has no other dependencies.

### Quickstart (no GPU needed)

`--simulate` generates plausible fake samples for two GPUs, so you can try
the tools on a laptop. Simulated output is not a measurement.

```bash
ipcollect-nvidia --simulate --interval 2s --duration 30s
ipview
```

```
TIME (UTC)           VENDOR  GPU  MODEL                  ID            UTIL%  MEM%  MEM MiB      TEMP C  POWER W     SM MHz     THROTTLE
2026-11-17 02:59:58  nvidia  0    NVIDIA A100-SXM4-80GB  4226fecbe912  93     56    77465/81920  72      383.51/400  1410/1410  none
2026-11-17 02:59:58  nvidia  1    NVIDIA A100-SXM4-80GB  4905c6aff1fc  3      2     10099/81920  36      63.85/400   210/1410   gpu_idle
```

`ipcollect-amd --simulate` does the same with two fake AMD GPUs.

### Running on an NVIDIA host

```bash
ipcollect-nvidia check                      # is nvidia-smi there, which GPUs, which fields
ipcollect-nvidia --interval 10s --out gpu-samples.csv
```

`check` exits non-zero if `nvidia-smi` is missing or no GPU is found.
The collector runs until Ctrl-C (or `--duration` elapses), flushing the file
after every sample, so you can watch it from another terminal:

```bash
ipview --watch 5                            # latest sample per GPU, refreshed every 5 s
ipview --all gpu-samples.csv | less -S      # every row
```

On drivers older than R535, which do not know `clocks_event_reasons.active`,
the collector falls back to `clocks_throttle_reasons.active` automatically.

### Running on an AMD host

```bash
ipcollect-amd check
ipcollect-amd --interval 10s --format jsonl --out gpu-samples.jsonl
ipview gpu-samples.jsonl
```

```
TIME (UTC)           VENDOR  GPU  MODEL                ID            UTIL%  MEM%  MEM MiB      TEMP C  POWER W     SM MHz     THROTTLE
2026-11-17 03:00:02  amd     0    AMD Instinct MI250X  4e80eb7f52d5  93     56    62251/65536  71      493.59/500  1700/1700  none
2026-11-17 03:00:02  amd     1    AMD Instinct MI250X  3dc94c6668cd  0      0     6794/65536   35      90.12/500   210/1700   none
```

(Both tables above come from `--simulate`.)

`ipcollect-amd` uses `amd-smi metric --json` each sample, plus
`amd-smi list` and `amd-smi static` once at start for the GPU model, UUID and
power limit. Without `amd-smi` it runs `rocm-smi --showuse --showmemuse
--showpower --showtemp --showclocks --showmeminfo vram --json`, and
`rocm-smi --showproductname --showuniqueid --showmaxpower --json` once.

### Flags

| Flag | Default | |
|---|---|---|
| `--interval` | `10s` | time between samples |
| `--duration` | `0` | stop after this long; `0` runs until Ctrl-C or SIGTERM |
| `--out` | `./gpu-samples.csv` (`.jsonl` with `--format jsonl`) | output file; appended to if it exists |
| `--format` | `csv` | `csv` or `jsonl` |
| `--raw-uuid` | off | record the real GPU UUID in `gpu_id` instead of a salted hash |
| `--simulate` | off | fake samples for 2 GPUs; no vendor tool needed |

`ipview [--all] [--watch N] [file]` reads either format (detected from the
content) and defaults to `./gpu-samples.csv`.

### Output columns

Both collectors write the same columns, in this order. An empty CSV cell
(or `null` in JSON Lines) means the GPU or driver did not report the value.

| Column | Unit | Meaning |
|---|---|---|
| `timestamp` | UTC, RFC 3339 | when the sample was taken |
| `vendor` | | `nvidia` or `amd` |
| `gpu_index` | | GPU index as numbered by the vendor tool |
| `gpu_model` | | product name, e.g. `NVIDIA A100-SXM4-80GB` |
| `gpu_id` | | 12 hex characters of SHA-256(salt + GPU UUID); the raw UUID with `--raw-uuid` |
| `util_gpu_pct` | % | share of time the GPU was executing work (NVIDIA `utilization.gpu`, AMD `gfx_activity`) |
| `util_mem_pct` | % | memory controller activity (NVIDIA `utilization.memory`, AMD `umc_activity`) |
| `mem_used_mib` | MiB | device memory in use |
| `mem_total_mib` | MiB | total device memory |
| `temp_c` | °C | GPU temperature (AMD: edge sensor, else hotspot/junction) |
| `power_w` | W | power draw (AMD: socket power) |
| `power_limit_w` | W | enforced power limit or cap |
| `clock_sm_mhz` | MHz | current SM clock (AMD: graphics clock) |
| `clock_sm_max_mhz` | MHz | maximum SM / graphics clock |
| `throttle_reasons` | | NVIDIA: active clock-event reasons joined by `\|` (`sw_power_cap`, `hw_slowdown`, `sw_thermal_slowdown`, `hw_thermal_slowdown`, `gpu_idle`, …), `none` if there are none. AMD with amd-smi: `throttled` or `none`. Empty when not reported. |

### Privacy

Recorded: the columns above and nothing else.

Not recorded: hostname, IP or MAC addresses, user names, process IDs or
names, container or job names, PCI bus IDs, serial numbers, or the GPU UUID
(unless you pass `--raw-uuid`).

`gpu_id` is a salted hash so that the rows of one GPU can be grouped without
revealing which physical GPU it is. The salt is random for each run, printed
once to stderr, and never written to the file; the same GPU gets a different
`gpu_id` in each run. Keep the salt if you need to match a run's IDs to UUIDs
later.

Nothing leaves the machine. The tools only run the local vendor CLI and write
the output file; they open no network connections. Sharing the file is up to
you.

### Limitations

- Point-in-time sampling through the vendor CLIs: each value is whatever
  `nvidia-smi` or `amd-smi` reports at that instant, so short spikes or idle
  periods between samples are missed. Each sample costs a process start, so
  intervals below about one second are not useful.
- No profiling counters: tensor-core, DRAM or SM activity and PCIe/NVLink
  traffic need DCGM or ROCm profiling tools and are not collected.
  `util_gpu_pct` says the GPU was busy, not how efficiently it was used.
- AMD field coverage varies by ROCm version and GPU. Missing fields are left
  empty rather than failing; run `ipcollect-amd check` to see what your
  system reports. With `rocm-smi`, the max graphics clock and throttle state
  are not collected.
- Linux only.

## License and contact

Apache License 2.0; see [LICENSE](LICENSE). Copyright 2026 Data Stelio LLC.

Questions and bug reports: open a GitHub issue, or email
support@datastelio.com.
