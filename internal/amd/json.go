package amd

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The JSON printed by amd-smi and rocm-smi changes between ROCm releases:
// keys move, values are plain numbers in one release, strings such as
// "120 W" in another and {"value": 120, "unit": "W"} in a third. These
// helpers read whatever is there and report "not found" for the rest.

// get walks a dotted path ("usage.gfx_activity") through nested objects,
// matching keys case-insensitively.
func get(v any, path string) any {
	for _, key := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v, ok = m[key]
		if !ok {
			v = nil
			for k, val := range m {
				if strings.EqualFold(k, key) {
					v = val
					break
				}
			}
			if v == nil {
				return nil
			}
		}
	}
	return v
}

// first returns the first of the paths that has a usable number.
func first(v any, paths ...string) (float64, string, bool) {
	for _, p := range paths {
		if f, unit, ok := number(get(v, p)); ok {
			return f, unit, true
		}
	}
	return 0, "", false
}

var numUnitRE = regexp.MustCompile(`([-+]?\d*\.?\d+)\s*([A-Za-z%]*)`)

// number reads 12, "12", "120.5 W", "(1500Mhz)" or {"value": 12, "unit": "MB"}.
// "N/A" and other non-numeric values are not ok.
func number(v any) (float64, string, bool) {
	switch v := v.(type) {
	case float64:
		return v, "", true
	case string:
		s := strings.TrimSpace(v)
		if s == "" || strings.EqualFold(s, "N/A") {
			return 0, "", false
		}
		m := numUnitRE.FindStringSubmatch(s)
		if m == nil {
			return 0, "", false
		}
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, "", false
		}
		return f, m[2], true
	case map[string]any:
		f, unit, ok := number(get(v, "value"))
		if u, isStr := get(v, "unit").(string); isStr && u != "" {
			unit = u
		}
		return f, unit, ok
	}
	return 0, "", false
}

// mib converts a memory amount to MiB. amd-smi's "MB" is in fact MiB.
func mib(f float64, unit string) float64 {
	switch strings.ToUpper(unit) {
	case "B":
		return f / (1 << 20)
	case "KB", "KIB":
		return f / (1 << 10)
	case "GB", "GIB":
		return f * (1 << 10)
	}
	return f
}

// str returns the first path holding a non-empty, non-"N/A" string.
func str(v any, paths ...string) string {
	for _, p := range paths {
		if s, ok := get(v, p).(string); ok {
			if s = strings.TrimSpace(s); s != "" && !strings.EqualFold(s, "N/A") {
				return s
			}
		}
	}
	return ""
}

// entries finds the per-GPU objects: a top-level list, or a list under
// "gpu_data" (newer amd-smi) or any other top-level key.
func entries(v any) []map[string]any {
	switch v := v.(type) {
	case []any:
		var out []map[string]any
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		if l := get(v, "gpu_data"); l != nil {
			return entries(l)
		}
		for _, val := range v {
			if l, ok := val.([]any); ok {
				return entries(l)
			}
		}
	}
	return nil
}

// lookup returns the value of the first key (in m, case-insensitive) that
// contains one of the substrings, trying the substrings in order.
func lookup(m map[string]any, substrs ...string) any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, sub := range substrs {
		for _, k := range keys {
			if strings.Contains(strings.ToLower(k), sub) {
				return m[k]
			}
		}
	}
	return nil
}
