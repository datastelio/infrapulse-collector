// Package view renders collector output rows as a plain-text table.
package view

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/datastelio/infrapulse-collector/internal/sample"
)

// Latest keeps the last row seen for each GPU (vendor + gpu_index), sorted
// by vendor and index. Rows are assumed to be in file (time) order.
func Latest(rows []sample.Row) []sample.Row {
	last := map[string]sample.Row{}
	for _, r := range rows {
		last[r["vendor"]+"/"+r["gpu_index"]] = r
	}
	out := make([]sample.Row, 0, len(last))
	for _, r := range last {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i]["vendor"] != out[j]["vendor"] {
			return out[i]["vendor"] < out[j]["vendor"]
		}
		a, _ := strconv.Atoi(out[i]["gpu_index"])
		b, _ := strconv.Atoi(out[j]["gpu_index"])
		return a < b
	})
	return out
}

var headers = []string{"TIME (UTC)", "VENDOR", "GPU", "MODEL", "ID", "UTIL%", "MEM%", "MEM MiB", "TEMP C", "POWER W", "SM MHz", "THROTTLE"}

// Render writes rows as an aligned table.
func Render(w io.Writer, rows []sample.Row) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		cells := []string{
			strings.TrimSuffix(strings.Replace(r["timestamp"], "T", " ", 1), "Z"),
			r["vendor"],
			r["gpu_index"],
			r["gpu_model"],
			r["gpu_id"],
			r["util_gpu_pct"],
			r["util_mem_pct"],
			pair(r["mem_used_mib"], r["mem_total_mib"]),
			r["temp_c"],
			pair(r["power_w"], r["power_limit_w"]),
			pair(r["clock_sm_mhz"], r["clock_sm_max_mhz"]),
			r["throttle_reasons"],
		}
		for i, c := range cells {
			if c == "" {
				cells[i] = "-"
			}
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

// pair shows "value/limit", or just one side when the other is missing.
func pair(v, limit string) string {
	switch {
	case v == "" && limit == "":
		return ""
	case limit == "":
		return v
	case v == "":
		return "-/" + limit
	}
	return v + "/" + limit
}
