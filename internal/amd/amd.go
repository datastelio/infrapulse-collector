// Package amd samples AMD GPUs through amd-smi (ROCm 6+), falling back to
// rocm-smi when amd-smi is not installed.
package amd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/collector"
	"github.com/datastelio/infrapulse-collector/internal/sample"
)

// Static is per-GPU information that does not change while collecting.
type Static struct {
	Model      string
	UUID       string
	PowerLimit float64 // W, 0 if unknown
}

type runFunc func(ctx context.Context, bin string, args ...string) ([]byte, error)

// Source queries amd-smi or rocm-smi.
type Source struct {
	tool   string // "amd-smi" or "rocm-smi"
	bin    string
	static map[int]Static
	run    runFunc
}

var rocmSampleArgs = []string{"--showuse", "--showmemuse", "--showpower", "--showtemp", "--showclocks", "--showmeminfo", "vram", "--json"}

// Open finds amd-smi (or rocm-smi) and reads the static GPU information once.
// A failure to read static information is not fatal: those fields stay empty.
func Open(ctx context.Context) (collector.Source, error) {
	s := &Source{run: execRun}
	for _, tool := range []string{"amd-smi", "rocm-smi"} {
		if bin, err := exec.LookPath(tool); err == nil {
			s.tool, s.bin = tool, bin
			break
		}
	}
	if s.bin == "" {
		return nil, errors.New("neither amd-smi nor rocm-smi found in PATH (install ROCm 6 or later)")
	}
	if s.tool == "amd-smi" {
		list, _ := s.run(ctx, s.bin, "list", "--json")
		static, _ := s.run(ctx, s.bin, "static", "--asic", "--limit", "--json")
		s.static = ParseAMDSMIStatic(list, static)
	} else {
		out, _ := s.run(ctx, s.bin, "--showproductname", "--showuniqueid", "--showmaxpower", "--json")
		s.static = ParseROCmSMIStatic(out)
	}
	return s, nil
}

func execRun(ctx context.Context, bin string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// rocm-smi exits non-zero when some requested fields are unsupported
		// but still prints valid JSON; keep the output in that case.
		if json.Valid(bytes.TrimSpace(stdout.Bytes())) && stdout.Len() > 0 {
			return stdout.Bytes(), nil
		}
		return nil, fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(stderr.String()+" "+stdout.String()))
	}
	return stdout.Bytes(), nil
}

// Describe implements collector.Source.
func (s *Source) Describe() string { return fmt.Sprintf("%s (%s)", s.tool, s.bin) }

// Query implements collector.Source.
func (s *Source) Query(ctx context.Context) ([]sample.Sample, error) {
	now := time.Now()
	if s.tool == "amd-smi" {
		out, err := s.run(ctx, s.bin, "metric", "--json")
		if err != nil {
			return nil, err
		}
		return ParseAMDSMI(out, s.static, now)
	}
	out, err := s.run(ctx, s.bin, rocmSampleArgs...)
	if err != nil {
		return nil, err
	}
	return ParseROCmSMI(out, s.static, now)
}

func decode(out []byte, what string) (any, error) {
	var v any
	if err := json.Unmarshal(jsonPart(out), &v); err != nil {
		return nil, fmt.Errorf("parsing %s JSON: %w", what, err)
	}
	return v, nil
}

// jsonPart drops any warning text printed before the JSON document.
func jsonPart(out []byte) []byte {
	if i := bytes.IndexAny(out, "[{"); i > 0 {
		return out[i:]
	}
	return out
}

func numStr(f float64, _ string, ok bool) string {
	if !ok {
		return ""
	}
	return sample.FormatNum(f)
}

func mibStr(f float64, unit string, ok bool) string {
	if !ok {
		return ""
	}
	return sample.FormatNum(mib(f, unit))
}

// ParseAMDSMI reads `amd-smi metric --json` output.
func ParseAMDSMI(out []byte, static map[int]Static, now time.Time) ([]sample.Sample, error) {
	v, err := decode(out, "amd-smi metric")
	if err != nil {
		return nil, err
	}
	var samples []sample.Sample
	for i, g := range entries(v) {
		idx := i
		if f, _, ok := number(get(g, "gpu")); ok {
			idx = int(f)
		}
		st := static[idx]
		s := sample.Sample{
			Time:       now.UTC(),
			Vendor:     "amd",
			Index:      idx,
			Model:      st.Model,
			ID:         st.UUID,
			UtilGPU:    numStr(first(g, "usage.gfx_activity", "usage.gfx_usage", "gfx_activity")),
			UtilMem:    numStr(first(g, "usage.umc_activity", "usage.mem_activity", "umc_activity")),
			MemUsed:    mibStr(first(g, "mem_usage.used_vram", "mem_usage.vram_used", "vram.used")),
			MemTotal:   mibStr(first(g, "mem_usage.total_vram", "mem_usage.vram_total", "vram.total")),
			Temp:       numStr(first(g, "temperature.edge", "temperature.hotspot", "temperature.junction")),
			Power:      numStr(first(g, "power.socket_power", "power.average_socket_power", "power.current_socket_power")),
			PowerLimit: numStr(first(g, "power.power_cap", "power.power_limit")),
			ClockSM:    numStr(first(g, "clock.gfx_0.clk", "clock.gfx.clk", "clock.gfx_0.cur_clk", "clock.sclk")),
			ClockSMMax: numStr(first(g, "clock.gfx_0.max_clk", "clock.gfx.max_clk", "clock.gfx_0.max")),
		}
		if s.PowerLimit == "" && st.PowerLimit > 0 {
			s.PowerLimit = sample.FormatNum(st.PowerLimit)
		}
		switch strings.ToUpper(str(g, "power.throttle_status", "throttle_status")) {
		case "THROTTLED":
			s.Throttle = "throttled"
		case "UNTHROTTLED":
			s.Throttle = "none"
		}
		samples = append(samples, s)
	}
	return samples, nil
}

// ParseAMDSMIStatic combines `amd-smi list --json` (UUIDs) and
// `amd-smi static --asic --limit --json` (model, power limit). Either may be
// nil or unparseable.
func ParseAMDSMIStatic(list, static []byte) map[int]Static {
	m := map[int]Static{}
	at := func(i int, g map[string]any) (int, Static) {
		if f, _, ok := number(get(g, "gpu")); ok {
			i = int(f)
		}
		return i, m[i]
	}
	if v, err := decode(list, "amd-smi list"); err == nil {
		for i, g := range entries(v) {
			idx, st := at(i, g)
			st.UUID = str(g, "uuid")
			m[idx] = st
		}
	}
	if v, err := decode(static, "amd-smi static"); err == nil {
		for i, g := range entries(v) {
			idx, st := at(i, g)
			st.Model = str(g, "asic.market_name", "asic.product_name", "board.product_name")
			if f, _, ok := first(g, "limit.power_cap", "limit.max_power", "limit.socket_power", "limit.ppt0.max_power_limit"); ok {
				st.PowerLimit = f
			}
			m[idx] = st
		}
	}
	return m
}

// cards returns the rocm-smi "cardN" objects keyed by N, skipping "system".
func cards(out []byte, what string) (map[int]map[string]any, error) {
	v, err := decode(out, what)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected a JSON object", what)
	}
	res := map[int]map[string]any{}
	for k, val := range top {
		n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(k), "card"))
		if m, ok := val.(map[string]any); ok && err == nil && strings.HasPrefix(strings.ToLower(k), "card") {
			res[n] = m
		}
	}
	return res, nil
}

// ParseROCmSMI reads `rocm-smi --showuse --showmemuse --showpower --showtemp
// --showclocks --showmeminfo vram --json` output.
func ParseROCmSMI(out []byte, static map[int]Static, now time.Time) ([]sample.Sample, error) {
	cs, err := cards(out, "rocm-smi")
	if err != nil {
		return nil, err
	}
	idxs := make([]int, 0, len(cs))
	for i := range cs {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	var samples []sample.Sample
	for _, idx := range idxs {
		c := cs[idx]
		st := static[idx]
		s := sample.Sample{
			Time:     now.UTC(),
			Vendor:   "amd",
			Index:    idx,
			Model:    st.Model,
			ID:       st.UUID,
			UtilGPU:  numStr(rnum(c, "gpu use (%)")),
			UtilMem:  numStr(rnum(c, "memory read/write activity", "gpu memory use (%)", "memory activity")),
			MemUsed:  bytesStr(rnum(c, "vram total used memory")),
			MemTotal: bytesStr(rnum(c, "vram total memory")),
			Temp:     numStr(rnum(c, "(sensor edge)", "(sensor junction)")),
			Power:    numStr(rnum(c, "average graphics package power", "current socket graphics package power", "socket graphics package power")),
			ClockSM:  numStr(rnum(c, "sclk clock speed")),
		}
		if st.PowerLimit > 0 {
			s.PowerLimit = sample.FormatNum(st.PowerLimit)
		} else {
			s.PowerLimit = numStr(rnum(c, "max graphics package power"))
		}
		samples = append(samples, s)
	}
	return samples, nil
}

// rnum returns the first number among the rocm-smi keys matching subs,
// trying the substrings in order and skipping "N/A" values.
func rnum(c map[string]any, subs ...string) (float64, string, bool) {
	for _, sub := range subs {
		if f, unit, ok := number(lookup(c, sub)); ok {
			return f, unit, true
		}
	}
	return 0, "", false
}

// bytesStr converts a rocm-smi memory value, which is in bytes, to MiB.
func bytesStr(f float64, _ string, ok bool) string { return mibStr(f, "B", ok) }

// ParseROCmSMIStatic reads `rocm-smi --showproductname --showuniqueid
// --showmaxpower --json`. Unparseable input gives an empty map.
func ParseROCmSMIStatic(out []byte) map[int]Static {
	m := map[int]Static{}
	cs, err := cards(out, "rocm-smi static")
	if err != nil {
		return m
	}
	for idx, c := range cs {
		st := Static{}
		for _, key := range []string{"card series", "card model", "card sku"} {
			if s, ok := lookup(c, key).(string); ok && s != "" && !strings.HasPrefix(s, "0x") {
				st.Model = s
				break
			}
		}
		if s, ok := lookup(c, "unique id").(string); ok {
			st.UUID = s
		}
		if f, _, ok := rnum(c, "max graphics package power"); ok {
			st.PowerLimit = f
		}
		m[idx] = st
	}
	return m
}
