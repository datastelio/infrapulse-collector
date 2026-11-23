#!/usr/bin/env bash
# infrapulse-export.sh: export GPU telemetry from Prometheus (or anything with its
# HTTP API: VictoriaMetrics, Thanos, Mimir, Cortex, Grafana Cloud) for InfraPulse.
#
# It only reads. It asks the metrics store which DCGM metrics exist, then pulls
# each one hour by hour and writes one gzip JSON file per metric per hour.
#
#   PROM=http://prometheus:9090 START=2026-12-01T00:00:00Z HOURS=24 ./infrapulse-export.sh
#
# Settings (environment variables):
#   PROM       base URL of the metrics store, without /api/v1          (required)
#   START      start of the window, UTC, as 2026-12-01T00:00:00Z       (required)
#   HOURS      length of the window in hours; 24 minimum, 168 = a week (default 24)
#   OUT        output folder                                           (default ./infrapulse-export)
#   MODE       raw   = the samples as scraped, with their real timestamps (default)
#              range = query_range at a fixed STEP; Prometheus then repeats the last
#                      value for up to 5 minutes across missed scrapes, so use it
#                      only if your store refuses range selectors
#   STEP       step for MODE=range                                     (default 10s)
#   SELECTOR   extra label matchers, e.g. 'cluster="gpu-a"'            (default none)
#   CURL_ARGS  extra curl arguments for authentication, e.g.
#                CURL_ARGS='-H "X-Scope-OrgID: tenant1"'               (Mimir, Cortex)
#                CURL_ARGS='-H "Authorization: Bearer '"$TOKEN"'"'      (Grafana Cloud, proxies)
#                CURL_ARGS='-u user:password'                          (basic auth)
#
# Exit code: 0 if every request succeeded, 1 if any failed, 2 on a bad setting.
# A failed request writes no file. Metrics your store does not have are listed
# at the end; nothing is written for them.
set -u

PROM=${PROM:-}
START=${START:-}
HOURS=${HOURS:-24}
OUT=${OUT:-./infrapulse-export}
MODE=${MODE:-raw}
STEP=${STEP:-10s}
SELECTOR=${SELECTOR:-}
CURL_ARGS=${CURL_ARGS:-}

# Classic DCGM names (dcgm-exporter 4.8.x and earlier) and the DCGM 4.7 names
# (dcgm-exporter 4.9.0 and later). Whichever your store has is exported.
CLASSIC="
DCGM_FI_DEV_GPU_UTIL DCGM_FI_DEV_MEM_COPY_UTIL DCGM_FI_DEV_FB_USED DCGM_FI_DEV_FB_FREE
DCGM_FI_DEV_POWER_USAGE DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION DCGM_FI_DEV_GPU_TEMP
DCGM_FI_DEV_SM_CLOCK DCGM_FI_DEV_MEM_CLOCK DCGM_FI_DEV_MEMORY_TEMP DCGM_FI_DEV_PSTATE
DCGM_FI_DEV_CLOCKS_EVENT_REASONS DCGM_FI_DEV_CLOCK_THROTTLE_REASONS
DCGM_FI_DEV_POWER_VIOLATION DCGM_FI_DEV_THERMAL_VIOLATION DCGM_FI_DEV_SYNC_BOOST_VIOLATION
DCGM_FI_DEV_BOARD_LIMIT_VIOLATION DCGM_FI_DEV_RELIABILITY_VIOLATION DCGM_FI_DEV_XID_ERRORS
DCGM_FI_DEV_ECC_SBE_VOL_TOTAL DCGM_FI_DEV_ECC_DBE_VOL_TOTAL
DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS
DCGM_FI_DEV_ROW_REMAP_FAILURE DCGM_FI_DEV_PCIE_REPLAY_COUNTER
DCGM_FI_PROF_GR_ENGINE_ACTIVE DCGM_FI_PROF_SM_ACTIVE DCGM_FI_PROF_SM_OCCUPANCY
DCGM_FI_PROF_PIPE_TENSOR_ACTIVE DCGM_FI_PROF_DRAM_ACTIVE DCGM_FI_PROF_PIPE_FP64_ACTIVE
DCGM_FI_PROF_PIPE_FP32_ACTIVE DCGM_FI_PROF_PIPE_FP16_ACTIVE
DCGM_FI_PROF_PCIE_TX_BYTES DCGM_FI_PROF_PCIE_RX_BYTES
DCGM_FI_PROF_NVLINK_TX_BYTES DCGM_FI_PROF_NVLINK_RX_BYTES
DCGM_FI_DEV_ENFORCED_POWER_LIMIT DCGM_FI_DEV_MAX_SM_CLOCK DCGM_FI_DEV_MAX_MEM_CLOCK
"
NEW="
DCGM_FI_DEV_GPU_UTIL_RATIO DCGM_FI_DEV_MEMORY_UTIL_RATIO DCGM_FI_DEV_FB_USED_BYTES
DCGM_FI_DEV_FB_FREE_BYTES DCGM_FI_DEV_BOARD_POWER_WATTS DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL
DCGM_FI_DEV_GPU_TEMP_CELSIUS DCGM_FI_DEV_SM_CLOCK_HERTZ DCGM_FI_DEV_MEMORY_CLOCK_HERTZ
DCGM_FI_DEV_MEMORY_TEMP_CELSIUS DCGM_FI_DEV_GPU_PSTATE
DCGM_FI_DEV_GPU_POWER_THROTTLE_SECONDS_TOTAL DCGM_FI_DEV_GPU_THERMAL_THROTTLE_SECONDS_TOTAL
DCGM_FI_DEV_GPU_SYNC_BOOST_THROTTLE_SECONDS_TOTAL DCGM_FI_DEV_GPU_LIMIT_THROTTLE_SECONDS_TOTAL
DCGM_FI_DEV_GPU_RELIABILITY_THROTTLE_SECONDS_TOTAL DCGM_FI_DEV_XID_ERROR
DCGM_FI_DEV_ROW_REMAP_CORRECTABLE_TOTAL DCGM_FI_DEV_ROW_REMAP_UNCORRECTABLE_TOTAL
DCGM_FI_DEV_ROW_REMAP_FAILED DCGM_FI_DEV_PCIE_REPLAY_TOTAL
DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO DCGM_FI_PROF_SM_UTIL_RATIO DCGM_FI_PROF_SM_OCCUPANCY_RATIO
DCGM_FI_PROF_TENSOR_UTIL_RATIO DCGM_FI_PROF_DRAM_UTIL_RATIO DCGM_FI_PROF_FP64_UTIL_RATIO
DCGM_FI_PROF_FP32_UTIL_RATIO DCGM_FI_PROF_FP16_UTIL_RATIO
DCGM_FI_DEV_BOARD_POWER_LIMIT_ENFORCED_WATTS DCGM_FI_DEV_SM_MAX_CLOCK_HERTZ
DCGM_FI_DEV_MEMORY_MAX_CLOCK_HERTZ
DCGM_EXP_CLOCK_EVENTS_COUNT DCGM_EXP_XID_ERRORS_COUNT
"
METRICS="$CLASSIC $NEW"

die() { echo "infrapulse-export: $*" >&2; exit 2; }

[ -n "$PROM" ] || die "set PROM to your metrics store URL, e.g. PROM=http://prometheus:9090"
[ -n "$START" ] || die "set START to the start of the window in UTC, e.g. START=2026-12-01T00:00:00Z"
case "$HOURS" in ''|*[!0-9]*) die "HOURS must be a whole number, got '$HOURS'";; esac
[ "$HOURS" -ge 1 ] || die "HOURS must be at least 1"
case "$MODE" in raw|range) ;; *) die "MODE must be raw or range, got '$MODE'";; esac
for tool in curl gzip date; do command -v "$tool" >/dev/null 2>&1 || die "$tool is not installed"; done
PROM=${PROM%/}

# START to Unix seconds: GNU date (Linux) first, then BSD date (macOS).
if s=$(date -u -d "$START" +%s 2>/dev/null); then :
elif s=$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$START" +%s 2>/dev/null); then :
else die "cannot read START='$START'; write it as 2026-12-01T00:00:00Z"
fi
now=$(date -u +%s)
[ $((s + HOURS * 3600)) -le "$now" ] || die "the window ends in the future; choose a START at least $HOURS hours ago"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/infrapulse-export.XXXXXX") || die "cannot create a temporary folder"
trap 'rm -rf "$tmp"' EXIT

# request <output file> <api path> [curl data arguments...]
# Prints nothing. Returns 0 only for HTTP 200 with a Prometheus "success" body.
# On failure the reason is left in $tmp/err.
request() {
  local out=$1 path=$2 code; shift 2
  # CURL_ARGS is split by the shell on purpose, so it can carry quoted headers.
  code=$(eval "curl -sS -G -o \"\$out\" -w '%{http_code}' --connect-timeout 15 --max-time 600 $CURL_ARGS \"\$@\" \"\$PROM\$path\"" 2>"$tmp/curlerr") || {
    echo "cannot reach $PROM$path: $(head -c 300 "$tmp/curlerr")" >"$tmp/err"; return 1; }
  if [ "$code" != 200 ]; then
    echo "HTTP $code from $PROM$path: $(head -c 300 "$out" 2>/dev/null | tr '\n' ' ')" >"$tmp/err"; return 1
  fi
  if ! grep -q '"status"[[:space:]]*:[[:space:]]*"success"' "$out"; then
    echo "unexpected answer from $PROM$path: $(head -c 300 "$out" | tr '\n' ' ')" >"$tmp/err"; return 1
  fi
  return 0
}

# 1. Can we reach the store and log in? Which DCGM metrics does it have?
if ! request "$tmp/names.json" /api/v1/label/__name__/values; then
  echo "infrapulse-export: $(cat "$tmp/err")" >&2
  echo "infrapulse-export: nothing was exported. Check PROM, and CURL_ARGS if the store needs a login or a tenant header." >&2
  exit 1
fi
present= ; absent=
for m in $METRICS; do
  if grep -q "\"$m\"" "$tmp/names.json"; then present="$present $m"; else absent="$absent $m"; fi
done
if [ -z "$present" ]; then
  echo "infrapulse-export: the store answered, but it has none of the DCGM metrics. Is dcgm-exporter scraped by this store (and is this the right tenant)?" >&2
  exit 1
fi

mkdir -p "$OUT" || die "cannot create $OUT"
sel=
[ -n "$SELECTOR" ] && sel="{$SELECTOR}"
ok=0; empty=0; failed=0; failures=
echo "Exporting $(echo $present | wc -w | tr -d ' ') metrics, $HOURS hours from $START, mode $MODE, to $OUT"

h=0
while [ "$h" -lt "$HOURS" ]; do
  a=$((s + h * 3600)); b=$((a + 3600))
  for m in $present; do
    f=$(printf '%s/%s_h%03d.json.gz' "$OUT" "$m" "$h")
    if [ "$MODE" = raw ]; then
      # A range selector returns the samples as scraped, with their own timestamps.
      request "$tmp/body" /api/v1/query --data-urlencode "query=$m$sel[3600s]" --data-urlencode "time=$b"
    else
      # end is b-1 so the hour boundary is not returned twice.
      request "$tmp/body" /api/v1/query_range --data-urlencode "query=$m$sel" \
        --data-urlencode "start=$a" --data-urlencode "end=$((b - 1))" --data-urlencode "step=$STEP"
    fi
    if [ $? -ne 0 ]; then
      failed=$((failed + 1)); failures="$failures
  $m hour $h: $(cat "$tmp/err")"
      rm -f "$f"
      # Stop early if the very first request fails: every other one would too.
      if [ "$ok" -eq 0 ] && [ "$empty" -eq 0 ]; then
        echo "infrapulse-export: the first data request failed, stopping:$failures" >&2
        [ "$MODE" = raw ] && echo "infrapulse-export: if your store does not allow range selectors, retry with MODE=range" >&2
        exit 1
      fi
      continue
    fi
    if grep -q '"result"[[:space:]]*:[[:space:]]*\[[[:space:]]*\]' "$tmp/body"; then
      empty=$((empty + 1)); rm -f "$f"; continue
    fi
    gzip -c "$tmp/body" >"$f.part" && mv "$f.part" "$f" && ok=$((ok + 1)) || {
      failed=$((failed + 1)); failures="$failures
  $m hour $h: could not write $f"; rm -f "$f.part"; }
  done
  h=$((h + 1))
done

{
  echo "infrapulse-export manifest"
  echo "start: $START"
  echo "hours: $HOURS"
  echo "mode: $MODE"
  [ "$MODE" = range ] && echo "step: $STEP"
  echo "files written: $ok"
  echo "hours with no samples: $empty"
  echo "requests failed: $failed"
  echo "metrics exported:$present"
  echo "metrics not in the store:$absent"
} >"$OUT/MANIFEST.txt"

echo
echo "Files written: $ok   Hours with no samples: $empty   Requests failed: $failed"
# Report what is missing only for the naming scheme this store uses.
scheme=$CLASSIC; label="classic DCGM names (dcgm-exporter 4.8.x and earlier)"
case " $present " in *_RATIO\ *|*_HERTZ\ *|*_WATTS\ *|*_CELSIUS\ *) scheme=$NEW; label="DCGM 4.7 names (dcgm-exporter 4.9.0 and later)";; esac
echo "Your store uses the $label."
echo "Not collected (nothing written for these; see the guide for how to add them):"
none=1
for m in $scheme; do
  case " $absent " in *" $m "*) echo "  $m"; none=0;; esac
done
[ "$none" -eq 1 ] && echo "  none: every metric we ask for is there"
if [ "$failed" -gt 0 ]; then
  echo "infrapulse-export: $failed requests failed; those hours are missing from the export:$failures" >&2
  exit 1
fi
echo "Done. Send the $OUT folder."
