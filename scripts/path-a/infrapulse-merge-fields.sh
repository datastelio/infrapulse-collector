#!/usr/bin/env bash
# infrapulse-merge-fields.sh: add the InfraPulse fields to the dcgm-exporter
# collectors file you already use, without removing or changing anything in it.
#
#   ./infrapulse-merge-fields.sh <your current file> <infrapulse fields file> > merged.csv
#
# Example:
#   ./infrapulse-merge-fields.sh default-counters.csv infrapulse-dcgm-fields.csv > merged.csv
#
# Every line of your file is kept, in order, comments included. The InfraPulse
# lines you do not already have are appended under a marker comment. What was
# added is listed on stderr. Nothing is ever removed.
set -eu
[ $# -eq 2 ] || { echo "usage: $0 <your current collectors file> <infrapulse fields file> > merged.csv" >&2; exit 2; }
mine=$1; ours=$2
[ -r "$mine" ] || { echo "cannot read $mine" >&2; exit 2; }
[ -r "$ours" ] || { echo "cannot read $ours" >&2; exit 2; }

names() { sed -e 's/\r$//' "$1" | grep -v '^[[:space:]]*#' | grep -v '^[[:space:]]*$' | cut -d, -f1 | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//'; }
new_scheme() { names "$1" | grep -Eq '_(HERTZ|RATIO|WATTS|CELSIUS|JOULES_TOTAL)$|^DCGM_FI_SYSTEM_DRIVER_VERSION$'; }
old_scheme() { names "$1" | grep -Eq '^DCGM_FI_DEV_(GPU_UTIL|SM_CLOCK|POWER_USAGE|FB_USED|GPU_TEMP)$|^DCGM_FI_DRIVER_VERSION$'; }

# The two naming schemes must not be mixed: dcgm-exporter 4.9.0 renamed the fields.
if new_scheme "$mine" && old_scheme "$ours"; then
  echo "Your file uses the DCGM 4.7 field names (dcgm-exporter 4.9.0 or later)." >&2
  echo "Use infrapulse-dcgm-fields-4.9.csv instead of $ours. Nothing was written." >&2
  exit 1
fi
if old_scheme "$mine" && new_scheme "$ours"; then
  echo "Your file uses the classic DCGM field names (dcgm-exporter 4.8.x or earlier)." >&2
  echo "Use infrapulse-dcgm-fields.csv instead of $ours. Nothing was written." >&2
  exit 1
fi

have=$(names "$mine")
has() { printf '%s\n' "$have" | grep -Fxq "$1"; }
added=0; kept=$(printf '%s\n' "$have" | grep -c . || true); list=

cat "$mine"
# Make sure the appended block starts on its own line.
[ -z "$(tail -c1 "$mine")" ] || echo
echo
echo "# Added for InfraPulse (remove this block to go back to your original list)"
while IFS= read -r line; do
  case "$line" in ''|'#'*) continue;; esac
  n=$(printf '%s' "$line" | cut -d, -f1 | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
  has "$n" && continue
  # The same field under its older or newer name counts as already there.
  case "$n" in
    DCGM_FI_DEV_CLOCKS_EVENT_REASONS) has DCGM_FI_DEV_CLOCK_THROTTLE_REASONS && continue;;
    DCGM_FI_DEV_CLOCK_THROTTLE_REASONS) has DCGM_FI_DEV_CLOCKS_EVENT_REASONS && continue;;
  esac
  printf '%s\n' "$line"
  added=$((added + 1)); list="$list
  + $n"
done < <(sed -e 's/\r$//' "$ours")

{
  echo "Kept all $kept fields from $mine; removed 0."
  if [ "$added" -eq 0 ]; then echo "Added 0: you already collect every InfraPulse field."
  else echo "Added $added InfraPulse fields:$list"; fi
  echo "Try the merged file on one node before rolling it out."
} >&2
