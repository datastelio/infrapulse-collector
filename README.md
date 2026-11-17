# infrapulse-collector

Read-only command-line collectors for NVIDIA and AMD GPU metrics, plus a
table viewer. `ipcollect-nvidia` and `ipcollect-amd` sample utilization,
memory, temperature, power, clocks and throttle state from the vendor's own
CLI at a fixed interval and append the readings to a local CSV or JSON Lines
file; `ipview` prints that file as a terminal table. They change no GPU
settings, need no daemon, and send nothing over the network: your data stays
on your machine.

## Requirements

- Linux (the binaries are static; amd64 and arm64).
- NVIDIA: the NVIDIA driver, which ships `nvidia-smi`.
- AMD: ROCm with `amd-smi` (ROCm 6+), or `rocm-smi` as a fallback.
- Go 1.27 or later, only if you build from source.

## Install

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

## Quickstart (no GPU needed)

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

## Running on an NVIDIA host

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

## Running on an AMD host

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

## Flags

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

## Output columns

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

## Privacy

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

## Limitations

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
