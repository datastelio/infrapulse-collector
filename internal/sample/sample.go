// Package sample defines the output columns shared by every collector and
// reads and writes them as CSV or JSON Lines.
package sample

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Columns is the output schema, in order. Both collectors write exactly these.
var Columns = []string{
	"timestamp", "vendor", "gpu_index", "gpu_model", "gpu_id",
	"util_gpu_pct", "util_mem_pct", "mem_used_mib", "mem_total_mib",
	"temp_c", "power_w", "power_limit_w", "clock_sm_mhz", "clock_sm_max_mhz",
	"throttle_reasons",
}

// numeric marks the columns written as JSON numbers (or null when empty).
var numeric = map[string]bool{
	"gpu_index": true, "util_gpu_pct": true, "util_mem_pct": true,
	"mem_used_mib": true, "mem_total_mib": true, "temp_c": true,
	"power_w": true, "power_limit_w": true, "clock_sm_mhz": true,
	"clock_sm_max_mhz": true,
}

// Sample is one reading of one GPU. Metric values are normalized number
// text (see Num); an empty string means the value was not available.
type Sample struct {
	Time       time.Time
	Vendor     string
	Index      int
	Model      string
	ID         string // raw UUID from the source; replaced by a hash before writing unless --raw-uuid
	UtilGPU    string
	UtilMem    string
	MemUsed    string
	MemTotal   string
	Temp       string
	Power      string
	PowerLimit string
	ClockSM    string
	ClockSMMax string
	Throttle   string
}

// Record returns the sample's values in Columns order.
func (s Sample) Record() []string {
	return []string{
		s.Time.UTC().Format(time.RFC3339), s.Vendor, strconv.Itoa(s.Index), s.Model, s.ID,
		s.UtilGPU, s.UtilMem, s.MemUsed, s.MemTotal,
		s.Temp, s.Power, s.PowerLimit, s.ClockSM, s.ClockSMMax,
		s.Throttle,
	}
}

var numRE = regexp.MustCompile(`[-+]?\d*\.?\d+`)

// Num extracts the number from vendor text such as "45", "250.12 W",
// "(1500Mhz)" or "[N/A]" and returns it normalized, or "" if there is none.
func Num(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "[") || strings.EqualFold(v, "N/A") {
		return ""
	}
	m := numRE.FindString(v)
	if m == "" {
		return ""
	}
	f, err := strconv.ParseFloat(m, 64)
	if err != nil {
		return ""
	}
	return FormatNum(f)
}

// FormatNum formats f with at most two decimals and no trailing zeros.
func FormatNum(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	return strconv.FormatFloat(math.Round(f*100)/100, 'f', -1, 64)
}
